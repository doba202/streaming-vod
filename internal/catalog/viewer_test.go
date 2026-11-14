package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/auth"
)

type fakeViewerStore struct {
	videos      []Video // thứ tự = thứ tự catalog
	genres      []string
	subs        map[string][]Subtitle
	last        SearchParams
	limit, offs int
}

// Search giả: chỉ lọc ready và phân trang; tham số nhận được ghi vào last.
func (f *fakeViewerStore) Search(_ context.Context, p SearchParams) ([]Video, error) {
	f.last, f.limit, f.offs = p, p.Limit, p.Offset
	var ready []Video
	for _, v := range f.videos {
		if v.Status == StatusReady {
			ready = append(ready, v)
		}
	}
	if p.Offset >= len(ready) {
		return nil, nil
	}
	return ready[p.Offset:min(p.Offset+p.Limit, len(ready))], nil
}

func (f *fakeViewerStore) Genres(context.Context) ([]string, error) { return f.genres, nil }

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

func (f *fakeViewerStore) Subtitles(_ context.Context, id string) ([]Subtitle, error) {
	return f.subs[id], nil
}

type fakeSigner struct{ exp time.Time }

func (s fakeSigner) Path(id, file string) (string, time.Time, error) {
	return "/cdn/s/SIG/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}

func (s fakeSigner) StablePath(id, file string) (string, time.Time, error) {
	return "/cdn/s/STABLE/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}

func (f *fakeViewerStore) ReadyByIDs(_ context.Context, ids []string) ([]Video, error) {
	var out []Video
	for i := len(f.videos) - 1; i >= 0; i-- { // thứ tự khác ids: handler phải tự sắp lại
		v := f.videos[i]
		if v.Status == StatusReady && slices.Contains(ids, v.ID) {
			out = append(out, v)
		}
	}
	return out, nil
}

// fakeProgress: tiến độ của user "user-1".
type fakeProgress struct {
	resume  map[string]int
	entries []WatchEntry
	err     error
	limit   int
}

func (f *fakeProgress) ResumeAt(_ context.Context, userID, videoID string) (int, error) {
	if userID != "user-1" {
		return 0, nil
	}
	return f.resume[videoID], f.err
}

func (f *fakeProgress) ContinueWatching(_ context.Context, userID string, limit int) ([]WatchEntry, error) {
	f.limit = limit
	if userID != "user-1" {
		return nil, nil
	}
	return f.entries, f.err
}

func newViewerRouter(store ViewerStore) http.Handler {
	return newViewerRouterWith(store, &fakeProgress{})
}

// newViewerRouterWith: mọi request mang identity user-1 (thay cho auth.Authenticate + RequireUser).
func newViewerRouterWith(store ViewerStore, progress ProgressSource) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{UserID: "user-1", Role: auth.RoleViewer})))
		})
	})
	(&ViewerHandler{Videos: store, Progress: progress, Signer: fakeSigner{exp: time.Unix(1794736800, 0).UTC()},
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

func TestViewerSearchParams(t *testing.T) {
	store := &fakeViewerStore{}
	h := newViewerRouter(store)

	rec := get(h, "/api/videos?q=%20tro%20choi%20&genre=H%C3%A0nh%20%C4%91%E1%BB%99ng&year=2021&page=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	want := SearchParams{Query: "tro choi", Genre: "Hành động", Year: 2021, Limit: PageSize + 1, Offset: 2 * PageSize}
	if store.last != want {
		t.Errorf("params = %+v, want %+v", store.last, want)
	}
	if !strings.Contains(rec.Body.String(), `"videos":[]`) {
		t.Errorf("không có kết quả thì videos phải là []: %s", rec.Body)
	}

	get(h, "/api/videos")
	if store.last != (SearchParams{Limit: PageSize + 1}) {
		t.Errorf("không tham số: %+v", store.last)
	}

	long := strings.Repeat("ă", MaxQueryLen+1)
	for _, q := range []string{"?year=abc", "?year=1800", "?year=2101", "?q=" + long,
		"?genre=" + strings.Repeat("g", MaxGenreLen+1),
		"?q=%FF", "?q=%00", "?genre=%FF", "?genre=a%00b"} {
		if rec := get(h, "/api/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", q, rec.Code)
		}
	}
	if rec := get(h, "/api/videos?q="+strings.Repeat("ă", MaxQueryLen)); rec.Code != http.StatusOK {
		t.Errorf("q dài đúng %d ký tự phải hợp lệ: %d", MaxQueryLen, rec.Code)
	}
}

func TestViewerGenres(t *testing.T) {
	rec := get(newViewerRouter(&fakeViewerStore{}), "/api/genres")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"genres":[]}` {
		t.Errorf("empty: %d %s", rec.Code, rec.Body)
	}
	rec = get(newViewerRouter(&fakeViewerStore{genres: []string{"Gia đình", "Hành động"}}), "/api/genres")
	if strings.TrimSpace(rec.Body.String()) != `{"genres":["Gia đình","Hành động"]}` {
		t.Errorf("genres: %s", rec.Body)
	}
}

func TestViewerPosterAndPlayExtras(t *testing.T) {
	store := &fakeViewerStore{
		videos: []Video{
			{ID: idA, Title: "A", Status: StatusReady, DurationS: 30,
				PosterKey: idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"},
			{ID: idC, Title: "C (trước M5)", Status: StatusReady, DurationS: 30},
		},
		subs: map[string][]Subtitle{idA: {
			{Lang: "en", Label: "English", VTTKey: idA + "/subs/en.vtt"},
			{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"},
			{Lang: "xx", Label: "Hỏng", VTTKey: idB + "/subs/xx.vtt"}, // key ngoài thư mục video: bỏ qua
		}},
	}
	h := newViewerRouter(store)
	const base = "http://cdn.test/cdn/s/"

	var list struct {
		Videos []ViewerVideoJSON `json:"videos"`
	}
	json.Unmarshal(get(h, "/api/videos").Body.Bytes(), &list)
	if len(list.Videos) != 2 || list.Videos[0].PosterURL != base+"STABLE/1794736800/hls/"+idA+"/poster.jpg" ||
		list.Videos[1].PosterURL != "" {
		t.Errorf("list posters = %+v", list.Videos)
	}
	if rec := get(h, "/api/videos/"+idC); strings.Contains(rec.Body.String(), "poster_url") {
		t.Errorf("video không có poster thì bỏ poster_url: %s", rec.Body)
	}
	var detail ViewerVideoJSON
	json.Unmarshal(get(h, "/api/videos/"+idA).Body.Bytes(), &detail)
	if detail.PosterURL != base+"STABLE/1794736800/hls/"+idA+"/poster.jpg" {
		t.Errorf("detail poster = %q", detail.PosterURL)
	}

	var p PlayJSON
	json.Unmarshal(get(h, "/api/videos/"+idA+"/play").Body.Bytes(), &p)
	sig := base + "SIG/1794736800/hls/" + idA + "/"
	wantSubs := []SubtitleJSON{
		{Lang: "en", Label: "English", URL: sig + "subs/en.vtt"},
		{Lang: "vi", Label: "Tiếng Việt", URL: sig + "subs/vi.vtt"},
	}
	if p.ThumbnailsURL != sig+"thumbnails.vtt" || !reflect.DeepEqual(p.Subtitles, wantSubs) {
		t.Errorf("play = %+v", p)
	}
	rec := get(h, "/api/videos/"+idC+"/play")
	if strings.Contains(rec.Body.String(), "thumbnails_url") || !strings.Contains(rec.Body.String(), `"subtitles":[]`) {
		t.Errorf("video trước M5: %s", rec.Body)
	}
}

func TestViewerResumeAt(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{{ID: idA, Status: StatusReady, DurationS: 100}}}
	progress := &fakeProgress{resume: map[string]int{idA: 42}}
	var p PlayJSON
	json.Unmarshal(get(newViewerRouterWith(store, progress), "/api/videos/"+idA+"/play").Body.Bytes(), &p)
	if p.ResumeAt != 42 {
		t.Errorf("resume_at = %d, want 42", p.ResumeAt)
	}
	// Lỗi đọc tiến độ không chặn việc xem: vẫn 200, phát từ đầu.
	progress.err = errors.New("db down")
	rec := get(newViewerRouterWith(store, progress), "/api/videos/"+idA+"/play")
	json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusOK || p.ResumeAt != 0 {
		t.Errorf("progress lỗi: %d resume_at=%d", rec.Code, p.ResumeAt)
	}
}

func TestViewerContinueWatching(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusReady, DurationS: 100, PosterKey: idA + "/poster.jpg"},
		{ID: idB, Title: "B", Status: StatusReady, DurationS: 200},
	}}
	t1 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	progress := &fakeProgress{entries: []WatchEntry{
		{VideoID: idB, PositionS: 120, DurationS: 200, UpdatedAt: t1.Add(time.Minute)},
		{VideoID: idX, PositionS: 10, DurationS: 100, UpdatedAt: t1.Add(30 * time.Second)}, // đã bị xoá: bỏ qua
		{VideoID: idA, PositionS: 40, DurationS: 100, UpdatedAt: t1},
	}}
	rec := get(newViewerRouterWith(store, progress), "/api/me/continue-watching")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Videos []ContinueJSON `json:"videos"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != 2 || body.Videos[0].ID != idB || body.Videos[0].PositionS != 120 ||
		!body.Videos[0].WatchedAt.Equal(t1.Add(time.Minute)) || body.Videos[1].ID != idA || body.Videos[1].PositionS != 40 ||
		body.Videos[1].PosterURL == "" || body.Videos[1].DurationS != 100 {
		t.Errorf("videos = %+v", body.Videos)
	}
	if progress.limit != ContinueWatchingLimit {
		t.Errorf("limit = %d", progress.limit)
	}
	rec = get(newViewerRouterWith(store, &fakeProgress{}), "/api/me/continue-watching")
	if strings.TrimSpace(rec.Body.String()) != `{"videos":[]}` {
		t.Errorf("rỗng: %s", rec.Body)
	}
}
