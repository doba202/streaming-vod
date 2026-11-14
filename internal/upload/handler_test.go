package upload

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/catalog"
)

func newRouter(svc *Service) http.Handler {
	r := chi.NewRouter()
	(&Handler{Svc: svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateEndpoint(t *testing.T) {
	svc, _, _, _ := newService()
	h := newRouter(svc)
	rec := do(h, http.MethodPost, "/api/admin/videos", `{"title":"A","filename":"a.mp4","size_bytes":100}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	var out CreateOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != firstID || out.Status != catalog.StatusUploading || len(out.Upload.Parts) != 1 {
		t.Errorf("out = %+v", out)
	}
	for _, body := range []string{`{"title":`, `{"title":"A","nope":1}`, `{"title":"A","filename":"a.avi","size_bytes":1}`} {
		if rec := do(h, http.MethodPost, "/api/admin/videos", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d", body, rec.Code)
		}
	}
}

func TestCompleteEndpoint(t *testing.T) {
	svc, _, objs, pub := newService()
	h := newRouter(svc)
	id := createOne(t, svc)
	objs.parts = 1

	if rec := do(h, http.MethodPost, "/api/admin/videos/not-a-uuid/complete", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/api/admin/videos/00000000-0000-0000-0000-000000000099/complete", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: code = %d", rec.Code)
	}

	pub.err = errors.New("broker down")
	rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/complete", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "event bus") {
		t.Errorf("kafka down: code = %d body = %s", rec.Code, rec.Body)
	}

	pub.err = nil
	rec = do(h, http.MethodPost, "/api/admin/videos/"+id+"/complete", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Errorf("ok: code = %d body = %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/complete", ""); rec.Code != http.StatusConflict {
		t.Errorf("twice: code = %d", rec.Code)
	}
}

func TestRetryEndpoint(t *testing.T) {
	svc, vids, _, _ := newService()
	h := newRouter(svc)
	id := createOne(t, svc)
	if rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/retry", ""); rec.Code != http.StatusConflict {
		t.Errorf("retry uploading: code = %d", rec.Code)
	}
	vids.setStatus(id, catalog.StatusFailed)
	rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/retry", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Errorf("retry failed: code = %d body = %s", rec.Code, rec.Body)
	}
}
