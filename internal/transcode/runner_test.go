package transcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamfilm/internal/testutil"
)

func TestRunnerTranscode(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := t.TempDir()
	in := testutil.MakeSampleVideo(t, dir, 720, 8, true)
	out := filepath.Join(dir, "hls")

	res, err := Runner{FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"}.Transcode(context.Background(), in, out)
	if err != nil {
		t.Fatal(err)
	}
	if res.Probe.Height != 720 || !res.Probe.HasAudio {
		t.Errorf("probe = %+v", res.Probe)
	}
	if got := names(res.Rungs); strings.Join(got, ",") != "720p,480p,360p" {
		t.Errorf("rungs = %v", got)
	}

	master, err := os.ReadFile(res.MasterPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"720p/index.m3u8", "480p/index.m3u8", "360p/index.m3u8"} {
		if !strings.Contains(string(master), want) {
			t.Errorf("master missing %s:\n%s", want, master)
		}
	}
	if strings.Contains(string(master), "1080p") {
		t.Error("master should not contain 1080p for 720p source")
	}
	for _, r := range []string{"720p", "480p", "360p"} {
		segs, _ := filepath.Glob(filepath.Join(out, r, "seg_*.ts"))
		if len(segs) < 2 { // 8 giây / segment 6 giây = 2 segment
			t.Errorf("%s: %d segments", r, len(segs))
		}
	}
}

func TestRunnerRejectsNonVideo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.mp4")
	os.WriteFile(bad, []byte("not a video"), 0o644)
	_, err := Runner{FFmpegPath: "ffmpeg", FFprobePath: "ffprobe"}.Transcode(context.Background(), bad, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected error for non-video input")
	}
}
