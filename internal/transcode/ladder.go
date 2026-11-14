// Package transcode biến một file video thành HLS nhiều độ phân giải.
package transcode

// Rung là một bậc chất lượng trong encoding ladder.
type Rung struct {
	Name      string // tên thư mục con, ví dụ "720p"
	Height    int
	VideoKbps int
	AudioKbps int
}

// DefaultLadder sắp theo chiều cao giảm dần.
var DefaultLadder = []Rung{
	{Name: "1080p", Height: 1080, VideoKbps: 5000, AudioKbps: 128},
	{Name: "720p", Height: 720, VideoKbps: 2800, AudioKbps: 128},
	{Name: "480p", Height: 480, VideoKbps: 1400, AudioKbps: 96},
	{Name: "360p", Height: 360, VideoKbps: 800, AudioKbps: 96},
}

// SelectRungs trả về các bậc không cao hơn nguồn (không upscale).
// Nếu nguồn thấp hơn mọi bậc thì giữ bậc thấp nhất.
func SelectRungs(ladder []Rung, sourceHeight int) []Rung {
	var out []Rung
	for _, r := range ladder {
		if r.Height <= sourceHeight {
			out = append(out, r)
		}
	}
	if len(out) == 0 && len(ladder) > 0 {
		out = []Rung{ladder[len(ladder)-1]}
	}
	return out
}
