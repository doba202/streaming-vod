// Package worker là logic job của transcoder: nhận video.uploaded, transcode, publish kết quả.
// Transcoder không ghi Postgres; mọi thay đổi trạng thái đi qua sự kiện Kafka.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
	"streamfilm/internal/transcode"
)

type Transcoder interface {
	Transcode(ctx context.Context, input, outDir string) (transcode.Result, error)
}

type Storage interface {
	Download(ctx context.Context, bucket, key, dest string) error
	UploadDir(ctx context.Context, bucket, prefix, dir string) (int, error)
}

type Publisher interface {
	Publish(ctx context.Context, topic string, ev events.Event) error
}

// DefaultBackoffs: thời gian chờ trước lần thử lại 1, 2, 3 (spec §7).
var DefaultBackoffs = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

type Worker struct {
	Transcoder           Transcoder
	Store                Storage
	Pub                  Publisher
	BucketRaw, BucketHLS string
	WorkDir              string
	Backoffs             []time.Duration
	Sleep                func(ctx context.Context, d time.Duration) error
	Log                  *slog.Logger
}

func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Handle xử lý một record video.uploaded. Trả nil khi job đã có kết quả cuối (transcoded, hoặc
// failed + DLQ) để consumer commit offset. Trả lỗi khi không publish được hoặc ctx bị huỷ:
// offset không được commit và record sẽ được xử lý lại.
func (w *Worker) Handle(ctx context.Context, rec *kgo.Record) error {
	ev, err := events.Decode[events.VideoUploaded](rec.Value)
	if err == nil && ev.RawKey == "" {
		err = errors.New("decode event: missing raw_key")
	}
	if err != nil {
		w.Log.Error("bad video.uploaded message, sending to DLQ", "partition", rec.Partition, "offset", rec.Offset, "err", err)
		return w.deadLetter(ctx, ev.VideoID, rec.Value, err, 0)
	}
	log := w.Log.With("video_id", ev.VideoID)

	if err := w.Pub.Publish(ctx, events.TopicVideoProcessing, events.VideoProcessing{VideoID: ev.VideoID}); err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		start := time.Now()
		out, err := w.process(ctx, ev)
		if err == nil {
			if err := w.Pub.Publish(ctx, events.TopicVideoTranscoded, out); err != nil {
				return err
			}
			log.Info("video transcoded", "attempt", attempt, "duration_s", out.DurationS,
				"renditions", len(out.Renditions), "took_ms", time.Since(start).Milliseconds())
			return nil
		}
		if ctx.Err() != nil {
			log.Warn("job interrupted, offset not committed", "attempt", attempt)
			return ctx.Err()
		}
		permanent := errors.Is(err, transcode.ErrNoVideo)
		if permanent || attempt > len(w.Backoffs) {
			log.Error("transcode failed", "attempts", attempt, "permanent", permanent, "err", err)
			failed := events.VideoFailed{VideoID: ev.VideoID, Reason: truncate(err.Error(), 500)}
			if err := w.Pub.Publish(ctx, events.TopicVideoFailed, failed); err != nil {
				return err
			}
			return w.deadLetter(ctx, ev.VideoID, rec.Value, err, attempt)
		}
		wait := w.Backoffs[attempt-1]
		log.Warn("transcode attempt failed, retrying", "attempt", attempt, "retry_in", wait.String(), "err", err)
		if err := w.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (w *Worker) process(ctx context.Context, ev events.VideoUploaded) (events.VideoTranscoded, error) {
	work, err := os.MkdirTemp(w.WorkDir, "job-"+ev.VideoID+"-")
	if err != nil {
		return events.VideoTranscoded{}, err
	}
	defer os.RemoveAll(work)

	src := filepath.Join(work, "source"+path.Ext(ev.RawKey))
	if err := w.Store.Download(ctx, w.BucketRaw, ev.RawKey, src); err != nil {
		return events.VideoTranscoded{}, fmt.Errorf("download raw: %w", err)
	}
	out := filepath.Join(work, "hls")
	res, err := w.Transcoder.Transcode(ctx, src, out)
	if err != nil {
		return events.VideoTranscoded{}, fmt.Errorf("transcode: %w", err)
	}
	// Ghi đè hls/{id}/ nên chạy lại job là idempotent.
	if _, err := w.Store.UploadDir(ctx, w.BucketHLS, ev.VideoID, out); err != nil {
		return events.VideoTranscoded{}, fmt.Errorf("upload hls: %w", err)
	}
	return Transcoded(ev.VideoID, res), nil
}

// Transcoded dựng sự kiện video.transcoded từ kết quả FFmpeg.
func Transcoded(id string, res transcode.Result) events.VideoTranscoded {
	out := events.VideoTranscoded{VideoID: id, DurationS: int(math.Round(res.Probe.DurationSec)), HLSPrefix: id + "/"}
	for _, r := range res.Rungs {
		out.Renditions = append(out.Renditions, events.Rendition{
			Height: r.Height, BitrateKbps: r.VideoKbps, PlaylistKey: id + "/" + r.Name + "/index.m3u8",
		})
	}
	return out
}

func (w *Worker) deadLetter(ctx context.Context, videoID string, payload []byte, cause error, attempts int) error {
	raw := json.RawMessage(payload)
	if !json.Valid(payload) {
		raw, _ = json.Marshal(string(payload)) // payload hỏng: lưu dạng chuỗi JSON
	}
	return w.Pub.Publish(ctx, events.TopicVideoUploadedDLQ, events.DeadLetter{
		VideoID: videoID, Payload: raw, Error: cause.Error(), Attempts: attempts, FailedAt: time.Now().UTC(),
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
