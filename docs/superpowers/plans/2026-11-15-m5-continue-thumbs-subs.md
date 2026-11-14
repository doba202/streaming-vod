# Streamfilm M5: Xem tiếp, thumbnail, phụ đề — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transcoder sinh poster (frame ở 10% thời lượng) và sprite xem trước (một ảnh mỗi 10 giây, lưới 10×10, kèm `thumbnails.vtt`), `video.transcoded` mang `poster_key`/`sprite_key` và api lưu lại. Catalog trả `poster_url` ký; `/play` trả `subtitles` (URL ký), `thumbnails_url`, `resume_at` thật. Admin upload phụ đề `.srt`/`.vtt` (SRT → VTT). Player gửi sự kiện xem (`POST /api/playback/events`) → `watch_progress` + `playback.events` trên Kafka (best-effort, Kafka chết không ảnh hưởng việc xem); trang chủ có hàng "Xem tiếp"; player có `<track>` phụ đề và thanh tua có ảnh xem trước. Kiểm tra bằng test Go (gồm FFmpeg, Postgres, MinIO, nginx thật), Vitest và `make e2e-m5`.

**Architecture:** `internal/transcode` thêm `thumbs.go` (hàm thuần dựng lệnh FFmpeg poster/sprite và sinh `thumbnails.vtt`), `Runner.Transcode` chạy thêm hai lệnh FFmpeg sau HLS; `worker.Transcoded` điền `poster_key`/`sprite_key`. Migration `0004_subtitles_progress.sql` (bảng `subtitles`, `watch_progress`, index `(user_id, updated_at DESC)`; cột `videos.poster_key/sprite_key` có từ `0001`). Package mới `internal/subtitle` (SRT → WebVTT, chuẩn hoá VTT) và `internal/playback` (validate sự kiện, `Repo` cho `watch_progress`, `BestEffort` publish nền có giới hạn, handler `POST /api/playback/events`). `catalog` thêm `SubtitleHandler` (admin upload), `ProgressSource` (catalog khai báo, `playback.Repo` thoả), `GET /api/me/continue-watching`, ký `poster_url` bằng `signing.Signer.StablePath` (exp làm tròn theo giờ). nginx không cache `subs/`. Web: `Poster`, `ContinueRow`, `SeekPreview` + `player/thumbnails.ts`, `player/reporter.ts` (`PlaybackReporter`), `<track>` trong `Player`. M5 chỉ **publish** `playback.events`; consumer analytics + ClickHouse là M6.

**Tech Stack:** Go 1.27.4, chi v5.5.0, pgx v5.13.0 (thêm import `pgx/v5/pgconn`, cùng module), franz-go, testcontainers-go v0.45.0, FFmpeg 8.1 (host) / FFmpeg 8 (image transcoder alpine), Postgres 16, nginx `1.27-alpine`, Node `20.19.6`. Web không thêm dependency (React 18.3.1, react-router-dom 7.18.5, hls.js 1.6.5, Vitest 4.1.12, jsdom 26.1.0 như M4).

**Spec:** `docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md` (mục 3.3, 4.1, 4.2, 4.3, 4.4, 5, 7, 9, 10, 11)

## Global Constraints

- Go module path: `streamfilm`. Làm trên nhánh `feat/mvp`. Mọi lệnh `npm` chạy trong `web/app` (`node -v` → `v20.19.6`), lockfile không đổi (không thêm package).
- **Poster:** `poster.jpg`, frame ở `0.10 × duration` (`-ss` trước `-i`), `scale=640:-2`, `-q:v 3`. Key `{id}/poster.jpg`.
- **Sprite:** một ô mỗi `10` giây (`select='isnan(prev_selected_t)+gte(t-prev_selected_t,10)'`), ô `160×90` (`force_original_aspect_ratio=decrease` + `pad`), lưới `10×10` = 100 ô/tấm (1600×900), tên `sprite_001.jpg`, `sprite_002.jpg`…, `-fps_mode passthrough`, `-q:v 5`. Số ô = `ceil(duration/10)`. `thumbnails.vtt`: cue `i` = `[10i, min(10(i+1), duration))`, payload `sprite_{⌊i/100⌋+1:03d}.jpg#xywh={(i%100)%10·160},{⌊(i%100)/10⌋·90},160,90` (đường dẫn tương đối). `sprite_key` = `{id}/thumbnails.vtt`. Thời lượng ≤ 0 → chỉ poster (ở 0s), không sprite.
- **Poster URL:** `Signer.StablePath` — `exp = ⌊(now + 2h) / 3600⌋ × 3600` (hạn còn lại trong (1h, 2h]); mọi URL khác (`master.m3u8`, `subs/…`, `thumbnails.vtt`) dùng `Signer.Path` (`exp = now + 2h`).
- **Phụ đề:** `POST /api/admin/videos/{id}/subtitles` multipart `lang`, `label`, `file`; `lang` khớp `^[a-z]{2,3}(-[A-Z]{2})?$`; `label` trim, 1–50 rune, không ký tự điều khiển; file `.srt`/`.vtt` (không phân biệt hoa thường), UTF-8, ≤ `1 MiB` (`1<<20`) → `413`; body tối đa 1 MiB + 64 KiB. Lưu `hls/{id}/subs/{lang}.vtt` với `Content-Type: text/vtt`, rồi upsert `subtitles` PK `(video_id, lang)`. `201 {"lang","label","vtt_key"}`.
- **Sự kiện xem:** `POST /api/playback/events` body `{"video_id","type","position_s","bitrate_kbps","buffering_ms"}` (không nhận field lạ); `type` ∈ `play|pause|seek|ended|buffering|heartbeat`; `position_s` số thực 0–172800, `bitrate_kbps` 0–100000, `buffering_ms` 0–600000. Video phải `ready` (khác → `404`). `position_s` lưu = `min(⌊position_s⌋, duration_s)`. Chỉ `heartbeat|pause|ended` upsert `watch_progress`. Publish `playback.events` (key `user_id`) qua `BestEffort`: timeout `2s`, tối đa `256` publish đang chờ, đầy/lỗi → log `playback event dropped` và bỏ. Trả `204` (kể cả Kafka chết).
- **Xem tiếp:** `GET /api/me/continue-watching` → `{"videos":[…ViewerVideoJSON, "position_s", "watched_at"]}`, điều kiện `duration_s > 0 AND 0.05·duration_s < position_s < 0.95·duration_s`, `ORDER BY updated_at DESC LIMIT 20`, `Cache-Control: no-store`. `resume_at` = `position_s` đã lưu, `0` nếu chưa có hoặc `position_s ≥ 0.95·duration_s`; lỗi đọc tiến độ → log, `resume_at: 0`, `/play` vẫn `200`.
- **Web:** heartbeat mỗi `10 000 ms` khi đang phát; `pause`/`seek` trước lần `play` đầu bị bỏ; `buffering_ms` = thời gian từ `waiting` đầu tiên tới `playing`; rời trang (`pagehide`, unmount) gửi heartbeat vị trí cuối; gửi bằng `fetch` `keepalive: true`, lỗi/401 bỏ qua. `<video crossOrigin="anonymous">`.
- **nginx:** `map $sl_file $sl_no_cache { ~^subs/ 1; default 0; }` + `proxy_no_cache`/`proxy_cache_bypass` trong location `/cdn/s/…`. Còn lại không đổi.
- Integration test tự `t.Skip` khi `-short` hoặc thiếu Docker/FFmpeg (như M2–M4). Tài liệu tiếng Việt, chỉ mô tả những gì đã chạy (spec §11).

## Quyết định thiết kế chính (ghi ADR ở Task 12)

| Quyết định | Lý do |
|---|---|
| Sprite nhiều tấm 10×10 (100 ô = 16 phút 40 giây/tấm), không giãn khoảng cách theo độ dài phim | Một tấm cho phim 2 giờ là ảnh > 4800 px (nặng, vượt giới hạn texture di động); giãn khoảng cách làm phim dài xem trước rất thưa. FFmpeg `tile` tự ghi tấm cuối thiếu ô (đã thử với FFmpeg 8.1) |
| `select` theo thời gian thay cho `fps=1/10` | `fps` có thể nhân bản frame cuối làm số ô lệch với `ceil(duration/10)`; test FFmpeg kiểm tra số frame đúng bằng `TileCount` |
| `thumbnails.vtt` do Go sinh từ thời lượng, đường dẫn sprite tương đối | Hàm thuần test được; tương đối nên resolve dưới prefix ký `/cdn/s/{sig}/{exp}/hls/{id}/` |
| `sprite_key` = `{id}/thumbnails.vtt` | Có nhiều tấm nên "key của sprite" là chỉ mục của chúng; `/play` ký nó thành `thumbnails_url` |
| Poster/sprite chạy FFmpeg riêng sau HLS; lỗi ảnh = lỗi job (retry như HLS) | Lệnh HLS giữ nguyên; lỗi rõ ràng; test độc lập |
| `poster_url` ký bằng `StablePath` (exp tròn giờ) thay vì đường poster công khai | Không thêm đường không ký vào nginx; URL ổn định 1 giờ nên trình duyệt cache được, hạn không vượt 2 giờ |
| Phụ đề: `internal/subtitle` thuần; không UTF-8 → `400` (không đoán bảng mã) | Test bảng nhiều trường hợp khó (BOM, CRLF, CR, dấu phẩy, số thứ tự, toạ độ, thẻ font/ASS, escape); đoán Windows-1258/1252 dễ sai |
| Ghi object trước, upsert `subtitles` sau | Lỗi DB chỉ để lại object sẽ bị ghi đè; `/play` không bao giờ trỏ tới file chưa có |
| nginx không cache `subs/` | Upload lại đè cùng key; cache 7 ngày sẽ phục vụ bản cũ (test nginx thật kiểm tra `BYPASS` và bản mới) |
| Package `internal/playback` (spec §3.2) sở hữu `watch_progress`; `catalog` khai báo `ProgressSource` | Ranh giới module: `playback` → `catalog`, không ngược lại; handler catalog test bằng fake |
| `BestEffort`: publish trong goroutine, timeout 2s, semaphore 256 | Spec §7 "log rồi bỏ, không làm hỏng việc xem"; `Producer.Publish` có thể chờ 10s khi broker chết; giới hạn bộ nhớ/goroutine |
| Ghi `watch_progress` đồng bộ trước khi trả `204` | Nguồn của "Xem tiếp"/`resume_at` phải bền; Kafka chỉ phục vụ analytics |
| `duration_s` chép vào `watch_progress`, không join `videos` | Lọc 5–95% dùng index `(user_id, updated_at DESC)`; `ready` là trạng thái cuối và chỉ video `ready` nhận sự kiện |
| Hàng "Xem tiếp": `playback.Repo` trả id + vị trí, `catalog.Repo.ReadyByIDs` lấy metadata, giữ thứ tự `updated_at` | Không SQL chéo module; poster ký như catalog |
| Web gửi sự kiện bằng `fetch keepalive` (không `sendBeacon`) | Một đường code cho cả lúc xem và lúc đóng tab; đặt được `Content-Type: application/json`; không qua `request()` nên 401 không đá người xem ra giữa phim |
| Ảnh xem trước bằng thanh tua phụ `SeekPreview` dưới video, giữ controls gốc | Trình duyệt không cho gắn preview vào thanh gốc; tự làm toàn bộ controls ngoài phạm vi |
| `PlaybackSession` thêm `onInfo` (không đổi chữ ký `load`) | Phụ đề/thumbnails nhận URL ký mới cùng lúc với master khi làm mới; test M4 giữ nguyên |
| `e2e-m5.sh` bằng `curl`; đọc topic bằng `kafka-console-consumer` trong container; bước Kafka chết là tuỳ chọn (`KAFKA_DOWN=1`) | Kiểm tra toàn bộ đường dữ liệu; dừng Kafka ảnh hưởng stack đang dùng nên không chạy mặc định |

## File Structure

```
streamfilm/
  internal/transcode/thumbs.go(+_test)        # mới: hằng số, PosterArgs, SpriteArgs, TileCount, ThumbnailsVTT
  internal/transcode/runner.go(+_test)        # Result.PosterFile/ThumbnailsFile/SpriteSheets; thumbnails(); ffmpeg()
  internal/worker/worker.go(+_test)           # Transcoded điền PosterKey/SpriteKey
  cmd/transcoder/main.go                      # log poster/sprite_sheets
  migrations/0004_subtitles_progress.sql      # mới: subtitles, watch_progress, index
  internal/db/migrate_test.go                 # kiểm tra 5 bảng
  internal/catalog/repo.go(+_test)            # Video.PosterKey/SpriteKey, ReadyInfo.PosterKey/SpriteKey, ReadyByIDs
  internal/catalog/consumer.go(+_test)        # chuyển poster/sprite từ sự kiện
  internal/catalog/handler.go                 # admin JSON: poster_key, sprite_key
  internal/subtitle/subtitle.go(+_test)       # mới: Convert, FromSRT, NormalizeVTT
  internal/storage/s3.go(+_test)              # PutObject
  internal/catalog/subtitles.go(+_test)       # mới: Subtitle, UpsertSubtitle, Subtitles, SubtitleHandler
  internal/signing/signing.go(+_test)         # StableStep, StablePath
  internal/catalog/viewer.go(+_test)          # poster_url, thumbnails_url, subtitles, resume_at, continue-watching
  internal/catalog/progress.go                # mới: ContinueWatchingLimit, WatchEntry, ProgressSource
  internal/events/events.go(+_test)           # TopicPlaybackEvents, PlaybackEvent
  internal/playback/{event,progress,publisher,handler}.go (+ playback_test, progress_test, handler_test)  # mới
  cmd/api/{main,router,router_test}.go        # nối SubtitleHandler, playback.Handler, Progress, BestEffort
  deploy/nginx/templates/default.conf.template  # subs/ không cache
  internal/signing/nginx_test.go              # Content-Type jpg/vtt, subs BYPASS, poster HIT
  web/app/src/api.ts(+test), format.ts(+test), styles.css
  web/app/src/components/Poster.tsx(+test), ContinueRow.tsx(+test), SeekPreview.tsx(+test)   # mới
  web/app/src/components/VideoGrid.tsx, Player.tsx; pages/HomePage.tsx, VideoPage.tsx
  web/app/src/player/thumbnails.ts(+test), reporter.ts(+test)   # mới
  web/app/src/player/recovery.ts(+test)       # onInfo
  scripts/e2e-m5.sh, Makefile                 # e2e-m5
  README.md, docs/{architecture,api,data-model,flows,operations}.md
  docs/decisions/0013-thumbnails-sprite-sheets.md, 0014-subtitles-srt-to-vtt.md, 0015-playback-events-best-effort.md  # mới
```

Không đổi: `deploy/docker-compose.yml` (topic `playback.events` đã được `kafka-init` tạo từ M0), `storage.ContentTypeFor` (đã có `.jpg` → `image/jpeg`, `.vtt` → `text/vtt`), các script e2e cũ.

Quy ước trình bày code: file mới có toàn văn; file sửa được ghi thành các cặp "thay đoạn A bằng đoạn B" — A khớp đúng một lần trong file, áp theo thứ tự.

---

### Task 1: Transcoder — poster, sprite và `thumbnails.vtt`

**Files:**
- Create: `internal/transcode/thumbs.go`, `internal/transcode/thumbs_test.go`
- Modify: `internal/transcode/runner.go`, `internal/transcode/runner_test.go`, `internal/worker/worker.go`, `internal/worker/worker_test.go`, `cmd/transcoder/main.go`

**Interfaces:**
- Consumes: `transcode.Runner{FFmpegPath, FFprobePath}`, `Probe`, `BuildArgs`, `Result`; test helper `hasSeq` (`ffmpeg_test.go`), `names` (`ladder_test.go`), `testutil.MakeSampleVideo`; `events.VideoTranscoded.PosterKey/SpriteKey` (đã có từ M2, `omitempty`).
- Produces:
  ```go
  const (
  	PosterFile = "poster.jpg"; PosterWidth = 640; PosterAt = 0.10
  	ThumbnailsFile = "thumbnails.vtt"; SpriteInterval = 10
  	TileWidth = 160; TileHeight = 90; SpriteColumns = 10; SpriteRows = 10
  	TilesPerSheet = SpriteColumns * SpriteRows
  )
  func SpriteName(sheet int) string                      // 0 → "sprite_001.jpg"
  func TileCount(durationSec float64) int                 // ceil(d/10), 0 nếu d ≤ 0
  func PosterArgs(input, out string, durationSec float64) []string
  func SpriteArgs(input, outDir string) []string
  func ThumbnailsVTT(durationSec float64) string
  type Result struct { Probe ProbeResult; Rungs []Rung; MasterPath string
  	PosterFile, ThumbnailsFile string; SpriteSheets int }   // thêm 3 field
  func (r Runner) ffmpeg(ctx context.Context, step string, args []string) error  // không export
  func worker.Transcoded(id string, res transcode.Result) events.VideoTranscoded  // + PosterKey, SpriteKey
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/transcode/runner_test.go` — thay:

```go
	"context"
	"os"
	"path/filepath"
	"strings"
```

bằng:

```go
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
```

`internal/transcode/runner_test.go` — thay:

```go
		}
	}
}
```

bằng:

```go
		}
	}

	// Ảnh xem trước: poster rộng 640, một tấm sprite 1600x900 (8 giây = 1 ô), thumbnails.vtt 1 cue.
	if res.PosterFile != PosterFile || res.ThumbnailsFile != ThumbnailsFile || res.SpriteSheets != 1 {
		t.Errorf("thumbs = %q %q %d", res.PosterFile, res.ThumbnailsFile, res.SpriteSheets)
	}
	if w, h := imageSize(t, filepath.Join(out, PosterFile)); w != 640 || h != 360 {
		t.Errorf("poster = %dx%d, want 640x360", w, h)
	}
	if w, h := imageSize(t, filepath.Join(out, "sprite_001.jpg")); w != 1600 || h != 900 {
		t.Errorf("sprite = %dx%d, want 1600x900", w, h)
	}
	vtt, err := os.ReadFile(filepath.Join(out, ThumbnailsFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(vtt) != ThumbnailsVTT(res.Probe.DurationSec) || !strings.Contains(string(vtt), "sprite_001.jpg#xywh=0,0,160,90") {
		t.Errorf("thumbnails.vtt:\n%s", vtt)
	}
}

// TestSpriteFrameSelection: bộ lọc select của SpriteArgs chọn đúng TileCount frame (khớp số cue trong
// thumbnails.vtt). Bỏ tile để đếm từng frame: video 25 giây → frame 0s, 10s, 20s.
func TestSpriteFrameSelection(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dir := t.TempDir()
	in := testutil.MakeSampleVideo(t, dir, 360, 25, false)
	args := SpriteArgs(in, dir)
	i := slices.Index(args, "-vf")
	args[i+1] = strings.Replace(args[i+1], fmt.Sprintf("tile=%dx%d", SpriteColumns, SpriteRows), "null", 1)
	args[len(args)-1] = filepath.Join(dir, "frame_%03d.jpg")
	if err := (Runner{FFmpegPath: "ffmpeg"}).ffmpeg(context.Background(), "sprite", args); err != nil {
		t.Fatal(err)
	}
	frames, _ := filepath.Glob(filepath.Join(dir, "frame_*.jpg"))
	if len(frames) != TileCount(25) {
		t.Errorf("frames = %d, want %d", len(frames), TileCount(25))
	}
}

// imageSize đọc kích thước ảnh bằng ffprobe.
func imageSize(t *testing.T, path string) (int, int) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var w, h int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d,%d", &w, &h); err != nil {
		t.Fatalf("ffprobe %s: %q", path, out)
	}
	return w, h
}
```

`internal/transcode/thumbs_test.go` (mới):

```go
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
```

`internal/worker/worker_test.go` — thay:

```go
var okResult = transcode.Result{
	Probe: transcode.ProbeResult{Width: 1280, Height: 720, DurationSec: 29.6, HasAudio: true},
	Rungs: []transcode.Rung{transcode.DefaultLadder[1], transcode.DefaultLadder[3]},
}
```

bằng:

```go
var okResult = transcode.Result{
	Probe:      transcode.ProbeResult{Width: 1280, Height: 720, DurationSec: 29.6, HasAudio: true},
	Rungs:      []transcode.Rung{transcode.DefaultLadder[1], transcode.DefaultLadder[3]},
	PosterFile: transcode.PosterFile, ThumbnailsFile: transcode.ThumbnailsFile, SpriteSheets: 1,
}
```

`internal/worker/worker_test.go` — thay:

```go
		{Height: 360, BitrateKbps: 800, PlaylistKey: testVID + "/360p/index.m3u8"},
	}}
	if got := h.pub.got[1].ev; !reflect.DeepEqual(got, want) {
```

bằng:

```go
		{Height: 360, BitrateKbps: 800, PlaylistKey: testVID + "/360p/index.m3u8"},
	}, PosterKey: testVID + "/poster.jpg", SpriteKey: testVID + "/thumbnails.vtt"}
	if got := h.pub.got[1].ev; !reflect.DeepEqual(got, want) {
```

`internal/worker/worker_test.go` — thay:

```go
		t.Error("transcoder must not run when processing event cannot be published")
	}
}
```

bằng:

```go
		t.Error("transcoder must not run when processing event cannot be published")
	}
}

func TestTranscodedWithoutThumbnails(t *testing.T) {
	res := okResult
	res.PosterFile, res.ThumbnailsFile = "", ""
	ev := Transcoded(testVID, res)
	if ev.PosterKey != "" || ev.SpriteKey != "" {
		t.Errorf("poster_key=%q sprite_key=%q, want rỗng", ev.PosterKey, ev.SpriteKey)
	}
	b, _ := json.Marshal(ev)
	if strings.Contains(string(b), "poster_key") || strings.Contains(string(b), "sprite_key") {
		t.Errorf("key rỗng phải bị bỏ khỏi JSON: %s", b)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/transcode/ ./internal/worker/`
Expected: FAIL — biên dịch lỗi `undefined: TileCount`, `undefined: PosterArgs`, `res.PosterFile undefined`…

- [ ] **Step 3: Cài đặt**

`cmd/transcoder/main.go` — thay:

```go
	}
	slog.Info("transcode done", "video_id", id, "duration_s", res.Probe.DurationSec, "rungs", rungs)
```

bằng:

```go
	}
	slog.Info("transcode done", "video_id", id, "duration_s", res.Probe.DurationSec, "rungs", rungs,
		"poster", res.PosterFile, "sprite_sheets", res.SpriteSheets)
```

`internal/transcode/runner.go` — thay:

```go
	MasterPath string
}
```

bằng:

```go
	MasterPath string
	// Ảnh xem trước (M5), đường dẫn tương đối so với outDir. ThumbnailsFile rỗng khi không biết
	// thời lượng (không sinh sprite).
	PosterFile     string
	ThumbnailsFile string
	SpriteSheets   int
}
```

`internal/transcode/runner.go` — thay:

```go
	})
	cmd := exec.CommandContext(ctx, r.FFmpegPath, args...)
```

bằng:

```go
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
```

`internal/transcode/runner.go` — thay:

```go
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("ffmpeg: %w: %s", err, tail(stderr.Bytes(), 2000))
	}
	return Result{Probe: p, Rungs: rungs, MasterPath: filepath.Join(outDir, "master.m3u8")}, nil
}
```

bằng:

```go
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", step, err, tail(stderr.Bytes(), 2000))
	}
	return nil
}
```

`internal/transcode/thumbs.go` (mới):

```go
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
```

`internal/worker/worker.go` — thay:

```go
	}
	return out
```

bằng:

```go
	}
	// poster_key / sprite_key là key trong bucket hls (giống playlist_key). sprite_key trỏ tới
	// thumbnails.vtt — chỉ mục của các tấm sprite (video dài có nhiều tấm), ADR 0013.
	if res.PosterFile != "" {
		out.PosterKey = id + "/" + res.PosterFile
	}
	if res.ThumbnailsFile != "" {
		out.SpriteKey = id + "/" + res.ThumbnailsFile
	}
	return out
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal cmd; go vet ./... && go test ./internal/transcode/ ./internal/worker/`
Expected: không file nào cần gofmt; `ok  streamfilm/internal/transcode` (gồm `TestRunnerTranscode` với poster 640x360, sprite 1600x900 và `TestSpriteFrameSelection` chạy FFmpeg thật, ~8 giây), `ok  streamfilm/internal/worker`.

- [ ] **Step 5: Commit**

```bash
git add internal/transcode internal/worker cmd/transcoder/main.go
git commit -m "feat(transcode): poster, sprite sheets and thumbnails.vtt"
```

---

### Task 2: Migration `0004` và lưu `poster_key` / `sprite_key`

**Files:**
- Create: `migrations/0004_subtitles_progress.sql`
- Modify: `internal/catalog/repo.go`, `internal/catalog/repo_test.go`, `internal/catalog/consumer.go`, `internal/catalog/consumer_test.go`, `internal/catalog/handler.go`, `internal/db/migrate_test.go`

**Interfaces:**
- Consumes: `events.VideoTranscoded{PosterKey, SpriteKey}` (Task 1), `Repo.transition`, `videoColumns`, `scanVideo`.
- Produces:
  ```go
  type Video struct { …; HLSPrefix string; PosterKey, SpriteKey string; CreatedAt, UpdatedAt time.Time }
  type ReadyInfo struct { DurationS int; HLSPrefix string; Renditions []Rendition; PosterKey, SpriteKey string }
  ```
  `MarkReady` ghi `poster_key = NULLIF($4,'')`, `sprite_key = NULLIF($5,'')`; admin `VideoJSON` thêm `poster_key`, `sprite_key` (`omitempty`). Bảng `subtitles(video_id, lang, label, vtt_key, created_at, updated_at)`, `watch_progress(user_id, video_id, position_s, duration_s, updated_at)` + index `watch_progress_user_updated_idx`.

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/catalog/consumer_test.go` — thay:

```go
		record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA, DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []events.Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}}),
		record(t, events.TopicVideoFailed, events.VideoFailed{VideoID: idB, Reason: "ffmpeg: boom"}),
```

bằng:

```go
		record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA, DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []events.Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}},
			PosterKey:  idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"}),
		record(t, events.TopicVideoFailed, events.VideoFailed{VideoID: idB, Reason: "ffmpeg: boom"}),
```

`internal/catalog/consumer_test.go` — thay:

```go
		{"MarkReady", idA, ReadyInfo{DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}}},
		{"MarkFailed", idB, "ffmpeg: boom"},
```

bằng:

```go
		{"MarkReady", idA, ReadyInfo{DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}},
			PosterKey:  idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"}},
		{"MarkFailed", idB, "ffmpeg: boom"},
```

`internal/catalog/repo_test.go` — thay:

```go
			{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"},
		}}
		if _, err := r.MarkReady(ctx, idA, info); err != nil {
```

bằng:

```go
			{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"},
		}, PosterKey: idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"}
		if v, _ := r.Get(ctx, idA); v.PosterKey != "" || v.SpriteKey != "" {
			t.Errorf("chưa ready mà đã có poster/sprite: %+v", v)
		}
		if _, err := r.MarkReady(ctx, idA, info); err != nil {
```

`internal/catalog/repo_test.go` — thay:

```go
		v, _ := r.Get(ctx, idA)
		if v.Status != StatusReady || v.DurationS != 30 || v.HLSPrefix != idA+"/" {
			t.Errorf("after ready: %+v", v)
```

bằng:

```go
		v, _ := r.Get(ctx, idA)
		if v.Status != StatusReady || v.DurationS != 30 || v.HLSPrefix != idA+"/" ||
			v.PosterKey != idA+"/poster.jpg" || v.SpriteKey != idA+"/thumbnails.vtt" {
			t.Errorf("after ready: %+v", v)
```

`internal/db/migrate_test.go` — thay:

```go
		err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('videos', 'renditions')`).Scan(&n)
		if err != nil || n != 2 {
			t.Errorf("tables = %d, err = %v", n, err)
```

bằng:

```go
		err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('videos', 'renditions', 'users', 'subtitles', 'watch_progress')`).Scan(&n)
		if err != nil || n != 5 {
			t.Errorf("tables = %d, err = %v", n, err)
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/catalog/`
Expected: FAIL — `unknown field PosterKey in struct literal of type ReadyInfo`.

- [ ] **Step 3: Cài đặt**

`internal/catalog/consumer.go` — thay:

```go
		id = ev.VideoID
		info := ReadyInfo{DurationS: ev.DurationS, HLSPrefix: ev.HLSPrefix}
		for _, r := range ev.Renditions {
```

bằng:

```go
		id = ev.VideoID
		info := ReadyInfo{DurationS: ev.DurationS, HLSPrefix: ev.HLSPrefix, PosterKey: ev.PosterKey, SpriteKey: ev.SpriteKey}
		for _, r := range ev.Renditions {
```

`internal/catalog/handler.go` — thay:

```go
	HLSPrefix   string      `json:"hls_prefix,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
```

bằng:

```go
	HLSPrefix   string      `json:"hls_prefix,omitempty"`
	PosterKey   string      `json:"poster_key,omitempty"`
	SpriteKey   string      `json:"sprite_key,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
```

`internal/catalog/handler.go` — thay:

```go
		Status: v.Status, FailReason: v.FailReason, DurationS: v.DurationS, SizeBytes: v.SizeBytes,
		HLSPrefix: v.HLSPrefix, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
```

bằng:

```go
		Status: v.Status, FailReason: v.FailReason, DurationS: v.DurationS, SizeBytes: v.SizeBytes,
		HLSPrefix: v.HLSPrefix, PosterKey: v.PosterKey, SpriteKey: v.SpriteKey, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}
```

`internal/catalog/repo.go` — thay:

```go
	HLSPrefix              string
	CreatedAt, UpdatedAt   time.Time
```

bằng:

```go
	HLSPrefix              string
	PosterKey, SpriteKey   string // key trong bucket hls; rỗng với video transcode trước M5
	CreatedAt, UpdatedAt   time.Time
```

`internal/catalog/repo.go` — thay:

```go
	Renditions []Rendition
}
```

bằng:

```go
	Renditions []Rendition
	PosterKey  string // {id}/poster.jpg
	SpriteKey  string // {id}/thumbnails.vtt (chỉ mục các tấm sprite)
}
```

`internal/catalog/repo.go` — thay:

```go
	COALESCE(fail_reason, ''), COALESCE(duration_s, 0), raw_key, COALESCE(upload_id, ''),
	size_bytes, COALESCE(hls_prefix, ''), created_at, updated_at`
```

bằng:

```go
	COALESCE(fail_reason, ''), COALESCE(duration_s, 0), raw_key, COALESCE(upload_id, ''),
	size_bytes, COALESCE(hls_prefix, ''), COALESCE(poster_key, ''), COALESCE(sprite_key, ''),
	created_at, updated_at`
```

`internal/catalog/repo.go` — thay:

```go
		&v.FailReason, &v.DurationS, &v.RawKey, &v.UploadID, &v.SizeBytes, &v.HLSPrefix,
		&v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
```

bằng:

```go
		&v.FailReason, &v.DurationS, &v.RawKey, &v.UploadID, &v.SizeBytes, &v.HLSPrefix,
		&v.PosterKey, &v.SpriteKey, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
```

`internal/catalog/repo.go` — thay:

```go

// MarkReady: processing → ready, ghi thời lượng, prefix HLS và thay toàn bộ rendition.
func (r *Repo) MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error) {
	return r.transition(ctx, id, StatusReady, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE videos SET duration_s = $2, hls_prefix = $3, fail_reason = NULL
			WHERE id = $1`, id, info.DurationS, info.HLSPrefix); err != nil {
			return err
```

bằng:

```go

// MarkReady: processing → ready, ghi thời lượng, prefix HLS, poster/sprite và thay toàn bộ rendition.
func (r *Repo) MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error) {
	return r.transition(ctx, id, StatusReady, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE videos SET duration_s = $2, hls_prefix = $3, fail_reason = NULL,
			poster_key = NULLIF($4, ''), sprite_key = NULLIF($5, '') WHERE id = $1`,
			id, info.DurationS, info.HLSPrefix, info.PosterKey, info.SpriteKey); err != nil {
			return err
```

`migrations/0004_subtitles_progress.sql` (mới):

```sql
-- M5: phụ đề và tiến độ xem (spec §4.2, §4.4, §5). Cột videos.poster_key / sprite_key có từ 0001.

-- Một phụ đề mỗi (video, ngôn ngữ); upload lại cùng lang thì ghi đè. vtt_key là key trong bucket hls:
-- {video_id}/subs/{lang}.vtt.
CREATE TABLE subtitles (
  video_id   uuid NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  lang       text NOT NULL,
  label      text NOT NULL,
  vtt_key    text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (video_id, lang)
);

-- Vị trí xem gần nhất của mỗi user với mỗi video. duration_s chép từ videos lúc ghi để lọc hàng
-- "Xem tiếp" (5% < position/duration < 95%) mà không cần tính lại.
CREATE TABLE watch_progress (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  video_id   uuid NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  position_s int  NOT NULL CHECK (position_s >= 0),
  duration_s int  NOT NULL CHECK (duration_s >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, video_id)
);

-- Hàng "Xem tiếp": video của một user, mới xem nhất trước.
CREATE INDEX watch_progress_user_updated_idx ON watch_progress (user_id, updated_at DESC);
```

- [ ] **Step 4: Chạy test**

Run: `go vet ./... && go test ./internal/catalog/ ./internal/db/`
Expected: `ok  streamfilm/internal/catalog` (Postgres thật: `TestRepo/happy_path_to_ready` thấy poster/sprite sau `MarkReady`), `ok  streamfilm/internal/db` (5 bảng).

- [ ] **Step 5: Commit**

```bash
git add migrations/0004_subtitles_progress.sql internal/catalog internal/db/migrate_test.go
git commit -m "feat(catalog): persist poster and sprite keys; subtitles and watch_progress tables"
```

---

### Task 3: Package `subtitle` — SRT → WebVTT

**Files:**
- Create: `internal/subtitle/subtitle.go`, `internal/subtitle/subtitle_test.go`

**Interfaces:**
- Consumes: chỉ thư viện chuẩn.
- Produces:
  ```go
  var ErrUnsupported, ErrNotUTF8, ErrNoCues, ErrNoHeader error
  func Convert(filename string, data []byte) ([]byte, error) // .srt → FromSRT, .vtt → NormalizeVTT, khác → ErrUnsupported
  func FromSRT(data []byte) ([]byte, error)
  func NormalizeVTT(data []byte) ([]byte, error)
  ```
  Lưu ý khi chép test: BOM viết bằng escape `"\xef\xbb\xbf"` trong chuỗi Go — không dán ký tự BOM thật vào file nguồn (Go báo `illegal byte order mark`).

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/subtitle/subtitle_test.go` (mới):

```go
package subtitle

import (
	"errors"
	"strings"
	"testing"
)

func TestFromSRT(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "cơ bản: số thứ tự, dấu phẩy, nhiều dòng",
			in: "1\n00:00:01,000 --> 00:00:03,500\nXin chào\nthế giới\n\n" +
				"2\n00:00:04,250 --> 00:00:06,000\nTạm biệt\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:03.500\nXin chào\nthế giới\n\n" +
				"00:00:04.250 --> 00:00:06.000\nTạm biệt\n",
		},
		{
			name: "BOM và CRLF",
			in:   "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:02,000\r\nMột\r\n\r\n2\r\n00:00:02,000 --> 00:00:03,000\r\nHai\r\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nMột\n\n00:00:02.000 --> 00:00:03.000\nHai\n",
		},
		{
			name: "chỉ CR (Mac cũ)",
			in:   "1\r00:00:01,000 --> 00:00:02,000\rMột\r",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nMột\n",
		},
		{
			name: "không có số thứ tự, nhiều dòng trống, khoảng trắng thừa, toạ độ sau mốc thời gian",
			in: "\n\n00:00:01,000 --> 00:00:02,000 X1:100 X2:200 Y1:10 Y2:20\nMột   \n\n\n\n" +
				"  \t\n00:00:03.000-->00:00:04.000\nHai\n\n\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nMột\n\n00:00:03.000 --> 00:00:04.000\nHai\n",
		},
		{
			name: "giờ lớn hơn 99 và mili giây",
			in:   "7\n100:59:59,999 --> 101:00:00,001\nDài\n",
			want: "WEBVTT\n\n100:59:59.999 --> 101:00:00.001\nDài\n",
		},
		{
			name: "thẻ: giữ i/b/u, bỏ font và ASS, escape & < > và -->",
			in: "1\n00:00:01,000 --> 00:00:02,000\n{\\an8}<i>Nghiêng</i> <FONT color=\"#ff0\">vàng</FONT> <B>đậm</B>\n" +
				"Tom & Jerry: 1 < 2 --> 3\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<i>Nghiêng</i> vàng <b>đậm</b>\n" +
				"Tom &amp; Jerry: 1 &lt; 2 --&gt; 3\n",
		},
		{
			name: "cue không có chữ bị bỏ",
			in:   "1\n00:00:01,000 --> 00:00:02,000\n\n2\n00:00:03,000 --> 00:00:04,000\nCòn lại\n",
			want: "WEBVTT\n\n00:00:03.000 --> 00:00:04.000\nCòn lại\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FromSRT([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestFromSRTErrors(t *testing.T) {
	cases := []struct {
		name, in string
		is       error  // lỗi sentinel mong đợi, hoặc
		contains string // chuỗi trong thông báo lỗi
	}{
		{name: "rỗng", in: "", is: ErrNoCues},
		{name: "chỉ dòng trống", in: "\r\n\n  \n", is: ErrNoCues},
		{name: "không phải UTF-8 (Windows-1258)", in: "1\n00:00:01,000 --> 00:00:02,000\nXin ch\xe0o\n", is: ErrNotUTF8},
		{name: "có byte NUL", in: "1\n00:00:01,000 --> 00:00:02,000\nA\x00B\n", is: ErrNotUTF8},
		{name: "khối không có dòng thời gian", in: "1\n00:00:01,000 --> 00:00:02,000\nA\n\n2\nkhông phải mốc\nB\n", contains: "line 6"},
		{name: "mili giây thiếu chữ số", in: "1\n00:00:01,5 --> 00:00:02,000\nA\n", contains: "line 2: invalid timing"},
		{name: "kết thúc trước bắt đầu", in: "1\n00:00:05,000 --> 00:00:02,000\nA\n", contains: "ends before it starts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FromSRT([]byte(c.in))
			if err == nil {
				t.Fatal("want error")
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("err = %v, want %v", err, c.is)
			}
			if c.contains != "" && !strings.Contains(err.Error(), c.contains) {
				t.Errorf("err = %v, want chứa %q", err, c.contains)
			}
		})
	}
}

func TestNormalizeVTT(t *testing.T) {
	got, err := NormalizeVTT([]byte("\xef\xbb\xbfWEBVTT - Tiếng Việt\r\n\r\n00:01.000 --> 00:02.000\r\n<v Nam>Chào</v>"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "WEBVTT - Tiếng Việt\n\n00:01.000 --> 00:02.000\n<v Nam>Chào</v>\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, c := range []struct {
		in   string
		want error
	}{
		{"1\n00:00:01,000 --> 00:00:02,000\nSRT đổi đuôi\n", ErrNoHeader},
		{"WEBVTTX\n\n00:01.000 --> 00:02.000\nA\n", ErrNoHeader},
		{"WEBVTT\n\nNOTE chỉ có ghi chú\n", ErrNoCues},
		{"WEBVTT\n\n00:01.000 --> 00:02.000\nXin ch\xe0o\n", ErrNotUTF8},
	} {
		if _, err := NormalizeVTT([]byte(c.in)); !errors.Is(err, c.want) {
			t.Errorf("NormalizeVTT(%q) err = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestConvert(t *testing.T) {
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\nA\n")
	if got, err := Convert("Phim.VI.SRT", srt); err != nil || !strings.HasPrefix(string(got), "WEBVTT\n\n00:00:01.000") {
		t.Errorf(".SRT: %q, %v", got, err)
	}
	if got, err := Convert("phim.vtt", []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nA\n")); err != nil || !strings.HasPrefix(string(got), "WEBVTT") {
		t.Errorf(".vtt: %q, %v", got, err)
	}
	for _, name := range []string{"phim.ass", "phim", "phim.srt.txt"} {
		if _, err := Convert(name, srt); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", name, err)
		}
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/subtitle/`
Expected: FAIL — `undefined: FromSRT`, `undefined: NormalizeVTT`, `undefined: Convert`.

- [ ] **Step 3: Cài đặt**

`internal/subtitle/subtitle.go` (mới):

```go
// Package subtitle chuyển phụ đề admin upload (.srt hoặc .vtt) thành WebVTT mà thẻ <track> của
// trình duyệt đọc được (spec §4.2). Trình duyệt chỉ hiểu WebVTT; SRT khác ở dấu phẩy trong mốc
// thời gian, số thứ tự cue, không có header và cho phép thẻ <font>.
package subtitle

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupported = errors.New("subtitle: unsupported format, want .srt or .vtt")
	ErrNotUTF8     = errors.New("subtitle: file must be UTF-8 text")
	ErrNoCues      = errors.New("subtitle: no cues found")
	ErrNoHeader    = errors.New("subtitle: WebVTT file must start with WEBVTT")
)

// Convert chọn theo phần mở rộng của filename (không phân biệt hoa thường): .srt → FromSRT, .vtt → NormalizeVTT.
func Convert(filename string, data []byte) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".srt":
		return FromSRT(data)
	case ".vtt":
		return NormalizeVTT(data)
	default:
		return nil, ErrUnsupported
	}
}

// normalize bỏ BOM UTF-8, kiểm tra UTF-8 (không NUL) và đổi CRLF / CR thành LF.
func normalize(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", ErrNotUTF8
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// NormalizeVTT giữ nguyên nội dung WebVTT (để trình duyệt tự parse), chỉ chuẩn hoá BOM / xuống dòng
// và kiểm tra tối thiểu: dòng đầu là "WEBVTT" (có thể kèm tiêu đề sau dấu cách/tab) và có ít nhất một cue.
func NormalizeVTT(data []byte) ([]byte, error) {
	s, err := normalize(data)
	if err != nil {
		return nil, err
	}
	first, _, _ := strings.Cut(s, "\n")
	if first != "WEBVTT" && !strings.HasPrefix(first, "WEBVTT ") && !strings.HasPrefix(first, "WEBVTT\t") {
		return nil, ErrNoHeader
	}
	if !strings.Contains(s, "-->") {
		return nil, ErrNoCues
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s), nil
}

// timingRe: "00:01:02,500 --> 00:01:04,000" (SRT dùng dấu phẩy; chấp nhận cả dấu chấm). Phần sau mốc
// kết thúc (toạ độ X1:… Y1:… của một số SRT) bị bỏ.
var timingRe = regexp.MustCompile(`^\s*(\d+):(\d{2}):(\d{2})[,.](\d{3})\s*-->\s*(\d+):(\d{2}):(\d{2})[,.](\d{3})(?:\s.*)?$`)

// assRe: thẻ override kiểu ASS mà nhiều file SRT mang theo, ví dụ {\an8}, {\i1}.
var assRe = regexp.MustCompile(`\{\\[^}]*\}`)

// tagRe: thẻ dạng <tên ...> hoặc </tên>. Chỉ <i>, <b>, <u> (WebVTT hỗ trợ) được giữ; <font ...> và
// thẻ khác bị bỏ. "<" không theo sau bởi chữ cái (ví dụ "1 < 2") là chữ thường và được escape.
var tagRe = regexp.MustCompile(`</?[A-Za-z][^<>]*>`)

type cue struct {
	start, end int64 // mili giây
	text       []string
}

// FromSRT chuyển SRT sang WebVTT. Cue cách nhau bởi một hoặc nhiều dòng trống; dòng số thứ tự có thể
// có hoặc không. Lỗi kèm số dòng khi một khối không có dòng thời gian hợp lệ hoặc kết thúc trước khi bắt đầu.
func FromSRT(data []byte) ([]byte, error) {
	s, err := normalize(data)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(s, "\n")
	var cues []cue
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		start := i // khối từ start tới trước dòng trống kế tiếp
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			i++
		}
		c, err := parseBlock(lines[start:i], start+1)
		if err != nil {
			return nil, err
		}
		if len(c.text) > 0 {
			cues = append(cues, c)
		}
	}
	if len(cues) == 0 {
		return nil, ErrNoCues
	}
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for _, c := range cues {
		fmt.Fprintf(&b, "\n%s --> %s\n%s\n", vttTime(c.start), vttTime(c.end), strings.Join(c.text, "\n"))
	}
	return []byte(b.String()), nil
}

// parseBlock đọc một khối SRT; lineNo là số dòng (1-based) của dòng đầu khối, dùng trong thông báo lỗi.
func parseBlock(block []string, lineNo int) (cue, error) {
	t := 0
	if !strings.Contains(block[0], "-->") && len(block) > 1 { // dòng số thứ tự
		t = 1
	}
	m := timingRe.FindStringSubmatch(block[t])
	if m == nil {
		return cue{}, fmt.Errorf("subtitle: line %d: invalid timing line %q", lineNo+t, truncate(block[t], 60))
	}
	c := cue{start: millis(m[1:5]), end: millis(m[5:9])}
	if c.end < c.start {
		return cue{}, fmt.Errorf("subtitle: line %d: cue ends before it starts", lineNo+t)
	}
	for _, l := range block[t+1:] {
		if l = cleanText(strings.TrimRight(l, " \t")); strings.TrimSpace(l) != "" {
			c.text = append(c.text, l)
		}
	}
	return c, nil
}

// cleanText bỏ thẻ ASS, bỏ thẻ không phải i/b/u và escape &, <, > trong phần chữ (nên "-->" trong lời
// thoại thành "--&gt;", không làm hỏng cấu trúc WebVTT).
func cleanText(s string) string {
	s = assRe.ReplaceAllString(s, "")
	var b strings.Builder
	last := 0
	for _, loc := range tagRe.FindAllStringIndex(s, -1) {
		b.WriteString(escape(s[last:loc[0]]))
		switch tag := strings.ToLower(s[loc[0]:loc[1]]); tag {
		case "<i>", "</i>", "<b>", "</b>", "<u>", "</u>":
			b.WriteString(tag)
		}
		last = loc[1]
	}
	b.WriteString(escape(s[last:]))
	return b.String()
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escape(s string) string { return escaper.Replace(s) }

func millis(p []string) int64 {
	n := make([]int64, 4)
	for i, s := range p {
		n[i], _ = strconv.ParseInt(s, 10, 64) // regex đã bảo đảm là chữ số
	}
	return ((n[0]*60+n[1])*60+n[2])*1000 + n[3]
}

// vttTime: 3723500 → "01:02:03.500".
func vttTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
```

- [ ] **Step 4: Chạy test**

Run: `go vet ./internal/subtitle && go test -v ./internal/subtitle 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: TestFromSRT`, `--- PASS: TestFromSRTErrors`, `--- PASS: TestNormalizeVTT`, `--- PASS: TestConvert`, `ok  streamfilm/internal/subtitle`.

- [ ] **Step 5: Commit**

```bash
git add internal/subtitle
git commit -m "feat(subtitle): SRT to WebVTT converter and VTT normalizer"
```

---

### Task 4: Admin upload phụ đề — `PutObject`, bảng `subtitles`, `POST /api/admin/videos/{id}/subtitles`

**Files:**
- Create: `internal/catalog/subtitles.go`, `internal/catalog/subtitles_test.go`
- Modify: `internal/storage/s3.go`, `internal/storage/s3_test.go`, `cmd/api/router.go`, `cmd/api/router_test.go`, `cmd/api/main.go`

**Interfaces:**
- Consumes: `subtitle.Convert` (Task 3), bảng `subtitles` (Task 2), `catalog.Repo.Get`, `httpx.WriteJSON/WriteError`, `config.Config.BucketHLS`; test helper `newTestRepo`, `newVideo`, `idA`, `idB`, `idX`.
- Produces:
  ```go
  func (s *storage.Store) PutObject(ctx context.Context, bucket, key, contentType string, body []byte) error
  type Subtitle struct{ Lang, Label, VTTKey string }
  const MaxSubtitleBytes = 1 << 20; const MaxLabelLen = 50
  var LangRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)
  func SubtitleKey(videoID, lang string) string           // "{id}/subs/{lang}.vtt"
  func (r *Repo) UpsertSubtitle(ctx context.Context, videoID string, s Subtitle) error // FK lỗi → ErrNotFound
  func (r *Repo) Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)   // ORDER BY lang
  type SubtitleStore interface { Get(ctx, id) (Video, error); UpsertSubtitle(ctx, videoID string, s Subtitle) error }
  type ObjectPutter interface { PutObject(ctx context.Context, bucket, key, contentType string, body []byte) error }
  type SubtitleHandler struct { Videos SubtitleStore; Objects ObjectPutter; BucketHLS string; Log *slog.Logger }
  func (h *SubtitleHandler) Routes(r chi.Router)          // POST /api/admin/videos/{id}/subtitles
  type AdminSubtitleJSON struct { Lang, Label, VTTKey string } // json lang, label, vtt_key
  ```
  `routes` (cmd/api) thêm field `Subtitles *catalog.SubtitleHandler`, gắn vào nhóm admin.

- [ ] **Step 1: Viết test (thất bại trước)**

`cmd/api/router_test.go` — thay:

```go
	h := newRouter(log, routes{
		Tokens: tokens,
		Auth:   &auth.Handler{Svc: &auth.Service{Tokens: tokens}, Log: log},
		Viewer: &catalog.ViewerHandler{Videos: emptyStore{}, Log: log},
		Upload: &upload.Handler{Log: log},
		Admin:  &catalog.AdminHandler{Videos: emptyStore{}, Log: log},
	})
```

bằng:

```go
	h := newRouter(log, routes{
		Tokens:    tokens,
		Auth:      &auth.Handler{Svc: &auth.Service{Tokens: tokens}, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: emptyStore{}, Log: log},
		Upload:    &upload.Handler{Log: log},
		Admin:     &catalog.AdminHandler{Videos: emptyStore{}, Log: log},
		Subtitles: &catalog.SubtitleHandler{Log: log},
	})
```

`cmd/api/router_test.go` — thay:

```go
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/retry", "", 401},
		{"GET", "/api/admin/videos", admin, 200},
```

bằng:

```go
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/retry", "", 401},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", "", 401},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", admin, 400}, // không phải multipart
		{"GET", "/api/admin/videos", admin, 200},
```

`internal/catalog/subtitles_test.go` (mới):

```go
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeSubtitleStore struct {
	videos map[string]Video
	saved  []Subtitle
}

func (f *fakeSubtitleStore) Get(_ context.Context, id string) (Video, error) {
	if v, ok := f.videos[id]; ok {
		return v, nil
	}
	return Video{}, ErrNotFound
}

func (f *fakeSubtitleStore) UpsertSubtitle(_ context.Context, _ string, s Subtitle) error {
	f.saved = append(f.saved, s)
	return nil
}

type putCall struct{ bucket, key, contentType, body string }

type fakePutter struct {
	calls []putCall
	err   error
}

func (f *fakePutter) PutObject(_ context.Context, bucket, key, contentType string, body []byte) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, putCall{bucket, key, contentType, string(body)})
	return nil
}

// subtitleForm dựng body multipart; filename rỗng thì không có phần file.
func subtitleForm(t *testing.T, lang, label, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("lang", lang)
	mw.WriteField("label", label)
	if filename != "" {
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(content)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func newSubtitleRouter(store SubtitleStore, put ObjectPutter) http.Handler {
	r := chi.NewRouter()
	(&SubtitleHandler{Videos: store, Objects: put, BucketHLS: "hls",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func postSubtitle(h http.Handler, id string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/videos/"+id+"/subtitles", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const testSRT = "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:03,000\r\nXin chào\r\n"

func TestSubtitleUpload(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA, Status: StatusReady}}}
	put := &fakePutter{}
	h := newSubtitleRouter(store, put)

	body, ct := subtitleForm(t, "vi", "  Tiếng Việt ", "phim.vi.srt", []byte(testSRT))
	rec := postSubtitle(h, idA, body, ct)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	var out AdminSubtitleJSON
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out != (AdminSubtitleJSON{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}) {
		t.Errorf("out = %+v", out)
	}
	wantPut := []putCall{{"hls", idA + "/subs/vi.vtt", "text/vtt", "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nXin chào\n"}}
	if !reflect.DeepEqual(put.calls, wantPut) {
		t.Errorf("put = %+v", put.calls)
	}
	if !reflect.DeepEqual(store.saved, []Subtitle{{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}}) {
		t.Errorf("saved = %+v", store.saved)
	}

	body, ct = subtitleForm(t, "pt-BR", "Português", "a.VTT", []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nOlá\n"))
	if rec := postSubtitle(h, idA, body, ct); rec.Code != http.StatusCreated {
		t.Errorf("vtt pt-BR: %d %s", rec.Code, rec.Body)
	}
}

func TestSubtitleUploadRejects(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA}}}
	h := newSubtitleRouter(store, &fakePutter{})
	big := bytes.Repeat([]byte("a"), MaxSubtitleBytes+1)
	cases := []struct {
		name, id, lang, label, filename string
		content                         []byte
		want                            int
		msg                             string
	}{
		{"lang hoa", idA, "VI", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang gạch dưới", idA, "vi_VN", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang vùng thường", idA, "pt-br", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang traversal", idA, "../x", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"lang rỗng", idA, "", "Việt", "a.srt", []byte(testSRT), 400, "lang"},
		{"label rỗng", idA, "vi", "   ", "a.srt", []byte(testSRT), 400, "label"},
		{"label dài", idA, "vi", strings.Repeat("ạ", MaxLabelLen+1), "a.srt", []byte(testSRT), 400, "label"},
		{"label ký tự điều khiển", idA, "vi", "Việt\nNam", "a.srt", []byte(testSRT), 400, "label"},
		{"thiếu file", idA, "vi", "Việt", "", nil, 400, "file is required"},
		{"đuôi không hỗ trợ", idA, "vi", "Việt", "a.ass", []byte(testSRT), 400, "unsupported"},
		{"SRT hỏng", idA, "vi", "Việt", "a.srt", []byte("1\nkhông phải mốc\nA\n"), 400, "line 2"},
		{"không phải UTF-8", idA, "vi", "Việt", "a.srt", []byte("1\n00:00:01,000 --> 00:00:02,000\nch\xe0o\n"), 400, "UTF-8"},
		{"quá 1 MiB", idA, "vi", "Việt", "a.srt", big, 413, "1 MiB"},
		{"video không tồn tại", idX, "vi", "Việt", "a.srt", []byte(testSRT), 404, "not found"},
		{"id không phải uuid", "abc", "vi", "Việt", "a.srt", []byte(testSRT), 404, "not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, ct := subtitleForm(t, c.lang, c.label, c.filename, c.content)
			rec := postSubtitle(h, c.id, body, ct)
			if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.msg) {
				t.Errorf("code = %d body = %s, want %d chứa %q", rec.Code, rec.Body, c.want, c.msg)
			}
		})
	}
	if rec := postSubtitle(h, idA, strings.NewReader(`{"lang":"vi"}`), "application/json"); rec.Code != 400 {
		t.Errorf("json body: %d", rec.Code)
	}
	if len(store.saved) != 0 {
		t.Errorf("không được lưu gì: %+v", store.saved)
	}
}

func TestSubtitleUploadStorageError(t *testing.T) {
	store := &fakeSubtitleStore{videos: map[string]Video{idA: {ID: idA}}}
	h := newSubtitleRouter(store, &fakePutter{err: errors.New("minio down")})
	body, ct := subtitleForm(t, "vi", "Việt", "a.srt", []byte(testSRT))
	if rec := postSubtitle(h, idA, body, ct); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "minio") {
		t.Errorf("code = %d body = %s", rec.Code, rec.Body)
	}
	if len(store.saved) != 0 {
		t.Error("lỗi storage thì không ghi bảng subtitles")
	}
}

func TestRepoSubtitles(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if err := r.Create(ctx, newVideo(idA)); err != nil {
		t.Fatal(err)
	}
	for _, s := range []Subtitle{
		{Lang: "vi", Label: "Tiếng Việt", VTTKey: SubtitleKey(idA, "vi")},
		{Lang: "en", Label: "English", VTTKey: SubtitleKey(idA, "en")},
		{Lang: "vi", Label: "Tiếng Việt (sửa)", VTTKey: SubtitleKey(idA, "vi")}, // upload lại: ghi đè
	} {
		if err := r.UpsertSubtitle(ctx, idA, s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Subtitles(ctx, idA)
	want := []Subtitle{
		{Lang: "en", Label: "English", VTTKey: idA + "/subs/en.vtt"},
		{Lang: "vi", Label: "Tiếng Việt (sửa)", VTTKey: idA + "/subs/vi.vtt"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("subtitles = %+v, err = %v", got, err)
	}
	if got, err := r.Subtitles(ctx, idB); err != nil || len(got) != 0 {
		t.Errorf("video không có phụ đề: %+v, %v", got, err)
	}
	if err := r.UpsertSubtitle(ctx, idX, Subtitle{Lang: "vi", Label: "x", VTTKey: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("video không tồn tại: err = %v, want ErrNotFound", err)
	}
}
```

`internal/storage/s3_test.go` — thay:

```go

	"github.com/testcontainers/testcontainers-go"
```

bằng:

```go

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
```

`internal/storage/s3_test.go` — thay:

```go
		t.Errorf("downloaded %q", b)
	}
}
```

bằng:

```go
		t.Errorf("downloaded %q", b)
	}

	// PutObject ghi đè và giữ Content-Type (nginx trả nguyên header này cho <track>).
	for _, body := range []string{"WEBVTT\n\nold", "WEBVTT\n\nnew"} {
		if err := s.PutObject(ctx, "hls", "vid1/subs/vi.vtt", "text/vtt", []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("hls"), Key: aws.String("vid1/subs/vi.vtt")})
	if err != nil {
		t.Fatal(err)
	}
	if ct := aws.ToString(head.ContentType); ct != "text/vtt" {
		t.Errorf("content type = %q", ct)
	}
	if err := s.Download(ctx, "hls", "vid1/subs/vi.vtt", dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "WEBVTT\n\nnew" {
		t.Errorf("after overwrite %q", b)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/catalog/ ./internal/storage/ ./cmd/api/`
Expected: FAIL — `undefined: SubtitleHandler`, `s.PutObject undefined`, `unknown field Subtitles in struct literal of type routes`.

- [ ] **Step 3: Cài đặt**

`cmd/api/main.go` — thay:

```go
	handler := newRouter(log, routes{
		Tokens: tokens,
		Auth:   &auth.Handler{Svc: authSvc, SecureCookie: cfg.CookieSecure, Log: log},
		Viewer: &catalog.ViewerHandler{Videos: repo, Signer: signer, CDNBaseURL: cfg.CDNBaseURL, Log: log},
		Upload: &upload.Handler{Svc: svc, Log: log},
		Admin:  &catalog.AdminHandler{Videos: repo, Log: log},
	})
```

bằng:

```go
	handler := newRouter(log, routes{
		Tokens:    tokens,
		Auth:      &auth.Handler{Svc: authSvc, SecureCookie: cfg.CookieSecure, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: repo, Signer: signer, CDNBaseURL: cfg.CDNBaseURL, Log: log},
		Upload:    &upload.Handler{Svc: svc, Log: log},
		Admin:     &catalog.AdminHandler{Videos: repo, Log: log},
		Subtitles: &catalog.SubtitleHandler{Videos: repo, Objects: store, BucketHLS: cfg.BucketHLS, Log: log},
	})
```

`cmd/api/router.go` — thay:

```go
type routes struct {
	Tokens *auth.Tokens
	Auth   *auth.Handler
	Viewer *catalog.ViewerHandler
	Upload *upload.Handler
	Admin  *catalog.AdminHandler
}
```

bằng:

```go
type routes struct {
	Tokens    *auth.Tokens
	Auth      *auth.Handler
	Viewer    *catalog.ViewerHandler
	Upload    *upload.Handler
	Admin     *catalog.AdminHandler
	Subtitles *catalog.SubtitleHandler
}
```

`cmd/api/router.go` — thay:

```go
		rt.Admin.Routes(r)
	})
```

bằng:

```go
		rt.Admin.Routes(r)
		rt.Subtitles.Routes(r)
	})
```

`internal/catalog/subtitles.go` (mới):

```go
package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"streamfilm/internal/httpx"
	"streamfilm/internal/subtitle"
)

// Subtitle là một phụ đề của video (bảng subtitles). VTTKey là key trong bucket hls.
type Subtitle struct {
	Lang, Label, VTTKey string
}

// Giới hạn upload phụ đề (spec §4.2).
const (
	MaxSubtitleBytes = 1 << 20 // 1 MiB: phụ đề phim 2 giờ thường 50–150 KB
	MaxLabelLen      = 50      // rune
)

// LangRe: mã ngôn ngữ dạng BCP 47 rút gọn — ISO 639-1/2 chữ thường, tuỳ chọn mã vùng ISO 3166 chữ hoa
// ("vi", "en", "fil", "pt-BR"). Đi thẳng vào key object nên không được có ký tự khác.
var LangRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

// SubtitleKey: {video_id}/subs/{lang}.vtt trong bucket hls (spec §4.2), cùng prefix ký với HLS.
func SubtitleKey(videoID, lang string) string { return videoID + "/subs/" + lang + ".vtt" }

// UpsertSubtitle thêm hoặc thay phụ đề (video_id, lang). Video không tồn tại → ErrNotFound.
func (r *Repo) UpsertSubtitle(ctx context.Context, videoID string, s Subtitle) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO subtitles (video_id, lang, label, vtt_key) VALUES ($1, $2, $3, $4)
		ON CONFLICT (video_id, lang) DO UPDATE SET label = EXCLUDED.label, vtt_key = EXCLUDED.vtt_key, updated_at = now()`,
		videoID, s.Lang, s.Label, s.VTTKey)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("upsert subtitle: %w", err)
	}
	return nil
}

// Subtitles trả phụ đề của video, sắp theo lang.
func (r *Repo) Subtitles(ctx context.Context, videoID string) ([]Subtitle, error) {
	rows, err := r.pool.Query(ctx, `SELECT lang, label, vtt_key FROM subtitles WHERE video_id = $1 ORDER BY lang`, videoID)
	if err != nil {
		return nil, fmt.Errorf("list subtitles: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Subtitle, error) {
		var s Subtitle
		err := row.Scan(&s.Lang, &s.Label, &s.VTTKey)
		return s, err
	})
}

type SubtitleStore interface {
	Get(ctx context.Context, id string) (Video, error)
	UpsertSubtitle(ctx context.Context, videoID string, s Subtitle) error
}

// ObjectPutter ghi object nhỏ vào storage (storage.Store).
type ObjectPutter interface {
	PutObject(ctx context.Context, bucket, key, contentType string, body []byte) error
}

// SubtitleHandler: POST /api/admin/videos/{id}/subtitles (router bọc auth.RequireAdmin).
type SubtitleHandler struct {
	Videos    SubtitleStore
	Objects   ObjectPutter
	BucketHLS string
	Log       *slog.Logger
}

func (h *SubtitleHandler) Routes(r chi.Router) {
	r.Post("/api/admin/videos/{id}/subtitles", h.upload)
}

// AdminSubtitleJSON là phản hồi của upload phụ đề.
type AdminSubtitleJSON struct {
	Lang   string `json:"lang"`
	Label  string `json:"label"`
	VTTKey string `json:"vtt_key"`
}

// formOverhead: phần multipart ngoài nội dung file (boundary, header, lang, label).
const formOverhead = 64 << 10

// upload nhận multipart/form-data: lang, label, file (.srt hoặc .vtt, ≤ 1 MiB, UTF-8). SRT được chuyển
// sang WebVTT; lưu vào hls/{id}/subs/{lang}.vtt (ghi đè nếu đã có) rồi upsert bảng subtitles.
func (h *SubtitleHandler) upload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxSubtitleBytes+formOverhead)
	if err := r.ParseMultipartForm(MaxSubtitleBytes + formOverhead); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "subtitle file must be at most 1 MiB")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "expected multipart/form-data with lang, label and file")
		return
	}
	defer r.MultipartForm.RemoveAll()

	lang := r.FormValue("lang")
	if !LangRe.MatchString(lang) {
		httpx.WriteError(w, http.StatusBadRequest, `lang must look like "vi", "en" or "pt-BR"`)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxLabelLen ||
		strings.IndexFunc(label, unicode.IsControl) >= 0 {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("label must be 1-%d characters of text", MaxLabelLen))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSubtitleBytes+1))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "cannot read file")
		return
	}
	if len(data) > MaxSubtitleBytes {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "subtitle file must be at most 1 MiB")
		return
	}
	vtt, err := subtitle.Convert(hdr.Filename, data)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := h.Videos.Get(r.Context(), id); errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	} else if err != nil {
		h.fail(w, id, "get video", err)
		return
	}
	s := Subtitle{Lang: lang, Label: label, VTTKey: SubtitleKey(id, lang)}
	if err := h.Objects.PutObject(r.Context(), h.BucketHLS, s.VTTKey, "text/vtt", vtt); err != nil {
		h.fail(w, id, "put subtitle", err)
		return
	}
	if err := h.Videos.UpsertSubtitle(r.Context(), id, s); errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	} else if err != nil {
		h.fail(w, id, "save subtitle", err)
		return
	}
	h.Log.Info("subtitle uploaded", "video_id", id, "lang", lang, "bytes", len(vtt))
	httpx.WriteJSON(w, http.StatusCreated, AdminSubtitleJSON{Lang: s.Lang, Label: s.Label, VTTKey: s.VTTKey})
}

func (h *SubtitleHandler) fail(w http.ResponseWriter, id, msg string, err error) {
	h.Log.Error(msg, "video_id", id, "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}
```

`internal/storage/s3.go` — thay:

```go
import (
	"context"
```

bằng:

```go
import (
	"bytes"
	"context"
```

`internal/storage/s3.go` — thay:

```go

func (s *Store) Download(ctx context.Context, bucket, key, dest string) error {
```

bằng:

```go

// PutObject ghi một object nhỏ (ví dụ phụ đề .vtt) từ bộ nhớ, ghi đè nếu đã có.
func (s *Store) PutObject(ctx context.Context, bucket, key, contentType string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

func (s *Store) Download(ctx context.Context, bucket, key, dest string) error {
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal cmd; go vet ./... && go test ./internal/catalog/ ./internal/storage/ ./cmd/api/`
Expected: `ok` cả ba (gồm `TestRepoSubtitles` với Postgres thật, `TestUploadDirAndDownload` kiểm tra `PutObject` ghi đè và giữ `text/vtt` trên MinIO thật, router: viewer → `403`, admin không multipart → `400`).

- [ ] **Step 5: Commit**

```bash
git add internal/catalog internal/storage cmd/api
git commit -m "feat(api): admin subtitle upload with SRT to VTT conversion"
```

---

### Task 5: URL ký cho poster, thumbnails và phụ đề

**Files:**
- Modify: `internal/signing/signing.go`, `internal/signing/signing_test.go`, `internal/catalog/viewer.go`, `internal/catalog/viewer_test.go`, `cmd/api/router_test.go`

**Interfaces:**
- Consumes: `Video.PosterKey/SpriteKey` (Task 2), `Repo.Subtitles` (Task 4).
- Produces:
  ```go
  const signing.StableStep = time.Hour
  func (s *Signer) StablePath(videoID, file string) (string, time.Time, error)
  type PlaySigner interface { Path(videoID, file string) (string, time.Time, error)
  	StablePath(videoID, file string) (string, time.Time, error) }
  type ViewerStore interface { Search; Genres; Get; Renditions
  	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error) }   // thêm Subtitles
  type ViewerVideoJSON struct { …; PosterURL string `json:"poster_url,omitempty"`; … }
  type PlayJSON struct { URL; ExpiresAt; Subtitles []SubtitleJSON; ThumbnailsURL string `json:"thumbnails_url,omitempty"`; ResumeAt int }
  ```
  `*catalog.Repo` vẫn thoả `ViewerStore`; `*signing.Signer` thoả `PlaySigner`.

- [ ] **Step 1: Viết test (thất bại trước)**

`cmd/api/router_test.go` — thay:

```go
func (emptyStore) Renditions(context.Context, string) ([]catalog.Rendition, error) { return nil, nil }
```

bằng:

```go
func (emptyStore) Renditions(context.Context, string) ([]catalog.Rendition, error) { return nil, nil }
func (emptyStore) Subtitles(context.Context, string) ([]catalog.Subtitle, error)   { return nil, nil }
```

`internal/catalog/viewer_test.go` — thay:

```go
	"net/http"
	"strings"
```

bằng:

```go
	"net/http"
	"reflect"
	"strings"
```

`internal/catalog/viewer_test.go` — thay:

```go
	genres      []string
	last        SearchParams
```

bằng:

```go
	genres      []string
	subs        map[string][]Subtitle
	last        SearchParams
```

`internal/catalog/viewer_test.go` — thay:

```go

type fakeSigner struct{ exp time.Time }
```

bằng:

```go

func (f *fakeViewerStore) Subtitles(_ context.Context, id string) ([]Subtitle, error) {
	return f.subs[id], nil
}

type fakeSigner struct{ exp time.Time }
```

`internal/catalog/viewer_test.go` — thay:

```go
	return "/cdn/s/SIG/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}
```

bằng:

```go
	return "/cdn/s/SIG/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}

func (s fakeSigner) StablePath(id, file string) (string, time.Time, error) {
	return "/cdn/s/STABLE/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}
```

`internal/catalog/viewer_test.go` — thay:

```go
		t.Errorf("genres: %s", rec.Body)
	}
}
```

bằng:

```go
		t.Errorf("genres: %s", rec.Body)
	}
}

func TestViewerPosterAndPlayExtras(t *testing.T) {
	store := &fakeViewerStore{
		videos: []Video{
			{ID: idA, Title: "A", Status: StatusReady, DurationS: 30,
				PosterKey: idA + "/poster.jpg", SpriteKey: idA + "/thumbnails.vtt"},
			{ID: idC, Title: "C (trước M5)", Status: StatusReady, DurationS: 30},
		},
		subs: map[string][]Subtitle{idA: {
			{Lang: "en", Label: "English", VTTKey: idA + "/subs/en.vtt"},
			{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"},
			{Lang: "xx", Label: "Hỏng", VTTKey: idB + "/subs/xx.vtt"}, // key ngoài thư mục video: bỏ qua
		}},
	}
	h := newViewerRouter(store)
	const base = "http://cdn.test/cdn/s/"

	var list struct {
		Videos []ViewerVideoJSON `json:"videos"`
	}
	json.Unmarshal(get(h, "/api/videos").Body.Bytes(), &list)
	if len(list.Videos) != 2 || list.Videos[0].PosterURL != base+"STABLE/1794736800/hls/"+idA+"/poster.jpg" ||
		list.Videos[1].PosterURL != "" {
		t.Errorf("list posters = %+v", list.Videos)
	}
	if rec := get(h, "/api/videos/"+idC); strings.Contains(rec.Body.String(), "poster_url") {
		t.Errorf("video không có poster thì bỏ poster_url: %s", rec.Body)
	}
	var detail ViewerVideoJSON
	json.Unmarshal(get(h, "/api/videos/"+idA).Body.Bytes(), &detail)
	if detail.PosterURL != base+"STABLE/1794736800/hls/"+idA+"/poster.jpg" {
		t.Errorf("detail poster = %q", detail.PosterURL)
	}

	var p PlayJSON
	json.Unmarshal(get(h, "/api/videos/"+idA+"/play").Body.Bytes(), &p)
	sig := base + "SIG/1794736800/hls/" + idA + "/"
	wantSubs := []SubtitleJSON{
		{Lang: "en", Label: "English", URL: sig + "subs/en.vtt"},
		{Lang: "vi", Label: "Tiếng Việt", URL: sig + "subs/vi.vtt"},
	}
	if p.ThumbnailsURL != sig+"thumbnails.vtt" || !reflect.DeepEqual(p.Subtitles, wantSubs) {
		t.Errorf("play = %+v", p)
	}
	rec := get(h, "/api/videos/"+idC+"/play")
	if strings.Contains(rec.Body.String(), "thumbnails_url") || !strings.Contains(rec.Body.String(), `"subtitles":[]`) {
		t.Errorf("video trước M5: %s", rec.Body)
	}
}
```

`internal/signing/signing_test.go` — thay:

```go

func TestValidateSecret(t *testing.T) {
```

bằng:

```go

func TestSignerStablePath(t *testing.T) {
	// testExp là đúng một mốc giờ. now + TTL rơi vào [testExp, testExp+1h) → exp = testExp.
	for _, offset := range []time.Duration{0, time.Second, 30 * time.Minute, time.Hour - time.Second} {
		now := time.Unix(testExp, 0).Add(-TTL + offset)
		s, err := NewSigner(testSecret, TTL, func() time.Time { return now })
		if err != nil {
			t.Fatal(err)
		}
		p, exp, err := s.StablePath(testVID, "poster.jpg")
		if err != nil {
			t.Fatal(err)
		}
		if exp.Unix() != testExp || p != SignedPath(testSecret, testVID, "poster.jpg", testExp) {
			t.Errorf("offset %v: exp = %v, path = %q", offset, exp, p)
		}
		if left := exp.Sub(now); left > TTL || left <= TTL-StableStep {
			t.Errorf("offset %v: còn hạn %v, want trong (1h, 2h]", offset, left)
		}
	}
	s, _ := NewSigner(testSecret, TTL, time.Now)
	if _, _, err := s.StablePath(testVID, "../x.jpg"); err == nil {
		t.Error("StablePath phải kiểm tra file như Path")
	}
}

func TestValidateSecret(t *testing.T) {
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/signing/ ./internal/catalog/ ./cmd/api/`
Expected: FAIL — biên dịch lỗi `s.StablePath undefined`, `list.Videos[0].PosterURL undefined`, `p.ThumbnailsURL undefined`.

- [ ] **Step 3: Cài đặt**

`internal/catalog/viewer.go` — thay:

```go
	Renditions(ctx context.Context, id string) ([]Rendition, error)
}

// PlaySigner ký đường dẫn HLS (signing.Signer).
type PlaySigner interface {
	Path(videoID, file string) (string, time.Time, error)
}
```

bằng:

```go
	Renditions(ctx context.Context, id string) ([]Rendition, error)
	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
}

// PlaySigner ký đường dẫn trong thư mục hls/{id}/ (signing.Signer). StablePath cho ảnh poster
// (URL ổn định trong một giờ để trình duyệt cache), Path cho mọi thứ khác.
type PlaySigner interface {
	Path(videoID, file string) (string, time.Time, error)
	StablePath(videoID, file string) (string, time.Time, error)
}
```

`internal/catalog/viewer.go` — thay:

```go
	CreatedAt   time.Time             `json:"created_at"`
	Renditions  []ViewerRenditionJSON `json:"renditions,omitempty"`
```

bằng:

```go
	CreatedAt   time.Time             `json:"created_at"`
	PosterURL   string                `json:"poster_url,omitempty"` // URL ký; không có với video trước M5
	Renditions  []ViewerRenditionJSON `json:"renditions,omitempty"`
```

`internal/catalog/viewer.go` — thay:

```go

// PlayJSON: subtitles luôn là [] và resume_at luôn 0 tới M5 (giữ nguyên hình dạng cho web M4).
type PlayJSON struct {
	URL       string         `json:"url"`
	ExpiresAt time.Time      `json:"expires_at"`
	Subtitles []SubtitleJSON `json:"subtitles"`
	ResumeAt  int            `json:"resume_at"`
}
```

bằng:

```go

// PlayJSON (spec §4.3): mọi URL cùng prefix ký /cdn/s/{sig}/{exp}/hls/{id}/. subtitles luôn là mảng
// ([] khi không có); thumbnails_url bỏ khi video chưa có sprite (transcode trước M5).
type PlayJSON struct {
	URL           string         `json:"url"`
	ExpiresAt     time.Time      `json:"expires_at"`
	Subtitles     []SubtitleJSON `json:"subtitles"`
	ThumbnailsURL string         `json:"thumbnails_url,omitempty"`
	ResumeAt      int            `json:"resume_at"`
}
```

`internal/catalog/viewer.go` — thay:

```go
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}
```

bằng:

```go
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}

// fileInDir: key "{id}/poster.jpg" → "poster.jpg". Key không nằm trong thư mục của video → false.
func fileInDir(videoID, key string) (string, bool) {
	f, ok := strings.CutPrefix(key, videoID+"/")
	return f, ok && f != ""
}

// toViewer: toViewerJSON + poster_url đã ký. Ký lỗi thì bỏ poster (web hiện ảnh tạm), không lỗi cả trang.
func (h *ViewerHandler) toViewer(v Video) ViewerVideoJSON {
	out := toViewerJSON(v)
	if f, ok := fileInDir(v.ID, v.PosterKey); ok {
		if p, _, err := h.Signer.StablePath(v.ID, f); err == nil {
			out.PosterURL = h.CDNBaseURL + p
		} else {
			h.Log.Warn("sign poster url", "video_id", v.ID, "err", err)
		}
	}
	return out
}
```

`internal/catalog/viewer.go` — thay:

```go
	for _, v := range vs {
		out = append(out, toViewerJSON(v))
	}
```

bằng:

```go
	for _, v := range vs {
		out = append(out, h.toViewer(v))
	}
```

`internal/catalog/viewer.go` — thay:

```go
	}
	out := toViewerJSON(v)
	for _, rd := range rs {
```

bằng:

```go
	}
	out := h.toViewer(v)
	for _, rd := range rs {
```

`internal/catalog/viewer.go` — thay:

```go
	}
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, PlayJSON{URL: h.CDNBaseURL + path, ExpiresAt: exp,
		Subtitles: []SubtitleJSON{}, ResumeAt: 0})
}
```

bằng:

```go
	}
	subs, err := h.Videos.Subtitles(r.Context(), v.ID)
	if err != nil {
		h.Log.Error("list subtitles", "video_id", v.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := PlayJSON{URL: h.CDNBaseURL + path, ExpiresAt: exp, Subtitles: make([]SubtitleJSON, 0, len(subs))}
	for _, s := range subs {
		if u, ok := h.signFile(v.ID, s.VTTKey); ok {
			out.Subtitles = append(out.Subtitles, SubtitleJSON{Lang: s.Lang, Label: s.Label, URL: u})
		}
	}
	out.ThumbnailsURL, _ = h.signFile(v.ID, v.SpriteKey)
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, out)
}

// signFile trả URL tuyệt đối đã ký cho key trong thư mục của video; key rỗng hoặc lạ → false.
func (h *ViewerHandler) signFile(videoID, key string) (string, bool) {
	f, ok := fileInDir(videoID, key)
	if !ok {
		return "", false
	}
	p, _, err := h.Signer.Path(videoID, f)
	if err != nil {
		h.Log.Warn("sign file", "video_id", videoID, "key", key, "err", err)
		return "", false
	}
	return h.CDNBaseURL + p, true
}
```

`internal/signing/signing.go` — thay:

```go
const TTL = 2 * time.Hour
```

bằng:

```go
const TTL = 2 * time.Hour

// StableStep: StablePath làm tròn exp xuống bội số của bước này, nên URL ảnh (poster) giống hệt nhau
// trong suốt một giờ và trình duyệt cache được (URL ký với exp theo giây đổi ở mỗi lần gọi API).
const StableStep = time.Hour
```

`internal/signing/signing.go` — thay:

```go

// Path ký file trong thư mục HLS của video; trả path và thời điểm hết hạn.
func (s *Signer) Path(videoID, file string) (string, time.Time, error) {
	if !videoIDRe.MatchString(videoID) {
```

bằng:

```go

// Path ký file trong thư mục HLS của video; trả path và thời điểm hết hạn (now + TTL).
func (s *Signer) Path(videoID, file string) (string, time.Time, error) {
	return s.sign(videoID, file, s.now().Add(s.ttl).Unix())
}

// StablePath như Path nhưng exp = now + TTL làm tròn xuống bội số StableStep (Unix giây): hạn còn lại
// nằm trong (TTL − StableStep, TTL], không bao giờ dài hơn TTL. Dùng cho ảnh poster trong catalog.
func (s *Signer) StablePath(videoID, file string) (string, time.Time, error) {
	step := int64(StableStep / time.Second)
	return s.sign(videoID, file, s.now().Add(s.ttl).Unix()/step*step)
}

func (s *Signer) sign(videoID, file string, exp int64) (string, time.Time, error) {
	if !videoIDRe.MatchString(videoID) {
```

`internal/signing/signing.go` — thay:

```go
	}
	exp := s.now().Add(s.ttl).Unix()
	return SignedPath(s.secret, videoID, file, exp), time.Unix(exp, 0).UTC(), nil
```

bằng:

```go
	}
	return SignedPath(s.secret, videoID, file, exp), time.Unix(exp, 0).UTC(), nil
```

- [ ] **Step 4: Chạy test**

Run: `go vet ./... && go test -short ./...`
Expected: mọi package `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/signing internal/catalog cmd/api/router_test.go
git commit -m "feat(api): signed poster, thumbnails and subtitle URLs"
```

---

### Task 6: Package `playback` — sự kiện, `watch_progress`, publish best-effort

**Files:**
- Create: `internal/catalog/progress.go`, `internal/playback/event.go`, `internal/playback/progress.go`, `internal/playback/publisher.go`, `internal/playback/playback_test.go`, `internal/playback/progress_test.go`
- Modify: `internal/events/events.go`, `internal/events/events_test.go`

**Interfaces:**
- Consumes: bảng `watch_progress`, `users` (Task 2, M3); `catalog.NewRepo`, `Create`, `Transition`, `MarkReady` (trong test); `events.Event`, `NewRecord`, `Decode`.
- Produces:
  ```go
  // events
  const TopicPlaybackEvents = "playback.events"
  type PlaybackEvent struct { TS time.Time `json:"ts"`; UserID string `json:"user_id"`; VideoID string `json:"video_id"`
  	Type string `json:"type"`; PositionS int `json:"position_s"`; BitrateKbps int `json:"bitrate_kbps"`; BufferingMs int `json:"buffering_ms"` }
  func (e PlaybackEvent) Key() string // UserID
  // catalog
  const ContinueWatchingLimit = 20
  type WatchEntry struct { VideoID string; PositionS, DurationS int; UpdatedAt time.Time }
  type ProgressSource interface {
  	ResumeAt(ctx context.Context, userID, videoID string) (int, error)
  	ContinueWatching(ctx context.Context, userID string, limit int) ([]WatchEntry, error) }
  // playback
  const ContinueMinRatio = 0.05; const ContinueMaxRatio = 0.95
  var ErrNotFound, ErrInvalidEvent error
  func InContinueRow(positionS, durationS int) bool
  func ResumePosition(positionS, durationS int) int
  type Repo struct{…}; func NewRepo(pool *pgxpool.Pool) *Repo
  func (r *Repo) SaveProgress(ctx context.Context, userID, videoID string, positionS, durationS int) error
  func (r *Repo) ResumeAt(ctx context.Context, userID, videoID string) (int, error)
  func (r *Repo) ContinueWatching(ctx context.Context, userID string, limit int) ([]catalog.WatchEntry, error)
  type EventType string // TypePlay, TypePause, TypeSeek, TypeEnded, TypeBuffering, TypeHeartbeat
  func (t EventType) Valid() bool; func (t EventType) SavesProgress() bool
  const MaxPositionS = 48 * 3600; const MaxBitrateKbps = 100_000; const MaxBufferingMs = 10 * 60 * 1000
  type EventInput struct { VideoID string; Type EventType; PositionS float64; BitrateKbps, BufferingMs int } // json snake_case
  func (in *EventInput) Normalize() (int, error)
  const PublishTimeout = 2 * time.Second; const MaxInFlight = 256
  type Publisher interface { Publish(ctx context.Context, topic string, ev events.Event) error }
  func NewBestEffort(pub Publisher, timeout time.Duration, maxInFlight int, log *slog.Logger) *BestEffort
  func (b *BestEffort) Send(ev events.PlaybackEvent) bool; func (b *BestEffort) Wait(); func (b *BestEffort) Dropped() int64
  ```
  `*playback.Repo` thoả `catalog.ProgressSource`.

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/events/events_test.go` — thay:

```go
		t.Errorf("VideoTopics = %v", VideoTopics)
	}
}
```

bằng:

```go
		t.Errorf("VideoTopics = %v", VideoTopics)
	}
}

func TestPlaybackEventRecord(t *testing.T) {
	ev := PlaybackEvent{TS: time.Date(2026, 11, 15, 10, 0, 0, 123e6, time.UTC), UserID: "u1", VideoID: "v1",
		Type: "heartbeat", PositionS: 42, BitrateKbps: 2800, BufferingMs: 0}
	rec, err := NewRecord(TopicPlaybackEvents, ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-11-15T10:00:00.123Z","user_id":"u1","video_id":"v1","type":"heartbeat","position_s":42,"bitrate_kbps":2800,"buffering_ms":0}`
	if string(rec.Value) != want || string(rec.Key) != "u1" || rec.Topic != "playback.events" {
		t.Errorf("record = %s key=%s topic=%s", rec.Value, rec.Key, rec.Topic)
	}
	got, err := Decode[PlaybackEvent](rec.Value)
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Errorf("decode = %+v, err = %v", got, err)
	}
	if _, err := Decode[PlaybackEvent]([]byte(`{"video_id":"v1"}`)); err == nil {
		t.Error("thiếu user_id phải lỗi")
	}
}
```

`internal/playback/playback_test.go` (mới):

```go
package playback

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"streamfilm/internal/events"
)

func TestInContinueRowAndResume(t *testing.T) {
	cases := []struct {
		pos, dur   int
		inRow      bool
		resumeWant int
	}{
		{0, 100, false, 0},
		{5, 100, false, 5}, // đúng 5%: chưa vào hàng
		{6, 100, true, 6},
		{50, 100, true, 50},
		{94, 100, true, 94},
		{95, 100, false, 0}, // đúng 95%: coi như xem xong, phát lại từ đầu
		{100, 100, false, 0},
		{10, 0, false, 10}, // không rõ thời lượng
	}
	for _, c := range cases {
		if got := InContinueRow(c.pos, c.dur); got != c.inRow {
			t.Errorf("InContinueRow(%d, %d) = %v", c.pos, c.dur, got)
		}
		if got := ResumePosition(c.pos, c.dur); got != c.resumeWant {
			t.Errorf("ResumePosition(%d, %d) = %d, want %d", c.pos, c.dur, got, c.resumeWant)
		}
	}
}

func TestEventInputNormalize(t *testing.T) {
	in := EventInput{VideoID: "11111111-1111-1111-1111-11111111111A", Type: TypeHeartbeat, PositionS: 42.9,
		BitrateKbps: 2800}
	pos, err := in.Normalize()
	if err != nil || pos != 42 || in.VideoID != "11111111-1111-1111-1111-11111111111a" {
		t.Errorf("pos=%d id=%s err=%v", pos, in.VideoID, err)
	}
	const id = "11111111-1111-1111-1111-111111111111"
	for _, bad := range []EventInput{
		{VideoID: "abc", Type: TypePlay},
		{VideoID: id, Type: "rewind"},
		{VideoID: id, Type: ""},
		{VideoID: id, Type: TypePlay, PositionS: -1},
		{VideoID: id, Type: TypePlay, PositionS: MaxPositionS + 1},
		{VideoID: id, Type: TypePlay, BitrateKbps: -1},
		{VideoID: id, Type: TypePlay, BitrateKbps: MaxBitrateKbps + 1},
		{VideoID: id, Type: TypeBuffering, BufferingMs: -5},
		{VideoID: id, Type: TypeBuffering, BufferingMs: MaxBufferingMs + 1},
	} {
		if _, err := bad.Normalize(); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%+v: err = %v, want ErrInvalidEvent", bad, err)
		}
	}
	for _, typ := range []EventType{TypeHeartbeat, TypePause, TypeEnded} {
		if !typ.SavesProgress() {
			t.Errorf("%s phải ghi tiến độ", typ)
		}
	}
	for _, typ := range []EventType{TypePlay, TypeSeek, TypeBuffering} {
		if typ.SavesProgress() {
			t.Errorf("%s không ghi tiến độ", typ)
		}
	}
}

// blockingPub: Publish chờ tới khi release đóng hoặc ctx hết hạn.
type blockingPub struct {
	release chan struct{}
	mu      sync.Mutex
	got     []events.PlaybackEvent
}

func (p *blockingPub) Publish(ctx context.Context, topic string, ev events.Event) error {
	if topic != events.TopicPlaybackEvents {
		return errors.New("wrong topic " + topic)
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, ev.(events.PlaybackEvent))
	return nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBestEffortDropsWhenSaturated(t *testing.T) {
	pub := &blockingPub{release: make(chan struct{})}
	b := NewBestEffort(pub, time.Minute, 2, discard())
	start := time.Now()
	sent := []bool{
		b.Send(events.PlaybackEvent{UserID: "u", Type: "play"}),
		b.Send(events.PlaybackEvent{UserID: "u", Type: "heartbeat"}),
		b.Send(events.PlaybackEvent{UserID: "u", Type: "pause"}), // đã có 2 publish đang chờ: bỏ
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("Send không được chặn")
	}
	if !sent[0] || !sent[1] || sent[2] || b.Dropped() != 1 {
		t.Errorf("sent = %v dropped = %d", sent, b.Dropped())
	}
	close(pub.release)
	b.Wait()
	if len(pub.got) != 2 {
		t.Errorf("published %d, want 2", len(pub.got))
	}
	if !b.Send(events.PlaybackEvent{UserID: "u", Type: "ended"}) { // hết tải thì nhận lại
		t.Error("sau khi publish xong phải nhận sự kiện mới")
	}
	b.Wait()
}

func TestBestEffortTimeout(t *testing.T) {
	var logs strings.Builder
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}), nil))
	b := NewBestEffort(&blockingPub{release: make(chan struct{})}, 50*time.Millisecond, 4, log) // Kafka "chết"
	b.Send(events.PlaybackEvent{UserID: "u", VideoID: "v", Type: "heartbeat"})
	done := make(chan struct{})
	go func() { b.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish phải bỏ cuộc sau timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if b.Dropped() != 1 || !strings.Contains(logs.String(), "playback event dropped") {
		t.Errorf("dropped = %d, logs = %s", b.Dropped(), logs.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
```

`internal/playback/progress_test.go` (mới):

```go
package playback

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"streamfilm/internal/catalog"
	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

const (
	userA = "aaaaaaaa-0000-0000-0000-000000000001"
	userB = "aaaaaaaa-0000-0000-0000-000000000002"
)

func videoID(i int) string { return fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", i) }

// setup: Postgres thật, 2 user, n video ready (duration 100 giây).
func setup(t *testing.T, n int) (*Repo, *pgxpool.Pool) {
	t.Helper()
	dsn := testutil.StartPostgres(t)
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for i, u := range []string{userA, userB} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, 'x', 'viewer')`,
			u, fmt.Sprintf("u%d@example.com", i)); err != nil {
			t.Fatal(err)
		}
	}
	cat := catalog.NewRepo(pool)
	for i := range n {
		id := videoID(i)
		if err := cat.Create(ctx, catalog.Video{ID: id, Title: "V", RawKey: id + "/source.mp4", SizeBytes: 1}); err != nil {
			t.Fatal(err)
		}
		for _, to := range []catalog.Status{catalog.StatusQueued, catalog.StatusProcessing} {
			if _, err := cat.Transition(ctx, id, to); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := cat.MarkReady(ctx, id, catalog.ReadyInfo{DurationS: 100, HLSPrefix: id + "/"}); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepo(pool), pool
}

func TestRepoProgress(t *testing.T) {
	r, _ := setup(t, 25)
	ctx := context.Background()

	if at, err := r.ResumeAt(ctx, userA, videoID(0)); err != nil || at != 0 {
		t.Errorf("chưa xem: resume = %d, err = %v", at, err)
	}
	if err := r.SaveProgress(ctx, userA, videoID(0), 30, 100); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveProgress(ctx, userA, videoID(0), 42, 100); err != nil { // upsert
		t.Fatal(err)
	}
	if at, _ := r.ResumeAt(ctx, userA, videoID(0)); at != 42 {
		t.Errorf("resume = %d, want 42", at)
	}
	if at, _ := r.ResumeAt(ctx, userB, videoID(0)); at != 0 {
		t.Errorf("user khác: resume = %d, want 0", at)
	}
	if err := r.SaveProgress(ctx, userA, videoID(1), 97, 100); err != nil {
		t.Fatal(err)
	}
	if at, _ := r.ResumeAt(ctx, userA, videoID(1)); at != 0 {
		t.Errorf("xem ≥ 95%%: resume = %d, want 0", at)
	}
	if err := r.SaveProgress(ctx, userA, "cccccccc-0000-0000-0000-000000000000", 1, 100); !errors.Is(err, ErrNotFound) {
		t.Errorf("video không tồn tại: err = %v, want ErrNotFound", err)
	}
}

func TestRepoContinueWatching(t *testing.T) {
	r, _ := setup(t, 25)
	ctx := context.Background()
	// Video 0..3: biên 5%/95% (5 và 95 bị loại, 6 và 94 được giữ). Video 4..24: 21 video ở 50%.
	positions := map[int]int{0: 5, 1: 6, 2: 94, 3: 95}
	for i := 4; i < 25; i++ {
		positions[i] = 50
	}
	for i := range 25 { // ghi theo thứ tự 0..24: video 24 mới xem nhất
		if err := r.SaveProgress(ctx, userA, videoID(i), positions[i], 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SaveProgress(ctx, userB, videoID(0), 50, 100); err != nil {
		t.Fatal(err)
	}

	got, err := r.ContinueWatching(ctx, userA, catalog.ContinueWatchingLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != catalog.ContinueWatchingLimit {
		t.Fatalf("len = %d, want %d", len(got), catalog.ContinueWatchingLimit)
	}
	if got[0].VideoID != videoID(24) || got[0].PositionS != 50 || got[0].DurationS != 100 || got[0].UpdatedAt.IsZero() {
		t.Errorf("đầu hàng = %+v, want video 24", got[0])
	}
	for i := 1; i < len(got); i++ {
		if got[i].UpdatedAt.After(got[i-1].UpdatedAt) {
			t.Errorf("chưa sắp updated_at giảm dần ở %d", i)
		}
	}
	all, _ := r.ContinueWatching(ctx, userA, 100)
	seen := map[string]bool{}
	for _, e := range all {
		seen[e.VideoID] = true
		if !InContinueRow(e.PositionS, e.DurationS) {
			t.Errorf("%+v không thoả InContinueRow", e)
		}
	}
	if len(all) != 23 || seen[videoID(0)] || !seen[videoID(1)] || !seen[videoID(2)] || seen[videoID(3)] {
		t.Errorf("biên 5%%/95%%: len=%d seen0=%v seen1=%v seen2=%v seen3=%v",
			len(all), seen[videoID(0)], seen[videoID(1)], seen[videoID(2)], seen[videoID(3)])
	}
	// Xem tiếp video 1 → lên đầu hàng; xem hết video 2 → rời hàng.
	r.SaveProgress(ctx, userA, videoID(1), 60, 100)
	r.SaveProgress(ctx, userA, videoID(2), 100, 100)
	got, _ = r.ContinueWatching(ctx, userA, catalog.ContinueWatchingLimit)
	if got[0].VideoID != videoID(1) || got[0].PositionS != 60 {
		t.Errorf("đầu hàng = %+v, want video 1 ở 60s", got[0])
	}
	for _, e := range got {
		if e.VideoID == videoID(2) {
			t.Error("video đã xem hết vẫn trong hàng")
		}
	}
	if b, _ := r.ContinueWatching(ctx, userB, catalog.ContinueWatchingLimit); len(b) != 1 || b[0].VideoID != videoID(0) {
		t.Errorf("user B = %+v", b)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/events/ ./internal/playback/`
Expected: FAIL — `undefined: PlaybackEvent`, `undefined: TopicPlaybackEvents`; package `playback` chưa có file nguồn (`no non-test Go files`).

- [ ] **Step 3: Cài đặt**

`internal/catalog/progress.go` (mới):

```go
package catalog

import (
	"context"
	"time"
)

// ContinueWatchingLimit: số video tối đa của hàng "Xem tiếp" (spec §4.4).
const ContinueWatchingLimit = 20

// WatchEntry là tiến độ xem một video của một user (bảng watch_progress, module playback).
type WatchEntry struct {
	VideoID              string
	PositionS, DurationS int
	UpdatedAt            time.Time
}

// ProgressSource: phần của playback.Repo mà catalog cần cho /play (resume_at) và hàng "Xem tiếp".
// Khai báo ở catalog (bên dùng) để catalog không import playback.
type ProgressSource interface {
	ResumeAt(ctx context.Context, userID, videoID string) (int, error)
	ContinueWatching(ctx context.Context, userID string, limit int) ([]WatchEntry, error)
}
```

`internal/events/events.go` — thay:

```go
	TopicVideoFailed      = "video.failed"
)
```

bằng:

```go
	TopicVideoFailed      = "video.failed"
	TopicPlaybackEvents   = "playback.events"
)
```

`internal/events/events.go` — thay:

```go

func (e VideoUploaded) Key() string   { return e.VideoID }
```

bằng:

```go

// PlaybackEvent: sự kiện xem phim (spec §3.3, §4.4). api publish, analytics (M6) ghi vào ClickHouse.
// Key = user_id (không phải video_id) để các sự kiện của một người xem giữ đúng thứ tự.
type PlaybackEvent struct {
	TS          time.Time `json:"ts"`
	UserID      string    `json:"user_id"`
	VideoID     string    `json:"video_id"`
	Type        string    `json:"type"`
	PositionS   int       `json:"position_s"`
	BitrateKbps int       `json:"bitrate_kbps"`
	BufferingMs int       `json:"buffering_ms"`
}

func (e VideoUploaded) Key() string   { return e.VideoID }
```

`internal/events/events.go` — thay:

```go
func (e DeadLetter) Key() string      { return e.VideoID }
```

bằng:

```go
func (e DeadLetter) Key() string      { return e.VideoID }
func (e PlaybackEvent) Key() string   { return e.UserID }
```

`internal/events/events.go` — thay:

```go

// Decode parse JSON và bắt buộc có video_id.
func Decode[T Event](data []byte) (T, error) {
```

bằng:

```go

// Decode parse JSON và bắt buộc có key (video_id; user_id với PlaybackEvent).
func Decode[T Event](data []byte) (T, error) {
```

`internal/events/events.go` — thay:

```go
	if v.Key() == "" {
		return v, errors.New("decode event: missing video_id")
	}
```

bằng:

```go
	if v.Key() == "" {
		return v, errors.New("decode event: missing key (video_id or user_id)")
	}
```

`internal/playback/event.go` (mới):

```go
package playback

import (
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
)

// EventType: loại sự kiện player gửi (spec §4.4).
type EventType string

const (
	TypePlay      EventType = "play"
	TypePause     EventType = "pause"
	TypeSeek      EventType = "seek"
	TypeEnded     EventType = "ended"
	TypeBuffering EventType = "buffering"
	TypeHeartbeat EventType = "heartbeat" // mỗi 10 giây khi đang phát
)

func (t EventType) Valid() bool {
	switch t {
	case TypePlay, TypePause, TypeSeek, TypeEnded, TypeBuffering, TypeHeartbeat:
		return true
	}
	return false
}

// SavesProgress: chỉ heartbeat, pause, ended cập nhật watch_progress (spec §4.4). play/seek/buffering
// chỉ đi vào analytics: seek liên tục khi kéo thanh tua không làm nhiễu "Xem tiếp".
func (t EventType) SavesProgress() bool {
	return t == TypeHeartbeat || t == TypePause || t == TypeEnded
}

// Giới hạn giá trị của sự kiện.
const (
	MaxPositionS   = 48 * 3600 // giây
	MaxBitrateKbps = 100_000
	MaxBufferingMs = 10 * 60 * 1000
)

// EventInput là body của POST /api/playback/events. position_s là currentTime của <video> (số thực).
type EventInput struct {
	VideoID     string    `json:"video_id"`
	Type        EventType `json:"type"`
	PositionS   float64   `json:"position_s"`
	BitrateKbps int       `json:"bitrate_kbps"`
	BufferingMs int       `json:"buffering_ms"`
}

var ErrInvalidEvent = errors.New("invalid playback event")

// Normalize kiểm tra input, chuẩn hoá video_id (uuid dạng chữ thường) và trả vị trí theo giây nguyên.
func (in *EventInput) Normalize() (int, error) {
	id, err := uuid.Parse(in.VideoID)
	if err != nil {
		return 0, fmt.Errorf("%w: video_id must be a UUID", ErrInvalidEvent)
	}
	in.VideoID = id.String()
	if !in.Type.Valid() {
		return 0, fmt.Errorf("%w: type must be one of play, pause, seek, ended, buffering, heartbeat", ErrInvalidEvent)
	}
	if math.IsNaN(in.PositionS) || in.PositionS < 0 || in.PositionS > MaxPositionS {
		return 0, fmt.Errorf("%w: position_s must be between 0 and %d", ErrInvalidEvent, MaxPositionS)
	}
	if in.BitrateKbps < 0 || in.BitrateKbps > MaxBitrateKbps {
		return 0, fmt.Errorf("%w: bitrate_kbps must be between 0 and %d", ErrInvalidEvent, MaxBitrateKbps)
	}
	if in.BufferingMs < 0 || in.BufferingMs > MaxBufferingMs {
		return 0, fmt.Errorf("%w: buffering_ms must be between 0 and %d", ErrInvalidEvent, MaxBufferingMs)
	}
	return int(in.PositionS), nil
}
```

`internal/playback/progress.go` (mới):

```go
// Package playback nhận sự kiện xem phim từ player: ghi tiến độ (watch_progress) cho "Xem tiếp" /
// resume_at và publish playback.events cho analytics (spec §4.4).
package playback

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"streamfilm/internal/catalog"
)

// Ngưỡng hàng "Xem tiếp" (spec §4.4): 5% < position/duration < 95%. Số video tối đa (20) là
// catalog.ContinueWatchingLimit.
const (
	ContinueMinRatio = 0.05
	ContinueMaxRatio = 0.95
)

// ErrNotFound: user hoặc video không còn tồn tại khi ghi tiến độ.
var ErrNotFound = errors.New("user or video not found")

// InContinueRow: video có thuộc hàng "Xem tiếp" không. Khớp điều kiện WHERE của ContinueWatching.
func InContinueRow(positionS, durationS int) bool {
	if durationS <= 0 {
		return false
	}
	r := float64(positionS) / float64(durationS)
	return r > ContinueMinRatio && r < ContinueMaxRatio
}

// ResumePosition: vị trí phát tiếp. Đã xem tới ≥ 95% (coi như xem xong) thì phát lại từ đầu.
func ResumePosition(positionS, durationS int) int {
	if durationS > 0 && float64(positionS) >= ContinueMaxRatio*float64(durationS) {
		return 0
	}
	return max(positionS, 0)
}

// Repo đọc/ghi bảng watch_progress. Thoả catalog.ProgressSource.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// SaveProgress ghi vị trí mới nhất (upsert theo (user_id, video_id)) và chép duration_s của video.
func (r *Repo) SaveProgress(ctx context.Context, userID, videoID string, positionS, durationS int) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO watch_progress (user_id, video_id, position_s, duration_s, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, video_id) DO UPDATE
		SET position_s = EXCLUDED.position_s, duration_s = EXCLUDED.duration_s, updated_at = now()`,
		userID, videoID, positionS, durationS)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("save progress: %w", err)
	}
	return nil
}

// ResumeAt trả vị trí phát tiếp (ResumePosition); chưa xem bao giờ → 0.
func (r *Repo) ResumeAt(ctx context.Context, userID, videoID string) (int, error) {
	var pos, dur int
	err := r.pool.QueryRow(ctx, `SELECT position_s, duration_s FROM watch_progress
		WHERE user_id = $1 AND video_id = $2`, userID, videoID).Scan(&pos, &dur)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resume at: %w", err)
	}
	return ResumePosition(pos, dur), nil
}

// ContinueWatching: hàng "Xem tiếp" của user, dùng index (user_id, updated_at DESC). Chỉ video ready mới
// có tiến độ (event handler từ chối video chưa ready) và ready là trạng thái cuối nên không cần join videos.
func (r *Repo) ContinueWatching(ctx context.Context, userID string, limit int) ([]catalog.WatchEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT video_id::text, position_s, duration_s, updated_at FROM watch_progress
		WHERE user_id = $1 AND duration_s > 0
		  AND position_s > duration_s * $2::float8 AND position_s < duration_s * $3::float8
		ORDER BY updated_at DESC LIMIT $4`, userID, ContinueMinRatio, ContinueMaxRatio, limit)
	if err != nil {
		return nil, fmt.Errorf("continue watching: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (catalog.WatchEntry, error) {
		var e catalog.WatchEntry
		err := row.Scan(&e.VideoID, &e.PositionS, &e.DurationS, &e.UpdatedAt)
		return e, err
	})
}
```

`internal/playback/publisher.go` (mới):

```go
package playback

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"streamfilm/internal/events"
)

// Giá trị mặc định của BestEffort trong api.
const (
	PublishTimeout = 2 * time.Second
	MaxInFlight    = 256
)

type Publisher interface {
	Publish(ctx context.Context, topic string, ev events.Event) error
}

// BestEffort publish playback.events ở nền, không bao giờ chặn request của player (spec §7: Kafka
// không khả dụng → ghi log rồi bỏ sự kiện, không làm hỏng việc xem). Mỗi sự kiện chờ tối đa timeout;
// đang có maxInFlight lần publish chưa xong (Kafka chậm hoặc chết) thì sự kiện mới bị bỏ ngay,
// nên số goroutine và bộ nhớ có giới hạn.
type BestEffort struct {
	pub     Publisher
	timeout time.Duration
	log     *slog.Logger
	sem     chan struct{}
	wg      sync.WaitGroup
	dropped atomic.Int64
}

func NewBestEffort(pub Publisher, timeout time.Duration, maxInFlight int, log *slog.Logger) *BestEffort {
	return &BestEffort{pub: pub, timeout: timeout, log: log, sem: make(chan struct{}, maxInFlight)}
}

// Send trả về ngay: true nếu sự kiện được đưa đi publish, false nếu bị bỏ vì quá nhiều publish đang chờ.
func (b *BestEffort) Send(ev events.PlaybackEvent) bool {
	select {
	case b.sem <- struct{}{}:
	default:
		b.dropped.Add(1)
		b.log.Warn("playback event dropped: too many publishes in flight", "video_id", ev.VideoID, "type", ev.Type)
		return false
	}
	b.wg.Go(func() {
		defer func() { <-b.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
		defer cancel()
		if err := b.pub.Publish(ctx, events.TopicPlaybackEvents, ev); err != nil {
			b.dropped.Add(1)
			b.log.Warn("playback event dropped", "video_id", ev.VideoID, "type", ev.Type, "err", err)
		}
	})
	return true
}

// Wait chờ các publish đang chạy (mỗi cái tối đa timeout). Gọi khi tắt api, trước khi đóng producer.
func (b *BestEffort) Wait() { b.wg.Wait() }

// Dropped: tổng số sự kiện đã bỏ (quá tải hoặc publish lỗi).
func (b *BestEffort) Dropped() int64 { return b.dropped.Load() }
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal; go vet ./... && go test ./internal/events/ ./internal/playback/`
Expected: `ok` cả hai (`TestRepoContinueWatching` với Postgres thật: biên 5/6/94/95, 20 phần tử, sắp `updated_at` giảm dần; `TestBestEffortDropsWhenSaturated`, `TestBestEffortTimeout`).

- [ ] **Step 5: Commit**

```bash
git add internal/events internal/catalog/progress.go internal/playback
git commit -m "feat(playback): watch progress repo, event validation and best-effort publisher"
```

---

### Task 7: API — `POST /api/playback/events`, `GET /api/me/continue-watching`, `resume_at`

**Files:**
- Create: `internal/playback/handler.go`, `internal/playback/handler_test.go`
- Modify: `internal/catalog/repo.go`, `internal/catalog/repo_test.go`, `internal/catalog/viewer.go`, `internal/catalog/viewer_test.go`, `cmd/api/router.go`, `cmd/api/router_test.go`, `cmd/api/main.go`

**Interfaces:**
- Consumes: `playback.EventInput.Normalize`, `SaveProgress`, `BestEffort` (Task 6); `catalog.ProgressSource`, `WatchEntry`, `ContinueWatchingLimit` (Task 6); `auth.FromContext`, `auth.WithIdentity`; `makeReady` (`search_test.go`).
- Produces:
  ```go
  func (r *catalog.Repo) ReadyByIDs(ctx context.Context, ids []string) ([]Video, error)
  type ViewerStore interface { …; ReadyByIDs(ctx context.Context, ids []string) ([]Video, error) }  // thêm
  type ViewerHandler struct { Videos ViewerStore; Progress ProgressSource; Signer PlaySigner; CDNBaseURL string; Log *slog.Logger }
  type ContinueJSON struct { ViewerVideoJSON; PositionS int `json:"position_s"`; WatchedAt time.Time `json:"watched_at"` }
  // route GET /api/me/continue-watching trong ViewerHandler.Routes
  type playback.VideoGetter interface { Get(ctx context.Context, id string) (catalog.Video, error) }
  type playback.ProgressWriter interface { SaveProgress(ctx context.Context, userID, videoID string, positionS, durationS int) error }
  type playback.EventSink interface { Send(ev events.PlaybackEvent) bool }
  type playback.Handler struct { Videos VideoGetter; Progress ProgressWriter; Events EventSink; Now func() time.Time; Log *slog.Logger }
  func (h *playback.Handler) Routes(r chi.Router) // POST /api/playback/events
  ```
  `routes` (cmd/api) thêm field `Events *playback.Handler`, gắn vào nhóm user. `main`: `playback.NewRepo(pool)`, `playback.NewBestEffort(producer, playback.PublishTimeout, playback.MaxInFlight, log)`, `Wait()` sau `wg.Wait()` (trước `producer.Close()` của `defer`).

- [ ] **Step 1: Viết test (thất bại trước)**

`cmd/api/router_test.go` — thay:

```go
	"streamfilm/internal/catalog"
	"streamfilm/internal/upload"
```

bằng:

```go
	"streamfilm/internal/catalog"
	"streamfilm/internal/playback"
	"streamfilm/internal/upload"
```

`cmd/api/router_test.go` — thay:

```go
func (emptyStore) Subtitles(context.Context, string) ([]catalog.Subtitle, error)   { return nil, nil }
```

bằng:

```go
func (emptyStore) Subtitles(context.Context, string) ([]catalog.Subtitle, error)   { return nil, nil }
func (emptyStore) ReadyByIDs(context.Context, []string) ([]catalog.Video, error)   { return nil, nil }
func (emptyStore) ResumeAt(context.Context, string, string) (int, error)           { return 0, nil }
func (emptyStore) ContinueWatching(context.Context, string, int) ([]catalog.WatchEntry, error) {
	return nil, nil
}
```

`cmd/api/router_test.go` — thay:

```go
		Auth:      &auth.Handler{Svc: &auth.Service{Tokens: tokens}, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: emptyStore{}, Log: log},
		Upload:    &upload.Handler{Log: log},
```

bằng:

```go
		Auth:      &auth.Handler{Svc: &auth.Service{Tokens: tokens}, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: emptyStore{}, Progress: emptyStore{}, Log: log},
		Events:    &playback.Handler{Videos: emptyStore{}, Log: log},
		Upload:    &upload.Handler{Log: log},
```

`cmd/api/router_test.go` — thay:

```go
		{"GET", "/api/me", "", 401},
	}
```

bằng:

```go
		{"GET", "/api/me", "", 401},
		{"GET", "/api/me/continue-watching", "", 401},
		{"GET", "/api/me/continue-watching", viewer, 200},
		{"POST", "/api/playback/events", "", 401},
		{"POST", "/api/playback/events", viewer, 400}, // body rỗng
	}
```

`internal/catalog/repo_test.go` — thay:

```go
	}
}
```

bằng:

```go
	}
}

func TestRepoReadyByIDs(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}
	makeReady(t, r, idA) // idB vẫn uploading
	vs, err := r.ReadyByIDs(ctx, []string{idA, idB, idX})
	if err != nil || len(vs) != 1 || vs[0].ID != idA {
		t.Errorf("ReadyByIDs = %+v, err = %v", vs, err)
	}
	if vs, err := r.ReadyByIDs(ctx, nil); err != nil || len(vs) != 0 {
		t.Errorf("ids rỗng: %+v, %v", vs, err)
	}
}
```

`internal/catalog/viewer_test.go` — thay:

```go
	"encoding/json"
	"fmt"
```

bằng:

```go
	"encoding/json"
	"errors"
	"fmt"
```

`internal/catalog/viewer_test.go` — thay:

```go
	"reflect"
	"strings"
```

bằng:

```go
	"reflect"
	"slices"
	"strings"
```

`internal/catalog/viewer_test.go` — thay:

```go
	"github.com/go-chi/chi/v5"
)
```

bằng:

```go
	"github.com/go-chi/chi/v5"

	"streamfilm/internal/auth"
)
```

`internal/catalog/viewer_test.go` — thay:

```go

func newViewerRouter(store ViewerStore) http.Handler {
	r := chi.NewRouter()
	(&ViewerHandler{Videos: store, Signer: fakeSigner{exp: time.Unix(1794736800, 0).UTC()},
		CDNBaseURL: "http://cdn.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
```

bằng:

```go

func (f *fakeViewerStore) ReadyByIDs(_ context.Context, ids []string) ([]Video, error) {
	var out []Video
	for i := len(f.videos) - 1; i >= 0; i-- { // thứ tự khác ids: handler phải tự sắp lại
		v := f.videos[i]
		if v.Status == StatusReady && slices.Contains(ids, v.ID) {
			out = append(out, v)
		}
	}
	return out, nil
}

// fakeProgress: tiến độ của user "user-1".
type fakeProgress struct {
	resume  map[string]int
	entries []WatchEntry
	err     error
	limit   int
}

func (f *fakeProgress) ResumeAt(_ context.Context, userID, videoID string) (int, error) {
	if userID != "user-1" {
		return 0, nil
	}
	return f.resume[videoID], f.err
}

func (f *fakeProgress) ContinueWatching(_ context.Context, userID string, limit int) ([]WatchEntry, error) {
	f.limit = limit
	if userID != "user-1" {
		return nil, nil
	}
	return f.entries, f.err
}

func newViewerRouter(store ViewerStore) http.Handler {
	return newViewerRouterWith(store, &fakeProgress{})
}

// newViewerRouterWith: mọi request mang identity user-1 (thay cho auth.Authenticate + RequireUser).
func newViewerRouterWith(store ViewerStore, progress ProgressSource) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{UserID: "user-1", Role: auth.RoleViewer})))
		})
	})
	(&ViewerHandler{Videos: store, Progress: progress, Signer: fakeSigner{exp: time.Unix(1794736800, 0).UTC()},
		CDNBaseURL: "http://cdn.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
```

`internal/catalog/viewer_test.go` — thay:

```go
		t.Errorf("video trước M5: %s", rec.Body)
	}
}
```

bằng:

```go
		t.Errorf("video trước M5: %s", rec.Body)
	}
}

func TestViewerResumeAt(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{{ID: idA, Status: StatusReady, DurationS: 100}}}
	progress := &fakeProgress{resume: map[string]int{idA: 42}}
	var p PlayJSON
	json.Unmarshal(get(newViewerRouterWith(store, progress), "/api/videos/"+idA+"/play").Body.Bytes(), &p)
	if p.ResumeAt != 42 {
		t.Errorf("resume_at = %d, want 42", p.ResumeAt)
	}
	// Lỗi đọc tiến độ không chặn việc xem: vẫn 200, phát từ đầu.
	progress.err = errors.New("db down")
	rec := get(newViewerRouterWith(store, progress), "/api/videos/"+idA+"/play")
	json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusOK || p.ResumeAt != 0 {
		t.Errorf("progress lỗi: %d resume_at=%d", rec.Code, p.ResumeAt)
	}
}

func TestViewerContinueWatching(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusReady, DurationS: 100, PosterKey: idA + "/poster.jpg"},
		{ID: idB, Title: "B", Status: StatusReady, DurationS: 200},
	}}
	t1 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	progress := &fakeProgress{entries: []WatchEntry{
		{VideoID: idB, PositionS: 120, DurationS: 200, UpdatedAt: t1.Add(time.Minute)},
		{VideoID: idX, PositionS: 10, DurationS: 100, UpdatedAt: t1.Add(30 * time.Second)}, // đã bị xoá: bỏ qua
		{VideoID: idA, PositionS: 40, DurationS: 100, UpdatedAt: t1},
	}}
	rec := get(newViewerRouterWith(store, progress), "/api/me/continue-watching")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Videos []ContinueJSON `json:"videos"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != 2 || body.Videos[0].ID != idB || body.Videos[0].PositionS != 120 ||
		!body.Videos[0].WatchedAt.Equal(t1.Add(time.Minute)) || body.Videos[1].ID != idA || body.Videos[1].PositionS != 40 ||
		body.Videos[1].PosterURL == "" || body.Videos[1].DurationS != 100 {
		t.Errorf("videos = %+v", body.Videos)
	}
	if progress.limit != ContinueWatchingLimit {
		t.Errorf("limit = %d", progress.limit)
	}
	rec = get(newViewerRouterWith(store, &fakeProgress{}), "/api/me/continue-watching")
	if strings.TrimSpace(rec.Body.String()) != `{"videos":[]}` {
		t.Errorf("rỗng: %s", rec.Body)
	}
}
```

`internal/playback/handler_test.go` (mới):

```go
package playback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
)

const (
	readyID  = "11111111-1111-1111-1111-111111111111"
	queuedID = "22222222-2222-2222-2222-222222222222"
	otherID  = "99999999-9999-9999-9999-999999999999"
)

type fakeVideos struct{}

func (fakeVideos) Get(_ context.Context, id string) (catalog.Video, error) {
	switch id {
	case readyID:
		return catalog.Video{ID: id, Status: catalog.StatusReady, DurationS: 100}, nil
	case queuedID:
		return catalog.Video{ID: id, Status: catalog.StatusQueued}, nil
	}
	return catalog.Video{}, catalog.ErrNotFound
}

type saved struct {
	user, video string
	pos, dur    int
}

type fakeProgress struct {
	saved []saved
	err   error
}

func (f *fakeProgress) SaveProgress(_ context.Context, userID, videoID string, pos, dur int) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, saved{userID, videoID, pos, dur})
	return nil
}

type fakeSink struct {
	got  []events.PlaybackEvent
	full bool // giả lập BestEffort quá tải
}

func (f *fakeSink) Send(ev events.PlaybackEvent) bool {
	if f.full {
		return false
	}
	f.got = append(f.got, ev)
	return true
}

var now = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

func newHandler(p *fakeProgress, s *fakeSink) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{UserID: "user-1", Role: auth.RoleViewer})))
		})
	})
	(&Handler{Videos: fakeVideos{}, Progress: p, Events: s, Now: func() time.Time { return now }, Log: discard()}).Routes(r)
	return r
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/playback/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEventHandler(t *testing.T) {
	p, s := &fakeProgress{}, &fakeSink{}
	h := newHandler(p, s)
	bodies := []string{
		`{"video_id":"` + readyID + `","type":"play","position_s":0}`,
		`{"video_id":"` + readyID + `","type":"heartbeat","position_s":12.7,"bitrate_kbps":2800}`,
		`{"video_id":"` + readyID + `","type":"buffering","position_s":13,"buffering_ms":850}`,
		`{"video_id":"` + readyID + `","type":"seek","position_s":60}`,
		`{"video_id":"` + readyID + `","type":"pause","position_s":61.2}`,
		`{"video_id":"` + readyID + `","type":"ended","position_s":100.4}`, // vượt thời lượng: kẹp về 100
	}
	for _, b := range bodies {
		if rec := post(h, b); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", b, rec.Code, rec.Body)
		}
	}
	wantSaved := []saved{{"user-1", readyID, 12, 100}, {"user-1", readyID, 61, 100}, {"user-1", readyID, 100, 100}}
	if !reflect.DeepEqual(p.saved, wantSaved) {
		t.Errorf("saved = %+v", p.saved)
	}
	if len(s.got) != 6 {
		t.Fatalf("events = %d, want 6", len(s.got))
	}
	want := events.PlaybackEvent{TS: now, UserID: "user-1", VideoID: readyID, Type: "buffering", PositionS: 13, BufferingMs: 850}
	if s.got[2] != want || s.got[1].BitrateKbps != 2800 || s.got[5].PositionS != 100 {
		t.Errorf("events = %+v", s.got)
	}
}

func TestEventHandlerRejects(t *testing.T) {
	p, s := &fakeProgress{}, &fakeSink{}
	h := newHandler(p, s)
	cases := []struct {
		body string
		want int
	}{
		{`{"video_id":"` + readyID + `","type":"rewind","position_s":1}`, 400},
		{`{"video_id":"` + readyID + `","type":"heartbeat","position_s":-1}`, 400},
		{`{"video_id":"` + readyID + `","type":"heartbeat","position_s":"10"}`, 400},
		{`{"video_id":"` + readyID + `","type":"heartbeat","extra":1}`, 400},
		{`{"video_id":"not-a-uuid","type":"heartbeat"}`, 400},
		{`not json`, 400},
		{`{"video_id":"` + queuedID + `","type":"heartbeat","position_s":1}`, 404},
		{`{"video_id":"` + otherID + `","type":"play","position_s":1}`, 404},
	}
	for _, c := range cases {
		if rec := post(h, c.body); rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.body, rec.Code, c.want)
		}
	}
	if len(p.saved) != 0 || len(s.got) != 0 {
		t.Errorf("không được ghi gì: saved=%v events=%v", p.saved, s.got)
	}
}

func TestEventHandlerKafkaAndDB(t *testing.T) {
	// BestEffort quá tải / Kafka chết: vẫn 204, tiến độ vẫn được ghi (spec §7).
	p := &fakeProgress{}
	h := newHandler(p, &fakeSink{full: true})
	if rec := post(h, `{"video_id":"`+readyID+`","type":"heartbeat","position_s":30}`); rec.Code != http.StatusNoContent || len(p.saved) != 1 {
		t.Errorf("sink đầy: %d saved=%v", rec.Code, p.saved)
	}
	// Lỗi DB khi ghi tiến độ → 500 (player bỏ qua); user/video đã bị xoá → 404.
	for err, want := range map[error]int{errors.New("db down"): 500, ErrNotFound: 404} {
		s := &fakeSink{}
		h := newHandler(&fakeProgress{err: err}, s)
		if rec := post(h, `{"video_id":"`+readyID+`","type":"pause","position_s":30}`); rec.Code != want || len(s.got) != 0 {
			t.Errorf("%v: %d events=%d, want %d", err, rec.Code, len(s.got), want)
		}
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/catalog/ ./internal/playback/ ./cmd/api/`
Expected: FAIL — `unknown field Progress in struct literal of type ViewerHandler`, `undefined: Handler` (playback), `unknown field Events in struct literal of type routes`.

- [ ] **Step 3: Cài đặt**

`cmd/api/main.go` — thay:

```go
// M3: đăng ký/đăng nhập (JWT cookie), /api/admin/* chỉ cho role admin, catalog và /play cho user.
package main
```

bằng:

```go
// M3: đăng ký/đăng nhập (JWT cookie), /api/admin/* chỉ cho role admin, catalog và /play cho user.
// M5: phụ đề (admin upload), poster/sprite ký URL, sự kiện xem → watch_progress + playback.events, "Xem tiếp".
package main
```

`cmd/api/main.go` — thay:

```go
	"streamfilm/internal/events"
	"streamfilm/internal/signing"
```

bằng:

```go
	"streamfilm/internal/events"
	"streamfilm/internal/playback"
	"streamfilm/internal/signing"
```

`cmd/api/main.go` — thay:

```go
	repo := catalog.NewRepo(pool)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw,
```

bằng:

```go
	repo := catalog.NewRepo(pool)
	progress := playback.NewRepo(pool)
	// playback.events: publish ở nền, Kafka chết thì bỏ sự kiện, không làm hỏng việc xem (spec §7).
	playbackEvents := playback.NewBestEffort(producer, playback.PublishTimeout, playback.MaxInFlight, log)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw,
```

`cmd/api/main.go` — thay:

```go
		Auth:      &auth.Handler{Svc: authSvc, SecureCookie: cfg.CookieSecure, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: repo, Signer: signer, CDNBaseURL: cfg.CDNBaseURL, Log: log},
		Upload:    &upload.Handler{Svc: svc, Log: log},
```

bằng:

```go
		Auth:      &auth.Handler{Svc: authSvc, SecureCookie: cfg.CookieSecure, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: repo, Progress: progress, Signer: signer, CDNBaseURL: cfg.CDNBaseURL, Log: log},
		Events:    &playback.Handler{Videos: repo, Progress: progress, Events: playbackEvents, Now: time.Now, Log: log},
		Upload:    &upload.Handler{Svc: svc, Log: log},
```

`cmd/api/main.go` — thay:

```go
	wg.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
```

bằng:

```go
	wg.Wait()
	playbackEvents.Wait() // trước producer.Close (defer): mỗi publish chờ tối đa PublishTimeout
	if errors.Is(serveErr, http.ErrServerClosed) {
```

`cmd/api/router.go` — thay:

```go
	"streamfilm/internal/httpx"
	"streamfilm/internal/upload"
```

bằng:

```go
	"streamfilm/internal/httpx"
	"streamfilm/internal/playback"
	"streamfilm/internal/upload"
```

`cmd/api/router.go` — thay:

```go
	Viewer    *catalog.ViewerHandler
	Upload    *upload.Handler
```

bằng:

```go
	Viewer    *catalog.ViewerHandler
	Events    *playback.Handler
	Upload    *upload.Handler
```

`cmd/api/router.go` — thay:

```go

// newRouter: public (/healthz, /api/auth/*), user (/api/me, /api/videos/*), admin (/api/admin/*).
func newRouter(log *slog.Logger, rt routes) http.Handler {
```

bằng:

```go

// newRouter: public (/healthz, /api/auth/*), user (/api/me, /api/me/continue-watching, /api/videos/*,
// /api/genres, /api/playback/events), admin (/api/admin/*).
func newRouter(log *slog.Logger, rt routes) http.Handler {
```

`cmd/api/router.go` — thay:

```go
		rt.Viewer.Routes(r)
	})
```

bằng:

```go
		rt.Viewer.Routes(r)
		rt.Events.Routes(r)
	})
```

`internal/catalog/repo.go` — thay:

```go
		WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC LIMIT $2`, string(f.Status), f.Limit)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}
```

bằng:

```go
		WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC LIMIT $2`, string(f.Status), f.Limit)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}

// ReadyByIDs trả các video ready trong ids (thứ tự bất kỳ; id không có hoặc chưa ready bị bỏ qua).
func (r *Repo) ReadyByIDs(ctx context.Context, ids []string) ([]Video, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE id = ANY($1::uuid[]) AND status = 'ready'`, ids)
	if err != nil {
		return nil, fmt.Errorf("videos by ids: %w", err)
	}
	return collectVideos(rows)
}
```

`internal/catalog/viewer.go` — thay:

```go

	"streamfilm/internal/httpx"
```

bằng:

```go

	"streamfilm/internal/auth"
	"streamfilm/internal/httpx"
```

`internal/catalog/viewer.go` — thay:

```go
	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
}
```

bằng:

```go
	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
	ReadyByIDs(ctx context.Context, ids []string) ([]Video, error)
}
```

`internal/catalog/viewer.go` — thay:

```go
	Videos     ViewerStore
	Signer     PlaySigner
```

bằng:

```go
	Videos     ViewerStore
	Progress   ProgressSource // playback.Repo: resume_at và hàng "Xem tiếp"
	Signer     PlaySigner
```

`internal/catalog/viewer.go` — thay:

```go
	r.Get("/api/videos/{id}/play", h.play)
}
```

bằng:

```go
	r.Get("/api/videos/{id}/play", h.play)
	r.Get("/api/me/continue-watching", h.continueWatching)
}
```

`internal/catalog/viewer.go` — thay:

```go
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}
```

bằng:

```go
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}

// ContinueJSON: một video của hàng "Xem tiếp" — thông tin như catalog kèm vị trí đã xem.
type ContinueJSON struct {
	ViewerVideoJSON
	PositionS int       `json:"position_s"`
	WatchedAt time.Time `json:"watched_at"`
}
```

`internal/catalog/viewer.go` — thay:

```go
	out.ThumbnailsURL, _ = h.signFile(v.ID, v.SpriteKey)
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
```

bằng:

```go
	out.ThumbnailsURL, _ = h.signFile(v.ID, v.SpriteKey)
	// resume_at lỗi thì phát từ đầu: không vì tiến độ mà chặn việc xem.
	if id, ok := auth.FromContext(r.Context()); ok {
		if at, err := h.Progress.ResumeAt(r.Context(), id.UserID, v.ID); err != nil {
			h.Log.Warn("resume_at", "video_id", v.ID, "err", err)
		} else {
			out.ResumeAt = at
		}
	}
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
```

`internal/catalog/viewer.go` — thay:

```go
	return h.CDNBaseURL + p, true
}
```

bằng:

```go
	return h.CDNBaseURL + p, true
}

// continueWatching: GET /api/me/continue-watching → {"videos": [...]} tối đa ContinueWatchingLimit video
// đang xem dở (5%–95%), mới xem nhất trước (spec §4.4).
func (h *ViewerHandler) continueWatching(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}
	entries, err := h.Progress.ContinueWatching(r.Context(), id.UserID, ContinueWatchingLimit)
	if err != nil {
		h.Log.Error("continue watching", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.VideoID
	}
	vs, err := h.Videos.ReadyByIDs(r.Context(), ids)
	if err != nil {
		h.Log.Error("continue watching videos", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	byID := make(map[string]Video, len(vs))
	for _, v := range vs {
		byID[v.ID] = v
	}
	out := make([]ContinueJSON, 0, len(entries))
	for _, e := range entries { // giữ thứ tự updated_at DESC của watch_progress
		if v, ok := byID[e.VideoID]; ok {
			out = append(out, ContinueJSON{ViewerVideoJSON: h.toViewer(v), PositionS: e.PositionS, WatchedAt: e.UpdatedAt})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"videos": out})
}
```

`internal/playback/handler.go` (mới):

```go
package playback

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
	"streamfilm/internal/httpx"
)

type VideoGetter interface {
	Get(ctx context.Context, id string) (catalog.Video, error)
}

type ProgressWriter interface {
	SaveProgress(ctx context.Context, userID, videoID string, positionS, durationS int) error
}

// EventSink nhận sự kiện để publish ở nền (*BestEffort).
type EventSink interface {
	Send(ev events.PlaybackEvent) bool
}

// Handler: POST /api/playback/events (router bọc auth.RequireUser).
type Handler struct {
	Videos   VideoGetter
	Progress ProgressWriter
	Events   EventSink
	Now      func() time.Time
	Log      *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/playback/events", h.event)
}

// event: kiểm tra input và video (ready), heartbeat/pause/ended thì upsert watch_progress, rồi đưa sự kiện
// cho BestEffort publish lên playback.events. Kafka lỗi không ảnh hưởng mã trả về (204).
func (h *Handler) event(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}
	var in EventInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	pos, err := in.Normalize()
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := h.Videos.Get(r.Context(), in.VideoID)
	if errors.Is(err, catalog.ErrNotFound) || (err == nil && v.Status != catalog.StatusReady) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.Error("get video", "video_id", in.VideoID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if v.DurationS > 0 {
		pos = min(pos, v.DurationS)
	}
	if in.Type.SavesProgress() {
		err := h.Progress.SaveProgress(r.Context(), id.UserID, v.ID, pos, v.DurationS)
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "video not found")
			return
		}
		if err != nil {
			h.Log.Error("save progress", "video_id", v.ID, "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	h.Events.Send(events.PlaybackEvent{TS: h.Now().UTC(), UserID: id.UserID, VideoID: v.ID, Type: string(in.Type),
		PositionS: pos, BitrateKbps: in.BitrateKbps, BufferingMs: in.BufferingMs})
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal cmd; go vet ./... && go test ./...`
Expected: mọi package `ok` (gồm integration với Docker/FFmpeg; `TestRepoReadyByIDs`, `TestViewerContinueWatching`, `TestViewerResumeAt`, `TestEventHandler*`, router: `/api/me/continue-watching` và `/api/playback/events` không cookie → `401`).

- [ ] **Step 5: Commit**

```bash
git add internal/catalog internal/playback cmd/api
git commit -m "feat(api): playback events, continue watching and resume_at"
```

---

### Task 8: nginx — không cache phụ đề; kiểm tra Content-Type ảnh và VTT

**Files:**
- Modify: `deploy/nginx/templates/default.conf.template`, `internal/signing/nginx_test.go`

**Interfaces:**
- Consumes: biến `$sl_file` của location `/cdn/s/…` (M3), `SignedPath`, test `TestNginxSecureLinkParity` (container origin đóng vai MinIO).
- Produces: `map $sl_file $sl_no_cache`; `subs/*` trả `X-Cache-Status: BYPASS`; `.jpg` → `image/jpeg`, `.vtt` → `text/vtt` đi nguyên từ origin.

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/signing/nginx_test.go` — thay:

```go
// cổng 8000) trả lại URI nhận được để kiểm tra proxy /api/ giữ nguyên đường dẫn và query.
const originConf = `server { listen 9000; root /srv; }
server { listen 8000; location / { return 200 "api:$request_uri"; } }`
```

bằng:

```go
// cổng 8000) trả lại URI nhận được để kiểm tra proxy /api/ giữ nguyên đường dẫn và query.
const originConf = `server { listen 9000; root /srv;
  types { application/vnd.apple.mpegurl m3u8; video/mp2t ts; image/jpeg jpg; text/vtt vtt; } }
server { listen 8000; location / { return 200 "api:$request_uri"; } }`
```

`internal/signing/nginx_test.go` — thay:

```go
				ContainerFilePath: "/srv/hls/" + testVID + "/720p/seg_000.ts", FileMode: 0o644},
		),
```

bằng:

```go
				ContainerFilePath: "/srv/hls/" + testVID + "/720p/seg_000.ts", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("jpeg-bytes"),
				ContainerFilePath: "/srv/hls/" + testVID + "/poster.jpg", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("WEBVTT\n\nsprite_001.jpg#xywh=0,0,160,90\n"),
				ContainerFilePath: "/srv/hls/" + testVID + "/thumbnails.vtt", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("WEBVTT\n\nold"),
				ContainerFilePath: "/srv/hls/" + testVID + "/subs/vi.vtt", FileMode: 0o644},
		),
```

`internal/signing/nginx_test.go` — thay:

```go
	}
}
```

bằng:

```go
	}

	// M5: poster, thumbnails.vtt, phụ đề đi chung prefix ký; Content-Type giữ nguyên từ origin (MinIO).
	for file, ct := range map[string]string{"poster.jpg": "image/jpeg", "thumbnails.vtt": "text/vtt", "subs/vi.vtt": "text/vtt"} {
		resp, err := http.Get(endpoint + SignedPath(testSecret, testVID, file, exp))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != ct {
			t.Errorf("%s: %d %q, want 200 %q", file, resp.StatusCode, resp.Header.Get("Content-Type"), ct)
		}
	}
	// Phụ đề không cache ở CDN: admin upload lại thì lần tải sau thấy bản mới ngay.
	subs := SignedPath(testSecret, testVID, "subs/vi.vtt", exp)
	if err := origin.CopyToContainer(ctx, []byte("WEBVTT\n\nnew"), "/srv/hls/"+testVID+"/subs/vi.vtt", 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(endpoint + subs)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "WEBVTT\n\nnew" || resp.Header.Get("X-Cache-Status") != "BYPASS" {
		t.Errorf("subs sau khi đổi: %q, X-Cache-Status = %q, want bản mới và BYPASS", body, resp.Header.Get("X-Cache-Status"))
	}
	poster := SignedPath(testSecret, testVID, "poster.jpg", exp+60)
	if resp, err := http.Get(endpoint + poster); err != nil || resp.Header.Get("X-Cache-Status") != "HIT" {
		t.Errorf("poster lần hai phải HIT: %v %v", err, resp.Header.Get("X-Cache-Status"))
	} else {
		resp.Body.Close()
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -run TestNginxSecureLinkParity ./internal/signing/`
Expected: FAIL — `subs sau khi đổi: "WEBVTT\n\nold", X-Cache-Status = "HIT", want bản mới và BYPASS`.

- [ ] **Step 3: Cài đặt**

`deploy/nginx/templates/default.conf.template` — thay:

```nginx
# Cổng vào duy nhất của trình duyệt (M4, ADR 0011): SPA người xem ở /, API ở /api/ (proxy tới api:8000),
# "CDN" giả lập ở /cdn/ (chỉ phục vụ HLS qua URL có chữ ký, M3). Cùng origin nên cookie HttpOnly
# của API dùng được mà không cần CORS.
```

bằng:

```nginx
# Cổng vào duy nhất của trình duyệt (M4, ADR 0011): SPA người xem ở /, API ở /api/ (proxy tới api:8000),
# "CDN" giả lập ở /cdn/ (chỉ phục vụ HLS, poster, sprite, phụ đề qua URL có chữ ký, M3/M5). Content-Type
# lấy từ object MinIO (storage.ContentTypeFor: .m3u8, .ts, .jpg → image/jpeg, .vtt → text/vtt). Cùng origin nên cookie HttpOnly
# của API dùng được mà không cần CORS.
```

`deploy/nginx/templates/default.conf.template` — thay:

```nginx
proxy_cache_path /var/cache/nginx/hls levels=1:2 keys_zone=hls:10m max_size=1g inactive=7d use_temp_path=off;
```

bằng:

```nginx
proxy_cache_path /var/cache/nginx/hls levels=1:2 keys_zone=hls:10m max_size=1g inactive=7d use_temp_path=off;

# Phụ đề (subs/{lang}.vtt) bị admin upload lại đè cùng key bất cứ lúc nào (M5): không cache ở CDN để người xem
# thấy bản mới ngay. Segment, playlist, poster, sprite chỉ đổi khi transcode lại nên vẫn cache 7 ngày.
map $sl_file $sl_no_cache {
    ~^subs/  1;
    default  0;
}
```

`deploy/nginx/templates/default.conf.template` — thay:

```nginx
        proxy_cache_valid 200 7d;
        # Không đưa chữ ký vào cache key: mọi người xem dùng chung một bản cache của segment.
```

bằng:

```nginx
        proxy_cache_valid 200 7d;
        proxy_no_cache $sl_no_cache;
        proxy_cache_bypass $sl_no_cache;
        # Không đưa chữ ký vào cache key: mọi người xem dùng chung một bản cache của segment.
```

- [ ] **Step 4: Chạy test**

Run: `go test -run TestNginxSecureLinkParity -v ./internal/signing/ 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: TestNginxSecureLinkParity`, `ok  streamfilm/internal/signing`.

- [ ] **Step 5: Commit**

```bash
git add deploy/nginx/templates/default.conf.template internal/signing/nginx_test.go
git commit -m "feat(nginx): bypass CDN cache for subtitles; test poster and VTT content types"
```

---

### Task 9: Web — poster thật và hàng "Xem tiếp"

**Files:**
- Create: `web/app/src/components/Poster.tsx`, `Poster.test.tsx`, `ContinueRow.tsx`, `ContinueRow.test.tsx`
- Modify: `web/app/src/api.ts`, `api.test.ts`, `format.ts`, `format.test.ts`, `components/VideoGrid.tsx`, `pages/HomePage.tsx`, `pages/VideoPage.tsx`, `styles.css`

**Interfaces:**
- Consumes: `GET /api/videos` / `/api/videos/{id}` (`poster_url`, Task 5), `GET /api/me/continue-watching` (Task 7), `POST /api/playback/events` (Task 7).
- Produces (TS):
  ```ts
  interface Video { …; poster_url?: string }
  interface ContinueVideo extends Video { position_s: number; watched_at: string }
  interface PlayInfo { url; expires_at; subtitles: Subtitle[]; thumbnails_url?: string; resume_at: number }
  type PlaybackEventType = 'play' | 'pause' | 'seek' | 'ended' | 'buffering' | 'heartbeat'
  interface PlaybackEvent { video_id: string; type: PlaybackEventType; position_s: number; bitrate_kbps: number; buffering_ms: number }
  api.continueWatching(): Promise<ContinueVideo[]>
  export function sendPlaybackEvent(ev: PlaybackEvent): void
  export function progressPercent(positionS: number, durationS: number): number   // format.ts
  export function Poster(props: { video: Pick<Video, 'id' | 'title' | 'poster_url'>; large?: boolean }): JSX.Element
  export function ContinueRow(props: { videos: ContinueVideo[] }): JSX.Element | null
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

`web/app/src/api.test.ts` — thay:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, setUnauthorizedHandler, videosQueryString } from './api';
```

bằng:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, sendPlaybackEvent, setUnauthorizedHandler, videosQueryString } from './api';
```

`web/app/src/api.test.ts` — thay:

```ts
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json');
  });
});
```

bằng:

```ts
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json');
  });
});

describe('M5', () => {
  it('continueWatching trả mảng videos', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ videos: [{ id: 'v1', position_s: 42 }] }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const vs = await api.continueWatching();
    expect(vs).toEqual([{ id: 'v1', position_s: 42 }]);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/me/continue-watching');
  });

  it('sendPlaybackEvent: POST JSON keepalive, lỗi và 401 bị bỏ qua', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{"error":"login required"}', { status: 401 }));
    vi.stubGlobal('fetch', fetchMock);
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    const ev = { video_id: 'v1', type: 'heartbeat' as const, position_s: 12.5, bitrate_kbps: 2800, buffering_ms: 0 };
    sendPlaybackEvent(ev);
    await Promise.resolve();
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/playback/events');
    expect(init).toMatchObject({ method: 'POST', keepalive: true, credentials: 'same-origin' });
    expect(JSON.parse(init.body as string)).toEqual(ev);
    expect(onUnauthorized).not.toHaveBeenCalled();

    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('network down')));
    expect(() => sendPlaybackEvent(ev)).not.toThrow();
  });
});
```

`web/app/src/components/ContinueRow.test.tsx` (mới):

```tsx
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { ContinueVideo } from '../api';
import { ContinueRow } from './ContinueRow';

const cv = (id: string, title: string, position_s: number, duration_s: number): ContinueVideo => ({
  id,
  title,
  description: '',
  genres: [],
  duration_s,
  created_at: '2026-11-15T00:00:00Z',
  position_s,
  watched_at: '2026-11-15T10:00:00Z',
});

describe('ContinueRow', () => {
  it('mỗi video một thẻ dẫn tới trang xem, có thanh tiến độ và thời gian còn lại', () => {
    render(
      <MemoryRouter>
        <ContinueRow
          videos={[
            cv('11111111-1111-1111-1111-111111111111', 'Trò chơi con mực', 1800, 3600),
            cv('22222222-2222-2222-2222-222222222222', 'Đường về nhà', 12, 30),
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { name: 'Xem tiếp' })).toBeInTheDocument();
    const links = screen.getAllByRole('link');
    expect(links[0]).toHaveAttribute('href', '/watch/11111111-1111-1111-1111-111111111111');
    const bars = screen.getAllByRole('progressbar');
    expect(bars[0]).toHaveAttribute('aria-valuenow', '50');
    expect(bars[1]).toHaveAttribute('aria-valuenow', '40');
    expect(screen.getByText('Còn 30:00')).toBeInTheDocument();
    expect(screen.getByText('Còn 0:18')).toBeInTheDocument();
  });

  it('rỗng thì không hiện gì', () => {
    const { container } = render(
      <MemoryRouter>
        <ContinueRow videos={[]} />
      </MemoryRouter>,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
```

`web/app/src/components/Poster.test.tsx` (mới):

```tsx
import { fireEvent, render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Poster } from './Poster';

const id = '11111111-1111-1111-1111-111111111111';

describe('Poster', () => {
  it('có poster_url thì hiện ảnh (lazy)', () => {
    const { container } = render(<Poster video={{ id, title: 'Phim', poster_url: '/cdn/s/x/1/hls/v/poster.jpg' }} />);
    const img = container.querySelector('img.poster');
    expect(img).toHaveAttribute('src', '/cdn/s/x/1/hls/v/poster.jpg');
    expect(img).toHaveAttribute('loading', 'lazy');
  });

  it('không có poster_url hoặc ảnh lỗi thì hiện khối màu với chữ cái đầu', () => {
    const { container, rerender } = render(<Poster video={{ id, title: 'đường về' }} large />);
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('.poster.large')).toHaveTextContent('Đ');

    rerender(<Poster video={{ id, title: 'đường về', poster_url: '/cdn/s/x/1/hls/v/poster.jpg' }} />);
    fireEvent.error(container.querySelector('img')!);
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('.poster')).toHaveTextContent('Đ');
  });
});
```

`web/app/src/format.test.ts` — thay:

```ts
import { describe, expect, it } from 'vitest';
import { formatDuration, posterHue } from './format';
```

bằng:

```ts
import { describe, expect, it } from 'vitest';
import { formatDuration, posterHue, progressPercent } from './format';
```

`web/app/src/format.test.ts` — thay:

```ts
    expect(h).toBeLessThan(360);
  });
});
```

bằng:

```ts
    expect(h).toBeLessThan(360);
  });
});

describe('progressPercent', () => {
  it('làm tròn và kẹp trong 0..100', () => {
    expect(progressPercent(42, 100)).toBe(42);
    expect(progressPercent(10, 30)).toBe(33);
    expect(progressPercent(150, 100)).toBe(100);
    expect(progressPercent(-5, 100)).toBe(0);
    expect(progressPercent(10, 0)).toBe(0);
  });
});
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `cd web/app && npm test`
Expected: FAIL — `Failed to resolve import "./Poster"`, `"./ContinueRow"`; `progressPercent is not a function`; `sendPlaybackEvent` không có.

- [ ] **Step 3: Cài đặt**

`web/app/src/api.ts` — thay:

```ts
  created_at: string;
  renditions?: Rendition[];
}
```

bằng:

```ts
  created_at: string;
  // URL ký (/cdn/s/...) của poster.jpg; không có với video transcode trước M5.
  poster_url?: string;
  renditions?: Rendition[];
}

// Một video của hàng "Xem tiếp" (GET /api/me/continue-watching).
export interface ContinueVideo extends Video {
  position_s: number;
  watched_at: string;
}
```

`web/app/src/api.ts` — thay:

```ts
  subtitles: Subtitle[];
  resume_at: number;
}
```

bằng:

```ts
  subtitles: Subtitle[];
  // thumbnails.vtt (sprite xem trước khi tua); không có với video transcode trước M5.
  thumbnails_url?: string;
  resume_at: number;
}

export type PlaybackEventType = 'play' | 'pause' | 'seek' | 'ended' | 'buffering' | 'heartbeat';

// Body của POST /api/playback/events (spec §4.4).
export interface PlaybackEvent {
  video_id: string;
  type: PlaybackEventType;
  position_s: number;
  bitrate_kbps: number;
  buffering_ms: number;
}
```

`web/app/src/api.ts` — thay:

```ts
  genres: () => request<{ genres: string[] }>('/api/genres').then((r) => r.genres),
};
```

bằng:

```ts
  genres: () => request<{ genres: string[] }>('/api/genres').then((r) => r.genres),
  continueWatching: () =>
    request<{ videos: ContinueVideo[] }>('/api/me/continue-watching').then((r) => r.videos),
};

// sendPlaybackEvent gửi sự kiện xem và không chờ kết quả: lỗi (mạng, 401, Kafka chết phía server) bị bỏ qua
// để không bao giờ ảnh hưởng việc xem. keepalive: request vẫn đi khi tab đang đóng (pagehide).
// Không qua request() nên 401 ở đây không đẩy người dùng về trang đăng nhập giữa lúc đang xem.
export function sendPlaybackEvent(ev: PlaybackEvent): void {
  try {
    void fetch('/api/playback/events', {
      method: 'POST',
      credentials: 'same-origin',
      keepalive: true,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(ev),
    }).catch(() => undefined);
  } catch {
    // fetch có thể ném đồng bộ (ví dụ vượt hạn mức keepalive 64 KB): bỏ qua
  }
}
```

`web/app/src/components/ContinueRow.tsx` (mới):

```tsx
import { Link } from 'react-router-dom';
import type { ContinueVideo } from '../api';
import { formatDuration, progressPercent } from '../format';
import { Poster } from './Poster';

// ContinueRow: hàng "Xem tiếp" trên trang chủ (spec §4.4). Thẻ dẫn thẳng tới trang xem; player phát tiếp
// từ resume_at mà /play trả về. Không có video nào thì không hiện gì.
export function ContinueRow({ videos }: { videos: ContinueVideo[] }) {
  if (videos.length === 0) return null;
  return (
    <section className="continue" aria-labelledby="continue-heading">
      <h2 id="continue-heading">Xem tiếp</h2>
      <ul className="row">
        {videos.map((v) => {
          const pct = progressPercent(v.position_s, v.duration_s);
          return (
            <li key={v.id}>
              <Link to={`/watch/${v.id}`} className="card">
                <Poster video={v} />
                <div
                  className="progress"
                  role="progressbar"
                  aria-label={`Đã xem ${pct}%`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={pct}
                >
                  <span style={{ width: `${pct}%` }} />
                </div>
                <h3>{v.title}</h3>
                <p className="meta">Còn {formatDuration(Math.max(v.duration_s - v.position_s, 0)) || '0:00'}</p>
              </Link>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
```

`web/app/src/components/Poster.tsx` (mới):

```tsx
import { useState } from 'react';
import type { Video } from '../api';
import { posterHue } from '../format';

export interface PosterProps {
  video: Pick<Video, 'id' | 'title' | 'poster_url'>;
  large?: boolean;
}

// Poster: ảnh poster.jpg qua URL ký (M5). Video chưa có poster hoặc ảnh lỗi (ví dụ URL ký đã hết hạn khi
// trang mở quá lâu) thì hiện khối màu theo id kèm chữ cái đầu như M4.
export function Poster({ video, large = false }: PosterProps) {
  const [failedUrl, setFailedUrl] = useState('');
  const className = large ? 'poster large' : 'poster';
  if (video.poster_url && video.poster_url !== failedUrl) {
    return (
      <img
        className={className}
        src={video.poster_url}
        alt=""
        loading="lazy"
        onError={() => setFailedUrl(video.poster_url ?? '')}
      />
    );
  }
  return (
    <div className={className} style={{ background: `hsl(${posterHue(video.id)} 45% 30%)` }} aria-hidden="true">
      {video.title.charAt(0).toUpperCase()}
    </div>
  );
}
```

`web/app/src/components/VideoGrid.tsx` — thay:

```tsx
import type { Video } from '../api';
import { formatDuration, posterHue } from '../format';
```

bằng:

```tsx
import type { Video } from '../api';
import { formatDuration } from '../format';
import { Poster } from './Poster';
```

`web/app/src/components/VideoGrid.tsx` — thay:

```tsx
          <Link to={`/videos/${v.id}`} className="card">
            {/* Poster thật (poster_key) có ở M5; tạm dùng khối màu theo id. */}
            <div className="poster" style={{ background: `hsl(${posterHue(v.id)} 45% 30%)` }} aria-hidden="true">
              {v.title.charAt(0).toUpperCase()}
            </div>
            <h3>{v.title}</h3>
```

bằng:

```tsx
          <Link to={`/videos/${v.id}`} className="card">
            <Poster video={v} />
            <h3>{v.title}</h3>
```

`web/app/src/format.ts` — thay:

```ts

// posterHue: màu ổn định theo id cho poster tạm (poster thật có ở M5).
export function posterHue(id: string): number {
```

bằng:

```ts

// progressPercent: phần trăm đã xem (0–100, làm tròn) cho thanh tiến độ của hàng "Xem tiếp".
export function progressPercent(positionS: number, durationS: number): number {
  if (!durationS || durationS <= 0) return 0;
  return Math.round(Math.min(Math.max(positionS / durationS, 0), 1) * 100);
}

// posterHue: màu ổn định theo id cho poster tạm (video transcode trước M5 hoặc ảnh không tải được).
export function posterHue(id: string): number {
```

`web/app/src/pages/HomePage.tsx` — thay:

```tsx
import { useEffect, useState } from 'react';
import { api, type Video } from '../api';
import { VideoGrid } from '../components/VideoGrid';
```

bằng:

```tsx
import { useEffect, useState } from 'react';
import { api, type ContinueVideo, type Video } from '../api';
import { ContinueRow } from '../components/ContinueRow';
import { VideoGrid } from '../components/VideoGrid';
```

`web/app/src/pages/HomePage.tsx` — thay:

```tsx
  const [error, setError] = useState('');
```

bằng:

```tsx
  const [error, setError] = useState('');
  const [continueVideos, setContinueVideos] = useState<ContinueVideo[]>([]);

  // Hàng "Xem tiếp": lỗi thì chỉ ẩn hàng, không chặn catalog.
  useEffect(() => {
    let cancelled = false;
    api
      .continueWatching()
      .then((vs) => !cancelled && setContinueVideos(vs))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);
```

`web/app/src/pages/HomePage.tsx` — thay:

```tsx
    <section>
      <h1>Phim mới</h1>
```

bằng:

```tsx
    <section>
      <ContinueRow videos={continueVideos} />
      <h1>Phim mới</h1>
```

`web/app/src/pages/VideoPage.tsx` — thay:

```tsx
import { ApiError, api, type Video } from '../api';
import { formatDuration, posterHue } from '../format';
```

bằng:

```tsx
import { ApiError, api, type Video } from '../api';
import { Poster } from '../components/Poster';
import { formatDuration } from '../format';
```

`web/app/src/pages/VideoPage.tsx` — thay:

```tsx
    <article className="detail">
      <div className="poster large" style={{ background: `hsl(${posterHue(video.id)} 45% 30%)` }} aria-hidden="true">
        {video.title.charAt(0).toUpperCase()}
      </div>
      <div>
```

bằng:

```tsx
    <article className="detail">
      <Poster video={video} large />
      <div>
```

`web/app/src/styles.css` — thay:

```css

.poster.large {
```

bằng:

```css

img.poster {
  display: block;
  width: 100%;
  object-fit: cover;
  background: var(--surface);
}

.continue {
  margin-bottom: 2rem;
}

.continue h2 {
  font-size: 1.25rem;
  margin: 0 0 0.75rem;
}

/* Hàng ngang cuộn được: tối đa 20 thẻ. */
.row {
  list-style: none;
  margin: 0;
  padding: 0 0 0.5rem;
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: minmax(200px, 240px);
  gap: 1.25rem;
  overflow-x: auto;
}

.progress {
  height: 4px;
  margin-top: 0.4rem;
  background: var(--border);
  border-radius: 2px;
  overflow: hidden;
}

.progress span {
  display: block;
  height: 100%;
  background: var(--accent);
}

.poster.large {
```

- [ ] **Step 4: Chạy test và build**

Run: `cd web/app && npx tsc --noEmit && npm test && npm run build`
Expected: `Test Files  11 passed (11)`, `Tests  39 passed (39)`; build tạo `dist/assets/index-*.js` (~198 kB) và `WatchPage-*.js` (~510 kB).

- [ ] **Step 5: Commit**

```bash
git add web/app/src
git commit -m "feat(web): posters and continue-watching row"
```

---

### Task 10: Web — phụ đề, ảnh xem trước khi tua, gửi sự kiện xem

**Files:**
- Create: `web/app/src/player/thumbnails.ts`, `thumbnails.test.ts`, `player/reporter.ts`, `reporter.test.ts`, `components/SeekPreview.tsx`, `SeekPreview.test.tsx`
- Modify: `web/app/src/player/recovery.ts`, `player/recovery.test.ts`, `components/Player.tsx`, `styles.css`

**Interfaces:**
- Consumes: `PlayInfo.subtitles/thumbnails_url` (Task 5, 9), `sendPlaybackEvent` (Task 9), `formatDuration`, `PlaybackSession`.
- Produces (TS):
  ```ts
  interface ThumbCue { start: number; end: number; url: string; x: number; y: number; w: number; h: number }
  export function parseVttTime(s: string): number | null
  export function parseThumbnailsVtt(text: string, baseUrl: string): ThumbCue[]
  export function findThumb(cues: ThumbCue[], t: number): ThumbCue | null
  export const HEARTBEAT_MS = 10_000
  interface ReporterDeps { videoId: string; send: (ev: PlaybackEvent) => void; position: () => number; bitrateKbps: () => number; now?: () => number; heartbeatMs?: number }
  class PlaybackReporter { play(); pause(); seeked(); ended(); waiting(); playing(); flush(); dispose() }
  interface PlaybackDeps { …; onInfo?: (p: PlayInfo) => void }   // recovery.ts
  export function SeekPreview(props: { videoRef: RefObject<HTMLVideoElement | null>; cues: ThumbCue[] }): JSX.Element | null
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

`web/app/src/components/SeekPreview.test.tsx` (mới):

```tsx
import { act, fireEvent, render, screen } from '@testing-library/react';
import { createRef } from 'react';
import { describe, expect, it } from 'vitest';
import type { ThumbCue } from '../player/thumbnails';
import { SeekPreview } from './SeekPreview';

const cues: ThumbCue[] = [
  { start: 0, end: 10, url: 'http://x/hls/v/sprite_001.jpg', x: 0, y: 0, w: 160, h: 90 },
  { start: 10, end: 20, url: 'http://x/hls/v/sprite_001.jpg', x: 160, y: 0, w: 160, h: 90 },
  { start: 20, end: 30, url: 'http://x/hls/v/sprite_001.jpg', x: 320, y: 0, w: 160, h: 90 },
];

// fakeVideo: jsdom không phát video; đặt duration/currentTime bằng defineProperty.
function fakeVideo(duration: number) {
  const video = document.createElement('video');
  let t = 0;
  Object.defineProperty(video, 'duration', { configurable: true, get: () => duration });
  Object.defineProperty(video, 'currentTime', {
    configurable: true,
    get: () => t,
    set: (v: number) => {
      t = v;
    },
  });
  return video;
}

function renderPreview(duration = 30) {
  const video = fakeVideo(duration);
  const ref = createRef<HTMLVideoElement>();
  (ref as { current: HTMLVideoElement | null }).current = video;
  render(<SeekPreview videoRef={ref} cues={cues} />);
  const bar = screen.getByRole('slider', { name: 'Tua video' });
  bar.getBoundingClientRect = () => ({ left: 100, width: 300, top: 0, height: 10, right: 400, bottom: 10, x: 100, y: 0, toJSON: () => ({}) });
  return { video, bar };
}

describe('SeekPreview', () => {
  it('rê chuột hiện ô sprite đúng thời điểm và mốc thời gian', () => {
    const { bar } = renderPreview();
    fireEvent.mouseMove(bar, { clientX: 100 + 150 }); // giữa thanh = 15s → ô thứ 2
    const thumb = screen.getByTestId('seek-preview').querySelector('.scrub-thumb') as HTMLElement;
    expect(thumb.style.backgroundPosition).toBe('-160px 0px');
    expect(thumb.style.backgroundImage).toContain('sprite_001.jpg');
    expect(screen.getByText('0:15')).toBeInTheDocument();
    fireEvent.mouseLeave(bar);
    expect(screen.queryByTestId('seek-preview')).toBeNull();
  });

  it('bấm để tua, phím mũi tên tua 10 giây', () => {
    const { video, bar } = renderPreview();
    fireEvent.click(bar, { clientX: 100 + 250 }); // 25s
    expect(video.currentTime).toBe(25);
    act(() => {
      video.dispatchEvent(new Event('timeupdate'));
    });
    expect(bar).toHaveAttribute('aria-valuenow', '25');
    fireEvent.keyDown(bar, { key: 'ArrowRight' });
    expect(video.currentTime).toBe(30); // kẹp ở duration
    fireEvent.keyDown(bar, { key: 'ArrowLeft' });
    expect(video.currentTime).toBe(15); // current trong state vẫn là 25 → 15
  });

  it('chưa biết thời lượng thì không hiện', () => {
    const video = fakeVideo(Number.NaN);
    const ref = { current: video };
    const { container } = render(<SeekPreview videoRef={ref} cues={cues} />);
    expect(container).toBeEmptyDOMElement();
  });
});
```

`web/app/src/player/recovery.test.ts` — thay:

```ts
    expect(r.next()).toBe('recover');
  });
});
```

bằng:

```ts
    expect(r.next()).toBe('recover');
  });
});

describe('PlaybackSession onInfo', () => {
  it('nhận PlayInfo lần đầu và sau mỗi lần làm mới, trước load', async () => {
    const order: string[] = [];
    let n = 0;
    const session = new PlaybackSession({
      fetchPlay: async () => play(++n),
      onInfo: (p) => order.push(`info:${p.url}`),
      load: (url) => order.push(`load:${url}`),
      onFatal: vi.fn(),
      currentTime: () => 0,
    });
    await session.start();
    await session.refresh();
    expect(order).toEqual([`info:${play(1).url}`, `load:${play(1).url}`, `info:${play(2).url}`, `load:${play(2).url}`]);
  });
});
```

`web/app/src/player/reporter.test.ts` (mới):

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PlaybackEvent } from '../api';
import { PlaybackReporter } from './reporter';

function setup() {
  const sent: PlaybackEvent[] = [];
  let pos = 0;
  let clock = 1_000;
  const r = new PlaybackReporter({
    videoId: 'v1',
    send: (ev) => sent.push(ev),
    position: () => pos,
    bitrateKbps: () => 2799.6,
    now: () => clock,
  });
  return {
    r,
    sent,
    types: () => sent.map((e) => e.type),
    setPos: (p: number) => (pos = p),
    advance: (ms: number) => (clock += ms),
  };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('PlaybackReporter', () => {
  it('heartbeat mỗi 10 giây chỉ khi đang phát', () => {
    const { r, types, setPos, sent } = setup();
    r.play();
    setPos(10.12345);
    vi.advanceTimersByTime(10_000);
    setPos(20);
    vi.advanceTimersByTime(10_000);
    expect(types()).toEqual(['play', 'heartbeat', 'heartbeat']);
    expect(sent[1]).toEqual({ video_id: 'v1', type: 'heartbeat', position_s: 10.123, bitrate_kbps: 2800, buffering_ms: 0 });
    r.pause();
    vi.advanceTimersByTime(60_000);
    expect(types()).toEqual(['play', 'heartbeat', 'heartbeat', 'pause']);
    r.play();
    r.play(); // play hai lần không tạo hai interval
    vi.advanceTimersByTime(10_000);
    expect(types().filter((t) => t === 'heartbeat')).toHaveLength(3);
    r.dispose();
  });

  it('seek, ended; pause/seek trước lần play đầu (nạp nguồn, tua tới resume_at) bị bỏ qua', () => {
    const { r, types } = setup();
    r.seeked();
    r.pause();
    expect(types()).toEqual([]);
    r.play();
    r.seeked();
    r.ended();
    vi.advanceTimersByTime(30_000);
    expect(types()).toEqual(['play', 'seek', 'ended']);
  });

  it('buffering: thời gian từ waiting đầu tiên tới playing', () => {
    const { r, sent, advance } = setup();
    r.play();
    r.waiting();
    advance(400);
    r.waiting(); // lần chờ thứ hai trong cùng một đợt không đặt lại mốc
    advance(450);
    r.playing();
    r.playing(); // không chờ thì không gửi
    expect(sent.filter((e) => e.type === 'buffering')).toEqual([
      { video_id: 'v1', type: 'buffering', position_s: 0, bitrate_kbps: 2800, buffering_ms: 850 },
    ]);
  });

  it('flush gửi heartbeat vị trí cuối khi đã xem', () => {
    const { r, types, setPos } = setup();
    r.flush();
    expect(types()).toEqual([]); // chưa xem
    r.play();
    setPos(42);
    r.flush();
    expect(types()).toEqual(['play', 'heartbeat']);
  });
});
```

`web/app/src/player/thumbnails.test.ts` (mới):

```ts
import { describe, expect, it } from 'vitest';
import { findThumb, parseThumbnailsVtt, parseVttTime } from './thumbnails';

const base = 'http://localhost:8088/cdn/s/SIG/1794736800/hls/v1/thumbnails.vtt';
const vtt =
  'WEBVTT\r\n\r\n00:00:00.000 --> 00:00:10.000\r\nsprite_001.jpg#xywh=0,0,160,90\r\n\r\n' +
  '00:00:10.000 --> 00:00:20.000\r\nsprite_001.jpg#xywh=160,0,160,90\r\n\r\n' +
  '00:16:40.000 --> 00:16:45.500 align:start\r\nsprite_002.jpg#xywh=0,0,160,90\r\n\r\n' +
  '00:00:30.000 --> 00:00:40.000\r\nkhông-có-xywh.jpg\r\n';

describe('parseVttTime', () => {
  it('có và không có giờ; sai dạng → null', () => {
    expect(parseVttTime('01:02:03.500')).toBe(3723.5);
    expect(parseVttTime(' 02:03.250 ')).toBe(123.25);
    expect(parseVttTime('00:00:61.000')).toBeNull();
    expect(parseVttTime('1:2:3')).toBeNull();
  });
});

describe('parseThumbnailsVtt', () => {
  it('đọc cue, resolve sprite theo URL ký của file VTT, bỏ cue thiếu #xywh', () => {
    const cues = parseThumbnailsVtt(vtt, base);
    expect(cues).toHaveLength(3);
    expect(cues[1]).toEqual({
      start: 10,
      end: 20,
      url: 'http://localhost:8088/cdn/s/SIG/1794736800/hls/v1/sprite_001.jpg',
      x: 160,
      y: 0,
      w: 160,
      h: 90,
    });
    expect(cues[2].url).toBe('http://localhost:8088/cdn/s/SIG/1794736800/hls/v1/sprite_002.jpg');
    expect(cues[2].end).toBe(1005.5);
  });

  it('file rỗng hoặc hỏng → []', () => {
    expect(parseThumbnailsVtt('', base)).toEqual([]);
    expect(parseThumbnailsVtt('WEBVTT\n\nrác --> rác\nx\n', base)).toEqual([]);
  });
});

describe('findThumb', () => {
  const cues = parseThumbnailsVtt(vtt, base);
  it('tìm ô chứa thời điểm; ngoài khoảng đầu → null; sau cue cuối → cue cuối', () => {
    expect(findThumb(cues, 0)?.x).toBe(0);
    expect(findThumb(cues, 9.99)?.x).toBe(0);
    expect(findThumb(cues, 10)?.x).toBe(160);
    expect(findThumb(cues, 5000)?.url).toContain('sprite_002.jpg');
    expect(findThumb([], 3)).toBeNull();
    expect(findThumb(cues, -1)).toBeNull();
  });
});
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `cd web/app && npm test`
Expected: FAIL — `Failed to resolve import "./thumbnails"`, `"./reporter"`, `"./SeekPreview"`; test `onInfo` thất bại (`order` rỗng phần `info:`).

- [ ] **Step 3: Cài đặt**

`web/app/src/components/Player.tsx` — thay:

```tsx
import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { MediaRecovery, PlaybackSession, classifyHlsError } from '../player/recovery';
```

bằng:

```tsx
import { useEffect, useRef, useState } from 'react';
import { type PlayInfo, api, sendPlaybackEvent } from '../api';
import { MediaRecovery, PlaybackSession, classifyHlsError } from '../player/recovery';
import { PlaybackReporter } from '../player/reporter';
import { type ThumbCue, parseThumbnailsVtt } from '../player/thumbnails';
import { SeekPreview } from './SeekPreview';
```

`web/app/src/components/Player.tsx` — thay:

```tsx
// Khi URL hết hạn (403) hoặc lỗi mạng: PlaybackSession gọi lại /play và phát tiếp từ vị trí hiện tại.
// Kiểm tra tay trên Chrome và Safari (spec §9); logic phục hồi có test trong player/recovery.test.ts.
export function Player({ videoId }: { videoId: string }) {
```

bằng:

```tsx
// Khi URL hết hạn (403) hoặc lỗi mạng: PlaybackSession gọi lại /play và phát tiếp từ vị trí hiện tại.
// M5: phụ đề bằng <track> (URL ký từ /play), ảnh xem trước khi tua (SeekPreview + thumbnails.vtt), gửi sự kiện
// xem (PlaybackReporter → POST /api/playback/events) để có "Xem tiếp" và analytics.
// Kiểm tra tay trên Chrome và Safari (spec §9); logic có test trong player/*.test.ts.
export function Player({ videoId }: { videoId: string }) {
```

`web/app/src/components/Player.tsx` — thay:

```tsx
  const [level, setLevel] = useState(-1); // -1 = tự động (ABR)
```

bằng:

```tsx
  const [level, setLevel] = useState(-1); // -1 = tự động (ABR)
  const [info, setInfo] = useState<PlayInfo | null>(null);
  const [cues, setCues] = useState<ThumbCue[]>([]);
```

`web/app/src/components/Player.tsx` — thay:

```tsx
    setLevels([]);
    setLevel(-1);

    const mediaRecovery = new MediaRecovery();
```

bằng:

```tsx
    setLevels([]);
    setLevel(-1);
    setInfo(null);

    const reporter = new PlaybackReporter({
      videoId,
      send: sendPlaybackEvent,
      position: () => video.currentTime,
      // bitrate của mức đang phát (hls.js); Safari HLS gốc không cho biết → 0.
      bitrateKbps: () => {
        const hls = hlsRef.current;
        return hls && hls.currentLevel >= 0 ? (hls.levels[hls.currentLevel]?.bitrate ?? 0) / 1000 : 0;
      },
    });
    const media: Array<[string, () => void]> = [
      ['play', () => reporter.play()],
      ['pause', () => reporter.pause()],
      ['seeked', () => reporter.seeked()],
      ['ended', () => reporter.ended()],
      ['waiting', () => reporter.waiting()],
      ['playing', () => reporter.playing()],
    ];
    media.forEach(([e, fn]) => video.addEventListener(e, fn));
    // Đóng tab / tải lại trang: ghi vị trí cuối (fetch keepalive vẫn gửi được khi trang đang đóng).
    const onPageHide = () => reporter.flush();
    window.addEventListener('pagehide', onPageHide);

    const mediaRecovery = new MediaRecovery();
```

`web/app/src/components/Player.tsx` — thay:

```tsx
      fetchPlay: () => api.play(videoId),
      currentTime: () => video.currentTime,
```

bằng:

```tsx
      fetchPlay: () => api.play(videoId),
      onInfo: setInfo,
      currentTime: () => video.currentTime,
```

`web/app/src/components/Player.tsx` — thay:

```tsx
    return () => {
      session.dispose();
```

bằng:

```tsx
    return () => {
      // Rời trang xem trong SPA: ghi vị trí cuối trước khi gỡ listener (video.load() bên dưới phát "pause").
      reporter.flush();
      reporter.dispose();
      media.forEach(([e, fn]) => video.removeEventListener(e, fn));
      window.removeEventListener('pagehide', onPageHide);
      session.dispose();
```

`web/app/src/components/Player.tsx` — thay:

```tsx

  const changeLevel = (value: number) => {
```

bằng:

```tsx

  // thumbnails.vtt: tải lại mỗi khi URL ký đổi (lần đầu và sau khi làm mới /play). Lỗi → không có ảnh xem trước.
  const thumbnailsUrl = info?.thumbnails_url;
  useEffect(() => {
    setCues([]);
    if (!thumbnailsUrl) return;
    const ctrl = new AbortController();
    fetch(thumbnailsUrl, { signal: ctrl.signal })
      .then((res) => (res.ok ? res.text() : Promise.reject(new Error(`HTTP ${res.status}`))))
      .then((text) => setCues(parseThumbnailsVtt(text, thumbnailsUrl)))
      .catch(() => undefined);
    return () => ctrl.abort();
  }, [thumbnailsUrl]);

  const changeLevel = (value: number) => {
```

`web/app/src/components/Player.tsx` — thay:

```tsx
    <div className="player">
      <video ref={videoRef} controls playsInline />
      {error && (
```

bằng:

```tsx
    <div className="player">
      {/* crossOrigin: khi chạy Vite dev (:5173) URL ký là origin khác (:8088); <track> cần CORS
          (nginx trả Access-Control-Allow-Origin: * cho /cdn/). Không gửi cookie tới CDN. */}
      <video ref={videoRef} controls playsInline crossOrigin="anonymous">
        {info?.subtitles.map((s) => (
          <track key={s.lang} kind="subtitles" src={s.url} srcLang={s.lang} label={s.label} />
        ))}
      </video>
      {cues.length > 0 && <SeekPreview videoRef={videoRef} cues={cues} />}
      {error && (
```

`web/app/src/components/SeekPreview.tsx` (mới):

```tsx
import { type KeyboardEvent, type MouseEvent, type RefObject, useEffect, useState } from 'react';
import { formatDuration } from '../format';
import { type ThumbCue, findThumb } from '../player/thumbnails';

export interface SeekPreviewProps {
  videoRef: RefObject<HTMLVideoElement | null>;
  cues: ThumbCue[];
}

interface Hover {
  t: number;
  left: number; // px trong thanh
}

// SeekPreview: thanh tua phụ dưới video. Rê chuột → ảnh xem trước (một ô của tấm sprite, cắt bằng
// background-position) và mốc thời gian; bấm → tua tới đó; phím ←/→ tua 10 giây. Thanh điều khiển gốc của
// <video> vẫn giữ nguyên (trình duyệt không cho gắn ảnh xem trước vào thanh gốc).
export function SeekPreview({ videoRef, cues }: SeekPreviewProps) {
  const [duration, setDuration] = useState(0);
  const [current, setCurrent] = useState(0);
  const [hover, setHover] = useState<Hover | null>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const sync = () => {
      setDuration(Number.isFinite(video.duration) ? video.duration : 0);
      setCurrent(video.currentTime);
    };
    sync();
    const events = ['loadedmetadata', 'durationchange', 'timeupdate', 'seeked'] as const;
    events.forEach((e) => video.addEventListener(e, sync));
    return () => events.forEach((e) => video.removeEventListener(e, sync));
  }, [videoRef]);

  if (duration <= 0) return null;

  const timeAt = (e: MouseEvent<HTMLDivElement>): Hover => {
    const rect = e.currentTarget.getBoundingClientRect();
    const left = Math.min(Math.max(e.clientX - rect.left, 0), rect.width);
    return { t: rect.width > 0 ? (left / rect.width) * duration : 0, left };
  };
  const seek = (t: number) => {
    const video = videoRef.current;
    if (video) video.currentTime = Math.min(Math.max(t, 0), duration);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'ArrowRight') seek(current + 10);
    else if (e.key === 'ArrowLeft') seek(current - 10);
    else return;
    e.preventDefault();
  };

  const thumb = hover ? findThumb(cues, hover.t) : null;
  return (
    <div
      className="scrub"
      role="slider"
      tabIndex={0}
      aria-label="Tua video"
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      aria-valuenow={Math.round(current)}
      aria-valuetext={formatDuration(current) || '0:00'}
      onMouseMove={(e) => setHover(timeAt(e))}
      onMouseLeave={() => setHover(null)}
      onClick={(e) => seek(timeAt(e).t)}
      onKeyDown={onKeyDown}
    >
      <div className="scrub-played" style={{ width: `${(current / duration) * 100}%` }} />
      {hover && (
        <div className="scrub-preview" style={{ left: hover.left }} data-testid="seek-preview">
          {thumb && (
            <div
              className="scrub-thumb"
              style={{
                width: thumb.w,
                height: thumb.h,
                backgroundImage: `url("${thumb.url}")`,
                backgroundPosition: `${-thumb.x}px ${-thumb.y}px`,
              }}
            />
          )}
          <span>{formatDuration(hover.t) || '0:00'}</span>
        </div>
      )}
    </div>
  );
}
```

`web/app/src/player/recovery.ts` — thay:

```ts
  fetchPlay: () => Promise<PlayInfo>;
  // load nạp URL HLS và bắt đầu phát từ startAt giây (0 = đầu phim).
```

bằng:

```ts
  fetchPlay: () => Promise<PlayInfo>;
  // onInfo nhận mọi PlayInfo mới (lần đầu và sau mỗi lần làm mới), trước load: phụ đề và thumbnails có
  // URL ký mới cùng lúc với master.
  onInfo?: (p: PlayInfo) => void;
  // load nạp URL HLS và bắt đầu phát từ startAt giây (0 = đầu phim).
```

`web/app/src/player/recovery.ts` — thay:

```ts
  constructor(deps: PlaybackDeps) {
    this.deps = { now: Date.now, maxRefreshes: 3, refreshWindowMs: 60_000, ...deps };
  }
```

bằng:

```ts
  constructor(deps: PlaybackDeps) {
    this.deps = { now: Date.now, maxRefreshes: 3, refreshWindowMs: 60_000, onInfo: () => undefined, ...deps };
  }
```

`web/app/src/player/recovery.ts` — thay:

```ts
      const p = await this.deps.fetchPlay();
      if (!this.disposed) this.deps.load(p.url, p.resume_at);
    } catch (err) {
```

bằng:

```ts
      const p = await this.deps.fetchPlay();
      if (this.disposed) return;
      this.deps.onInfo(p);
      this.deps.load(p.url, p.resume_at);
    } catch (err) {
```

`web/app/src/player/recovery.ts` — thay:

```ts
      .then((p) => {
        if (!this.disposed) this.deps.load(p.url, at);
      })
```

bằng:

```ts
      .then((p) => {
        if (this.disposed) return;
        this.deps.onInfo(p);
        this.deps.load(p.url, at);
      })
```

`web/app/src/player/reporter.ts` (mới):

```ts
// Gửi sự kiện xem phim (spec §4.4): heartbeat mỗi 10 giây khi đang phát, và khi play, pause, seek, ended,
// buffering. Tách khỏi React và <video> để test được bằng fake timer; Player chỉ nối sự kiện DOM vào đây.
import type { PlaybackEvent, PlaybackEventType } from '../api';

export const HEARTBEAT_MS = 10_000;

export interface ReporterDeps {
  videoId: string;
  send: (ev: PlaybackEvent) => void;
  position: () => number; // currentTime của <video>
  bitrateKbps: () => number; // bitrate của mức chất lượng đang phát (0 nếu không biết)
  now?: () => number;
  heartbeatMs?: number;
}

export class PlaybackReporter {
  private readonly deps: Required<ReporterDeps>;
  private timer: ReturnType<typeof setInterval> | null = null;
  private bufferingSince: number | null = null;
  private started = false;

  constructor(deps: ReporterDeps) {
    this.deps = { now: Date.now, heartbeatMs: HEARTBEAT_MS, ...deps };
  }

  // play: người xem bấm phát (hoặc tự phát lại sau khi nạp URL mới). Bắt đầu heartbeat.
  play(): void {
    this.started = true;
    this.emit('play');
    this.stopHeartbeat();
    this.timer = setInterval(() => this.emit('heartbeat'), this.deps.heartbeatMs);
  }

  pause(): void {
    this.stopHeartbeat();
    if (this.started) this.emit('pause');
  }

  seeked(): void {
    if (this.started) this.emit('seek');
  }

  ended(): void {
    this.stopHeartbeat();
    this.emit('ended');
  }

  // waiting: hết dữ liệu, player đứng hình chờ tải. Chỉ tính từ lần chờ đầu tiên cho tới khi phát lại.
  waiting(): void {
    if (this.bufferingSince === null) this.bufferingSince = this.deps.now();
  }

  // playing: phát lại sau khi chờ → một sự kiện buffering với thời gian đã chờ.
  playing(): void {
    if (this.bufferingSince === null) return;
    const ms = Math.max(0, Math.round(this.deps.now() - this.bufferingSince));
    this.bufferingSince = null;
    this.emit('buffering', ms);
  }

  // flush: rời trang / đóng tab / chuyển trang khi đã xem → ghi lại vị trí cuối (heartbeat lưu watch_progress).
  flush(): void {
    if (this.started && this.deps.position() > 0) this.emit('heartbeat');
  }

  dispose(): void {
    this.stopHeartbeat();
  }

  private stopHeartbeat(): void {
    if (this.timer !== null) clearInterval(this.timer);
    this.timer = null;
  }

  private emit(type: PlaybackEventType, bufferingMs = 0): void {
    this.deps.send({
      video_id: this.deps.videoId,
      type,
      position_s: Math.round(this.deps.position() * 1000) / 1000,
      bitrate_kbps: Math.max(0, Math.round(this.deps.bitrateKbps())),
      buffering_ms: bufferingMs,
    });
  }
}
```

`web/app/src/player/thumbnails.ts` (mới):

```ts
// Đọc thumbnails.vtt do transcoder sinh (spec §4.1): mỗi cue ánh xạ một khoảng thời gian tới một ô trong
// tấm sprite, dạng "sprite_001.jpg#xywh=x,y,w,h". Đường dẫn sprite tương đối → resolve theo URL của file VTT
// nên giữ nguyên prefix ký /cdn/s/{sig}/{exp}/hls/{id}/.

export interface ThumbCue {
  start: number; // giây
  end: number;
  url: string; // URL tuyệt đối của tấm sprite
  x: number;
  y: number;
  w: number;
  h: number;
}

// parseVttTime: "01:02:03.500" hoặc "02:03.500" → giây; sai dạng → null.
export function parseVttTime(s: string): number | null {
  const m = /^(?:(\d+):)?([0-5]\d):([0-5]\d)\.(\d{3})$/.exec(s.trim());
  if (!m) return null;
  return Number(m[1] ?? 0) * 3600 + Number(m[2]) * 60 + Number(m[3]) + Number(m[4]) / 1000;
}

const xywhRe = /^(.+)#xywh=(\d+),(\d+),(\d+),(\d+)$/;

export function parseThumbnailsVtt(text: string, baseUrl: string): ThumbCue[] {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const cues: ThumbCue[] = [];
  for (let i = 0; i < lines.length; i++) {
    const [a, b] = lines[i].split('-->');
    if (b === undefined) continue;
    const start = parseVttTime(a);
    const end = parseVttTime(b.trim().split(/\s+/)[0]);
    const m = xywhRe.exec((lines[i + 1] ?? '').trim());
    if (start === null || end === null || !m) continue;
    let url: string;
    try {
      url = new URL(m[1], baseUrl).toString();
    } catch {
      continue;
    }
    cues.push({ start, end, url, x: Number(m[2]), y: Number(m[3]), w: Number(m[4]), h: Number(m[5]) });
  }
  return cues.sort((p, q) => p.start - q.start);
}

// findThumb: cue chứa thời điểm t (start ≤ t < end); sau cue cuối thì trả cue cuối. Tìm nhị phân.
export function findThumb(cues: ThumbCue[], t: number): ThumbCue | null {
  if (cues.length === 0 || t < cues[0].start) return null;
  let lo = 0;
  let hi = cues.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (cues[mid].start <= t) lo = mid;
    else hi = mid - 1;
  }
  return cues[lo];
}
```

`web/app/src/styles.css` — thay:

```css

.quality {
```

bằng:

```css

/* Thanh tua phụ có ảnh xem trước (SeekPreview). */
.scrub {
  position: relative;
  height: 10px;
  margin-top: 0.5rem;
  background: var(--border);
  border-radius: 5px;
  cursor: pointer;
}

.scrub:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.scrub-played {
  height: 100%;
  background: var(--accent);
  border-radius: 5px;
  pointer-events: none;
}

.scrub-preview {
  position: absolute;
  bottom: 16px;
  transform: translateX(-50%);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.25rem;
  padding: 4px;
  background: rgb(0 0 0 / 0.85);
  border-radius: 6px;
  font-size: 0.8rem;
  pointer-events: none;
  white-space: nowrap;
}

.scrub-thumb {
  background-repeat: no-repeat;
  border-radius: 4px;
}

.quality {
```

- [ ] **Step 4: Chạy test và build**

Run: `cd web/app && npx tsc --noEmit && npm test && npm run build`
Expected: `Test Files  14 passed (14)`, `Tests  51 passed (51)`; build thành công (`WatchPage-*.js` ~518 kB, dưới `chunkSizeWarningLimit` 600).

- [ ] **Step 5: Commit**

```bash
git add web/app/src
git commit -m "feat(web): subtitles, seek preview from sprite and playback events"
```

---

### Task 11: E2E M5

**Files:**
- Create: `scripts/e2e-m5.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `scripts/e2e-m2.sh` (`API`, `E2E_TITLE`, `E2E_ID_FILE`), stack `make up` (image mới của api, transcoder, nginx), `ADMIN_EMAIL`/`ADMIN_PASSWORD` trong `.env`, `tmp/sample.mp4` (30 giây, `make sample`).
- Produces: `make e2e-m5` in `OK  M5  mở http://localhost:8088/watch/{id}`; `make e2e-m5 KAFKA_DOWN=1` thêm bước dừng Kafka. Biến script: `SITE` (mặc định `http://localhost:8088`), `COMPOSE`, `E2E_KAFKA_DOWN`.

- [ ] **Step 1: Script và target**

`Makefile` — thay:

```make

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker e2e-m2 e2e-m3 e2e-m4 seed-admin sign-url \
	web-install web-dev web-test web-build
```

bằng:

```make

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker e2e-m2 e2e-m3 e2e-m4 e2e-m5 seed-admin sign-url \
	web-install web-dev web-test web-build
```

`Makefile` — thay:

```make

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
```

bằng:

```make

# E2E M5: poster/sprite/thumbnails.vtt qua URL ký, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" + resume_at,
# playback.events trong Kafka. `make e2e-m5 KAFKA_DOWN=1` thêm bước dừng Kafka (sự kiện xem vẫn 204).
e2e-m5: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; COMPOSE="$(COMPOSE)" E2E_KAFKA_DOWN="$(KAFKA_DOWN)" ./scripts/e2e-m5.sh tmp/sample.mp4

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
```

`scripts/e2e-m5.sh` (mới):

```bash
#!/usr/bin/env bash
# E2E M5: poster, sprite và thumbnails.vtt qua URL ký; phụ đề (SRT → VTT) upload bởi admin và phát qua /play;
# sự kiện xem → watch_progress → "Xem tiếp" + resume_at; playback.events có trong Kafka; Kafka chết thì
# sự kiện xem vẫn 204 (tuỳ chọn).
# Dùng: scripts/e2e-m5.sh [file] (mặc định tmp/sample.mp4, 30 giây). Cần: curl, jq, od.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m5 nạp từ .env); SITE (mặc định http://localhost:8088);
# COMPOSE: lệnh docker compose của stack (make truyền vào) để đọc topic playback.events, bỏ trống thì bỏ qua;
# E2E_KAFKA_DOWN=1: dừng Kafka, gửi sự kiện, bật lại (cần COMPOSE).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m5)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m5)}"
read -ra COMPOSE_CMD <<< "${COMPOSE:-}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq od; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
KAFKA_STOPPED=0
cleanup() {
  if [ "$KAFKA_STOPPED" = 1 ]; then "${COMPOSE_CMD[@]}" up -d --wait kafka >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
VJAR="$WORK/viewer.jar"
AJAR="$WORK/admin.jar"
EMAIL="viewer-m5-$(date +%s)-$RANDOM@example.com"

# expect CODE curl-args...: gọi curl và so mã HTTP. Khi lỗi chỉ in URL (body có thể chứa mật khẩu).
expect() {
  local want=$1; shift
  local code a url=""
  for a in "$@"; do case $a in http://* | https://*) url=$a ;; esac; done
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "$url: HTTP $code, want $want"
}
# fetch URL OUT: tải qua CDN, in Content-Type.
fetch() { curl -fsS --max-time 30 -o "$2" -w '%{content_type}' "$1"; }
play() { curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/videos/$ID/play"; }
# event TYPE POS: gửi sự kiện xem bằng cookie viewer, trả mã HTTP.
event() {
  jq -n --arg id "$ID" --arg t "$1" --argjson p "$2" \
    '{video_id: $id, type: $t, position_s: $p, bitrate_kbps: 2800, buffering_ms: 0}' |
    curl -s --max-time 30 -o /dev/null -w '%{http_code}' -b "$VJAR" -X POST "$SITE/api/playback/events" \
      -H 'Content-Type: application/json' -d @-
}
continue_pos() {
  curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/me/continue-watching" |
    jq -r --arg id "$ID" '[.videos[] | select(.id == $id) | .position_s][0] // "none"'
}
subtitle() { # subtitle LANG LABEL FILE [cookie jar]
  curl -s --max-time 30 -o "$WORK/sub.out" -w '%{http_code}' -b "${4:-$AJAR}" \
    -F "lang=$1" -F "label=$2" -F "file=@$3" "$SITE/api/admin/videos/$ID/subtitles"
}

echo "1/6 admin upload + transcode (scripts/e2e-m2.sh qua $SITE)"
API="$SITE" E2E_TITLE="M5 $(date +%H:%M:%S)" E2E_ID_FILE="$WORK/id" "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")
jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$AJAR" -X POST "$SITE/api/auth/login" -H 'Content-Type: application/json' -d @- \
  >/dev/null || die "đăng nhập admin"
jq -n --arg e "$EMAIL" '{email: $e, password: "viewer-password-1"}' |
  curl -fsS --max-time 30 -c "$VJAR" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- \
  >/dev/null || die "đăng ký viewer"

echo "2/6 poster: poster_url ký trong catalog và chi tiết, JPEG qua CDN"
POSTER=$(curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/videos/$ID" | jq -r '.poster_url // empty')
case "$POSTER" in "$SITE/cdn/s/"*"/hls/$ID/poster.jpg") ;; *) die "poster_url = '$POSTER'" ;; esac
curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/videos?page=1" |
  jq -e --arg id "$ID" --arg p "$POSTER" 'any(.videos[]; .id == $id and .poster_url == $p)' >/dev/null ||
  die "catalog thiếu poster_url (hoặc khác trang chi tiết: URL ổn định theo giờ)"
[ "$(fetch "$POSTER" "$WORK/poster.jpg")" = image/jpeg ] || die "poster Content-Type"
[ "$(od -An -tx1 -N2 "$WORK/poster.jpg" | tr -d ' \n')" = ffd8 ] || die "poster không phải JPEG"

echo "3/6 sprite: thumbnails_url trong /play, 3 cue cho video 30 giây, tấm sprite là JPEG"
THUMBS=$(play | jq -r '.thumbnails_url // empty')
case "$THUMBS" in "$SITE/cdn/s/"*"/hls/$ID/thumbnails.vtt") ;; *) die "thumbnails_url = '$THUMBS'" ;; esac
[ "$(fetch "$THUMBS" "$WORK/thumbs.vtt")" = text/vtt ] || die "thumbnails.vtt Content-Type"
[ "$(head -1 "$WORK/thumbs.vtt")" = WEBVTT ] || die "thumbnails.vtt thiếu header WEBVTT"
[ "$(grep -c -- '-->' "$WORK/thumbs.vtt")" = 3 ] || die "thumbnails.vtt: $(grep -c -- '-->' "$WORK/thumbs.vtt") cue, want 3"
grep -q '^sprite_001.jpg#xywh=320,0,160,90$' "$WORK/thumbs.vtt" || die "thumbnails.vtt thiếu ô thứ 3"
SPRITE="${THUMBS%thumbnails.vtt}sprite_001.jpg" # đường dẫn tương đối trong VTT, resolve như trình duyệt
[ "$(fetch "$SPRITE" "$WORK/sprite.jpg")" = image/jpeg ] || die "sprite Content-Type"

echo "4/6 phụ đề: admin upload SRT (BOM, CRLF) và VTT; /play trả URL ký; upload lại thấy bản mới ngay"
printf '\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:04,000\r\nXin chào <i>thế giới</i>\r\n\r\n2\r\n00:00:05,000 --> 00:00:08,000\r\nPhụ đề tiếng Việt\r\n' \
  > "$WORK/vi.srt"
printf 'WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello world\n' > "$WORK/en.vtt"
[ "$(subtitle vi 'Tiếng Việt' "$WORK/vi.srt")" = 201 ] || die "upload vi.srt: $(cat "$WORK/sub.out")"
jq -e --arg k "$ID/subs/vi.vtt" '.vtt_key == $k' "$WORK/sub.out" >/dev/null || die "vtt_key: $(cat "$WORK/sub.out")"
[ "$(subtitle en English "$WORK/en.vtt")" = 201 ] || die "upload en.vtt: $(cat "$WORK/sub.out")"
[ "$(subtitle VI 'Sai mã' "$WORK/vi.srt")" = 400 ] || die "lang VI phải 400"
printf 'không phải phụ đề\n' > "$WORK/bad.srt"
[ "$(subtitle fr Français "$WORK/bad.srt")" = 400 ] || die "SRT hỏng phải 400"
[ "$(subtitle fr Français "$WORK/vi.srt" "$VJAR")" = 403 ] || die "viewer upload phụ đề phải 403"
PLAY=$(play)
echo "$PLAY" | jq -e '[.subtitles[] | .lang + ":" + .label] == ["en:English", "vi:Tiếng Việt"]' >/dev/null ||
  die "subtitles: $(echo "$PLAY" | jq -c .subtitles)"
VI=$(echo "$PLAY" | jq -r '.subtitles[] | select(.lang == "vi") | .url')
[ "$(fetch "$VI" "$WORK/vi.vtt")" = text/vtt ] || die "vi.vtt Content-Type"
[ "$(head -1 "$WORK/vi.vtt")" = WEBVTT ] || die "vi.vtt thiếu header"
grep -q '^00:00:01.000 --> 00:00:04.000$' "$WORK/vi.vtt" || die "vi.vtt: mốc thời gian chưa chuyển"
grep -q '^Xin chào <i>thế giới</i>$' "$WORK/vi.vtt" || die "vi.vtt: nội dung"
printf '1\n00:00:01,000 --> 00:00:04,000\nBản sửa\n' > "$WORK/vi2.srt"
[ "$(subtitle vi 'Tiếng Việt' "$WORK/vi2.srt")" = 201 ] || die "upload lại vi"
fetch "$VI" "$WORK/vi.vtt" >/dev/null
grep -q '^Bản sửa$' "$WORK/vi.vtt" || die "CDN vẫn trả phụ đề cũ (subs/ không được cache)"

echo "5/6 sự kiện xem → Xem tiếp + resume_at"
[ "$(continue_pos)" = none ] || die "video chưa xem đã có trong Xem tiếp"
[ "$(play | jq .resume_at)" = 0 ] || die "resume_at trước khi xem phải 0"
[ "$(event play 0)" = 204 ] || die "event play"
[ "$(event heartbeat 12.4)" = 204 ] || die "event heartbeat"
[ "$(event seek 20)" = 204 ] || die "event seek"           # seek không ghi tiến độ
[ "$(event rewind 1)" = 400 ] || die "type sai phải 400"
[ "$(continue_pos)" = 12 ] || die "Xem tiếp: position_s = $(continue_pos), want 12"
[ "$(play | jq .resume_at)" = 12 ] || die "resume_at = $(play | jq .resume_at), want 12"
[ "$(event ended 30)" = 204 ] || die "event ended"
[ "$(continue_pos)" = none ] || die "xem hết vẫn còn trong Xem tiếp"
[ "$(play | jq .resume_at)" = 0 ] || die "xem hết thì resume_at = 0"
[ "$(event pause 15)" = 204 ] || die "event pause"
[ "$(continue_pos)" = 15 ] || die "pause phải ghi tiến độ"

echo "6/6 playback.events trong Kafka (key user_id)"
if [ ${#COMPOSE_CMD[@]} -eq 0 ]; then
  echo "    bỏ qua (không có COMPOSE)"
else
  MSGS=$("${COMPOSE_CMD[@]}" exec -T kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server kafka:9092 \
    --topic playback.events --from-beginning --timeout-ms 10000 2>/dev/null || true)
  for t in play heartbeat seek ended pause; do
    echo "$MSGS" | jq -e --arg id "$ID" --arg t "$t" 'select(.video_id == $id and .type == $t)' >/dev/null ||
      die "không thấy sự kiện $t của video $ID trong playback.events"
  done
  if [ "${E2E_KAFKA_DOWN:-}" = 1 ]; then
    echo "    dừng Kafka: sự kiện xem vẫn 204 ngay, tiến độ vẫn được ghi"
    KAFKA_STOPPED=1
    "${COMPOSE_CMD[@]}" stop kafka >/dev/null 2>&1
    START=$(date +%s)
    [ "$(event heartbeat 18)" = 204 ] || die "Kafka chết: event phải 204"
    [ $(($(date +%s) - START)) -le 2 ] || die "Kafka chết: event chậm $(($(date +%s) - START))s"
    [ "$(continue_pos)" = 18 ] || die "Kafka chết: tiến độ không được ghi"
    "${COMPOSE_CMD[@]}" up -d --wait kafka >/dev/null 2>&1 || die "bật lại Kafka"
    KAFKA_STOPPED=0
  fi
fi

echo "OK  M5  mở $SITE/watch/$ID"
```

Nhớ: `chmod +x scripts/e2e-m5.sh`.

- [ ] **Step 2: Chạy**

Run: `chmod +x scripts/e2e-m5.sh && bash -n scripts/e2e-m5.sh && make up && make e2e-m5 KAFKA_DOWN=1`
Expected (rút gọn):

```
1/6 admin upload + transcode (scripts/e2e-m2.sh qua http://localhost:8088)
    …
    {"duration_s":30,"renditions":[1080,720,480,360]}
    …
2/6 poster: poster_url ký trong catalog và chi tiết, JPEG qua CDN
3/6 sprite: thumbnails_url trong /play, 3 cue cho video 30 giây, tấm sprite là JPEG
4/6 phụ đề: admin upload SRT (BOM, CRLF) và VTT; /play trả URL ký; upload lại thấy bản mới ngay
5/6 sự kiện xem → Xem tiếp + resume_at
6/6 playback.events trong Kafka (key user_id)
    dừng Kafka: sự kiện xem vẫn 204 ngay, tiến độ vẫn được ghi
OK  M5  mở http://localhost:8088/watch/<id>
```

Run: `make e2e-m4 && make e2e-m3`
Expected: `OK  M4  …` và `OK  M3` (không hồi quy; `e2e-m3` vẫn thấy `subtitles == []`, `resume_at == 0` với video mới của viewer mới).

Run: `make logs-app 2>/dev/null | grep -m1 'playback event dropped'` (Ctrl+C để thoát)
Expected: có ít nhất một dòng từ lúc Kafka bị dừng.

- [ ] **Step 3: Kiểm tra tay trên trình duyệt (spec §9)**

1. Mở http://localhost:8088, đăng nhập admin. Lưới "Phim mới" hiện ảnh poster thật (video cũ trước M5 vẫn là khối màu).
2. Mở `/watch/<id>` của video từ Step 2 → phát; dưới video có thanh tua phụ: rê chuột thấy ô sprite và mốc thời gian, bấm để tua, ←/→ tua 10 giây.
3. Menu phụ đề (CC) của player có "English" và "Tiếng Việt"; chọn "Tiếng Việt" → ở giây 1–4 hiện "Bản sửa".
4. Phát ~15 giây rồi về trang chủ → hàng "Xem tiếp" có phim, thanh tiến độ ~50%, "Còn 0:1x". Bấm → phát tiếp từ vị trí cũ.
5. Tab Network: `POST /api/playback/events` mỗi 10 giây khi đang phát (`204`), khi pause/tua/hết phim.
6. Lặp bước 2–4 trên Safari (HLS gốc; bitrate gửi `0`).

- [ ] **Step 4: Commit**

```bash
git add scripts/e2e-m5.sh Makefile
git commit -m "test(e2e): M5 thumbnails, subtitles, playback events and continue watching"
```

---

### Task 12: Tài liệu M5 và ADR

**Files:**
- Modify: `README.md`, `docs/architecture.md`, `docs/api.md`, `docs/data-model.md`, `docs/flows.md`, `docs/operations.md`
- Create: `docs/decisions/0013-thumbnails-sprite-sheets.md`, `docs/decisions/0014-subtitles-srt-to-vtt.md`, `docs/decisions/0015-playback-events-best-effort.md`

**Interfaces:**
- Consumes: hành vi đã chạy ở Task 1–11 (spec §11: tài liệu chỉ mô tả những gì đã chạy).
- Produces: tài liệu khớp code M5.

Mỗi thay đổi dưới đây là một cặp "thay đoạn cũ (khớp đúng một lần trong file) bằng đoạn mới".

- [ ] **Step 1: README.md**

`README.md` — thay:

````text
Trạng thái: **M4** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4).
````

bằng:

````text
Trạng thái: **M5** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4); poster, ảnh xem trước khi tua (sprite), phụ đề nhiều ngôn ngữ, hàng "Xem tiếp" và sự kiện xem lên Kafka (M5).
````

`README.md` — thay:

````text
make e2e-m4      # upload video tiêu đề tiếng Việt → tìm "tro choi dien tu" → /play qua nginx
```
````

bằng:

````text
make e2e-m4      # upload video tiêu đề tiếng Việt → tìm "tro choi dien tu" → /play qua nginx
make e2e-m5      # poster/sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" (KAFKA_DOWN=1: thử Kafka chết)
```
````

- [ ] **Step 2: docs/architecture.md**

Thay:

````text
Phần đã chạy tới M4: `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu và `/play` cho người xem, upload và danh sách video cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker`, Postgres, Kafka, MinIO, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL), web người xem `web/app`. Chưa có: xem tiếp, poster/sprite và phụ đề (M5), analytics (M6), trang admin (M7).
````

bằng:

````text
Phần đã chạy tới M5: `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu, `/play` kèm phụ đề / thumbnails / `resume_at`, hàng "Xem tiếp", nhận sự kiện xem, upload video và phụ đề cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker` (HLS + poster + sprite), Postgres, Kafka, MinIO, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL), web người xem `web/app`. Chưa có: analytics service và ClickHouse (M6 — `playback.events` đã được publish), trang admin (M7).
````

Thay:

````text
| `internal/events` | Topic, schema JSON, producer/consumer franz-go |
````

bằng:

````text
| `internal/playback` | `POST /api/playback/events`: kiểm tra sự kiện, upsert `watch_progress` (heartbeat/pause/ended), publish `playback.events` qua `BestEffort` (nền, giới hạn 256 publish đang chờ, timeout 2 giây, lỗi thì log và bỏ); `Repo` đọc tiến độ cho `resume_at` và "Xem tiếp" ([ADR 0015](decisions/0015-playback-events-best-effort.md)) |
| `internal/subtitle` | Chuyển SRT → WebVTT, chuẩn hoá VTT (BOM, CRLF, kiểm tra UTF-8) cho `POST /api/admin/videos/{id}/subtitles` (handler ở `catalog`, [ADR 0014](decisions/0014-subtitles-srt-to-vtt.md)) |
| `internal/events` | Topic, schema JSON, producer/consumer franz-go |
````

Thay:

````text
| Đóng gói | HLS VOD, segment 6 giây MPEG-TS, `master.m3u8` + `{bậc}/index.m3u8` |
````

bằng:

````text
| Đóng gói | HLS VOD, segment 6 giây MPEG-TS, `master.m3u8` + `{bậc}/index.m3u8` |
| Poster (M5) | Lần chạy FFmpeg thứ hai: một frame ở 10% thời lượng, rộng 640 px → `poster.jpg` |
| Sprite (M5) | Lần chạy thứ ba: `select` một frame mỗi 10 giây, thu về 160×90 (giữ tỉ lệ, viền đen), `tile=10x10` → `sprite_001.jpg`, `sprite_002.jpg`… (100 ô/tấm), kèm `thumbnails.vtt` (`sprite_NNN.jpg#xywh=x,y,w,h`) — [ADR 0013](decisions/0013-thumbnails-sprite-sheets.md) |
````

Thay:

````text
`/videos/{id}` (chi tiết), `/watch/{id}` (player, tải lazy cùng hls.js). Poster là khối màu tạm tới M5.
````

bằng:

````text
`/videos/{id}` (chi tiết), `/watch/{id}` (player, tải lazy cùng hls.js). Poster là ảnh `poster.jpg` qua URL ký; video transcode trước M5 (hoặc ảnh lỗi) hiện khối màu như cũ. Trang chủ có hàng "Xem tiếp" (tối đa 20 video đang xem dở, thanh tiến độ, bấm vào là phát tiếp).
````

Thay:

````text
## Tìm kiếm
````

bằng:

````text
## Ảnh xem trước, phụ đề và xem tiếp (M5)

- **Poster:** `GET /api/videos` và `/api/videos/{id}` trả `poster_url` ký bằng `Signer.StablePath` — `exp` làm tròn xuống theo giờ nên URL giống hệt nhau trong một giờ và trình duyệt cache được; hạn còn lại 1–2 giờ, không vượt TTL 2 giờ ([ADR 0013](decisions/0013-thumbnails-sprite-sheets.md)).
- **Sprite khi tua:** `/play` trả `thumbnails_url`. Player tải `thumbnails.vtt`, resolve đường dẫn sprite theo URL của chính file VTT (vẫn dưới prefix ký) và hiện thanh tua phụ `SeekPreview` dưới video: rê chuột hiện ô sprite (`background-position`) và mốc thời gian, bấm hoặc ←/→ để tua. Thanh điều khiển gốc của `<video>` giữ nguyên.
- **Phụ đề:** admin upload `.srt`/`.vtt` (≤ 1 MiB, UTF-8) qua `POST /api/admin/videos/{id}/subtitles`; api chuyển SRT → VTT, ghi `hls/{id}/subs/{lang}.vtt` (Content-Type `text/vtt`) và upsert bảng `subtitles`. `/play` trả `subtitles: [{lang,label,url}]` với URL ký; player thêm `<track kind="subtitles">`. nginx không cache `subs/` để bản upload lại có hiệu lực ngay ([ADR 0014](decisions/0014-subtitles-srt-to-vtt.md)).
- **Sự kiện xem và "Xem tiếp":** `PlaybackReporter` (web) gửi `play`, `pause`, `seek`, `ended`, `buffering` (thời gian chờ) và `heartbeat` mỗi 10 giây khi đang phát; rời trang thì gửi heartbeat vị trí cuối (`fetch` `keepalive`). api ghi `watch_progress` cho heartbeat/pause/ended, publish mọi sự kiện lên `playback.events` (key `user_id`) ở nền — Kafka chết thì sự kiện bị bỏ, request vẫn `204` ([ADR 0015](decisions/0015-playback-events-best-effort.md)). "Xem tiếp" = `5% < position/duration < 95%`, mới xem nhất trước, tối đa 20; `resume_at` = vị trí đã lưu (0 nếu chưa xem hoặc đã xem ≥ 95%).

## Tìm kiếm
````

Thay:

````text
hls/{id}/{1080p|720p|480p|360p}/seg_000.ts …
```
````

bằng:

````text
hls/{id}/{1080p|720p|480p|360p}/seg_000.ts …
hls/{id}/poster.jpg                    (M5, 640 px, frame ở 10% thời lượng)
hls/{id}/sprite_001.jpg …              (M5, 1600×900, 10×10 ô 160×90, một ô mỗi 10 giây)
hls/{id}/thumbnails.vtt                (M5, chỉ mục ô sprite theo thời gian)
hls/{id}/subs/{lang}.vtt               (M5, phụ đề do admin upload, ghi đè khi upload lại)
```
````

- [ ] **Step 3: docs/api.md**

Thay:

````text
| GET | `/api/videos/{id}/play` | user | URL ký để phát (hạn 2 giờ) |
````

bằng:

````text
| GET | `/api/videos/{id}/play` | user | URL ký để phát (hạn 2 giờ), phụ đề, thumbnails, `resume_at` |
| GET | `/api/me/continue-watching` | user | Hàng "Xem tiếp" (tối đa 20) |
| POST | `/api/playback/events` | user | Sự kiện xem (heartbeat 10 giây, play/pause/seek/ended/buffering) |
| POST | `/api/admin/videos/{id}/subtitles` | admin | Upload phụ đề `.srt`/`.vtt` (multipart `lang`, `label`, `file`) |
````

Thay:

````text
Chỉ video `ready`. Không có `q`: mới nhất trước.
````

bằng:

````text
Từ M5 mỗi video có thêm `poster_url` (URL ký `…/cdn/s/{sig}/{exp}/hls/{id}/poster.jpg`, `exp` làm tròn theo giờ nên URL ổn định để trình duyệt cache); video transcode trước M5 không có trường này.

Chỉ video `ready`. Không có `q`: mới nhất trước.
````

Thay:

````text
```json
{"url":"http://localhost:8088/cdn/s/H23K0bGijtyB6JSJpoVT1g/1794736800/hls/7c1e…/master.m3u8",
 "expires_at":"2026-11-15T12:00:00Z","subtitles":[],"resume_at":0}
```

`url` hết hạn sau 2 giờ; playlist con và segment dùng chung chữ ký. nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play` (player của web tự làm và phát tiếp từ vị trí hiện tại). `subtitles` luôn `[]` và `resume_at` luôn `0` tới M5. `404` nếu không có hoặc chưa `ready`.
````

bằng:

````text
```json
{"url":"http://localhost:8088/cdn/s/H23K0bGijtyB6JSJpoVT1g/1794736800/hls/7c1e…/master.m3u8",
 "expires_at":"2026-11-15T12:00:00Z",
 "subtitles":[{"lang":"en","label":"English","url":"http://localhost:8088/cdn/s/H23K…/1794736800/hls/7c1e…/subs/en.vtt"},
              {"lang":"vi","label":"Tiếng Việt","url":"http://localhost:8088/cdn/s/H23K…/1794736800/hls/7c1e…/subs/vi.vtt"}],
 "thumbnails_url":"http://localhost:8088/cdn/s/H23K…/1794736800/hls/7c1e…/thumbnails.vtt",
 "resume_at":42}
```

`url` hết hạn sau 2 giờ; playlist con, segment, phụ đề, `thumbnails.vtt` và các tấm sprite dùng chung chữ ký thư mục. nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play` (player của web tự làm và phát tiếp từ vị trí hiện tại). `subtitles` sắp theo `lang`, `[]` khi chưa có. `thumbnails_url` không có với video transcode trước M5. `resume_at` = vị trí đã lưu của user (giây), `0` nếu chưa xem hoặc đã xem ≥ 95%. `404` nếu không có hoặc chưa `ready`.

## GET /api/me/continue-watching

`200` (`Cache-Control: no-store`): tối đa 20 video `ready` mà user đang xem dở (`5% < position_s/duration_s < 95%`), mới xem nhất trước. Mỗi phần tử như catalog, thêm `position_s` và `watched_at`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"duration_s":30,
  "created_at":"2026-11-15T10:00:00Z","poster_url":"http://localhost:8088/cdn/s/…/poster.jpg",
  "position_s":12,"watched_at":"2026-11-15T10:05:00Z"}]}
```

## POST /api/playback/events

```json
{"video_id":"7c1e…","type":"heartbeat","position_s":12.4,"bitrate_kbps":2800,"buffering_ms":0}
```

`type`: `play`, `pause`, `seek`, `ended`, `buffering`, `heartbeat`. `position_s` 0–172800 (số thực, làm tròn xuống giây và kẹp theo thời lượng video), `bitrate_kbps` 0–100000, `buffering_ms` 0–600000. `heartbeat`, `pause`, `ended` ghi `watch_progress`; mọi loại được publish lên `playback.events`. `204` kể cả khi Kafka không khả dụng (sự kiện bị bỏ và ghi log). `400` input sai, `404` video không có hoặc chưa `ready`.

## POST /api/admin/videos/{id}/subtitles

`multipart/form-data`: `lang` (`^[a-z]{2,3}(-[A-Z]{2})?$`, ví dụ `vi`, `en`, `pt-BR`), `label` (1–50 ký tự, ví dụ `Tiếng Việt`), `file` (`.srt` hoặc `.vtt`, UTF-8, ≤ 1 MiB).

```bash
curl -b admin.jar -F lang=vi -F 'label=Tiếng Việt' -F file=@phim.vi.srt \
  http://localhost:8088/api/admin/videos/7c1e…/subtitles
```

`201`: `{"lang":"vi","label":"Tiếng Việt","vtt_key":"7c1e…/subs/vi.vtt"}`. Upload lại cùng `lang` thì ghi đè (file và nhãn). `400` lang/label sai, thiếu file, đuôi khác `.srt`/`.vtt`, không phải UTF-8, SRT hỏng (thông báo có số dòng) hoặc VTT thiếu header; `413` file > 1 MiB; `404` video không tồn tại.
````

- [ ] **Step 4: docs/data-model.md**

Thay:

````text
## Postgres (tới M4)

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp). `subtitles` và `watch_progress` (M5) được thêm bằng migration của giai đoạn đó.
````

bằng:

````text
## Postgres (tới M5)

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp).
````

Thay:

````text
renditions (video_id uuid fk on delete cascade, height int, bitrate_kbps int, playlist_key text,
            pk(video_id, height))
```

Index: `videos(created_at desc)`, `videos(status, created_at)`, GIN `videos(search)`, GIN `pg_trgm` trên `immutable_unaccent(title)`.
````

bằng:

````text
renditions (video_id uuid fk on delete cascade, height int, bitrate_kbps int, playlist_key text,
            pk(video_id, height))
-- M5, migrations/0004_subtitles_progress.sql
subtitles (video_id uuid fk on delete cascade, lang text, label text,
           vtt_key text,            -- {video_id}/subs/{lang}.vtt trong bucket hls
           created_at timestamptz, updated_at timestamptz, pk(video_id, lang))
watch_progress (user_id uuid fk on delete cascade, video_id uuid fk on delete cascade,
                position_s int check (>= 0), duration_s int check (>= 0),  -- duration_s chép từ videos lúc ghi
                updated_at timestamptz, pk(user_id, video_id))
```

`videos.poster_key` = `{id}/poster.jpg`, `videos.sprite_key` = `{id}/thumbnails.vtt` (chỉ mục các tấm sprite, [ADR 0013](decisions/0013-thumbnails-sprite-sheets.md)); `NULL` với video transcode trước M5.

Index: `videos(created_at desc)`, `videos(status, created_at)`, GIN `videos(search)`, GIN `pg_trgm` trên `immutable_unaccent(title)`, `watch_progress(user_id, updated_at desc)` (hàng "Xem tiếp").
````

Thay:

````text
| `video.transcoded` | transcoder | `api` | `{"video_id","duration_s","hls_prefix","renditions":[{"height","bitrate_kbps","playlist_key"}]}` |
| `video.failed` | transcoder | `api` | `{"video_id","reason"}` |

Key message = `video_id`. Mỗi topic 3 partition. `playback.events` và `app.logs` đã tạo sẵn, dùng từ M6. `poster_key` / `sprite_key` trong `video.transcoded` có từ M5.
````

bằng:

````text
| `video.transcoded` | transcoder | `api` | `{"video_id","duration_s","hls_prefix","renditions":[{"height","bitrate_kbps","playlist_key"}],"poster_key","sprite_key"}` |
| `video.failed` | transcoder | `api` | `{"video_id","reason"}` |
| `playback.events` | api | (analytics, M6) | `{"ts","user_id","video_id","type","position_s","bitrate_kbps","buffering_ms"}` |

Key message = `video_id`, riêng `playback.events` key = `user_id` (giữ thứ tự sự kiện của một người xem). Mỗi topic 3 partition. `app.logs` đã tạo sẵn, dùng từ M6. `poster_key` / `sprite_key` có từ M5 (bỏ khỏi JSON khi rỗng).

Ví dụ `playback.events`:

```json
{"ts":"2026-11-15T10:05:00.123Z","user_id":"5f0c…","video_id":"7c1e…","type":"heartbeat",
 "position_s":12,"bitrate_kbps":2800,"buffering_ms":0}
```
````

Thay:

````text
```json
{"video_id":"7c1e…","duration_s":30,"hls_prefix":"7c1e…/","renditions":[
  {"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"},
  {"height":720,"bitrate_kbps":2800,"playlist_key":"7c1e…/720p/index.m3u8"}]}
```
````

bằng:

````text
```json
{"video_id":"7c1e…","duration_s":30,"hls_prefix":"7c1e…/","renditions":[
  {"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"},
  {"height":720,"bitrate_kbps":2800,"playlist_key":"7c1e…/720p/index.m3u8"}],
 "poster_key":"7c1e…/poster.jpg","sprite_key":"7c1e…/thumbnails.vtt"}
```
````

- [ ] **Step 5: docs/flows.md**

Thay:

````text
  API-->>P: {url: http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8, expires_at, subtitles: [], resume_at: 0}
````

bằng:

````text
  API-->>P: {url: …/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8, expires_at, subtitles: [{lang,label,url}], thumbnails_url, resume_at}
````

Thay:

````text
Web gọi `/api/...` và tải `/cdn/...` qua cùng một origin
````

bằng:

````text
Phụ đề (`…/subs/{lang}.vtt`, thẻ `<track>`), `thumbnails.vtt` và các tấm sprite cũng nằm dưới `/hls/{id}/` nên dùng chung chữ ký và cũng được lấy lại khi gọi lại `/play`.

Web gọi `/api/...` và tải `/cdn/...` qua cùng một origin
````

Thêm vào **cuối file**:

````text

## Xem tiếp và sự kiện xem (M5)

```mermaid
sequenceDiagram
  autonumber
  participant P as Player (PlaybackReporter)
  participant API as api
  participant PG as Postgres
  participant K as Kafka
  participant H as Trang chủ
  P->>API: POST /api/playback/events {video_id, type: play, position_s, bitrate_kbps}
  API->>PG: video ready? (catalog.Repo.Get)
  API-)K: playback.events (nền, BestEffort, key user_id)
  API-->>P: 204
  loop mỗi 10 giây khi đang phát
    P->>API: heartbeat {position_s}
    API->>PG: UPSERT watch_progress (user_id, video_id, position_s, duration_s)
    API-)K: playback.events
    API-->>P: 204
  end
  Note over P,API: pause / ended cũng ghi watch_progress; seek / play / buffering chỉ vào Kafka
  Note over API,K: Kafka chết: publish hết 2 giây thì log và bỏ; quá 256 publish đang chờ thì bỏ ngay — request vẫn 204
  P->>API: rời trang (pagehide / chuyển trang): heartbeat vị trí cuối, fetch keepalive
  H->>API: GET /api/me/continue-watching
  API->>PG: watch_progress WHERE user_id AND 5% < pos/dur < 95% ORDER BY updated_at DESC LIMIT 20
  API->>PG: videos ready theo id (poster_url ký)
  API-->>H: {videos: [..., position_s, watched_at]}
  H->>API: bấm thẻ → /watch/{id} → GET /play
  API->>PG: resume_at = position_s (0 nếu ≥ 95%)
```
````

- [ ] **Step 6: docs/operations.md**

Thay:

````text
## Theo dõi Kafka
````

bằng:

````text
## Phụ đề, poster và xem tiếp (M5)

- Upload phụ đề: xem `docs/api.md` (`POST /api/admin/videos/{id}/subtitles`). File phải là UTF-8; SRT xuất từ phần mềm cũ thường là Windows-1258/1252 → chuyển trước: `iconv -f WINDOWS-1258 -t UTF-8 phim.srt > phim.utf8.srt`.
- Xem tiến độ của mọi user:

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec postgres psql -U streamfilm -c \
  "select u.email, w.video_id, w.position_s, w.duration_s, w.updated_at from watch_progress w join users u on u.id = w.user_id order by w.updated_at desc limit 10"
```

- Đọc `playback.events` (đến M6 chưa có consumer; Redpanda Console → Topics → `playback.events` cũng xem được):

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:9092 --topic playback.events --from-beginning --timeout-ms 5000
```

- Thử Kafka chết: `make e2e-m5 KAFKA_DOWN=1` (dừng kafka, gửi sự kiện — vẫn `204`, tiến độ vẫn ghi — rồi bật lại). Log api có `playback event dropped`.

## Theo dõi Kafka
````

Thay:

````text
| Tìm "tro choi" không ra video vừa upload |
````

bằng:

````text
| Thẻ phim hiện khối màu thay vì poster | Video transcode trước M5 (không có `poster_key`): transcode lại bằng cách upload lại; hoặc trang mở quá 1–2 giờ nên URL poster đã hết hạn (tải lại trang) |
| Không có ảnh xem trước khi tua | `/play` không có `thumbnails_url` (video trước M5) hoặc tải `thumbnails.vtt` lỗi: xem tab Network |
| Upload phụ đề `400 subtitle: file must be UTF-8 text` | Chuyển mã file sang UTF-8 (xem mục M5 ở trên) |
| Phụ đề không hiện | Chọn phụ đề trong menu CC của player; kiểm tra `/play` có `subtitles` và URL `.vtt` trả `200 text/vtt` |
| Hàng "Xem tiếp" trống | Mới xem < 5% hoặc đã ≥ 95% thời lượng, hoặc chưa có heartbeat (cần phát ≥ 10 giây, hoặc pause) |
| Tìm "tro choi" không ra video vừa upload |
````

- [ ] **Step 7: ADR 0013** — `docs/decisions/0013-thumbnails-sprite-sheets.md`:

````markdown
# ADR 0013: Poster và sprite xem trước — nhiều tấm 10×10, URL poster ổn định theo giờ

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.1: poster là frame ở 10% thời lượng; sprite mỗi 10 giây một ảnh ghép thành lưới, kèm `thumbnails.vtt` để player xem trước khi tua; `video.transcoded` mang `poster_key`, `sprite_key`. Ảnh nằm dưới `hls/{id}/` nên chỉ đọc được qua URL ký (ADR 0009). Một lưới duy nhất cho phim 2 giờ (720 ô) sẽ là ảnh 4800×2430+ px, nặng và vượt giới hạn texture của một số trình duyệt di động.

## Quyết định
- Transcoder chạy FFmpeg thêm hai lần sau HLS: poster (`-ss` 10% thời lượng, rộng 640 px) và sprite (`select='isnan(prev_selected_t)+gte(t-prev_selected_t,10)'`, `scale=160:90:force_original_aspect_ratio=decrease,pad=160:90`, `tile=10x10`, `-fps_mode passthrough`). `select` theo thời gian thay cho `fps=1/10` vì `fps` có thể nhân bản frame cuối ở cuối video, làm số ô lệch với VTT.
- Mỗi tấm 1600×900 chứa 100 ô (16 phút 40 giây); video dài hơn có nhiều tấm `sprite_001.jpg`, `sprite_002.jpg`… (FFmpeg tự ghi tấm cuối thiếu ô). Không giãn khoảng cách ảnh theo độ dài phim: mọi phim đều xem trước được từng 10 giây.
- `thumbnails.vtt` do Go sinh (`transcode.ThumbnailsVTT`) từ thời lượng: `ceil(duration/10)` cue, mỗi cue `sprite_NNN.jpg#xywh=x,y,160,90` với đường dẫn **tương đối** — player resolve theo URL của file VTT nên vẫn ở dưới prefix ký. Test FFmpeg kiểm tra bộ lọc `select` cho đúng số ô này.
- `sprite_key` = `{id}/thumbnails.vtt`: với nhiều tấm, "key của sprite" hợp lý nhất là chỉ mục của chúng. `/play` trả `thumbnails_url` ký từ key này.
- `poster_url` trong catalog ký bằng `Signer.StablePath`: `exp` = now + 2 giờ làm tròn xuống theo giờ. URL giống hệt nhau trong một giờ nên trình duyệt cache ảnh (URL ký theo giây sẽ đổi ở mỗi lần gọi API); hạn còn lại 1–2 giờ, không vượt TTL của spec. Cache CDN không phụ thuộc chữ ký (cache key `{id}/{file}`).
- Lỗi khi sinh ảnh làm job lỗi và thử lại như lỗi HLS (ảnh là một phần của "transcode xong").

## Phương án đã loại
- Một tấm duy nhất với khoảng cách giãn theo độ dài (≤ 100 ô): phim dài xem trước rất thưa.
- Poster công khai ngoài CDN ký (bucket/đường riêng): thêm đường không ký và quy tắc nginx; lợi ích (cache lâu hơn) nhỏ ở MVP.
- Sinh ảnh trong cùng lệnh FFmpeg của HLS: lệnh dài và khó đọc, lỗi ảnh lẫn với lỗi encode, không test riêng được.

## Hệ quả
- Video đã `ready` trước M5 không có poster/sprite (web hiện khối màu, không có xem trước) tới khi transcode lại.
- Transcode chậm hơn một lần giải mã toàn bộ video (sprite); poster gần như tức thì nhờ `-ss` trước `-i`.
- Trang mở lâu hơn hạn của URL poster thì ảnh lỗi → web chuyển về khối màu; tải lại trang lấy URL mới.
````

- [ ] **Step 8: ADR 0014** — `docs/decisions/0014-subtitles-srt-to-vtt.md`:

````markdown
# ADR 0014: Phụ đề — chuyển SRT sang WebVTT phía api, lưu cạnh HLS, không cache ở CDN

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.2: admin upload `.srt` hoặc `.vtt` (multipart, kèm `lang`, `label`), api chuyển SRT → VTT, lưu `hls/{id}/subs/{lang}.vtt`, ghi bảng `subtitles`; player nạp bằng `<track>` với URL ký. Trình duyệt chỉ hiểu WebVTT. File SRT ngoài đời thường có BOM, CRLF, số thứ tự, thẻ `<font>`, thẻ ASS `{\an8}`, đôi khi không phải UTF-8.

## Quyết định
- Package `internal/subtitle` (thuần, không I/O): `FromSRT` (bỏ BOM, CRLF/CR → LF, khối cách nhau bởi dòng trống, số thứ tự tuỳ chọn, mốc `HH:MM:SS,mmm` hoặc `.mmm`, bỏ toạ độ sau mốc, giữ `<i>/<b>/<u>`, bỏ `<font>` và thẻ ASS, escape `& < >` để lời thoại có `-->` không phá cấu trúc VTT, lỗi kèm số dòng); `NormalizeVTT` (chuẩn hoá và kiểm tra tối thiểu: header `WEBVTT`, có cue — phần còn lại để trình duyệt parse). Không UTF-8 → `400` với hướng dẫn chuyển mã; không tự đoán bảng mã.
- Giới hạn: file ≤ 1 MiB (`413`), `lang` khớp `^[a-z]{2,3}(-[A-Z]{2})?$` (đi thẳng vào key object nên không cho ký tự khác), `label` 1–50 ký tự không có ký tự điều khiển.
- Ghi object trước (`storage.PutObject`, `text/vtt`), rồi upsert `subtitles` (PK `(video_id, lang)`): lỗi DB để lại object sẽ bị ghi đè ở lần upload lại; không có phụ đề "ma" trong `/play`.
- nginx không cache `subs/` (`proxy_no_cache`/`proxy_cache_bypass` theo `map $sl_file`): upload lại đè cùng key nên cache 7 ngày như segment sẽ phục vụ bản cũ. Segment, playlist, poster, sprite vẫn cache.
- `<video crossorigin="anonymous">`: khi chạy Vite dev (origin khác `:8088`) `<track>` cần CORS; nginx đã trả `Access-Control-Allow-Origin: *` cho `/cdn/`.

## Hệ quả
- Phụ đề đi chung chữ ký thư mục với HLS, hết hạn và được làm mới cùng `/play`.
- Mỗi lần xem tải phụ đề từ MinIO (không cache) — file nhỏ, chấp nhận ở MVP; production có thể đặt tên file theo phiên bản (`subs/{lang}-{rev}.vtt`) để cache được.
- Chưa có xoá phụ đề (trang admin M7).
````

- [ ] **Step 9: ADR 0015** — `docs/decisions/0015-playback-events-best-effort.md`:

````markdown
# ADR 0015: Sự kiện xem — ghi tiến độ đồng bộ, publish Kafka best-effort ở nền

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.4: player gửi `POST /api/playback/events` mỗi 10 giây và khi play/pause/seek/ended/buffering; api upsert `watch_progress` chỉ với heartbeat/pause/ended rồi publish `playback.events` (key `user_id`). Spec §7: Kafka không khả dụng thì ghi log và bỏ sự kiện, không làm hỏng việc xem. `events.Producer.Publish` chờ kết quả (có thể tới 10 giây khi broker chết).

## Quyết định
- Handler ghi `watch_progress` đồng bộ (nguồn của "Xem tiếp" và `resume_at`, phải bền), rồi giao sự kiện cho `playback.BestEffort` và trả `204` ngay.
- `BestEffort.Send` không chặn: mỗi sự kiện publish trong goroutine riêng với timeout 2 giây; semaphore 256 publish đang chờ — đầy thì bỏ sự kiện ngay (log `playback event dropped`). Bộ nhớ và số goroutine có giới hạn khi Kafka chết. Khi tắt api, `Wait()` trước khi đóng producer.
- Chỉ video `ready` nhận sự kiện (`404` cho video khác) để analytics không có rác; `position_s` làm tròn xuống giây và kẹp theo thời lượng.
- "Xem tiếp": `5% < position/duration < 95%` (SQL trong `playback.Repo.ContinueWatching` và hàm Go `InContinueRow` cùng ngưỡng, test biên 5/6/94/95), `updated_at` mới nhất trước, tối đa 20, index `(user_id, updated_at DESC)`. `duration_s` chép vào `watch_progress` khi ghi để lọc không cần join; `ready` là trạng thái cuối nên không cần join `videos` để kiểm tra lại. `resume_at` = vị trí đã lưu, `0` nếu ≥ 95%.
- Catalog (bên dùng) khai báo `ProgressSource`; `playback` import `catalog`, không ngược lại.
- Web: `PlaybackReporter` thuần (test bằng fake timer); gửi bằng `fetch` `keepalive` (không dùng `sendBeacon` để một đường code cho cả lúc đang xem lẫn lúc đóng tab, và đặt được `Content-Type: application/json`); lỗi/401 bị bỏ qua, không đẩy người xem về trang đăng nhập giữa phim.

## Phương án đã loại
- Publish đồng bộ với timeout ngắn: Kafka chết làm mỗi heartbeat chậm thêm timeout.
- Outbox trong Postgres: đúng hơn (không mất sự kiện) nhưng ngoài phạm vi MVP (spec §1, ADR 0007). Mất một phần sự kiện analytics khi Kafka chết là chấp nhận được; tiến độ xem không mất vì nằm ở Postgres.

## Hệ quả
- Sự kiện có thể mất khi Kafka chết hoặc api tắt giữa chừng (tối đa 2 giây chờ); số bị bỏ ghi vào log.
- Thứ tự sự kiện của một user trong Kafka gần đúng (publish song song trong goroutine); analytics (M6) sắp theo `ts`.
- Mỗi heartbeat là một upsert Postgres — đủ cho MVP; khi tải lớn có thể gom ghi theo lô hoặc dùng Redis.
````

- [ ] **Step 10: Kiểm tra liên kết và commit**

Run: `ls docs/decisions/ | tail -3 && grep -c 'M5' docs/architecture.md docs/api.md docs/data-model.md docs/flows.md docs/operations.md`
Expected: `0013-thumbnails-sprite-sheets.md`, `0014-subtitles-srt-to-vtt.md`, `0015-playback-events-best-effort.md`; mỗi file đếm ≥ 1.

```bash
git add README.md docs/architecture.md docs/api.md docs/data-model.md docs/flows.md docs/operations.md docs/decisions
git commit -m "docs: M5 thumbnails, subtitles and continue watching docs and ADRs"
```

---

## Self-review

**Độ phủ spec M5:**

| Yêu cầu | Task |
|---|---|
| Poster frame ở 10% thời lượng (§4.1) | 1 (`PosterArgs`, test FFmpeg 640x360) |
| Sprite mỗi 10 giây ghép lưới + `thumbnails.vtt` (§4.1) | 1 (`SpriteArgs`, `ThumbnailsVTT`, `TestSpriteFrameSelection`) |
| Upload lên `hls/{id}/`, `video.transcoded` mang `poster_key`, `sprite_key` (§3.3, §4.1) | 1 (`worker.Transcoded`; `UploadDir` đã upload cả thư mục) |
| API ghi `poster_key`, `sprite_key` khi ready (§4.1 bước 5, §5) | 2 |
| Bảng `subtitles`, `watch_progress`, index `(user_id, updated_at desc)` (§5) | 2 |
| Admin upload `.srt`/`.vtt` multipart `lang`, `label`; SRT → VTT; `hls/{id}/subs/{lang}.vtt`; bảng `subtitles` (§4.2, §6) | 3, 4 |
| Unit test SRT → VTT (§9) | 3 |
| `/play` trả `subtitles [{lang,label,url}]`, `thumbnails_url`, `resume_at` (§4.3) | 5, 7 |
| Player nạp phụ đề bằng `<track>` với URL ký (§4.2) | 10 |
| Poster trong catalog (§10 M5) | 5 (`poster_url`), 9 (`Poster`) |
| Sprite xem trước khi tua (§10 M5) | 10 (`SeekPreview`, `thumbnails.ts`) |
| `POST /api/playback/events` mỗi 10 giây và khi play/pause/seek/ended/buffering (§4.4 bước 1) | 7 (API), 10 (`PlaybackReporter`) |
| Upsert `watch_progress` chỉ với heartbeat/pause/ended, rồi publish `playback.events` key `user_id` (§4.4 bước 2, §3.3) | 6, 7 |
| "Xem tiếp" 5% < pos/dur < 95%, `updated_at` mới nhất, tối đa 20; `GET /api/me/continue-watching` (§4.4 bước 3, §6) | 6 (`ContinueWatching`, `InContinueRow`), 7, 9 (`ContinueRow`) |
| Unit test logic "xem tiếp", parse sự kiện (§9) | 6 (`TestInContinueRowAndResume`, `TestEventInputNormalize`), 7 (`TestEventHandler*`) |
| Kafka không khả dụng → sự kiện xem log rồi bỏ, không hỏng việc xem (§7) | 6 (`BestEffort`, test timeout/quá tải), 7 (`TestEventHandlerKafkaAndDB`), 11 (`KAFKA_DOWN=1`) |
| Integration: repository, storage, nginx (§9) | 2, 4, 6, 7 (Postgres), 4 (MinIO), 8 (nginx) |
| E2E (§9) | 11 |
| Tài liệu + ADR (§11) | 12 |

Ngoài phạm vi M5 (đúng spec): analytics service, ClickHouse, `app.logs` (M6 — M5 chỉ publish `playback.events`); trang admin để upload/xoá phụ đề (M7 — M5 dùng API/`curl`); tạo lại poster/sprite cho video đã `ready` trước M5 (cần transcode lại).

**Quét placeholder:** không có "TBD", "TODO", "tương tự Task N", "thêm xử lý lỗi phù hợp"; mọi bước có code đầy đủ hoặc lệnh kèm kết quả mong đợi. Toàn bộ code trong plan được sinh từ một bản sao của nhánh `feat/mvp` đã áp từng task thành một commit và kiểm tra: mỗi commit `go vet ./...` + `go test -short ./...` xanh; commit cuối `go test -count=1 ./...` (gồm FFmpeg 8.1, Postgres, MinIO, Redpanda, nginx trong container) đều `ok`; `web/app`: `npm ci`, `npx tsc --noEmit`, `npm test` (51 test), `npm run build`; các cặp "thay A bằng B" được kiểm tra bằng script (A khớp đúng một lần, áp tuần tự ra đúng file đích). Một stack compose riêng (project `streamfilm-m5test`, cổng 18088/18000/19000/…, image tag `:m5test`, đã gỡ sau khi chạy) dựng từ bản sao: `make e2e-m5 KAFKA_DOWN=1` → `OK  M5`, `make e2e-m4` → `OK  M4`, `make e2e-m3` → `OK  M3`; Chrome headless: video phát (`readyState=4`), 2 text track `en:English`/`vi:Tiếng Việt` (cue "Bản sửa" ở giây 2), rê thanh tua hiện ô sprite `-160px 0px`, heartbeat ghi tiến độ, trang chủ có hàng "Xem tiếp" với thanh tiến độ, 4 poster tải được (640 px).

**Nhất quán chữ ký:**
- `transcode.Result.{PosterFile, ThumbnailsFile, SpriteSheets}` → `worker.Transcoded` → `events.VideoTranscoded.{PosterKey, SpriteKey}` → `catalog.StatusUpdater` → `ReadyInfo.{PosterKey, SpriteKey}` → `Repo.MarkReady` → `Video.{PosterKey, SpriteKey}` → `ViewerHandler.toViewer` (`poster_url`) và `play` (`thumbnails_url`).
- `catalog.Subtitle{Lang, Label, VTTKey}` dùng ở `UpsertSubtitle`, `Subtitles`, `SubtitleHandler`, `fakeSubtitleStore`, `fakeViewerStore.subs`, `emptyStore.Subtitles`; `SubtitleKey` khớp `fileInDir` (`{id}/subs/{lang}.vtt`).
- `catalog.ViewerStore` (Search, Genres, Get, Renditions, Subtitles, ReadyByIDs) — thoả bởi `*catalog.Repo`, `fakeViewerStore`, `emptyStore`. `catalog.ProgressSource` (ResumeAt, ContinueWatching) — thoả bởi `*playback.Repo`, `fakeProgress`, `emptyStore`. `PlaySigner` (Path, StablePath) — `*signing.Signer`, `fakeSigner`.
- `playback.Handler{Videos, Progress, Events, Now, Log}`: `*catalog.Repo` thoả `VideoGetter`, `*playback.Repo` thoả `ProgressWriter`, `*playback.BestEffort` thoả `EventSink`; `emptyStore` thoả `VideoGetter` trong router test.
- JSON ↔ TS: `ViewerVideoJSON.poster_url` ↔ `Video.poster_url?`; `ContinueJSON` (`position_s`, `watched_at`, nhúng `ViewerVideoJSON`) ↔ `ContinueVideo`; `PlayJSON.thumbnails_url` (`omitempty`) ↔ `PlayInfo.thumbnails_url?`; `EventInput` (`video_id`, `type`, `position_s`, `bitrate_kbps`, `buffering_ms`) ↔ `PlaybackEvent`; `EventType` ↔ `PlaybackEventType` (cùng 6 giá trị).
- Ngưỡng 5%/95% giống nhau ở `ContinueMinRatio`/`ContinueMaxRatio` (SQL tham số `$2`, `$3`), `InContinueRow`, `ResumePosition`; giới hạn 20 = `catalog.ContinueWatchingLimit` dùng ở handler và test.
- Định dạng `thumbnails.vtt`: Go `ThumbnailsVTT` (`HH:MM:SS.mmm`, `sprite_NNN.jpg#xywh=x,y,w,h`) ↔ TS `parseVttTime`/`parseThumbnailsVtt` (test cùng mẫu) ↔ e2e (`grep '^sprite_001.jpg#xywh=320,0,160,90$'`).
- Cổng/đường dẫn như M4: nginx `:8088` (`SITE`, `CDN_BASE_URL`), api `:8000`; topic `playback.events` đã có trong `kafka-init`.

## Các kế hoạch tiếp theo

1. **M6** — `cmd/analytics` consume `playback.events` và `app.logs` (handler `slog` bất đồng bộ), ghi lô vào ClickHouse; SQL mẫu (top phim, tỉ lệ xem hết, buffering theo bitrate).
2. **M7** — Trang admin trong `web/app` (danh sách, upload video và phụ đề, retry, xem lỗi, xoá phụ đề), `scripts/e2e.sh` đầy đủ, hoàn thiện tài liệu.
