package transcode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Runner struct {
	FFmpegPath, FFprobePath string
}

type Result struct {
	Probe      ProbeResult
	Rungs      []Rung
	MasterPath string
	// Ảnh xem trước (M5), đường dẫn tương đối so với outDir. ThumbnailsFile rỗng khi không biết
	// thời lượng (không sinh sprite).
	PosterFile     string
	ThumbnailsFile string
	SpriteSheets   int
}

// Transcode probe file đầu vào, chọn ladder, chạy FFmpeg và ghi HLS vào outDir.
func (r Runner) Transcode(ctx context.Context, input, outDir string) (Result, error) {
	p, err := Probe(ctx, r.FFprobePath, input)
	if err != nil {
		return Result{}, err
	}
	rungs := SelectRungs(DefaultLadder, p.Height)
	for _, rg := range rungs {
		if err := os.MkdirAll(filepath.Join(outDir, rg.Name), 0o755); err != nil {
			return Result{}, err
		}
	}
	args := BuildArgs(Job{
		Input: input, OutDir: outDir, Rungs: rungs, HasAudio: p.HasAudio,
		SegmentSeconds: 6, KeyframeSeconds: 2,
	})
	if err := r.ffmpeg(ctx, "ffmpeg", args); err != nil {
		return Result{}, err
	}
	res := Result{Probe: p, Rungs: rungs, MasterPath: filepath.Join(outDir, "master.m3u8")}
	if err := r.thumbnails(ctx, input, outDir, &res); err != nil {
		return Result{}, err
	}
	return res, nil
}

// thumbnails sinh poster.jpg, các tấm sprite_NNN.jpg và thumbnails.vtt vào outDir (spec §4.1).
// Chạy FFmpeg riêng sau HLS: lệnh HLS giữ nguyên, lỗi ảnh dễ đọc, test được độc lập.
func (r Runner) thumbnails(ctx context.Context, input, outDir string, res *Result) error {
	dur := res.Probe.DurationSec
	if err := r.ffmpeg(ctx, "poster", PosterArgs(input, filepath.Join(outDir, PosterFile), dur)); err != nil {
		return err
	}
	res.PosterFile = PosterFile
	if TileCount(dur) == 0 {
		return nil
	}
	if err := r.ffmpeg(ctx, "sprite", SpriteArgs(input, outDir)); err != nil {
		return err
	}
	sheets, err := filepath.Glob(filepath.Join(outDir, "sprite_*.jpg"))
	if err != nil {
		return err
	}
	if want := (TileCount(dur) + TilesPerSheet - 1) / TilesPerSheet; len(sheets) < want {
		return fmt.Errorf("sprite: got %d sheets, want %d", len(sheets), want)
	}
	if err := os.WriteFile(filepath.Join(outDir, ThumbnailsFile), []byte(ThumbnailsVTT(dur)), 0o644); err != nil {
		return err
	}
	res.ThumbnailsFile, res.SpriteSheets = ThumbnailsFile, len(sheets)
	return nil
}

func (r Runner) ffmpeg(ctx context.Context, step string, args []string) error {
	cmd := exec.CommandContext(ctx, r.FFmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", step, err, tail(stderr.Bytes(), 2000))
	}
	return nil
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
