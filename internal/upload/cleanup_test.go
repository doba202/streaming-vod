package upload

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"streamfilm/internal/catalog"
)

type fakeStale struct {
	videos  []catalog.Video
	before  time.Time
	gone    map[string]bool
	deleted []string
}

func (f *fakeStale) ListStaleUploading(_ context.Context, before time.Time) ([]catalog.Video, error) {
	f.before = before
	return f.videos, nil
}

func (f *fakeStale) DeleteUploading(_ context.Context, id string) error {
	if f.gone[id] {
		return catalog.ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeStaleObjects struct {
	aborted, prefixes []string
}

func (f *fakeStaleObjects) AbortMultipartUpload(_ context.Context, bucket, key, uploadID string) error {
	f.aborted = append(f.aborted, bucket+"/"+key+"#"+uploadID)
	return nil
}

func (f *fakeStaleObjects) DeletePrefix(_ context.Context, bucket, prefix string) (int, error) {
	f.prefixes = append(f.prefixes, bucket+"/"+prefix)
	return 1, nil
}

func TestCleanerRunOnce(t *testing.T) {
	vids := &fakeStale{
		videos: []catalog.Video{
			{ID: "v1", RawKey: "v1/source.mp4", UploadID: "up-1"},
			{ID: "v2", RawKey: "v2/source.mp4", UploadID: "up-2"}, // vừa được complete: không còn uploading
		},
		gone: map[string]bool{"v2": true},
	}
	objs := &fakeStaleObjects{}
	c := &Cleaner{Videos: vids, Objects: objs, BucketRaw: "raw", MaxAge: 24 * time.Hour,
		Now: func() time.Time { return testNow }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	n, err := c.RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if !vids.before.Equal(testNow.Add(-24 * time.Hour)) {
		t.Errorf("cutoff = %v", vids.before)
	}
	if !reflect.DeepEqual(vids.deleted, []string{"v1"}) ||
		!reflect.DeepEqual(objs.aborted, []string{"raw/v1/source.mp4#up-1"}) ||
		!reflect.DeepEqual(objs.prefixes, []string{"raw/v1/"}) {
		t.Errorf("deleted=%v aborted=%v prefixes=%v", vids.deleted, objs.aborted, objs.prefixes)
	}
}
