package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeSubtitleStore struct {
	videos map[string]Video
	saved  []Subtitle
}

func (f *fakeSubtitleStore) Get(_ context.Context, id string) (Video, error) {
	if v, ok := f.videos[id]; ok {
		return v, nil
	}
	return Video{}, ErrNotFound
}

func (f *fakeSubtitleStore) UpsertSubtitle(_ context.Context, _ string, s Subtitle) error {
	f.saved = append(f.saved, s)
	return nil
}

type putCall struct{ bucket, key, contentType, body string }

type fakePutter struct {
	calls []putCall
	err   error
}

func (f *fakePutter) PutObject(_ context.Context, bucket, key, contentType string, body []byte) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, putCall{bucket, key, contentType, string(body)})
	return nil
}

// subtitleForm dựng body multipart; filename rỗng thì không có phần file.
func subtitleForm(t *testing.T, lang, label, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("lang", lang)
	mw.WriteField("label", label)
	if filename != "" {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(content)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func newSubtitleRouter(store SubtitleStore, put ObjectPutter) http.Handler {
	r := chi.NewRouter()
	(&SubtitleHandler{Videos: store, Objects: put, BucketHLS: "hls",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func postSubtitle(h http.Handler, id string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/videos/"+id+"/subtitles", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const testSRT = "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:03,000\r\nXin chào\r\n"

func TestSubtitleUpload(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA, Status: StatusReady}}}
	put := &fakePutter{}
	h := newSubtitleRouter(store, put)

	body, ct := subtitleForm(t, "vi", "  Tiếng Việt ", "phim.vi.srt", []byte(testSRT))
	rec := postSubtitle(h, idA, body, ct)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	var out AdminSubtitleJSON
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out != (AdminSubtitleJSON{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}) {
		t.Errorf("out = %+v", out)
	}
	wantPut := []putCall{{"hls", idA + "/subs/vi.vtt", "text/vtt", "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nXin chào\n"}}
	if !reflect.DeepEqual(put.calls, wantPut) {
		t.Errorf("put = %+v", put.calls)
	}
	if !reflect.DeepEqual(store.saved, []Subtitle{{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}}) {
		t.Errorf("saved = %+v", store.saved)
	}

	body, ct = subtitleForm(t, "pt-BR", "Português", "a.VTT", []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nOlá\n"))
	if rec := postSubtitle(h, idA, body, ct); rec.Code != http.StatusCreated {
		t.Errorf("vtt pt-BR: %d %s", rec.Code, rec.Body)
	}
}

func TestSubtitleUploadRejects(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA}}}
	h := newSubtitleRouter(store, &fakePutter{})
	big := bytes.Repeat([]byte("a"), MaxSubtitleBytes+1)
	cases := []struct {
		name, id, lang, label, filename string
		content                         []byte
		want                            int
		msg                             string
	}{
		{"lang hoa", idA, "VI", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang gạch dưới", idA, "vi_VN", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang vùng thường", idA, "pt-br", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang traversal", idA, "../x", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang rỗng", idA, "", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"label rỗng", idA, "vi", "   ", "a.srt", []byte(testSRT), 400, "label"},
		{"label dài", idA, "vi", strings.Repeat("ạ", MaxLabelLen+1), "a.srt", []byte(testSRT), 400, "label"},
		{"label ký tự điều khiển", idA, "vi", "Việt\nNam", "a.srt", []byte(testSRT), 400, "label"},
		{"thiếu file", idA, "vi", "Việt", "", nil, 400, "file is required"},
		{"đuôi không hỗ trợ", idA, "vi", "Việt", "a.ass", []byte(testSRT), 400, "unsupported"},
		{"SRT hỏng", idA, "vi", "Việt", "a.srt", []byte("1\nkhông phải mốc\nA\n"), 400, "line 2"},
		{"không phải UTF-8", idA, "vi", "Việt", "a.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nch\xe0o\n"), 400, "UTF-8"},
		{"quá 1 MiB", idA, "vi", "Việt", "a.srt", big, 413, "1 MiB"},
		{"video không tồn tại", idX, "vi", "Việt", "a.srt", []byte(testSRT), 404, "not found"},
		{"id không phải uuid", "abc", "vi", "Việt", "a.srt", []byte(testSRT), 404, "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, ct := subtitleForm(t, c.lang, c.label, c.filename, c.content)
			rec := postSubtitle(h, c.id, body, ct)
			if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.msg) {
				t.Errorf("code = %d body = %s, want %d chứa %q", rec.Code, rec.Body, c.want, c.msg)
			}
		})
	}
	if rec := postSubtitle(h, idA, strings.NewReader(`{"lang":"vi"}`), "application/json"); rec.Code != 400 {
		t.Errorf("json body: %d", rec.Code)
	}
	if len(store.saved) != 0 {
		t.Errorf("không được lưu gì: %+v", store.saved)
	}
}

func TestSubtitleUploadStorageError(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA}}}
	h := newSubtitleRouter(store, &fakePutter{err: errors.New("minio down")})
	body, ct := subtitleForm(t, "vi", "Việt", "a.srt", []byte(testSRT))
	if rec := postSubtitle(h, idA, body, ct); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "minio") {
		t.Errorf("code = %d body = %s", rec.Code, rec.Body)
	}
	if len(store.saved) != 0 {
		t.Error("lỗi storage thì không ghi bảng subtitles")
	}
}

func TestRepoSubtitles(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if err := r.Create(ctx, newVideo(idA)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []Subtitle{
		{Lang: "vi", Label: "Tiếng Việt", VTTKey: SubtitleKey(idA, "vi")},
		{Lang: "en", Label: "English", VTTKey: SubtitleKey(idA, "en")},
		{Lang: "vi", Label: "Tiếng Việt (sửa)", VTTKey: SubtitleKey(idA, "vi")}, // upload lại: ghi đè
	} {
		if err := r.UpsertSubtitle(ctx, idA, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Subtitles(ctx, idA)
	want := []Subtitle{
		{Lang: "en", Label: "English", VTTKey: idA + "/subs/en.vtt"},
		{Lang: "vi", Label: "Tiếng Việt (sửa)", VTTKey: idA + "/subs/vi.vtt"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("subtitles = %+v, err = %v", got, err)
	}
	if got, err := r.Subtitles(ctx, idB); err != nil || len(got) != 0 {
		t.Errorf("video không có phụ đề: %+v, %v", got, err)
	}
	if err := r.UpsertSubtitle(ctx, idX, Subtitle{Lang: "vi", Label: "x", VTTKey: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("video không tồn tại: err = %v, want ErrNotFound", err)
	}
}

func TestSubtitleUploadCanonicalID(t *testing.T) {
	const id = "abcdefab-1111-2222-3333-444444444444"
	store := &fakeSubtitleStore{videos: map[string]Video{id: {ID: id, Status: StatusReady}}}
	put := &fakePutter{}
	h := newSubtitleRouter(store, put)
	body, ct := subtitleForm(t, "vi", "Việt", "a.srt", []byte(testSRT))
	rec := postSubtitle(h, strings.ToUpper(id), body, ct) // uuid.Parse chấp nhận chữ hoa
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	var out AdminSubtitleJSON
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.VTTKey != id+"/subs/vi.vtt" || len(put.calls) != 1 || put.calls[0].key != id+"/subs/vi.vtt" ||
		len(store.saved) != 1 || store.saved[0].VTTKey != id+"/subs/vi.vtt" {
		t.Errorf("key phải theo uuid chuẩn: out=%+v put=%+v saved=%+v", out, put.calls, store.saved)
	}
}

func TestSubtitleUploadFormOnlyFields(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA}}}
	h := newSubtitleRouter(store, &fakePutter{})
	// lang/label chỉ lấy từ body multipart, không từ query string.
	body, ct := subtitleForm(t, "", "Việt", "a.srt", []byte(testSRT))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/videos/"+idA+"/subtitles?lang=vi", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lang") || len(store.saved) != 0 {
		t.Errorf("code = %d body = %s saved = %+v", rec.Code, rec.Body, store.saved)
	}
}

func TestSubtitleUploadBodyOverCap(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA}}}
	put := &fakePutter{}
	h := newSubtitleRouter(store, put)
	// Lớn hơn MaxSubtitleBytes + formOverhead: MaxBytesReader chặn khi parse multipart.
	huge := bytes.Repeat([]byte("a"), MaxSubtitleBytes+formOverhead+1)
	body, ct := subtitleForm(t, "vi", "Việt", "a.srt", huge)
	rec := postSubtitle(h, idA, body, ct)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "1 MiB") {
		t.Errorf("code = %d body = %s", rec.Code, rec.Body)
	}
	if len(put.calls) != 0 || len(store.saved) != 0 {
		t.Errorf("không được ghi gì: %+v %+v", put.calls, store.saved)
	}
}
