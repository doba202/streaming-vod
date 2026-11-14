// Package httpx chứa helper HTTP dùng chung cho các module của api.
package httpx

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// DecodeJSON đọc body JSON tối đa 1 MiB và từ chối field không biết.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// RequestLogger log mỗi request kèm request_id (từ middleware.RequestID) và video_id khi route có
// tham số {id} là UUID (M6: lọc log theo video trong ClickHouse); bỏ qua /healthz.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			attrs := []any{"request_id", middleware.GetReqID(r.Context()),
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds()}
			// chi điền URLParams vào RouteContext trong lúc định tuyến, nên đọc được sau next.
			if id, err := uuid.Parse(chi.URLParam(r, "id")); err == nil {
				attrs = append(attrs, "video_id", id.String())
			}
			log.Info("http request", attrs...)
		})
	}
}
