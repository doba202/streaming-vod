// Package testutil chứa helper dùng chung cho test.
package testutil

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// MakeSampleVideo sinh video H.264 tổng hợp bằng FFmpeg; skip test nếu thiếu ffmpeg.
func MakeSampleVideo(t testing.TB, dir string, height, seconds int, audio bool) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	out := filepath.Join(dir, "sample.mp4")
	args := []string{"-hide_banner", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height)}
	if audio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000")
	}
	args = append(args, "-t", fmt.Sprint(seconds), "-c:v", "libx264", "-pix_fmt", "yuv420p")
	if audio {
		args = append(args, "-c:a", "aac")
	}
	args = append(args, out)
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("make sample: %v\n%s", err, b)
	}
	return out
}
