package transcode

import (
	"reflect"
	"testing"
)

func names(rs []Rung) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func TestSelectRungs(t *testing.T) {
	cases := []struct {
		src  int
		want []string
	}{
		{2160, []string{"1080p", "720p", "480p", "360p"}},
		{1080, []string{"1080p", "720p", "480p", "360p"}},
		{720, []string{"720p", "480p", "360p"}},
		{544, []string{"480p", "360p"}},
		{360, []string{"360p"}},
		{240, []string{"360p"}}, // nguồn nhỏ hơn mọi bậc: giữ bậc thấp nhất
	}
	for _, c := range cases {
		got := names(SelectRungs(DefaultLadder, c.src))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("src=%d: got %v want %v", c.src, got, c.want)
		}
	}
}

func TestDefaultLadderBitrates(t *testing.T) {
	want := map[string]int{"1080p": 5000, "720p": 2800, "480p": 1400, "360p": 800}
	for _, r := range DefaultLadder {
		if want[r.Name] != r.VideoKbps {
			t.Errorf("%s: %d kbps, want %d", r.Name, r.VideoKbps, want[r.Name])
		}
	}
}
