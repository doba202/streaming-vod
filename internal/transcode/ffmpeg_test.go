package transcode

import (
	"path/filepath"
	"strings"
	"testing"
)

// hasSeq kiểm tra args chứa đúng chuỗi liên tiếp seq.
func hasSeq(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		ok := true
		for j := range seq {
			if args[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func testJob(audio bool) Job {
	return Job{
		Input: "in.mp4", OutDir: "/out",
		Rungs:          SelectRungs(DefaultLadder, 720),
		HasAudio:       audio,
		SegmentSeconds: 6, KeyframeSeconds: 2,
	}
}

func TestBuildArgsWithAudio(t *testing.T) {
	a := BuildArgs(testJob(true))
	checks := [][]string{
		{"-i", "in.mp4"},
		{"-filter_complex", "[0:v]split=3[s0][s1][s2];[s0]scale=-2:720[v0];[s1]scale=-2:480[v1];[s2]scale=-2:360[v2]"},
		{"-map", "[v0]"}, {"-map", "[v2]"}, {"-map", "0:a:0"},
		{"-b:v:0", "2800k"}, {"-b:v:2", "800k"},
		{"-force_key_frames", "expr:gte(t,n_forced*2)"},
		{"-sc_threshold", "0"},
		{"-c:a", "aac"}, {"-b:a:1", "96k"},
		{"-hls_time", "6"}, {"-hls_playlist_type", "vod"},
		{"-master_pl_name", "master.m3u8"},
		{"-var_stream_map", "v:0,a:0,name:720p v:1,a:1,name:480p v:2,a:2,name:360p"},
		{"-hls_segment_filename", filepath.Join("/out", "%v", "seg_%03d.ts")},
	}
	for _, c := range checks {
		if !hasSeq(a, c...) {
			t.Errorf("missing %v in %s", c, strings.Join(a, " "))
		}
	}
	if a[len(a)-1] != filepath.Join("/out", "%v", "index.m3u8") {
		t.Errorf("last arg = %q", a[len(a)-1])
	}
}

func TestBuildArgsWithoutAudio(t *testing.T) {
	a := BuildArgs(testJob(false))
	if hasSeq(a, "-map", "0:a:0") || hasSeq(a, "-c:a", "aac") {
		t.Errorf("audio args present without audio: %v", a)
	}
	if !hasSeq(a, "-var_stream_map", "v:0,name:720p v:1,name:480p v:2,name:360p") {
		t.Errorf("bad var_stream_map: %v", a)
	}
}
