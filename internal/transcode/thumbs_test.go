package transcode

import (
	"slices"
	"strings"
	"testing"
)

func TestTileCount(t *testing.T) {
	for _, c := range []struct {
		dur  float64
		want int
	}{{0, 0}, {-1, 0}, {0.5, 1}, {10, 1}, {10.01, 2}, {30, 3}, {29.6, 3}, {1000, 100}, {1001, 101}} {
		if got := TileCount(c.dur); got != c.want {
			t.Errorf("TileCount(%v) = %d, want %d", c.dur, got, c.want)
		}
	}
}

func TestPosterArgs(t *testing.T) {
	args := PosterArgs("/in/source.mp4", "/out/poster.jpg", 30)
	if !hasSeq(args, "-ss", "3.000", "-i", "/in/source.mp4") {
		t.Errorf("poster phải tua tới 10%% thời lượng trước -i: %v", args)
	}
	if !hasSeq(args, "-frames:v", "1", "-vf", "scale=640:-2") || args[len(args)-1] != "/out/poster.jpg" {
		t.Errorf("args = %v", args)
	}
	if got := PosterArgs("in", "out", 0); !hasSeq(got, "-ss", "0.000") {
		t.Errorf("thời lượng 0: %v", got)
	}
}

func TestSpriteArgs(t *testing.T) {
	args := SpriteArgs("/in/source.mp4", "/out")
	i := slices.Index(args, "-vf")
	if i < 0 {
		t.Fatalf("thiếu -vf: %v", args)
	}
	vf := args[i+1]
	for _, want := range []string{"gte(t-prev_selected_t,10)", "scale=160:90:force_original_aspect_ratio=decrease",
		"pad=160:90", "tile=10x10"} {
		if !strings.Contains(vf, want) {
			t.Errorf("-vf thiếu %q: %s", want, vf)
		}
	}
	if !hasSeq(args, "-fps_mode", "passthrough") || args[len(args)-1] != "/out/sprite_%03d.jpg" {
		t.Errorf("args = %v", args)
	}
}

func TestThumbnailsVTT(t *testing.T) {
	want := "WEBVTT\n" +
		"\n00:00:00.000 --> 00:00:10.000\nsprite_001.jpg#xywh=0,0,160,90\n" +
		"\n00:00:10.000 --> 00:00:20.000\nsprite_001.jpg#xywh=160,0,160,90\n" +
		"\n00:00:20.000 --> 00:00:25.500\nsprite_001.jpg#xywh=320,0,160,90\n"
	if got := ThumbnailsVTT(25.5); got != want {
		t.Errorf("ThumbnailsVTT(25.5) =\n%s\nwant\n%s", got, want)
	}
	if got := ThumbnailsVTT(0); got != "WEBVTT\n" {
		t.Errorf("ThumbnailsVTT(0) = %q", got)
	}
}

func TestThumbnailsVTTSheets(t *testing.T) {
	// 1 giờ 5 giây = 361 ô: tấm 1-3 đầy (300 ô), tấm 4 có 61 ô.
	vtt := ThumbnailsVTT(3605)
	for _, want := range []string{
		"\n00:16:30.000 --> 00:16:40.000\nsprite_001.jpg#xywh=1440,810,160,90\n", // ô 99: góc dưới phải tấm 1
		"\n00:16:40.000 --> 00:16:50.000\nsprite_002.jpg#xywh=0,0,160,90\n",      // ô 100: đầu tấm 2
		"\n00:58:20.000 --> 00:58:30.000\nsprite_004.jpg#xywh=0,450,160,90\n",    // ô 350: tấm 4, ô 50
		"\n01:00:00.000 --> 01:00:05.000\nsprite_004.jpg#xywh=0,540,160,90\n",    // ô cuối 360: tấm 4, ô 60
	} {
		if !strings.Contains(vtt, want) {
			t.Errorf("thiếu cue %q", want)
		}
	}
	if n := strings.Count(vtt, " --> "); n != 361 {
		t.Errorf("số cue = %d, want 361", n)
	}
}
