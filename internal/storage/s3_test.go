package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"
)

func TestContentTypeFor(t *testing.T) {
	cases := map[string]string{
		"master.m3u8": "application/vnd.apple.mpegurl",
		"seg_000.ts":  "video/mp2t",
		"vi.vtt":      "text/vtt",
		"poster.jpg":  "image/jpeg",
		"x.bin":       "application/octet-stream",
	}
	for name, want := range cases {
		if got := ContentTypeFor(name); got != want {
			t.Errorf("%s: %q want %q", name, got, want)
		}
	}
}

func TestUploadDirAndDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcminio.Run(ctx, "cgr.dev/chainguard/minio:latest-dev@sha256:5735e8125c4af7dd310dc5780508bfb850f0b68783a1a56089b5c64316a8f603")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })
	addr, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}

	s := New(Config{Endpoint: "http://" + addr, Region: "us-east-1", AccessKey: ctr.Username, SecretKey: ctr.Password})
	if err := s.EnsureBucket(ctx, "hls"); err != nil {
		t.Fatal(err)
	}

	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "720p"), 0o755)
	os.WriteFile(filepath.Join(src, "master.m3u8"), []byte("#EXTM3U\n"), 0o644)
	os.WriteFile(filepath.Join(src, "720p", "seg_000.ts"), []byte("segment"), 0o644)

	n, err := s.UploadDir(ctx, "hls", "vid1", src)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("uploaded %d files, want 2", n)
	}

	dest := filepath.Join(t.TempDir(), "seg.ts")
	if err := s.Download(ctx, "hls", "vid1/720p/seg_000.ts", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "segment" {
		t.Errorf("downloaded %q", b)
	}
}
