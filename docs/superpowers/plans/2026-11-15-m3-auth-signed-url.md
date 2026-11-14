# Streamfilm M3: Auth + Signed URL — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Người dùng đăng ký / đăng nhập (bcrypt, JWT HS256 trong cookie `HttpOnly` `SameSite=Lax` 24 giờ), `/api/admin/*` chỉ cho role `admin` (admin đầu tiên tạo bằng `make seed-admin`), người xem đã đăng nhập gọi `GET /api/videos`, `GET /api/videos/{id}`, `GET /api/videos/{id}/play`; `/play` trả URL ký `/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8` hạn 2 giờ; nginx kiểm tra chữ ký bằng `secure_link_md5`, URL đúng → 200, sửa chữ ký hoặc `exp` quá khứ → 403, đường `/hls/` không ký không còn phục vụ. Kiểm tra bằng `make e2e-m3`.

**Architecture:** Thêm `internal/auth` (mật khẩu bcrypt, `Tokens` phát/kiểm tra JWT bằng `golang-jwt/jwt/v5`, middleware `Authenticate` / `RequireUser` / `RequireAdmin`, repository `users`, service register/login/seed-admin, handler `/api/auth/*` + `/api/me`) và `internal/signing` (MD5 base64url theo đúng biểu thức `secure_link_md5` của nginx, ký theo prefix thư mục `/hls/{id}/`). `internal/catalog` thêm `ViewerHandler` (catalog `ready`, chi tiết, `/play`). `cmd/api` tách `newRouter` thành 3 nhóm route (public / user / admin) và thêm subcommand `seed-admin`, `sign`. Cấu hình nginx chuyển thành template (`/etc/nginx/templates`, envsubst của image chính thức) để nhận `CDN_SIGNING_SECRET` chung với api. Một integration test chạy **đúng template nginx của repo** trong container và gọi bằng URL do `signing.SignedPath` sinh ra, chứng minh hai bên khớp công thức.

**Tech Stack:** Go 1.27.4, chi v5.5.0, pgx v5.13.0, **github.com/golang-jwt/jwt/v5 v5.3.1** (mới), **golang.org/x/crypto v0.58.0** (đang là indirect, chuyển thành direct; dùng `bcrypt`), testcontainers-go v0.45.0 (+ package `network`, `wait`), Docker image `nginx:1.27-alpine` (module `ngx_http_secure_link_module` có sẵn trong bản build chính thức), `openssl` (e2e tính chữ ký độc lập).

**Spec:** `docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md` (mục 4.3, 5, 6, 8, 9, 10, 11)

## Global Constraints

- Go module path: `streamfilm`. Go 1.27.4 (theo `go.mod`).
- Bảng `users (id uuid pk, email text unique, password_hash text, role text check (role in ('admin','viewer')), created_at timestamptz)`. Email lưu chữ thường.
- Mật khẩu hash bằng **bcrypt** (cost 12). Mật khẩu 8–72 byte.
- JWT ký **HS256**, lưu trong cookie **`HttpOnly`, `SameSite=Lax`**, hết hạn sau **24 giờ**. Tên cookie `sf_session`, `Path=/`. `Secure` theo `COOKIE_SECURE` (mặc định `false` vì local chạy `http://`). `JWT_SECRET` ≥ 32 ký tự, api từ chối khởi động nếu thiếu.
- `POST /api/auth/register` tạo tài khoản **viewer** (public). `POST /api/auth/login` / `/logout` đặt / xoá cookie (public). `GET /api/me`, `GET /api/videos`, `GET /api/videos/{id}`, `GET /api/videos/{id}/play`: cần đăng nhập (thiếu → **401**). `/api/admin/*`: cần role `admin` (chưa đăng nhập → **401**, viewer → **403**). Admin đầu tiên tạo bằng **`make seed-admin`**.
- Signed URL: `/cdn/s/{sig}/{exp}/hls/{id}/{file}`; **`exp` = now + 2 giờ** (Unix giây); ký theo **prefix thư mục `/hls/{id}/`** nên playlist con và segment (đường dẫn tương đối) tự mang theo chữ ký.
- MVP dùng nginx **`secure_link_md5`**: `sig` = base64url **không padding** của `MD5("{exp}/hls/{id}/ {secret}")` (đúng 22 ký tự). nginx: `secure_link $sl_sig,$sl_exp; secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";`. Package `signing` phải sinh đúng công thức này.
- nginx: chữ ký sai **hoặc** hết hạn → **403** (cả hai). Hợp lệ → proxy tới MinIO bucket `hls` có `proxy_cache` (cache key `{id}/{file}`, không chứa chữ ký). Đường `/hls/...` không ký → 404.
- `CDN_SIGNING_SECRET`: 16–128 ký tự `[A-Za-z0-9_-]` (an toàn khi chèn vào cấu hình nginx), cùng giá trị cho `api` và `nginx`.
- `/play` (M3): `{"url", "expires_at", "subtitles": [], "resume_at": 0}`. `subtitles` luôn `[]` và `resume_at` luôn `0` tới M5; `thumbnails_url` thêm ở M5. `url` là URL tuyệt đối `CDN_BASE_URL + path` (web M4 chạy khác origin với nginx).
- `GET /api/videos` (M3): chỉ video `ready`, mới nhất trước, `?page=` (1–1000, 20 video/trang), trả `has_more`. **Tìm kiếm `q` / lọc `genre` và migration `unaccent` + `pg_trgm` thuộc M4**; M3 bỏ qua hai tham số này.
- Bucket `hls` vẫn cho đọc ẩn danh trên MinIO (chỉ bind `127.0.0.1`) — thoả hiệp dev của ADR 0002, cập nhật ở Task 12. nginx không còn đường không ký.
- Integration test tự `t.Skip` khi `-short` hoặc thiếu Docker. Image MinIO trong test (nếu cần): `cgr.dev/chainguard/minio:latest-dev@sha256:5735e8125c4af7dd310dc5780508bfb850f0b68783a1a56089b5c64316a8f603`. Test nginx dùng `nginx:1.27-alpine` (trùng compose).
- Log `slog` JSON có `service`, `request_id`, `video_id` khi có. Tài liệu tiếng Việt, chỉ mô tả những gì đã chạy, cập nhật trong cùng giai đoạn (spec §11).

## Quyết định thiết kế chính (ghi ADR ở Task 12)

| Quyết định | Lý do |
|---|---|
| Biểu thức ký `"{exp}/hls/{id}/ {secret}"`, location regex bắt `sl_sig`, `sl_exp`, `sl_vid`, `sl_file` từ path | nginx tự dựng lại đúng chuỗi từ URL, không cần `map`. Dấu cách ngăn cách secret (theo ví dụ trong tài liệu nginx); secret bị giới hạn `[A-Za-z0-9_-]` nên không phá cú pháp cấu hình |
| `if ($secure_link = "")` và `if ($secure_link = "0")` đều `return 403` | Spec §4.3/§9: sai hoặc hết hạn đều 403. nginx trả `""` cho chữ ký sai/`exp` không phải số, `"0"` cho hết hạn |
| `rewrite ^ /hls/$sl_vid/$sl_file break; proxy_pass http://minio:9000;` | `proxy_pass` không chứa biến nên không cần `resolver`; URI sau rewrite được gửi nguyên cho MinIO |
| Cache key `$sl_vid/$sl_file` | Mọi người xem (mỗi người một chữ ký) dùng chung bản cache của segment; kiểm tra chữ ký chạy ở phase rewrite, trước cache, nên cache HIT vẫn cần URL hợp lệ |
| nginx config thành template `/etc/nginx/templates/default.conf.template`, `NGINX_ENVSUBST_FILTER=^CDN_` | Cơ chế có sẵn của image `nginx:1.27-alpine`; filter để envsubst không đụng biến nginx |
| Bỏ hẳn `location /hls/` không ký; dev player nhận `?src=<URL ký>`; `api sign -id` / `make sign-url` in URL ký cho dev | Spec yêu cầu chặn truy cập không ký; vẫn giữ được trang test hls.js của M1 |
| JWT chứa `sub` (user id) + `role`; middleware không truy vấn DB | Nhanh, đơn giản. Hệ quả: đổi role / logout không thu hồi token đã phát (tối đa 24 giờ) — ghi ADR 0010 |
| Đăng ký xong đăng nhập luôn (đặt cookie) | Ít bước cho web M4 |
| Login sai email chạy bcrypt với hash giả | Thời gian phản hồi không lộ email nào đã đăng ký |
| `/api/me` truy vấn DB; user không còn → 401 + xoá cookie | Một điểm kiểm tra tài khoản thật, không làm chậm các route khác |
| `seed-admin` là subcommand của binary `api`, chạy trong container (`compose exec`), đọc `ADMIN_EMAIL` / `ADMIN_PASSWORD`; chạy lại thì đặt lại mật khẩu và nâng role | Không cần Go trên máy để seed; idempotent nên e2e gọi trước mỗi lần chạy |
| `make env` bổ sung khoá mới từ `.env.example` vào `.env` sẵn có; compose dùng `${JWT_SECRET:?…}` | `.env` của M2 thiếu khoá M3; lỗi thiếu biến hiện rõ ngay khi `make up` |
| `GET /api/videos` M3 chỉ phân trang `ready`, tìm kiếm để M4 | Migration `unaccent`/`pg_trgm` đi cùng code dùng nó (M4) |

## File Structure

```
streamfilm/
  .env.example                               # + JWT_SECRET, CDN_SIGNING_SECRET, COOKIE_SECURE, ADMIN_EMAIL, ADMIN_PASSWORD
  Makefile                                   # env bổ sung khoá; seed-admin, sign-url, e2e-m2 (đăng nhập), e2e-m3
  go.mod, go.sum                             # + golang-jwt/jwt/v5 v5.3.1, x/crypto v0.58.0 direct
  migrations/0002_users.sql                  # bảng users
  internal/config/config.go(+_test)          # JWTSecret, CDNSigningSecret, CookieSecure
  internal/auth/user.go(+_test)              # Role, User, lỗi, HashPassword, CheckPassword, NormalizeEmail, ValidatePassword
  internal/auth/token.go(+_test)             # Tokens (JWT HS256), Identity, SessionTTL
  internal/auth/repo.go(+_test)              # Repo (pgx): Create, GetByEmail, GetByID, UpsertAdmin
  internal/auth/middleware.go(+_test)        # Authenticate, RequireUser, RequireAdmin, cookie
  internal/auth/service.go(+_test)           # Register, Login, Session, SeedAdmin
  internal/auth/handler.go(+_test)           # /api/auth/register|login|logout, /api/me
  internal/signing/signing.go(+_test)        # Signature, SignedPath, Signer (khớp nginx)
  internal/signing/nginx_test.go             # chạy template nginx thật trong container
  internal/catalog/repo.go(+_test)           # + ListReady
  internal/catalog/viewer.go(+_test)         # GET /api/videos, /api/videos/{id}, /api/videos/{id}/play
  internal/events/kafka.go                   # sửa comment cũ (carry-over M2)
  cmd/api/main.go                            # dispatch subcommand, secret, router mới
  cmd/api/router.go(+_test)                  # nhóm route public / user / admin
  cmd/api/commands.go                        # seed-admin, sign
  cmd/transcoder/main.go                     # chế độ file không in URL /hls/ không ký nữa
  deploy/nginx/default.conf                  # XOÁ
  deploy/nginx/templates/default.conf.template  # mới: secure_link
  deploy/docker-compose.yml                  # nginx templates + secret; api secret + CDN_BASE_URL
  web/dev/player.html                        # ?src=<URL ký>
  scripts/e2e-m2.sh                          # --max-time; đăng nhập admin; HLS qua /play
  scripts/e2e-m3.sh                          # mới
  README.md, docs/architecture.md, docs/flows.md, docs/data-model.md, docs/api.md, docs/operations.md
  docs/decisions/0002-dev-hls-bucket-read.md, 0007-accept-dual-write.md (cập nhật)
  docs/decisions/0009-signed-url-secure-link-md5.md, 0010-jwt-cookie-session.md (mới)
```

---

### Task 1: Carry-over từ review M2

**Files:**
- Modify: `internal/events/kafka.go` (comment dòng 13), `docs/decisions/0007-accept-dual-write.md`, `scripts/e2e-m2.sh`

**Interfaces:**
- Consumes: không đổi code chạy.
- Produces: không có API mới. `scripts/e2e-m2.sh` vẫn chạy được với stack M2 (chưa auth).

- [ ] **Step 1: Sửa comment cũ trong `internal/events/kafka.go`**

Thay dòng

```go
// Producer publish sự kiện; ProduceSync chờ broker xác nhận (acks=all, idempotent mặc định của franz-go).
```

bằng

```go
// Producer publish sự kiện. Publish gửi bằng Produce (bất đồng bộ) rồi tự chờ kết quả theo ctx,
// vì ProduceSync không trả về đúng hạn khi broker chết (xem Publish). acks=all, idempotent mặc định của franz-go.
```

- [ ] **Step 2: Mô tả cụ thể "ghost publish" trong ADR 0007**

Trong `docs/decisions/0007-accept-dual-write.md`, thay gạch đầu dòng đầu tiên của mục `## Hệ quả`

```markdown
- `Producer.Publish` tự chờ theo `ctx` (5 giây): `ProduceSync` của franz-go không trả về đúng hạn khi Kafka chết sau khi client đã kết nối, nên `/complete` từng treo thay vì trả `503`. Nếu ctx hết hạn trước, record có thể vẫn được gửi sau đó; consumer idempotent nên chấp nhận.
```

bằng

```markdown
- `Producer.Publish` tự chờ theo `ctx` (5 giây): `ProduceSync` của franz-go không trả về đúng hạn khi Kafka chết sau khi client đã kết nối, nên `/complete` từng treo thay vì trả `503`.
- **Ghost publish.** Khi ctx hết hạn, `/complete` trả `503` và đưa video về `uploading`, nhưng record `video.uploaded` có thể vẫn nằm trong buffer của franz-go và tới broker muộn (khi Kafka sống lại). Transcoder nhận nó như mọi job: publish `video.processing`, tải raw, transcode, upload `hls/{id}/`, publish `video.transcoded`. Lúc đó video đang `uploading`, nên consumer của api gặp chuyển `uploading → processing` / `uploading → ready` (hoặc `uploading → failed`) không hợp lệ, log và bỏ qua; trạng thái không đổi. Hệ quả: tốn CPU cho một lần transcode thừa và có thể để lại output HLS mồ côi dưới `hls/{id}/` (job dọn upload bỏ dở chỉ xoá object raw và multipart, không xoá `hls/`). Admin gọi lại `/complete` là an toàn: video `uploading → queued`, publish bản mới, transcoder ghi đè `hls/{id}/`, video tới `ready` như bình thường; nếu bản "ma" và bản mới cùng được xử lý thì kết quả vẫn idempotent.
```

- [ ] **Step 3: Thêm `--max-time` cho mọi lệnh `curl` trong `scripts/e2e-m2.sh`**

Ghi đè `scripts/e2e-m2.sh` (gọi API 30 giây, upload một part 64 MiB tối đa 300 giây):

```bash
#!/usr/bin/env bash
# E2E M2: tạo video → upload multipart qua presigned URL → complete → đợi ready → kiểm tra HLS qua nginx.
# Dùng: scripts/e2e-m2.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, split.
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
API="${API:-http://localhost:8000}"
CDN="${CDN:-http://localhost:8088}"
TIMEOUT="${TIMEOUT:-300}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
[ -f "$FILE" ] || die "không thấy $FILE (chạy: make sample)"

SIZE=$(wc -c < "$FILE" | tr -d ' ')
NAME=$(basename "$FILE")

echo "1/5 tạo video ($NAME, $SIZE bytes)"
BODY=$(jq -n --arg t "E2E $(date +%H:%M:%S)" --arg f "$NAME" --argjson s "$SIZE" \
  '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}')
CREATE=$(curl -fsS --max-time 30 -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d "$BODY")
ID=$(echo "$CREATE" | jq -r .id)
PART_SIZE=$(echo "$CREATE" | jq -r .upload.part_size)
PARTS=$(echo "$CREATE" | jq -r '.upload.parts | length')
echo "    id=$ID parts=$PARTS part_size=$PART_SIZE"

echo "2/5 upload $PARTS part qua presigned URL"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
split -b "$PART_SIZE" -a 4 "$FILE" "$WORK/part."
i=0
for part in $(ls "$WORK"/part.* | sort); do
  i=$((i + 1))
  URL=$(echo "$CREATE" | jq -r --argjson n "$i" '.upload.parts[] | select(.part_number == $n) | .url')
  code=$(curl -sS --max-time 300 -o /dev/null -w '%{http_code}' -X PUT --upload-file "$part" "$URL")
  [ "$code" = 200 ] || die "upload part $i: HTTP $code"
done
[ "$i" = "$PARTS" ] || die "đã upload $i part, API cần $PARTS"

echo "3/5 complete"
curl -fsS --max-time 30 -X POST "$API/api/admin/videos/$ID/complete" | jq -c .

echo "4/5 đợi status=ready (tối đa ${TIMEOUT}s)"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  V=$(curl -fsS --max-time 30 "$API/api/admin/videos/$ID")
  STATUS=$(echo "$V" | jq -r .status)
  echo "    status=$STATUS"
  case "$STATUS" in
    ready) break ;;
    failed) die "transcode lỗi: $(echo "$V" | jq -r .fail_reason)" ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || die "hết thời gian chờ"
  sleep 3
done
echo "$V" | jq -c '{duration_s, renditions: [.renditions[].height]}'

echo "5/5 kiểm tra HLS qua nginx"
curl -fsS --max-time 30 "$CDN/hls/$ID/master.m3u8" | grep -q '#EXTM3U' || die "master.m3u8 không hợp lệ"
for key in $(echo "$V" | jq -r '.renditions[].playlist_key'); do
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$CDN/hls/$key")
  [ "$code" = 200 ] || die "$key: HTTP $code"
done
PLAYLIST=$(echo "$V" | jq -r '.renditions[0].playlist_key')
SEG=$(curl -fsS --max-time 30 "$CDN/hls/$PLAYLIST" | grep -m1 '\.ts$')
code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$CDN/hls/$(dirname "$PLAYLIST")/$SEG")
[ "$code" = 200 ] || die "segment $SEG: HTTP $code"

echo "OK  $CDN/dev/player.html?id=$ID"
```

- [ ] **Step 4: Kiểm tra**

Run: `bash -n scripts/e2e-m2.sh && grep -c 'curl ' scripts/e2e-m2.sh && grep 'curl ' scripts/e2e-m2.sh | grep -vc -- '--max-time' ; go build ./...`
Expected: `9` (số dòng có `curl `), rồi `1` (dòng duy nhất không có `--max-time` là `for c in curl jq split`, không phải lời gọi curl), build không lỗi.

Nếu stack M2 đang chạy: `make e2e-m2` → in `OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/events/kafka.go docs/decisions/0007-accept-dual-write.md scripts/e2e-m2.sh
git commit -m "fix: M2 review carry-overs (ghost publish ADR, stale comment, curl timeouts)"
```

---

### Task 2: Cấu hình M3 và `.env`

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `.env.example`, `Makefile`

**Interfaces:**
- Consumes: `config.Config` (M2).
- Produces:
  ```go
  type Config struct {
  	// ... các trường M2 giữ nguyên ...
  	JWTSecret, CDNSigningSecret string // JWT_SECRET, CDN_SIGNING_SECRET; không có mặc định
  	CookieSecure                bool   // COOKIE_SECURE, mặc định false
  }
  ```
  `CDNBaseURL` (`CDN_BASE_URL`, mặc định `http://localhost:8088`) đã có từ M1, M3 dùng để dựng URL `/play`.

- [ ] **Step 1: Viết test thất bại** — thêm vào cuối `internal/config/config_test.go`:

```go
func TestLoadM3(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("CDN_SIGNING_SECRET", "")
	t.Setenv("COOKIE_SECURE", "")
	c := Load()
	if c.JWTSecret != "" || c.CDNSigningSecret != "" || c.CookieSecure {
		t.Errorf("defaults = %q %q %v", c.JWTSecret, c.CDNSigningSecret, c.CookieSecure)
	}
	t.Setenv("JWT_SECRET", "jwt-secret")
	t.Setenv("CDN_SIGNING_SECRET", "cdn-secret")
	t.Setenv("COOKIE_SECURE", "true")
	c = Load()
	if c.JWTSecret != "jwt-secret" || c.CDNSigningSecret != "cdn-secret" || !c.CookieSecure {
		t.Errorf("override = %q %q %v", c.JWTSecret, c.CDNSigningSecret, c.CookieSecure)
	}
	t.Setenv("COOKIE_SECURE", "not-bool")
	if Load().CookieSecure {
		t.Error("COOKIE_SECURE sai định dạng phải về false")
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/config/`
Expected: `FAIL` build: `c.JWTSecret undefined (type Config has no field or method JWTSecret)`.

- [ ] **Step 3: Viết lại `internal/config/config.go`**

```go
// Package config đọc cấu hình từ biến môi trường.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	S3Endpoint, S3PublicEndpoint, S3Region, S3AccessKey, S3SecretKey string
	BucketRaw, BucketHLS                                             string
	CDNBaseURL                                                       string
	FFmpegPath, FFprobePath                                          string
	DatabaseURL                                                      string
	KafkaBrokers                                                     []string
	HTTPAddr                                                         string
	WorkDir                                                          string
	StaleUploadAfter, CleanupInterval                                time.Duration
	JWTSecret, CDNSigningSecret                                      string
	CookieSecure                                                     bool
}

func Load() Config {
	endpoint := get("S3_ENDPOINT", "http://localhost:9000")
	return Config{
		S3Endpoint: endpoint,
		// Địa chỉ mà client bên ngoài (trình duyệt, curl trên host) dùng để upload bằng presigned URL.
		S3PublicEndpoint: get("S3_PUBLIC_ENDPOINT", endpoint),
		S3Region:         get("S3_REGION", "us-east-1"),
		S3AccessKey:      get("S3_ACCESS_KEY", "streamfilm"),
		S3SecretKey:      get("S3_SECRET_KEY", "streamfilm-secret"),
		BucketRaw:        get("S3_BUCKET_RAW", "raw"),
		BucketHLS:        get("S3_BUCKET_HLS", "hls"),
		CDNBaseURL:       get("CDN_BASE_URL", "http://localhost:8088"),
		FFmpegPath:       get("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:      get("FFPROBE_PATH", "ffprobe"),
		DatabaseURL:      get("DATABASE_URL", "postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable"),
		KafkaBrokers:     splitList(get("KAFKA_BROKERS", "localhost:29092")),
		HTTPAddr:         get("HTTP_ADDR", ":8000"),
		WorkDir:          get("WORK_DIR", os.TempDir()),
		StaleUploadAfter: getDuration("STALE_UPLOAD_AFTER", 24*time.Hour),
		CleanupInterval:  getDuration("CLEANUP_INTERVAL", time.Hour),
		// Không có mặc định: api từ chối khởi động nếu thiếu (auth.NewTokens, signing.NewSigner kiểm tra).
		JWTSecret:        os.Getenv("JWT_SECRET"),
		CDNSigningSecret: os.Getenv("CDN_SIGNING_SECRET"),
		// false khi chạy http://localhost; bật khi có HTTPS.
		CookieSecure: getBool("COOKIE_SECURE", false),
	}
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getDuration đọc duration kiểu Go ("24h", "90m"); rỗng, sai định dạng hoặc <= 0 thì dùng mặc định.
func getDuration(key string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(key))
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// getBool đọc "true"/"false"/"1"/"0"; rỗng hoặc sai định dạng thì dùng mặc định.
func getBool(key string, def bool) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return b
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Chạy test**

Run: `go test ./internal/config/`
Expected: `ok  	streamfilm/internal/config`

- [ ] **Step 5: Thêm khoá M3 vào cuối `.env.example`**

```
# M3: auth + signed URL. Giá trị dưới chỉ dùng cho local; đổi khi chạy ở nơi khác.
# JWT_SECRET >= 32 ký tự. CDN_SIGNING_SECRET 16-128 ký tự [A-Za-z0-9_-], dùng chung cho api và nginx.
JWT_SECRET=dev-jwt-secret-change-me-0123456789abcdef
CDN_SIGNING_SECRET=dev-cdn-secret-change-me
COOKIE_SECURE=false
# Tài khoản admin mà `make seed-admin` tạo và script e2e dùng để đăng nhập.
ADMIN_EMAIL=admin@streamfilm.local
ADMIN_PASSWORD=admin-dev-password
```

- [ ] **Step 6: `make env` bổ sung khoá mới cho `.env` đã có**

Trong `Makefile`, thay

```make
env:
	@test -f .env || cp .env.example .env
```

bằng

```make
# Tạo .env từ .env.example; nếu .env đã có thì bổ sung các khoá mới (vd. khoá M3) mà không ghi đè giá trị cũ.
env:
	@test -f .env || cp .env.example .env
	@for k in $$(grep -oE '^[A-Z0-9_]+=' .env.example | tr -d =); do \
	  grep -q "^$$k=" .env || { grep "^$$k=" .env.example >> .env; echo "added $$k to .env"; }; \
	done
```

- [ ] **Step 7: Kiểm tra `make env`**

Run: `make env && grep -c '^JWT_SECRET=\|^CDN_SIGNING_SECRET=\|^ADMIN_EMAIL=' .env && make env`
Expected: lần đầu in `added JWT_SECRET to .env` … (với `.env` từ M2), sau đó `3`; lần `make env` thứ hai không in gì.

- [ ] **Step 8: Commit**

```bash
git add internal/config .env.example Makefile
git commit -m "feat(config): JWT and CDN signing secrets, cookie secure flag"
```

---

### Task 3: `internal/auth` — mật khẩu và JWT

**Files:**
- Create: `internal/auth/user.go`, `internal/auth/user_test.go`, `internal/auth/token.go`, `internal/auth/token_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: không.
- Produces:
  ```go
  type Role string // RoleAdmin = "admin", RoleViewer = "viewer"
  func (r Role) Valid() bool
  type User struct { ID, Email string; PasswordHash string; Role Role; CreatedAt time.Time }
  var ErrInvalidInput, ErrEmailTaken, ErrInvalidCredentials, ErrUserNotFound error
  const MinPasswordLen = 8; const MaxPasswordLen = 72
  func HashPassword(pw string) (string, error)
  func CheckPassword(hash, pw string) bool
  func NormalizeEmail(s string) (string, error)
  func ValidatePassword(pw string) error

  const SessionTTL = 24 * time.Hour; const MinSecretLen = 32
  var ErrInvalidToken error
  type Identity struct { UserID string; Role Role }
  type Tokens struct { /* unexported */ }
  func NewTokens(secret string, ttl time.Duration, now func() time.Time) (*Tokens, error)
  func (t *Tokens) Issue(id Identity) (string, time.Time, error)
  func (t *Tokens) Parse(s string) (Identity, error)
  ```

- [ ] **Step 1: Thêm dependency**

Run: `go get github.com/golang-jwt/jwt/v5@v5.3.1 golang.org/x/crypto@v0.58.0`
Expected: `go: added github.com/golang-jwt/jwt/v5 v5.3.1` (x/crypto đã có ở `go.sum`). `go mod tidy` ở Step 8 sẽ chuyển cả hai vào khối `require` trực tiếp.

- [ ] **Step 2: Viết test thất bại cho mật khẩu và email** — `internal/auth/user_test.go`:

```go
package auth

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	hashCost = bcrypt.MinCost // test nhanh; production dùng 12
	os.Exit(m.Run())
}

func TestHashAndCheckPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2a$") {
		t.Errorf("hash = %q, want bcrypt", h)
	}
	if !CheckPassword(h, "correct horse") {
		t.Error("đúng mật khẩu phải khớp")
	}
	if CheckPassword(h, "wrong horse") || CheckPassword("not-a-hash", "correct horse") {
		t.Error("sai mật khẩu hoặc hash hỏng phải không khớp")
	}
}

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail("  An.Nguyen@Example.COM ")
	if err != nil || got != "an.nguyen@example.com" {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "abc", "a@", "An <a@b.com>", strings.Repeat("a", 250) + "@b.com"} {
		if _, err := NormalizeEmail(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("12345678"); err != nil {
		t.Error(err)
	}
	for _, bad := range []string{"1234567", strings.Repeat("x", 73)} {
		if err := ValidatePassword(bad); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("len %d: err = %v", len(bad), err)
		}
	}
}
```

- [ ] **Step 3: Viết test thất bại cho JWT** — `internal/auth/token_test.go`:

```go
package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-0123456789-0123456789-abcdef"

func newTestTokens(t *testing.T, now *time.Time) *Tokens {
	t.Helper()
	tk, err := NewTokens(testSecret, SessionTTL, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestNewTokensRejectsShortSecret(t *testing.T) {
	if _, err := NewTokens("short", SessionTTL, time.Now); err == nil {
		t.Fatal("want error for short secret")
	}
}

func TestIssueParse(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	tk := newTestTokens(t, &now)
	tok, exp, err := tk.Issue(Identity{UserID: "u1", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(24 * time.Hour)) {
		t.Errorf("exp = %v", exp)
	}
	id, err := tk.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if id != (Identity{UserID: "u1", Role: RoleAdmin}) {
		t.Errorf("identity = %+v", id)
	}

	now = now.Add(24*time.Hour + time.Second)
	if _, err := tk.Parse(tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token: err = %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	tk := newTestTokens(t, &now)
	good, _, _ := tk.Issue(Identity{UserID: "u1", Role: RoleViewer})

	other, err := NewTokens(strings.Repeat("x", 40), SessionTTL, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	otherTok, _, _ := other.Issue(Identity{UserID: "u1", Role: RoleAdmin})

	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": "u1", "role": "admin", "iss": "streamfilm", "exp": now.Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	noExp, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "role": "admin", "iss": "streamfilm",
	}).SignedString([]byte(testSecret))
	badRole, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "role": "root", "iss": "streamfilm", "exp": now.Add(time.Hour).Unix(),
	}).SignedString([]byte(testSecret))
	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]

	cases := map[string]string{
		"other secret": otherTok, "alg none": none, "no exp": noExp, "bad role": badRole,
		"tampered": tampered, "garbage": "abc", "empty": "",
	}
	for name, tok := range cases {
		if _, err := tk.Parse(tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
```

- [ ] **Step 4: Chạy test, xác nhận thất bại**

Run: `go test ./internal/auth/`
Expected: `FAIL` build: `undefined: hashCost`, `undefined: HashPassword`, `undefined: NewTokens`, …

- [ ] **Step 5: Viết `internal/auth/user.go`**

```go
// Package auth: tài khoản (bcrypt), phiên đăng nhập bằng JWT HS256 trong cookie HttpOnly,
// middleware phân quyền admin/viewer.
package auth

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleViewer }

type User struct {
	ID, Email    string
	PasswordHash string
	Role         Role
	CreatedAt    time.Time
}

var (
	ErrInvalidInput       = errors.New("invalid input")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrUserNotFound       = errors.New("user not found")
)

const (
	MinPasswordLen = 8
	MaxPasswordLen = 72 // bcrypt chỉ dùng 72 byte đầu; dài hơn thì từ chối thay vì cắt ngầm
)

// hashCost là cost của bcrypt. Biến (không phải hằng) để test hạ xuống bcrypt.MinCost.
var hashCost = 12

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), hashCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(b), nil
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// NormalizeEmail cắt khoảng trắng, chuyển chữ thường và kiểm tra dạng "a@b" (không nhận "Tên <a@b>").
func NormalizeEmail(s string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(s))
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 254 {
		return "", fmt.Errorf("%w: email is not valid", ErrInvalidInput)
	}
	return e, nil
}

func ValidatePassword(pw string) error {
	if n := len(pw); n < MinPasswordLen || n > MaxPasswordLen {
		return fmt.Errorf("%w: password must be %d-%d bytes", ErrInvalidInput, MinPasswordLen, MaxPasswordLen)
	}
	return nil
}
```

- [ ] **Step 6: Viết `internal/auth/token.go`**

```go
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SessionTTL: phiên đăng nhập (JWT và cookie) hết hạn sau 24 giờ (spec §8).
const SessionTTL = 24 * time.Hour

const issuer = "streamfilm"

// MinSecretLen: HS256 cần khoá ít nhất 256 bit.
const MinSecretLen = 32

var ErrInvalidToken = errors.New("invalid session token")

// Identity là người dùng của request hiện tại, lấy từ JWT (không truy vấn DB).
type Identity struct {
	UserID string
	Role   Role
}

type claims struct {
	Role Role `json:"role"`
	jwt.RegisteredClaims
}

// Tokens phát và kiểm tra JWT HS256.
type Tokens struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokens(secret string, ttl time.Duration, now func() time.Time) (*Tokens, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("JWT_SECRET must be at least %d characters", MinSecretLen)
	}
	return &Tokens{secret: []byte(secret), ttl: ttl, now: now}, nil
}

// Issue trả token và thời điểm hết hạn.
func (t *Tokens) Issue(id Identity) (string, time.Time, error) {
	now := t.now()
	exp := now.Add(t.ttl)
	c := claims{Role: id.Role, RegisteredClaims: jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   id.UserID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(exp),
	}}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return s, exp, nil
}

// Parse chỉ nhận HS256 (chặn "alg":"none" và nhầm thuật toán), bắt buộc có exp và đúng issuer.
func (t *Tokens) Parse(s string) (Identity, error) {
	var c claims
	_, err := jwt.ParseWithClaims(s, &c, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if c.Subject == "" || !c.Role.Valid() {
		return Identity{}, fmt.Errorf("%w: missing subject or role", ErrInvalidToken)
	}
	return Identity{UserID: c.Subject, Role: c.Role}, nil
}
```

- [ ] **Step 7: Chạy test**

Run: `go test ./internal/auth/`
Expected: `ok  	streamfilm/internal/auth`

- [ ] **Step 8: Tidy và commit**

Run: `go mod tidy && grep -nE 'golang-jwt/jwt/v5 v5.3.1$|golang.org/x/crypto v0.58.0$' go.mod`
Expected: hai dòng, không có `// indirect`.

```bash
git add go.mod go.sum internal/auth
git commit -m "feat(auth): bcrypt passwords and HS256 JWT tokens"
```

---

### Task 4: Migration `users` và repository

**Files:**
- Create: `migrations/0002_users.sql`, `internal/auth/repo.go`, `internal/auth/repo_test.go`

**Interfaces:**
- Consumes: `db.Connect`, `db.Migrate`, `migrations.FS`, `testutil.StartPostgres` (M2); `auth.User`, `auth.Role`, `ErrEmailTaken`, `ErrUserNotFound` (Task 3).
- Produces:
  ```go
  type Repo struct { /* pool */ }
  func NewRepo(pool *pgxpool.Pool) *Repo
  func (r *Repo) Create(ctx context.Context, u User) error            // email trùng → ErrEmailTaken
  func (r *Repo) GetByEmail(ctx context.Context, email string) (User, error) // không có → ErrUserNotFound
  func (r *Repo) GetByID(ctx context.Context, id string) (User, error)
  func (r *Repo) UpsertAdmin(ctx context.Context, u User) (User, bool, error) // bool = vừa tạo mới
  ```

- [ ] **Step 1: Viết test thất bại** — `internal/auth/repo_test.go`:

```go
package auth

import (
	"context"
	"errors"
	"testing"

	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

func TestRepo(t *testing.T) {
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
	r := NewRepo(pool)

	const (
		idV = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		idA = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	if err := r.Create(ctx, User{ID: idV, Email: "v@example.com", PasswordHash: "h1", Role: RoleViewer}); err != nil {
		t.Fatal(err)
	}
	err = r.Create(ctx, User{ID: idA, Email: "v@example.com", PasswordHash: "h2", Role: RoleViewer})
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate email: err = %v", err)
	}

	u, err := r.GetByEmail(ctx, "v@example.com")
	if err != nil || u.ID != idV || u.Role != RoleViewer || u.PasswordHash != "h1" || u.CreatedAt.IsZero() {
		t.Errorf("GetByEmail = %+v, %v", u, err)
	}
	if u, err := r.GetByID(ctx, idV); err != nil || u.Email != "v@example.com" {
		t.Errorf("GetByID = %+v, %v", u, err)
	}
	if _, err := r.GetByEmail(ctx, "none@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("missing email: err = %v", err)
	}
	if _, err := r.GetByID(ctx, idA); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("missing id: err = %v", err)
	}

	admin, created, err := r.UpsertAdmin(ctx, User{ID: idA, Email: "admin@example.com", PasswordHash: "ha"})
	if err != nil || !created || admin.ID != idA || admin.Role != RoleAdmin {
		t.Errorf("UpsertAdmin new = %+v, %v, %v", admin, created, err)
	}
	// Email đã có (viewer): giữ id cũ, đổi mật khẩu, nâng role.
	up, created, err := r.UpsertAdmin(ctx, User{ID: idA, Email: "v@example.com", PasswordHash: "h9"})
	if err != nil || created || up.ID != idV || up.Role != RoleAdmin || up.PasswordHash != "h9" {
		t.Errorf("UpsertAdmin existing = %+v, %v, %v", up, created, err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role)
		VALUES ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'Upper@example.com', 'h', 'viewer')`); err == nil {
		t.Error("CHECK email = lower(email) phải chặn email chữ hoa")
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -run TestRepo ./internal/auth/`
Expected: `FAIL` build: `undefined: NewRepo`.

- [ ] **Step 3: Viết migration** — `migrations/0002_users.sql`:

```sql
-- M3: tài khoản. Email lưu dạng chữ thường (Go chuẩn hoá trước khi ghi) nên UNIQUE trên email là đủ.
CREATE TABLE users (
  id            uuid PRIMARY KEY,
  email         text NOT NULL UNIQUE CHECK (email = lower(email)),
  password_hash text NOT NULL,
  role          text NOT NULL CHECK (role IN ('admin','viewer')),
  created_at    timestamptz NOT NULL DEFAULT now()
);
```

- [ ] **Step 4: Viết `internal/auth/repo.go`**

```go
package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo lưu tài khoản trong bảng users.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const userColumns = `id::text, email, password_hash, role, created_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	var role string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	u.Role = Role(role)
	return u, err
}

// Create thêm tài khoản; email đã tồn tại thì trả ErrEmailTaken.
func (r *Repo) Create(ctx context.Context, u User) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, $3, $4)`,
		u.ID, u.Email, u.PasswordHash, string(u.Role))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return ErrEmailTaken
	}
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (r *Repo) GetByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

func (r *Repo) GetByID(ctx context.Context, id string) (User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// UpsertAdmin tạo admin, hoặc nếu email đã có thì đặt lại mật khẩu và nâng role lên admin.
// created = true khi là bản ghi mới (xmax = 0 nghĩa là dòng vừa INSERT, không phải UPDATE).
func (r *Repo) UpsertAdmin(ctx context.Context, u User) (User, bool, error) {
	var created bool
	out, err := scanUserCreated(r.pool.QueryRow(ctx, `INSERT INTO users (id, email, password_hash, role)
		VALUES ($1, $2, $3, 'admin')
		ON CONFLICT (email) DO UPDATE SET password_hash = EXCLUDED.password_hash, role = 'admin'
		RETURNING `+userColumns+`, (xmax = 0)`, u.ID, u.Email, u.PasswordHash), &created)
	if err != nil {
		return User{}, false, fmt.Errorf("upsert admin: %w", err)
	}
	return out, created, nil
}

func scanUserCreated(row pgx.Row, created *bool) (User, error) {
	var u User
	var role string
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &role, &u.CreatedAt, created)
	u.Role = Role(role)
	return u, err
}
```

- [ ] **Step 5: Chạy test (cần Docker)**

Run: `go test -run TestRepo ./internal/auth/ && go test ./internal/db/`
Expected: `ok  	streamfilm/internal/auth` và `ok  	streamfilm/internal/db` (test migrate của M2 vẫn qua: `applied[0]` vẫn là `0001_videos`).

- [ ] **Step 6: Commit**

```bash
git add migrations/0002_users.sql internal/auth/repo.go internal/auth/repo_test.go
git commit -m "feat(auth): users table and Postgres repository"
```

---

### Task 5: Middleware phân quyền và cookie phiên

**Files:**
- Create: `internal/auth/middleware.go`, `internal/auth/middleware_test.go`

**Interfaces:**
- Consumes: `Tokens.Parse`, `Identity`, `RoleAdmin` (Task 3); `httpx.WriteError` (M2).
- Produces:
  ```go
  const CookieName = "sf_session"
  func WithIdentity(ctx context.Context, id Identity) context.Context
  func FromContext(ctx context.Context) (Identity, bool)
  func (t *Tokens) Authenticate(next http.Handler) http.Handler // gắn Identity nếu cookie hợp lệ, không chặn
  func RequireUser(next http.Handler) http.Handler             // 401
  func RequireAdmin(next http.Handler) http.Handler            // 401 / 403
  func SetSessionCookie(w http.ResponseWriter, token string, exp time.Time, secure bool)
  func ClearSessionCookie(w http.ResponseWriter, secure bool)
  ```

- [ ] **Step 1: Viết test thất bại** — `internal/auth/middleware_test.go`:

```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddleware(t *testing.T) {
	now := time.Now()
	tk := newTestTokens(t, &now)
	admin, _, _ := tk.Issue(Identity{UserID: "a1", Role: RoleAdmin})
	viewer, _, _ := tk.Issue(Identity{UserID: "v1", Role: RoleViewer})

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		w.Write([]byte(id.UserID))
	})
	cases := []struct {
		name, cookie string
		mw           func(http.Handler) http.Handler
		want         int
	}{
		{"user: no cookie", "", RequireUser, 401},
		{"user: bad token", "garbage", RequireUser, 401},
		{"user: viewer", viewer, RequireUser, 200},
		{"admin: no cookie", "", RequireAdmin, 401},
		{"admin: viewer", viewer, RequireAdmin, 403},
		{"admin: admin", admin, RequireAdmin, 200},
	}
	for _, c := range cases {
		h := tk.Authenticate(c.mw(ok))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: CookieName, Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: code = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestSessionCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	SetSessionCookie(rec, "tok", time.Now().Add(SessionTTL), false)
	c := rec.Result().Cookies()[0]
	if c.Name != CookieName || c.Value != "tok" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode ||
		c.Path != "/" || c.Secure || c.MaxAge < 86390 || c.MaxAge > 86400 {
		t.Errorf("cookie = %+v", c)
	}
	rec = httptest.NewRecorder()
	ClearSessionCookie(rec, true)
	c = rec.Result().Cookies()[0]
	if c.Name != CookieName || c.Value != "" || c.MaxAge != -1 || !c.Secure {
		t.Errorf("clear cookie = %+v", c)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/auth/`
Expected: `FAIL` build: `undefined: RequireUser`, `undefined: CookieName`, …

- [ ] **Step 3: Viết `internal/auth/middleware.go`**

```go
package auth

import (
	"context"
	"net/http"
	"time"

	"streamfilm/internal/httpx"
)

// CookieName là cookie chứa JWT phiên đăng nhập.
const CookieName = "sf_session"

type ctxKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Authenticate đọc cookie phiên; token hợp lệ thì gắn Identity vào context.
// Không chặn request: RequireUser / RequireAdmin quyết định.
func (t *Tokens) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(CookieName); err == nil {
			if id, err := t.Parse(c.Value); err == nil {
				r = r.WithContext(WithIdentity(r.Context(), id))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireUser trả 401 nếu chưa đăng nhập.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "login required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin trả 401 nếu chưa đăng nhập, 403 nếu không phải admin.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "login required")
			return
		}
		if id.Role != RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetSessionCookie đặt cookie HttpOnly, SameSite=Lax, sống tới exp (spec §8).
func SetSessionCookie(w http.ResponseWriter, token string, exp time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Expires: exp, MaxAge: int(time.Until(exp).Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}

// ClearSessionCookie xoá cookie phiên phía trình duyệt.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
	})
}
```

- [ ] **Step 4: Chạy test**

Run: `go test -short ./internal/auth/`
Expected: `ok  	streamfilm/internal/auth`

- [ ] **Step 5: Commit**

```bash
git add internal/auth/middleware.go internal/auth/middleware_test.go
git commit -m "feat(auth): session cookie and RequireUser/RequireAdmin middleware"
```

---

### Task 6: Service và HTTP handler: register, login, logout, me

**Files:**
- Create: `internal/auth/service.go`, `internal/auth/service_test.go`, `internal/auth/handler.go`, `internal/auth/handler_test.go`

**Interfaces:**
- Consumes: Task 3–5; `httpx.DecodeJSON`, `httpx.WriteJSON`, `httpx.WriteError` (M2).
- Produces:
  ```go
  type UserStore interface {
  	Create(ctx context.Context, u User) error
  	GetByEmail(ctx context.Context, email string) (User, error)
  	GetByID(ctx context.Context, id string) (User, error)
  	UpsertAdmin(ctx context.Context, u User) (User, bool, error)
  } // *Repo thoả
  type Service struct { Users UserStore; Tokens *Tokens; NewID func() string }
  func (s *Service) Register(ctx context.Context, email, password string) (User, error) // role viewer
  func (s *Service) Login(ctx context.Context, email, password string) (User, error)
  func (s *Service) Session(u User) (string, time.Time, error)
  func (s *Service) SeedAdmin(ctx context.Context, email, password string) (User, bool, error)

  type Handler struct { Svc *Service; SecureCookie bool; Log *slog.Logger }
  func (h *Handler) Routes(r chi.Router)
  // POST /api/auth/register {email,password} → 201 UserJSON + cookie | 400 | 409
  // POST /api/auth/login    {email,password} → 200 UserJSON + cookie | 400 | 401
  // POST /api/auth/logout                    → 204, xoá cookie
  // GET  /api/me (RequireUser)               → 200 UserJSON | 401
  type UserJSON struct { ID string `json:"id"`; Email string `json:"email"`; Role Role `json:"role"`; CreatedAt time.Time `json:"created_at"` }
  ```

- [ ] **Step 1: Viết test thất bại cho service** — `internal/auth/service_test.go`:

```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeUsers là UserStore trong bộ nhớ.
type fakeUsers struct {
	byID map[string]User
}

func newFakeUsers() *fakeUsers { return &fakeUsers{byID: map[string]User{}} }

func (f *fakeUsers) Create(_ context.Context, u User) error {
	for _, x := range f.byID {
		if x.Email == u.Email {
			return ErrEmailTaken
		}
	}
	u.CreatedAt = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	f.byID[u.ID] = u
	return nil
}

func (f *fakeUsers) GetByEmail(_ context.Context, email string) (User, error) {
	for _, u := range f.byID {
		if u.Email == email {
			return u, nil
		}
	}
	return User{}, ErrUserNotFound
}

func (f *fakeUsers) GetByID(_ context.Context, id string) (User, error) {
	u, ok := f.byID[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (f *fakeUsers) UpsertAdmin(_ context.Context, u User) (User, bool, error) {
	for id, x := range f.byID {
		if x.Email == u.Email {
			x.PasswordHash, x.Role = u.PasswordHash, RoleAdmin
			f.byID[id] = x
			return x, false, nil
		}
	}
	u.Role = RoleAdmin
	f.byID[u.ID] = u
	return u, true, nil
}

func newTestService(t *testing.T) (*Service, *fakeUsers) {
	t.Helper()
	now := time.Now()
	users := newFakeUsers()
	n := 0
	return &Service{Users: users, Tokens: newTestTokens(t, &now), NewID: func() string {
		n++
		return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
	}}, users
}

func TestRegisterLogin(t *testing.T) {
	s, users := newTestService(t)
	ctx := context.Background()

	u, err := s.Register(ctx, " An@Example.com ", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "an@example.com" || u.Role != RoleViewer || u.PasswordHash == "password123" {
		t.Errorf("user = %+v", u)
	}
	if _, err := s.Register(ctx, "an@example.com", "password456"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err := s.Register(ctx, "bad", "password123"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad email: err = %v", err)
	}
	if _, err := s.Register(ctx, "b@example.com", "short"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("short password: err = %v", err)
	}
	if len(users.byID) != 1 {
		t.Errorf("users = %d, want 1", len(users.byID))
	}

	got, err := s.Login(ctx, "AN@example.com", "password123")
	if err != nil || got.ID != u.ID {
		t.Errorf("login = %+v, %v", got, err)
	}
	for _, c := range [][2]string{{"an@example.com", "wrong-pass"}, {"nobody@example.com", "password123"}, {"bad", "x"}} {
		if _, err := s.Login(ctx, c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("login %v: err = %v", c, err)
		}
	}

	tok, _, err := s.Session(got)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Tokens.Parse(tok)
	if err != nil || id.UserID != u.ID || id.Role != RoleViewer {
		t.Errorf("session identity = %+v, %v", id, err)
	}
}

func TestSeedAdmin(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	a, created, err := s.SeedAdmin(ctx, "Admin@Streamfilm.local", "admin-pass-1")
	if err != nil || !created || a.Role != RoleAdmin || a.Email != "admin@streamfilm.local" {
		t.Fatalf("seed = %+v, %v, %v", a, created, err)
	}
	a2, created, err := s.SeedAdmin(ctx, "admin@streamfilm.local", "admin-pass-2")
	if err != nil || created || a2.ID != a.ID {
		t.Errorf("reseed = %+v, %v, %v", a2, created, err)
	}
	if _, err := s.Login(ctx, "admin@streamfilm.local", "admin-pass-2"); err != nil {
		t.Errorf("login with new password: %v", err)
	}
	if _, _, err := s.SeedAdmin(ctx, "admin@streamfilm.local", "short"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("short password: err = %v", err)
	}
}
```

- [ ] **Step 2: Viết test thất bại cho handler** — `internal/auth/handler_test.go`:

```go
package auth

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newTestRouter(t *testing.T) (http.Handler, *Service) {
	t.Helper()
	s, _ := newTestService(t)
	r := chi.NewRouter()
	r.Use(s.Tokens.Authenticate)
	(&Handler{Svc: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r, s
}

func do(h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", CookieName)
	return nil
}

func TestHandlerFlow(t *testing.T) {
	h, _ := newTestRouter(t)

	rec := do(h, "POST", "/api/auth/register", `{"email":"V@Example.com","password":"password123"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var u UserJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.Email != "v@example.com" || u.Role != RoleViewer || u.ID == "" {
		t.Errorf("register body = %+v", u)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Error("response không được chứa password/hash")
	}
	regCookie := sessionCookie(t, rec)

	if rec := do(h, "POST", "/api/auth/register", `{"email":"v@example.com","password":"password123"}`); rec.Code != http.StatusConflict {
		t.Errorf("duplicate register: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/register", `{"email":"x","password":"password123"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad email: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/auth/register", `{"email":"y@example.com","password":"password123","role":"admin"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field role phải bị từ chối: %d", rec.Code)
	}

	if rec := do(h, "POST", "/api/auth/login", `{"email":"v@example.com","password":"wrong-password"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", rec.Code)
	}
	rec = do(h, "POST", "/api/auth/login", `{"email":"v@example.com","password":"password123"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	loginCookie := sessionCookie(t, rec)

	if rec := do(h, "GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("me without cookie: %d", rec.Code)
	}
	for _, c := range []*http.Cookie{regCookie, loginCookie} {
		rec := do(h, "GET", "/api/me", "", &http.Cookie{Name: CookieName, Value: c.Value})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"v@example.com"`) {
			t.Errorf("me: %d %s", rec.Code, rec.Body)
		}
	}

	rec = do(h, "POST", "/api/auth/logout", "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	if c := sessionCookie(t, rec); c.MaxAge != -1 || c.Value != "" {
		t.Errorf("logout cookie = %+v", c)
	}
}

func TestMeDeletedUser(t *testing.T) {
	h, s := newTestRouter(t)
	tok, _, err := s.Tokens.Issue(Identity{UserID: "00000000-0000-0000-0000-000000000999", Role: RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	rec := do(h, "GET", "/api/me", "", &http.Cookie{Name: CookieName, Value: tok})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("me for deleted user: %d", rec.Code)
	}
}
```

- [ ] **Step 3: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/auth/`
Expected: `FAIL` build: `undefined: Service`, `undefined: Handler`, `undefined: UserJSON`.

- [ ] **Step 4: Viết `internal/auth/service.go`**

```go
package auth

import (
	"context"
	"errors"
	"sync"
	"time"
)

// UserStore là phần của Repo mà Service cần.
type UserStore interface {
	Create(ctx context.Context, u User) error
	GetByEmail(ctx context.Context, email string) (User, error)
	GetByID(ctx context.Context, id string) (User, error)
	UpsertAdmin(ctx context.Context, u User) (User, bool, error)
}

type Service struct {
	Users  UserStore
	Tokens *Tokens
	NewID  func() string
}

// dummyHash dùng khi email không tồn tại, để Login tốn thời gian như khi sai mật khẩu
// (không lộ email nào đã đăng ký qua thời gian phản hồi).
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("streamfilm-dummy-password")
	return h
})

// Register tạo tài khoản viewer.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := User{ID: s.NewID(), Email: email, PasswordHash: hash, Role: RoleViewer}
	if err := s.Users.Create(ctx, u); err != nil {
		return User{}, err
	}
	return s.Users.GetByID(ctx, u.ID)
}

// Login kiểm tra email + mật khẩu. Sai email hay sai mật khẩu đều trả ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		CheckPassword(dummyHash(), password)
		return User{}, ErrInvalidCredentials
	}
	u, err := s.Users.GetByEmail(ctx, email)
	if errors.Is(err, ErrUserNotFound) {
		CheckPassword(dummyHash(), password)
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	if !CheckPassword(u.PasswordHash, password) {
		return User{}, ErrInvalidCredentials
	}
	return u, nil
}

// Session phát JWT cho user.
func (s *Service) Session(u User) (string, time.Time, error) {
	return s.Tokens.Issue(Identity{UserID: u.ID, Role: u.Role})
}

// SeedAdmin tạo admin hoặc đặt lại mật khẩu + nâng quyền nếu email đã có (make seed-admin).
func (s *Service) SeedAdmin(ctx context.Context, email, password string) (User, bool, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, false, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, false, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, false, err
	}
	return s.Users.UpsertAdmin(ctx, User{ID: s.NewID(), Email: email, PasswordHash: hash})
}
```

- [ ] **Step 5: Viết `internal/auth/handler.go`**

```go
package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"streamfilm/internal/httpx"
)

// Handler gắn các endpoint /api/auth/* và /api/me.
type Handler struct {
	Svc          *Service
	SecureCookie bool
	Log          *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/auth/register", h.register)
	r.Post("/api/auth/login", h.login)
	r.Post("/api/auth/logout", h.logout)
	r.With(RequireUser).Get("/api/me", h.me)
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UserJSON struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

func toJSON(u User) UserJSON {
	return UserJSON{ID: u.ID, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt}
}

// register tạo viewer và đăng nhập luôn (đặt cookie).
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := h.Svc.Register(r.Context(), in.Email, in.Password)
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		h.internal(w, r, err)
		return
	}
	h.startSession(w, r, u, http.StatusCreated)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	u, err := h.Svc.Login(r.Context(), in.Email, in.Password)
	if errors.Is(err, ErrInvalidCredentials) {
		httpx.WriteError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		h.internal(w, r, err)
		return
	}
	h.startSession(w, r, u, http.StatusOK)
}

func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, u User, status int) {
	tok, exp, err := h.Svc.Session(u)
	if err != nil {
		h.internal(w, r, err)
		return
	}
	SetSessionCookie(w, tok, exp, h.SecureCookie)
	httpx.WriteJSON(w, status, toJSON(u))
}

// logout chỉ xoá cookie. JWT không lưu phía server nên token đã lộ vẫn hợp lệ tới khi hết hạn (ADR 0010).
func (h *Handler) logout(w http.ResponseWriter, _ *http.Request) {
	ClearSessionCookie(w, h.SecureCookie)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	id, _ := FromContext(r.Context())
	u, err := h.Svc.Users.GetByID(r.Context(), id.UserID)
	if errors.Is(err, ErrUserNotFound) {
		ClearSessionCookie(w, h.SecureCookie)
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}
	if err != nil {
		h.internal(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toJSON(u))
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, err error) {
	h.Log.Error("auth request failed", "request_id", middleware.GetReqID(r.Context()),
		"path", r.URL.Path, "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}
```

- [ ] **Step 6: Chạy test**

Run: `go vet ./internal/auth/ && go test ./internal/auth/`
Expected: `ok  	streamfilm/internal/auth` (gồm `TestRepo` khi có Docker).

- [ ] **Step 7: Commit**

```bash
git add internal/auth/service.go internal/auth/service_test.go internal/auth/handler.go internal/auth/handler_test.go
git commit -m "feat(auth): register, login, logout and me endpoints"
```

---

### Task 7: `internal/signing` — ký URL theo công thức nginx

**Files:**
- Create: `internal/signing/signing.go`, `internal/signing/signing_test.go`

**Interfaces:**
- Consumes: không.
- Produces:
  ```go
  const TTL = 2 * time.Hour
  func ValidateSecret(secret string) error
  func Dir(videoID string) string                                   // "/hls/{id}/"
  func Signature(secret, dir string, exp int64) string              // base64url(MD5("{exp}{dir} {secret}")), 22 ký tự
  func SignedPath(secret, videoID, file string, exp int64) string   // "/cdn/s/{sig}/{exp}/hls/{id}/{file}"
  type Signer struct { /* unexported */ }
  func NewSigner(secret string, ttl time.Duration, now func() time.Time) (*Signer, error)
  func (s *Signer) Path(videoID, file string) (string, time.Time, error) // thoả catalog.PlaySigner
  ```

- [ ] **Step 1: Viết test thất bại** — `internal/signing/signing_test.go`. Vector `H23K0bGijtyB6JSJpoVT1g` được tính độc lập bằng `openssl` (lệnh trong comment):

```go
package signing

import (
	"strings"
	"testing"
	"time"
)

const (
	testSecret = "dev-cdn-secret-change-me"
	testVID    = "0b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"
	testExp    = int64(1794736800) // 2026-11-15T10:00:00Z
)

// Vector tính độc lập bằng openssl (cùng công thức với nginx secure_link_md5):
//
//	printf '%s/hls/%s/ %s' 1794736800 0b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f dev-cdn-secret-change-me \
//	  | openssl dgst -md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
const wantSig = "H23K0bGijtyB6JSJpoVT1g"

func TestSignatureMatchesOpenSSLVector(t *testing.T) {
	if got := Signature(testSecret, Dir(testVID), testExp); got != wantSig {
		t.Errorf("Signature = %q, want %q", got, wantSig)
	}
}

func TestSignedPath(t *testing.T) {
	got := SignedPath(testSecret, testVID, "master.m3u8", testExp)
	want := "/cdn/s/" + wantSig + "/1794736800/hls/" + testVID + "/master.m3u8"
	if got != want {
		t.Errorf("SignedPath = %q, want %q", got, want)
	}
	// Ký theo prefix thư mục: file khác trong cùng thư mục có cùng chữ ký.
	seg := SignedPath(testSecret, testVID, "720p/seg_000.ts", testExp)
	if !strings.HasPrefix(seg, "/cdn/s/"+wantSig+"/1794736800/hls/"+testVID+"/") {
		t.Errorf("segment path = %q", seg)
	}
	// Khác video, khác exp hoặc khác secret thì khác chữ ký.
	others := []string{
		Signature(testSecret, Dir("1b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"), testExp),
		Signature(testSecret, Dir(testVID), testExp+1),
		Signature(testSecret+"x", Dir(testVID), testExp),
	}
	for i, s := range others {
		if s == wantSig {
			t.Errorf("case %d: signature must differ", i)
		}
	}
	if len(wantSig) != 22 || strings.ContainsAny(wantSig, "+/=") {
		t.Errorf("signature phải là base64url 22 ký tự không padding: %q", wantSig)
	}
}

func TestSignerPath(t *testing.T) {
	now := time.Unix(testExp, 0).Add(-TTL)
	s, err := NewSigner(testSecret, TTL, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	p, exp, err := s.Path(testVID, "master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if p != SignedPath(testSecret, testVID, "master.m3u8", testExp) || exp.Unix() != testExp {
		t.Errorf("Path = %q, %v", p, exp)
	}
	for _, bad := range []string{"", "a/b", "../x", "a b"} {
		if _, _, err := s.Path(bad, "master.m3u8"); err == nil {
			t.Errorf("video id %q: want error", bad)
		}
	}
}

func TestValidateSecret(t *testing.T) {
	if _, err := NewSigner("short", TTL, time.Now); err == nil {
		t.Error("short secret: want error")
	}
	for _, bad := range []string{"has space in it 123", `has"quote-0123456789`, "dollar$sign-0123456789", "semi;colon-0123456789"} {
		if err := ValidateSecret(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	if err := ValidateSecret(testSecret); err != nil {
		t.Error(err)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/signing/`
Expected: `FAIL` build: `undefined: Signature`, `undefined: Dir`, …

- [ ] **Step 3: Kiểm tra vector bằng openssl (độc lập với Go)**

Run: `printf '%s/hls/%s/ %s' 1794736800 0b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f dev-cdn-secret-change-me | openssl dgst -md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='`
Expected: `H23K0bGijtyB6JSJpoVT1g`

- [ ] **Step 4: Viết `internal/signing/signing.go`**

```go
// Package signing ký URL phát HLS theo đúng công thức nginx secure_link_md5 trong
// deploy/nginx/templates/default.conf.template:
//
//	secure_link     $sl_sig,$sl_exp;
//	secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";
//
// sig = base64url không padding của MD5("{exp}/hls/{id}/ {secret}"). Ký theo prefix thư mục
// /hls/{id}/ nên một chữ ký dùng được cho master, playlist con và mọi segment (spec §4.3).
// MD5 vì nginx secure_link chỉ hỗ trợ MD5 (ADR 0009).
package signing

import (
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// TTL của URL phát (spec §4.3).
const TTL = 2 * time.Hour

// secretRe: chỉ ký tự an toàn khi chèn vào chuỗi trong cấu hình nginx (không có dấu cách, ", $, ;).
var secretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// videoIDRe khớp segment {id} mà location nginx nhận ([^/]+), thu hẹp cho an toàn.
var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidateSecret(secret string) error {
	if !secretRe.MatchString(secret) {
		return fmt.Errorf("CDN_SIGNING_SECRET must be 16-128 characters of [A-Za-z0-9_-]")
	}
	return nil
}

// Dir là prefix thư mục được ký: "/hls/{id}/".
func Dir(videoID string) string { return "/hls/" + videoID + "/" }

// Signature tính chữ ký cho prefix dir và thời điểm hết hạn exp (Unix giây).
func Signature(secret, dir string, exp int64) string {
	sum := md5.Sum([]byte(strconv.FormatInt(exp, 10) + dir + " " + secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SignedPath trả "/cdn/s/{sig}/{exp}/hls/{id}/{file}".
func SignedPath(secret, videoID, file string, exp int64) string {
	dir := Dir(videoID)
	return "/cdn/s/" + Signature(secret, dir, exp) + "/" + strconv.FormatInt(exp, 10) + dir + file
}

// Signer ký URL với TTL cố định.
type Signer struct {
	secret string
	ttl    time.Duration
	now    func() time.Time
}

func NewSigner(secret string, ttl time.Duration, now func() time.Time) (*Signer, error) {
	if err := ValidateSecret(secret); err != nil {
		return nil, err
	}
	return &Signer{secret: secret, ttl: ttl, now: now}, nil
}

// Path ký file trong thư mục HLS của video; trả path và thời điểm hết hạn.
func (s *Signer) Path(videoID, file string) (string, time.Time, error) {
	if !videoIDRe.MatchString(videoID) {
		return "", time.Time{}, fmt.Errorf("invalid video id %q", videoID)
	}
	exp := s.now().Add(s.ttl).Unix()
	return SignedPath(s.secret, videoID, file, exp), time.Unix(exp, 0).UTC(), nil
}
```

- [ ] **Step 5: Chạy test**

Run: `go test ./internal/signing/`
Expected: `ok  	streamfilm/internal/signing`

- [ ] **Step 6: Commit**

```bash
git add internal/signing
git commit -m "feat(signing): secure_link_md5 compatible signed HLS paths"
```

---

### Task 8: nginx `secure_link` (template) + test khớp chữ ký với nginx thật

**Files:**
- Delete: `deploy/nginx/default.conf`
- Create: `deploy/nginx/templates/default.conf.template`, `internal/signing/nginx_test.go`
- Modify: `deploy/docker-compose.yml` (service `nginx`), `web/dev/player.html`

**Interfaces:**
- Consumes: `signing.SignedPath`, `signing.Signature`, `signing.Dir`, `signing.TTL` (Task 7); env `CDN_SIGNING_SECRET` (Task 2).
- Produces: nginx location `~ ^/cdn/s/(?<sl_sig>[^/]+)/(?<sl_exp>[^/]+)/hls/(?<sl_vid>[^/]+)/(?<sl_file>.+)$` → 200 (proxy MinIO, cache) / 403; `/cdn/*` sai dạng → 403; `/hls/*` và mọi đường khác → 404; `/healthz`, `/dev/` giữ nguyên. Header `Access-Control-Allow-Origin: *` trên cả 200 và 403 (hls.js cần đọc mã 403 để gọi lại `/play`), `X-Cache-Status`.

- [ ] **Step 1: Viết test thất bại** — `internal/signing/nginx_test.go`. Test dựng một network Docker, một nginx thứ hai đóng vai MinIO (alias `minio`, cổng 9000, phục vụ file tĩnh `/srv/hls/...`), và nginx "CDN" chạy **đúng file template của repo** với `CDN_SIGNING_SECRET`:

```go
package signing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// nginxImage phải trùng image của service nginx trong deploy/docker-compose.yml.
const nginxImage = "nginx:1.27-alpine"

// originConf: nginx thứ hai đóng vai MinIO (alias "minio", cổng 9000), phục vụ file tĩnh
// dưới /srv/hls/... giống bucket hls cho đọc ẩn danh (ADR 0002).
const originConf = `server { listen 9000; root /srv; }`

// TestNginxSecureLinkParity chạy đúng template nginx của repo và kiểm tra nginx chấp nhận
// URL do SignedPath sinh ra, từ chối URL sửa chữ ký / hết hạn / khác video.
func TestNginxSecureLinkParity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	nw, err := network.New(ctx)
	testcontainers.CleanupNetwork(t, nw)
	if err != nil {
		t.Fatal(err)
	}

	origin, err := testcontainers.Run(ctx, nginxImage,
		network.WithNetwork([]string{"minio"}, nw),
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(originConf),
				ContainerFilePath: "/etc/nginx/conf.d/default.conf", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("#EXTM3U\n"),
				ContainerFilePath: "/srv/hls/" + testVID + "/master.m3u8", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("segment-bytes"),
				ContainerFilePath: "/srv/hls/" + testVID + "/720p/seg_000.ts", FileMode: 0o644},
		),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9000/tcp")),
	)
	testcontainers.CleanupContainer(t, origin)
	if err != nil {
		t.Fatal(err)
	}

	cdn, err := testcontainers.Run(ctx, nginxImage,
		network.WithNetwork([]string{"cdn"}, nw),
		testcontainers.WithExposedPorts("80/tcp"),
		testcontainers.WithEnv(map[string]string{
			"CDN_SIGNING_SECRET":    testSecret,
			"NGINX_ENVSUBST_FILTER": "^CDN_",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/templates/default.conf.template",
			ContainerFilePath: "/etc/nginx/templates/default.conf.template", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("80/tcp")),
	)
	testcontainers.CleanupContainer(t, cdn)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := cdn.PortEndpoint(ctx, "80/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}

	exp := time.Now().Add(TTL).Unix()
	past := time.Now().Add(-time.Minute).Unix()
	master := SignedPath(testSecret, testVID, "master.m3u8", exp)
	sig := Signature(testSecret, Dir(testVID), exp)
	flipped := "A"
	if sig[0] == 'A' {
		flipped = "B"
	}
	tampered := strings.Replace(master, sig, flipped+sig[1:], 1)
	otherVID := "1b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"

	cases := []struct {
		name, path string
		want       int
		body       string
	}{
		{"signed master", master, 200, "#EXTM3U\n"},
		{"signed segment (same prefix)", SignedPath(testSecret, testVID, "720p/seg_000.ts", exp), 200, "segment-bytes"},
		{"tampered signature", tampered, 403, ""},
		{"expired", SignedPath(testSecret, testVID, "master.m3u8", past), 403, ""},
		{"exp changed, old signature", fmt.Sprintf("/cdn/s/%s/%d/hls/%s/master.m3u8", sig, exp+3600, testVID), 403, ""},
		{"signature of another video", fmt.Sprintf("/cdn/s/%s/%d/hls/%s/master.m3u8", sig, exp, otherVID), 403, ""},
		{"wrong secret", SignedPath(testSecret+"x", testVID, "master.m3u8", exp), 403, ""},
		{"non-numeric exp", fmt.Sprintf("/cdn/s/%s/abc/hls/%s/master.m3u8", sig, testVID), 403, ""},
		{"malformed cdn path", "/cdn/s/" + sig, 403, ""},
		{"unsigned hls path", "/hls/" + testVID + "/master.m3u8", 404, ""},
	}
	for _, c := range cases {
		resp, err := http.Get(endpoint + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: GET %s = %d, want %d", c.name, c.path, resp.StatusCode, c.want)
			continue
		}
		if c.body != "" && string(body) != c.body {
			t.Errorf("%s: body = %q, want %q", c.name, body, c.body)
		}
		if strings.HasPrefix(c.path, "/cdn/") && resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: thiếu CORS header (player cần đọc được mã 403)", c.name)
		}
	}

	// Lần hai cùng file (kể cả với chữ ký khác) là cache HIT: cache key không chứa chữ ký.
	other := SignedPath(testSecret, testVID, "master.m3u8", exp+60)
	resp, err := http.Get(endpoint + other)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Cache-Status"); got != "HIT" {
		t.Errorf("X-Cache-Status = %q, want HIT", got)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -run TestNginxSecureLinkParity ./internal/signing/`
Expected: `FAIL` với `can't copy ../../deploy/nginx/templates/default.conf.template to container: open ...: no such file or directory`.

- [ ] **Step 3: Xoá config cũ, viết template** — `git rm deploy/nginx/default.conf`, tạo `deploy/nginx/templates/default.conf.template`:

```nginx
# "CDN" giả lập (M3): chỉ phục vụ HLS qua URL có chữ ký.
# Đây là template: lúc khởi động, image nginx chạy envsubst (chỉ với biến khớp NGINX_ENVSUBST_FILTER=^CDN_)
# rồi ghi ra /etc/nginx/conf.d/default.conf. Biến nginx ($sl_exp, $uri, ...) giữ nguyên.
proxy_cache_path /var/cache/nginx/hls levels=1:2 keys_zone=hls:10m max_size=1g inactive=7d use_temp_path=off;

server {
    listen 80;

    location = /healthz { return 200 "ok\n"; }

    # /cdn/s/{sig}/{exp}/hls/{id}/{file}
    # sig = base64url(MD5("{exp}/hls/{id}/ {secret}")) không padding; khớp internal/signing (ADR 0009).
    location ~ ^/cdn/s/(?<sl_sig>[^/]+)/(?<sl_exp>[^/]+)/hls/(?<sl_vid>[^/]+)/(?<sl_file>.+)$ {
        secure_link $sl_sig,$sl_exp;
        secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";

        add_header Access-Control-Allow-Origin * always;
        add_header X-Cache-Status $upstream_cache_status always;

        # $secure_link: "" = chữ ký sai (hoặc exp không phải số), "0" = hết hạn, "1" = hợp lệ.
        # Spec §4.3: sai hoặc hết hạn đều trả 403.
        if ($secure_link = "") { return 403; }
        if ($secure_link = "0") { return 403; }

        # Bỏ prefix chữ ký rồi proxy tới MinIO: /hls/{id}/{file} = bucket hls, key {id}/{file}.
        rewrite ^ /hls/$sl_vid/$sl_file break;
        proxy_pass http://minio:9000;
        proxy_cache hls;
        proxy_cache_valid 200 7d;
        # Không đưa chữ ký vào cache key: mọi người xem dùng chung một bản cache của segment.
        proxy_cache_key "$sl_vid/$sl_file";
    }

    # URL /cdn/ sai dạng (thiếu sig/exp/id): coi như chữ ký sai.
    location /cdn/ {
        add_header Access-Control-Allow-Origin * always;
        return 403;
    }

    # Trang test hls.js (M1): /dev/player.html?src=<URL ký từ /api/videos/{id}/play>.
    location /dev/ {
        root /usr/share/nginx/html;
    }

    # Không còn đường /hls/ không ký; mọi đường khác 404.
    location / { return 404; }
}
```

- [ ] **Step 4: Chạy test (cần Docker)**

Run: `go test -count=1 -run TestNginxSecureLinkParity -v ./internal/signing/ 2>&1 | grep -E '^(---|ok|FAIL)'`
Expected: `--- PASS: TestNginxSecureLinkParity` và `ok  	streamfilm/internal/signing`.

- [ ] **Step 5: Chứng minh test bắt được lệch công thức**

Tạm sửa template: đổi `"$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}"` thành `"$sl_exp/hls/$sl_vid/${CDN_SIGNING_SECRET}"` (bỏ dấu cách).
Run: `go test -count=1 -run TestNginxSecureLinkParity ./internal/signing/`
Expected: `FAIL` với `signed master: GET /cdn/s/... = 403, want 200`. Khôi phục: `git checkout deploy/nginx/templates/default.conf.template` (file đã `git add` ở Step 3 trước khi sửa) hoặc sửa lại tay, chạy lại Step 4 → PASS.

- [ ] **Step 6: Compose: nginx đọc template và secret**

Trong `deploy/docker-compose.yml`, thay khối đầu service `nginx`

```yaml
  nginx:
    image: nginx:1.27-alpine
    ports: ["127.0.0.1:8088:80"]
    volumes:
      - ./nginx/default.conf:/etc/nginx/conf.d/default.conf:ro
      - ../web/dev:/usr/share/nginx/html/dev:ro
```

bằng

```yaml
  nginx:
    image: nginx:1.27-alpine
    ports: ["127.0.0.1:8088:80"]
    environment:
      # Entrypoint của image chạy envsubst trên /etc/nginx/templates/*.template → /etc/nginx/conf.d/.
      CDN_SIGNING_SECRET: ${CDN_SIGNING_SECRET:?CDN_SIGNING_SECRET chưa đặt trong .env (chạy make env)}
      NGINX_ENVSUBST_FILTER: "^CDN_"
    volumes:
      - ./nginx/templates:/etc/nginx/templates:ro
      - ../web/dev:/usr/share/nginx/html/dev:ro
```

(`depends_on` và `healthcheck` giữ nguyên.)

- [ ] **Step 7: Dev player nhận URL ký** — ghi đè `web/dev/player.html`:

```html
<!doctype html>
<html lang="vi">
<head>
<meta charset="utf-8">
<title>Streamfilm dev player</title>
<script src="https://cdn.jsdelivr.net/npm/hls.js@1.5.17/dist/hls.min.js"></script>
<style>
  body { font-family: system-ui, sans-serif; max-width: 960px; margin: 24px auto; padding: 0 16px; }
  video { width: 100%; background: #000; }
  #levels button { margin: 4px; }
  #levels button.on { font-weight: bold; }
  pre { background: #f3f3f3; padding: 8px; max-height: 200px; overflow: auto; }
</style>
</head>
<body>
<h1>Streamfilm dev player</h1>
<p>Mở <code>/dev/player.html?src=&lt;URL ký&gt;</code>. Lấy URL ký từ <code>GET /api/videos/{id}/play</code>
  (trường <code>url</code>) hoặc <code>make sign-url ID={id}</code>. URL hết hạn sau 2 giờ: nginx trả 403.</p>
<video id="v" controls muted></video>
<div id="levels"></div>
<pre id="log"></pre>
<script>
  const src = new URLSearchParams(location.search).get('src');
  const video = document.getElementById('v');
  const logEl = document.getElementById('log');
  const log = (m) => { logEl.textContent += m + '\n'; logEl.scrollTop = logEl.scrollHeight; };

  if (!src) {
    log('thiếu ?src=<URL ký>');
  } else if (Hls.isSupported()) {
    const hls = new Hls();
    hls.loadSource(src);
    hls.attachMedia(video);
    hls.on(Hls.Events.MANIFEST_PARSED, (_, data) => {
      log(`manifest: ${data.levels.map(l => l.height + 'p').join(', ')}`);
      const box = document.getElementById('levels');
      const auto = document.createElement('button');
      auto.textContent = 'Auto';
      auto.onclick = () => { hls.currentLevel = -1; };
      box.appendChild(auto);
      data.levels.forEach((l, i) => {
        const b = document.createElement('button');
        b.textContent = l.height + 'p';
        b.onclick = () => { hls.currentLevel = i; };
        box.appendChild(b);
      });
    });
    hls.on(Hls.Events.LEVEL_SWITCHED, (_, d) => {
      const h = hls.levels[d.level].height;
      log(`switched to ${h}p`);
      document.querySelectorAll('#levels button').forEach(b => b.classList.toggle('on', b.textContent === h + 'p'));
    });
    hls.on(Hls.Events.FRAG_LOADED, (_, d) => log(`segment ${d.frag.sn} @ level ${d.frag.level}`));
    hls.on(Hls.Events.ERROR, (_, d) => log(`error: ${d.type} ${d.details} ${d.response ? 'HTTP ' + d.response.code : ''}`));
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    video.src = src; // Safari phát HLS gốc
  }
</script>
</body>
</html>
```

- [ ] **Step 8: Kiểm tra compose**

Run: `make env && docker compose -f deploy/docker-compose.yml --env-file .env config >/dev/null && echo ok`
Expected: `ok`. (Chạy thật cả stack ở Task 10.)

- [ ] **Step 9: Commit**

```bash
git add deploy/nginx deploy/docker-compose.yml web/dev/player.html internal/signing/nginx_test.go
git commit -m "feat(nginx): secure_link_md5 signed HLS location with parity test"
```

---

### Task 9: Catalog cho người xem và `/play`

**Files:**
- Modify: `internal/catalog/repo.go`, `internal/catalog/repo_test.go`
- Create: `internal/catalog/viewer.go`, `internal/catalog/viewer_test.go`

**Interfaces:**
- Consumes: `catalog.Repo`, `Video`, `Rendition`, `StatusReady`, `ErrNotFound` (M2); helper test `get`, `idA`, `idB`, `idC`, `idX`, `newTestRepo`, `newVideo` (M2).
- Produces:
  ```go
  func (r *Repo) ListReady(ctx context.Context, limit, offset int) ([]Video, error)
  type ViewerStore interface {
  	ListReady(ctx context.Context, limit, offset int) ([]Video, error)
  	Get(ctx context.Context, id string) (Video, error)
  	Renditions(ctx context.Context, id string) ([]Rendition, error)
  } // *Repo thoả
  type PlaySigner interface { Path(videoID, file string) (string, time.Time, error) } // *signing.Signer thoả
  type ViewerHandler struct { Videos ViewerStore; Signer PlaySigner; CDNBaseURL string; Log *slog.Logger }
  const PageSize = 20
  func (h *ViewerHandler) Routes(r chi.Router)
  // GET /api/videos?page=     → {"videos":[ViewerVideoJSON],"page":n,"has_more":bool} | 400
  // GET /api/videos/{id}      → ViewerVideoJSON (+renditions{height,bitrate_kbps}) | 404 nếu không có / chưa ready
  // GET /api/videos/{id}/play → PlayJSON, Cache-Control: no-store | 404
  type PlayJSON struct { URL string `json:"url"`; ExpiresAt time.Time `json:"expires_at"`; Subtitles []SubtitleJSON `json:"subtitles"`; ResumeAt int `json:"resume_at"` }
  ```

- [ ] **Step 1: Viết test thất bại cho repo** — thêm vào cuối `internal/catalog/repo_test.go`:

```go
func TestRepoListReady(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB, idC} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}
	// idA, idC ready; idB còn uploading.
	for _, id := range []string{idA, idC} {
		for _, to := range []Status{StatusQueued, StatusProcessing} {
			if _, err := r.Transition(ctx, id, to); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.MarkReady(ctx, id, ReadyInfo{DurationS: 30, HLSPrefix: id + "/"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := r.ListReady(ctx, 10, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListReady = %+v, err = %v", all, err)
	}
	for _, v := range all {
		if v.Status != StatusReady || v.ID == idB {
			t.Errorf("non-ready video in catalog: %+v", v)
		}
	}
	page2, err := r.ListReady(ctx, 1, 1)
	if err != nil || len(page2) != 1 || page2[0].ID != all[1].ID {
		t.Errorf("page 2 = %+v, err = %v", page2, err)
	}
}
```

- [ ] **Step 2: Viết test thất bại cho handler** — `internal/catalog/viewer_test.go`:

```go
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type fakeViewerStore struct {
	videos      []Video // thứ tự = thứ tự catalog
	limit, offs int
}

func (f *fakeViewerStore) ListReady(_ context.Context, limit, offset int) ([]Video, error) {
	f.limit, f.offs = limit, offset
	var ready []Video
	for _, v := range f.videos {
		if v.Status == StatusReady {
			ready = append(ready, v)
		}
	}
	if offset >= len(ready) {
		return nil, nil
	}
	return ready[offset:min(offset+limit, len(ready))], nil
}

func (f *fakeViewerStore) Get(_ context.Context, id string) (Video, error) {
	for _, v := range f.videos {
		if v.ID == id {
			return v, nil
		}
	}
	return Video{}, ErrNotFound
}

func (f *fakeViewerStore) Renditions(context.Context, string) ([]Rendition, error) {
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

type fakeSigner struct{ exp time.Time }

func (s fakeSigner) Path(id, file string) (string, time.Time, error) {
	return "/cdn/s/SIG/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}

func newViewerRouter(store ViewerStore) http.Handler {
	r := chi.NewRouter()
	(&ViewerHandler{Videos: store, Signer: fakeSigner{exp: time.Unix(1794736800, 0).UTC()},
		CDNBaseURL: "http://cdn.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func TestViewerList(t *testing.T) {
	store := &fakeViewerStore{}
	for i := range PageSize + 3 {
		store.videos = append(store.videos, Video{ID: fmt.Sprintf("v%02d", i), Title: "T", Status: StatusReady})
	}
	store.videos = append(store.videos, Video{ID: "hidden", Status: StatusProcessing})
	h := newViewerRouter(store)

	var body struct {
		Videos  []ViewerVideoJSON `json:"videos"`
		Page    int               `json:"page"`
		HasMore bool              `json:"has_more"`
	}
	rec := get(h, "/api/videos")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != PageSize || body.Page != 1 || !body.HasMore || store.limit != PageSize+1 || store.offs != 0 {
		t.Errorf("page 1: n=%d page=%d more=%v limit=%d off=%d", len(body.Videos), body.Page, body.HasMore, store.limit, store.offs)
	}
	if body.Videos[0].Genres == nil {
		t.Error("genres phải là [] chứ không phải null")
	}
	if strings.Contains(rec.Body.String(), `"status"`) || strings.Contains(rec.Body.String(), "raw_key") {
		t.Error("catalog người xem không được lộ status/raw_key")
	}

	rec = get(h, "/api/videos?page=2")
	body.Videos = nil
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != 3 || body.HasMore || store.offs != PageSize {
		t.Errorf("page 2: n=%d more=%v off=%d", len(body.Videos), body.HasMore, store.offs)
	}
	for _, q := range []string{"?page=0", "?page=abc", "?page=1001"} {
		if rec := get(h, "/api/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", q, rec.Code)
		}
	}
}

func TestViewerGetAndPlay(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusReady, DurationS: 30},
		{ID: idB, Title: "B", Status: StatusProcessing},
	}}
	h := newViewerRouter(store)

	rec := get(h, "/api/videos/"+idA)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"renditions":[{"height":720,"bitrate_kbps":2800}]`) {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}

	rec = get(h, "/api/videos/"+idA+"/play")
	if rec.Code != http.StatusOK {
		t.Fatalf("play: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("play phải có Cache-Control: no-store")
	}
	var p PlayJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.URL != "http://cdn.test/cdn/s/SIG/1794736800/hls/"+idA+"/master.m3u8" ||
		!p.ExpiresAt.Equal(time.Unix(1794736800, 0)) || p.ResumeAt != 0 || p.Subtitles == nil {
		t.Errorf("play = %+v", p)
	}
	if !strings.Contains(rec.Body.String(), `"subtitles":[]`) {
		t.Errorf("subtitles phải là []: %s", rec.Body)
	}

	for _, path := range []string{
		"/api/videos/" + idB, "/api/videos/" + idB + "/play", // chưa ready
		"/api/videos/" + idX + "/play", "/api/videos/not-a-uuid/play",
	} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404", path, rec.Code)
		}
	}
}
```

- [ ] **Step 3: Chạy test, xác nhận thất bại**

Run: `go test ./internal/catalog/`
Expected: `FAIL` build: `r.ListReady undefined`, `undefined: ViewerHandler`, `undefined: PageSize`.

- [ ] **Step 4: Thêm `ListReady` vào cuối `internal/catalog/repo.go`**

```go
// ListReady trả video ready (catalog cho người xem), mới nhất trước, phân trang bằng limit/offset.
// Tìm kiếm (q, genre) thêm ở M4.
func (r *Repo) ListReady(ctx context.Context, limit, offset int) ([]Video, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE status = 'ready' ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}
```

- [ ] **Step 5: Viết `internal/catalog/viewer.go`**

```go
package catalog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"streamfilm/internal/httpx"
)

// ViewerStore là phần của Repo mà endpoint người xem cần.
type ViewerStore interface {
	ListReady(ctx context.Context, limit, offset int) ([]Video, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
}

// PlaySigner ký đường dẫn HLS (signing.Signer).
type PlaySigner interface {
	Path(videoID, file string) (string, time.Time, error)
}

// ViewerHandler: catalog và /play cho user đã đăng nhập (router bọc auth.RequireUser).
type ViewerHandler struct {
	Videos     ViewerStore
	Signer     PlaySigner
	CDNBaseURL string // ví dụ http://localhost:8088; URL phát = CDNBaseURL + path đã ký
	Log        *slog.Logger
}

// PageSize: số video mỗi trang của GET /api/videos.
const PageSize = 20

func (h *ViewerHandler) Routes(r chi.Router) {
	r.Get("/api/videos", h.list)
	r.Get("/api/videos/{id}", h.get)
	r.Get("/api/videos/{id}/play", h.play)
}

type ViewerRenditionJSON struct {
	Height      int `json:"height"`
	BitrateKbps int `json:"bitrate_kbps"`
}

// ViewerVideoJSON không lộ thông tin nội bộ (status, raw_key, size_bytes, fail_reason).
type ViewerVideoJSON struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Genres      []string              `json:"genres"`
	Year        int                   `json:"year,omitempty"`
	DurationS   int                   `json:"duration_s"`
	CreatedAt   time.Time             `json:"created_at"`
	Renditions  []ViewerRenditionJSON `json:"renditions,omitempty"`
}

type SubtitleJSON struct {
	Lang  string `json:"lang"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// PlayJSON: subtitles luôn là [] và resume_at luôn 0 tới M5 (giữ nguyên hình dạng cho web M4).
type PlayJSON struct {
	URL       string         `json:"url"`
	ExpiresAt time.Time      `json:"expires_at"`
	Subtitles []SubtitleJSON `json:"subtitles"`
	ResumeAt  int            `json:"resume_at"`
}

func toViewerJSON(v Video) ViewerVideoJSON {
	genres := v.Genres
	if genres == nil {
		genres = []string{}
	}
	return ViewerVideoJSON{ID: v.ID, Title: v.Title, Description: v.Description, Genres: genres,
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}

func (h *ViewerHandler) list(w http.ResponseWriter, r *http.Request) {
	page := 1
	if s := r.URL.Query().Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			httpx.WriteError(w, http.StatusBadRequest, "page must be between 1 and 1000")
			return
		}
		page = n
	}
	// Lấy dư 1 bản ghi để biết còn trang sau.
	vs, err := h.Videos.ListReady(r.Context(), PageSize+1, (page-1)*PageSize)
	if err != nil {
		h.Log.Error("list ready videos", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	hasMore := len(vs) > PageSize
	if hasMore {
		vs = vs[:PageSize]
	}
	out := make([]ViewerVideoJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, toViewerJSON(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"videos": out, "page": page, "has_more": hasMore})
}

// readyVideo trả video nếu tồn tại và ready; ngược lại ghi 404 (người xem không thấy video chưa ready).
func (h *ViewerHandler) readyVideo(w http.ResponseWriter, r *http.Request) (Video, bool) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return Video{}, false
	}
	v, err := h.Videos.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && v.Status != StatusReady) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return Video{}, false
	}
	if err != nil {
		h.Log.Error("get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return Video{}, false
	}
	return v, true
}

func (h *ViewerHandler) get(w http.ResponseWriter, r *http.Request) {
	v, ok := h.readyVideo(w, r)
	if !ok {
		return
	}
	rs, err := h.Videos.Renditions(r.Context(), v.ID)
	if err != nil {
		h.Log.Error("get renditions", "video_id", v.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toViewerJSON(v)
	for _, rd := range rs {
		out.Renditions = append(out.Renditions, ViewerRenditionJSON{Height: rd.Height, BitrateKbps: rd.BitrateKbps})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *ViewerHandler) play(w http.ResponseWriter, r *http.Request) {
	v, ok := h.readyVideo(w, r)
	if !ok {
		return
	}
	path, exp, err := h.Signer.Path(v.ID, "master.m3u8")
	if err != nil {
		h.Log.Error("sign play url", "video_id", v.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, PlayJSON{URL: h.CDNBaseURL + path, ExpiresAt: exp,
		Subtitles: []SubtitleJSON{}, ResumeAt: 0})
}
```

- [ ] **Step 6: Chạy test**

Run: `go vet ./internal/catalog/ && go test ./internal/catalog/`
Expected: `ok  	streamfilm/internal/catalog`

- [ ] **Step 7: Commit**

```bash
git add internal/catalog
git commit -m "feat(catalog): viewer catalog, detail and signed play URL endpoints"
```

---

### Task 10: `cmd/api` — router phân quyền, `seed-admin`, `sign`; chạy trong compose

**Files:**
- Create: `cmd/api/router.go`, `cmd/api/router_test.go`, `cmd/api/commands.go`
- Modify: `cmd/api/main.go`, `cmd/transcoder/main.go`, `deploy/docker-compose.yml` (service `api`), `Makefile`

**Interfaces:**
- Consumes: `auth.NewTokens`, `auth.SessionTTL`, `auth.Handler`, `auth.Service`, `auth.NewRepo`, `auth.RequireUser`, `auth.RequireAdmin`, `Tokens.Authenticate` (Task 3–6); `signing.NewSigner`, `signing.TTL` (Task 7); `catalog.ViewerHandler` (Task 9); `upload.Handler`, `catalog.AdminHandler` (M2).
- Produces:
  ```go
  type routes struct { Tokens *auth.Tokens; Auth *auth.Handler; Viewer *catalog.ViewerHandler; Upload *upload.Handler; Admin *catalog.AdminHandler }
  func newRouter(log *slog.Logger, rt routes) http.Handler
  func seedAdmin(args []string) error // api seed-admin [-email] [-password]
  func signURL(args []string) error   // api sign -id ID [-file] [-ttl]
  ```
  Make targets: `seed-admin`, `sign-url ID=…`.

- [ ] **Step 1: Viết test thất bại** — `cmd/api/router_test.go`:

```go
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/upload"
)

type emptyStore struct{}

func (emptyStore) List(context.Context, catalog.ListFilter) ([]catalog.Video, error) { return nil, nil }
func (emptyStore) ListReady(context.Context, int, int) ([]catalog.Video, error)      { return nil, nil }
func (emptyStore) Get(context.Context, string) (catalog.Video, error) {
	return catalog.Video{}, catalog.ErrNotFound
}
func (emptyStore) Renditions(context.Context, string) ([]catalog.Rendition, error) { return nil, nil }

// TestRouterAuthz kiểm tra phân quyền theo nhóm route; handler phía sau dùng store rỗng.
func TestRouterAuthz(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokens, err := auth.NewTokens("router-test-secret-0123456789-abcdef", auth.SessionTTL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	h := newRouter(log, routes{
		Tokens: tokens,
		Auth:   &auth.Handler{Svc: &auth.Service{Tokens: tokens}, Log: log},
		Viewer: &catalog.ViewerHandler{Videos: emptyStore{}, Log: log},
		Upload: &upload.Handler{Log: log},
		Admin:  &catalog.AdminHandler{Videos: emptyStore{}, Log: log},
	})
	admin, _, _ := tokens.Issue(auth.Identity{UserID: "a", Role: auth.RoleAdmin})
	viewer, _, _ := tokens.Issue(auth.Identity{UserID: "v", Role: auth.RoleViewer})

	cases := []struct {
		method, path, cookie string
		want                 int
	}{
		{"GET", "/healthz", "", 200},
		{"GET", "/api/videos", "", 401},
		{"GET", "/api/videos", viewer, 200},
		{"GET", "/api/videos/11111111-1111-1111-1111-111111111111/play", "", 401},
		{"GET", "/api/videos/11111111-1111-1111-1111-111111111111/play", viewer, 404},
		{"GET", "/api/admin/videos", "", 401},
		{"GET", "/api/admin/videos", viewer, 403},
		{"POST", "/api/admin/videos", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/complete", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/retry", "", 401},
		{"GET", "/api/admin/videos", admin, 200},
		{"GET", "/api/admin/videos", "not-a-jwt", 401},
		{"GET", "/api/me", "", 401},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s (cookie=%v): %d, want %d", c.method, c.path, c.cookie != "", rec.Code, c.want)
		}
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./cmd/api/`
Expected: `FAIL` build: `undefined: newRouter`, `undefined: routes`.

- [ ] **Step 3: Viết `cmd/api/router.go`**

```go
package main

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/httpx"
	"streamfilm/internal/upload"
)

type routes struct {
	Tokens *auth.Tokens
	Auth   *auth.Handler
	Viewer *catalog.ViewerHandler
	Upload *upload.Handler
	Admin  *catalog.AdminHandler
}

// newRouter: public (/healthz, /api/auth/*), user (/api/me, /api/videos/*), admin (/api/admin/*).
func newRouter(log *slog.Logger, rt routes) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, httpx.RequestLogger(log), rt.Tokens.Authenticate)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	rt.Auth.Routes(r)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireUser)
		rt.Viewer.Routes(r)
	})
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAdmin)
		rt.Upload.Routes(r)
		rt.Admin.Routes(r)
	})
	return r
}
```

- [ ] **Step 4: Viết `cmd/api/commands.go`**

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"streamfilm/internal/auth"
	"streamfilm/internal/config"
	"streamfilm/internal/db"
	"streamfilm/internal/signing"
	"streamfilm/migrations"
)

// seedAdmin: `api seed-admin` — tạo admin đầu tiên (make seed-admin). Chạy lại thì đặt lại mật khẩu.
func seedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ContinueOnError)
	email := fs.String("email", os.Getenv("ADMIN_EMAIL"), "email admin (mặc định $ADMIN_EMAIL)")
	password := fs.String("password", os.Getenv("ADMIN_PASSWORD"), "mật khẩu, 8-72 byte (mặc định $ADMIN_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		return err
	}
	svc := &auth.Service{Users: auth.NewRepo(pool), NewID: uuid.NewString}
	u, created, err := svc.SeedAdmin(ctx, *email, *password)
	if err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if created {
		fmt.Printf("admin created: %s (%s)\n", u.Email, u.ID)
	} else {
		fmt.Printf("admin updated: %s (%s)\n", u.Email, u.ID)
	}
	return nil
}

// signURL: `api sign -id ID` — in URL CDN đã ký (dùng với dev player hoặc video từ `transcoder file`).
func signURL(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	id := fs.String("id", "", "video id (thư mục hls/{id}/)")
	file := fs.String("file", "master.m3u8", "file trong thư mục video")
	ttl := fs.Duration("ttl", signing.TTL, "thời hạn URL (âm để tạo URL đã hết hạn khi thử)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	s, err := signing.NewSigner(cfg.CDNSigningSecret, *ttl, time.Now)
	if err != nil {
		return err
	}
	path, exp, err := s.Path(*id, *file)
	if err != nil {
		return err
	}
	fmt.Println(cfg.CDNBaseURL + path)
	fmt.Fprintf(os.Stderr, "expires %s\n", exp.Format(time.RFC3339))
	return nil
}
```

- [ ] **Step 5: Viết lại `cmd/api/main.go`**

```go
// Command api là HTTP API của Streamfilm.
//
//	api                                   chạy HTTP server (mặc định)
//	api seed-admin [-email E] [-password P] tạo/đặt lại tài khoản admin (mặc định đọc ADMIN_EMAIL, ADMIN_PASSWORD)
//	api sign -id ID [-file F] [-ttl 2h]     in URL CDN đã ký cho một video (debug, dev player)
//
// M3: đăng ký/đăng nhập (JWT cookie), /api/admin/* chỉ cho role admin, catalog và /play cho user.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/config"
	"streamfilm/internal/db"
	"streamfilm/internal/events"
	"streamfilm/internal/signing"
	"streamfilm/internal/storage"
	"streamfilm/internal/upload"
	"streamfilm/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	slog.SetDefault(log)
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "seed-admin":
		err = seedAdmin(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "sign":
		err = signURL(os.Args[2:])
	default:
		err = run(log)
	}
	if err != nil {
		log.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	// Kiểm tra secret trước khi kết nối gì: thiếu thì dừng ngay với thông báo rõ.
	tokens, err := auth.NewTokens(cfg.JWTSecret, auth.SessionTTL, time.Now)
	if err != nil {
		return err
	}
	signer, err := signing.NewSigner(cfg.CDNSigningSecret, signing.TTL, time.Now)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "versions", applied)

	store := storage.New(storage.Config{
		Endpoint: cfg.S3Endpoint, PublicEndpoint: cfg.S3PublicEndpoint, Region: cfg.S3Region,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
	})
	producer, err := events.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	consumer, err := events.NewConsumer(cfg.KafkaBrokers, "api", log,
		events.TopicVideoProcessing, events.TopicVideoTranscoded, events.TopicVideoFailed)
	if err != nil {
		return err
	}
	defer consumer.Close()

	repo := catalog.NewRepo(pool)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw,
		Now: time.Now, NewID: uuid.NewString, Log: log}

	authSvc := &auth.Service{Users: auth.NewRepo(pool), Tokens: tokens, NewID: uuid.NewString}
	handler := newRouter(log, routes{
		Tokens: tokens,
		Auth:   &auth.Handler{Svc: authSvc, SecureCookie: cfg.CookieSecure, Log: log},
		Viewer: &catalog.ViewerHandler{Videos: repo, Signer: signer, CDNBaseURL: cfg.CDNBaseURL, Log: log},
		Upload: &upload.Handler{Svc: svc, Log: log},
		Admin:  &catalog.AdminHandler{Videos: repo, Log: log},
	})

	updater := &catalog.StatusUpdater{Store: repo, Log: log}
	cleaner := &upload.Cleaner{Videos: repo, Objects: store, BucketRaw: cfg.BucketRaw,
		MaxAge: cfg.StaleUploadAfter, Now: time.Now, Log: log}

	var (
		wg          sync.WaitGroup
		consumerMu  sync.Mutex
		consumerErr error
	)
	wg.Go(func() {
		if err := consumer.Run(ctx, updater.Handle); err != nil {
			log.Error("status consumer stopped", "err", err)
			consumerMu.Lock()
			consumerErr = err
			consumerMu.Unlock()
			stop()
		}
	})
	wg.Go(func() { cleaner.Run(ctx, cfg.CleanupInterval) })

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		stop() // lần Ctrl+C thứ hai dùng xử lý mặc định (thoát ngay)
		log.Info("shutting down")
	case serveErr = <-errc:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		serveErr = err
	}
	wg.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		return serveErr
	}
	consumerMu.Lock()
	defer consumerMu.Unlock()
	if consumerErr != nil {
		return fmt.Errorf("status consumer: %w", consumerErr)
	}
	return nil
}
```

- [ ] **Step 6: Chế độ `transcoder file` không in URL không ký nữa**

Trong `cmd/transcoder/main.go`, thay

```go
	url := fmt.Sprintf("%s/hls/%s/master.m3u8", cfg.CDNBaseURL, id)
	slog.Info("upload done", "video_id", id, "files", n, "url", url)
	fmt.Println(url)
	return nil
```

bằng

```go
	// M3: nginx chỉ phục vụ URL có chữ ký; lấy URL bằng `make sign-url ID=...`.
	slog.Info("upload done", "video_id", id, "files", n, "prefix", cfg.BucketHLS+"/"+id+"/")
	fmt.Printf("uploaded %s/%s/ - signed URL: make sign-url ID=%s\n", cfg.BucketHLS, id, id)
	return nil
```

- [ ] **Step 7: Chạy test và build**

Run: `gofmt -l . ; go vet ./... && go test -short ./...`
Expected: `gofmt` không in gì; mọi package `ok` (hoặc `[no test files]`).

- [ ] **Step 8: Compose: api nhận secret và CDN_BASE_URL**

Trong `deploy/docker-compose.yml`, service `api`, thay

```yaml
      STALE_UPLOAD_AFTER: 24h
      CLEANUP_INTERVAL: 1h
    ports: ["127.0.0.1:8000:8000"]
```

bằng

```yaml
      STALE_UPLOAD_AFTER: 24h
      CLEANUP_INTERVAL: 1h
      CDN_BASE_URL: http://localhost:8088
      JWT_SECRET: ${JWT_SECRET:?JWT_SECRET chưa đặt trong .env (chạy make env)}
      CDN_SIGNING_SECRET: ${CDN_SIGNING_SECRET:?CDN_SIGNING_SECRET chưa đặt trong .env (chạy make env)}
      COOKIE_SECURE: ${COOKIE_SECURE:-false}
    ports: ["127.0.0.1:8000:8000"]
```

- [ ] **Step 9: Makefile: `seed-admin`, `sign-url`**

Thêm `seed-admin sign-url` vào dòng `.PHONY`, và thêm trước target `e2e-m2`:

```make
# Tạo hoặc đặt lại admin (ADMIN_EMAIL / ADMIN_PASSWORD trong .env) trong container api đang chạy.
seed-admin: env
	set -a; . ./.env; set +a; $(COMPOSE) exec -T -e ADMIN_EMAIL -e ADMIN_PASSWORD api api seed-admin

# In URL CDN đã ký cho một video: make sign-url ID=<video_id>
sign-url: env
	@$(COMPOSE) exec -T api api sign -id $(ID)
```

- [ ] **Step 10: Chạy stack và thử tay**

Run:
```bash
make up && make seed-admin && make seed-admin
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8000/api/admin/videos
curl -s -c /tmp/sf.jar -H 'Content-Type: application/json' \
  -d '{"email":"admin@streamfilm.local","password":"admin-dev-password"}' http://localhost:8000/api/auth/login
curl -s -b /tmp/sf.jar http://localhost:8000/api/admin/videos | head -c 80; echo
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8088/hls/x/master.m3u8
```
Expected: `admin created: admin@streamfilm.local (…)`, rồi `admin updated: …`; `401`; JSON `{"id":"…","email":"admin@streamfilm.local","role":"admin",…}`; `{"videos":[…`; `404`.

Nếu đã có video `ready` từ M2: `make sign-url ID=<id>` in `http://localhost:8088/cdn/s/…/master.m3u8` (dòng `expires …` ra stderr); `curl -s -o /dev/null -w '%{http_code}\n' "<url>"` → `200`.

- [ ] **Step 11: Commit**

```bash
git add cmd/api cmd/transcoder/main.go deploy/docker-compose.yml Makefile
git commit -m "feat(api): role-based routing, seed-admin and sign commands"
```

---

### Task 11: E2E M3

**Files:**
- Modify: `scripts/e2e-m2.sh`, `Makefile`
- Create: `scripts/e2e-m3.sh`

**Interfaces:**
- Consumes: toàn bộ endpoint M3, `make seed-admin`, nginx signed location.
- Produces: `make e2e-m2` (đăng nhập admin, kiểm tra HLS qua URL ký; ghi id vào `$E2E_ID_FILE` nếu đặt), `make e2e-m3` (in `OK  M3`).

- [ ] **Step 1: Cập nhật `scripts/e2e-m2.sh`** (admin route giờ cần cookie; `/hls/` không ký đã bị chặn):

```bash
#!/usr/bin/env bash
# E2E M2 (cập nhật M3): đăng nhập admin → tạo video → upload multipart qua presigned URL → complete
# → đợi ready → kiểm tra HLS qua nginx bằng URL ký từ /play.
# Dùng: scripts/e2e-m2.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, split.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m2 nạp từ .env); E2E_ID_FILE: nếu đặt, ghi video id vào file đó.
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
API="${API:-http://localhost:8000}"
TIMEOUT="${TIMEOUT:-300}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m2)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m2)}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
[ -f "$FILE" ] || die "không thấy $FILE (chạy: make sample)"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/admin.jar"
# api: gọi API với cookie admin, lỗi HTTP thì dừng.
api() { curl -fsS --max-time 30 -b "$JAR" "$@"; }

SIZE=$(wc -c < "$FILE" | tr -d ' ')
NAME=$(basename "$FILE")

echo "0/5 đăng nhập admin ($ADMIN_EMAIL)"
jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$JAR" -X POST "$API/api/auth/login" -H 'Content-Type: application/json' -d @- |
  jq -e '.role == "admin"' >/dev/null || die "đăng nhập admin thất bại (chạy: make seed-admin)"

echo "1/5 tạo video ($NAME, $SIZE bytes)"
BODY=$(jq -n --arg t "E2E $(date +%H:%M:%S)" --arg f "$NAME" --argjson s "$SIZE" \
  '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}')
CREATE=$(api -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d "$BODY")
ID=$(echo "$CREATE" | jq -r .id)
PART_SIZE=$(echo "$CREATE" | jq -r .upload.part_size)
PARTS=$(echo "$CREATE" | jq -r '.upload.parts | length')
echo "    id=$ID parts=$PARTS part_size=$PART_SIZE"

echo "2/5 upload $PARTS part qua presigned URL"
split -b "$PART_SIZE" -a 4 "$FILE" "$WORK/part."
i=0
for part in "$WORK"/part.*; do
  i=$((i + 1))
  URL=$(echo "$CREATE" | jq -r --argjson n "$i" '.upload.parts[] | select(.part_number == $n) | .url')
  code=$(curl -sS --max-time 300 -o /dev/null -w '%{http_code}' -X PUT --upload-file "$part" "$URL")
  [ "$code" = 200 ] || die "upload part $i: HTTP $code"
done
[ "$i" = "$PARTS" ] || die "đã upload $i part, API cần $PARTS"

echo "3/5 complete"
api -X POST "$API/api/admin/videos/$ID/complete" | jq -c .

echo "4/5 đợi status=ready (tối đa ${TIMEOUT}s)"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  V=$(api "$API/api/admin/videos/$ID")
  STATUS=$(echo "$V" | jq -r .status)
  echo "    status=$STATUS"
  case "$STATUS" in
    ready) break ;;
    failed) die "transcode lỗi: $(echo "$V" | jq -r .fail_reason)" ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || die "hết thời gian chờ"
  sleep 3
done
echo "$V" | jq -c '{duration_s, renditions: [.renditions[].height]}'

echo "5/5 kiểm tra HLS qua nginx bằng URL ký"
MASTER=$(api "$API/api/videos/$ID/play" | jq -r .url)
BASE="${MASTER%master.m3u8}" # .../cdn/s/{sig}/{exp}/hls/{id}/
curl -fsS --max-time 30 "$MASTER" | grep -q '#EXTM3U' || die "master.m3u8 không hợp lệ"
for key in $(echo "$V" | jq -r '.renditions[].playlist_key'); do
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$BASE${key#"$ID"/}")
  [ "$code" = 200 ] || die "$key: HTTP $code"
done
PLAYLIST=$(echo "$V" | jq -r '.renditions[0].playlist_key')
REL="${PLAYLIST#"$ID"/}" # 1080p/index.m3u8
SEG=$(curl -fsS --max-time 30 "$BASE$REL" | grep -m1 '\.ts$')
code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$BASE$(dirname "$REL")/$SEG")
[ "$code" = 200 ] || die "segment $SEG: HTTP $code"

[ -z "${E2E_ID_FILE:-}" ] || echo "$ID" > "$E2E_ID_FILE"
PLAYER="${MASTER%%/cdn/*}/dev/player.html?src=$(jq -rn --arg u "$MASTER" '$u | @uri')"
echo "OK  $PLAYER"
```

- [ ] **Step 2: Viết `scripts/e2e-m3.sh`** rồi `chmod +x scripts/e2e-m3.sh`:

```bash
#!/usr/bin/env bash
# E2E M3: đăng ký/đăng nhập, phân quyền, /play trả URL ký, nginx chặn URL sai hoặc hết hạn.
# Dùng: scripts/e2e-m3.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, openssl, split.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD, CDN_SIGNING_SECRET (make e2e-m3 nạp từ .env).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
API="${API:-http://localhost:8000}"
CDN="${CDN:-http://localhost:8088}"
: "${CDN_SIGNING_SECRET:?cần CDN_SIGNING_SECRET (chạy qua make e2e-m3)}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq openssl split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/viewer.jar"
EMAIL="viewer-$(date +%s)-$RANDOM@example.com"
PASSWORD="viewer-password-1"
NOID="00000000-0000-0000-0000-000000000000"

# expect CODE curl-args...: gọi curl và so mã HTTP.
expect() {
  local want=$1; shift
  local code
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "curl $*: HTTP $code, want $want"
}
creds() { jq -n --arg e "$1" --arg p "$2" '{email: $e, password: $p}'; }
# sign EXP ID: cùng công thức với nginx secure_link_md5 và internal/signing (tính độc lập bằng openssl).
sign() {
  printf '%s/hls/%s/ %s' "$1" "$2" "$CDN_SIGNING_SECRET" |
    openssl dgst -md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
}

echo "1/6 chưa đăng nhập → 401"
expect 401 "$API/api/me"
expect 401 "$API/api/videos"
expect 401 "$API/api/videos/$NOID/play"
expect 401 "$API/api/admin/videos"

echo "2/6 đăng ký viewer ($EMAIL), đăng nhập"
creds "$EMAIL" "$PASSWORD" | curl -fsS --max-time 30 -c "$JAR" -X POST "$API/api/auth/register" \
  -H 'Content-Type: application/json' -d @- | jq -e '.role == "viewer"' >/dev/null || die "register"
expect 409 -X POST "$API/api/auth/register" -H 'Content-Type: application/json' -d "$(creds "$EMAIL" "$PASSWORD")"
expect 401 -X POST "$API/api/auth/login" -H 'Content-Type: application/json' -d "$(creds "$EMAIL" "wrong-password")"
creds "$EMAIL" "$PASSWORD" | curl -fsS --max-time 30 -c "$JAR" -X POST "$API/api/auth/login" \
  -H 'Content-Type: application/json' -d @- >/dev/null || die "login"
curl -fsS --max-time 30 -b "$JAR" "$API/api/me" | jq -e --arg e "$EMAIL" '.email == $e and .role == "viewer"' >/dev/null ||
  die "/api/me"

echo "3/6 viewer gọi admin → 403"
expect 403 -b "$JAR" "$API/api/admin/videos"
expect 403 -b "$JAR" -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d '{}'
expect 403 -b "$JAR" -X POST "$API/api/admin/videos/$NOID/complete"

echo "4/6 admin upload + transcode (scripts/e2e-m2.sh)"
E2E_ID_FILE="$WORK/id" "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")

echo "5/6 viewer: catalog, chi tiết, /play, tải master + segment qua URL ký"
curl -fsS --max-time 30 -b "$JAR" "$API/api/videos" | jq -e --arg id "$ID" 'any(.videos[]; .id == $id)' >/dev/null ||
  die "video $ID không có trong /api/videos"
curl -fsS --max-time 30 -b "$JAR" "$API/api/videos/$ID" | jq -e '.renditions | length > 0' >/dev/null || die "/api/videos/$ID"
PLAY=$(curl -fsS --max-time 30 -b "$JAR" "$API/api/videos/$ID/play")
echo "$PLAY" | jq -e '.resume_at == 0 and .subtitles == []' >/dev/null || die "play body: $PLAY"
URL=$(echo "$PLAY" | jq -r .url)
# URL = $CDN/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8
IFS=/ read -r SIG EXP _ <<< "${URL#"$CDN"/cdn/s/}"
NOW=$(date +%s)
[ $((EXP - NOW)) -gt 7000 ] && [ $((EXP - NOW)) -le 7200 ] || die "exp phải ≈ now + 2h, còn $((EXP - NOW))s"
[ "$SIG" = "$(sign "$EXP" "$ID")" ] || die "chữ ký api khác công thức openssl/nginx"
BASE="${URL%master.m3u8}"
MASTER=$(curl -fsS --max-time 30 "$URL") || die "master.m3u8 qua URL ký"
VARIANT=$(echo "$MASTER" | grep -m1 '\.m3u8$')
SEG=$(curl -fsS --max-time 30 "$BASE$VARIANT" | grep -m1 '\.ts$')
expect 200 "$BASE$(dirname "$VARIANT")/$SEG"

echo "6/6 URL sai hoặc hết hạn → 403, không ký → không xem được"
if [ "${SIG:0:1}" = A ]; then R=B; else R=A; fi
expect 403 "$CDN/cdn/s/$R${SIG:1}/$EXP/hls/$ID/master.m3u8"         # sửa chữ ký
expect 403 "$CDN/cdn/s/$SIG/$((EXP + 3600))/hls/$ID/master.m3u8"     # kéo dài exp, giữ chữ ký
PAST=$((NOW - 60))
expect 403 "$CDN/cdn/s/$(sign "$PAST" "$ID")/$PAST/hls/$ID/master.m3u8" # ký đúng nhưng exp đã qua
expect 403 "$CDN/cdn/s/$SIG/$EXP/hls/$NOID/master.m3u8"              # chữ ký của video khác
expect 404 "$CDN/hls/$ID/master.m3u8"                                 # đường /hls/ cũ không còn

echo "    logout → cookie bị xoá → /api/me 401"
expect 204 -b "$JAR" -c "$JAR" -X POST "$API/api/auth/logout"
expect 401 -b "$JAR" "$API/api/me"

echo "OK  M3"
```

- [ ] **Step 3: Makefile**

Thêm `e2e-m3` vào `.PHONY`, thay target `e2e-m2` cũ

```make
# E2E M2: create → upload presigned → complete → ready → HLS qua nginx.
e2e-m2:
	@test -f tmp/sample.mp4 || $(MAKE) sample
	./scripts/e2e-m2.sh tmp/sample.mp4
```

bằng

```make
# E2E M2: đăng nhập admin → create → upload presigned → complete → ready → HLS qua URL ký.
e2e-m2: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; ./scripts/e2e-m2.sh tmp/sample.mp4

# E2E M3: auth, phân quyền, /play, nginx chặn URL sai hoặc hết hạn (chạy kèm luồng M2).
e2e-m3: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; ./scripts/e2e-m3.sh tmp/sample.mp4
```

- [ ] **Step 4: Lint script**

Run: `bash -n scripts/e2e-m2.sh scripts/e2e-m3.sh && shellcheck scripts/e2e-m2.sh scripts/e2e-m3.sh && echo ok` (bỏ `shellcheck` nếu máy không có)
Expected: `ok`

- [ ] **Step 5: Chạy E2E**

Run: `make up && make e2e-m3`
Expected (rút gọn):
```
1/6 chưa đăng nhập → 401
2/6 đăng ký viewer (viewer-…@example.com), đăng nhập
3/6 viewer gọi admin → 403
4/6 admin upload + transcode (scripts/e2e-m2.sh)
    0/5 đăng nhập admin (admin@streamfilm.local)
    …
    5/5 kiểm tra HLS qua nginx bằng URL ký
    OK  http://localhost:8088/dev/player.html?src=http%3A%2F%2Flocalhost%3A8088%2Fcdn%2Fs%2F…
5/6 viewer: catalog, chi tiết, /play, tải master + segment qua URL ký
6/6 URL sai hoặc hết hạn → 403, không ký → không xem được
    logout → cookie bị xoá → /api/me 401
OK  M3
```
Mở link dev player in ra ở bước 4 trong Chrome: log hiện `manifest: 360p, 480p, 720p, 1080p` và các dòng `segment N @ level L`.

- [ ] **Step 6: Commit**

```bash
git add scripts/e2e-m2.sh scripts/e2e-m3.sh Makefile
git commit -m "test(e2e): M3 auth and signed URL end-to-end script"
```

---

### Task 12: Tài liệu M3 và ADR

**Files:**
- Modify: `README.md`, `docs/architecture.md`, `docs/flows.md`, `docs/data-model.md`, `docs/api.md`, `docs/operations.md`, `docs/decisions/0002-dev-hls-bucket-read.md`
- Create: `docs/decisions/0009-signed-url-secure-link-md5.md`, `docs/decisions/0010-jwt-cookie-session.md`

**Interfaces:**
- Consumes: hành vi đã chạy ở Task 1–11.
- Produces: tài liệu khớp code (spec §11).

- [ ] **Step 1: `README.md`**

Thay dòng `Trạng thái: **M2** — …` bằng:

```markdown
Trạng thái: **M3** — upload và transcode qua Kafka (M2); đăng ký/đăng nhập bằng cookie JWT, phân quyền `admin`/`viewer`; `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn. Chưa có web (M4). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).
```

Thay khối lệnh trong `## Chạy nhanh` bằng:

````markdown
```bash
make up          # dựng hạ tầng + build và chạy api (cổng 8000) và transcoder
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make e2e-m3      # auth + upload → transcode → /play → kiểm tra URL ký qua nginx (gồm cả e2e-m2)
```
````

Trong `## Yêu cầu`, đổi dòng `curl`, `jq` thành: `` - `curl`, `jq`, `openssl` — cho script e2e ``.

- [ ] **Step 2: `docs/architecture.md`**

(a) Trong sơ đồ mermaid, thay hai dòng

```
  Viewer[Web + hls.js] -->|segments| N[nginx CDN]
  N --> S3
```

bằng

```
  Viewer[Web + hls.js] -->|login, /api/videos/:id/play| API
  Viewer -->|/cdn/s/sig/exp/hls/id/… segments| N[nginx CDN]
  N -->|secure_link_md5 hợp lệ| S3
```

(b) Thay đoạn `Phần đã chạy ở M2: … analytics (M6).` bằng:

```markdown
Phần đã chạy tới M3: `api` (đăng ký/đăng nhập, catalog và `/play` cho người xem, upload và danh sách video cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker`, Postgres, Kafka, MinIO, nginx kiểm tra signed URL. Chưa có: web (M4), xem tiếp và phụ đề (M5), analytics (M6).
```

(c) Thay tiêu đề cột `| Module | Trách nhiệm ở M2 |` bằng `| Module | Trách nhiệm (tới M3) |`; thay dòng `internal/catalog` bằng dòng dưới và thêm hai dòng mới ngay sau nó:

```markdown
| `internal/catalog` | Repository `videos` / `renditions`, máy trạng thái, `GET /api/admin/videos[/{id}]`, `GET /api/videos[/{id}]` và `/play` cho người xem (chỉ video `ready`), consumer `video.processing` / `video.transcoded` / `video.failed` (group `api`) |
| `internal/auth` | Bảng `users`, bcrypt, JWT HS256 trong cookie `sf_session`, `/api/auth/register|login|logout`, `/api/me`, middleware `Authenticate` / `RequireUser` / `RequireAdmin`, lệnh `api seed-admin` |
| `internal/signing` | Ký đường dẫn `/cdn/s/{sig}/{exp}/hls/{id}/…` đúng công thức `secure_link_md5` của nginx |
```

(d) Thay câu ``Endpoint admin **chưa có auth** ở M2; M3 bọc nhóm `/api/admin` bằng middleware yêu cầu role `admin`.`` (cuối đoạn `Nếu consumer trạng thái…`) bằng:

```markdown
Router chia ba nhóm: public (`/healthz`, `/api/auth/*`), user (`/api/me`, `/api/videos/*`; `RequireUser`, thiếu đăng nhập → `401`) và admin (`/api/admin/*`; `RequireAdmin`, thiếu đăng nhập → `401`, viewer → `403`). Middleware `Authenticate` chạy cho mọi request: đọc cookie `sf_session`, kiểm tra JWT và gắn user id + role vào context, không truy vấn DB ([ADR 0010](decisions/0010-jwt-cookie-session.md)).
```

(e) Thêm mục mới ngay trước `## Bố cục storage`:

```markdown
## CDN và signed URL

`/api/videos/{id}/play` trả `CDN_BASE_URL + /cdn/s/{sig}/{exp}/hls/{id}/master.m3u8`, với `exp` = now + 2 giờ và `sig` = base64url (không padding) của `MD5("{exp}/hls/{id}/ {CDN_SIGNING_SECRET}")`. Chữ ký phủ cả thư mục `/hls/{id}/`, nên playlist con và segment (đường dẫn tương đối trong playlist) dùng chung một chữ ký.

nginx (`deploy/nginx/templates/default.conf.template`) bắt `sig`, `exp`, `id`, `file` từ path, tính lại chữ ký bằng `secure_link_md5`, sai hoặc hết hạn trả `403`, đúng thì bỏ prefix chữ ký và proxy `/hls/{id}/{file}` tới MinIO kèm `proxy_cache` (cache key `{id}/{file}`, không chứa chữ ký nên mọi người xem dùng chung cache). Secret dùng chung giữa api và nginx qua biến `CDN_SIGNING_SECRET`; image nginx chèn biến vào template bằng envsubst khi khởi động. Test `internal/signing/nginx_test.go` chạy đúng template này trong container để chứng minh hai bên khớp. Vì sao MD5: [ADR 0009](decisions/0009-signed-url-secure-link-md5.md).
```

(f) Trong bảng đối chiếu Hygraph, thay dòng ``| CDN | nginx `proxy_cache` (ký URL từ M3) |`` bằng hai dòng:

```markdown
| CDN + token auth | nginx `secure_link_md5` + `proxy_cache`, URL ký theo prefix `/hls/{id}/`, hạn 2 giờ |
| User/Auth service | module `auth` trong `api` (bcrypt, JWT trong cookie HttpOnly) |
```

- [ ] **Step 3: `docs/flows.md`**

Thay dòng 3 bằng `Chỉ gồm các luồng đã chạy (tới M3). Xem tiếp và analytics được thêm ở M5–M6.` và thêm vào cuối file:

````markdown
## Đăng nhập và phân quyền

```mermaid
sequenceDiagram
  participant B as Trình duyệt / e2e
  participant API as api
  participant PG as Postgres
  B->>API: POST /api/auth/login {email, password}
  API->>PG: SELECT users WHERE email
  API->>API: bcrypt.Compare (email không tồn tại: so với hash giả, cùng thời gian)
  API-->>B: 200 {id, email, role} + Set-Cookie sf_session=JWT (HttpOnly, SameSite=Lax, 24h)
  B->>API: GET /api/admin/videos (cookie)
  API->>API: Authenticate: kiểm tra JWT HS256 → {user_id, role}
  API-->>B: 200 nếu role admin · 403 nếu viewer · 401 nếu thiếu/sai cookie
```

Tài khoản admin đầu tiên: `make seed-admin` (chạy `api seed-admin` trong container, đọc `ADMIN_EMAIL` / `ADMIN_PASSWORD`; chạy lại thì đặt lại mật khẩu). `POST /api/auth/register` luôn tạo `viewer`.

## Xem phim với signed URL

```mermaid
sequenceDiagram
  autonumber
  participant P as Player (hls.js)
  participant API as api
  participant N as nginx CDN
  participant S3 as MinIO
  P->>API: GET /api/videos/{id}/play (cookie)
  API->>API: user đã đăng nhập? video ready? exp = now + 2h, sig = md5("{exp}/hls/{id}/ secret")
  API-->>P: {url: http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8, expires_at, subtitles: [], resume_at: 0}
  P->>N: GET …/master.m3u8
  N->>N: secure_link_md5 → hợp lệ
  N->>S3: GET /hls/{id}/master.m3u8 (cache)
  N-->>P: 200
  P->>N: GET /cdn/s/{sig}/{exp}/hls/{id}/720p/index.m3u8, …/720p/seg_000.ts (đường dẫn tương đối)
  N-->>P: 200 (X-Cache-Status: MISS lần đầu, HIT sau đó)
  Note over P,N: Sửa sig, đổi exp, dùng sig của video khác hoặc quá exp → nginx 403
  Note over P,API: Player gặp 403 thì gọi lại /play và phát tiếp (web M4)
```
````

- [ ] **Step 4: `docs/data-model.md`**

Thay `## Postgres (M2)` bằng `## Postgres (tới M3)`; trong câu ngay dưới, thay ``Bảng `users` (M3), cột `search` (M4)`` bằng ``Cột `search` (M4)``. Trong khối SQL, thêm trước `videos (`:

```sql
users (                    -- M3, migrations/0002_users.sql
  id uuid pk, email text unique check (email = lower(email)), password_hash text,  -- bcrypt cost 12
  role text check (role in ('admin','viewer')), created_at timestamptz
)
```

- [ ] **Step 5: `docs/api.md`**

Thay phần từ đầu file tới hết bảng endpoint (trước `## POST /api/admin/videos`) bằng:

```markdown
# API

Base URL local: `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.

Xác thực bằng cookie `sf_session` (JWT HS256, `HttpOnly`, `SameSite=Lax`, 24 giờ) do `/api/auth/register` hoặc `/api/auth/login` đặt. Quyền: **public** không cần cookie; **user** cần đăng nhập (thiếu/sai/hết hạn → `401`); **admin** cần role `admin` (chưa đăng nhập → `401`, viewer → `403`).

| Method | Path | Quyền | Mô tả |
|---|---|---|---|
| GET | `/healthz` | public | `ok` |
| POST | `/api/auth/register` | public | Tạo tài khoản viewer và đăng nhập |
| POST | `/api/auth/login` | public | Đăng nhập, đặt cookie |
| POST | `/api/auth/logout` | public | Xoá cookie |
| GET | `/api/me` | user | Thông tin user |
| GET | `/api/videos?page=` | user | Catalog video `ready`, 20 video/trang |
| GET | `/api/videos/{id}` | user | Chi tiết video `ready` |
| GET | `/api/videos/{id}/play` | user | URL ký để phát (hạn 2 giờ) |
| POST | `/api/admin/videos` | admin | Tạo video, nhận presigned multipart upload |
| POST | `/api/admin/videos/{id}/complete` | admin | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | admin | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | admin | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | admin | Chi tiết video kèm `renditions` |

## POST /api/auth/register

`{"email":"an@example.com","password":"password123"}` → `201` + cookie:

```json
{"id":"5f0c…","email":"an@example.com","role":"viewer","created_at":"2026-11-15T10:00:00Z"}
```

Email được chuyển chữ thường. Mật khẩu 8–72 byte. `400` email/mật khẩu không hợp lệ hoặc có field lạ (ví dụ `role`), `409` email đã đăng ký.

## POST /api/auth/login

Body như register. `200` + cookie, body như trên. `401 {"error":"invalid email or password"}` khi sai email hoặc mật khẩu (không phân biệt).

## POST /api/auth/logout

`204`, xoá cookie. JWT không lưu phía server: token đã sao chép ra ngoài vẫn hợp lệ tới khi hết hạn ([ADR 0010](decisions/0010-jwt-cookie-session.md)).

## GET /api/me

`200` body như register. `401` nếu chưa đăng nhập hoặc tài khoản không còn.

## GET /api/videos

`?page=2` (1–1000, mặc định 1) → `200`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,
  "duration_s":30,"created_at":"2026-11-14T10:00:00Z"}],"page":2,"has_more":false}
```

Chỉ video `ready`, mới nhất trước. Tìm kiếm `q` và lọc `genre` có ở M4 (M3 bỏ qua).

## GET /api/videos/{id}

`200`: như một phần tử của `videos` ở trên, thêm `"renditions":[{"height":1080,"bitrate_kbps":5000},…]`. `404` nếu không có hoặc chưa `ready`.

## GET /api/videos/{id}/play

`200` (header `Cache-Control: no-store`):

```json
{"url":"http://localhost:8088/cdn/s/H23K0bGijtyB6JSJpoVT1g/1794736800/hls/7c1e…/master.m3u8",
 "expires_at":"2026-11-15T12:00:00Z","subtitles":[],"resume_at":0}
```

`url` hết hạn sau 2 giờ; playlist con và segment dùng chung chữ ký. nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play`. `subtitles` luôn `[]` và `resume_at` luôn `0` tới M5. `404` nếu không có hoặc chưa `ready`.
```

- [ ] **Step 6: `docs/operations.md`**

(a) Bảng `## Lệnh`: thay dòng `make e2e-m2` bằng các dòng:

```markdown
| `make seed-admin` | Tạo/đặt lại admin `ADMIN_EMAIL` / `ADMIN_PASSWORD` (trong `.env`) bên trong container api |
| `make e2e-m2` | E2E: đăng nhập admin, upload `tmp/sample.mp4` → đợi `ready` → kiểm tra HLS qua URL ký |
| `make e2e-m3` | E2E: 401/403, register/login/logout, `/play`, URL ký → 200, sửa chữ ký / hết hạn → 403 (chạy kèm `e2e-m2`) |
| `make sign-url ID=…` | In URL CDN đã ký (2 giờ) cho video `ID`, dùng với dev player hoặc video từ `make transcode` |
```

(b) Bảng `## Cổng và giao diện`: thay dòng `api` thành ``| api | http://localhost:8000 (`/healthz`) | cookie sau `POST /api/auth/login`; admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD` |``, thay dòng `Dev player` thành `| Dev player | http://localhost:8088/dev/player.html?src={URL ký, đã URL-encode} | |`, thay dòng `nginx "CDN"` thành `| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |`.

(c) Thêm trước `## Theo dõi Kafka`:

```markdown
## Biến môi trường mới (M3)

`make env` (chạy trong `make up`) tự thêm các khoá còn thiếu từ `.env.example` vào `.env`.

| Biến | Mặc định (`.env.example`) | Ý nghĩa |
|---|---|---|
| `JWT_SECRET` | chuỗi dev | Khoá HS256, ≥ 32 ký tự; thiếu thì api không khởi động |
| `CDN_SIGNING_SECRET` | `dev-cdn-secret-change-me` | Secret ký URL, 16–128 ký tự `[A-Za-z0-9_-]`; api và nginx phải cùng giá trị |
| `COOKIE_SECURE` | `false` | Bật thuộc tính `Secure` của cookie khi chạy HTTPS |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | `admin@streamfilm.local` / `admin-dev-password` | Dùng bởi `make seed-admin` và script e2e |
| `CDN_BASE_URL` | `http://localhost:8088` | Tiền tố của URL trong `/play` |

Đổi `CDN_SIGNING_SECRET` thì chạy lại `make up` để nginx và api cùng nhận giá trị mới; URL đã phát trước đó thành `403`.
```

(d) Thay khối lệnh trong `## Kiểm tra cache CDN` bằng:

````markdown
```bash
URL=$(make -s sign-url ID={video_id} 2>/dev/null)
curl -sI "${URL%master.m3u8}720p/seg_000.ts" | grep -i x-cache-status
```
````

(e) Bảng `## Sự cố thường gặp`, thêm các dòng:

```markdown
| `make up` báo `JWT_SECRET chưa đặt trong .env` | Chạy `make env` (thêm khoá mới từ `.env.example`) rồi `make up` |
| api thoát với `JWT_SECRET must be at least 32 characters` hoặc `CDN_SIGNING_SECRET must be …` | Sửa giá trị trong `.env` |
| `/api/admin/*` trả `401` | Chưa đăng nhập hoặc cookie hết hạn (24 giờ): `POST /api/auth/login`; chưa có admin: `make seed-admin` |
| `/api/admin/*` trả `403` | Tài khoản là viewer; dùng admin của `make seed-admin` |
| URL ký trả `403` | Hết hạn 2 giờ (gọi lại `/play`), URL bị sửa, hoặc `CDN_SIGNING_SECRET` của api và nginx khác nhau (chạy lại `make up`) |
| `http://localhost:8088/hls/...` trả `404` | Đúng thiết kế từ M3: chỉ còn đường `/cdn/s/...` có chữ ký |
```

- [ ] **Step 7: ADR**

Trong `docs/decisions/0002-dev-hls-bucket-read.md`, đổi dòng trạng thái thành `- Trạng thái: Chấp nhận (2026-11-14), cập nhật M3 (2026-11-15)` và thay mục `## Hệ quả` bằng:

```markdown
## Hệ quả
- Từ M3, nginx chỉ phục vụ HLS qua URL ký (`/cdn/s/...`, ADR 0009); đường `/hls/` không ký trên nginx đã bị bỏ.
- Bucket vẫn đọc ẩn danh trực tiếp trên MinIO (`http://localhost:9000/hls/...`), nên trên máy local vẫn vượt qua được lớp ký. Đây là thoả hiệp dev có chủ đích: MinIO chỉ bind `127.0.0.1`, và ký request S3 trong nginx không phải trọng tâm bài học.
- Production: bucket private, CDN đọc origin bằng credential riêng (R2 + Cloudflare, S3 + CloudFront OAC), người xem chỉ đi qua CDN.
```

Tạo `docs/decisions/0009-signed-url-secure-link-md5.md`:

```markdown
# ADR 0009: Signed URL theo prefix thư mục, kiểm tra bằng nginx `secure_link_md5`

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Segment HLS phát thẳng từ "CDN" (nginx), không đi qua api. Cần chặn người không đăng nhập và URL bị chia sẻ lâu dài, nhưng không thể ký từng segment (một video có hàng trăm file, playlist dùng đường dẫn tương đối). nginx `secure_link` gốc chỉ hỗ trợ MD5; HMAC-SHA256 cần njs hoặc OpenResty.

## Quyết định
- URL: `/cdn/s/{sig}/{exp}/hls/{id}/{file}`, `exp` = now + 2 giờ (Unix giây).
- `sig` = base64url không padding của `MD5("{exp}/hls/{id}/ {secret}")`, ký theo prefix thư mục `/hls/{id}/` nên mọi file trong thư mục dùng chung chữ ký và đường dẫn tương đối trong playlist tự mang chữ ký.
- nginx: `secure_link $sl_sig,$sl_exp; secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";` với các biến bắt từ regex của location. `$secure_link` rỗng (sai) hoặc `"0"` (hết hạn) đều trả `403`. Hợp lệ thì bỏ prefix chữ ký, proxy tới MinIO, cache theo `{id}/{file}`.
- Secret dùng chung qua biến `CDN_SIGNING_SECRET`, chỉ gồm `[A-Za-z0-9_-]` (16–128 ký tự) để chèn an toàn vào cấu hình nginx bằng envsubst của image chính thức.
- `internal/signing` là nguồn duy nhất của công thức phía Go; `internal/signing/nginx_test.go` chạy đúng template nginx của repo trong container để chứng minh hai bên khớp; script e2e tính lại chữ ký độc lập bằng `openssl`.

## Hệ quả
- Không thêm module nginx; giống cơ chế token của nhiều CDN thương mại.
- MD5 với secret ghép chuỗi không phải MAC chuẩn (dễ bị length-extension về lý thuyết; ở đây chuỗi có cấu trúc cố định và secret ở cuối). Đủ cho mục đích học; production nên dùng HMAC-SHA256 (njs/OpenResty hoặc token auth của CDN thật).
- URL không gắn với user: ai có URL thì xem được tới khi hết hạn (2 giờ). Thu ngắn TTL hoặc gắn IP/user vào chuỗi ký là hướng nâng cấp.
- Đổi secret làm mọi URL đang phát trả `403`; player gọi lại `/play`.
```

Tạo `docs/decisions/0010-jwt-cookie-session.md`:

```markdown
# ADR 0010: Phiên đăng nhập bằng JWT HS256 trong cookie HttpOnly

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §8: mật khẩu bcrypt, JWT HS256 trong cookie `HttpOnly`, `SameSite=Lax`, 24 giờ; role `admin` / `viewer`. Web (M4) và player cần gửi kèm phiên tự động.

## Quyết định
- Thư viện `github.com/golang-jwt/jwt/v5` (v5.3.1); bcrypt từ `golang.org/x/crypto` (cost 12).
- Claims: `sub` = user id, `role`, `iss` = `streamfilm`, `iat`, `exp` (24 giờ). Parse chỉ nhận `HS256`, bắt buộc `exp` và đúng `iss`.
- Cookie `sf_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` theo `COOKIE_SECURE`.
- Middleware `Authenticate` chỉ đọc JWT (không truy vấn DB); `RequireUser` → 401, `RequireAdmin` → 401/403. `/api/me` là chỗ duy nhất đọc DB để kiểm tra tài khoản còn tồn tại.
- Register tạo `viewer` và đăng nhập luôn. Login sai email vẫn chạy bcrypt với hash giả để thời gian phản hồi không lộ email đã đăng ký.
- Admin đầu tiên: `make seed-admin` → `api seed-admin` (upsert theo email, đặt lại mật khẩu, nâng role).

## Hệ quả
- Stateless, nhanh, dễ scale nhiều instance api (chỉ cần chung `JWT_SECRET`).
- Không thu hồi được token: logout chỉ xoá cookie phía trình duyệt; đổi role hoặc xoá user có hiệu lực khi token hết hạn (tối đa 24 giờ). Bản sau: TTL ngắn + refresh token, hoặc danh sách thu hồi.
- `SameSite=Lax` chặn cookie trong POST cross-site nên đủ chống CSRF ở mức MVP (các cổng khác nhau của `localhost` vẫn là same-site).
- Chưa giới hạn tốc độ đăng nhập (brute force); ghi nhận cho bản sau.
```

- [ ] **Step 8: Kiểm tra lần cuối và commit**

Run: `go vet ./... && make test && make e2e-m3`
Expected: `go vet` không cảnh báo; mọi package `ok` (integration test chạy vì không `-short`, gồm `TestNginxSecureLinkParity`); e2e in `OK  M3`.

```bash
git add README.md docs/architecture.md docs/flows.md docs/data-model.md docs/api.md docs/operations.md docs/decisions
git commit -m "docs: M3 auth and signed URL docs and ADRs"
```

---

## Self-review

**Độ phủ spec M3:**

| Yêu cầu | Task |
|---|---|
| Bảng `users` (id, email unique, password_hash, role admin/viewer, created_at) | 4 |
| bcrypt; JWT HS256 trong cookie HttpOnly SameSite=Lax 24h | 3, 5, 6 |
| `POST /api/auth/register` (viewer), `/login`, `/logout`, `GET /api/me` | 6 |
| `make seed-admin` | 6 (`SeedAdmin`), 4 (`UpsertAdmin`), 10 (subcommand + Makefile) |
| `/api/admin/*` chỉ cho role admin | 5, 10 (router + test), 11 (e2e 401/403) |
| `GET /api/videos` (ready), `GET /api/videos/{id}`, `GET /api/videos/{id}/play` cho user | 9, 10 |
| `/play`: kiểm tra đăng nhập + `ready`, `exp` = now + 2h, URL `/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8` | 7, 9 |
| Ký theo prefix thư mục `/hls/{id}/`; playlist con/segment tương đối mang theo chữ ký | 7, 8 (test segment), 11 |
| nginx `secure_link_md5`, package `signing` khớp đúng công thức nginx | 7 (vector openssl), 8 (nginx thật), 11 (openssl so với `/play`) |
| nginx: sai hoặc hết hạn → 403; đúng → proxy MinIO có cache | 8, 11 |
| E2E: chữ ký đúng → 200 (master + segment), sửa chữ ký / `exp` quá khứ → 403 | 8, 11 |
| Chặn truy cập không ký qua nginx | 8 (`/hls/` → 404), 11 |
| Secret đọc từ env, `.env` không commit | 2, 8, 10 |
| Tài liệu architecture/flows/data-model/api/operations, README `make seed-admin`, ADR signed URL theo prefix + `secure_link_md5` | 12 |
| Carry-over M2: ADR 0007 ghost publish, comment `kafka.go`, `--max-time` trong e2e | 1 |

Ngoài phạm vi M3 (đúng spec): tìm kiếm `q`/`genre` + migration `unaccent`/`pg_trgm` (M4); web React và logic player gọi lại `/play` khi 403 (M4); `resume_at` thật, `subtitles`, `thumbnails_url` (M5); `/api/me/continue-watching`, `/api/playback/events` (M5); PATCH/DELETE video (M7).

**Quét placeholder:** không có "TBD", "TODO", "tương tự Task N"; mọi bước có code hoặc lệnh đầy đủ kèm kết quả mong đợi. Toàn bộ code Go và test trong plan đã được biên dịch và chạy (`go vet ./...`, `go test ./...` gồm integration) trên một bản sao của nhánh `feat/mvp`; `make e2e-m3` đã chạy xanh trên stack compose dựng từ bản sao đó (cổng khác), và dev player phát được bằng URL ký.

**Nhất quán chữ ký:**
- `auth.Tokens.Issue(Identity) (string, time.Time, error)` dùng trong `Service.Session`, test middleware, `cmd/api/router_test.go`; `Tokens.Parse` dùng trong `Tokens.Authenticate`.
- `*auth.Repo` thoả `auth.UserStore` (Create / GetByEmail / GetByID / UpsertAdmin); `Handler.me` dùng `Svc.Users.GetByID`.
- `auth.RequireUser`, `auth.RequireAdmin` có kiểu `func(http.Handler) http.Handler`, dùng với `r.Use` / `r.With` của chi.
- `*signing.Signer.Path(videoID, file string) (string, time.Time, error)` thoả `catalog.PlaySigner`; `fakeSigner` trong test cùng chữ ký.
- `*catalog.Repo` thoả `catalog.ViewerStore` (ListReady / Get / Renditions) và `catalog.AdminStore` (M2); `emptyStore` trong `router_test.go` thoả cả hai.
- Công thức ký giống hệt ở 4 nơi: `signing.Signature` (`exp + dir + " " + secret`), template nginx (`"$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}"`), hàm `sign` trong `e2e-m3.sh` (`printf '%s/hls/%s/ %s'`), vector openssl trong `signing_test.go`.
- Tên biến môi trường thống nhất: `JWT_SECRET`, `CDN_SIGNING_SECRET`, `COOKIE_SECURE`, `CDN_BASE_URL`, `ADMIN_EMAIL`, `ADMIN_PASSWORD` trong `config.Load`, `.env.example`, compose, Makefile, `commands.go`, script e2e.

## Các kế hoạch tiếp theo

1. **M4** — Web React + Vite: đăng nhập, trang chủ (`GET /api/videos`), chi tiết, player hls.js gọi `/play` và gọi lại khi nhận 403; tìm kiếm không dấu (migration cột `search`, `unaccent`, `pg_trgm`, tham số `q`, `genre`).
2. **M5** — Xem tiếp (`watch_progress`, `resume_at` thật), poster, sprite + `thumbnails_url`, phụ đề (URL ký trong `subtitles`).
3. **M6** — analytics service, ClickHouse schema, `slog` handler gửi `app.logs`.
4. **M7** — Trang admin, `scripts/e2e.sh` đầy đủ, hoàn thiện tài liệu.
