package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/upload"
)

type emptyStore struct{}

func (emptyStore) List(context.Context, catalog.ListFilter) ([]catalog.Video, error) { return nil, nil }
func (emptyStore) ListReady(context.Context, int, int) ([]catalog.Video, error)      { return nil, nil }
func (emptyStore) Get(context.Context, string) (catalog.Video, error) {
	return catalog.Video{}, catalog.ErrNotFound
}
func (emptyStore) Renditions(context.Context, string) ([]catalog.Rendition, error) { return nil, nil }

// TestRouterAuthz kiểm tra phân quyền theo nhóm route; handler phía sau dùng store rỗng.
func TestRouterAuthz(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokens, err := auth.NewTokens("router-test-secret-0123456789-abcdef", auth.SessionTTL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h := newRouter(log, routes{
		Tokens: tokens,
		Auth:   &auth.Handler{Svc: &auth.Service{Tokens: tokens}, Log: log},
		Viewer: &catalog.ViewerHandler{Videos: emptyStore{}, Log: log},
		Upload: &upload.Handler{Log: log},
		Admin:  &catalog.AdminHandler{Videos: emptyStore{}, Log: log},
	})
	admin, _, _ := tokens.Issue(auth.Identity{UserID: "a", Role: auth.RoleAdmin})
	viewer, _, _ := tokens.Issue(auth.Identity{UserID: "v", Role: auth.RoleViewer})

	cases := []struct {
		method, path, cookie string
		want                 int
	}{
		{"GET", "/healthz", "", 200},
		{"GET", "/api/videos", "", 401},
		{"GET", "/api/videos", viewer, 200},
		{"GET", "/api/videos/11111111-1111-1111-1111-111111111111/play", "", 401},
		{"GET", "/api/videos/11111111-1111-1111-1111-111111111111/play", viewer, 404},
		{"GET", "/api/admin/videos", "", 401},
		{"GET", "/api/admin/videos", viewer, 403},
		{"POST", "/api/admin/videos", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/complete", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/retry", "", 401},
		{"GET", "/api/admin/videos", admin, 200},
		{"GET", "/api/admin/videos", "not-a-jwt", 401},
		{"GET", "/api/me", "", 401},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s (cookie=%v): %d, want %d", c.method, c.path, c.cookie != "", rec.Code, c.want)
		}
	}
}
