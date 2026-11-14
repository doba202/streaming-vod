package upload

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
	"streamfilm/internal/storage"
)

type fakeVideos struct {
	mu sync.Mutex
	m  map[string]catalog.Video
}

func newFakeVideos() *fakeVideos { return &fakeVideos{m: map[string]catalog.Video{}} }

func (f *fakeVideos) Create(_ context.Context, v catalog.Video) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v.Status = catalog.StatusUploading
	f.m[v.ID] = v
	return nil
}

func (f *fakeVideos) Get(_ context.Context, id string) (catalog.Video, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[id]
	if !ok {
		return catalog.Video{}, catalog.ErrNotFound
	}
	return v, nil
}

func (f *fakeVideos) Transition(_ context.Context, id string, to catalog.Status) (catalog.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[id]
	if !ok {
		return "", catalog.ErrNotFound
	}
	from := v.Status
	if !catalog.CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", catalog.ErrInvalidTransition, from, to)
	}
	v.Status = to
	f.m[id] = v
	return from, nil
}

func (f *fakeVideos) RevertQueued(_ context.Context, id string, to catalog.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[id]; ok && v.Status == catalog.StatusQueued {
		v.Status = to
		f.m[id] = v
	}
	return nil
}

func (f *fakeVideos) setStatus(id string, s catalog.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.m[id]
	v.Status = s
	f.m[id] = v
}

func (f *fakeVideos) status(id string) catalog.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[id].Status
}

type fakeObjects struct {
	parts         int // số part client đã upload
	exists        bool
	completeCalls int
	created       []string
}

func (f *fakeObjects) CreateMultipartUpload(_ context.Context, bucket, key, contentType string) (string, error) {
	f.created = append(f.created, bucket+"/"+key+"|"+contentType)
	return "up-1", nil
}

func (f *fakeObjects) PresignUploadPart(_ context.Context, bucket, key, uploadID string, n int32, ttl time.Duration) (string, error) {
	return fmt.Sprintf("http://s3.test/%s/%s?uploadId=%s&partNumber=%d&ttl=%s", bucket, key, uploadID, n, ttl), nil
}

func (f *fakeObjects) CompleteMultipartUpload(_ context.Context, bucket, key, uploadID string, want int) error {
	f.completeCalls++
	if f.parts != want {
		return fmt.Errorf("%w: got %d parts, want %d", storage.ErrIncompleteUpload, f.parts, want)
	}
	f.exists = true
	return nil
}

func (f *fakeObjects) Exists(context.Context, string, string) (bool, error) { return f.exists, nil }

type published struct {
	topic string
	ev    events.Event
}

type fakePub struct {
	err error
	got []published
}

func (f *fakePub) Publish(_ context.Context, topic string, ev events.Event) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, published{topic, ev})
	return nil
}

var testNow = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

func newService() (*Service, *fakeVideos, *fakeObjects, *fakePub) {
	vids, objs, pub := newFakeVideos(), &fakeObjects{}, &fakePub{}
	n := 0
	svc := &Service{Videos: vids, Objects: objs, Pub: pub, BucketRaw: "raw",
		Now: func() time.Time { return testNow },
		NewID: func() string {
			n++
			return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
		}}
	return svc, vids, objs, pub
}

const firstID = "00000000-0000-0000-0000-000000000001"

func TestPartCount(t *testing.T) {
	cases := map[int64]int{1: 1, PartSize: 1, PartSize + 1: 2, MaxSizeBytes: 160}
	for size, want := range cases {
		if got := PartCount(size); got != want {
			t.Errorf("PartCount(%d) = %d, want %d", size, got, want)
		}
	}
}

func TestCreateValidates(t *testing.T) {
	svc, _, _, _ := newService()
	cases := map[string]CreateInput{
		"no title":  {Title: "  ", Filename: "a.mp4", SizeBytes: 10},
		"zero size": {Title: "A", Filename: "a.mp4"},
		"too big":   {Title: "A", Filename: "a.mp4", SizeBytes: MaxSizeBytes + 1},
		"bad ext":   {Title: "A", Filename: "a.avi", SizeBytes: 10},
	}
	for name, in := range cases {
		if _, err := svc.Create(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestCreateReturnsPresignedParts(t *testing.T) {
	svc, vids, objs, _ := newService()
	out, err := svc.Create(context.Background(), CreateInput{Title: " Phim A ", Filename: "Movie.MP4",
		Genres: []string{"drama"}, Year: 2024, SizeBytes: 2*PartSize + 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != firstID || out.Status != catalog.StatusUploading {
		t.Errorf("out = %+v", out)
	}
	up := out.Upload
	if up.UploadID != "up-1" || up.PartSize != PartSize || len(up.Parts) != 3 ||
		up.Parts[2].PartNumber != 3 || !up.ExpiresAt.Equal(testNow.Add(time.Hour)) {
		t.Errorf("upload plan = %+v", up)
	}
	if !strings.Contains(up.Parts[0].URL, "raw/"+firstID+"/source.mp4") || !strings.Contains(up.Parts[0].URL, "ttl=1h0m0s") {
		t.Errorf("part url = %s", up.Parts[0].URL)
	}
	if len(objs.created) != 1 || objs.created[0] != "raw/"+firstID+"/source.mp4|video/mp4" {
		t.Errorf("created = %v", objs.created)
	}
	v := vids.m[firstID]
	if v.Title != "Phim A" || v.RawKey != firstID+"/source.mp4" || v.UploadID != "up-1" ||
		v.SizeBytes != 2*PartSize+1 || v.Year != 2024 {
		t.Errorf("stored video = %+v", v)
	}
}

func createOne(t *testing.T, svc *Service) string {
	t.Helper()
	out, err := svc.Create(context.Background(), CreateInput{Title: "A", Filename: "a.mp4", SizeBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func TestCompleteQueuesAndPublishes(t *testing.T) {
	svc, vids, objs, pub := newService()
	id := createOne(t, svc)
	objs.parts = 1
	v, err := svc.Complete(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != catalog.StatusQueued || vids.status(id) != catalog.StatusQueued {
		t.Errorf("status = %s / %s", v.Status, vids.status(id))
	}
	if len(pub.got) != 1 || pub.got[0].topic != events.TopicVideoUploaded {
		t.Fatalf("published = %+v", pub.got)
	}
	if ev := pub.got[0].ev.(events.VideoUploaded); ev.VideoID != id || ev.RawKey != id+"/source.mp4" {
		t.Errorf("event = %+v", ev)
	}
}

func TestCompleteIncompleteUpload(t *testing.T) {
	svc, vids, _, pub := newService()
	id := createOne(t, svc)
	if _, err := svc.Complete(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
	if vids.status(id) != catalog.StatusUploading || len(pub.got) != 0 {
		t.Errorf("status = %s, published = %d", vids.status(id), len(pub.got))
	}
}

func TestCompleteKafkaDownThenRetry(t *testing.T) {
	svc, vids, objs, pub := newService()
	id := createOne(t, svc)
	objs.parts = 1
	pub.err = errors.New("broker down")
	if _, err := svc.Complete(context.Background(), id); !errors.Is(err, ErrKafkaUnavailable) {
		t.Fatalf("err = %v, want ErrKafkaUnavailable", err)
	}
	if vids.status(id) != catalog.StatusUploading {
		t.Errorf("status = %s, want uploading after failed publish", vids.status(id))
	}
	pub.err = nil
	if _, err := svc.Complete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if objs.completeCalls != 1 {
		t.Errorf("CompleteMultipartUpload called %d times, want 1 (object đã tồn tại)", objs.completeCalls)
	}
	if vids.status(id) != catalog.StatusQueued || len(pub.got) != 1 {
		t.Errorf("status = %s, published = %d", vids.status(id), len(pub.got))
	}
}

func TestCompleteWrongState(t *testing.T) {
	svc, _, objs, _ := newService()
	id := createOne(t, svc)
	objs.parts = 1
	if _, err := svc.Complete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Complete(context.Background(), id); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Errorf("second complete: err = %v", err)
	}
	if _, err := svc.Complete(context.Background(), "00000000-0000-0000-0000-000000000099"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
}

func TestRetry(t *testing.T) {
	svc, vids, _, pub := newService()
	id := createOne(t, svc)
	if _, err := svc.Retry(context.Background(), id); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Errorf("retry uploading: err = %v", err)
	}
	vids.setStatus(id, catalog.StatusFailed)

	pub.err = errors.New("broker down")
	if _, err := svc.Retry(context.Background(), id); !errors.Is(err, ErrKafkaUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if vids.status(id) != catalog.StatusFailed {
		t.Errorf("status = %s, want failed", vids.status(id))
	}

	pub.err = nil
	v, err := svc.Retry(context.Background(), id)
	if err != nil || v.Status != catalog.StatusQueued {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	if len(pub.got) != 1 || pub.got[0].topic != events.TopicVideoUploaded {
		t.Errorf("published = %+v", pub.got)
	}
}
