package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

type storeCall struct {
	method, id string
	arg        any
}

type fakeStatusStore struct {
	calls []storeCall
	err   error
	// status mô phỏng máy trạng thái thật khi != "" (dùng cho test thứ tự sự kiện).
	status Status
}

func (f *fakeStatusStore) step(to Status) (Status, error) {
	if f.status == "" {
		return "", f.err
	}
	from := f.status
	if !CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	f.status = to
	return from, nil
}

func (f *fakeStatusStore) Transition(_ context.Context, id string, to Status) (Status, error) {
	f.calls = append(f.calls, storeCall{"Transition", id, to})
	return f.step(to)
}

func (f *fakeStatusStore) MarkReady(_ context.Context, id string, info ReadyInfo) (Status, error) {
	f.calls = append(f.calls, storeCall{"MarkReady", id, info})
	return f.step(StatusReady)
}

func (f *fakeStatusStore) MarkFailed(_ context.Context, id, reason string) (Status, error) {
	f.calls = append(f.calls, storeCall{"MarkFailed", id, reason})
	return f.step(StatusFailed)
}

func record(t *testing.T, topic string, ev events.Event) *kgo.Record {
	t.Helper()
	rec, err := events.NewRecord(topic, ev)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func newUpdater(err error) (*StatusUpdater, *fakeStatusStore) {
	store := &fakeStatusStore{err: err}
	return &StatusUpdater{Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, store
}

func TestStatusUpdaterDispatch(t *testing.T) {
	u, store := newUpdater(nil)
	ctx := context.Background()
	recs := []*kgo.Record{
		record(t, events.TopicVideoProcessing, events.VideoProcessing{VideoID: idA}),
		record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA, DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []events.Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}},
			PosterKey:  idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"}),
		record(t, events.TopicVideoFailed, events.VideoFailed{VideoID: idB, Reason: "ffmpeg: boom"}),
	}
	for _, rec := range recs {
		if err := u.Handle(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	want := []storeCall{
		{"Transition", idA, StatusProcessing},
		{"MarkReady", idA, ReadyInfo{DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}},
			PosterKey:  idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"}},
		{"MarkFailed", idB, "ffmpeg: boom"},
	}
	if !reflect.DeepEqual(store.calls, want) {
		t.Errorf("calls:\n got %+v\nwant %+v", store.calls, want)
	}
}

func TestStatusUpdaterSkipsIgnorableErrors(t *testing.T) {
	ctx := context.Background()
	for _, err := range []error{fmt.Errorf("%w: ready -> ready", ErrInvalidTransition), ErrNotFound} {
		u, _ := newUpdater(err)
		if got := u.Handle(ctx, record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA})); got != nil {
			t.Errorf("err %v must be skipped, got %v", err, got)
		}
	}
	u, store := newUpdater(nil)
	if err := u.Handle(ctx, &kgo.Record{Topic: events.TopicVideoFailed, Value: []byte("not json")}); err != nil {
		t.Errorf("bad JSON must be skipped, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("bad JSON must not touch store: %+v", store.calls)
	}
}

func TestStatusUpdaterReturnsDBErrors(t *testing.T) {
	dbErr := errors.New("connection refused")
	u, _ := newUpdater(dbErr)
	err := u.Handle(context.Background(), record(t, events.TopicVideoProcessing, events.VideoProcessing{VideoID: idA}))
	if !errors.Is(err, dbErr) {
		t.Errorf("err = %v, want DB error so consumer retries", err)
	}
}

func TestStatusUpdaterTranscodedBeforeProcessing(t *testing.T) {
	u, store := newUpdater(nil)
	store.status = StatusQueued
	ctx := context.Background()
	transcoded := record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA, DurationS: 30, HLSPrefix: idA + "/"})
	if err := u.Handle(ctx, transcoded); err != nil {
		t.Fatal(err)
	}
	if store.status != StatusReady {
		t.Fatalf("status = %s, want ready", store.status)
	}
	// video.processing đến muộn trên video đã ready phải bị bỏ qua.
	if err := u.Handle(ctx, record(t, events.TopicVideoProcessing, events.VideoProcessing{VideoID: idA})); err != nil {
		t.Fatal(err)
	}
	if store.status != StatusReady {
		t.Errorf("status = %s after late processing, want ready", store.status)
	}
}

func TestStatusUpdaterFailedFromQueued(t *testing.T) {
	u, store := newUpdater(nil)
	store.status = StatusQueued
	if err := u.Handle(context.Background(), record(t, events.TopicVideoFailed, events.VideoFailed{VideoID: idA, Reason: "x"})); err != nil {
		t.Fatal(err)
	}
	if store.status != StatusFailed {
		t.Errorf("status = %s, want failed", store.status)
	}
}
