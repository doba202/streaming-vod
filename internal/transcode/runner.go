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
	cmd := exec.CommandContext(ctx, r.FFmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("ffmpeg: %w: %s", err, tail(stderr.Bytes(), 2000))
	}
	return Result{Probe: p, Rungs: rungs, MasterPath: filepath.Join(outDir, "master.m3u8")}, nil
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
