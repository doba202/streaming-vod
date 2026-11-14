package main

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/httpx"
	"streamfilm/internal/playback"
	"streamfilm/internal/upload"
)

type routes struct {
	Tokens    *auth.Tokens
	Auth      *auth.Handler
	Viewer    *catalog.ViewerHandler
	Events    *playback.Handler
	Upload    *upload.Handler
	Admin     *catalog.AdminHandler
	Subtitles *catalog.SubtitleHandler
}

// newRouter: public (/healthz, /api/auth/*), user (/api/me, /api/me/continue-watching, /api/videos/*,
// /api/genres, /api/playback/events), admin (/api/admin/*).
func newRouter(log *slog.Logger, rt routes) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, httpx.RequestLogger(log), rt.Tokens.Authenticate)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	rt.Auth.Routes(r)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireUser)
		rt.Viewer.Routes(r)
		rt.Events.Routes(r)
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		rt.Upload.Routes(r)
		rt.Admin.Routes(r)
		rt.Subtitles.Routes(r)
	})
	return r
}
