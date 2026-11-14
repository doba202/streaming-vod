package catalog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"streamfilm/internal/httpx"
)

type AdminStore interface {
	List(ctx context.Context, f ListFilter) ([]Video, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
}

// AdminHandler: endpoint đọc cho admin. M2 chưa có auth; M3 thêm middleware role admin.
type AdminHandler struct {
	Videos AdminStore
	Log    *slog.Logger
}

type VideoJSON struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Genres      []string    `json:"genres"`
	Year        int         `json:"year,omitempty"`
	Status      Status      `json:"status"`
	FailReason  string      `json:"fail_reason,omitempty"`
	DurationS   int         `json:"duration_s,omitempty"`
	SizeBytes   int64       `json:"size_bytes"`
	HLSPrefix   string      `json:"hls_prefix,omitempty"`
	PosterKey   string      `json:"poster_key,omitempty"`
	SpriteKey   string      `json:"sprite_key,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Renditions  []Rendition `json:"renditions,omitempty"`
}

func toJSON(v Video) VideoJSON {
	genres := v.Genres
	if genres == nil {
		genres = []string{}
	}
	return VideoJSON{ID: v.ID, Title: v.Title, Description: v.Description, Genres: genres, Year: v.Year,
		Status: v.Status, FailReason: v.FailReason, DurationS: v.DurationS, SizeBytes: v.SizeBytes,
		HLSPrefix: v.HLSPrefix, PosterKey: v.PosterKey, SpriteKey: v.SpriteKey, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}

func (h *AdminHandler) Routes(r chi.Router) {
	r.Get("/api/admin/videos", h.list)
	r.Get("/api/admin/videos/{id}", h.get)
}

func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ListFilter{Status: Status(q.Get("status"))}
	if f.Status != "" && !f.Status.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "unknown status")
		return
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		f.Limit = n
	}
	vs, err := h.Videos.List(r.Context(), f)
	if err != nil {
		h.Log.Error("list videos", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]VideoJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, toJSON(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"videos": out})
}

func (h *AdminHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	v, err := h.Videos.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.Error("get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	rs, err := h.Videos.Renditions(r.Context(), id)
	if err != nil {
		h.Log.Error("get renditions", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toJSON(v)
	out.Renditions = rs
	httpx.WriteJSON(w, http.StatusOK, out)
}
