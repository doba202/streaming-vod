// Package logging dựng logger slog JSON cho mọi service (spec §7): mỗi dòng luôn có "service", có
// "request_id" khi ctx của lời gọi *Context mang request id của chi, ghi ra stdout và (tuỳ chọn) đồng
// thời gửi vào Kafka topic app.logs qua KafkaSink — không bao giờ chặn người gọi, đầy thì bỏ.
package logging

import (
	"context"
	"io"
	"log/slog"

	"github.com/go-chi/chi/v5/middleware"
)

// New trả logger JSON của service. Mỗi bản ghi được ghi ra stdout; nếu kafka khác nil thì cùng
// dòng JSON đó được ghi tiếp vào kafka (thường là *KafkaSink). JSONHandler gọi Write đúng một lần
// cho mỗi bản ghi, nên mỗi lần Write là một dòng log hoàn chỉnh.
func New(service string, stdout io.Writer, kafka io.Writer) *slog.Logger {
	w := stdout
	if kafka != nil {
		w = io.MultiWriter(stdout, kafka)
	}
	return slog.New(contextHandler{slog.NewJSONHandler(w, nil)}).With("service", service)
}

// contextHandler thêm request_id từ ctx (middleware.RequestID của chi) khi bản ghi chưa có.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := middleware.GetReqID(ctx); id != "" && !hasAttr(r, "request_id") {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(as)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}
