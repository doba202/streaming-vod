package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type fakeViewerStore struct {
	videos      []Video // thứ tự = thứ tự catalog
	limit, offs int
}

func (f *fakeViewerStore) ListReady(_ context.Context, limit, offset int) ([]Video, error) {
	f.limit, f.offs = limit, offset
	var ready []Video
	for _, v := range f.videos {
		if v.Status == StatusReady {
			ready = append(ready, v)
		}
	}
	if offset >= len(ready) {
		return nil, nil
	}
	return ready[offset:min(offset+limit, len(ready))], nil
}

func (f *fakeViewerStore) Get(_ context.Context, id string) (Video, error) {
	for _, v := range f.videos {
		if v.ID == id {
			return v, nil
		}
	}
	return Video{}, ErrNotFound
}

func (f *fakeViewerStore) Renditions(context.Context, string) ([]Rendition, error) {
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

type fakeSigner struct{ exp time.Time }

func (s fakeSigner) Path(id, file string) (string, time.Time, error) {
	return "/cdn/s/SIG/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}

func newViewerRouter(store ViewerStore) http.Handler {
	r := chi.NewRouter()
	(&ViewerHandler{Videos: store, Signer: fakeSigner{exp: time.Unix(1794736800, 0).UTC()},
		CDNBaseURL: "http://cdn.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func TestViewerList(t *testing.T) {
	store := &fakeViewerStore{}
	for i := range PageSize + 3 {
		store.videos = append(store.videos, Video{ID: fmt.Sprintf("v%02d", i), Title: "T", Status: StatusReady})
	}
	store.videos = append(store.videos, Video{ID: "hidden", Status: StatusProcessing})
	h := newViewerRouter(store)

	var body struct {
		Videos  []ViewerVideoJSON `json:"videos"`
		Page    int               `json:"page"`
		HasMore bool              `json:"has_more"`
	}
	rec := get(h, "/api/videos")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != PageSize || body.Page != 1 || !body.HasMore || store.limit != PageSize+1 || store.offs != 0 {
		t.Errorf("page 1: n=%d page=%d more=%v limit=%d off=%d", len(body.Videos), body.Page, body.HasMore, store.limit, store.offs)
	}
	if body.Videos[0].Genres == nil {
		t.Error("genres phải là [] chứ không phải null")
	}
	if strings.Contains(rec.Body.String(), `"status"`) || strings.Contains(rec.Body.String(), "raw_key") {
		t.Error("catalog người xem không được lộ status/raw_key")
	}

	rec = get(h, "/api/videos?page=2")
	body.Videos = nil
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != 3 || body.HasMore || store.offs != PageSize {
		t.Errorf("page 2: n=%d more=%v off=%d", len(body.Videos), body.HasMore, store.offs)
	}
	for _, q := range []string{"?page=0", "?page=abc", "?page=1001"} {
		if rec := get(h, "/api/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", q, rec.Code)
		}
	}
}

func TestViewerGetAndPlay(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusReady, DurationS: 30},
		{ID: idB, Title: "B", Status: StatusProcessing},
	}}
	h := newViewerRouter(store)

	rec := get(h, "/api/videos/"+idA)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"renditions":[{"height":720,"bitrate_kbps":2800}]`) {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}

	rec = get(h, "/api/videos/"+idA+"/play")
	if rec.Code != http.StatusOK {
		t.Fatalf("play: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("play phải có Cache-Control: no-store")
	}
	var p PlayJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.URL != "http://cdn.test/cdn/s/SIG/1794736800/hls/"+idA+"/master.m3u8" ||
		!p.ExpiresAt.Equal(time.Unix(1794736800, 0)) || p.ResumeAt != 0 || p.Subtitles == nil {
		t.Errorf("play = %+v", p)
	}
	if !strings.Contains(rec.Body.String(), `"subtitles":[]`) {
		t.Errorf("subtitles phải là []: %s", rec.Body)
	}

	for _, path := range []string{
		"/api/videos/" + idB, "/api/videos/" + idB + "/play", // chưa ready
		"/api/videos/" + idX + "/play", "/api/videos/not-a-uuid/play",
	} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404", path, rec.Code)
		}
	}
}
