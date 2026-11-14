// Package upload cấp presigned URL upload thẳng lên storage, hoàn tất upload và đưa video vào hàng đợi transcode.
package upload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
	"streamfilm/internal/storage"
)

const (
	MaxSizeBytes   int64 = 10 << 30 // 10 GB (spec §8)
	PartSize       int64 = 64 << 20 // 64 MiB → tối đa 160 part cho 10 GB (S3 cho phép 10 000)
	URLTTL               = time.Hour
	publishTimeout       = 5 * time.Second
)

var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrKafkaUnavailable = errors.New("event bus unavailable")
)

var allowedExt = map[string]string{
	".mp4": "video/mp4", ".mov": "video/quicktime", ".mkv": "video/x-matroska", ".webm": "video/webm",
}

type Videos interface {
	Create(ctx context.Context, v catalog.Video) error
	Get(ctx context.Context, id string) (catalog.Video, error)
	Transition(ctx context.Context, id string, to catalog.Status) (catalog.Status, error)
	RevertQueued(ctx context.Context, id string, to catalog.Status) error
	Delete(ctx context.Context, id string) (catalog.Video, error)
}

type Objects interface {
	CreateMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error)
	PresignUploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int32, ttl time.Duration) (string, error)
	CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int, wantSize, partSize, maxSize int64) error
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
	Exists(ctx context.Context, bucket, key string) (bool, error)
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
}

type Publisher interface {
	Publish(ctx context.Context, topic string, ev events.Event) error
}

type Service struct {
	Videos    Videos
	Objects   Objects
	Pub       Publisher
	BucketRaw string
	BucketHLS string // M7: Delete dọn hls/{id}/
	Now       func() time.Time
	NewID     func() string
	Log       *slog.Logger // tuỳ chọn; nil = slog.Default()
}

type CreateInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Genres      []string `json:"genres"`
	Year        int      `json:"year"`
	Filename    string   `json:"filename"`
	SizeBytes   int64    `json:"size_bytes"`
}

type Part struct {
	PartNumber int32  `json:"part_number"`
	URL        string `json:"url"`
}

type UploadPlan struct {
	UploadID  string    `json:"upload_id"`
	PartSize  int64     `json:"part_size"`
	Parts     []Part    `json:"parts"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CreateOutput struct {
	ID     string         `json:"id"`
	Status catalog.Status `json:"status"`
	Upload UploadPlan     `json:"upload"`
}

func PartCount(size int64) int { return int((size + PartSize - 1) / PartSize) }

// RawKey là key trong bucket raw: {id}/source.{ext}.
func RawKey(id, ext string) string { return id + "/source" + ext }

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// Create tạo video (uploading), mở multipart upload và trả presigned URL cho từng part.
func (s *Service) Create(ctx context.Context, in CreateInput) (CreateOutput, error) {
	meta, err := catalog.NormalizeMetadata(catalog.Metadata{Title: in.Title, Description: in.Description,
		Genres: in.Genres, Year: in.Year})
	if err != nil {
		return CreateOutput{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if in.SizeBytes <= 0 || in.SizeBytes > MaxSizeBytes {
		return CreateOutput{}, invalid("size_bytes must be between 1 and %d", MaxSizeBytes)
	}
	ext := strings.ToLower(path.Ext(in.Filename))
	contentType, ok := allowedExt[ext]
	if !ok {
		return CreateOutput{}, invalid("filename must end with .mp4, .mov, .mkv or .webm")
	}

	id := s.NewID()
	key := RawKey(id, ext)
	uploadID, err := s.Objects.CreateMultipartUpload(ctx, s.BucketRaw, key, contentType)
	if err != nil {
		return CreateOutput{}, err
	}
	n := PartCount(in.SizeBytes)
	parts := make([]Part, n)
	for i := range n {
		num := int32(i + 1)
		url, err := s.Objects.PresignUploadPart(ctx, s.BucketRaw, key, uploadID, num, URLTTL)
		if err != nil {
			return CreateOutput{}, err
		}
		parts[i] = Part{PartNumber: num, URL: url}
	}
	err = s.Videos.Create(ctx, catalog.Video{ID: id, Title: meta.Title, Description: meta.Description,
		Genres: meta.Genres, Year: meta.Year, RawKey: key, UploadID: uploadID, SizeBytes: in.SizeBytes})
	if err != nil {
		// Không để multipart upload mồ côi; lỗi huỷ chỉ log (cleaner/lifecycle sẽ dọn sau).
		if aerr := s.Objects.AbortMultipartUpload(context.WithoutCancel(ctx), s.BucketRaw, key, uploadID); aerr != nil {
			s.logger().Warn("abort multipart after create failure", "video_id", id, "err", aerr)
		}
		return CreateOutput{}, err
	}
	return CreateOutput{ID: id, Status: catalog.StatusUploading, Upload: UploadPlan{
		UploadID: uploadID, PartSize: PartSize, Parts: parts, ExpiresAt: s.Now().Add(URLTTL),
	}}, nil
}

// Complete ghép multipart (nếu chưa), đặt queued rồi publish video.uploaded.
// Publish lỗi thì trả video về uploading và báo ErrKafkaUnavailable (HTTP 503) để admin gọi lại.
func (s *Service) Complete(ctx context.Context, id string) (catalog.Video, error) {
	v, err := s.Videos.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if v.Status != catalog.StatusUploading {
		return v, fmt.Errorf("%w: video is %s", catalog.ErrInvalidTransition, v.Status)
	}
	exists, err := s.Objects.Exists(ctx, s.BucketRaw, v.RawKey)
	if err != nil {
		return v, err
	}
	if !exists { // lần gọi trước có thể đã ghép xong nhưng publish lỗi
		err := s.Objects.CompleteMultipartUpload(ctx, s.BucketRaw, v.RawKey, v.UploadID, PartCount(v.SizeBytes), v.SizeBytes, PartSize, MaxSizeBytes)
		if errors.Is(err, storage.ErrIncompleteUpload) {
			return v, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if err != nil {
			return v, err
		}
	}
	return s.enqueue(ctx, v, catalog.StatusUploading)
}

// Retry đưa video failed về queued và publish lại video.uploaded.
func (s *Service) Retry(ctx context.Context, id string) (catalog.Video, error) {
	v, err := s.Videos.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if v.Status != catalog.StatusFailed {
		return v, fmt.Errorf("%w: only failed videos can be retried, video is %s", catalog.ErrInvalidTransition, v.Status)
	}
	return s.enqueue(ctx, v, catalog.StatusFailed)
}

// enqueue: đặt queued TRƯỚC rồi mới publish, để api không bao giờ nhận video.processing
// khi DB còn ở trạng thái cũ. Đây là dual-write chấp nhận ở MVP (ADR 0007).
func (s *Service) enqueue(ctx context.Context, v catalog.Video, prev catalog.Status) (catalog.Video, error) {
	if _, err := s.Videos.Transition(ctx, v.ID, catalog.StatusQueued); err != nil {
		return v, err
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	ev := events.VideoUploaded{VideoID: v.ID, RawKey: v.RawKey}
	if err := s.Pub.Publish(pctx, events.TopicVideoUploaded, ev); err != nil {
		if rerr := s.Videos.RevertQueued(context.WithoutCancel(ctx), v.ID, prev); rerr != nil {
			return v, errors.Join(fmt.Errorf("%w: %v", ErrKafkaUnavailable, err), rerr)
		}
		return v, fmt.Errorf("%w: %v", ErrKafkaUnavailable, err)
	}
	v.Status = catalog.StatusQueued
	return v, nil
}

// Delete xoá video khỏi DB (cascade rendition, phụ đề, tiến độ xem) rồi dọn storage: huỷ multipart upload dở,
// xoá raw/{id}/ và hls/{id}/. Video queued/processing → catalog.ErrVideoBusy (HTTP 409): transcoder có thể
// đang ghi hls/{id}/, xoá lúc đó sẽ để lại HLS mồ côi. Lỗi storage chỉ log (DB là nguồn sự thật, ADR 0020).
func (s *Service) Delete(ctx context.Context, id string) error {
	v, err := s.Videos.Delete(ctx, id)
	if err != nil {
		return err
	}
	bg := context.WithoutCancel(ctx) // client ngắt kết nối cũng dọn nốt
	log := s.logger().With("video_id", id)
	if v.Status == catalog.StatusUploading && v.UploadID != "" {
		if err := s.Objects.AbortMultipartUpload(bg, s.BucketRaw, v.RawKey, v.UploadID); err != nil {
			log.WarnContext(ctx, "abort multipart after delete failed", "err", err)
		}
	}
	removed := 0
	for _, bucket := range []string{s.BucketRaw, s.BucketHLS} {
		n, err := s.Objects.DeletePrefix(bg, bucket, id+"/")
		removed += n
		if err != nil {
			log.WarnContext(ctx, "delete objects after delete failed", "bucket", bucket, "err", err)
		}
	}
	log.InfoContext(ctx, "video deleted", "status", v.Status, "objects_removed", removed)
	return nil
}
