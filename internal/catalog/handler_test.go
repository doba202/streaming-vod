package catalog

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeAdminStore struct {
	videos  []Video
	filter  ListFilter
	subs    []Subtitle
	updated *Metadata // tham số của lần UpdateMetadata gần nhất
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

func (f *fakeAdminStore) Subtitles(context.Context, string) ([]Subtitle, error) { return f.subs, nil }

func (f *fakeAdminStore) UpdateMetadata(_ context.Context, id string, m Metadata) (Video, error) {
	for i, v := range f.videos {
		if v.ID == id {
			f.updated = &m
			v.Title, v.Description, v.Genres, v.Year = m.Title, m.Description, m.Genres, m.Year
			f.videos[i] = v
			return v, nil
		}
	}
	return Video{}, ErrNotFound
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
	if v.Status != StatusReady || v.DurationS != 30 || len(v.Renditions) != 1 || v.Renditions[0].Height != 720 ||
		v.Subtitles == nil || len(v.Subtitles) != 0 {
		t.Errorf("video = %+v", v)
	}
	if rec := get(h, "/api/admin/videos/"+idX); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: code = %d", rec.Code)
	}
	if rec := get(h, "/api/admin/videos/xyz"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
}

func TestAdminGetSubtitles(t *testing.T) {
	store := &fakeAdminStore{videos: []Video{{ID: idA, Title: "A", Status: StatusReady}},
		subs: []Subtitle{{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}}}
	rec := get(newAdminRouter(store), "/api/admin/videos/"+idA)
	var v VideoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Subtitles) != 1 || v.Subtitles[0] != (AdminSubtitleJSON{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}) {
		t.Errorf("subtitles = %+v", v.Subtitles)
	}
}

func patch(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminPatch(t *testing.T) {
	store := &fakeAdminStore{videos: []Video{{ID: idA, Title: "Cũ", Description: "mô tả", Genres: []string{"drama"},
		Year: 2020, Status: StatusProcessing}}}
	h := newAdminRouter(store)

	// Chỉ đổi title: các field khác giữ nguyên; title được trim; trạng thái không ảnh hưởng.
	rec := patch(h, "/api/admin/videos/"+strings.ToUpper(idA), `{"title":"  Mới  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	var v VideoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Title != "Mới" || v.Description != "mô tả" || v.Year != 2020 || !slices.Equal(v.Genres, []string{"drama"}) ||
		v.Status != StatusProcessing {
		t.Errorf("patched = %+v", v)
	}

	// year 0 xoá năm, genres [] xoá thể loại.
	rec = patch(h, "/api/admin/videos/"+idA, `{"year":0,"genres":[],"description":""}`)
	if rec.Code != http.StatusOK || store.updated.Year != 0 || len(store.updated.Genres) != 0 || store.updated.Description != "" ||
		store.updated.Title != "Mới" {
		t.Errorf("code = %d updated = %+v", rec.Code, store.updated)
	}

	cases := []struct {
		path, body string
		want       int
	}{
		{"/api/admin/videos/" + idA, `{}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"title":"  "}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"year":1500}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"status":"ready"}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"title":`, http.StatusBadRequest},
		{"/api/admin/videos/" + idX, `{"title":"A"}`, http.StatusNotFound},
		{"/api/admin/videos/xyz", `{"title":"A"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := patch(h, c.path, c.body); rec.Code != c.want {
			t.Errorf("PATCH %s %s: code = %d, want %d (%s)", c.path, c.body, rec.Code, c.want, rec.Body)
		}
	}
}
