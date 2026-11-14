package catalog

import (
	"context"
	"errors"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

type StatusStore interface {
	Transition(ctx context.Context, id string, to Status) (Status, error)
	MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error)
	MarkFailed(ctx context.Context, id, reason string) (Status, error)
}

// StatusUpdater consume video.processing / video.transcoded / video.failed và cập nhật Postgres.
// Idempotent: sự kiện trùng hoặc đến sau (chuyển trạng thái không hợp lệ, video đã bị xoá) được log và bỏ qua.
// Lỗi DB được trả về để events.Consumer thử lại record tại chỗ.
type StatusUpdater struct {
	Store StatusStore
	Log   *slog.Logger
}

func (u *StatusUpdater) Handle(ctx context.Context, rec *kgo.Record) error {
	var id string
	var err error
	switch rec.Topic {
	case events.TopicVideoProcessing:
		ev, derr := events.Decode[events.VideoProcessing](rec.Value)
		if derr != nil {
			return u.skipBad(rec, derr)
		}
		id = ev.VideoID
		_, err = u.Store.Transition(ctx, id, StatusProcessing)
	case events.TopicVideoTranscoded:
		ev, derr := events.Decode[events.VideoTranscoded](rec.Value)
		if derr != nil {
			return u.skipBad(rec, derr)
		}
		id = ev.VideoID
		info := ReadyInfo{DurationS: ev.DurationS, HLSPrefix: ev.HLSPrefix}
		for _, r := range ev.Renditions {
			info.Renditions = append(info.Renditions, Rendition{Height: r.Height, BitrateKbps: r.BitrateKbps, PlaylistKey: r.PlaylistKey})
		}
		_, err = u.Store.MarkReady(ctx, id, info)
	case events.TopicVideoFailed:
		ev, derr := events.Decode[events.VideoFailed](rec.Value)
		if derr != nil {
			return u.skipBad(rec, derr)
		}
		id = ev.VideoID
		_, err = u.Store.MarkFailed(ctx, id, ev.Reason)
	default:
		u.Log.Warn("unexpected topic", "topic", rec.Topic)
		return nil
	}

	switch {
	case err == nil:
		u.Log.Info("video status updated", "video_id", id, "topic", rec.Topic)
		return nil
	case errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrNotFound):
		u.Log.Info("event ignored", "video_id", id, "topic", rec.Topic, "reason", err.Error())
		return nil
	default:
		return err
	}
}

func (u *StatusUpdater) skipBad(rec *kgo.Record, err error) error {
	u.Log.Warn("bad event skipped", "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
	return nil
}
