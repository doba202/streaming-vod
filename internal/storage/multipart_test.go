package storage

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"streamfilm/internal/testutil"
)

func TestPresignUsesPublicEndpoint(t *testing.T) {
	s := New(Config{Endpoint: "http://minio:9000", PublicEndpoint: "http://localhost:9000",
		Region: "us-east-1", AccessKey: "k", SecretKey: "s"})
	u, err := s.PresignUploadPart(context.Background(), "raw", "vid/source.mp4", "up1", 2, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://localhost:9000/raw/vid/source.mp4?") {
		t.Errorf("url = %s", u)
	}
	for _, want := range []string{"partNumber=2", "uploadId=up1", "X-Amz-Expires=3600"} {
		if !strings.Contains(u, want) {
			t.Errorf("url thiếu %q: %s", want, u)
		}
	}
}

func putPart(t *testing.T, url string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT part: HTTP %d", resp.StatusCode)
	}
}

func TestMultipartUpload(t *testing.T) {
	m := testutil.StartMinIO(t)
	s := New(Config{Endpoint: m.Endpoint, Region: "us-east-1", AccessKey: m.User, SecretKey: m.Password})
	ctx := context.Background()
	if err := s.EnsureBucket(ctx, "raw"); err != nil {
		t.Fatal(err)
	}
	key := "vid1/source.mp4"
	uploadID, err := s.CreateMultipartUpload(ctx, "raw", key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	part1 := bytes.Repeat([]byte("a"), 5<<20) // mọi part trừ part cuối phải >= 5 MiB
	part2 := []byte("tail")
	for i, body := range [][]byte{part1, part2} {
		u, err := s.PresignUploadPart(ctx, "raw", key, uploadID, int32(i+1), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		putPart(t, u, body)
	}

	if ok, err := s.Exists(ctx, "raw", key); err != nil || ok {
		t.Fatalf("before complete: exists=%v err=%v", ok, err)
	}
	total := int64(len(part1) + len(part2))
	const maxSize = 10 << 30
	partSize := int64(len(part1))
	for name, c := range map[string]struct {
		parts            int
		size, part, maxS int64
	}{
		"wrong part count": {3, total, partSize, maxSize},
		"size mismatch":    {2, total + 1, partSize, maxSize},
		"part size":        {2, total, partSize + 1, maxSize},
		"over max":         {2, total, partSize, total - 1},
	} {
		if err := s.CompleteMultipartUpload(ctx, "raw", key, uploadID, c.parts, c.size, c.part, c.maxS); !errors.Is(err, ErrIncompleteUpload) {
			t.Fatalf("%s: want ErrIncompleteUpload, got %v", name, err)
		}
	}
	if ok, _ := s.Exists(ctx, "raw", key); ok {
		t.Fatal("object must not exist after rejected completes")
	}
	if err := s.CompleteMultipartUpload(ctx, "raw", key, uploadID, 2, total, partSize, maxSize); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Exists(ctx, "raw", key); err != nil || !ok {
		t.Fatalf("after complete: exists=%v err=%v", ok, err)
	}
	dest := filepath.Join(t.TempDir(), "out.mp4")
	if err := s.Download(ctx, "raw", key, dest); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dest); fi.Size() != int64(len(part1)+len(part2)) {
		t.Errorf("size = %d", fi.Size())
	}

	other, err := s.CreateMultipartUpload(ctx, "raw", "vid2/source.mp4", "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipartUpload(ctx, "raw", "vid2/source.mp4", other); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipartUpload(ctx, "raw", "vid2/source.mp4", other); err != nil {
		t.Errorf("abort twice must be nil (NoSuchUpload), got %v", err)
	}

	n, err := s.DeletePrefix(ctx, "raw", "vid1/")
	if err != nil || n != 1 {
		t.Fatalf("delete prefix: n=%d err=%v", n, err)
	}
	if ok, _ := s.Exists(ctx, "raw", key); ok {
		t.Error("object still exists after DeletePrefix")
	}
}
