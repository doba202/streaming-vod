package upload

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"streamfilm/internal/catalog"
)

type StaleVideos interface {
	ListStaleUploading(ctx context.Context, before time.Time) ([]catalog.Video, error)
	DeleteUploading(ctx context.Context, id string) error
}

type StaleObjects interface {
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
}

// Cleaner xoá video uploading quá MaxAge (spec: 24h) cùng multipart upload dở và object raw.
type Cleaner struct {
	Videos    StaleVideos
	Objects   StaleObjects
	BucketRaw string
	MaxAge    time.Duration
	Now       func() time.Time
	Log       *slog.Logger
}

// RunOnce xoá dòng DB trước (có điều kiện status = uploading, nên không đụng video vừa được complete),
// rồi mới dọn storage. Lỗi storage chỉ log: object mồ côi không ảnh hưởng tính đúng.
func (c *Cleaner) RunOnce(ctx context.Context) (int, error) {
	stale, err := c.Videos.ListStaleUploading(ctx, c.Now().Add(-c.MaxAge))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range stale {
		if err := c.Videos.DeleteUploading(ctx, v.ID); err != nil {
			if errors.Is(err, catalog.ErrNotFound) {
				continue
			}
			return n, err
		}
		if v.UploadID != "" {
			if err := c.Objects.AbortMultipartUpload(ctx, c.BucketRaw, v.RawKey, v.UploadID); err != nil {
				c.Log.Warn("abort multipart failed", "video_id", v.ID, "err", err)
			}
		}
		if _, err := c.Objects.DeletePrefix(ctx, c.BucketRaw, v.ID+"/"); err != nil {
			c.Log.Warn("delete raw objects failed", "video_id", v.ID, "err", err)
		}
		c.Log.Info("stale upload removed", "video_id", v.ID, "created_at", v.CreatedAt)
		n++
	}
	return n, nil
}

// Run chạy RunOnce ngay khi khởi động rồi lặp lại mỗi `every` cho tới khi ctx bị huỷ.
func (c *Cleaner) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := c.RunOnce(ctx); err != nil {
			c.Log.Error("stale upload cleanup failed", "err", err)
		} else if n > 0 {
			c.Log.Info("stale upload cleanup done", "removed", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
