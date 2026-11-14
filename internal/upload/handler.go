package upload

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"streamfilm/internal/catalog"
	"streamfilm/internal/httpx"
)

// Handler gắn các endpoint upload của admin.
// M2: CHƯA có auth. M3 bọc nhóm /api/admin bằng middleware yêu cầu role admin.
type Handler struct {
	Svc *Service
	Log *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/admin/videos", h.create)
	r.Post("/api/admin/videos/{id}/complete", h.complete)
	r.Post("/api/admin/videos/{id}/retry", h.retry)
}

type statusResponse struct {
	ID     string         `json:"id"`
	Status catalog.Status `json:"status"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.Svc.Create(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	id, ok := videoID(w, r)
	if !ok {
		return
	}
	v, err := h.Svc.Complete(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, statusResponse{ID: v.ID, Status: v.Status})
}

func (h *Handler) retry(w http.ResponseWriter, r *http.Request) {
	id, ok := videoID(w, r)
	if !ok {
		return
	}
	v, err := h.Svc.Retry(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, statusResponse{ID: v.ID, Status: v.Status})
}

func videoID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return "", false
	}
	return id, true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, catalog.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "video not found")
	case errors.Is(err, catalog.ErrInvalidTransition):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrKafkaUnavailable):
		h.Log.Warn("publish failed", "request_id", middleware.GetReqID(r.Context()), "err", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "event bus unavailable, try again")
	default:
		h.Log.Error("request failed", "request_id", middleware.GetReqID(r.Context()),
			"path", r.URL.Path, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
	}
}
