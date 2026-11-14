package transcode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

	// Ảnh xem trước: poster rộng 640, một tấm sprite 1600x900 (8 giây = 1 ô), thumbnails.vtt 1 cue.
	if res.PosterFile != PosterFile || res.ThumbnailsFile != ThumbnailsFile || res.SpriteSheets != 1 {
		t.Errorf("thumbs = %q %q %d", res.PosterFile, res.ThumbnailsFile, res.SpriteSheets)
	}
	if w, h := imageSize(t, filepath.Join(out, PosterFile)); w != 640 || h != 360 {
		t.Errorf("poster = %dx%d, want 640x360", w, h)
	}
	if w, h := imageSize(t, filepath.Join(out, "sprite_001.jpg")); w != 1600 || h != 900 {
		t.Errorf("sprite = %dx%d, want 1600x900", w, h)
	}
	vtt, err := os.ReadFile(filepath.Join(out, ThumbnailsFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(vtt) != ThumbnailsVTT(res.Probe.DurationSec) || !strings.Contains(string(vtt), "sprite_001.jpg#xywh=0,0,160,90") {
		t.Errorf("thumbnails.vtt:\n%s", vtt)
	}
}

// TestSpriteFrameSelection: bộ lọc select của SpriteArgs chọn đúng TileCount frame (khớp số cue trong
// thumbnails.vtt). Bỏ tile để đếm từng frame: video 25 giây → frame 0s, 10s, 20s.
func TestSpriteFrameSelection(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := t.TempDir()
	in := testutil.MakeSampleVideo(t, dir, 360, 25, false)
	args := SpriteArgs(in, dir)
	i := slices.Index(args, "-vf")
	args[i+1] = strings.Replace(args[i+1], fmt.Sprintf("tile=%dx%d", SpriteColumns, SpriteRows), "null", 1)
	args[len(args)-1] = filepath.Join(dir, "frame_%03d.jpg")
	if err := (Runner{FFmpegPath: "ffmpeg"}).ffmpeg(context.Background(), "sprite", args); err != nil {
		t.Fatal(err)
	}
	frames, _ := filepath.Glob(filepath.Join(dir, "frame_*.jpg"))
	if len(frames) != TileCount(25) {
		t.Errorf("frames = %d, want %d", len(frames), TileCount(25))
	}
}

// imageSize đọc kích thước ảnh bằng ffprobe.
func imageSize(t *testing.T, path string) (int, int) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d,%d", &w, &h); err != nil {
		t.Fatalf("ffprobe %s: %q", path, out)
	}
	return w, h
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
