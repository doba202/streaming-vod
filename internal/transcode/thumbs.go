package transcode

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Poster và sprite xem trước khi tua (spec §4.1). Mọi file nằm cạnh master.m3u8 trong hls/{id}/
// nên đi chung URL ký theo prefix thư mục.
const (
	PosterFile     = "poster.jpg"
	PosterWidth    = 640  // px, chiều cao giữ tỉ lệ
	PosterAt       = 0.10 // poster lấy ở mốc 10% thời lượng
	ThumbnailsFile = "thumbnails.vtt"
	SpriteInterval = 10 // giây giữa hai ảnh
	TileWidth      = 160
	TileHeight     = 90
	SpriteColumns  = 10
	SpriteRows     = 10
	// TilesPerSheet: một tấm sprite 1600x900 chứa 100 ảnh (16 phút 40 giây); video dài hơn có nhiều tấm.
	TilesPerSheet = SpriteColumns * SpriteRows
)

// SpriteName: tên tấm sprite thứ sheet (0-based) — trùng mẫu sprite_%03d.jpg mà FFmpeg ghi (bắt đầu từ 1).
func SpriteName(sheet int) string { return fmt.Sprintf("sprite_%03d.jpg", sheet+1) }

// TileCount: số ảnh sprite cho video dài durationSec giây, một ảnh mỗi SpriteInterval giây (ảnh đầu ở 0s).
func TileCount(durationSec float64) int {
	if durationSec <= 0 {
		return 0
	}
	return int(math.Ceil(durationSec / SpriteInterval))
}

// PosterArgs: một frame ở mốc PosterAt thời lượng, rộng PosterWidth px, JPEG chất lượng cao (-q:v 3).
// -ss trước -i: tua nhanh theo keyframe rồi giải mã chính xác tới mốc.
func PosterArgs(input, out string, durationSec float64) []string {
	at := max(durationSec*PosterAt, 0)
	return []string{"-hide_banner", "-y", "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", input,
		"-frames:v", "1", "-vf", fmt.Sprintf("scale=%d:-2", PosterWidth), "-q:v", "3", out}
}

// SpriteArgs: chọn frame đầu và sau đó cứ cách ít nhất SpriteInterval giây một frame (select, không phụ
// thuộc fps), thu về TileWidth x TileHeight (giữ tỉ lệ, viền đen), ghép lưới SpriteColumns x SpriteRows.
// Mỗi 100 ảnh FFmpeg ghi một tấm sprite_001.jpg, sprite_002.jpg, ...; tấm cuối thiếu ô thì ô trống màu đen.
func SpriteArgs(input, outDir string) []string {
	vf := fmt.Sprintf("select='isnan(prev_selected_t)+gte(t-prev_selected_t,%d)',"+
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,tile=%dx%d",
		SpriteInterval, TileWidth, TileHeight, TileWidth, TileHeight, SpriteColumns, SpriteRows)
	return []string{"-hide_banner", "-y", "-i", input, "-an", "-sn", "-vf", vf,
		"-fps_mode", "passthrough", "-q:v", "5", filepath.Join(outDir, "sprite_%03d.jpg")}
}

// ThumbnailsVTT sinh WebVTT ánh xạ từng khoảng SpriteInterval giây tới một ô sprite bằng fragment
// #xywh=x,y,w,h (cách JW Player/Video.js/Vidstack dùng). Đường dẫn sprite là tương đối nên player
// resolve theo URL của chính thumbnails.vtt, tức là vẫn nằm dưới prefix /cdn/s/{sig}/{exp}/hls/{id}/.
func ThumbnailsVTT(durationSec float64) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for i := range TileCount(durationSec) {
		start := float64(i * SpriteInterval)
		end := min(float64((i+1)*SpriteInterval), durationSec)
		idx := i % TilesPerSheet
		x, y := idx%SpriteColumns*TileWidth, idx/SpriteColumns*TileHeight
		fmt.Fprintf(&b, "\n%s --> %s\n%s#xywh=%d,%d,%d,%d\n", vttTime(start), vttTime(end),
			SpriteName(i/TilesPerSheet), x, y, TileWidth, TileHeight)
	}
	return b.String()
}

// vttTime: 75.5 → "00:01:15.500".
func vttTime(sec float64) string {
	ms := int64(math.Round(sec * 1000))
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
