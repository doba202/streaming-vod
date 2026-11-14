package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger: request_id lấy từ header X-Request-Id (middleware.RequestID), video_id từ {id} khi là
// UUID (kể cả route con), không có video_id khi {id} không phải UUID hoặc route không có {id}.
func TestRequestLoggerAddsRequestAndVideoID(t *testing.T) {
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(middleware.RequestID, RequestLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	r.Route("/api/videos", func(r chi.Router) {
		r.Get("/{id}/play", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	r.Get("/api/genres", func(w http.ResponseWriter, _ *http.Request) {})

	const id = "0b0c4c1e-8f4a-4a52-9a52-6f1f2b0c9d11"
	for _, path := range []string{"/api/videos/" + strings.ToUpper(id) + "/play", "/api/videos/abc/play", "/api/genres"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Request-Id", "e2e-req-1")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d: %s", len(lines), buf.String())
	}
	want := []any{id, nil, nil}
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if m["request_id"] != "e2e-req-1" || m["msg"] != "http request" {
			t.Errorf("line %d = %v", i, m)
		}
		if m["video_id"] != want[i] {
			t.Errorf("line %d video_id = %v, want %v", i, m["video_id"], want[i])
		}
	}
}
