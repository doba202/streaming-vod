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
	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
	UpdateMetadata(ctx context.Context, id string, m Metadata) (Video, error)
}

// AdminHandler: danh sách, chi tiết (M2) và sửa metadata (M7) cho admin; router bọc auth.RequireAdmin.
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
	// Subtitles chỉ có ở GET /api/admin/videos/{id} (omitzero: nil ở danh sách thì bỏ, [] khi chưa có phụ đề vẫn ghi).
	Subtitles []AdminSubtitleJSON `json:"subtitles,omitzero"`
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
	r.Patch("/api/admin/videos/{id}", h.patch)
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
		h.Log.ErrorContext(r.Context(), "list videos", "err", err)
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
		h.Log.ErrorContext(r.Context(), "get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	rs, err := h.Videos.Renditions(r.Context(), id)
	if err != nil {
		h.Log.ErrorContext(r.Context(), "get renditions", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	subs, err := h.Videos.Subtitles(r.Context(), id)
	if err != nil {
		h.Log.ErrorContext(r.Context(), "get subtitles", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toJSON(v)
	out.Renditions = rs
	out.Subtitles = make([]AdminSubtitleJSON, 0, len(subs))
	for _, s := range subs {
		out.Subtitles = append(out.Subtitles, AdminSubtitleJSON{Lang: s.Lang, Label: s.Label, VTTKey: s.VTTKey})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// PatchInput: field bỏ trống (không có trong JSON) giữ nguyên; "year": 0 xoá năm; "genres": [] xoá thể loại.
type PatchInput struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Genres      *[]string `json:"genres"`
	Year        *int      `json:"year"`
}

// patch sửa metadata ở mọi trạng thái (kể cả đang transcode: chỉ đụng các cột metadata).
// Đọc rồi ghi đè cả bốn cột: hai admin sửa cùng lúc thì bản ghi sau thắng.
func (h *AdminHandler) patch(w http.ResponseWriter, r *http.Request) {
	parsed, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	id := parsed.String()
	var in PatchInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if in.Title == nil && in.Description == nil && in.Genres == nil && in.Year == nil {
		httpx.WriteError(w, http.StatusBadRequest, "nothing to update")
		return
	}
	cur, err := h.Videos.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.ErrorContext(r.Context(), "get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	m := Metadata{Title: cur.Title, Description: cur.Description, Genres: cur.Genres, Year: cur.Year}
	if in.Title != nil {
		m.Title = *in.Title
	}
	if in.Description != nil {
		m.Description = *in.Description
	}
	if in.Genres != nil {
		m.Genres = *in.Genres
	}
	if in.Year != nil {
		m.Year = *in.Year
	}
	m, err = NormalizeMetadata(m)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := h.Videos.UpdateMetadata(r.Context(), id, m)
	if errors.Is(err, ErrNotFound) { // bị xoá giữa Get và UPDATE
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.ErrorContext(r.Context(), "update video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.Log.InfoContext(r.Context(), "video metadata updated", "video_id", id)
	httpx.WriteJSON(w, http.StatusOK, toJSON(v))
}
