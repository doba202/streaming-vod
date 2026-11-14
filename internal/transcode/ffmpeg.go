package transcode

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Job mô tả một lần transcode ra HLS.
type Job struct {
	Input, OutDir   string
	Rungs           []Rung
	HasAudio        bool
	SegmentSeconds  int
	KeyframeSeconds int
}

// BuildArgs dựng tham số FFmpeg cho một lần chạy sinh mọi bậc cùng lúc.
// Keyframe ép theo thời gian (không phụ thuộc fps) để mọi bậc có ranh giới
// segment thẳng hàng, giúp player đổi chất lượng không giật.
func BuildArgs(j Job) []string {
	n := len(j.Rungs)
	args := []string{"-hide_banner", "-y", "-i", j.Input}

	var split, scales strings.Builder
	split.WriteString(fmt.Sprintf("[0:v]split=%d", n))
	for i, r := range j.Rungs {
		split.WriteString(fmt.Sprintf("[s%d]", i))
		scales.WriteString(fmt.Sprintf(";[s%d]scale=-2:%d[v%d]", i, r.Height, i))
	}
	args = append(args, "-filter_complex", split.String()+scales.String())

	for i := range j.Rungs {
		args = append(args, "-map", fmt.Sprintf("[v%d]", i))
	}
	if j.HasAudio {
		for range j.Rungs {
			args = append(args, "-map", "0:a:0")
		}
	}

	for i, r := range j.Rungs {
		args = append(args,
			fmt.Sprintf("-c:v:%d", i), "libx264",
			fmt.Sprintf("-b:v:%d", i), fmt.Sprintf("%dk", r.VideoKbps),
			fmt.Sprintf("-maxrate:v:%d", i), fmt.Sprintf("%dk", r.VideoKbps*107/100),
			fmt.Sprintf("-bufsize:v:%d", i), fmt.Sprintf("%dk", r.VideoKbps*3/2),
		)
	}
	args = append(args,
		"-preset", "veryfast",
		"-profile:v", "main",
		"-pix_fmt", "yuv420p",
		"-sc_threshold", "0",
		"-force_key_frames", fmt.Sprintf("expr:gte(t,n_forced*%d)", j.KeyframeSeconds),
	)

	if j.HasAudio {
		args = append(args, "-c:a", "aac", "-ac", "2")
		for i, r := range j.Rungs {
			args = append(args, fmt.Sprintf("-b:a:%d", i), fmt.Sprintf("%dk", r.AudioKbps))
		}
	}

	streams := make([]string, n)
	for i, r := range j.Rungs {
		if j.HasAudio {
			streams[i] = fmt.Sprintf("v:%d,a:%d,name:%s", i, i, r.Name)
		} else {
			streams[i] = fmt.Sprintf("v:%d,name:%s", i, r.Name)
		}
	}

	args = append(args,
		"-f", "hls",
		"-hls_time", fmt.Sprint(j.SegmentSeconds),
		"-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(j.OutDir, "%v", "seg_%03d.ts"),
		"-master_pl_name", "master.m3u8",
		"-var_stream_map", strings.Join(streams, " "),
		filepath.Join(j.OutDir, "%v", "index.m3u8"),
	)
	return args
}
