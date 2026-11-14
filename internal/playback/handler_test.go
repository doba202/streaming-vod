package playback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
)

const (
	readyID  = "11111111-1111-1111-1111-111111111111"
	queuedID = "22222222-2222-2222-2222-222222222222"
	otherID  = "99999999-9999-9999-9999-999999999999"
)

type fakeVideos struct{}

func (fakeVideos) Get(_ context.Context, id string) (catalog.Video, error) {
	switch id {
	case readyID:
		return catalog.Video{ID: id, Status: catalog.StatusReady, DurationS: 100}, nil
	case queuedID:
		return catalog.Video{ID: id, Status: catalog.StatusQueued}, nil
	}
	return catalog.Video{}, catalog.ErrNotFound
}

type saved struct {
	user, video string
	pos, dur    int
}

type fakeProgress struct {
	saved []saved
	err   error
}

func (f *fakeProgress) SaveProgress(_ context.Context, userID, videoID string, pos, dur int) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, saved{userID, videoID, pos, dur})
	return nil
}

type fakeSink struct {
	got  []events.PlaybackEvent
	full bool // giả lập BestEffort quá tải
}

func (f *fakeSink) Send(ev events.PlaybackEvent) bool {
	if f.full {
		return false
	}
	f.got = append(f.got, ev)
	return true
}

var now = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func newHandler(p *fakeProgress, s *fakeSink) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{UserID: "user-1", Role: auth.RoleViewer})))
		})
	})
	(&Handler{Videos: fakeVideos{}, Progress: p, Events: s, Now: func() time.Time { return now }, Log: discard()}).Routes(r)
	return r
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/playback/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEventHandler(t *testing.T) {
	p, s := &fakeProgress{}, &fakeSink{}
	h := newHandler(p, s)
	bodies := []string{
		`{"video_id":"` + readyID + `","type":"play","position_s":0}`,
		`{"video_id":"` + readyID + `","type":"heartbeat","position_s":12.7,"bitrate_kbps":2800}`,
		`{"video_id":"` + readyID + `","type":"buffering","position_s":13,"buffering_ms":850}`,
		`{"video_id":"` + readyID + `","type":"seek","position_s":60}`,
		`{"video_id":"` + readyID + `","type":"pause","position_s":61.2}`,
		`{"video_id":"` + readyID + `","type":"ended","position_s":100.4}`, // vượt thời lượng: kẹp về 100
	}
	for _, b := range bodies {
		if rec := post(h, b); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", b, rec.Code, rec.Body)
		}
	}
	wantSaved := []saved{{"user-1", readyID, 12, 100}, {"user-1", readyID, 61, 100}, {"user-1", readyID, 100, 100}}
	if !reflect.DeepEqual(p.saved, wantSaved) {
		t.Errorf("saved = %+v", p.saved)
	}
	if len(s.got) != 6 {
		t.Fatalf("events = %d, want 6", len(s.got))
	}
	want := events.PlaybackEvent{TS: now, UserID: "user-1", VideoID: readyID, Type: "buffering", PositionS: 13, BufferingMs: 850}
	if s.got[2] != want || s.got[1].BitrateKbps != 2800 || s.got[5].PositionS != 100 {
		t.Errorf("events = %+v", s.got)
	}
}

func TestEventHandlerRejects(t *testing.T) {
	p, s := &fakeProgress{}, &fakeSink{}
	h := newHandler(p, s)
	cases := []struct {
		body string
		want int
	}{
		{`{"video_id":"` + readyID + `","type":"rewind","position_s":1}`, 400},
		{`{"video_id":"` + readyID + `","type":"heartbeat","position_s":-1}`, 400},
		{`{"video_id":"` + readyID + `","type":"heartbeat","position_s":"10"}`, 400},
		{`{"video_id":"` + readyID + `","type":"heartbeat","extra":1}`, 400},
		{`{"video_id":"not-a-uuid","type":"heartbeat"}`, 400},
		{`not json`, 400},
		{`{"video_id":"` + queuedID + `","type":"heartbeat","position_s":1}`, 404},
		{`{"video_id":"` + otherID + `","type":"play","position_s":1}`, 404},
	}
	for _, c := range cases {
		if rec := post(h, c.body); rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.body, rec.Code, c.want)
		}
	}
	if len(p.saved) != 0 || len(s.got) != 0 {
		t.Errorf("không được ghi gì: saved=%v events=%v", p.saved, s.got)
	}
}

func TestEventHandlerKafkaAndDB(t *testing.T) {
	// BestEffort quá tải / Kafka chết: vẫn 204, tiến độ vẫn được ghi (spec §7).
	p := &fakeProgress{}
	h := newHandler(p, &fakeSink{full: true})
	if rec := post(h, `{"video_id":"`+readyID+`","type":"heartbeat","position_s":30}`); rec.Code != http.StatusNoContent || len(p.saved) != 1 {
		t.Errorf("sink đầy: %d saved=%v", rec.Code, p.saved)
	}
	// Lỗi DB khi ghi tiến độ → 500 (player bỏ qua); user/video đã bị xoá → 404.
	for err, want := range map[error]int{errors.New("db down"): 500, ErrNotFound: 404} {
		s := &fakeSink{}
		h := newHandler(&fakeProgress{err: err}, s)
		if rec := post(h, `{"video_id":"`+readyID+`","type":"pause","position_s":30}`); rec.Code != want || len(s.got) != 0 {
			t.Errorf("%v: %d events=%d, want %d", err, rec.Code, len(s.got), want)
		}
	}
}
