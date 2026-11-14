package transcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
)

var ErrNoVideo = errors.New("input has no video stream")

type ProbeResult struct {
	Width, Height int
	DurationSec   float64
	HasAudio      bool
}

type probeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func ParseProbe(data []byte) (ProbeResult, error) {
	var out probeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return ProbeResult{}, fmt.Errorf("parse ffprobe json: %w", err)
	}
	var r ProbeResult
	hasVideo := false
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			if !hasVideo {
				r.Width, r.Height = s.Width, s.Height
				hasVideo = true
			}
		case "audio":
			r.HasAudio = true
		}
	}
	if !hasVideo {
		return ProbeResult{}, ErrNoVideo
	}
	if out.Format.Duration != "" {
		d, err := strconv.ParseFloat(out.Format.Duration, 64)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("parse duration %q: %w", out.Format.Duration, err)
		}
		r.DurationSec = d
	}
	return r, nil
}

func Probe(ctx context.Context, ffprobePath, input string) (ProbeResult, error) {
	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "error", "-print_format", "json", "-show_streams", "-show_format", input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return ProbeResult{}, fmt.Errorf("ffprobe: %w: %s", err, stderr.String())
	}
	return ParseProbe(data)
}
