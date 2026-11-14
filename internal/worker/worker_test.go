package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
	"streamfilm/internal/transcode"
)

type fakeTranscoder struct {
	errs  []error // lỗi theo từng lần gọi; hết danh sách thì thành công
	calls int
	res   transcode.Result
}

func (f *fakeTranscoder) Transcode(_ context.Context, input, outDir string) (transcode.Result, error) {
	f.calls++
	if f.calls <= len(f.errs) && f.errs[f.calls-1] != nil {
		return transcode.Result{}, f.errs[f.calls-1]
	}
	return f.res, nil
}

type fakeStore struct {
	downloads, uploads []string
}

func (f *fakeStore) Download(_ context.Context, bucket, key, dest string) error {
	f.downloads = append(f.downloads, bucket+"/"+key)
	return os.WriteFile(dest, []byte("raw"), 0o644)
}

func (f *fakeStore) UploadDir(_ context.Context, bucket, prefix, dir string) (int, error) {
	f.uploads = append(f.uploads, bucket+"/"+prefix)
	return 3, nil
}

type published struct {
	topic string
	ev    events.Event
}

type fakePub struct {
	got   []published
	errOn map[string]error
}

func (f *fakePub) Publish(_ context.Context, topic string, ev events.Event) error {
	if err := f.errOn[topic]; err != nil {
		return err
	}
	f.got = append(f.got, published{topic, ev})
	return nil
}

func (f *fakePub) topics() []string {
	var out []string
	for _, p := range f.got {
		out = append(out, p.topic)
	}
	return out
}

var okResult = transcode.Result{
	Probe: transcode.ProbeResult{Width: 1280, Height: 720, DurationSec: 29.6, HasAudio: true},
	Rungs: []transcode.Rung{transcode.DefaultLadder[1], transcode.DefaultLadder[3]},
}

type harness struct {
	w     *Worker
	tr    *fakeTranscoder
	store *fakeStore
	pub   *fakePub
	slept []time.Duration
}

func newHarness(t *testing.T, errs ...error) *harness {
	h := &harness{tr: &fakeTranscoder{errs: errs, res: okResult}, store: &fakeStore{},
		pub: &fakePub{errOn: map[string]error{}}}
	h.w = &Worker{Transcoder: h.tr, Store: h.store, Pub: h.pub, BucketRaw: "raw", BucketHLS: "hls",
		WorkDir: t.TempDir(), Backoffs: DefaultBackoffs,
		Sleep: func(_ context.Context, d time.Duration) error { h.slept = append(h.slept, d); return nil },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil))}
	return h
}

func uploadedRecord(t *testing.T) *kgo.Record {
	t.Helper()
	rec, err := events.NewRecord(events.TopicVideoUploaded, events.VideoUploaded{VideoID: "vid-1", RawKey: "vid-1/source.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestHandleSuccess(t *testing.T) {
	h := newHarness(t)
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err != nil {
		t.Fatal(err)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoProcessing, events.TopicVideoTranscoded}) {
		t.Fatalf("topics = %v", got)
	}
	want := events.VideoTranscoded{VideoID: "vid-1", DurationS: 30, HLSPrefix: "vid-1/", Renditions: []events.Rendition{
		{Height: 720, BitrateKbps: 2800, PlaylistKey: "vid-1/720p/index.m3u8"},
		{Height: 360, BitrateKbps: 800, PlaylistKey: "vid-1/360p/index.m3u8"},
	}}
	if got := h.pub.got[1].ev; !reflect.DeepEqual(got, want) {
		t.Errorf("transcoded = %+v", got)
	}
	if !reflect.DeepEqual(h.store.downloads, []string{"raw/vid-1/source.mp4"}) ||
		!reflect.DeepEqual(h.store.uploads, []string{"hls/vid-1"}) {
		t.Errorf("downloads=%v uploads=%v", h.store.downloads, h.store.uploads)
	}
	if len(h.slept) != 0 {
		t.Errorf("slept %v", h.slept)
	}
	if entries, _ := os.ReadDir(h.w.WorkDir); len(entries) != 0 {
		t.Errorf("work dir not cleaned: %d entries", len(entries))
	}
}

func TestHandleRetriesThenSucceeds(t *testing.T) {
	boom := errors.New("ffmpeg: boom")
	h := newHarness(t, boom, boom)
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err != nil {
		t.Fatal(err)
	}
	if h.tr.calls != 3 || !reflect.DeepEqual(h.slept, []time.Duration{5 * time.Second, 30 * time.Second}) {
		t.Errorf("calls=%d slept=%v", h.tr.calls, h.slept)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoProcessing, events.TopicVideoTranscoded}) {
		t.Errorf("topics = %v", got)
	}
}

func TestHandleExhaustsRetries(t *testing.T) {
	boom := errors.New("ffmpeg: boom")
	h := newHarness(t, boom, boom, boom, boom, boom)
	rec := uploadedRecord(t)
	if err := h.w.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if h.tr.calls != 4 {
		t.Errorf("calls = %d, want 4 (1 lần + 3 lần thử lại)", h.tr.calls)
	}
	if !reflect.DeepEqual(h.slept, DefaultBackoffs) {
		t.Errorf("slept = %v", h.slept)
	}
	want := []string{events.TopicVideoProcessing, events.TopicVideoFailed, events.TopicVideoUploadedDLQ}
	if got := h.pub.topics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("topics = %v", got)
	}
	failed := h.pub.got[1].ev.(events.VideoFailed)
	if failed.VideoID != "vid-1" || !strings.Contains(failed.Reason, "ffmpeg: boom") {
		t.Errorf("failed = %+v", failed)
	}
	dl := h.pub.got[2].ev.(events.DeadLetter)
	if dl.VideoID != "vid-1" || dl.Attempts != 4 || string(dl.Payload) != string(rec.Value) ||
		!strings.Contains(dl.Error, "ffmpeg: boom") || dl.FailedAt.IsZero() {
		t.Errorf("dead letter = %+v", dl)
	}
}

func TestHandlePermanentError(t *testing.T) {
	h := newHarness(t, transcode.ErrNoVideo)
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err != nil {
		t.Fatal(err)
	}
	if h.tr.calls != 1 || len(h.slept) != 0 {
		t.Errorf("calls=%d slept=%v, want no retry", h.tr.calls, h.slept)
	}
	if got := h.pub.topics(); len(got) != 3 || got[2] != events.TopicVideoUploadedDLQ {
		t.Errorf("topics = %v", got)
	}
	if dl := h.pub.got[2].ev.(events.DeadLetter); dl.Attempts != 1 {
		t.Errorf("attempts = %d", dl.Attempts)
	}
}

func TestHandleBadMessageGoesToDLQ(t *testing.T) {
	h := newHarness(t)
	rec := &kgo.Record{Topic: events.TopicVideoUploaded, Value: []byte("not json")}
	if err := h.w.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoUploadedDLQ}) {
		t.Fatalf("topics = %v", got)
	}
	dl := h.pub.got[0].ev.(events.DeadLetter)
	var s string
	if dl.Attempts != 0 || json.Unmarshal(dl.Payload, &s) != nil || s != "not json" {
		t.Errorf("dead letter = %+v", dl)
	}
	if h.tr.calls != 0 {
		t.Error("transcoder must not run")
	}
}

func TestHandleCancelledDuringBackoff(t *testing.T) {
	h := newHarness(t, errors.New("boom"))
	h.w.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled (không commit)", err)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoProcessing}) {
		t.Errorf("topics = %v, must not publish failed", got)
	}
}

func TestHandlePublishErrorIsReturned(t *testing.T) {
	h := newHarness(t)
	h.pub.errOn[events.TopicVideoProcessing] = errors.New("broker down")
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err == nil {
		t.Fatal("want error so the consumer retries the record")
	}
	if h.tr.calls != 0 {
		t.Error("transcoder must not run when processing event cannot be published")
	}
}
