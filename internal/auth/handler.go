package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"streamfilm/internal/httpx"
)

// Handler gắn các endpoint /api/auth/* và /api/me.
type Handler struct {
	Svc          *Service
	SecureCookie bool
	Log          *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/auth/register", h.register)
	r.Post("/api/auth/login", h.login)
	r.Post("/api/auth/logout", h.logout)
	r.With(RequireUser).Get("/api/me", h.me)
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserJSON struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func toJSON(u User) UserJSON {
	return UserJSON{ID: u.ID, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt}
}

// register tạo viewer và đăng nhập luôn (đặt cookie).
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := h.Svc.Register(r.Context(), in.Email, in.Password)
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		h.internal(w, r, err)
		return
	}
	h.startSession(w, r, u, http.StatusCreated)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := h.Svc.Login(r.Context(), in.Email, in.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		httpx.WriteError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		h.internal(w, r, err)
		return
	}
	h.startSession(w, r, u, http.StatusOK)
}

func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, u User, status int) {
	tok, exp, err := h.Svc.Session(u)
	if err != nil {
		h.internal(w, r, err)
		return
	}
	SetSessionCookie(w, tok, exp, h.SecureCookie)
	httpx.WriteJSON(w, status, toJSON(u))
}

// logout chỉ xoá cookie. JWT không lưu phía server nên token đã lộ vẫn hợp lệ tới khi hết hạn (ADR 0010).
func (h *Handler) logout(w http.ResponseWriter, _ *http.Request) {
	ClearSessionCookie(w, h.SecureCookie)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	u, err := h.Svc.Users.GetByID(r.Context(), id.UserID)
	if errors.Is(err, ErrUserNotFound) {
		ClearSessionCookie(w, h.SecureCookie)
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}
	if err != nil {
		h.internal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toJSON(u))
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, err error) {
	h.Log.Error("auth request failed", "request_id", middleware.GetReqID(r.Context()),
		"path", r.URL.Path, "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}
