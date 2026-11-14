package catalog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeAdminStore struct {
	videos []Video
	filter ListFilter
}

func (f *fakeAdminStore) List(_ context.Context, lf ListFilter) ([]Video, error) {
	f.filter = lf
	return f.videos, nil
}

func (f *fakeAdminStore) Get(_ context.Context, id string) (Video, error) {
	for _, v := range f.videos {
		if v.ID == id {
			return v, nil
		}
	}
	return Video{}, ErrNotFound
}

func (f *fakeAdminStore) Renditions(context.Context, string) ([]Rendition, error) {
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

func newAdminRouter(store AdminStore) http.Handler {
	r := chi.NewRouter()
	(&AdminHandler{Videos: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAdminList(t *testing.T) {
	store := &fakeAdminStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusFailed, FailReason: "ffmpeg: boom"},
	}}
	h := newAdminRouter(store)
	rec := get(h, "/api/admin/videos?status=failed&limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if store.filter.Status != StatusFailed || store.filter.Limit != 10 {
		t.Errorf("filter = %+v", store.filter)
	}
	var body struct {
		Videos []VideoJSON `json:"videos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Videos) != 1 || body.Videos[0].FailReason != "ffmpeg: boom" || body.Videos[0].Genres == nil {
		t.Errorf("videos = %+v", body.Videos)
	}
	for _, q := range []string{"?status=bogus", "?limit=0", "?limit=abc", "?limit=201"} {
		if rec := get(h, "/api/admin/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", q, rec.Code)
		}
	}
}

func TestAdminGet(t *testing.T) {
	h := newAdminRouter(&fakeAdminStore{videos: []Video{{ID: idA, Title: "A", Status: StatusReady, DurationS: 30}}})
	rec := get(h, "/api/admin/videos/"+idA)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var v VideoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusReady || v.DurationS != 30 || len(v.Renditions) != 1 || v.Renditions[0].Height != 720 {
		t.Errorf("video = %+v", v)
	}
	if rec := get(h, "/api/admin/videos/"+idX); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: code = %d", rec.Code)
	}
	if rec := get(h, "/api/admin/videos/xyz"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
}
