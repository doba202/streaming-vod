package playback

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
	"streamfilm/internal/httpx"
)

type VideoGetter interface {
	Get(ctx context.Context, id string) (catalog.Video, error)
}

type ProgressWriter interface {
	SaveProgress(ctx context.Context, userID, videoID string, positionS, durationS int) error
}

// EventSink nhận sự kiện để publish ở nền (*BestEffort).
type EventSink interface {
	Send(ev events.PlaybackEvent) bool
}

// Handler: POST /api/playback/events (router bọc auth.RequireUser).
type Handler struct {
	Videos   VideoGetter
	Progress ProgressWriter
	Events   EventSink
	Now      func() time.Time
	Log      *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/playback/events", h.event)
}

// event: kiểm tra input và video (ready), heartbeat/pause/ended thì upsert watch_progress, rồi đưa sự kiện
// cho BestEffort publish lên playback.events. Kafka lỗi không ảnh hưởng mã trả về (204).
func (h *Handler) event(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}
	var in EventInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	pos, err := in.Normalize()
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := h.Videos.Get(r.Context(), in.VideoID)
	if errors.Is(err, catalog.ErrNotFound) || (err == nil && v.Status != catalog.StatusReady) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.Error("get video", "video_id", in.VideoID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if v.DurationS > 0 {
		pos = min(pos, v.DurationS)
	}
	if in.Type.SavesProgress() {
		err := h.Progress.SaveProgress(r.Context(), id.UserID, v.ID, pos, v.DurationS)
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "video not found")
			return
		}
		if err != nil {
			h.Log.Error("save progress", "video_id", v.ID, "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	h.Events.Send(events.PlaybackEvent{TS: h.Now().UTC(), UserID: id.UserID, VideoID: v.ID, Type: string(in.Type),
		PositionS: pos, BitrateKbps: in.BitrateKbps, BufferingMs: in.BufferingMs})
	w.WriteHeader(http.StatusNoContent)
}
