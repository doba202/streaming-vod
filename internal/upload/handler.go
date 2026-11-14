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

// Handler gắn các endpoint vòng đời video của admin: tạo + presigned upload, complete, retry (M2), xoá (M7).
// Router bọc nhóm /api/admin bằng auth.RequireAdmin (M3).
type Handler struct {
	Svc *Service
	Log *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/admin/videos", h.create)
	r.Post("/api/admin/videos/{id}/complete", h.complete)
	r.Post("/api/admin/videos/{id}/retry", h.retry)
	r.Delete("/api/admin/videos/{id}", h.delete)
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

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := videoID(w, r)
	if !ok {
		return
	}
	if err := h.Svc.Delete(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// videoID trả id dạng chuẩn (chữ thường) để khớp key object {id}/ và cột uuid.
func videoID(w http.ResponseWriter, r *http.Request) (string, bool) {
	parsed, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return "", false
	}
	return parsed.String(), true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, catalog.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "video not found")
	case errors.Is(err, catalog.ErrInvalidTransition), errors.Is(err, catalog.ErrVideoBusy):
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
