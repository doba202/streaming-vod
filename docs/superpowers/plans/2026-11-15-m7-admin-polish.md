# Streamfilm M7: Trang admin, hardening và tài liệu cuối — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hoàn thiện MVP (spec §10, M7): trang quản trị `/admin` trong SPA (danh sách có chip trạng thái + `fail_reason` + tự cập nhật, upload multipart thẳng từ trình duyệt lên MinIO có thanh tiến độ, thử lại video lỗi, sửa metadata, upload phụ đề, xoá); hai endpoint còn thiếu của spec §6 `PATCH` và `DELETE /api/admin/videos/{id}`; `scripts/e2e.sh` + `make e2e` chạy toàn bộ kịch bản và in tổng kết, `make test-all`; các nhận xét review rẻ mà đáng sửa (header bảo mật nginx, flake MinIO testcontainer, `stop_grace_period`, `completion_rate` ≤ 1, Dockerfile thừa, JWT `iat`, bcrypt warm-up, `ErrorBoundary`); bộ tài liệu §11 đầy đủ, khớp code, thêm `docs/known-limitations.md` và ADR 0019–0020.

**Architecture:** Không thêm service, bảng hay dependency. `catalog` thêm `NormalizeMetadata` (giới hạn metadata dùng chung cho tạo và sửa), `Repo.UpdateMetadata`, `Repo.Delete` (khoá dòng, từ chối `queued`/`processing` bằng `ErrVideoBusy`), `PATCH` trong `AdminHandler`, `subtitles` trong chi tiết admin. `upload.Service.Delete` xoá dòng rồi dọn `raw/{id}/` + `hls/{id}/` (lỗi storage chỉ log), `DELETE` trong `upload.Handler`. Web: `adminApi` trong `api.ts`, `src/admin/upload.ts` (PUT part song song bằng XHR, thử lại, huỷ), ba trang lazy dưới `/admin` bọc `RequireAdmin`. nginx include một snippet header bảo mật ở cấp server và trong từng location có `add_header`. MinIO bật CORS giới hạn origin (`MINIO_API_CORS_ALLOW_ORIGIN`). `scripts/e2e-m7.sh` kiểm tra phần M7 qua HTTP; `scripts/e2e.sh` chạy m3→m7.

**Tech Stack:** Go 1.27.4, chi v5.5.0, pgx v5, google/uuid v1.6.0, golang-jwt v5 (`jwt.WithIssuedAt`), testcontainers-go v0.45.0 (`testcontainers.WithWaitStrategy` ghi đè wait mặc định của module minio); React 18.3.1, react-router-dom 7.18.5, Vitest 4.1.12 + Testing Library; nginx 1.27-alpine; MinIO Chainguard (digest như compose). **Không thêm dependency:** `go.mod`, `go.sum`, `web/app/package.json`, `package-lock.json` không đổi; không có migration mới.

**Spec:** `docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md` (mục 6, 8, 9, 10, 11)

## Global Constraints

- Go module `streamfilm`, nhánh `feat/mvp`. `git diff --stat go.mod go.sum web/app/package.json web/app/package-lock.json migrations/` rỗng sau mọi task. Ở mỗi task Go: `gofmt -l .` rỗng, `go vet ./...` và `go test -short ./...` xanh. Ở mỗi task web: `cd web/app && npx tsc --noEmit && npm test` xanh.
- **Metadata** (`catalog.NormalizeMetadata`, dùng cho `POST /api/admin/videos` và `PATCH`): cắt khoảng trắng đầu/cuối title, description, từng genre; bỏ genre rỗng và trùng (giữ thứ tự); `title` 1–`MaxTitleLen`=200 rune, không ký tự điều khiển; `description` ≤ `MaxDescriptionLen`=5000 rune (cho phép `\n`, `\r`, `\t`); tối đa `MaxGenres`=10 genre, mỗi cái ≤ `MaxGenreLen`=50 rune (hằng có sẵn ở `viewer.go`); `year` 0 (không rõ → NULL) hoặc `MinYear`=1888–`MaxYear`=2100 (hằng có sẵn). Sai → `ErrInvalidMetadata` → HTTP `400`.
- **PATCH** `/api/admin/videos/{id}`: body `{"title"?, "description"?, "genres"?, "year"?}` (field lạ → `400`, `{}` → `400 nothing to update`); field vắng giữ nguyên, `year: 0` xoá năm, `genres: []` xoá thể loại; mọi trạng thái; trả `200` + `VideoJSON` (không `renditions`/`subtitles`). `{id}` không phải UUID hoặc không có → `404`.
- **DELETE** `/api/admin/videos/{id}`: `uploading`/`ready`/`failed` → `204`; `queued`/`processing` → `409` (`catalog.ErrVideoBusy`, thông báo `video is being processed: video is {status}`); không có → `404`. Thứ tự: transaction `SELECT … FOR UPDATE` → `DELETE` (cascade `renditions`, `subtitles`, `watch_progress`) → commit → `AbortMultipartUpload` nếu `uploading` và có `upload_id` → `DeletePrefix(raw, "{id}/")` → `DeletePrefix(hls, "{id}/")`; lỗi storage chỉ log `delete objects after delete failed` / `abort multipart after delete failed`, dùng `context.WithoutCancel`.
- **Admin GET** `/api/admin/videos/{id}` thêm `subtitles: [{lang, label, vtt_key}]` (`[]` khi chưa có, tag `omitzero`); danh sách không có field này.
- **Web admin:** route `/admin` (danh sách, `ADMIN_LIST_LIMIT`=200), `/admin/upload`, `/admin/videos/:id`, tải lazy, bọc `RequireAdmin` (viewer → trang `403 — Không có quyền`); header link "Quản trị" chỉ cho admin. Poll `POLL_MS`=3000 khi có video `queued`/`processing` (không poll `uploading`). Upload: `UPLOAD_CONCURRENCY`=3 part cùng lúc, `PART_ATTEMPTS`=3 lần/part, XHR `PUT` không đặt `Content-Type`, không đọc `ETag`; lỗi sau khi đã tạo video → `UploadVideoError{videoId}` để xoá video dở.
- **nginx** (mọi phản hồi, kể cả 4xx): `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `Referrer-Policy: same-origin` — `deploy/nginx/snippets/security-headers.conf`, mount `/etc/nginx/snippets/` (ro), include ở cấp `server` và trong mọi location có `add_header` riêng (`/cdn/s/…`, `/cdn/`, `/assets/`, `/`).
- **MinIO CORS:** compose `MINIO_API_CORS_ALLOW_ORIGIN: ${MINIO_CORS_ALLOW_ORIGIN:-http://localhost:8088,http://localhost:5173}`; `.env.example` có `MINIO_CORS_ALLOW_ORIGIN` cùng giá trị. Service `api`: `S3_PUBLIC_ENDPOINT: ${S3_PUBLIC_ENDPOINT:-http://localhost:9000}`.
- **Hardening:** `analytics` `stop_grace_period: 25s`; Dockerfile transcoder/analytics không `COPY migrations`; `auth.Tokens.Parse` thêm `jwt.WithIssuedAt()`; `auth.WarmUp()` chạy nền khi api khởi động; `testutil.StartMinIO` đợi `/minio/health/ready`; `completion_rate.sql` gom (video, user) rồi đếm người có cả `play` và `ended`.
- **E2E:** `scripts/e2e.sh` chạy `E2E_ONLY` (mặc định `m3 m4 m5 m6 m7`) tuần tự, không dừng khi một kịch bản lỗi, log `$E2E_LOG_DIR` (mặc định `tmp/e2e-logs`), in bảng `m? PASS|FAIL Ns log`, thoát 1 nếu có lỗi. `scripts/e2e-m7.sh` cần `curl jq split ffmpeg`, biến `SITE` (mặc định `http://localhost:8088`), `MINIO` (mặc định `http://localhost:9000`), `TIMEOUT` (300).
- Tài liệu tiếng Việt, chỉ mô tả cái đã chạy (spec §11). Commit kết thúc bằng dòng `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` (tham số `-m` thứ hai).

## Quyết định thiết kế chính

| Quyết định | Lý do |
|---|---|
| Trang admin nằm trong cùng SPA (`/admin`, lazy), không phải app riêng | Cùng origin nên cookie `sf_session` dùng lại, không thêm CORS cho API (ADR 0011); người xem không tải code admin |
| Upload từ trình duyệt PUT thẳng presigned URL lên MinIO (`:9000`) bằng XHR, 3 part song song, mỗi part thử 3 lần (ADR 0019) | Giữ "video không đi qua api" (spec §3, ADR 0004); XHR có `upload.onprogress`, fetch thì không; api `ListParts` nên không cần expose `ETag` |
| MinIO CORS giới hạn origin bằng `MINIO_API_CORS_ALLOW_ORIGIN` thay vì để mặc định `*` | Đã kiểm tra: MinIO mặc định trả `Access-Control-Allow-Origin` cho mọi origin; giới hạn về SPA + Vite dev là hardening rẻ, e2e kiểm cả origin lạ |
| `DELETE` từ chối `queued`/`processing` (409), xoá dòng trước rồi dọn object best-effort (ADR 0020) | Transcoder không đọc DB: xoá giữa lúc transcode để lại `hls/{id}/` mồ côi; DB là nguồn sự thật, object mồ côi không làm sai hành vi |
| `PATCH` đọc–gộp–chuẩn hoá–ghi đè 4 cột, last-write-wins | Phân biệt "không gửi" với "xoá" (`year: 0`, `genres: []`) và kiểm giới hạn trên giá trị sau gộp; không cần optimistic locking ở MVP |
| `NormalizeMetadata` dùng chung cho tạo video và PATCH | Sửa luôn nhận xét M2 (không giới hạn title/genres, không kiểm year) cho cả hai đường |
| `DELETE` nằm ở `upload.Service` (có `Objects`, `BucketRaw`), thêm `BucketHLS`; `PATCH` ở `catalog.AdminHandler` | `upload` đã sở hữu vòng đời object (create/abort/cleaner); PATCH chỉ đụng metadata |
| Poll 3 giây chỉ khi có `queued`/`processing` | `uploading` chỉ đổi khi admin gọi `/complete`; video bỏ dở có thể ở `uploading` 24 giờ |
| Header bảo mật qua snippet `include` ở server + từng location có `add_header` | nginx không kế thừa `add_header` của cấp trên khi location có `add_header` riêng; nginx 1.27 chưa có `add_header_inherit` |
| `e2e.sh` điều phối các script theo giai đoạn (m3 gồm m2) + `e2e-m7.sh` mới, chạy tiếp khi một kịch bản lỗi | Tái dùng kịch bản đã xanh; tổng kết đủ bức tranh thay vì dừng ở lỗi đầu |
| Kịch bản lỗi e2e dùng file chỉ có tiếng (ffmpeg `sine`) | `transcode.ErrNoVideo` là lỗi vĩnh viễn → `failed` ngay (không đợi backoff 2,5 phút) với `fail_reason` xác định |
| Nhận xét review còn lại ghi vào `docs/known-limitations.md` | Spec §11 yêu cầu tài liệu khớp code; giới hạn chấp nhận phải nhìn thấy được |

## File Structure

```
streamfilm/
  internal/catalog/metadata.go(+_test)        # mới: Metadata, NormalizeMetadata, ErrInvalidMetadata, MaxTitleLen…
  internal/catalog/repo.go                     # ErrVideoBusy, UpdateMetadata, Delete
  internal/catalog/admin_repo_test.go          # mới: Postgres thật
  internal/catalog/handler.go(+_test)          # AdminStore + Subtitles/UpdateMetadata, PATCH, subtitles trong GET
  internal/upload/service.go(+_test)           # Videos.Delete, Objects.DeletePrefix, BucketHLS, Create dùng NormalizeMetadata, Delete
  internal/upload/handler.go, delete_test.go   # DELETE, videoID chuẩn hoá
  internal/auth/token.go, service.go(+_test)   # WithIssuedAt, WarmUp
  internal/testutil/containers.go              # MinIO wait /minio/health/ready
  internal/signing/nginx_test.go               # kiểm header bảo mật
  internal/analytics/queries_test.go, scripts/analytics/completion_rate.sql
  cmd/api/main.go, router_test.go
  deploy/nginx/snippets/security-headers.conf  # mới
  deploy/nginx/templates/default.conf.template, deploy/docker-compose.yml, deploy/docker/{transcoder,analytics}.Dockerfile
  .env.example, Makefile
  scripts/e2e-m7.sh, scripts/e2e.sh            # mới
  web/app/src/api.ts(+test), format.ts(+test), App.tsx, components/{Header,ErrorBoundary(+test)}.tsx, styles.css
  web/app/src/admin/{upload.ts, StatusChip.tsx, RequireAdmin.tsx, AdminVideosPage.tsx, AdminUploadPage.tsx, AdminVideoPage.tsx} (+ test)  # mới
  README.md, docs/{api,architecture,data-model,flows,operations,known-limitations}.md
  docs/decisions/0019-admin-browser-upload.md, 0020-video-delete-and-edit.md   # mới
```

Quy ước trình bày code: file mới có toàn văn; file sửa được ghi thành các cặp "Thay A bằng B" — A khớp đúng một lần trong file tại thời điểm áp (đã kiểm tra bằng script theo đúng thứ tự task), áp theo thứ tự. Lệnh chạy từ gốc repo trừ khi ghi `cd web/app`.

---

### Task 1: Hardening nhỏ: JWT `iat`, bcrypt warm-up, MinIO wait, Dockerfile, grace period, `completion_rate`

**Files:**
- Modify: `internal/auth/token_test.go`, `internal/auth/service_test.go`, `internal/analytics/queries_test.go`, `internal/auth/token.go`, `internal/auth/service.go`, `cmd/api/main.go`, `internal/testutil/containers.go`, `scripts/analytics/completion_rate.sql`, `deploy/docker/transcoder.Dockerfile`, `deploy/docker/analytics.Dockerfile`, `deploy/docker-compose.yml`

**Interfaces:**
- Produces:
  ```go
  // internal/auth
  func WarmUp() // tính sẵn dummyHash; cmd/api gọi `go auth.WarmUp()` trước khi kết nối DB
  // (*Tokens).Parse thêm jwt.WithIssuedAt(): iat ở tương lai → ErrInvalidToken
  ```
- `testutil.StartMinIO` giữ chữ ký `func StartMinIO(t *testing.T) MinIO`, chỉ đổi wait strategy.

- [ ] **Step 1: Viết test (thất bại trước)**

`TestParseRejects` thêm token có `iat` sau `now` một giờ; `TestWarmUp` đo lần gọi `dummyHash()` sau `WarmUp`; `TestSampleQueries` thêm người xem `u3` chỉ có `ended` (câu SQL cũ cho `completed = 2`, tỉ lệ 1).

`internal/auth/token_test.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
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
```

   bằng:

```go
	badRole, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "role": "root", "iss": "streamfilm", "exp": now.Add(time.Hour).Unix(),
	}).SignedString([]byte(testSecret))
	// iat ở tương lai (token giả mạo hoặc đồng hồ server lệch): jwt.WithIssuedAt từ chối.
	future, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "role": "admin", "iss": "streamfilm", "iat": now.Add(time.Hour).Unix(), "exp": now.Add(2 * time.Hour).Unix(),
	}).SignedString([]byte(testSecret))
	parts := strings.Split(good, ".")
	tampered := parts[0] + "." + parts[1] + "x." + parts[2]

	cases := map[string]string{
		"other secret": otherTok, "alg none": none, "no exp": noExp, "bad role": badRole,
		"tampered": tampered, "garbage": "abc", "empty": "", "iat in future": future,
	}
	for name, tok := range cases {
		if _, err := tk.Parse(tok); !errors.Is(err, ErrInvalidToken) {
```

`internal/auth/service_test.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
		t.Errorf("short password: err = %v", err)
	}
}
```

   bằng:

```go
		t.Errorf("short password: err = %v", err)
	}
}

func TestWarmUp(t *testing.T) {
	WarmUp()
	start := time.Now()
	h := dummyHash()
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("dummyHash after WarmUp took %v, want cached", d)
	}
	if !CheckPassword(h, "streamfilm-dummy-password") {
		t.Error("dummy hash must be a valid bcrypt hash")
	}
}
```

`internal/analytics/queries_test.go` (sửa — 2 cặp thay thế):

1. Thay:

```go
	ts := func(sec int) string { return CHTimeString(now.Add(time.Duration(sec) * time.Second)) }
	const (
		u1, u2 = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333"
		v1     = "22222222-2222-2222-2222-222222222222"
	)
	var rows []string
```

   bằng:

```go
	ts := func(sec int) string { return CHTimeString(now.Add(time.Duration(sec) * time.Second)) }
	const (
		u1, u2 = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333"
		u3     = "44444444-4444-4444-4444-444444444444" // chỉ có ended (play bị mất): không làm tỉ lệ xem hết > 1
		v1     = "22222222-2222-2222-2222-222222222222"
	)
	var rows []string
```

2. Thay:

```go
		bitrate, buf int
	}{
		{u1, "play", 2800, 0}, {u2, "play", 1400, 0}, {u1, "buffering", 2800, 1500}, {u2, "buffering", 1400, 500},
		{u2, "buffering", 1400, 700}, {u1, "ended", 2800, 0},
	} {
		b, _ := json.Marshal(map[string]any{"ts": ts(len(rows)), "user_id": e.user, "video_id": v1, "type": e.typ,
			"position_s": 0, "bitrate_kbps": e.bitrate, "buffering_ms": e.buf})
```

   bằng:

```go
		bitrate, buf int
	}{
		{u1, "play", 2800, 0}, {u2, "play", 1400, 0}, {u1, "buffering", 2800, 1500}, {u2, "buffering", 1400, 500},
		{u2, "buffering", 1400, 700}, {u1, "ended", 2800, 0}, {u3, "ended", 2800, 0},
	} {
		b, _ := json.Marshal(map[string]any{"ts": ts(len(rows)), "user_id": e.user, "video_id": v1, "type": e.typ,
			"position_s": 0, "bitrate_kbps": e.bitrate, "buffering_ms": e.buf})
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/auth/ -run 'TestParseRejects|TestWarmUp' ; go test ./internal/analytics/ -run TestSampleQueries`
Expected: `internal/auth` FAIL biên dịch `undefined: WarmUp`; `TestSampleQueries` FAIL `completion_rate: got {"video_id":"2222…","started":"2","completed":"2","completion_rate":1} want …"completed":"1","completion_rate":0.5}` (cần Docker; ClickHouse ~15 giây).

- [ ] **Step 3: Cài đặt**

`cmd/api/main.go` ở task này chỉ thêm `go auth.WarmUp()`. `deploy/docker-compose.yml` ở task này chỉ đổi `stop_grace_period` của `analytics` (INSERT đang chạy tối đa 10 giây + ghi nốt lô 10 giây).

`internal/auth/token.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
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
```

   bằng:

```go
	return s, exp, nil
}

// Parse chỉ nhận HS256 (chặn "alg":"none" và nhầm thuật toán), bắt buộc có exp, đúng issuer và iat (nếu có)
// không ở tương lai.
func (t *Tokens) Parse(s string) (Identity, error) {
	var c claims
	_, err := jwt.ParseWithClaims(s, &c, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
```

`internal/auth/service.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
	return h
})

// Register tạo tài khoản viewer.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
```

   bằng:

```go
	return h
})

// WarmUp tính sẵn dummyHash (bcrypt, ~250ms) để request đăng nhập đầu tiên với email lạ không chậm hơn
// các request sau (chênh lệch thời gian đó lộ email chưa đăng ký). api gọi lúc khởi động.
func WarmUp() { dummyHash() }

// Register tạo tài khoản viewer.
func (s *Service) Register(ctx context.Context, email, password string) (User, error) {
	email, err := NormalizeEmail(email)
```

`cmd/api/main.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
	}()
	log = logging.New("api", os.Stdout, logSink)
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

```

   bằng:

```go
	}()
	log = logging.New("api", os.Stdout, logSink)
	slog.SetDefault(log)
	go auth.WarmUp() // bcrypt dummy hash chạy nền trong lúc kết nối DB, Kafka
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

```

`internal/testutil/containers.go` (sửa — 2 cặp thay thế):

1. Thay:

```go
}

// StartMinIO chạy MinIO trong container. Skip khi -short hoặc không có Docker.
func StartMinIO(t *testing.T) MinIO {
	t.Helper()
	if testing.Short() {
```

   bằng:

```go
}

// StartMinIO chạy MinIO trong container. Skip khi -short hoặc không có Docker.
// Đợi /minio/health/ready (không phải /live như mặc định của module): /live trả 200 trước khi MinIO nhận
// request S3, khiến lệnh đầu tiên đôi khi lỗi "Server not initialized yet".
func StartMinIO(t *testing.T) MinIO {
	t.Helper()
	if testing.Short() {
```

2. Thay:

```go
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcminio.Run(ctx, MinIOImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
```

   bằng:

```go
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcminio.Run(ctx, MinIOImage, testcontainers.WithWaitStrategy(
		wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").WithStartupTimeout(time.Minute)))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
```

`scripts/analytics/completion_rate.sql` (sửa — 1 cặp thay thế):

1. Thay:

```sql
-- Tỉ lệ xem hết theo phim: số người có sự kiện ended / số người có sự kiện play
SELECT
    video_id,
    uniqExactIf(user_id, type = 'play') AS started,
    uniqExactIf(user_id, type = 'ended') AS completed,
    round(completed / started, 3) AS completion_rate
FROM playback_events
GROUP BY video_id
HAVING started > 0
ORDER BY started DESC, video_id
```

   bằng:

```sql
-- Tỉ lệ xem hết theo phim: trong số người có sự kiện play, bao nhiêu người cũng có sự kiện ended.
-- Người chỉ có ended (play bị mất vì best-effort, ADR 0015) không được tính nên tỉ lệ luôn trong [0, 1].
SELECT
    video_id,
    countIf(played) AS started,
    countIf(played AND finished) AS completed,
    round(completed / started, 3) AS completion_rate
FROM
(
    SELECT video_id, user_id, max(type = 'play') AS played, max(type = 'ended') AS finished
    FROM playback_events
    GROUP BY video_id, user_id
)
GROUP BY video_id
HAVING started > 0
ORDER BY started DESC, video_id
```

`deploy/docker/transcoder.Dockerfile` (sửa — 1 cặp thay thế):

1. Thay:

```dockerfile
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/transcoder ./cmd/transcoder

FROM alpine:3.24.4
```

   bằng:

```dockerfile
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/transcoder ./cmd/transcoder

FROM alpine:3.24.4
```

`deploy/docker/analytics.Dockerfile` (sửa — 1 cặp thay thế):

1. Thay:

```dockerfile
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/analytics ./cmd/analytics

FROM alpine:3.24.4
```

   bằng:

```dockerfile
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/analytics ./cmd/analytics

FROM alpine:3.24.4
```

`deploy/docker-compose.yml` (sửa — 1 cặp thay thế):

1. Thay:

```yaml
      kafka-init: { condition: service_completed_successfully }
      clickhouse: { condition: service_healthy }
    restart: unless-stopped
    # Đủ cho lần ghi nốt lô đang dở khi tắt (ShutdownTimeout 10 giây).
    stop_grace_period: 15s

volumes:
  pgdata:
```

   bằng:

```yaml
      kafka-init: { condition: service_completed_successfully }
      clickhouse: { condition: service_healthy }
    restart: unless-stopped
    # Khi tắt: chờ INSERT đang chạy (InsertTimeout 10 giây) rồi ghi nốt lô (ShutdownTimeout 10 giây) = tối đa 20 giây.
    stop_grace_period: 25s

volumes:
  pgdata:
```

- [ ] **Step 4: Kiểm tra Dockerfile và compose**

Run: `docker build -q -f deploy/docker/transcoder.Dockerfile . >/dev/null && docker build -q -f deploy/docker/analytics.Dockerfile . >/dev/null && echo build-ok; docker compose -f deploy/docker-compose.yml --env-file .env config | grep 'stop_grace_period'`
Expected: `build-ok` (không package nào của `cmd/transcoder`, `cmd/analytics` import `streamfilm/migrations`); hai dòng `stop_grace_period: 30s` (transcoder) và `stop_grace_period: 25s` (analytics). Lệnh `docker build` không gắn tag nên không đụng image `:dev` đang chạy.

- [ ] **Step 5: Commit**

```bash
git add internal/auth cmd/api/main.go internal/testutil internal/analytics/queries_test.go scripts/analytics/completion_rate.sql deploy/docker deploy/docker-compose.yml
git commit -m "fix: JWT iat check, bcrypt warm-up, MinIO ready wait, completion_rate <= 1, analytics grace period" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `catalog`: chuẩn hoá metadata, `Repo.UpdateMetadata`, `Repo.Delete`

**Files:**
- Create: `internal/catalog/metadata_test.go`, `internal/catalog/admin_repo_test.go`, `internal/catalog/metadata.go`
- Modify: `internal/catalog/repo.go`

**Interfaces:**
- Produces (`internal/catalog`):
  ```go
  const (MaxTitleLen = 200; MaxDescriptionLen = 5000; MaxGenres = 10) // MaxGenreLen=50, MinYear=1888, MaxYear=2100 có sẵn ở viewer.go
  var ErrInvalidMetadata = errors.New("invalid metadata")
  var ErrVideoBusy = errors.New("video is being processed")
  type Metadata struct { Title, Description string; Genres []string; Year int }
  func NormalizeMetadata(m Metadata) (Metadata, error)
  func (r *Repo) UpdateMetadata(ctx context.Context, id string, m Metadata) (Video, error) // không có → ErrNotFound
  func (r *Repo) Delete(ctx context.Context, id string) (Video, error)                      // ErrNotFound | ErrVideoBusy (kèm video)
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/catalog/metadata_test.go` (mới, toàn văn):

```go
package catalog

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeMetadata(t *testing.T) {
	got, err := NormalizeMetadata(Metadata{Title: "  Trò chơi  ", Description: " dòng 1\ndòng 2 ",
		Genres: []string{" drama ", "", "drama", "hài"}, Year: 2024})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Trò chơi" || got.Description != "dòng 1\ndòng 2" || got.Year != 2024 ||
		!slices.Equal(got.Genres, []string{"drama", "hài"}) {
		t.Errorf("got %+v", got)
	}
	if got, _ := NormalizeMetadata(Metadata{Title: "A"}); got.Genres == nil {
		t.Error("genres must be non-nil")
	}

	tooManyGenres := make([]string, MaxGenres+1)
	for i := range tooManyGenres {
		tooManyGenres[i] = strings.Repeat("g", i+1)
	}
	bad := map[string]Metadata{
		"empty title":      {Title: "   "},
		"long title":       {Title: strings.Repeat("ữ", MaxTitleLen+1)},
		"control in title": {Title: "a\x00b"},
		"newline in title": {Title: "a\nb"},
		"long description": {Title: "A", Description: strings.Repeat("a", MaxDescriptionLen+1)},
		"long genre":       {Title: "A", Genres: []string{strings.Repeat("g", MaxGenreLen+1)}},
		"too many genres":  {Title: "A", Genres: tooManyGenres},
		"year too small":   {Title: "A", Year: 1500},
		"year too big":     {Title: "A", Year: 3000},
		"invalid utf8":     {Title: "\xff"},
	}
	for name, m := range bad {
		if _, err := NormalizeMetadata(m); !errors.Is(err, ErrInvalidMetadata) {
			t.Errorf("%s: err = %v, want ErrInvalidMetadata", name, err)
		}
	}
	if _, err := NormalizeMetadata(Metadata{Title: strings.Repeat("ữ", MaxTitleLen), Year: MinYear}); err != nil {
		t.Errorf("limits must be inclusive: %v", err)
	}
}
```

`internal/catalog/admin_repo_test.go` (mới, toàn văn):

```go
package catalog

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestRepoUpdateMetadata(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if err := r.Create(ctx, newVideo(idA)); err != nil {
		t.Fatal(err)
	}
	makeReady(t, r, idA)
	before, err := r.Get(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}

	v, err := r.UpdateMetadata(ctx, idA, Metadata{Title: "Hạ cánh nơi anh", Description: "phim Hàn",
		Genres: []string{"romance", "drama"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Hạ cánh nơi anh" || v.Description != "phim Hàn" || v.Year != 0 ||
		!slices.Equal(v.Genres, []string{"romance", "drama"}) || v.Status != StatusReady ||
		!v.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated = %+v", v)
	}
	// Trigger tính lại cột search: tìm không dấu theo tiêu đề mới, tiêu đề cũ không còn khớp.
	if got, err := r.Search(ctx, SearchParams{Query: "ha canh", Limit: 10}); err != nil || !slices.Equal(ids(got), []string{idA}) {
		t.Errorf("search new title = %v, err = %v", ids(got), err)
	}
	if got, err := r.Search(ctx, SearchParams{Query: "Phim 1111", Limit: 10}); err != nil || len(got) != 0 {
		t.Errorf("search old title = %v, err = %v", ids(got), err)
	}
	if _, err := r.UpdateMetadata(ctx, idX, Metadata{Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestRepoDelete(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB, idC} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}
	// idA ready, có rendition + phụ đề + tiến độ xem; idB queued; idC failed.
	makeReady(t, r, idA)
	if err := r.UpsertSubtitle(ctx, idA, Subtitle{Lang: "vi", Label: "Tiếng Việt", VTTKey: SubtitleKey(idA, "vi")}); err != nil {
		t.Fatal(err)
	}
	const userID = "77777777-7777-7777-7777-777777777777"
	if _, err := r.pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, 'u@x.vn', 'h', 'viewer')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO watch_progress (user_id, video_id, position_s, duration_s) VALUES ($1, $2, 10, 30)`, userID, idA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transition(ctx, idB, StatusQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transition(ctx, idC, StatusQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MarkFailed(ctx, idC, "boom"); err != nil {
		t.Fatal(err)
	}

	v, err := r.Delete(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != idA || v.Status != StatusReady || v.RawKey != idA+"/source.mp4" {
		t.Errorf("deleted = %+v", v)
	}
	if _, err := r.Get(ctx, idA); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v", err)
	}
	for _, table := range []string{"renditions", "subtitles", "watch_progress"} {
		var n int
		if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE video_id = $1`, idA).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows after delete = %d, err = %v", table, n, err)
		}
	}
	if _, err := r.Delete(ctx, idA); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: err = %v", err)
	}
	if v, err := r.Delete(ctx, idB); !errors.Is(err, ErrVideoBusy) || v.Status != StatusQueued {
		t.Errorf("delete queued: v = %+v, err = %v", v, err)
	}
	if _, err := r.Get(ctx, idB); err != nil {
		t.Errorf("queued video must still exist: %v", err)
	}
	if v, err := r.Delete(ctx, idC); err != nil || v.Status != StatusFailed || v.FailReason != "boom" {
		t.Errorf("delete failed video: v = %+v, err = %v", v, err)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/catalog/ -run 'TestNormalizeMetadata|TestRepoUpdateMetadata|TestRepoDelete'`
Expected: FAIL biên dịch — `undefined: NormalizeMetadata`, `undefined: Metadata`, `r.UpdateMetadata undefined`, `r.Delete undefined`, `undefined: ErrVideoBusy`.

- [ ] **Step 3: Cài đặt**

`internal/catalog/metadata.go` (mới, toàn văn):

```go
package catalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Giới hạn metadata của video, dùng chung cho tạo video (POST /api/admin/videos) và sửa (PATCH).
// Độ dài thể loại và khoảng năm dùng chung hằng MaxGenreLen, MinYear, MaxYear với bộ lọc tìm kiếm (viewer.go).
const (
	MaxTitleLen       = 200  // rune
	MaxDescriptionLen = 5000 // rune
	MaxGenres         = 10
)

// ErrInvalidMetadata: tiêu đề, mô tả, thể loại hoặc năm không hợp lệ (HTTP 400).
var ErrInvalidMetadata = errors.New("invalid metadata")

// Metadata là phần admin sửa được của video. Year = 0 nghĩa là không rõ (NULL trong DB).
type Metadata struct {
	Title       string
	Description string
	Genres      []string
	Year        int
}

func invalidMetadata(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidMetadata, fmt.Sprintf(format, args...))
}

func hasControl(s string, allowNewline bool) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && !(allowNewline && (r == '\n' || r == '\r' || r == '\t'))
	}) >= 0
}

// NormalizeMetadata cắt khoảng trắng đầu/cuối, bỏ thể loại rỗng hoặc trùng (giữ thứ tự) và kiểm tra giới hạn.
// Genres luôn khác nil trong kết quả.
func NormalizeMetadata(m Metadata) (Metadata, error) {
	m.Title = strings.TrimSpace(m.Title)
	m.Description = strings.TrimSpace(m.Description)
	if m.Title == "" || !utf8.ValidString(m.Title) || utf8.RuneCountInString(m.Title) > MaxTitleLen || hasControl(m.Title, false) {
		return Metadata{}, invalidMetadata("title must be 1-%d characters of text", MaxTitleLen)
	}
	if !utf8.ValidString(m.Description) || utf8.RuneCountInString(m.Description) > MaxDescriptionLen || hasControl(m.Description, true) {
		return Metadata{}, invalidMetadata("description must be at most %d characters of text", MaxDescriptionLen)
	}
	genres := make([]string, 0, len(m.Genres))
	seen := map[string]bool{}
	for _, g := range m.Genres {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		if !utf8.ValidString(g) || utf8.RuneCountInString(g) > MaxGenreLen || hasControl(g, false) {
			return Metadata{}, invalidMetadata("each genre must be 1-%d characters of text", MaxGenreLen)
		}
		seen[g] = true
		genres = append(genres, g)
	}
	if len(genres) > MaxGenres {
		return Metadata{}, invalidMetadata("at most %d genres", MaxGenres)
	}
	m.Genres = genres
	if m.Year != 0 && (m.Year < MinYear || m.Year > MaxYear) {
		return Metadata{}, invalidMetadata("year must be 0 (unknown) or between %d and %d", MinYear, MaxYear)
	}
	return m, nil
}
```

`internal/catalog/repo.go` (sửa — 2 cặp thay thế):

1. Thay:

```go
var (
	ErrNotFound          = errors.New("video not found")
	ErrInvalidTransition = errors.New("invalid status transition")
)

type Video struct {
```

   bằng:

```go
var (
	ErrNotFound          = errors.New("video not found")
	ErrInvalidTransition = errors.New("invalid status transition")
	// ErrVideoBusy: video đang queued/processing nên chưa xoá được (transcoder có thể đang ghi hls/{id}/).
	ErrVideoBusy = errors.New("video is being processed")
)

type Video struct {
```

2. Thay:

```go
	return nil
}

// SearchParams: tham số catalog / tìm kiếm cho người xem. Chỉ video ready được trả về.
type SearchParams struct {
	Query  string // đã trim; rỗng = duyệt catalog, mới nhất trước
```

   bằng:

```go
	return nil
}

// UpdateMetadata ghi đè title, description, genres, year (m đã qua NormalizeMetadata) và trả video sau khi sửa.
// Trigger videos_search_update tính lại cột search khi title/description đổi.
func (r *Repo) UpdateMetadata(ctx context.Context, id string, m Metadata) (Video, error) {
	if m.Genres == nil {
		m.Genres = []string{}
	}
	return scanVideo(r.pool.QueryRow(ctx, `UPDATE videos SET title = $2, description = $3, genres = $4,
		year = NULLIF($5, 0), updated_at = now() WHERE id = $1 RETURNING `+videoColumns,
		id, m.Title, m.Description, m.Genres, m.Year))
}

// Delete xoá video (renditions, subtitles, watch_progress xoá theo ON DELETE CASCADE) và trả dòng đã xoá để
// dọn object. Video đang queued/processing → ErrVideoBusy; không tồn tại → ErrNotFound. Khoá dòng như
// transition nên không chen giữa được với retry hay cập nhật trạng thái từ transcoder.
func (r *Repo) Delete(ctx context.Context, id string) (Video, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Video{}, err
	}
	defer tx.Rollback(ctx)
	v, err := scanVideo(tx.QueryRow(ctx, `SELECT `+videoColumns+` FROM videos WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return Video{}, err
	}
	if v.Status == StatusQueued || v.Status == StatusProcessing {
		return v, fmt.Errorf("%w: video is %s", ErrVideoBusy, v.Status)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM videos WHERE id = $1`, id); err != nil {
		return Video{}, fmt.Errorf("delete video: %w", err)
	}
	return v, tx.Commit(ctx)
}

// SearchParams: tham số catalog / tìm kiếm cho người xem. Chỉ video ready được trả về.
type SearchParams struct {
	Query  string // đã trim; rỗng = duyệt catalog, mới nhất trước
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal; go vet ./internal/catalog/ && go test -count=1 ./internal/catalog/ -run 'TestNormalizeMetadata|TestRepoUpdateMetadata|TestRepoDelete' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: TestNormalizeMetadata`, `--- PASS: TestRepoUpdateMetadata`, `--- PASS: TestRepoDelete`, `ok  streamfilm/internal/catalog` (Postgres container ~10 giây). `TestRepoUpdateMetadata` chứng minh trigger tính lại `search` (tìm "ha canh" ra tiêu đề mới, tiêu đề cũ không còn khớp); `TestRepoDelete` chứng minh cascade `renditions`/`subtitles`/`watch_progress` và từ chối `queued`.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/metadata.go internal/catalog/metadata_test.go internal/catalog/repo.go internal/catalog/admin_repo_test.go
git commit -m "feat(catalog): metadata normalization, UpdateMetadata and Delete with busy guard" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `PATCH /api/admin/videos/{id}` và phụ đề trong chi tiết admin

**Files:**
- Modify: `internal/catalog/handler_test.go`, `cmd/api/router_test.go`, `internal/catalog/handler.go`

**Interfaces:**
- Consumes: `NormalizeMetadata`, `Repo.UpdateMetadata`, `Repo.Subtitles` (M5).
- Produces (`internal/catalog`):
  ```go
  type AdminStore interface {
  	List(ctx context.Context, f ListFilter) ([]Video, error)
  	Get(ctx context.Context, id string) (Video, error)
  	Renditions(ctx context.Context, id string) ([]Rendition, error)
  	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
  	UpdateMetadata(ctx context.Context, id string, m Metadata) (Video, error)
  }
  type PatchInput struct { Title, Description *string; Genres *[]string; Year *int } // tag json: title, description, genres, year
  // VideoJSON thêm: Subtitles []AdminSubtitleJSON `json:"subtitles,omitzero"`
  // Route mới: r.Patch("/api/admin/videos/{id}", h.patch)
  ```
  `*catalog.Repo` đã thoả `AdminStore` (Task 2 + M5).

- [ ] **Step 1: Viết test (thất bại trước)**

`cmd/api/router_test.go` ở task này chỉ thêm `UpdateMetadata` cho `emptyStore` (để `emptyStore` vẫn là `AdminStore`); các ca phân quyền PATCH/DELETE thêm ở Task 4.

`internal/catalog/handler_test.go` (sửa — 4 cặp thay thế):

1. Thay:

```go
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeAdminStore struct {
	videos []Video
	filter ListFilter
}

func (f *fakeAdminStore) List(_ context.Context, lf ListFilter) ([]Video, error) {
```

   bằng:

```go
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeAdminStore struct {
	videos  []Video
	filter  ListFilter
	subs    []Subtitle
	updated *Metadata // tham số của lần UpdateMetadata gần nhất
}

func (f *fakeAdminStore) List(_ context.Context, lf ListFilter) ([]Video, error) {
```

2. Thay:

```go
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

func newAdminRouter(store AdminStore) http.Handler {
	r := chi.NewRouter()
	(&AdminHandler{Videos: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
```

   bằng:

```go
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

func (f *fakeAdminStore) Subtitles(context.Context, string) ([]Subtitle, error) { return f.subs, nil }

func (f *fakeAdminStore) UpdateMetadata(_ context.Context, id string, m Metadata) (Video, error) {
	for i, v := range f.videos {
		if v.ID == id {
			f.updated = &m
			v.Title, v.Description, v.Genres, v.Year = m.Title, m.Description, m.Genres, m.Year
			f.videos[i] = v
			return v, nil
		}
	}
	return Video{}, ErrNotFound
}

func newAdminRouter(store AdminStore) http.Handler {
	r := chi.NewRouter()
	(&AdminHandler{Videos: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
```

3. Thay:

```go
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusReady || v.DurationS != 30 || len(v.Renditions) != 1 || v.Renditions[0].Height != 720 {
		t.Errorf("video = %+v", v)
	}
	if rec := get(h, "/api/admin/videos/"+idX); rec.Code != http.StatusNotFound {
```

   bằng:

```go
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusReady || v.DurationS != 30 || len(v.Renditions) != 1 || v.Renditions[0].Height != 720 ||
		v.Subtitles == nil || len(v.Subtitles) != 0 {
		t.Errorf("video = %+v", v)
	}
	if rec := get(h, "/api/admin/videos/"+idX); rec.Code != http.StatusNotFound {
```

4. Thay:

```go
		t.Errorf("bad id: code = %d", rec.Code)
	}
}
```

   bằng:

```go
		t.Errorf("bad id: code = %d", rec.Code)
	}
}

func TestAdminGetSubtitles(t *testing.T) {
	store := &fakeAdminStore{videos: []Video{{ID: idA, Title: "A", Status: StatusReady}},
		subs: []Subtitle{{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}}}
	rec := get(newAdminRouter(store), "/api/admin/videos/"+idA)
	var v VideoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Subtitles) != 1 || v.Subtitles[0] != (AdminSubtitleJSON{Lang: "vi", Label: "Tiếng Việt", VTTKey: idA + "/subs/vi.vtt"}) {
		t.Errorf("subtitles = %+v", v.Subtitles)
	}
}

func patch(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

func TestAdminPatch(t *testing.T) {
	store := &fakeAdminStore{videos: []Video{{ID: idA, Title: "Cũ", Description: "mô tả", Genres: []string{"drama"},
		Year: 2020, Status: StatusProcessing}}}
	h := newAdminRouter(store)

	// Chỉ đổi title: các field khác giữ nguyên; title được trim; trạng thái không ảnh hưởng.
	rec := patch(h, "/api/admin/videos/"+strings.ToUpper(idA), `{"title":"  Mới  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	var v VideoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Title != "Mới" || v.Description != "mô tả" || v.Year != 2020 || !slices.Equal(v.Genres, []string{"drama"}) ||
		v.Status != StatusProcessing {
		t.Errorf("patched = %+v", v)
	}

	// year 0 xoá năm, genres [] xoá thể loại.
	rec = patch(h, "/api/admin/videos/"+idA, `{"year":0,"genres":[],"description":""}`)
	if rec.Code != http.StatusOK || store.updated.Year != 0 || len(store.updated.Genres) != 0 || store.updated.Description != "" ||
		store.updated.Title != "Mới" {
		t.Errorf("code = %d updated = %+v", rec.Code, store.updated)
	}

	cases := []struct {
		path, body string
		want       int
	}{
		{"/api/admin/videos/" + idA, `{}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"title":"  "}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"year":1500}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"status":"ready"}`, http.StatusBadRequest},
		{"/api/admin/videos/" + idA, `{"title":`, http.StatusBadRequest},
		{"/api/admin/videos/" + idX, `{"title":"A"}`, http.StatusNotFound},
		{"/api/admin/videos/xyz", `{"title":"A"}`, http.StatusNotFound},
	}
	for _, c := range cases {
		if rec := patch(h, c.path, c.body); rec.Code != c.want {
			t.Errorf("PATCH %s %s: code = %d, want %d (%s)", c.path, c.body, rec.Code, c.want, rec.Body)
		}
	}
}
```

`cmd/api/router_test.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
func (emptyStore) Subtitles(context.Context, string) ([]catalog.Subtitle, error)   { return nil, nil }
func (emptyStore) ReadyByIDs(context.Context, []string) ([]catalog.Video, error)   { return nil, nil }
func (emptyStore) ResumeAt(context.Context, string, string) (int, error)           { return 0, nil }
func (emptyStore) ContinueWatching(context.Context, string, int) ([]catalog.WatchEntry, error) {
	return nil, nil
}
```

   bằng:

```go
func (emptyStore) Subtitles(context.Context, string) ([]catalog.Subtitle, error)   { return nil, nil }
func (emptyStore) ReadyByIDs(context.Context, []string) ([]catalog.Video, error)   { return nil, nil }
func (emptyStore) ResumeAt(context.Context, string, string) (int, error)           { return 0, nil }
func (emptyStore) UpdateMetadata(context.Context, string, catalog.Metadata) (catalog.Video, error) {
	return catalog.Video{}, catalog.ErrNotFound
}
func (emptyStore) ContinueWatching(context.Context, string, int) ([]catalog.WatchEntry, error) {
	return nil, nil
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/catalog/ -run 'TestAdmin' ; go vet ./cmd/api/`
Expected: `internal/catalog` FAIL biên dịch — `v.Subtitles undefined (type VideoJSON has no field or method Subtitles)` (test dùng `fakeAdminStore` có sẵn `Subtitles`/`UpdateMetadata`; route PATCH chưa có); `go vet ./cmd/api/` xanh (method thừa của `emptyStore` không lỗi).

- [ ] **Step 3: Cài đặt**

`internal/catalog/handler.go` (sửa — 4 cặp thay thế):

1. Thay:

```go
	List(ctx context.Context, f ListFilter) ([]Video, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
}

// AdminHandler: endpoint đọc cho admin. M2 chưa có auth; M3 thêm middleware role admin.
type AdminHandler struct {
	Videos AdminStore
	Log    *slog.Logger
```

   bằng:

```go
	List(ctx context.Context, f ListFilter) ([]Video, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
	UpdateMetadata(ctx context.Context, id string, m Metadata) (Video, error)
}

// AdminHandler: danh sách, chi tiết (M2) và sửa metadata (M7) cho admin; router bọc auth.RequireAdmin.
type AdminHandler struct {
	Videos AdminStore
	Log    *slog.Logger
```

2. Thay:

```go
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Renditions  []Rendition `json:"renditions,omitempty"`
}

func toJSON(v Video) VideoJSON {
```

   bằng:

```go
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Renditions  []Rendition `json:"renditions,omitempty"`
	// Subtitles chỉ có ở GET /api/admin/videos/{id} (omitzero: nil ở danh sách thì bỏ, [] khi chưa có phụ đề vẫn ghi).
	Subtitles []AdminSubtitleJSON `json:"subtitles,omitzero"`
}

func toJSON(v Video) VideoJSON {
```

3. Thay:

```go
func (h *AdminHandler) Routes(r chi.Router) {
	r.Get("/api/admin/videos", h.list)
	r.Get("/api/admin/videos/{id}", h.get)
}

func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
```

   bằng:

```go
func (h *AdminHandler) Routes(r chi.Router) {
	r.Get("/api/admin/videos", h.list)
	r.Get("/api/admin/videos/{id}", h.get)
	r.Patch("/api/admin/videos/{id}", h.patch)
}

func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
```

4. Thay:

```go
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toJSON(v)
	out.Renditions = rs
	httpx.WriteJSON(w, http.StatusOK, out)
}
```

   bằng:

```go
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	subs, err := h.Videos.Subtitles(r.Context(), id)
	if err != nil {
		h.Log.ErrorContext(r.Context(), "get subtitles", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toJSON(v)
	out.Renditions = rs
	out.Subtitles = make([]AdminSubtitleJSON, 0, len(subs))
	for _, s := range subs {
		out.Subtitles = append(out.Subtitles, AdminSubtitleJSON{Lang: s.Lang, Label: s.Label, VTTKey: s.VTTKey})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// PatchInput: field bỏ trống (không có trong JSON) giữ nguyên; "year": 0 xoá năm; "genres": [] xoá thể loại.
type PatchInput struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Genres      *[]string `json:"genres"`
	Year        *int      `json:"year"`
}

// patch sửa metadata ở mọi trạng thái (kể cả đang transcode: chỉ đụng các cột metadata).
// Đọc rồi ghi đè cả bốn cột: hai admin sửa cùng lúc thì bản ghi sau thắng.
func (h *AdminHandler) patch(w http.ResponseWriter, r *http.Request) {
	parsed, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	id := parsed.String()
	var in PatchInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if in.Title == nil && in.Description == nil && in.Genres == nil && in.Year == nil {
		httpx.WriteError(w, http.StatusBadRequest, "nothing to update")
		return
	}
	cur, err := h.Videos.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.ErrorContext(r.Context(), "get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	m := Metadata{Title: cur.Title, Description: cur.Description, Genres: cur.Genres, Year: cur.Year}
	if in.Title != nil {
		m.Title = *in.Title
	}
	if in.Description != nil {
		m.Description = *in.Description
	}
	if in.Genres != nil {
		m.Genres = *in.Genres
	}
	if in.Year != nil {
		m.Year = *in.Year
	}
	m, err = NormalizeMetadata(m)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	v, err := h.Videos.UpdateMetadata(r.Context(), id, m)
	if errors.Is(err, ErrNotFound) { // bị xoá giữa Get và UPDATE
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.ErrorContext(r.Context(), "update video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	h.Log.InfoContext(r.Context(), "video metadata updated", "video_id", id)
	httpx.WriteJSON(w, http.StatusOK, toJSON(v))
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l .; go vet ./... && go test -short ./internal/catalog/ ./cmd/api/`
Expected: `ok  streamfilm/internal/catalog`, `ok  streamfilm/cmd/api`.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/handler.go internal/catalog/handler_test.go cmd/api/router_test.go
git commit -m "feat(catalog): PATCH admin video metadata and subtitles in admin detail" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: `DELETE /api/admin/videos/{id}` và tạo video dùng `NormalizeMetadata`

**Files:**
- Create: `internal/upload/delete_test.go`
- Modify: `internal/upload/service_test.go`, `cmd/api/router_test.go`, `internal/upload/service.go`, `internal/upload/handler.go`, `cmd/api/main.go`

**Interfaces:**
- Consumes: `Repo.Delete`, `catalog.ErrVideoBusy`, `storage.Store.DeletePrefix` (M2), `AbortMultipartUpload`.
- Produces (`internal/upload`):
  ```go
  type Videos interface { /* Create, Get, Transition, RevertQueued như cũ */
  	Delete(ctx context.Context, id string) (catalog.Video, error)
  }
  type Objects interface { /* CreateMultipartUpload, PresignUploadPart, CompleteMultipartUpload, AbortMultipartUpload, Exists như cũ */
  	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
  }
  // Service thêm field: BucketHLS string
  func (s *Service) Delete(ctx context.Context, id string) error // catalog.ErrNotFound | catalog.ErrVideoBusy
  // Route mới: r.Delete("/api/admin/videos/{id}", h.delete) → 204; ErrVideoBusy → 409
  ```
  `*catalog.Repo` thoả `Videos`, `*storage.Store` thoả `Objects`. `videoID()` trả UUID dạng chuẩn (chữ thường) cho complete/retry/delete.

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/upload/service_test.go` (sửa — 5 cặp thay thế):

1. Thay:

```go
	return nil
}

func (f *fakeVideos) setStatus(id string, s catalog.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
```

   bằng:

```go
	return nil
}

func (f *fakeVideos) Delete(_ context.Context, id string) (catalog.Video, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[id]
	if !ok {
		return catalog.Video{}, catalog.ErrNotFound
	}
	if v.Status == catalog.StatusQueued || v.Status == catalog.StatusProcessing {
		return v, fmt.Errorf("%w: video is %s", catalog.ErrVideoBusy, v.Status)
	}
	delete(f.m, id)
	return v, nil
}

func (f *fakeVideos) setStatus(id string, s catalog.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
```

2. Thay:

```go
	exists        bool
	completeCalls int
	created       []string
}

func (f *fakeObjects) CreateMultipartUpload(_ context.Context, bucket, key, contentType string) (string, error) {
```

   bằng:

```go
	exists        bool
	completeCalls int
	created       []string
	deleted       []string // bucket/prefix của DeletePrefix
	deleteErr     error
}

func (f *fakeObjects) CreateMultipartUpload(_ context.Context, bucket, key, contentType string) (string, error) {
```

3. Thay:

```go

func (f *fakeObjects) Exists(context.Context, string, string) (bool, error) { return f.exists, nil }

type published struct {
	topic string
	ev    events.Event
```

   bằng:

```go

func (f *fakeObjects) Exists(context.Context, string, string) (bool, error) { return f.exists, nil }

func (f *fakeObjects) DeletePrefix(_ context.Context, bucket, prefix string) (int, error) {
	f.deleted = append(f.deleted, bucket+"/"+prefix)
	return 1, f.deleteErr
}

type published struct {
	topic string
	ev    events.Event
```

4. Thay:

```go
func newService() (*Service, *fakeVideos, *fakeObjects, *fakePub) {
	vids, objs, pub := newFakeVideos(), &fakeObjects{}, &fakePub{}
	n := 0
	svc := &Service{Videos: vids, Objects: objs, Pub: pub, BucketRaw: "raw",
		Now: func() time.Time { return testNow },
		NewID: func() string {
			n++
```

   bằng:

```go
func newService() (*Service, *fakeVideos, *fakeObjects, *fakePub) {
	vids, objs, pub := newFakeVideos(), &fakeObjects{}, &fakePub{}
	n := 0
	svc := &Service{Videos: vids, Objects: objs, Pub: pub, BucketRaw: "raw", BucketHLS: "hls",
		Now: func() time.Time { return testNow },
		NewID: func() string {
			n++
```

5. Thay:

```go
func TestCreateValidates(t *testing.T) {
	svc, _, _, _ := newService()
	cases := map[string]CreateInput{
		"no title":  {Title: "  ", Filename: "a.mp4", SizeBytes: 10},
		"zero size": {Title: "A", Filename: "a.mp4"},
		"too big":   {Title: "A", Filename: "a.mp4", SizeBytes: MaxSizeBytes + 1},
		"bad ext":   {Title: "A", Filename: "a.avi", SizeBytes: 10},
	}
	for name, in := range cases {
		if _, err := svc.Create(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
```

   bằng:

```go
func TestCreateValidates(t *testing.T) {
	svc, _, _, _ := newService()
	cases := map[string]CreateInput{
		"no title":   {Title: "  ", Filename: "a.mp4", SizeBytes: 10},
		"zero size":  {Title: "A", Filename: "a.mp4"},
		"too big":    {Title: "A", Filename: "a.mp4", SizeBytes: MaxSizeBytes + 1},
		"bad ext":    {Title: "A", Filename: "a.avi", SizeBytes: 10},
		"bad year":   {Title: "A", Filename: "a.mp4", SizeBytes: 10, Year: 99999},
		"long title": {Title: strings.Repeat("a", catalog.MaxTitleLen+1), Filename: "a.mp4", SizeBytes: 10},
	}
	for name, in := range cases {
		if _, err := svc.Create(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
```

`internal/upload/delete_test.go` (mới, toàn văn):

```go
package upload

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"streamfilm/internal/catalog"
)

func TestDeleteUploadingAbortsAndCleans(t *testing.T) {
	svc, vids, objs, _ := newService()
	id := createOne(t, svc)
	if err := svc.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := vids.Get(context.Background(), id); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("video still exists: %v", err)
	}
	if !slices.Equal(objs.aborted, []string{"raw/" + id + "/source.mp4|up-1"}) {
		t.Errorf("aborted = %v", objs.aborted)
	}
	if !slices.Equal(objs.deleted, []string{"raw/" + id + "/", "hls/" + id + "/"}) {
		t.Errorf("deleted = %v", objs.deleted)
	}
}

func TestDeleteReadyAndFailed(t *testing.T) {
	for _, st := range []catalog.Status{catalog.StatusReady, catalog.StatusFailed} {
		svc, vids, objs, _ := newService()
		id := createOne(t, svc)
		vids.setStatus(id, st)
		objs.deleteErr = errors.New("minio down") // lỗi storage chỉ log, xoá vẫn thành công
		if err := svc.Delete(context.Background(), id); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if len(objs.aborted) != 0 {
			t.Errorf("%s: must not abort multipart: %v", st, objs.aborted)
		}
		if len(objs.deleted) != 2 {
			t.Errorf("%s: deleted = %v", st, objs.deleted)
		}
	}
}

func TestDeleteBusyAndUnknown(t *testing.T) {
	svc, vids, objs, _ := newService()
	id := createOne(t, svc)
	for _, st := range []catalog.Status{catalog.StatusQueued, catalog.StatusProcessing} {
		vids.setStatus(id, st)
		if err := svc.Delete(context.Background(), id); !errors.Is(err, catalog.ErrVideoBusy) {
			t.Errorf("%s: err = %v, want ErrVideoBusy", st, err)
		}
	}
	if len(objs.deleted) != 0 || len(objs.aborted) != 0 {
		t.Errorf("busy video must not touch storage: deleted=%v aborted=%v", objs.deleted, objs.aborted)
	}
	if err := svc.Delete(context.Background(), "00000000-0000-0000-0000-00000000ffff"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
}

func TestDeleteEndpoint(t *testing.T) {
	svc, vids, _, _ := newService()
	h := newRouter(svc)
	id := createOne(t, svc)
	vids.setStatus(id, catalog.StatusProcessing)
	if rec := do(h, http.MethodDelete, "/api/admin/videos/"+id, ""); rec.Code != http.StatusConflict {
		t.Errorf("processing: code = %d body = %s", rec.Code, rec.Body)
	}
	vids.setStatus(id, catalog.StatusFailed)
	if rec := do(h, http.MethodDelete, "/api/admin/videos/"+strings.ToUpper(id), ""); rec.Code != http.StatusNoContent {
		t.Errorf("failed: code = %d body = %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodDelete, "/api/admin/videos/"+id, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted twice: code = %d", rec.Code)
	}
	if rec := do(h, http.MethodDelete, "/api/admin/videos/not-a-uuid", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
}
```

`cmd/api/router_test.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", admin, 400}, // không phải multipart
		{"GET", "/api/admin/videos", admin, 200},
		{"GET", "/api/admin/videos", "not-a-jwt", 401},
		{"GET", "/api/me", "", 401},
		{"GET", "/api/me/continue-watching", "", 401},
```

   bằng:

```go
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", viewer, 403},
		{"POST", "/api/admin/videos/11111111-1111-1111-1111-111111111111/subtitles", admin, 400}, // không phải multipart
		{"GET", "/api/admin/videos", admin, 200},
		{"PATCH", "/api/admin/videos/11111111-1111-1111-1111-111111111111", "", 401},
		{"PATCH", "/api/admin/videos/11111111-1111-1111-1111-111111111111", viewer, 403},
		{"PATCH", "/api/admin/videos/11111111-1111-1111-1111-111111111111", admin, 400}, // body rỗng
		{"DELETE", "/api/admin/videos/11111111-1111-1111-1111-111111111111", "", 401},
		{"DELETE", "/api/admin/videos/11111111-1111-1111-1111-111111111111", viewer, 403},
		{"GET", "/api/admin/videos", "not-a-jwt", 401},
		{"GET", "/api/me", "", 401},
		{"GET", "/api/me/continue-watching", "", 401},
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/upload/`
Expected: FAIL biên dịch — `svc.Delete undefined`, `unknown field BucketHLS in struct literal`.

- [ ] **Step 3: Cài đặt**

`internal/upload/service.go` (sửa — 6 cặp thay thế):

1. Thay:

```go
	Get(ctx context.Context, id string) (catalog.Video, error)
	Transition(ctx context.Context, id string, to catalog.Status) (catalog.Status, error)
	RevertQueued(ctx context.Context, id string, to catalog.Status) error
}

type Objects interface {
```

   bằng:

```go
	Get(ctx context.Context, id string) (catalog.Video, error)
	Transition(ctx context.Context, id string, to catalog.Status) (catalog.Status, error)
	RevertQueued(ctx context.Context, id string, to catalog.Status) error
	Delete(ctx context.Context, id string) (catalog.Video, error)
}

type Objects interface {
```

2. Thay:

```go
	CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int, wantSize, partSize, maxSize int64) error
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
	Exists(ctx context.Context, bucket, key string) (bool, error)
}

type Publisher interface {
```

   bằng:

```go
	CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int, wantSize, partSize, maxSize int64) error
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
	Exists(ctx context.Context, bucket, key string) (bool, error)
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
}

type Publisher interface {
```

3. Thay:

```go
	Objects   Objects
	Pub       Publisher
	BucketRaw string
	Now       func() time.Time
	NewID     func() string
	Log       *slog.Logger // tuỳ chọn; nil = slog.Default()
```

   bằng:

```go
	Objects   Objects
	Pub       Publisher
	BucketRaw string
	BucketHLS string // M7: Delete dọn hls/{id}/
	Now       func() time.Time
	NewID     func() string
	Log       *slog.Logger // tuỳ chọn; nil = slog.Default()
```

4. Thay:

```go

// Create tạo video (uploading), mở multipart upload và trả presigned URL cho từng part.
func (s *Service) Create(ctx context.Context, in CreateInput) (CreateOutput, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return CreateOutput{}, invalid("title is required")
	}
	if in.SizeBytes <= 0 || in.SizeBytes > MaxSizeBytes {
		return CreateOutput{}, invalid("size_bytes must be between 1 and %d", MaxSizeBytes)
```

   bằng:

```go

// Create tạo video (uploading), mở multipart upload và trả presigned URL cho từng part.
func (s *Service) Create(ctx context.Context, in CreateInput) (CreateOutput, error) {
	meta, err := catalog.NormalizeMetadata(catalog.Metadata{Title: in.Title, Description: in.Description,
		Genres: in.Genres, Year: in.Year})
	if err != nil {
		return CreateOutput{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if in.SizeBytes <= 0 || in.SizeBytes > MaxSizeBytes {
		return CreateOutput{}, invalid("size_bytes must be between 1 and %d", MaxSizeBytes)
```

5. Thay:

```go
		}
		parts[i] = Part{PartNumber: num, URL: url}
	}
	err = s.Videos.Create(ctx, catalog.Video{ID: id, Title: in.Title, Description: in.Description,
		Genres: in.Genres, Year: in.Year, RawKey: key, UploadID: uploadID, SizeBytes: in.SizeBytes})
	if err != nil {
		// Không để multipart upload mồ côi; lỗi huỷ chỉ log (cleaner/lifecycle sẽ dọn sau).
		if aerr := s.Objects.AbortMultipartUpload(context.WithoutCancel(ctx), s.BucketRaw, key, uploadID); aerr != nil {
```

   bằng:

```go
		}
		parts[i] = Part{PartNumber: num, URL: url}
	}
	err = s.Videos.Create(ctx, catalog.Video{ID: id, Title: meta.Title, Description: meta.Description,
		Genres: meta.Genres, Year: meta.Year, RawKey: key, UploadID: uploadID, SizeBytes: in.SizeBytes})
	if err != nil {
		// Không để multipart upload mồ côi; lỗi huỷ chỉ log (cleaner/lifecycle sẽ dọn sau).
		if aerr := s.Objects.AbortMultipartUpload(context.WithoutCancel(ctx), s.BucketRaw, key, uploadID); aerr != nil {
```

6. Thay:

```go
	v.Status = catalog.StatusQueued
	return v, nil
}
```

   bằng:

```go
	v.Status = catalog.StatusQueued
	return v, nil
}

// Delete xoá video khỏi DB (cascade rendition, phụ đề, tiến độ xem) rồi dọn storage: huỷ multipart upload dở,
// xoá raw/{id}/ và hls/{id}/. Video queued/processing → catalog.ErrVideoBusy (HTTP 409): transcoder có thể
// đang ghi hls/{id}/, xoá lúc đó sẽ để lại HLS mồ côi. Lỗi storage chỉ log (DB là nguồn sự thật, ADR 0020).
func (s *Service) Delete(ctx context.Context, id string) error {
	v, err := s.Videos.Delete(ctx, id)
	if err != nil {
		return err
	}
	bg := context.WithoutCancel(ctx) // client ngắt kết nối cũng dọn nốt
	log := s.logger().With("video_id", id)
	if v.Status == catalog.StatusUploading && v.UploadID != "" {
		if err := s.Objects.AbortMultipartUpload(bg, s.BucketRaw, v.RawKey, v.UploadID); err != nil {
			log.WarnContext(ctx, "abort multipart after delete failed", "err", err)
		}
	}
	removed := 0
	for _, bucket := range []string{s.BucketRaw, s.BucketHLS} {
		n, err := s.Objects.DeletePrefix(bg, bucket, id+"/")
		removed += n
		if err != nil {
			log.WarnContext(ctx, "delete objects after delete failed", "bucket", bucket, "err", err)
		}
	}
	log.InfoContext(ctx, "video deleted", "status", v.Status, "objects_removed", removed)
	return nil
}
```

`internal/upload/handler.go` (sửa — 4 cặp thay thế):

1. Thay:

```go
	"streamfilm/internal/httpx"
)

// Handler gắn các endpoint upload của admin.
// M2: CHƯA có auth. M3 bọc nhóm /api/admin bằng middleware yêu cầu role admin.
type Handler struct {
	Svc *Service
	Log *slog.Logger
```

   bằng:

```go
	"streamfilm/internal/httpx"
)

// Handler gắn các endpoint vòng đời video của admin: tạo + presigned upload, complete, retry (M2), xoá (M7).
// Router bọc nhóm /api/admin bằng auth.RequireAdmin (M3).
type Handler struct {
	Svc *Service
	Log *slog.Logger
```

2. Thay:

```go
	r.Post("/api/admin/videos", h.create)
	r.Post("/api/admin/videos/{id}/complete", h.complete)
	r.Post("/api/admin/videos/{id}/retry", h.retry)
}

type statusResponse struct {
```

   bằng:

```go
	r.Post("/api/admin/videos", h.create)
	r.Post("/api/admin/videos/{id}/complete", h.complete)
	r.Post("/api/admin/videos/{id}/retry", h.retry)
	r.Delete("/api/admin/videos/{id}", h.delete)
}

type statusResponse struct {
```

3. Thay:

```go
	httpx.WriteJSON(w, http.StatusOK, statusResponse{ID: v.ID, Status: v.Status})
}

func videoID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return "", false
	}
	return id, true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
```

   bằng:

```go
	httpx.WriteJSON(w, http.StatusOK, statusResponse{ID: v.ID, Status: v.Status})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := videoID(w, r)
	if !ok {
		return
	}
	if err := h.Svc.Delete(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// videoID trả id dạng chuẩn (chữ thường) để khớp key object {id}/ và cột uuid.
func videoID(w http.ResponseWriter, r *http.Request) (string, bool) {
	parsed, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return "", false
	}
	return parsed.String(), true
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
```

4. Thay:

```go
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, catalog.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "video not found")
	case errors.Is(err, catalog.ErrInvalidTransition):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrKafkaUnavailable):
		h.Log.Warn("publish failed", "request_id", middleware.GetReqID(r.Context()), "err", err)
```

   bằng:

```go
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, catalog.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "video not found")
	case errors.Is(err, catalog.ErrInvalidTransition), errors.Is(err, catalog.ErrVideoBusy):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrKafkaUnavailable):
		h.Log.Warn("publish failed", "request_id", middleware.GetReqID(r.Context()), "err", err)
```

`cmd/api/main.go` (sửa — 1 cặp thay thế):

1. Thay:

```go
	progress := playback.NewRepo(pool)
	// playback.events: publish ở nền, Kafka chết thì bỏ sự kiện, không làm hỏng việc xem (spec §7).
	playbackEvents := playback.NewBestEffort(producer, playback.PublishTimeout, playback.MaxInFlight, log)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw,
		Now: time.Now, NewID: uuid.NewString, Log: log}

	authSvc := &auth.Service{Users: auth.NewRepo(pool), Tokens: tokens, NewID: uuid.NewString}
```

   bằng:

```go
	progress := playback.NewRepo(pool)
	// playback.events: publish ở nền, Kafka chết thì bỏ sự kiện, không làm hỏng việc xem (spec §7).
	playbackEvents := playback.NewBestEffort(producer, playback.PublishTimeout, playback.MaxInFlight, log)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw, BucketHLS: cfg.BucketHLS,
		Now: time.Now, NewID: uuid.NewString, Log: log}

	authSvc := &auth.Service{Users: auth.NewRepo(pool), Tokens: tokens, NewID: uuid.NewString}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l .; go vet ./... && go test -short ./...`
Expected: mọi package `ok`; trong đó `TestDeleteUploadingAbortsAndCleans`, `TestDeleteReadyAndFailed` (lỗi MinIO vẫn trả nil), `TestDeleteBusyAndUnknown`, `TestDeleteEndpoint` (409 → 204 với id chữ hoa → 404), `TestCreateValidates` (thêm `bad year`, `long title`), `TestRouterAuthz` (PATCH/DELETE: 401 không cookie, 403 viewer, PATCH admin body rỗng 400).

- [ ] **Step 5: Commit**

```bash
git add internal/upload cmd/api
git commit -m "feat(upload): DELETE admin video with storage cleanup; validate metadata on create" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: nginx: header bảo mật; MinIO: CORS giới hạn origin; `S3_PUBLIC_ENDPOINT` từ `.env`

**Files:**
- Create: `deploy/nginx/snippets/security-headers.conf`
- Modify: `internal/signing/nginx_test.go`, `deploy/nginx/templates/default.conf.template`, `deploy/docker-compose.yml`, `.env.example`

**Interfaces:**
- Produces: `deploy/nginx/snippets/security-headers.conf` mount tại `/etc/nginx/snippets/security-headers.conf`; biến `.env` `MINIO_CORS_ALLOW_ORIGIN`.
- `internal/signing/nginx_test.go`: biến gói `securityHeaders map[string]string` (khớp snippet), container nginx test chép thêm snippet.

- [ ] **Step 1: Viết test (thất bại trước)**

Test copy snippet vào container nên phải tạo file snippet (rỗng cũng được) để thấy lỗi đúng chỗ: `mkdir -p deploy/nginx/snippets && : > deploy/nginx/snippets/security-headers.conf`.

`internal/signing/nginx_test.go` (sửa — 4 cặp thay thế):

1. Thay:

```go
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/templates/default.conf.template",
			ContainerFilePath: "/etc/nginx/templates/default.conf.template", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("80/tcp")),
	)
```

   bằng:

```go
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/templates/default.conf.template",
			ContainerFilePath: "/etc/nginx/templates/default.conf.template", FileMode: 0o644,
		}, testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/snippets/security-headers.conf",
			ContainerFilePath: "/etc/nginx/snippets/security-headers.conf", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("80/tcp")),
	)
```

2. Thay:

```go
		{"spa root", "/", 200, ""},
		{"spa fallback route", "/watch/" + testVID, 200, ""},
		{"missing asset", "/assets/missing.js", 404, ""},
	}
	for _, c := range cases {
		resp, err := http.Get(endpoint + c.path)
```

   bằng:

```go
		{"spa root", "/", 200, ""},
		{"spa fallback route", "/watch/" + testVID, 200, ""},
		{"missing asset", "/assets/missing.js", 404, ""},
		{"healthz", "/healthz", 200, "ok\n"},
	}
	for _, c := range cases {
		resp, err := http.Get(endpoint + c.path)
```

3. Thay:

```go
		if strings.HasPrefix(c.path, "/cdn/") && resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: thiếu CORS header (player cần đọc được mã 403)", c.name)
		}
	}

	// Fallback SPA trả đúng index.html (react-router xử lý route phía trình duyệt).
```

   bằng:

```go
		if strings.HasPrefix(c.path, "/cdn/") && resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: thiếu CORS header (player cần đọc được mã 403)", c.name)
		}
		// M7: mọi location (kể cả location có add_header riêng, và phản hồi 403/404) đều có header bảo mật.
		for h, want := range securityHeaders {
			if got := resp.Header.Get(h); got != want {
				t.Errorf("%s: %s = %q, want %q", c.name, h, got, want)
			}
		}
	}

	// Fallback SPA trả đúng index.html (react-router xử lý route phía trình duyệt).
```

4. Thay:

```go
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
```

   bằng:

```go
	}
}

// securityHeaders khớp deploy/nginx/snippets/security-headers.conf.
var securityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Content-Security-Policy": "frame-ancestors 'none'",
	"Referrer-Policy":         "same-origin",
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -count=1 ./internal/signing/ -run TestNginxSecureLinkParity 2>&1 | grep -c 'want "nosniff"'`
Expected: số > 0 (mọi case báo `X-Content-Type-Options = "", want "nosniff"` …), test FAIL.

- [ ] **Step 3: Cài đặt**

Ghi đè file snippet rỗng ở Step 1 bằng toàn văn dưới đây.

`deploy/nginx/snippets/security-headers.conf` (mới, toàn văn):

```nginx
# Header bảo mật cho mọi phản hồi của nginx (M7). nginx chỉ kế thừa add_header từ cấp trên khi location
# KHÔNG có add_header nào của riêng nó, nên file này được include ở cấp server VÀ trong từng location có
# add_header (kiểm tra bởi internal/signing/nginx_test.go). "always": gắn cả vào phản hồi 4xx/5xx.
add_header X-Content-Type-Options "nosniff" always;
add_header X-Frame-Options "DENY" always;
add_header Content-Security-Policy "frame-ancestors 'none'" always;
add_header Referrer-Policy "same-origin" always;
```

`deploy/nginx/templates/default.conf.template` (sửa — 6 cặp thay thế):

1. Thay:

```nginx
# Cổng vào duy nhất của trình duyệt (M4, ADR 0011): SPA người xem ở /, API ở /api/ (proxy tới api:8000),
# "CDN" giả lập ở /cdn/ (chỉ phục vụ HLS, poster, sprite, phụ đề qua URL có chữ ký, M3/M5). Content-Type
# lấy từ object MinIO (storage.ContentTypeFor: .m3u8, .ts, .jpg → image/jpeg, .vtt → text/vtt). Cùng origin nên cookie HttpOnly
# của API dùng được mà không cần CORS.
# Đây là template: lúc khởi động, image nginx chạy envsubst (chỉ với biến khớp NGINX_ENVSUBST_FILTER=^CDN_)
# rồi ghi ra /etc/nginx/conf.d/default.conf. Biến nginx ($sl_exp, $uri, ...) giữ nguyên.
proxy_cache_path /var/cache/nginx/hls levels=1:2 keys_zone=hls:10m max_size=1g inactive=7d use_temp_path=off;
```

   bằng:

```nginx
# Cổng vào duy nhất của trình duyệt (M4, ADR 0011): SPA người xem ở /, API ở /api/ (proxy tới api:8000),
# "CDN" giả lập ở /cdn/ (chỉ phục vụ HLS, poster, sprite, phụ đề qua URL có chữ ký, M3/M5). Content-Type
# lấy từ object MinIO (storage.ContentTypeFor: .m3u8, .ts, .jpg → image/jpeg, .vtt → text/vtt). Cùng origin nên cookie HttpOnly
# của API dùng được mà không cần CORS. Header bảo mật: deploy/nginx/snippets/security-headers.conf (M7).
# Đây là template: lúc khởi động, image nginx chạy envsubst (chỉ với biến khớp NGINX_ENVSUBST_FILTER=^CDN_)
# rồi ghi ra /etc/nginx/conf.d/default.conf. Biến nginx ($sl_exp, $uri, ...) giữ nguyên.
proxy_cache_path /var/cache/nginx/hls levels=1:2 keys_zone=hls:10m max_size=1g inactive=7d use_temp_path=off;
```

2. Thay:

```nginx

server {
    listen 80;

    location = /healthz { return 200 "ok\n"; }

```

   bằng:

```nginx

server {
    listen 80;
    include /etc/nginx/snippets/security-headers.conf;

    location = /healthz { return 200 "ok\n"; }

```

3. Thay:

```nginx
        secure_link $sl_sig,$sl_exp;
        secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";

        add_header Access-Control-Allow-Origin * always;
        add_header X-Cache-Status $upstream_cache_status always;

```

   bằng:

```nginx
        secure_link $sl_sig,$sl_exp;
        secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";

        include /etc/nginx/snippets/security-headers.conf;
        add_header Access-Control-Allow-Origin * always;
        add_header X-Cache-Status $upstream_cache_status always;

```

4. Thay:

```nginx

    # URL /cdn/ sai dạng (thiếu sig/exp/id): coi như chữ ký sai.
    location /cdn/ {
        add_header Access-Control-Allow-Origin * always;
        return 403;
    }
```

   bằng:

```nginx

    # URL /cdn/ sai dạng (thiếu sig/exp/id): coi như chữ ký sai.
    location /cdn/ {
        include /etc/nginx/snippets/security-headers.conf;
        add_header Access-Control-Allow-Origin * always;
        return 403;
    }
```

5. Thay:

```nginx
    # File build của Vite có hash trong tên: cache lâu dài.
    location /assets/ {
        root /usr/share/nginx/html;
        add_header Cache-Control "public, max-age=31536000, immutable";
        try_files $uri =404;
    }
```

   bằng:

```nginx
    # File build của Vite có hash trong tên: cache lâu dài.
    location /assets/ {
        root /usr/share/nginx/html;
        include /etc/nginx/snippets/security-headers.conf;
        add_header Cache-Control "public, max-age=31536000, immutable";
        try_files $uri =404;
    }
```

6. Thay:

```nginx
    # SPA: file tĩnh nếu có, còn lại trả index.html để react-router xử lý (/watch/{id}, /search, ...).
    location / {
        root /usr/share/nginx/html;
        add_header Cache-Control "no-cache";
        try_files $uri /index.html;
    }
```

   bằng:

```nginx
    # SPA: file tĩnh nếu có, còn lại trả index.html để react-router xử lý (/watch/{id}, /search, ...).
    location / {
        root /usr/share/nginx/html;
        include /etc/nginx/snippets/security-headers.conf;
        add_header Cache-Control "no-cache";
        try_files $uri /index.html;
    }
```

`deploy/docker-compose.yml` (sửa — 3 cặp thay thế):

1. Thay:

```yaml
    environment:
      MINIO_ROOT_USER: ${S3_ACCESS_KEY}
      MINIO_ROOT_PASSWORD: ${S3_SECRET_KEY}
    ports: ["127.0.0.1:9000:9000", "127.0.0.1:9001:9001"]
    volumes: [miniodata:/data]
    healthcheck:
```

   bằng:

```yaml
    environment:
      MINIO_ROOT_USER: ${S3_ACCESS_KEY}
      MINIO_ROOT_PASSWORD: ${S3_SECRET_KEY}
      # M7: trang admin PUT part thẳng từ trình duyệt (origin của SPA) lên presigned URL :9000 — cross-origin.
      # Mặc định MinIO cho mọi origin ("*"); giới hạn lại về SPA (nginx) và Vite dev server (ADR 0019).
      MINIO_API_CORS_ALLOW_ORIGIN: ${MINIO_CORS_ALLOW_ORIGIN:-http://localhost:8088,http://localhost:5173}
    ports: ["127.0.0.1:9000:9000", "127.0.0.1:9001:9001"]
    volumes: [miniodata:/data]
    healthcheck:
```

2. Thay:

```yaml
      NGINX_ENVSUBST_FILTER: "^CDN_"
    volumes:
      - ./nginx/templates:/etc/nginx/templates:ro
      - ../web/dev:/usr/share/nginx/html/dev:ro
    depends_on:
      minio: { condition: service_healthy }
```

   bằng:

```yaml
      NGINX_ENVSUBST_FILTER: "^CDN_"
    volumes:
      - ./nginx/templates:/etc/nginx/templates:ro
      - ./nginx/snippets:/etc/nginx/snippets:ro
      - ../web/dev:/usr/share/nginx/html/dev:ro
    depends_on:
      minio: { condition: service_healthy }
```

3. Thay:

```yaml
      KAFKA_BROKERS: kafka:9092
      S3_ENDPOINT: http://minio:9000
      # Presigned URL được ký cho địa chỉ mà máy host nhìn thấy (chữ ký gắn với Host).
      S3_PUBLIC_ENDPOINT: http://localhost:9000
      S3_REGION: ${S3_REGION}
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
```

   bằng:

```yaml
      KAFKA_BROKERS: kafka:9092
      S3_ENDPOINT: http://minio:9000
      # Presigned URL được ký cho địa chỉ mà máy host nhìn thấy (chữ ký gắn với Host).
      S3_PUBLIC_ENDPOINT: ${S3_PUBLIC_ENDPOINT:-http://localhost:9000}
      S3_REGION: ${S3_REGION}
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
```

`.env.example` (sửa — 1 cặp thay thế):

1. Thay:

```bash
CLICKHOUSE_DB=streamfilm
CLICKHOUSE_USER=streamfilm
CLICKHOUSE_PASSWORD=streamfilm
```

   bằng:

```bash
CLICKHOUSE_DB=streamfilm
CLICKHOUSE_USER=streamfilm
CLICKHOUSE_PASSWORD=streamfilm

# M7: origin được phép PUT part upload từ trình duyệt lên MinIO (CORS, phân tách bằng dấu phẩy). Phải chứa origin
# mở trang admin (CDN_BASE_URL) và Vite dev server. Đổi rồi chạy lại make up để MinIO nhận giá trị mới.
MINIO_CORS_ALLOW_ORIGIN=http://localhost:8088,http://localhost:5173
```

- [ ] **Step 4: Kiểm tra cấu hình nginx và MinIO CORS**

Run:
```bash
docker run --rm -e CDN_SIGNING_SECRET=abcdefabcdefabcdef -e NGINX_ENVSUBST_FILTER='^CDN_' \
  -v "$PWD/deploy/nginx/templates:/etc/nginx/templates:ro" -v "$PWD/deploy/nginx/snippets:/etc/nginx/snippets:ro" \
  --add-host minio:127.0.0.1 nginx:1.27-alpine nginx -t 2>&1 | tail -1
make env >/dev/null && docker compose -f deploy/docker-compose.yml --env-file .env config | grep -E 'MINIO_API_CORS|S3_PUBLIC_ENDPOINT'
```
Expected: `nginx: configuration file /etc/nginx/nginx.conf test is successful`; `MINIO_API_CORS_ALLOW_ORIGIN: http://localhost:8088,http://localhost:5173` và `S3_PUBLIC_ENDPOINT: http://localhost:9000` (`make env` thêm khoá `MINIO_CORS_ALLOW_ORIGIN` vào `.env`, in `added MINIO_CORS_ALLOW_ORIGIN to .env`).

- [ ] **Step 5: Chạy test**

Run: `go test -count=1 ./internal/signing/`
Expected: `ok  streamfilm/internal/signing` (mọi case, gồm `/healthz`, 403 của `/cdn/`, 404 của `/hls/`, SPA, `/api/` proxy, đều có đủ bốn header).

- [ ] **Step 6: Commit**

```bash
git add deploy .env.example internal/signing/nginx_test.go
git commit -m "feat(deploy): nginx security headers, MinIO CORS allow-list, S3_PUBLIC_ENDPOINT from env" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Web: `adminApi`, upload multipart từ trình duyệt, `formatBytes`/`parseGenres`

**Files:**
- Create: `web/app/src/admin/upload.test.ts`, `web/app/src/admin/upload.ts`
- Modify: `web/app/src/api.test.ts`, `web/app/src/format.test.ts`, `web/app/src/api.ts`, `web/app/src/format.ts`

**Interfaces:**
- Produces (`web/app/src/api.ts`):
  ```ts
  export type VideoStatus = 'uploading' | 'queued' | 'processing' | 'ready' | 'failed';
  export interface AdminSubtitle { lang: string; label: string; vtt_key: string }
  export interface AdminVideo { id; title; description; genres: string[]; year?; status: VideoStatus; fail_reason?; duration_s?; size_bytes; created_at; updated_at; renditions?: Rendition[]; subtitles?: AdminSubtitle[] }
  export interface UploadPart { part_number: number; url: string }
  export interface UploadPlan { upload_id: string; part_size: number; parts: UploadPart[]; expires_at: string }
  export interface CreateVideoInput { title: string; description?: string; genres?: string[]; year?: number; filename: string; size_bytes: number }
  export interface CreateVideoResult { id: string; status: VideoStatus; upload: UploadPlan }
  export interface VideoPatch { title?: string; description?: string; genres?: string[]; year?: number }
  export const ADMIN_LIST_LIMIT = 200;
  export const adminApi: { list(): Promise<AdminVideo[]>; get(id): Promise<AdminVideo>; create(input): Promise<CreateVideoResult>;
    complete(id): Promise<{id; status}>; retry(id): Promise<{id; status}>; update(id, patch: VideoPatch): Promise<AdminVideo>;
    remove(id): Promise<void>; uploadSubtitle(id, lang, label, file: File): Promise<AdminSubtitle> };
  // request(): chỉ body chuỗi mới đặt Content-Type: application/json (FormData để trình duyệt tự đặt boundary)
  ```
- Produces (`web/app/src/admin/upload.ts`):
  ```ts
  export const UPLOAD_CONCURRENCY = 3; export const PART_ATTEMPTS = 3;
  export type PutPart = (url: string, body: Blob, onProgress: (loaded: number) => void, signal: AbortSignal) => Promise<void>;
  export interface UploadOptions { onProgress?: (loaded: number, total: number) => void; concurrency?: number; putPart?: PutPart; signal?: AbortSignal }
  export class UploadError extends Error { readonly partNumber: number }
  export const xhrPutPart: PutPart;
  export function uploadParts(file: Blob, plan: UploadPlan, opts?: UploadOptions): Promise<void>;
  export type VideoMeta = Omit<CreateVideoInput, 'filename' | 'size_bytes'>;
  export class UploadVideoError extends Error { readonly videoId: string }
  export function uploadVideo(file: File, meta: VideoMeta, opts?: UploadOptions): Promise<string>;
  ```
- Produces (`web/app/src/format.ts`): `export function formatBytes(n: number): string`, `export function parseGenres(s: string): string[]`.

- [ ] **Step 1: Viết test (thất bại trước)**

`web/app/src/api.test.ts` (sửa — 2 cặp thay thế):

1. Thay:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, sendPlaybackEvent, setUnauthorizedHandler, videosQueryString } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
```

   bằng:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, adminApi, api, sendPlaybackEvent, setUnauthorizedHandler, videosQueryString } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
```

2. Thay:

```ts
    expect(() => sendPlaybackEvent(ev)).not.toThrow();
  });
});
```

   bằng:

```ts
    expect(() => sendPlaybackEvent(ev)).not.toThrow();
  });
});

describe('adminApi (M7)', () => {
  const ok = (body: unknown, status = 200) =>
    vi.fn().mockResolvedValue(new Response(status === 204 ? null : JSON.stringify(body), { status }));

  it('list lấy tối đa 200 video, update gửi PATCH JSON, remove nhận 204', async () => {
    let fetchMock = ok({ videos: [{ id: 'v1', status: 'failed' }] });
    vi.stubGlobal('fetch', fetchMock);
    expect(await adminApi.list()).toEqual([{ id: 'v1', status: 'failed' }]);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/admin/videos?limit=200');

    fetchMock = ok({ id: 'v1', title: 'Mới' });
    vi.stubGlobal('fetch', fetchMock);
    await adminApi.update('v1', { title: 'Mới', year: 0 });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/videos/v1');
    expect(init.method).toBe('PATCH');
    expect(JSON.parse(init.body as string)).toEqual({ title: 'Mới', year: 0 });

    fetchMock = ok(null, 204);
    vi.stubGlobal('fetch', fetchMock);
    await expect(adminApi.remove('v1')).resolves.toBeUndefined();
    expect((fetchMock.mock.calls[0][1] as RequestInit).method).toBe('DELETE');
  });

  it('uploadSubtitle gửi FormData, không đặt Content-Type JSON', async () => {
    const fetchMock = ok({ lang: 'vi', label: 'Tiếng Việt', vtt_key: 'v1/subs/vi.vtt' }, 201);
    vi.stubGlobal('fetch', fetchMock);
    const file = new File(['1\n00:00:01,000 --> 00:00:02,000\nXin chào\n'], 'vi.srt');
    await adminApi.uploadSubtitle('v1', 'vi', 'Tiếng Việt', file);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/videos/v1/subtitles');
    expect(init.body).toBeInstanceOf(FormData);
    const form = init.body as FormData;
    expect(form.get('lang')).toBe('vi');
    expect(form.get('label')).toBe('Tiếng Việt');
    expect((form.get('file') as File).name).toBe('vi.srt');
    expect(new Headers(init.headers).get('Content-Type')).toBeNull();
  });
});
```

`web/app/src/admin/upload.test.ts` (mới, toàn văn):

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type UploadPlan } from '../api';
import { PART_ATTEMPTS, UploadError, UploadVideoError, uploadParts, uploadVideo, type PutPart } from './upload';

afterEach(() => vi.restoreAllMocks());

// plan cho file `size` byte, part `partSize` byte; URL = "u{n}".
function planFor(size: number, partSize: number): UploadPlan {
  const n = Math.ceil(size / partSize);
  return {
    upload_id: 'up-1',
    part_size: partSize,
    parts: Array.from({ length: n }, (_, i) => ({ part_number: i + 1, url: `u${i + 1}` })),
    expires_at: '2026-11-15T12:00:00Z',
  };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('uploadParts', () => {
  it('cắt đúng khoảng byte, tối đa 3 part cùng lúc, tiến độ tới đủ kích thước file', async () => {
    const file = new Blob(['a'.repeat(10)]);
    const got: Record<string, string> = {};
    let active = 0;
    let maxActive = 0;
    const put: PutPart = async (url, body, onProgress) => {
      active++;
      maxActive = Math.max(maxActive, active);
      onProgress(1);
      await tick();
      got[url] = String(body.size);
      onProgress(body.size);
      active--;
    };
    const progress: number[] = [];
    await uploadParts(file, planFor(10, 3), { putPart: put, onProgress: (l, t) => progress.push(l / t) });
    expect(got).toEqual({ u1: '3', u2: '3', u3: '3', u4: '1' });
    expect(maxActive).toBe(3);
    expect(progress.at(-1)).toBe(1);
    expect(progress.every((p, i) => i === 0 || p >= progress[i - 1])).toBe(true); // không retry: chỉ tăng
  });

  it('thử lại part lỗi, thành công ở lần sau', async () => {
    const calls: string[] = [];
    const put: PutPart = async (url) => {
      calls.push(url);
      if (url === 'u2' && calls.filter((c) => c === 'u2').length === 1) throw new Error('HTTP 500');
    };
    await uploadParts(new Blob(['abcdef']), planFor(6, 3), { putPart: put, concurrency: 1 });
    expect(calls).toEqual(['u1', 'u2', 'u2']);
  });

  it(`lỗi ${PART_ATTEMPTS} lần thì ném UploadError và huỷ part đang gửi`, async () => {
    const aborted: string[] = [];
    // part 1 luôn lỗi; part 2 treo tới khi bị huỷ.
    const put: PutPart = (url, _body, _p, signal) =>
      new Promise<void>((_resolve, reject) => {
        if (url === 'u1') {
          reject(new Error('HTTP 403'));
          return;
        }
        signal.addEventListener('abort', () => {
          aborted.push(url);
          reject(new DOMException('aborted', 'AbortError'));
        });
      });
    const err = await uploadParts(new Blob(['abcdef']), planFor(6, 3), { putPart: put }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UploadError);
    expect((err as UploadError).partNumber).toBe(1);
    expect((err as UploadError).message).toBe('part 1: HTTP 403');
    expect(aborted).toEqual(['u2']);
  });
});

describe('uploadVideo', () => {
  it('create → PUT các part → complete, trả id', async () => {
    const order: string[] = [];
    vi.spyOn(adminApi, 'create').mockImplementation(async (input) => {
      order.push(`create ${input.filename} ${input.size_bytes} ${input.title}`);
      return { id: 'v1', status: 'uploading', upload: planFor(input.size_bytes, 4) };
    });
    vi.spyOn(adminApi, 'complete').mockImplementation(async (id) => {
      order.push(`complete ${id}`);
      return { id, status: 'queued' };
    });
    const put: PutPart = async (url) => {
      order.push(`put ${url}`);
    };
    const file = new File(['12345678'], 'phim.mp4');
    expect(await uploadVideo(file, { title: 'Phim' }, { putPart: put, concurrency: 1 })).toBe('v1');
    expect(order).toEqual(['create phim.mp4 8 Phim', 'put u1', 'put u2', 'complete v1']);
  });

  it('lỗi sau khi đã tạo video: UploadVideoError mang id để xoá', async () => {
    vi.spyOn(adminApi, 'create').mockResolvedValue({ id: 'v9', status: 'uploading', upload: planFor(4, 4) });
    const complete = vi.spyOn(adminApi, 'complete');
    const put: PutPart = async () => {
      throw new Error('lỗi mạng');
    };
    const err = await uploadVideo(new File(['1234'], 'a.mp4'), { title: 'A' }, { putPart: put }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UploadVideoError);
    expect((err as UploadVideoError).videoId).toBe('v9');
    expect(complete).not.toHaveBeenCalled();
  });
});
```

`web/app/src/format.test.ts` (sửa — 2 cặp thay thế):

1. Thay:

```ts
import { describe, expect, it } from 'vitest';
import { formatDuration, posterHue, progressPercent } from './format';

describe('formatDuration', () => {
  it('định dạng phút:giây và giờ:phút:giây', () => {
```

   bằng:

```ts
import { describe, expect, it } from 'vitest';
import { formatBytes, formatDuration, parseGenres, posterHue, progressPercent } from './format';

describe('formatDuration', () => {
  it('định dạng phút:giây và giờ:phút:giây', () => {
```

2. Thay:

```ts
    expect(progressPercent(10, 0)).toBe(0);
  });
});
```

   bằng:

```ts
    expect(progressPercent(10, 0)).toBe(0);
  });
});

describe('formatBytes', () => {
  it('cơ số 1024, một chữ số thập phân', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(30 * 1024 * 1024)).toBe('30 MB');
    expect(formatBytes(10 * 1024 ** 3)).toBe('10 GB');
  });
});

describe('parseGenres', () => {
  it('tách theo dấu phẩy, bỏ rỗng và trùng', () => {
    expect(parseGenres('drama, Hài ,, drama')).toEqual(['drama', 'Hài']);
    expect(parseGenres('   ')).toEqual([]);
  });
});
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `cd web/app && npx vitest run src/api.test.ts src/admin/upload.test.ts src/format.test.ts 2>&1 | tail -5`
Expected: FAIL — `src/admin/upload.test.ts`: `Failed to resolve import "./upload"`; `api.test.ts` ca `adminApi (M7)`: `TypeError: Cannot read properties of undefined (reading 'list')`; `format.test.ts`: `TypeError: formatBytes is not a function`.

- [ ] **Step 3: Cài đặt**

`web/app/src/api.ts` (sửa — 3 cặp thay thế):

1. Thay:

```ts
  page?: number;
}

export class ApiError extends Error {
  readonly status: number;

```

   bằng:

```ts
  page?: number;
}

// ---- Admin (M7): /api/admin/*, chỉ role admin (server trả 403 cho viewer) ----

export type VideoStatus = 'uploading' | 'queued' | 'processing' | 'ready' | 'failed';

export interface AdminSubtitle {
  lang: string;
  label: string;
  vtt_key: string;
}

// Video như admin thấy (GET /api/admin/videos[/{id}]): mọi trạng thái, có fail_reason.
export interface AdminVideo {
  id: string;
  title: string;
  description: string;
  genres: string[];
  year?: number;
  status: VideoStatus;
  fail_reason?: string;
  duration_s?: number;
  size_bytes: number;
  created_at: string;
  updated_at: string;
  renditions?: Rendition[];
  subtitles?: AdminSubtitle[]; // chỉ có ở chi tiết
}

export interface UploadPart {
  part_number: number;
  url: string;
}

// Kế hoạch upload multipart: mỗi part một presigned URL (PUT thẳng lên MinIO), part_size byte mỗi part
// (part cuối có thể ngắn hơn). URL hết hạn lúc expires_at (1 giờ).
export interface UploadPlan {
  upload_id: string;
  part_size: number;
  parts: UploadPart[];
  expires_at: string;
}

export interface CreateVideoInput {
  title: string;
  description?: string;
  genres?: string[];
  year?: number;
  filename: string;
  size_bytes: number;
}

export interface CreateVideoResult {
  id: string;
  status: VideoStatus;
  upload: UploadPlan;
}

// PATCH: field không có thì giữ nguyên; year 0 xoá năm; genres [] xoá thể loại.
export interface VideoPatch {
  title?: string;
  description?: string;
  genres?: string[];
  year?: number;
}

export class ApiError extends Error {
  readonly status: number;

```

2. Thay:

```ts

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body !== undefined) headers.set('Content-Type', 'application/json');
  const res = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
```

   bằng:

```ts

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  // Chỉ body JSON (chuỗi) mới đặt Content-Type; FormData để trình duyệt tự đặt multipart/form-data + boundary.
  if (typeof init.body === 'string') headers.set('Content-Type', 'application/json');
  const res = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
```

3. Thay:

```ts
    request<{ videos: ContinueVideo[] }>('/api/me/continue-watching').then((r) => r.videos),
};

// sendPlaybackEvent gửi sự kiện xem và không chờ kết quả: lỗi (mạng, 401, Kafka chết phía server) bị bỏ qua
// để không bao giờ ảnh hưởng việc xem. keepalive: request vẫn đi khi tab đang đóng (pagehide).
// Không qua request() nên 401 ở đây không đẩy người dùng về trang đăng nhập giữa lúc đang xem.
```

   bằng:

```ts
    request<{ videos: ContinueVideo[] }>('/api/me/continue-watching').then((r) => r.videos),
};

const adminVideo = (id: string) => `/api/admin/videos/${encodeURIComponent(id)}`;

// ADMIN_LIST_LIMIT: số video tối đa của trang quản trị (server cho phép 1–200, mới nhất trước).
export const ADMIN_LIST_LIMIT = 200;

export const adminApi = {
  list: () =>
    request<{ videos: AdminVideo[] }>(`/api/admin/videos?limit=${ADMIN_LIST_LIMIT}`).then((r) => r.videos),
  get: (id: string) => request<AdminVideo>(adminVideo(id)),
  create: (input: CreateVideoInput) =>
    request<CreateVideoResult>('/api/admin/videos', { method: 'POST', body: json(input) }),
  complete: (id: string) =>
    request<{ id: string; status: VideoStatus }>(`${adminVideo(id)}/complete`, { method: 'POST' }),
  retry: (id: string) => request<{ id: string; status: VideoStatus }>(`${adminVideo(id)}/retry`, { method: 'POST' }),
  update: (id: string, patch: VideoPatch) =>
    request<AdminVideo>(adminVideo(id), { method: 'PATCH', body: json(patch) }),
  remove: (id: string) => request<void>(adminVideo(id), { method: 'DELETE' }),
  uploadSubtitle: (id: string, lang: string, label: string, file: File) => {
    const form = new FormData();
    form.set('lang', lang);
    form.set('label', label);
    form.set('file', file);
    return request<AdminSubtitle>(`${adminVideo(id)}/subtitles`, { method: 'POST', body: form });
  },
};

// sendPlaybackEvent gửi sự kiện xem và không chờ kết quả: lỗi (mạng, 401, Kafka chết phía server) bị bỏ qua
// để không bao giờ ảnh hưởng việc xem. keepalive: request vẫn đi khi tab đang đóng (pagehide).
// Không qua request() nên 401 ở đây không đẩy người dùng về trang đăng nhập giữa lúc đang xem.
```

`web/app/src/admin/upload.ts` (mới, toàn văn):

```ts
// Upload video từ trình duyệt (M7, ADR 0019): tạo video → PUT từng part thẳng lên MinIO qua presigned URL
// (song song, có tiến độ) → /complete. File không đi qua api hay nginx. api ghép part bằng ListParts nên
// trình duyệt không cần đọc header ETag của phản hồi PUT.
import { adminApi, type CreateVideoInput, type UploadPlan } from '../api';

// UPLOAD_CONCURRENCY: số part PUT cùng lúc (part 64 MiB → tối đa ~192 MiB đang gửi).
export const UPLOAD_CONCURRENCY = 3;
// PART_ATTEMPTS: mỗi part thử tối đa 3 lần (lỗi mạng ngắn) trước khi bỏ cả upload.
export const PART_ATTEMPTS = 3;

// PutPart gửi một part; gọi onProgress(số byte đã gửi của part này) trong lúc gửi.
export type PutPart = (url: string, body: Blob, onProgress: (loaded: number) => void, signal: AbortSignal) => Promise<void>;

export interface UploadOptions {
  onProgress?: (loaded: number, total: number) => void;
  concurrency?: number;
  putPart?: PutPart;
  signal?: AbortSignal;
}

export class UploadError extends Error {
  readonly partNumber: number;

  constructor(partNumber: number, message: string) {
    super(message);
    this.name = 'UploadError';
    this.partNumber = partNumber;
  }
}

// xhrPutPart dùng XMLHttpRequest vì fetch không báo tiến độ upload. Blob cắt từ File có type rỗng nên
// trình duyệt không gửi Content-Type (presigned URL chỉ ký header host).
export const xhrPutPart: PutPart = (url, body, onProgress, signal) =>
  new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url);
    xhr.upload.onprogress = (e) => onProgress(e.loaded);
    xhr.onload = () =>
      xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new Error(`HTTP ${xhr.status}`));
    xhr.onerror = () => reject(new Error('lỗi mạng (kiểm tra CORS của MinIO và cổng 9000)'));
    xhr.onabort = () => reject(new DOMException('upload aborted', 'AbortError'));
    if (signal.aborted) {
      reject(new DOMException('upload aborted', 'AbortError'));
      return;
    }
    signal.addEventListener('abort', () => xhr.abort(), { once: true });
    xhr.send(body);
  });

// uploadParts gửi mọi part của plan, tối đa `concurrency` part cùng lúc. Một part lỗi PART_ATTEMPTS lần thì
// huỷ các part đang gửi và ném UploadError. onProgress nhận tổng byte đã gửi (không vượt file.size).
export async function uploadParts(file: Blob, plan: UploadPlan, opts: UploadOptions = {}): Promise<void> {
  const put = opts.putPart ?? xhrPutPart;
  const total = file.size;
  const loaded = new Map<number, number>();
  const report = () => {
    let sum = 0;
    for (const n of loaded.values()) sum += n;
    opts.onProgress?.(Math.min(sum, total), total);
  };
  const ctrl = new AbortController();
  const onOuterAbort = () => ctrl.abort();
  opts.signal?.addEventListener('abort', onOuterAbort, { once: true });

  const queue = [...plan.parts].sort((a, b) => a.part_number - b.part_number);
  const sendPart = async (partNumber: number, url: string) => {
    const start = (partNumber - 1) * plan.part_size;
    const body = file.slice(start, Math.min(start + plan.part_size, total));
    for (let attempt = 1; ; attempt++) {
      loaded.set(partNumber, 0);
      try {
        await put(url, body, (n) => {
          loaded.set(partNumber, Math.min(n, body.size));
          report();
        }, ctrl.signal);
        loaded.set(partNumber, body.size);
        report();
        return;
      } catch (err) {
        if (ctrl.signal.aborted) throw err;
        if (attempt >= PART_ATTEMPTS) {
          const msg = err instanceof Error ? err.message : String(err);
          throw new UploadError(partNumber, `part ${partNumber}: ${msg}`);
        }
      }
    }
  };
  const worker = async () => {
    for (let p = queue.shift(); p; p = queue.shift()) await sendPart(p.part_number, p.url);
  };
  try {
    const n = Math.max(1, Math.min(opts.concurrency ?? UPLOAD_CONCURRENCY, queue.length));
    await Promise.all(Array.from({ length: n }, worker));
  } catch (err) {
    queue.length = 0; // không bắt đầu part mới
    ctrl.abort(); // dừng các part còn đang gửi
    throw err;
  } finally {
    opts.signal?.removeEventListener('abort', onOuterAbort);
  }
}

export type VideoMeta = Omit<CreateVideoInput, 'filename' | 'size_bytes'>;

// uploadVideo: create → uploadParts → complete. Trả id; lỗi sau bước create kèm id (UploadVideoError)
// để trang admin cho xoá video dở (nếu không, cleaner tự xoá sau STALE_UPLOAD_AFTER = 24h).
export class UploadVideoError extends Error {
  readonly videoId: string;

  constructor(videoId: string, cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause));
    this.name = 'UploadVideoError';
    this.videoId = videoId;
  }
}

export async function uploadVideo(file: File, meta: VideoMeta, opts: UploadOptions = {}): Promise<string> {
  const created = await adminApi.create({ ...meta, filename: file.name, size_bytes: file.size });
  try {
    await uploadParts(file, created.upload, opts);
    await adminApi.complete(created.id);
  } catch (err) {
    throw new UploadVideoError(created.id, err);
  }
  return created.id;
}
```

`web/app/src/format.ts` (sửa — 1 cặp thay thế):

1. Thay:

```ts
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return h;
}
```

   bằng:

```ts
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return h;
}

// formatBytes: 0 → "0 B", 1536 → "1.5 KB", 31_457_280 → "30 MB" (cơ số 1024, tối đa một chữ số thập phân).
export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : Number(v.toFixed(1))} ${units[i]}`;
}

// parseGenres: "drama, Hài ,, drama" → ["drama", "Hài"] (tách theo dấu phẩy, bỏ rỗng và trùng).
export function parseGenres(s: string): string[] {
  return [...new Set(s.split(',').map((g) => g.trim()).filter(Boolean))];
}
```

- [ ] **Step 4: Chạy test**

Run: `cd web/app && npx tsc --noEmit && npx vitest run src/api.test.ts src/admin/upload.test.ts src/format.test.ts 2>&1 | grep -E 'Tests|FAIL'`
Expected: `Tests  19 passed (19)` (api 9, upload 5, format 5), không có `FAIL`.

- [ ] **Step 5: Commit**

```bash
git add web/app/src/api.ts web/app/src/api.test.ts web/app/src/admin/upload.ts web/app/src/admin/upload.test.ts web/app/src/format.ts web/app/src/format.test.ts
git commit -m "feat(web): admin API client and browser multipart upload with progress and retry" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Web: `RequireAdmin`, chip trạng thái, trang danh sách `/admin`, link "Quản trị"

**Files:**
- Create: `web/app/src/admin/RequireAdmin.test.tsx`, `web/app/src/admin/AdminVideosPage.test.tsx`, `web/app/src/admin/StatusChip.tsx`, `web/app/src/admin/RequireAdmin.tsx`, `web/app/src/admin/AdminVideosPage.tsx`
- Modify: `web/app/src/components/Header.tsx`

**Interfaces:**
- Produces (`web/app/src/admin/`):
  ```ts
  // StatusChip.tsx
  export const STATUS_LABELS: Record<VideoStatus, string>; // Đang upload, Chờ xử lý, Đang xử lý, Sẵn sàng, Lỗi
  export function isProcessing(status: VideoStatus): boolean; // queued | processing
  export function StatusChip(props: { status: VideoStatus }): JSX.Element; // <span class="chip chip-{status}">
  // RequireAdmin.tsx
  export function RequireAdmin(props: { children: ReactNode }): JSX.Element; // viewer → <h1>403 — Không có quyền</h1>
  // AdminVideosPage.tsx
  export const POLL_MS = 3000;
  export function errorMessage(err: unknown): string; // ApiError → message, khác → câu chung
  export function AdminVideosPage(): JSX.Element;
  ```
- `Header`: `NavLink to="/admin"` "Quản trị" khi `user.role === 'admin'`.

- [ ] **Step 1: Viết test (thất bại trước)**

`web/app/src/admin/RequireAdmin.test.tsx` (mới, toàn văn):

```tsx
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, type User } from '../api';
import { AuthProvider } from '../auth';
import { Header } from '../components/Header';
import { RequireAdmin } from './RequireAdmin';

const user = (role: User['role']): User => ({ id: 'u1', email: `${role}@x.vn`, role, created_at: '' });

function renderAs(role: User['role']) {
  vi.spyOn(api, 'me').mockResolvedValue(user(role));
  render(
    <MemoryRouter>
      <AuthProvider>
        <Header />
        <RequireAdmin>
          <p>khu vực admin</p>
        </RequireAdmin>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe('RequireAdmin', () => {
  afterEach(() => vi.restoreAllMocks());

  it('viewer thấy trang 403, không thấy link Quản trị', async () => {
    renderAs('viewer');
    expect(await screen.findByRole('heading', { name: '403 — Không có quyền' })).toBeInTheDocument();
    expect(screen.queryByText('khu vực admin')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Quản trị' })).toBeNull();
  });

  it('admin thấy nội dung và link Quản trị trên header', async () => {
    renderAs('admin');
    expect(await screen.findByText('khu vực admin')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Quản trị' })).toHaveAttribute('href', '/admin');
  });
});
```

`web/app/src/admin/AdminVideosPage.test.tsx` (mới, toàn văn):

```tsx
import { act, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type AdminVideo } from '../api';
import { AdminVideosPage, POLL_MS } from './AdminVideosPage';

const video = (id: string, status: AdminVideo['status'], extra: Partial<AdminVideo> = {}): AdminVideo => ({
  id,
  title: `Phim ${id}`,
  description: '',
  genres: [],
  status,
  size_bytes: 30 * 1024 * 1024,
  created_at: '2026-11-15T10:00:00Z',
  updated_at: '2026-11-15T10:00:00Z',
  ...extra,
});

function renderPage() {
  render(
    <MemoryRouter>
      <AdminVideosPage />
    </MemoryRouter>,
  );
}

describe('AdminVideosPage', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('hiện trạng thái, lý do lỗi, nút thử lại chỉ cho video failed, không cho xoá video đang xử lý', async () => {
    vi.spyOn(adminApi, 'list').mockResolvedValue([
      video('a', 'failed', { fail_reason: 'transcode: input has no video stream' }),
      video('b', 'ready'),
      video('c', 'processing'),
    ]);
    renderPage();
    expect(await screen.findByText('Lỗi')).toHaveClass('chip', 'chip-failed');
    expect(screen.getByText('Sẵn sàng')).toHaveClass('chip-ready');
    expect(screen.getByText('Đang xử lý')).toHaveClass('chip-processing');
    expect(screen.getByText('transcode: input has no video stream')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Phim a' })).toHaveAttribute('href', '/admin/videos/a');
    expect(screen.getAllByRole('button', { name: 'Thử lại' })).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Xoá' })).toHaveLength(2); // a, b; không có c
    expect(screen.getAllByText('30 MB')).toHaveLength(3);
  });

  it('thử lại gọi API rồi tải lại danh sách', async () => {
    const list = vi
      .spyOn(adminApi, 'list')
      .mockResolvedValueOnce([video('a', 'failed', { fail_reason: 'boom' })])
      .mockResolvedValue([video('a', 'queued')]);
    const retry = vi.spyOn(adminApi, 'retry').mockResolvedValue({ id: 'a', status: 'queued' });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Thử lại' }));
    expect(await screen.findByText('Chờ xử lý')).toBeInTheDocument();
    expect(retry).toHaveBeenCalledWith('a');
    expect(list).toHaveBeenCalledTimes(2);
  });

  it('xoá cần xác nhận', async () => {
    vi.spyOn(adminApi, 'list').mockResolvedValue([video('b', 'ready')]);
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage();
    const button = await screen.findByRole('button', { name: 'Xoá' });
    fireEvent.click(button);
    expect(remove).not.toHaveBeenCalled();
    fireEvent.click(button);
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(remove).toHaveBeenCalledWith('b');
  });

  it('poll mỗi POLL_MS khi còn video queued/processing, dừng khi tất cả xong', async () => {
    vi.useFakeTimers();
    const list = vi
      .spyOn(adminApi, 'list')
      .mockResolvedValueOnce([video('a', 'processing')])
      .mockResolvedValueOnce([video('a', 'ready')])
      .mockResolvedValue([video('a', 'ready')]);
    renderPage();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText('Đang xử lý')).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });
    expect(screen.getByText('Sẵn sàng')).toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS * 3);
    });
    expect(list).toHaveBeenCalledTimes(2);
  });
});
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `cd web/app && npx vitest run src/admin/RequireAdmin.test.tsx src/admin/AdminVideosPage.test.tsx 2>&1 | tail -4`
Expected: FAIL — `Failed to resolve import "./RequireAdmin"` / `"./AdminVideosPage"`.

- [ ] **Step 3: Cài đặt**

`web/app/src/admin/StatusChip.tsx` (mới, toàn văn):

```tsx
import type { VideoStatus } from '../api';

export const STATUS_LABELS: Record<VideoStatus, string> = {
  uploading: 'Đang upload',
  queued: 'Chờ xử lý',
  processing: 'Đang xử lý',
  ready: 'Sẵn sàng',
  failed: 'Lỗi',
};

// isProcessing: trạng thái do transcoder đẩy đi (queued, processing). Trang admin poll khi còn video ở đây.
// uploading không poll: nó chỉ đổi khi admin gọi /complete (hoặc bị cleaner xoá sau 24 giờ).
export function isProcessing(status: VideoStatus): boolean {
  return status === 'queued' || status === 'processing';
}

export function StatusChip({ status }: { status: VideoStatus }) {
  return <span className={`chip chip-${status}`}>{STATUS_LABELS[status] ?? status}</span>;
}
```

`web/app/src/admin/RequireAdmin.tsx` (mới, toàn văn):

```tsx
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useAuth } from '../auth';

// RequireAdmin: đặt bên trong RequireAuth. Viewer thấy trang 403 (server cũng trả 403 cho /api/admin/*,
// đây chỉ là giao diện).
export function RequireAdmin({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  if (user?.role !== 'admin') {
    return (
      <div role="alert" className="status">
        <h1>403 — Không có quyền</h1>
        <p>Trang quản trị chỉ dành cho tài khoản admin.</p>
        <Link to="/">Về trang chủ</Link>
      </div>
    );
  }
  return <>{children}</>;
}
```

`web/app/src/admin/AdminVideosPage.tsx` (mới, toàn văn):

```tsx
import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, adminApi, type AdminVideo } from '../api';
import { formatBytes } from '../format';
import { StatusChip, isProcessing } from './StatusChip';

// POLL_MS: chu kỳ tải lại danh sách khi còn video queued/processing.
export const POLL_MS = 3000;

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return 'Có lỗi xảy ra, vui lòng thử lại.';
}

// AdminVideosPage (/admin): mọi video mới nhất trước, trạng thái, lý do lỗi, retry và xoá.
export function AdminVideosPage() {
  const [videos, setVideos] = useState<AdminVideo[] | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState<string | null>(null); // id đang retry/xoá

  const load = useCallback(async () => {
    try {
      setVideos(await adminApi.list());
      setError('');
    } catch (err) {
      setError(`Không tải được danh sách: ${errorMessage(err)}`);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const polling = videos?.some((v) => isProcessing(v.status)) ?? false;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(t);
  }, [polling, load]);

  const act = async (id: string, fn: () => Promise<unknown>) => {
    setBusy(id);
    try {
      await fn();
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  };
  const retry = (v: AdminVideo) => act(v.id, () => adminApi.retry(v.id));
  const remove = (v: AdminVideo) => {
    if (!window.confirm(`Xoá "${v.title}"? Video, HLS, phụ đề và tiến độ xem sẽ bị xoá vĩnh viễn.`)) return;
    void act(v.id, () => adminApi.remove(v.id));
  };

  return (
    <section className="admin">
      <div className="admin-head">
        <h1>Quản trị video</h1>
        <Link to="/admin/upload" className="button">
          Tải video lên
        </Link>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {!videos && !error && <p className="status">Đang tải…</p>}
      {videos && videos.length === 0 && <p className="empty">Chưa có video nào.</p>}
      {videos && videos.length > 0 && (
        <table className="admin-table">
          <thead>
            <tr>
              <th>Tiêu đề</th>
              <th>Trạng thái</th>
              <th>Dung lượng</th>
              <th>Tạo lúc</th>
              <th aria-label="Thao tác" />
            </tr>
          </thead>
          <tbody>
            {videos.map((v) => (
              <tr key={v.id}>
                <td>
                  <Link to={`/admin/videos/${v.id}`}>{v.title}</Link>
                  {v.status === 'failed' && v.fail_reason && <p className="fail-reason">{v.fail_reason}</p>}
                </td>
                <td>
                  <StatusChip status={v.status} />
                </td>
                <td>{formatBytes(v.size_bytes)}</td>
                <td>{new Date(v.created_at).toLocaleString('vi-VN')}</td>
                <td className="actions">
                  {v.status === 'failed' && (
                    <button type="button" disabled={busy === v.id} onClick={() => void retry(v)}>
                      Thử lại
                    </button>
                  )}
                  {!isProcessing(v.status) && (
                    <button type="button" className="danger" disabled={busy === v.id} onClick={() => remove(v)}>
                      Xoá
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {polling && <p className="meta">Đang xử lý video, tự cập nhật mỗi {POLL_MS / 1000} giây…</p>}
    </section>
  );
}
```

`web/app/src/components/Header.tsx` (sửa — 1 cặp thay thế):

1. Thay:

```tsx
              Trang chủ
            </NavLink>
            <NavLink to="/search">Tìm kiếm</NavLink>
          </nav>
          <div className="user">
            <span title={user.role === 'admin' ? 'Quản trị viên' : 'Người xem'}>{user.email}</span>
```

   bằng:

```tsx
              Trang chủ
            </NavLink>
            <NavLink to="/search">Tìm kiếm</NavLink>
            {user.role === 'admin' && <NavLink to="/admin">Quản trị</NavLink>}
          </nav>
          <div className="user">
            <span title={user.role === 'admin' ? 'Quản trị viên' : 'Người xem'}>{user.email}</span>
```

- [ ] **Step 4: Chạy test**

Run: `cd web/app && npx tsc --noEmit && npx vitest run src/admin 2>&1 | grep -E 'Tests|FAIL'`
Expected: `Tests  11 passed (11)` (upload 5, RequireAdmin 2, AdminVideosPage 4 — gồm poll bằng fake timer: gọi lại sau `POLL_MS` khi `processing`, dừng khi tất cả `ready`).

- [ ] **Step 5: Commit**

```bash
git add web/app/src/admin web/app/src/components/Header.tsx
git commit -m "feat(web): admin guard, status chips and video list with retry, delete and polling" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Web: trang upload, trang chi tiết video, route `/admin/*`, `ErrorBoundary` reset theo route, CSS

**Files:**
- Create: `web/app/src/admin/AdminUploadPage.test.tsx`, `web/app/src/admin/AdminVideoPage.test.tsx`, `web/app/src/admin/AdminUploadPage.tsx`, `web/app/src/admin/AdminVideoPage.tsx`
- Modify: `web/app/src/components/ErrorBoundary.test.tsx`, `web/app/src/components/ErrorBoundary.tsx`, `web/app/src/App.tsx`, `web/app/src/styles.css`

**Interfaces:**
- Produces (`web/app/src/admin/`):
  ```ts
  // AdminUploadPage.tsx
  export const VIDEO_ACCEPT = '.mp4,.mov,.mkv,.webm'; export const MAX_VIDEO_BYTES = 10 * 1024 ** 3;
  export function titleFromFilename(name: string): string;
  export function AdminUploadPage(): JSX.Element;
  // AdminVideoPage.tsx
  export const LANG_RE = /^[a-z]{2,3}(-[A-Z]{2})?$/;
  export function AdminVideoPage(): JSX.Element; // form aria-label "Thông tin phim" và "Phụ đề"
  // components/ErrorBoundary.tsx
  export class ErrorBoundary extends Component<{ children: ReactNode; resetKey?: unknown }, { failed: boolean }>;
  ```
- `App.tsx`: `<Route path="admin" element={<RequireAdmin><Outlet/></RequireAdmin>}>` với `index` → `AdminVideosPage`, `upload` → `AdminUploadPage`, `videos/:id` → `AdminVideoPage` (cả ba `lazy`); `Layout` truyền `resetKey={pathname}`.

- [ ] **Step 1: Viết test (thất bại trước)**

`ErrorBoundary.test.tsx` chuyển việc gỡ listener `error` và `unstubAllGlobals` sang `afterEach` (nhận xét M5 T11-12) và thêm ca `resetKey`.

`web/app/src/admin/AdminUploadPage.test.tsx` (mới, toàn văn):

```tsx
import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi } from '../api';
import { AdminUploadPage, titleFromFilename } from './AdminUploadPage';
import * as upload from './upload';

function renderPage() {
  render(
    <MemoryRouter initialEntries={['/admin/upload']}>
      <Routes>
        <Route path="/admin/upload" element={<AdminUploadPage />} />
        <Route path="/admin/videos/:id" element={<p>trang chi tiết</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const pick = (file: File) => fireEvent.change(screen.getByLabelText(/File video/), { target: { files: [file] } });

describe('AdminUploadPage', () => {
  afterEach(() => vi.restoreAllMocks());

  it('titleFromFilename bỏ đuôi và gạch nối', () => {
    expect(titleFromFilename('tro_choi-con-muc.mp4')).toBe('tro choi con muc');
  });

  it('upload với metadata, hiện tiến độ rồi chuyển tới trang chi tiết', async () => {
    let resolve!: (id: string) => void;
    const spy = vi.spyOn(upload, 'uploadVideo').mockImplementation((_f, _m, opts) => {
      opts?.onProgress?.(5, 10);
      return new Promise((r) => (resolve = r));
    });
    renderPage();
    pick(new File(['0123456789'], 'phim_hay.mp4'));
    expect(screen.getByLabelText('Tiêu đề')).toHaveValue('phim hay');
    fireEvent.change(screen.getByLabelText(/Thể loại/), { target: { value: 'drama, hài' } });
    fireEvent.change(screen.getByLabelText('Năm'), { target: { value: '2024' } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));

    expect(await screen.findByText(/^50%/)).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: 'Tiến độ upload' })).toHaveAttribute('value', '50');
    expect(screen.getByRole('button', { name: 'Đang upload…' })).toBeDisabled();
    const [file, meta] = spy.mock.calls[0];
    expect(file.name).toBe('phim_hay.mp4');
    expect(meta).toEqual({ title: 'phim hay', description: '', genres: ['drama', 'hài'], year: 2024 });

    resolve('v1');
    expect(await screen.findByText('trang chi tiết')).toBeInTheDocument();
  });

  it('kiểm tra form trước khi gọi API', () => {
    const spy = vi.spyOn(upload, 'uploadVideo');
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Chọn file video.');
    pick(new File(['x'], 'a.mp4'));
    fireEvent.change(screen.getByLabelText('Năm'), { target: { value: '99' } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Năm phải từ 1888 đến 2100.');
    expect(spy).not.toHaveBeenCalled();
  });

  it('upload lỗi sau khi đã tạo video: cho xoá video dở', async () => {
    vi.spyOn(upload, 'uploadVideo').mockRejectedValue(new upload.UploadVideoError('v9', new Error('part 1: HTTP 403')));
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    renderPage();
    pick(new File(['x'], 'a.mp4'));
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Upload lỗi: part 1: HTTP 403');
    fireEvent.click(screen.getByRole('button', { name: 'Xoá video upload dở' }));
    expect(await screen.findByText('Đã xoá video upload dở.')).toBeInTheDocument();
    expect(remove).toHaveBeenCalledWith('v9');
  });
});
```

`web/app/src/admin/AdminVideoPage.test.tsx` (mới, toàn văn):

```tsx
import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type AdminVideo } from '../api';
import { AdminVideoPage } from './AdminVideoPage';

const base: AdminVideo = {
  id: 'v1',
  title: 'Phim cũ',
  description: 'mô tả',
  genres: ['drama'],
  year: 2020,
  status: 'ready',
  duration_s: 30,
  size_bytes: 1024,
  created_at: '2026-11-15T10:00:00Z',
  updated_at: '2026-11-15T10:00:00Z',
  renditions: [{ height: 720, bitrate_kbps: 2800 }],
  subtitles: [{ lang: 'vi', label: 'Tiếng Việt', vtt_key: 'v1/subs/vi.vtt' }],
};

function renderPage() {
  render(
    <MemoryRouter initialEntries={['/admin/videos/v1']}>
      <Routes>
        <Route path="/admin/videos/:id" element={<AdminVideoPage />} />
        <Route path="/admin" element={<p>danh sách</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('AdminVideoPage', () => {
  afterEach(() => vi.restoreAllMocks());

  it('hiện chi tiết, sửa metadata gửi PATCH đủ field', async () => {
    vi.spyOn(adminApi, 'get').mockResolvedValue(base);
    const update = vi.spyOn(adminApi, 'update').mockResolvedValue({ ...base, title: 'Phim mới' });
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Phim cũ' })).toBeInTheDocument();
    expect(screen.getByText('1 KB · 0:30 · 720p')).toBeInTheDocument();
    expect(screen.getByText('Tiếng Việt (vi)')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '▶ Xem phim' })).toHaveAttribute('href', '/watch/v1');

    const form = screen.getByRole('form', { name: 'Thông tin phim' });
    fireEvent.change(within(form).getByLabelText('Tiêu đề'), { target: { value: ' Phim mới ' } });
    fireEvent.change(within(form).getByLabelText(/Thể loại/), { target: { value: 'drama, hài, ' } });
    fireEvent.change(within(form).getByLabelText('Năm'), { target: { value: '' } });
    fireEvent.click(within(form).getByRole('button', { name: 'Lưu' }));
    expect(await within(form).findByText('Đã lưu.')).toBeInTheDocument();
    expect(update).toHaveBeenCalledWith('v1', { title: 'Phim mới', description: 'mô tả', genres: ['drama', 'hài'], year: 0 });
  });

  it('video failed: hiện lý do lỗi và thử lại', async () => {
    const get = vi
      .spyOn(adminApi, 'get')
      .mockResolvedValueOnce({ ...base, status: 'failed', fail_reason: 'transcode: input has no video stream' })
      .mockResolvedValue({ ...base, status: 'queued' });
    const retry = vi.spyOn(adminApi, 'retry').mockResolvedValue({ id: 'v1', status: 'queued' });
    renderPage();
    expect(await screen.findByText('transcode: input has no video stream')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Thử lại' }));
    expect(await screen.findByText('Chờ xử lý')).toBeInTheDocument();
    expect(retry).toHaveBeenCalledWith('v1');
    expect(get).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole('button', { name: 'Xoá video' })).toBeNull(); // queued: không xoá được
  });

  it('upload phụ đề kiểm tra mã ngôn ngữ rồi gọi API', async () => {
    vi.spyOn(adminApi, 'get').mockResolvedValue(base);
    const up = vi.spyOn(adminApi, 'uploadSubtitle').mockResolvedValue({ lang: 'en', label: 'English', vtt_key: 'v1/subs/en.vtt' });
    renderPage();
    const form = await screen.findByRole('form', { name: 'Phụ đề' });
    fireEvent.change(within(form).getByLabelText('Mã ngôn ngữ'), { target: { value: 'English' } });
    fireEvent.click(within(form).getByRole('button', { name: 'Upload phụ đề' }));
    expect(within(form).getByRole('status')).toHaveTextContent('Mã ngôn ngữ dạng');
    fireEvent.change(within(form).getByLabelText('Mã ngôn ngữ'), { target: { value: 'en' } });
    fireEvent.change(within(form).getByLabelText('Nhãn'), { target: { value: 'English' } });
    const file = new File(['WEBVTT\n'], 'en.vtt');
    fireEvent.change(within(form).getByLabelText(/File phụ đề/), { target: { files: [file] } });
    fireEvent.click(within(form).getByRole('button', { name: 'Upload phụ đề' }));
    expect(await within(form).findByText('Đã upload phụ đề en.')).toBeInTheDocument();
    expect(up).toHaveBeenCalledWith('v1', 'en', 'English', file);
  });

  it('xoá xong quay về danh sách', async () => {
    vi.spyOn(adminApi, 'get').mockResolvedValue(base);
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Xoá video' }));
    expect(await screen.findByText('danh sách')).toBeInTheDocument();
    expect(remove).toHaveBeenCalledWith('v1');
  });
});
```

`web/app/src/components/ErrorBoundary.test.tsx` (sửa — 3 cặp thay thế):

1. Thay:

```tsx
import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Boom(): never {
  throw new Error('Failed to fetch dynamically imported module');
}

describe('ErrorBoundary', () => {
  afterEach(() => vi.restoreAllMocks());

  it('hiện thông báo và nút tải lại khi con ném lỗi', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    // jsdom báo lỗi chưa bắt lên window ('error' event → "Error: Uncaught [...]"); chặn để output sạch.
    const quiet = (e: Event) => e.preventDefault();
    window.addEventListener('error', quiet);
    const reload = vi.fn();
    vi.stubGlobal('location', { ...window.location, reload });
    render(
```

   bằng:

```tsx
import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Boom(): never {
  throw new Error('Failed to fetch dynamically imported module');
}

// jsdom báo lỗi chưa bắt lên window ('error' event → "Error: Uncaught [...]"); chặn để output sạch.
const quiet = (e: Event) => e.preventDefault();

describe('ErrorBoundary', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    window.addEventListener('error', quiet);
  });
  afterEach(() => {
    // Dọn ở afterEach để test lỗi giữa chừng cũng không để listener/stub lại cho test sau.
    window.removeEventListener('error', quiet);
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('hiện thông báo và nút tải lại khi con ném lỗi', () => {
    const reload = vi.fn();
    vi.stubGlobal('location', { ...window.location, reload });
    render(
```

2. Thay:

```tsx
    expect(screen.getByText('Không tải được trang. Tải lại?')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Tải lại' }));
    expect(reload).toHaveBeenCalled();
    vi.unstubAllGlobals();
    window.removeEventListener('error', quiet);
  });

  it('render bình thường khi không lỗi', () => {
```

   bằng:

```tsx
    expect(screen.getByText('Không tải được trang. Tải lại?')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Tải lại' }));
    expect(reload).toHaveBeenCalled();
  });

  it('render bình thường khi không lỗi', () => {
```

3. Thay:

```tsx
    );
    expect(screen.getByText('ok')).toBeInTheDocument();
  });
});
```

   bằng:

```tsx
    );
    expect(screen.getByText('ok')).toBeInTheDocument();
  });

  it('đổi resetKey (sang route khác) thì bỏ trạng thái lỗi', () => {
    const { rerender } = render(
      <ErrorBoundary resetKey="/watch/1">
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByRole('alert')).toBeInTheDocument();
    rerender(
      <ErrorBoundary resetKey="/watch/1">
        <p>trang khác</p>
      </ErrorBoundary>,
    );
    expect(screen.getByRole('alert')).toBeInTheDocument(); // cùng key: vẫn giữ lỗi
    rerender(
      <ErrorBoundary resetKey="/search">
        <p>trang khác</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('trang khác')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `cd web/app && npx vitest run src/admin/AdminUploadPage.test.tsx src/admin/AdminVideoPage.test.tsx src/components/ErrorBoundary.test.tsx 2>&1 | tail -4`
Expected: FAIL — hai file trang không resolve được import; `ErrorBoundary` ca `đổi resetKey (sang route khác) thì bỏ trạng thái lỗi` fail (`Unable to find an element with the text: trang khác`).

- [ ] **Step 3: Cài đặt**

`web/app/src/admin/AdminUploadPage.tsx` (mới, toàn văn):

```tsx
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { adminApi } from '../api';
import { formatBytes, parseGenres } from '../format';
import { errorMessage } from './AdminVideosPage';
import { UploadVideoError, uploadVideo } from './upload';

// Cùng danh sách đuôi file và giới hạn với server (internal/upload: .mp4/.mov/.mkv/.webm, tối đa 10 GB).
export const VIDEO_ACCEPT = '.mp4,.mov,.mkv,.webm';
export const MAX_VIDEO_BYTES = 10 * 1024 ** 3;

// titleFromFilename: "tro_choi-con.muc.mp4" → "tro choi con.muc" (gợi ý tiêu đề, admin sửa được).
export function titleFromFilename(name: string): string {
  return name
    .replace(/\.[^.]+$/, '')
    .replace(/[_-]+/g, ' ')
    .trim();
}

// AdminUploadPage (/admin/upload): chọn file + metadata → upload multipart thẳng lên MinIO (thanh tiến độ)
// → complete → chuyển tới trang chi tiết để theo dõi transcode.
export function AdminUploadPage() {
  const navigate = useNavigate();
  const [file, setFile] = useState<File | null>(null);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [genres, setGenres] = useState('');
  const [year, setYear] = useState('');
  const [progress, setProgress] = useState<{ loaded: number; total: number } | null>(null);
  const [error, setError] = useState('');
  const [failedId, setFailedId] = useState('');
  const abort = useRef<AbortController | null>(null);

  useEffect(() => () => abort.current?.abort(), []); // rời trang thì huỷ upload đang chạy

  const onFile = (f: File | null) => {
    setFile(f);
    if (f && !title) setTitle(titleFromFilename(f.name));
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setFailedId('');
    if (!file) return setError('Chọn file video.');
    if (file.size > MAX_VIDEO_BYTES) return setError('File tối đa 10 GB.');
    if (!title.trim()) return setError('Nhập tiêu đề.');
    const y = year.trim() ? Number(year) : 0;
    if (!Number.isInteger(y) || (y !== 0 && (y < 1888 || y > 2100))) return setError('Năm phải từ 1888 đến 2100.');

    abort.current = new AbortController();
    setProgress({ loaded: 0, total: file.size });
    try {
      const id = await uploadVideo(
        file,
        { title: title.trim(), description: description.trim(), genres: parseGenres(genres), year: y || undefined },
        { signal: abort.current.signal, onProgress: (loaded, total) => setProgress({ loaded, total }) },
      );
      navigate(`/admin/videos/${id}`);
    } catch (err) {
      setProgress(null);
      if (err instanceof UploadVideoError) {
        setFailedId(err.videoId);
        setError(`Upload lỗi: ${err.message}`);
      } else {
        setError(`Không tạo được video: ${errorMessage(err)}`);
      }
    }
  };

  const discard = async () => {
    try {
      await adminApi.remove(failedId);
      setFailedId('');
      setError('Đã xoá video upload dở.');
    } catch (err) {
      setError(`Không xoá được: ${errorMessage(err)}`);
    }
  };

  const uploading = progress !== null;
  const percent = progress && progress.total > 0 ? Math.floor((progress.loaded / progress.total) * 100) : 0;
  return (
    <section className="admin">
      <p>
        <Link to="/admin">← Danh sách video</Link>
      </p>
      <h1>Tải video lên</h1>
      <form className="admin-form" onSubmit={submit} noValidate>
        <label>
          File video ({VIDEO_ACCEPT}, tối đa 10 GB)
          <input
            type="file"
            accept={VIDEO_ACCEPT}
            disabled={uploading}
            onChange={(e) => onFile(e.target.files?.[0] ?? null)}
          />
        </label>
        {file && <p className="meta">{`${file.name} · ${formatBytes(file.size)}`}</p>}
        <label>
          Tiêu đề
          <input value={title} maxLength={200} disabled={uploading} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label>
          Mô tả
          <textarea value={description} rows={3} disabled={uploading} onChange={(e) => setDescription(e.target.value)} />
        </label>
        <label>
          Thể loại (cách nhau bằng dấu phẩy)
          <input value={genres} disabled={uploading} onChange={(e) => setGenres(e.target.value)} />
        </label>
        <label>
          Năm
          <input value={year} inputMode="numeric" disabled={uploading} onChange={(e) => setYear(e.target.value)} />
        </label>
        {progress && (
          <div className="upload-progress">
            <progress max={100} value={percent} aria-label="Tiến độ upload" />
            <span>
              {percent}% · {formatBytes(progress.loaded)} / {formatBytes(progress.total)}
            </span>
          </div>
        )}
        {error && (
          <p role="alert" className="form-error">
            {error}
          </p>
        )}
        {failedId && (
          <button type="button" className="danger" onClick={() => void discard()}>
            Xoá video upload dở
          </button>
        )}
        <button type="submit" disabled={uploading}>
          {uploading ? 'Đang upload…' : 'Upload'}
        </button>
      </form>
    </section>
  );
}
```

`web/app/src/admin/AdminVideoPage.tsx` (mới, toàn văn):

```tsx
import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ApiError, adminApi, type AdminVideo } from '../api';
import { formatBytes, formatDuration, parseGenres } from '../format';
import { POLL_MS, errorMessage } from './AdminVideosPage';
import { StatusChip, isProcessing } from './StatusChip';

// Cùng quy tắc với server (catalog.LangRe): "vi", "en", "pt-BR".
export const LANG_RE = /^[a-z]{2,3}(-[A-Z]{2})?$/;

function MetadataForm({ video, onSaved }: { video: AdminVideo; onSaved: () => void }) {
  const [title, setTitle] = useState(video.title);
  const [description, setDescription] = useState(video.description);
  const [genres, setGenres] = useState(video.genres.join(', '));
  const [year, setYear] = useState(video.year ? String(video.year) : '');
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const y = year.trim() ? Number(year) : 0;
    if (!title.trim()) return setMsg('Nhập tiêu đề.');
    if (!Number.isInteger(y) || (y !== 0 && (y < 1888 || y > 2100))) return setMsg('Năm phải từ 1888 đến 2100.');
    setSaving(true);
    try {
      await adminApi.update(video.id, {
        title: title.trim(),
        description: description.trim(),
        genres: parseGenres(genres),
        year: y,
      });
      onSaved();
      setMsg('Đã lưu.');
    } catch (err) {
      setMsg(`Không lưu được: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form className="admin-form" onSubmit={submit} noValidate aria-label="Thông tin phim">
      <h2>Thông tin phim</h2>
      <label>
        Tiêu đề
        <input value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
      </label>
      <label>
        Mô tả
        <textarea value={description} rows={3} onChange={(e) => setDescription(e.target.value)} />
      </label>
      <label>
        Thể loại (cách nhau bằng dấu phẩy)
        <input value={genres} onChange={(e) => setGenres(e.target.value)} />
      </label>
      <label>
        Năm
        <input value={year} inputMode="numeric" onChange={(e) => setYear(e.target.value)} />
      </label>
      {msg && <p role="status">{msg}</p>}
      <button type="submit" disabled={saving}>
        Lưu
      </button>
    </form>
  );
}

function SubtitleForm({ videoId, onUploaded }: { videoId: string; onUploaded: () => void }) {
  const [lang, setLang] = useState('vi');
  const [label, setLabel] = useState('Tiếng Việt');
  const [file, setFile] = useState<File | null>(null);
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!LANG_RE.test(lang)) return setMsg('Mã ngôn ngữ dạng "vi", "en" hoặc "pt-BR".');
    if (!label.trim()) return setMsg('Nhập nhãn hiển thị.');
    if (!file) return setMsg('Chọn file .srt hoặc .vtt.');
    setSaving(true);
    try {
      await adminApi.uploadSubtitle(videoId, lang, label.trim(), file);
      setMsg(`Đã upload phụ đề ${lang}.`);
      onUploaded();
    } catch (err) {
      setMsg(`Không upload được: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form className="admin-form" onSubmit={submit} noValidate aria-label="Phụ đề">
      <h2>Thêm phụ đề</h2>
      <label>
        Mã ngôn ngữ
        <input value={lang} onChange={(e) => setLang(e.target.value)} />
      </label>
      <label>
        Nhãn
        <input value={label} maxLength={50} onChange={(e) => setLabel(e.target.value)} />
      </label>
      <label>
        File phụ đề (.srt, .vtt, tối đa 1 MiB)
        <input type="file" accept=".srt,.vtt" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
      </label>
      {msg && <p role="status">{msg}</p>}
      <button type="submit" disabled={saving}>
        Upload phụ đề
      </button>
    </form>
  );
}

// AdminVideoPage (/admin/videos/:id): trạng thái (tự cập nhật khi đang xử lý), lý do lỗi + thử lại,
// sửa metadata, phụ đề, xoá.
export function AdminVideoPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const [video, setVideo] = useState<AdminVideo | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setVideo(await adminApi.get(id));
      setError('');
    } catch (err) {
      setError(err instanceof ApiError && err.status === 404 ? 'Không tìm thấy video.' : errorMessage(err));
    }
  }, [id]);

  useEffect(() => {
    setVideo(null);
    void load();
  }, [load]);

  const polling = video ? isProcessing(video.status) : false;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(t);
  }, [polling, load]);

  const retry = async () => {
    try {
      await adminApi.retry(id);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const remove = async () => {
    if (!video || !window.confirm(`Xoá "${video.title}"? Không hoàn tác được.`)) return;
    try {
      await adminApi.remove(id);
      navigate('/admin', { replace: true });
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  if (!video) {
    return error ? (
      <p role="alert" className="form-error">
        {error}
      </p>
    ) : (
      <p className="status">Đang tải…</p>
    );
  }
  return (
    <section className="admin">
      <p>
        <Link to="/admin">← Danh sách video</Link>
      </p>
      <div className="admin-head">
        <h1>{video.title}</h1>
        <StatusChip status={video.status} />
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {video.status === 'failed' && (
        <div className="fail-box">
          <p className="fail-reason">{video.fail_reason || 'Không rõ lý do.'}</p>
          <button type="button" onClick={() => void retry()}>
            Thử lại
          </button>
        </div>
      )}
      {polling && <p className="meta">Đang xử lý, tự cập nhật mỗi {POLL_MS / 1000} giây…</p>}
      <p className="meta">
        {[
          formatBytes(video.size_bytes),
          video.duration_s ? formatDuration(video.duration_s) : '',
          video.renditions?.map((r) => `${r.height}p`).join(', '),
        ]
          .filter(Boolean)
          .join(' · ')}
      </p>
      {video.status === 'ready' && (
        <p>
          <Link to={`/watch/${video.id}`} className="button">
            ▶ Xem phim
          </Link>
        </p>
      )}
      <div className="admin-columns">
        {/* key theo id: poll hay lưu xong không xoá chữ admin đang gõ dở */}
        <MetadataForm key={video.id} video={video} onSaved={() => void load()} />
        <div>
          <h2>Phụ đề</h2>
          {video.subtitles && video.subtitles.length > 0 ? (
            <ul className="subtitle-list">
              {video.subtitles.map((s) => (
                <li key={s.lang}>
                  {s.label} ({s.lang})
                </li>
              ))}
            </ul>
          ) : (
            <p className="meta">Chưa có phụ đề.</p>
          )}
          <SubtitleForm videoId={video.id} onUploaded={() => void load()} />
        </div>
      </div>
      {!isProcessing(video.status) && (
        <p>
          <button type="button" className="danger" onClick={() => void remove()}>
            Xoá video
          </button>
        </p>
      )}
    </section>
  );
}
```

`web/app/src/components/ErrorBoundary.tsx` (sửa — 2 cặp thay thế):

1. Thay:

```tsx
import { Component, type ErrorInfo, type ReactNode } from 'react';

// ErrorBoundary bọc các route tải lazy: sau khi deploy bản mới, chunk cũ không còn trên server nên
// import() lỗi; hiện thông báo và nút tải lại thay vì trang trắng.
export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
```

   bằng:

```tsx
import { Component, type ErrorInfo, type ReactNode } from 'react';

interface Props {
  children: ReactNode;
  // resetKey đổi (Layout truyền pathname) thì bỏ trạng thái lỗi: lỗi của một trang không chặn các trang khác.
  resetKey?: unknown;
}

// ErrorBoundary bọc các route tải lazy: sau khi deploy bản mới, chunk cũ không còn trên server nên
// import() lỗi; hiện thông báo và nút tải lại thay vì trang trắng.
export class ErrorBoundary extends Component<Props, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
```

2. Thay:

```tsx
    console.error('route render failed', error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
```

   bằng:

```tsx
    console.error('route render failed', error, info.componentStack);
  }

  componentDidUpdate(prev: Props) {
    if (this.state.failed && prev.resetKey !== this.props.resetKey) this.setState({ failed: false });
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
```

`web/app/src/App.tsx` (sửa — 3 cặp thay thế):

1. Thay:

```tsx
import { Suspense, lazy } from 'react';
import { Link, Outlet, Route, Routes } from 'react-router-dom';
import { RequireAuth } from './auth';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Header } from './components/Header';
```

   bằng:

```tsx
import { Suspense, lazy } from 'react';
import { Link, Outlet, Route, Routes, useLocation } from 'react-router-dom';
import { RequireAdmin } from './admin/RequireAdmin';
import { RequireAuth } from './auth';
import { ErrorBoundary } from './components/ErrorBoundary';
import { Header } from './components/Header';
```

2. Thay:

```tsx

// Trang xem tải lazy: hls.js (~500 kB) chỉ được tải khi người dùng mở player.
const WatchPage = lazy(() => import('./pages/WatchPage').then((m) => ({ default: m.WatchPage })));

function Layout() {
  return (
    <>
      <Header />
      <main className="content">
        <ErrorBoundary>
          <Suspense fallback={<p className="status">Đang tải…</p>}>
            <Outlet />
          </Suspense>
```

   bằng:

```tsx

// Trang xem tải lazy: hls.js (~500 kB) chỉ được tải khi người dùng mở player.
const WatchPage = lazy(() => import('./pages/WatchPage').then((m) => ({ default: m.WatchPage })));
// Trang quản trị (M7) tải lazy: người xem không phải tải code admin.
const AdminVideosPage = lazy(() => import('./admin/AdminVideosPage').then((m) => ({ default: m.AdminVideosPage })));
const AdminUploadPage = lazy(() => import('./admin/AdminUploadPage').then((m) => ({ default: m.AdminUploadPage })));
const AdminVideoPage = lazy(() => import('./admin/AdminVideoPage').then((m) => ({ default: m.AdminVideoPage })));

function Layout() {
  const { pathname } = useLocation();
  return (
    <>
      <Header />
      <main className="content">
        {/* resetKey: sang trang khác thì bỏ trạng thái lỗi của trang trước */}
        <ErrorBoundary resetKey={pathname}>
          <Suspense fallback={<p className="status">Đang tải…</p>}>
            <Outlet />
          </Suspense>
```

3. Thay:

```tsx
        <Route path="search" element={<SearchPage />} />
        <Route path="videos/:id" element={<VideoPage />} />
        <Route path="watch/:id" element={<WatchPage />} />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
```

   bằng:

```tsx
        <Route path="search" element={<SearchPage />} />
        <Route path="videos/:id" element={<VideoPage />} />
        <Route path="watch/:id" element={<WatchPage />} />
        <Route
          path="admin"
          element={
            <RequireAdmin>
              <Outlet />
            </RequireAdmin>
          }
        >
          <Route index element={<AdminVideosPage />} />
          <Route path="upload" element={<AdminUploadPage />} />
          <Route path="videos/:id" element={<AdminVideoPage />} />
        </Route>
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
```

`web/app/src/styles.css` (sửa — 1 cặp thay thế):

1. Thay:

```css
  margin: 0;
  font-size: 0.9rem;
}
```

   bằng:

```css
  margin: 0;
  font-size: 0.9rem;
}

/* ---- Trang quản trị (M7) ---- */
textarea {
  font: inherit;
  color: inherit;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 0.5rem 0.75rem;
  resize: vertical;
}

button.danger {
  background: transparent;
  border: 1px solid var(--accent);
  color: #ff8a8d;
}

.admin-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  margin-bottom: 1rem;
}

.admin-head h1 {
  margin: 0;
}

.admin-table {
  width: 100%;
  border-collapse: collapse;
}

.admin-table th,
.admin-table td {
  text-align: left;
  padding: 0.6rem 0.5rem;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}

.admin-table th {
  color: var(--muted);
  font-weight: 500;
}

.admin-table .actions {
  display: flex;
  gap: 0.5rem;
  justify-content: flex-end;
}

.chip {
  display: inline-block;
  padding: 0.1rem 0.6rem;
  border-radius: 999px;
  font-size: 0.85rem;
  border: 1px solid currentColor;
  white-space: nowrap;
}

.chip-uploading {
  color: var(--muted);
}

.chip-queued {
  color: #f5c451;
}

.chip-processing {
  color: #5eb1ff;
}

.chip-ready {
  color: #4cc38a;
}

.chip-failed {
  color: #ff8a8d;
}

.fail-reason {
  color: #ff8a8d;
  font-size: 0.85rem;
  margin: 0.25rem 0 0;
  word-break: break-word;
}

.fail-box {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
  margin-bottom: 1rem;
}

.admin-form {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  max-width: 560px;
  margin-bottom: 1.5rem;
}

.admin-form label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.admin-form h2,
.admin-columns h2 {
  font-size: 1.1rem;
  margin: 0;
}

.admin-columns {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 2rem;
}

.upload-progress {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.upload-progress progress {
  flex: 1;
  accent-color: var(--accent);
}

.subtitle-list {
  padding-left: 1.2rem;
}
```

- [ ] **Step 4: Chạy test**

Run: `cd web/app && npx tsc --noEmit && npm test 2>&1 | grep -E 'Test Files|Tests|FAIL' && npm run build 2>&1 | grep -E 'Admin|built'`
Expected: `Test Files  19 passed (19)`, `Tests  77 passed (77)`, không có output stderr; build tạo ba chunk riêng `AdminVideosPage-*.js`, `AdminVideoPage-*.js`, `AdminUploadPage-*.js` (mỗi chunk ~3–5 kB) và `✓ built`.

- [ ] **Step 5: Commit**

```bash
git add web/app/src
git commit -m "feat(web): admin upload and video detail pages, /admin routes, ErrorBoundary reset per route" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: E2E: `scripts/e2e-m7.sh`, `scripts/e2e.sh`, `make e2e`, `make e2e-m7`, `make test-all`

**Files:**
- Create: `scripts/e2e-m7.sh`, `scripts/e2e.sh`
- Modify: `Makefile`

**Interfaces:**
- `scripts/e2e-m7.sh [file]` — biến `ADMIN_EMAIL`, `ADMIN_PASSWORD`, `SITE`, `MINIO`, `TIMEOUT`; in `0/6`…`6/6`, cuối `OK  M7  mở $SITE/admin`.
- `scripts/e2e.sh [file]` — biến `E2E_ONLY`, `E2E_LOG_DIR` và mọi biến của các script con (`API`, `SITE`, `CDN`, `MINIO`, `COMPOSE`, `CDN_SIGNING_SECRET`, `CLICKHOUSE_*`); in `=== Tổng kết e2e (Ns) ===`, mỗi dòng `mN   PASS|FAIL  Ns  tmp/e2e-logs/e2e-mN.log`, cuối `OK  5/5 kịch bản qua`.
- Makefile: `e2e-m7`, `e2e` (`ONLY="m4 m7"` tuỳ chọn), `test-all`.

- [ ] **Step 1: Cài đặt**

Sau khi tạo file: `chmod +x scripts/e2e-m7.sh scripts/e2e.sh`.

`scripts/e2e-m7.sh` (mới, toàn văn):

```bash
#!/usr/bin/env bash
# E2E M7: header bảo mật của nginx; CORS của MinIO cho upload từ trình duyệt (preflight PUT từ origin của SPA);
# xoá video (uploading → 204, queued/processing → 409, ready → 204 và HLS bị xoá khỏi MinIO); sửa metadata
# (PATCH) → tìm kiếm thấy tiêu đề mới; video lỗi (file chỉ có tiếng) → failed có fail_reason → retry → failed lại;
# viewer bị 403 với PATCH/DELETE/retry.
# Dùng: scripts/e2e-m7.sh [file] (mặc định tmp/sample.mp4, 30 giây 1080p). Cần: curl, jq, split, ffmpeg.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m7 nạp từ .env); SITE (mặc định http://localhost:8088);
# MINIO (mặc định http://localhost:9000, = S3_PUBLIC_ENDPOINT); TIMEOUT (giây chờ transcode, mặc định 300).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"
MINIO="${MINIO:-http://localhost:9000}"
TIMEOUT="${TIMEOUT:-300}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m7)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m7)}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq split ffmpeg; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
[ -f "$FILE" ] || die "không thấy $FILE (chạy: make sample)"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
ADMIN="$WORK/admin.jar"
VIEWER="$WORK/viewer.jar"
RUN="m7run$(date +%s)"

# code ARGS...: in mã HTTP của curl (không dừng khi 4xx/5xx).
code() { curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@"; }
# expect WANT WHAT ARGS...: so mã HTTP; khi lỗi chỉ in mô tả (không in body hay mật khẩu).
expect() {
  local want=$1 what=$2 got
  shift 2
  got=$(code "$@")
  [ "$got" = "$want" ] || die "$what: HTTP $got, want $want"
}
admin() { curl -fsS --max-time 30 -b "$ADMIN" "$@"; }
# create TITLE FILE: tạo video, in JSON (id + kế hoạch upload).
create() {
  jq -n --arg t "$1" --arg f "$(basename "$2")" --argjson s "$(wc -c < "$2" | tr -d ' ')" \
    '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}' |
    admin -X POST "$SITE/api/admin/videos" -H 'Content-Type: application/json' -d @-
}
# put_parts CREATE_JSON FILE: PUT từng part lên presigned URL (như trình duyệt, kèm Origin của SPA).
put_parts() {
  local plan=$1 file=$2 size i=0 part url
  size=$(echo "$plan" | jq -r .upload.part_size)
  rm -f "$WORK"/part.*
  split -b "$size" -a 4 "$file" "$WORK/part."
  for part in "$WORK"/part.*; do
    i=$((i + 1))
    url=$(echo "$plan" | jq -r --argjson n "$i" '.upload.parts[] | select(.part_number == $n) | .url')
    [ "$(curl -s --max-time 300 -o /dev/null -w '%{http_code}' -H "Origin: $SITE" -X PUT --upload-file "$part" "$url")" = 200 ] ||
      die "PUT part $i"
  done
}
# wait_status ID WANT: đợi tới trạng thái WANT (dừng nếu gặp ready/failed khác WANT), in JSON video.
wait_status() {
  local id=$1 want=$2 v s deadline=$(( $(date +%s) + TIMEOUT ))
  while :; do
    v=$(admin "$SITE/api/admin/videos/$id")
    s=$(echo "$v" | jq -r .status)
    [ "$s" = "$want" ] && { echo "$v"; return; }
    case "$s" in ready | failed) die "video $id: status=$s, want $want ($(echo "$v" | jq -r '.fail_reason // ""'))" ;; esac
    [ "$(date +%s)" -lt "$deadline" ] || die "hết thời gian chờ $id → $want"
    sleep 2
  done
}

echo "0/6 đăng nhập admin, đăng ký viewer"
jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$ADMIN" -X POST "$SITE/api/auth/login" -H 'Content-Type: application/json' -d @- |
  jq -e '.role == "admin"' >/dev/null || die "đăng nhập admin thất bại (chạy: make seed-admin)"
jq -n --arg e "viewer-m7-$(date +%s)-$RANDOM@example.com" '{email: $e, password: "viewer-password-1"}' |
  curl -fsS --max-time 30 -c "$VIEWER" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- >/dev/null ||
  die "đăng ký viewer"

echo "1/6 nginx: header bảo mật trên SPA, /api/, /cdn/, /assets/"
ASSET=$(curl -fsS --max-time 30 "$SITE/" | grep -oE '/assets/index-[^"]+\.js' | head -1)
for path in / /admin "$ASSET" /api/me /cdn/x /healthz; do
  H=$(curl -s --max-time 30 -o /dev/null -D - "$SITE$path" | tr -d '\r')
  for want in 'x-content-type-options: nosniff' 'x-frame-options: DENY' \
    "content-security-policy: frame-ancestors 'none'" 'referrer-policy: same-origin'; do
    echo "$H" | grep -qi "^$want\$" || die "GET $path thiếu header \"$want\""
  done
done
curl -fsS --max-time 30 "$SITE/admin/upload" | grep -q '<div id="root"></div>' || die "/admin/upload không trả SPA"

echo "2/6 upload như trình duyệt: preflight CORS của MinIO, PUT các part, complete"
PLAN=$(create "M7 $RUN" "$FILE")
ID=$(echo "$PLAN" | jq -r .id)
PART1=$(echo "$PLAN" | jq -r '.upload.parts[0].url')
PRE=$(curl -s --max-time 30 -o /dev/null -D - -X OPTIONS "$PART1" -H "Origin: $SITE" \
  -H 'Access-Control-Request-Method: PUT' | tr -d '\r')
echo "$PRE" | grep -qi "^access-control-allow-origin: $SITE\$" ||
  die "MinIO không cho PUT từ $SITE (MINIO_CORS_ALLOW_ORIGIN): $(echo "$PRE" | head -1)"
EVIL=$(curl -s --max-time 30 -o /dev/null -D - -X OPTIONS "$PART1" -H 'Origin: http://evil.example' \
  -H 'Access-Control-Request-Method: PUT' | tr -d '\r')
if echo "$EVIL" | grep -qi '^access-control-allow-origin: http://evil.example$'; then
  die "MinIO cho phép origin lạ http://evil.example"
fi
put_parts "$PLAN" "$FILE"
admin -X POST "$SITE/api/admin/videos/$ID/complete" | jq -e '.status == "queued"' >/dev/null || die "complete"
echo "    id=$ID queued"

echo "3/6 xoá: đang xử lý → 409; uploading → 204; viewer → 403"
expect 409 "DELETE video đang xử lý" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$ID"
expect 403 "viewer DELETE" -b "$VIEWER" -X DELETE "$SITE/api/admin/videos/$ID"
TMP_ID=$(create "M7 bỏ dở $RUN" "$FILE" | jq -r .id)
expect 204 "DELETE video uploading" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$TMP_ID"
expect 404 "GET video đã xoá" -b "$ADMIN" "$SITE/api/admin/videos/$TMP_ID"
wait_status "$ID" ready >/dev/null
echo "    $ID ready"

echo "4/6 PATCH metadata → tìm kiếm không dấu thấy tiêu đề mới; dữ liệu sai → 400; viewer → 403"
NEW_TITLE="Hạ cánh khẩn cấp $RUN"
jq -n --arg t "$NEW_TITLE" '{title: $t, genres: ["Tình cảm", "drama"], year: 2019}' |
  admin -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d @- |
  jq -e --arg t "$NEW_TITLE" '.title == $t and .genres == ["Tình cảm", "drama"] and .year == 2019 and .status == "ready"' \
    >/dev/null || die "PATCH"
curl -fsS --max-time 30 -b "$VIEWER" -G "$SITE/api/videos" --data-urlencode "q=ha canh khan cap $RUN" |
  jq -e --arg id "$ID" '[.videos[].id] == [$id]' >/dev/null || die "tìm tiêu đề mới sau PATCH"
expect 400 "PATCH year sai" -b "$ADMIN" -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d '{"year":1500}'
expect 400 "PATCH rỗng" -b "$ADMIN" -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d '{}'
expect 403 "viewer PATCH" -b "$VIEWER" -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d '{"title":"x"}'

echo "5/6 video lỗi: file chỉ có tiếng → failed + fail_reason; viewer retry → 403; retry → failed lại"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i sine=frequency=440:sample_rate=48000 -t 2 -c:a aac \
  "$WORK/audio-only.mp4"
BAD=$(create "M7 lỗi $RUN" "$WORK/audio-only.mp4")
BAD_ID=$(echo "$BAD" | jq -r .id)
put_parts "$BAD" "$WORK/audio-only.mp4"
admin -X POST "$SITE/api/admin/videos/$BAD_ID/complete" >/dev/null
wait_status "$BAD_ID" failed | jq -e '.fail_reason | test("no video stream")' >/dev/null || die "fail_reason"
admin "$SITE/api/admin/videos?status=failed" | jq -e --arg id "$BAD_ID" '[.videos[].id] | index($id) != null' >/dev/null ||
  die "danh sách ?status=failed thiếu $BAD_ID"
expect 403 "viewer retry" -b "$VIEWER" -X POST "$SITE/api/admin/videos/$BAD_ID/retry"
admin -X POST "$SITE/api/admin/videos/$BAD_ID/retry" | jq -e '.status == "queued"' >/dev/null || die "retry"
expect 409 "retry video không failed" -b "$ADMIN" -X POST "$SITE/api/admin/videos/$ID/retry"
wait_status "$BAD_ID" failed >/dev/null
echo "    $BAD_ID failed lại sau retry"

echo "6/6 xoá video ready và failed: 204, /play 404, HLS bị xoá khỏi MinIO"
expect 200 "master.m3u8 trên MinIO trước khi xoá" "$MINIO/hls/$ID/master.m3u8"
expect 204 "DELETE video ready" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$ID"
expect 204 "DELETE video failed" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$BAD_ID"
expect 404 "/play video đã xoá" -b "$VIEWER" "$SITE/api/videos/$ID/play"
expect 404 "master.m3u8 trên MinIO sau khi xoá" "$MINIO/hls/$ID/master.m3u8"
admin "$SITE/api/admin/videos" | jq -e --arg a "$ID" --arg b "$BAD_ID" '[.videos[].id] | (index($a) == null and index($b) == null)' \
  >/dev/null || die "video đã xoá còn trong danh sách admin"

echo "OK  M7  mở $SITE/admin"
```

`scripts/e2e.sh` (mới, toàn văn):

```bash
#!/usr/bin/env bash
# E2E toàn bộ MVP (spec §9): chạy lần lượt các kịch bản theo giai đoạn rồi in bảng tổng kết.
#   m3  auth, phân quyền, upload → transcode → ready (gồm e2e-m2), /play, URL ký sai/hết hạn → 403
#   m4  SPA + /api/ cùng origin qua nginx, tìm kiếm có dấu/không dấu/gõ sai
#   m5  poster, sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp", playback.events trong Kafka
#   m6  playback_events + app_logs trong ClickHouse, record hỏng bị bỏ qua, SQL mẫu
#   m7  header bảo mật, CORS MinIO cho upload từ trình duyệt, PATCH/DELETE, failed → retry
# Kịch bản lỗi không dừng các kịch bản sau; mã thoát 1 nếu có kịch bản lỗi.
# Dùng: scripts/e2e.sh [file] (mặc định tmp/sample.mp4). Biến: như các script e2e-m*.sh (make e2e nạp .env);
# E2E_ONLY="m4 m7" để chạy một phần; log đầy đủ của từng kịch bản ở $E2E_LOG_DIR (mặc định tmp/e2e-logs).
set -uo pipefail

FILE="${1:-tmp/sample.mp4}"
DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="${E2E_LOG_DIR:-tmp/e2e-logs}"
STEPS=(${E2E_ONLY:-m3 m4 m5 m6 m7})
mkdir -p "$LOG_DIR"

declare -a RESULTS=()
failed=0
start_all=$(date +%s)
for s in "${STEPS[@]}"; do
  script="$DIR/e2e-$s.sh"
  [ -x "$script" ] || { echo "không có $script" >&2; exit 2; }
  log="$LOG_DIR/e2e-$s.log"
  echo "=== e2e-$s ==="
  t0=$(date +%s)
  if "$script" "$FILE" 2>&1 | tee "$log"; then
    status=PASS
  else
    status=FAIL
    failed=$((failed + 1))
  fi
  RESULTS+=("$(printf '%-4s %-4s %4ss  %s' "$s" "$status" "$(( $(date +%s) - t0 ))" "$log")")
done

echo
echo "=== Tổng kết e2e ($(( $(date +%s) - start_all ))s) ==="
printf '%s\n' "${RESULTS[@]}"
if [ "$failed" -gt 0 ]; then
  echo "FAIL: $failed/${#STEPS[@]} kịch bản lỗi (xem log ở trên)"
  exit 1
fi
echo "OK  ${#STEPS[@]}/${#STEPS[@]} kịch bản qua"
```

`Makefile` (sửa — 2 cặp thay thế):

1. Thay:

```makefile
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker run-analytics \
	e2e-m2 e2e-m3 e2e-m4 e2e-m5 e2e-m6 seed-admin sign-url analytics-sql web-install web-dev web-test web-build

# Tạo .env từ .env.example; nếu .env đã có thì bổ sung các khoá mới (vd. khoá M3) mà không ghi đè giá trị cũ.
env:
```

   bằng:

```makefile
COMPOSE := docker compose -f deploy/docker-compose.yml --env-file .env

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker run-analytics \
	e2e e2e-m2 e2e-m3 e2e-m4 e2e-m5 e2e-m6 e2e-m7 test-all seed-admin sign-url analytics-sql \
	web-install web-dev web-test web-build

# Tạo .env từ .env.example; nếu .env đã có thì bổ sung các khoá mới (vd. khoá M3) mà không ghi đè giá trị cũ.
env:
```

2. Thay:

```makefile
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; COMPOSE="$(COMPOSE)" E2E_CLICKHOUSE_DOWN="$(CH_DOWN)" ./scripts/e2e-m6.sh tmp/sample.mp4

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
WEB := web/app

```

   bằng:

```makefile
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; COMPOSE="$(COMPOSE)" E2E_CLICKHOUSE_DOWN="$(CH_DOWN)" ./scripts/e2e-m6.sh tmp/sample.mp4

# E2E M7: header bảo mật nginx, CORS MinIO cho upload từ trình duyệt, PATCH/DELETE video, failed → retry.
e2e-m7: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; MINIO="$${S3_PUBLIC_ENDPOINT:-http://localhost:9000}" ./scripts/e2e-m7.sh tmp/sample.mp4

# E2E toàn bộ MVP: e2e-m3 (gồm m2) → m4 → m5 → m6 → m7, in bảng tổng kết; log ở tmp/e2e-logs/.
# Chạy một phần: make e2e ONLY="m4 m7".
e2e: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; COMPOSE="$(COMPOSE)" E2E_ONLY="$(ONLY)" MINIO="$${S3_PUBLIC_ENDPOINT:-http://localhost:9000}" \
	  ./scripts/e2e.sh tmp/sample.mp4

# Toàn bộ test: Go (unit + integration bằng testcontainers, cần Docker) rồi Vitest của web.
test-all: test
	@test -d $(WEB)/node_modules || $(MAKE) web-install
	$(MAKE) web-test

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
WEB := web/app

```

- [ ] **Step 2: Dựng lại stack với code M7 và chạy e2e**

Run: `bash -n scripts/e2e.sh scripts/e2e-m7.sh && make up && make e2e-m7`
Expected: `make up` build lại image `api`, `transcoder`, `analytics`, `nginx` (`:dev`) và mọi service healthy/running; `make e2e-m7` in `0/6` … `6/6` rồi `OK  M7  mở http://localhost:8088/admin` (khoảng 30 giây: video mẫu 30 giây transcode trong bước 3).

Run: `make e2e`
Expected (khoảng 2,5–3 phút):
```
=== Tổng kết e2e (152s) ===
m3   PASS   25s  tmp/e2e-logs/e2e-m3.log
m4   PASS   24s  tmp/e2e-logs/e2e-m4.log
m5   PASS   46s  tmp/e2e-logs/e2e-m5.log
m6   PASS   27s  tmp/e2e-logs/e2e-m6.log
m7   PASS   30s  tmp/e2e-logs/e2e-m7.log
OK  5/5 kịch bản qua
```
(thời gian thay đổi theo máy). Nếu `e2e-m7` bước 2 báo `MinIO không cho PUT từ http://localhost:8088`: MinIO chưa được tạo lại với biến mới — `docker compose -f deploy/docker-compose.yml --env-file .env up -d minio` rồi chạy lại.

Run: `make test-all 2>&1 | grep -E '^(ok|FAIL)|Tests '`
Expected: mọi package Go `ok` (integration mất vài phút), dòng cuối `Tests  77 passed (77)`.

- [ ] **Step 3: Commit**

```bash
git add scripts/e2e-m7.sh scripts/e2e.sh Makefile
git commit -m "test(e2e): M7 admin API e2e and single make e2e with summary; make test-all" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Tài liệu (1): `api.md`, `data-model.md`, `flows.md`, ADR 0019, ADR 0020

**Files:**
- Create: `docs/decisions/0019-admin-browser-upload.md`, `docs/decisions/0020-video-delete-and-edit.md`
- Modify: `docs/api.md`, `docs/data-model.md`, `docs/flows.md`

- [ ] **Step 1: Cài đặt**

`docs/api.md` (sửa — 5 cặp thay thế):

1. Thay:

```markdown
| POST | `/api/admin/videos/{id}/complete` | admin | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | admin | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | admin | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | admin | Chi tiết video kèm `renditions` |

## POST /api/auth/register

```

   bằng:

```markdown
| POST | `/api/admin/videos/{id}/complete` | admin | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | admin | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | admin | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | admin | Chi tiết video kèm `renditions`, `subtitles` |
| PATCH | `/api/admin/videos/{id}` | admin | Sửa `title`, `description`, `genres`, `year` (M7) |
| DELETE | `/api/admin/videos/{id}` | admin | Xoá video, rendition, phụ đề, tiến độ xem và object trên MinIO (M7) |

Trang quản trị trên web (`/admin`, M7) dùng đúng các endpoint admin ở trên.

## POST /api/auth/register

```

2. Thay:

````markdown
{"title":"Phim A","description":"","genres":["drama"],"year":2024,"filename":"movie.mp4","size_bytes":157286400}
```

`filename` phải kết thúc bằng `.mp4`, `.mov`, `.mkv` hoặc `.webm`. `size_bytes` từ 1 tới 10 GB.

`201`:

````

   bằng:

````markdown
{"title":"Phim A","description":"","genres":["drama"],"year":2024,"filename":"movie.mp4","size_bytes":157286400}
```

`filename` phải kết thúc bằng `.mp4`, `.mov`, `.mkv` hoặc `.webm`. `size_bytes` từ 1 tới 10 GB. Metadata được chuẩn hoá như `PATCH` (cắt khoảng trắng, bỏ thể loại rỗng/trùng) và kiểm tra cùng giới hạn: `title` 1–200 ký tự, `description` ≤ 5000, tối đa 10 `genres` mỗi cái ≤ 50 ký tự, `year` 0 (không rõ) hoặc 1888–2100.

`201`:

````

3. Thay:

````markdown
 "expires_at":"2026-11-14T11:00:00Z"}}
```

Client cắt file thành các khúc `part_size` byte (khúc cuối nhỏ hơn) và `PUT` khúc thứ n vào `url` của `part_number` n. Không cần gửi lại ETag: API tự liệt kê part khi complete.

Lỗi: `400` body sai / thiếu `title` / kích thước hoặc đuôi file không hợp lệ.

## POST /api/admin/videos/{id}/complete

````

   bằng:

````markdown
 "expires_at":"2026-11-14T11:00:00Z"}}
```

Client cắt file thành các khúc `part_size` byte (khúc cuối nhỏ hơn) và `PUT` khúc thứ n vào `url` của `part_number` n. Không cần gửi lại ETag: API tự liệt kê part khi complete. Từ trình duyệt, `PUT` là cross-origin tới MinIO: origin của trang phải nằm trong `MINIO_CORS_ALLOW_ORIGIN` ([ADR 0019](decisions/0019-admin-browser-upload.md)).

Lỗi: `400` body sai / metadata vượt giới hạn / kích thước hoặc đuôi file không hợp lệ.

## POST /api/admin/videos/{id}/complete

````

4. Thay:

```markdown

## POST /api/admin/videos/{id}/retry

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`. `409` nếu video không ở `failed`, `404`, `503` như trên.

## GET /api/admin/videos

```

   bằng:

```markdown

## POST /api/admin/videos/{id}/retry

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`. `409` nếu video không ở `failed`, `404`, `503` như trên. `fail_reason` cũ được giữ tới khi video `ready` (ghi đè nếu lỗi lại).

## GET /api/admin/videos

```

5. Thay:

````markdown
{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,"status":"ready",
 "duration_s":30,"size_bytes":157286400,"hls_prefix":"7c1e…/",
 "created_at":"…","updated_at":"…",
 "renditions":[{"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"}]}
```
````

   bằng:

````markdown
{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,"status":"ready",
 "duration_s":30,"size_bytes":157286400,"hls_prefix":"7c1e…/",
 "created_at":"…","updated_at":"…",
 "renditions":[{"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"}],
 "subtitles":[{"lang":"vi","label":"Tiếng Việt","vtt_key":"7c1e…/subs/vi.vtt"}]}
```

`subtitles` sắp theo `lang`, `[]` khi chưa có. `404` nếu không có video hoặc `{id}` không phải UUID.

## PATCH /api/admin/videos/{id}

Gửi field cần đổi; field vắng giữ nguyên. `"year": 0` xoá năm, `"genres": []` xoá thể loại. Dùng được ở mọi trạng thái (kể cả đang transcode).

```json
{"title":"Hạ cánh nơi anh","genres":["Tình cảm","drama"],"year":2019}
```

`200`: video như `GET /api/admin/videos` (không kèm `renditions`, `subtitles`). Tìm kiếm thấy tiêu đề/mô tả mới ngay (trigger cập nhật cột `search`).

| Mã | Khi nào |
|---|---|
| `400` | `{}` (`nothing to update`), JSON sai hoặc có field lạ (ví dụ `status`), metadata vượt giới hạn như khi tạo video |
| `404` | Không có video hoặc `{id}` không phải UUID |

Hai admin sửa cùng lúc: bản ghi sau thắng ([ADR 0020](decisions/0020-video-delete-and-edit.md)).

## DELETE /api/admin/videos/{id}

Không có body. `204` khi video ở `uploading`, `ready` hoặc `failed`: xoá dòng `videos` (cascade `renditions`, `subtitles`, `watch_progress`), rồi huỷ multipart upload dở (nếu còn `uploading`) và xoá `raw/{id}/`, `hls/{id}/` trên MinIO. Lỗi MinIO chỉ ghi log (`delete objects after delete failed`), vẫn `204`.

| Mã | Khi nào |
|---|---|
| `409` | Video đang `queued` hoặc `processing` (`video is being processed: video is queued`): đợi `ready`/`failed` rồi xoá |
| `404` | Không có video (kể cả đã xoá) hoặc `{id}` không phải UUID |

Sau khi xoá, `/play` và `/api/videos/{id}` trả `404`; segment đã nằm trong cache nginx có thể còn phục vụ cho URL ký cũ tới khi hết hạn ([ADR 0020](decisions/0020-video-delete-and-edit.md)).
````

`docs/data-model.md` (sửa — 3 cặp thay thế):

1. Thay:

````markdown
# Data model

## Postgres (tới M5)

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp).

```sql
users (                    -- M3, migrations/0002_users.sql
````

   bằng:

````markdown
# Data model

## Postgres

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp, [ADR 0005](decisions/0005-embedded-migrations.md)):

| File | Giai đoạn | Nội dung |
|---|---|---|
| `0001_videos.sql` | M2 | `videos`, `renditions`, index theo `created_at` và `(status, created_at)` |
| `0002_users.sql` | M3 | `users` |
| `0003_search.sql` | M4 | extension `unaccent`, `pg_trgm`; hàm `immutable_unaccent`, `videos_search_tsv`; cột `videos.search` + trigger; index GIN |
| `0004_subtitles_progress.sql` | M5 | `subtitles`, `watch_progress` |

M7 không thêm migration: sửa metadata ghi các cột có sẵn, xoá video dựa vào `ON DELETE CASCADE` của các bảng con.

```sql
users (                    -- M3, migrations/0002_users.sql
````

2. Thay:

````markdown
  queued --> failed: video.failed
  processing --> failed: video.failed
  failed --> queued: /retry
  uploading --> [*]: dọn sau 24 giờ
```

Mọi chuyển trạng thái đi qua `catalog.CanTransition` trong cùng transaction với `SELECT … FOR UPDATE`. Chuyển không hợp lệ (sự kiện trùng hoặc đến muộn) bị bỏ qua và ghi log. Một trường hợp đặc biệt: `video.processing` và `video.transcoded` nằm ở hai topic khác nhau nên `video.transcoded` có thể đến khi video còn `queued`; khi đó api đi `queued → processing → ready` (máy trạng thái không đổi). Ngoại lệ duy nhất: khi publish `video.uploaded` lỗi, `queued` được trả về trạng thái trước đó (bước bù trừ).

## Kafka
````

   bằng:

````markdown
  queued --> failed: video.failed
  processing --> failed: video.failed
  failed --> queued: /retry
  uploading --> [*]: dọn sau 24 giờ / DELETE
  ready --> [*]: DELETE
  failed --> [*]: DELETE
```

`DELETE /api/admin/videos/{id}` (M7) không phải chuyển trạng thái mà xoá dòng; video `queued`/`processing` bị từ chối (`409`, [ADR 0020](decisions/0020-video-delete-and-edit.md)). `PATCH` không đổi `status`.

Mọi chuyển trạng thái đi qua `catalog.CanTransition` trong cùng transaction với `SELECT … FOR UPDATE`. Chuyển không hợp lệ (sự kiện trùng hoặc đến muộn) bị bỏ qua và ghi log. Một trường hợp đặc biệt: `video.processing` và `video.transcoded` nằm ở hai topic khác nhau nên `video.transcoded` có thể đến khi video còn `queued`; khi đó api đi `queued → processing → ready` (máy trạng thái không đổi). Ngoại lệ duy nhất: khi publish `video.uploaded` lỗi, `queued` được trả về trạng thái trước đó (bước bù trừ).

## Kafka
````

3. Thay:

```markdown
- `app_logs`: `time` của dòng log → `ts`; `level`, `msg`, `service`, `request_id`, `video_id` thành cột (thiếu thì chuỗi rỗng); mọi trường còn lại thành JSON trong `attrs` (đọc bằng `JSONExtractString(attrs, 'path')`, `JSONExtractInt(attrs, 'status')`…). Giữ 30 ngày.
- Giờ lưu UTC; analytics gửi dạng `YYYY-MM-DD hh:mm:ss.sss`.

SQL mẫu: `scripts/analytics/*.sql` (chạy bằng `make analytics-sql`, xem [operations](operations.md#analytics-và-log-m6)).
```

   bằng:

```markdown
- `app_logs`: `time` của dòng log → `ts`; `level`, `msg`, `service`, `request_id`, `video_id` thành cột (thiếu thì chuỗi rỗng); mọi trường còn lại thành JSON trong `attrs` (đọc bằng `JSONExtractString(attrs, 'path')`, `JSONExtractInt(attrs, 'status')`…). Giữ 30 ngày.
- Giờ lưu UTC; analytics gửi dạng `YYYY-MM-DD hh:mm:ss.sss`.

SQL mẫu: `scripts/analytics/*.sql` (chạy bằng `make analytics-sql`, xem [operations](operations.md#analytics-và-log-m6)). `completion_rate.sql` gom theo (video, user) trước: người có `ended` nhưng mất `play` (best-effort) không được tính, nên tỉ lệ luôn trong [0, 1].

Xoá video (M7) không xoá row ClickHouse: lịch sử phân tích được giữ.
```

`docs/flows.md` (sửa — 3 cặp thay thế):

1. Thay:

```markdown
# Các luồng

Chỉ gồm các luồng đã chạy (tới M6).

## Upload và transcode

```

   bằng:

```markdown
# Các luồng

Chỉ gồm các luồng đã chạy (tới M7).

## Upload và transcode

```

2. Thay:

````markdown
  K->>API: video.transcoded → renditions, duration_s, status ready
```

## Retry và DLQ

```mermaid
````

   bằng:

````markdown
  K->>API: video.transcoded → renditions, duration_s, status ready
```

## Upload từ trang admin (M7)

```mermaid
sequenceDiagram
  autonumber
  participant B as Trình duyệt (/admin/upload)
  participant N as nginx (cổng 8088)
  participant API as api
  participant S3 as MinIO (cổng 9000)
  B->>N: POST /api/admin/videos {title, genres, year, filename, size_bytes} (cookie admin)
  N->>API: proxy
  API-->>B: 201 {id, upload: {part_size, parts[{part_number, url}]}}
  par tối đa 3 part cùng lúc
    B->>S3: OPTIONS presigned url (Origin: http://localhost:8088)
    S3-->>B: 204 Access-Control-Allow-Origin (MINIO_CORS_ALLOW_ORIGIN)
    B->>S3: PUT part (XMLHttpRequest, upload.onprogress → thanh tiến độ)
    Note over B,S3: lỗi → thử lại, tối đa 3 lần/part; hết lượt → huỷ các part đang gửi, cho admin xoá video dở
  end
  B->>N: POST /api/admin/videos/{id}/complete
  N->>API: ListParts, CompleteMultipartUpload, queued, video.uploaded
  API-->>B: 200 {status: queued}
  B->>B: chuyển tới /admin/videos/{id}
  loop mỗi 3 giây khi queued/processing
    B->>N: GET /api/admin/videos/{id}
  end
  Note over B: ready → nút "Xem phim"; failed → fail_reason + nút "Thử lại"
```

ETag của từng part không cần đọc (api tự `ListParts`), nên MinIO không phải expose header nào ([ADR 0019](decisions/0019-admin-browser-upload.md)).

## Sửa và xoá video (M7)

```mermaid
sequenceDiagram
  participant A as Admin
  participant API as api
  participant PG as Postgres
  participant S3 as MinIO
  A->>API: PATCH /api/admin/videos/{id} {title?, description?, genres?, year?}
  API->>PG: SELECT video; gộp field; NormalizeMetadata
  API->>PG: UPDATE title, description, genres, year (trigger tính lại search)
  API-->>A: 200 video
  A->>API: DELETE /api/admin/videos/{id}
  API->>PG: BEGIN; SELECT … FOR UPDATE
  alt queued / processing
    API-->>A: 409 video is being processed
  else uploading / ready / failed
    API->>PG: DELETE videos (cascade renditions, subtitles, watch_progress); COMMIT
    API->>S3: AbortMultipartUpload (nếu uploading), xoá raw/{id}/, hls/{id}/ (lỗi chỉ log)
    API-->>A: 204
  end
```

## Retry và DLQ

```mermaid
````

3. Thay:

````markdown
  T->>K: video.uploaded.dlq {payload, error, attempts: 4}
  T->>K: commit offset
  K->>API: video.failed → status failed, fail_reason
  Note over API: Admin gọi POST /{id}/retry: failed → queued, publish lại video.uploaded
```

Lỗi không sửa được bằng thử lại (file không có video stream) đi thẳng tới `video.failed` + DLQ với `attempts: 1`. Message hỏng (không phải JSON, thiếu `video_id`/`raw_key`, hoặc `video_id` không phải UUID dạng chuẩn) đi thẳng vào DLQ với `attempts: 0`.
````

   bằng:

````markdown
  T->>K: video.uploaded.dlq {payload, error, attempts: 4}
  T->>K: commit offset
  K->>API: video.failed → status failed, fail_reason
  Note over API: Admin bấm "Thử lại" (/admin) = POST /{id}/retry: failed → queued, publish lại video.uploaded
```

Lỗi không sửa được bằng thử lại (file không có video stream) đi thẳng tới `video.failed` + DLQ với `attempts: 1`. Message hỏng (không phải JSON, thiếu `video_id`/`raw_key`, hoặc `video_id` không phải UUID dạng chuẩn) đi thẳng vào DLQ với `attempts: 0`.
````

`docs/decisions/0019-admin-browser-upload.md` (mới, toàn văn):

```markdown
# ADR 0019: Trang admin upload thẳng từ trình duyệt lên MinIO (presigned multipart, CORS giới hạn origin)

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec M7 cần trang admin trong SPA để upload video. API đã có từ M2: `POST /api/admin/videos` trả presigned URL cho từng part 64 MiB (host = `S3_PUBLIC_ENDPOINT`, local `http://localhost:9000`), `/complete` ghép bằng `ListParts` ([ADR 0004](0004-presigned-multipart-upload.md)). SPA chạy ở origin `http://localhost:8088` (nginx), nên `PUT` lên `:9000` là request cross-origin: trình duyệt gửi preflight `OPTIONS` và chỉ cho `PUT` khi MinIO trả `Access-Control-Allow-Origin` khớp origin.

## Quyết định
- Trình duyệt `PUT` từng part thẳng lên presigned URL bằng `XMLHttpRequest` (fetch không báo tiến độ upload), tối đa 3 part cùng lúc, mỗi part thử tối đa 3 lần; một part hết lượt thì huỷ các part đang gửi và báo lỗi kèm id video để admin xoá (`web/app/src/admin/upload.ts`). Xong thì gọi `/complete`. File không đi qua api hay nginx.
- Không cần đọc header `ETag` của phản hồi `PUT` (api tự `ListParts`), nên không cần `Access-Control-Expose-Headers`. Blob cắt từ `File` có type rỗng nên trình duyệt không gửi `Content-Type`; presigned URL chỉ ký header `host`.
- MinIO mặc định cho mọi origin (`*`). Compose đặt `MINIO_API_CORS_ALLOW_ORIGIN=${MINIO_CORS_ALLOW_ORIGIN}` (mặc định `http://localhost:8088,http://localhost:5173`: SPA qua nginx và Vite dev server); origin khác không nhận được `Access-Control-Allow-Origin` (`scripts/e2e-m7.sh` kiểm tra cả hai chiều).
- `S3_PUBLIC_ENDPOINT` của service api đọc từ `.env` (mặc định `http://localhost:9000`) để một stack chạy ở cổng khác vẫn ký đúng host.

## Phương án đã loại
- Proxy upload qua nginx (`/upload/` → MinIO) để cùng origin: chữ ký SigV4 gắn với header `Host` mà presigned URL đã ký (`localhost:9000`), nginx phải giữ nguyên Host và path — dễ sai, và mọi byte video đi thêm một chặng.
- Upload qua api (multipart form): đi ngược thiết kế "video không đi qua api" (spec §3), api phải giữ tới 10 GB mỗi request.
- `fetch` + `ReadableStream` để có tiến độ: upload streaming cần HTTP/2 và `duplex: 'half'`, MinIO local là HTTP/1.1.

## Hệ quả
- Trình duyệt của admin phải tới được `S3_PUBLIC_ENDPOINT` (cổng 9000). Chạy SPA ở origin khác (đổi cổng nginx, domain khác) phải thêm origin đó vào `MINIO_CORS_ALLOW_ORIGIN` rồi `make up`; quên thì `PUT` báo "lỗi mạng" và preflight không có `Access-Control-Allow-Origin`.
- Rời trang upload giữa chừng thì upload bị huỷ; video ở lại `uploading` và được job dọn xoá sau 24 giờ (hoặc admin xoá ngay trên trang).
- Presigned URL hết hạn sau 1 giờ: file rất lớn trên mạng chậm có thể không kịp (chưa có re-presign, xem [known-limitations](../known-limitations.md)).
```

`docs/decisions/0020-video-delete-and-edit.md` (mới, toàn văn):

```markdown
# ADR 0020: Xoá video từ chối khi đang xử lý; sửa metadata ghi đè cả bốn cột

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §6 có `PATCH/DELETE /api/admin/videos/{id}`. Một video có dữ liệu ở Postgres (`videos`, và `renditions`, `subtitles`, `watch_progress` đều `ON DELETE CASCADE`), MinIO (`raw/{id}/`, `hls/{id}/`), cache nginx, ClickHouse. Transcoder không đọc Postgres: khi video `queued`/`processing`, nó có thể đang (hoặc sắp) ghi `hls/{id}/` rồi publish `video.transcoded`.

## Quyết định
- `DELETE` khoá dòng (`SELECT … FOR UPDATE`, như mọi chuyển trạng thái) rồi:
  - `queued` / `processing` → `409` (`catalog.ErrVideoBusy`), không đụng gì. Admin đợi `ready`/`failed` rồi xoá.
  - `uploading`, `ready`, `failed` → xoá dòng (cascade), commit, rồi dọn storage: abort multipart nếu còn `uploading`, xoá `raw/{id}/` và `hls/{id}/`. Lỗi storage chỉ log (`delete objects after delete failed`), request vẫn `204`: DB là nguồn sự thật, object mồ côi không làm sai hành vi.
- Sự kiện đến muộn của video đã xoá (`video.transcoded` …) bị `StatusUpdater` bỏ qua như `ErrNotFound` (đã có từ M2).
- `PATCH` nhận `title`, `description`, `genres`, `year` (field vắng giữ nguyên, `year: 0` xoá năm, `genres: []` xoá thể loại), ở mọi trạng thái. Handler đọc video, gộp, chuẩn hoá bằng `catalog.NormalizeMetadata` (dùng chung với tạo video: tiêu đề 1–200 ký tự, mô tả ≤ 5000, ≤ 10 thể loại mỗi cái ≤ 50 ký tự, bỏ trùng; năm 0 hoặc 1888–2100) rồi `UPDATE` cả bốn cột. Trigger `videos_search_update` tính lại cột `search` nên tìm kiếm thấy tiêu đề mới ngay.

## Phương án đã loại
- Cho xoá khi đang xử lý rồi dọn `hls/{id}/` khi `video.transcoded` đến muộn: cần thêm trạng thái `deleting` hoặc bảng tombstone, consumer phải xoá object — nhiều code cho một tình huống hiếm ở MVP.
- Xoá mềm (`deleted_at`): mọi truy vấn catalog/tìm kiếm/xem tiếp phải lọc thêm; không có yêu cầu khôi phục.
- `PATCH` bằng `UPDATE … SET col = COALESCE($n, col)`: không phân biệt được "không gửi" với "xoá" (`year`), và kiểm tra giới hạn vẫn cần giá trị sau gộp.

## Hệ quả
- Video kẹt `queued` (transcoder chết lâu, message mất) không xoá được qua API cho tới khi nó `failed`; xử lý tay bằng SQL (xem [operations](../operations.md#sự-cố-thường-gặp)).
- Segment đã nằm trong cache nginx (7 ngày) vẫn phục vụ được cho ai còn giữ URL ký chưa hết hạn (tối đa 2 giờ); không cấp được URL mới vì `/play` trả `404`.
- Sự kiện xem trong ClickHouse của video đã xoá được giữ lại (lịch sử phân tích).
- Hai admin sửa cùng lúc: bản ghi sau thắng (không có optimistic locking).
```

- [ ] **Step 2: Đối chiếu tài liệu với code**

Run:
```bash
grep -cE '^\| (GET|POST|PATCH|DELETE) \|' docs/api.md
for p in $(grep -E '^\| (GET|POST|PATCH|DELETE) \|' docs/api.md | cut -d'`' -f2 | sed 's/?.*//'); do
  grep -rqF "\"$p\"" --include='*.go' cmd internal || echo "không thấy route $p"
done
grep -n 'TODO\|TBD' docs/api.md docs/data-model.md docs/flows.md docs/decisions/0019-*.md docs/decisions/0020-*.md
```
Expected: `19` dòng endpoint trong bảng api.md (gồm `PATCH`, `DELETE` mới); vòng lặp không in gì (mọi path trong bảng có trong code Go); grep cuối không in gì. Mọi link tương đối trong các file vừa sửa trỏ tới file có thật: `for f in docs/decisions/0019-admin-browser-upload.md docs/decisions/0020-video-delete-and-edit.md docs/known-limitations.md; do test -e "$f" || echo "missing $f"; done` — `docs/known-limitations.md` được tạo ở Task 11 nên dòng `missing docs/known-limitations.md` là đúng ở bước này.

- [ ] **Step 3: Commit**

```bash
git add docs/api.md docs/data-model.md docs/flows.md docs/decisions/0019-admin-browser-upload.md docs/decisions/0020-video-delete-and-edit.md
git commit -m "docs: admin API (PATCH/DELETE), M7 flows, data model migrations, ADRs 0019-0020" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Tài liệu (2): `architecture.md`, `operations.md`, `README.md`, `known-limitations.md`

**Files:**
- Create: `docs/known-limitations.md`
- Modify: `docs/architecture.md`, `docs/operations.md`, `README.md`

- [ ] **Step 1: Cài đặt**

`docs/architecture.md` (sửa — 4 cặp thay thế):

1. Thay:

````markdown

```mermaid
flowchart LR
  Admin[Admin / script e2e] -->|metadata, complete| API[api]
  Admin -->|presigned PUT từng part| S3[(MinIO)]
  API --> PG[(Postgres)]
  API -->|video.uploaded| K{{Kafka}}
  K --> T[transcoder worker]
````

   bằng:

````markdown

```mermaid
flowchart LR
  Admin["Admin: trang /admin (SPA) / script e2e"] -->|"metadata, complete, retry, PATCH, DELETE (qua nginx)"| API[api]
  Admin -->|"presigned PUT từng part (CORS)"| S3[(MinIO)]
  API --> PG[(Postgres)]
  API -->|video.uploaded| K{{Kafka}}
  K --> T[transcoder worker]
````

2. Thay:

````markdown
  AN -->|"INSERT theo lô (HTTP 8123)"| CH[(ClickHouse)]
```

Phần đã chạy tới M6: `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu, `/play` kèm phụ đề / thumbnails / `resume_at`, hàng "Xem tiếp", nhận sự kiện xem, upload video và phụ đề cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker` (HLS + poster + sprite), `analytics` (`playback.events` và `app.logs` → ClickHouse), Postgres, Kafka, MinIO, ClickHouse, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL), web người xem `web/app`. Chưa có: trang admin (M7).

## api

| Module | Trách nhiệm (tới M4) |
|---|---|
| `internal/upload` | `POST /api/admin/videos` (tạo video, mở multipart upload, trả presigned URL), `/complete`, `/retry`; job dọn upload bỏ dở quá 24 giờ |
| `internal/catalog` | Repository `videos` / `renditions`, máy trạng thái, `GET /api/admin/videos[/{id}]`, `GET /api/videos[/{id}]` (tìm kiếm `q`, lọc `genre`/`year`), `GET /api/genres` và `/play` cho người xem (chỉ video `ready`), consumer `video.processing` / `video.transcoded` / `video.failed` (group `api`) |
| `internal/auth` | Bảng `users`, bcrypt, JWT HS256 trong cookie `sf_session`, `/api/auth/register|login|logout`, `/api/me`, middleware `Authenticate` / `RequireUser` / `RequireAdmin`, lệnh `api seed-admin` |
| `internal/signing` | Ký đường dẫn `/cdn/s/{sig}/{exp}/hls/{id}/…` đúng công thức `secure_link_md5` của nginx; từ chối tên file không an toàn (rỗng, bắt đầu bằng `/`, chứa `..`, `?`, `#`, `\` hoặc khoảng trắng) |
| `internal/playback` | `POST /api/playback/events`: kiểm tra sự kiện, upsert `watch_progress` (heartbeat/pause/ended), publish `playback.events` qua `BestEffort` (nền, giới hạn 256 publish đang chờ, timeout 2 giây, lỗi thì log và bỏ); `Repo` đọc tiến độ cho `resume_at` và "Xem tiếp" ([ADR 0015](decisions/0015-playback-events-best-effort.md)) |
````

   bằng:

````markdown
  AN -->|"INSERT theo lô (HTTP 8123)"| CH[(ClickHouse)]
```

MVP hoàn chỉnh (M7): `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu, `/play` kèm phụ đề / thumbnails / `resume_at`, hàng "Xem tiếp", nhận sự kiện xem, upload/sửa/xoá video và phụ đề cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker` (HLS + poster + sprite), `analytics` (`playback.events` và `app.logs` → ClickHouse), Postgres, Kafka, MinIO, ClickHouse, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL, header bảo mật), web `web/app` cho người xem và trang quản trị `/admin`. Các giới hạn chấp nhận: [known-limitations](known-limitations.md).

## api

| Module | Trách nhiệm |
|---|---|
| `internal/upload` | `POST /api/admin/videos` (tạo video, mở multipart upload, trả presigned URL), `/complete`, `/retry`, `DELETE /api/admin/videos/{id}` (xoá dòng rồi dọn `raw/`, `hls/`, [ADR 0020](decisions/0020-video-delete-and-edit.md)); job dọn upload bỏ dở quá 24 giờ |
| `internal/catalog` | Repository `videos` / `renditions` / `subtitles`, máy trạng thái, `NormalizeMetadata` (giới hạn metadata dùng chung cho tạo và sửa), `GET /api/admin/videos[/{id}]`, `PATCH /api/admin/videos/{id}`, `GET /api/videos[/{id}]` (tìm kiếm `q`, lọc `genre`/`year`), `GET /api/genres` và `/play` cho người xem (chỉ video `ready`), consumer `video.processing` / `video.transcoded` / `video.failed` (group `api`) |
| `internal/auth` | Bảng `users`, bcrypt, JWT HS256 trong cookie `sf_session`, `/api/auth/register|login|logout`, `/api/me`, middleware `Authenticate` / `RequireUser` / `RequireAdmin`, lệnh `api seed-admin` |
| `internal/signing` | Ký đường dẫn `/cdn/s/{sig}/{exp}/hls/{id}/…` đúng công thức `secure_link_md5` của nginx; từ chối tên file không an toàn (rỗng, bắt đầu bằng `/`, chứa `..`, `?`, `#`, `\` hoặc khoảng trắng) |
| `internal/playback` | `POST /api/playback/events`: kiểm tra sự kiện, upsert `watch_progress` (heartbeat/pause/ended), publish `playback.events` qua `BestEffort` (nền, giới hạn 256 publish đang chờ, timeout 2 giây, lỗi thì log và bỏ); `Repo` đọc tiến độ cho `resume_at` và "Xem tiếp" ([ADR 0015](decisions/0015-playback-events-best-effort.md)) |
````

3. Thay:

```markdown

Player: gọi `/play`, nạp URL ký vào hls.js. Lỗi `403` (URL hết hạn hoặc sai) hay lỗi mạng fatal → `PlaybackSession.refresh()` gọi lại `/play` và phát tiếp từ `currentTime`; nhiều lỗi cùng lúc chỉ gọi `/play` một lần, quá 3 lần trong 60 giây thì báo lỗi (`web/app/src/player/recovery.ts`). Lỗi media fatal → `hls.recoverMediaError()` (lần 2 gọi thêm `swapAudioCodec()`, lần 3 báo lỗi; đếm lại khi phát tiến triển). Route tải lazy được bọc `ErrorBoundary` để chunk cũ sau khi deploy hiện nút "Tải lại" thay vì trang trắng.

## Ảnh xem trước, phụ đề và xem tiếp (M5)

- **Poster:** `GET /api/videos` và `/api/videos/{id}` trả `poster_url` ký bằng `Signer.StablePath` — `exp` làm tròn xuống theo giờ nên URL giống hệt nhau trong một giờ và trình duyệt cache được; hạn còn lại 1–2 giờ, không vượt TTL 2 giờ ([ADR 0013](decisions/0013-thumbnails-sprite-sheets.md)).
```

   bằng:

```markdown

Player: gọi `/play`, nạp URL ký vào hls.js. Lỗi `403` (URL hết hạn hoặc sai) hay lỗi mạng fatal → `PlaybackSession.refresh()` gọi lại `/play` và phát tiếp từ `currentTime`; nhiều lỗi cùng lúc chỉ gọi `/play` một lần, quá 3 lần trong 60 giây thì báo lỗi (`web/app/src/player/recovery.ts`). Lỗi media fatal → `hls.recoverMediaError()` (lần 2 gọi thêm `swapAudioCodec()`, lần 3 báo lỗi; đếm lại khi phát tiến triển). Route tải lazy được bọc `ErrorBoundary` để chunk cũ sau khi deploy hiện nút "Tải lại" thay vì trang trắng.

## Trang quản trị (M7)

Route `/admin` của cùng SPA, tải lazy (người xem không tải code admin), bọc `RequireAdmin`: viewer thấy trang 403 (API cũng trả `403`), header chỉ hiện link "Quản trị" cho admin.

| Trang | Việc |
|---|---|
| `/admin` | Mọi video (tối đa 200, mới nhất trước): chip trạng thái `uploading`/`queued`/`processing`/`ready`/`failed`, `fail_reason`, dung lượng; nút "Thử lại" (failed) và "Xoá" (trừ queued/processing). Poll 3 giây khi còn video `queued`/`processing` |
| `/admin/upload` | Chọn file + metadata → `POST /api/admin/videos` → `PUT` các part song song (3) thẳng lên MinIO với thanh tiến độ → `/complete` → sang trang chi tiết ([ADR 0019](decisions/0019-admin-browser-upload.md)) |
| `/admin/videos/{id}` | Trạng thái (poll khi đang xử lý), lý do lỗi + thử lại, sửa metadata (`PATCH`), danh sách và upload phụ đề, xoá, link xem khi `ready` |

nginx gắn `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `Referrer-Policy: same-origin` cho mọi phản hồi (`deploy/nginx/snippets/security-headers.conf`, include ở cấp server và trong từng location có `add_header` riêng vì nginx không kế thừa `add_header` khi location tự khai báo).

## Ảnh xem trước, phụ đề và xem tiếp (M5)

- **Poster:** `GET /api/videos` và `/api/videos/{id}` trả `poster_url` ký bằng `Signer.StablePath` — `exp` làm tròn xuống theo giờ nên URL giống hệt nhau trong một giờ và trình duyệt cache được; hạn còn lại 1–2 giờ, không vượt TTL 2 giờ ([ADR 0013](decisions/0013-thumbnails-sprite-sheets.md)).
```

4. Thay:

```markdown
| CDN + token auth | nginx `secure_link_md5` + `proxy_cache`, URL ký theo prefix `/hls/{id}/`, hạn 2 giờ |
| User/Auth service | module `auth` trong `api` (bcrypt, JWT trong cookie HttpOnly) |
| Search | Postgres full-text + `unaccent` + `pg_trgm` trong module `catalog` |
| Client apps (web) | SPA React + hls.js phục vụ bởi nginx cùng origin với API |
| Analytics / logging | `analytics` consume `playback.events` + `app.logs`, ghi lô vào ClickHouse |
```

   bằng:

```markdown
| CDN + token auth | nginx `secure_link_md5` + `proxy_cache`, URL ký theo prefix `/hls/{id}/`, hạn 2 giờ |
| User/Auth service | module `auth` trong `api` (bcrypt, JWT trong cookie HttpOnly) |
| Search | Postgres full-text + `unaccent` + `pg_trgm` trong module `catalog` |
| Client apps (web) | SPA React + hls.js phục vụ bởi nginx cùng origin với API; trang quản trị `/admin` trong cùng SPA |
| Analytics / logging | `analytics` consume `playback.events` + `app.logs`, ghi lô vào ClickHouse |
```

`docs/operations.md` (sửa — 5 cặp thay thế):

1. Thay:

```markdown
| `make e2e-m4` | E2E: SPA và fallback route qua nginx, `/api/` cùng origin (cookie), upload video tiêu đề tiếng Việt, tìm không dấu / có dấu / gõ sai / lọc thể loại, `/play` cùng origin |
| `make e2e-m5` | E2E: poster/sprite, phụ đề, sự kiện xem → "Xem tiếp", `playback.events` trong Kafka (`KAFKA_DOWN=1`: thử Kafka chết) |
| `make e2e-m6` | E2E: sự kiện xem và log api/transcoder vào ClickHouse, record hỏng bị bỏ qua, SQL mẫu có kết quả (`CH_DOWN=1`: dừng ClickHouse, analytics thử lại, không mất sự kiện) |
| `make analytics-sql [Q=tên]` | Chạy SQL mẫu `scripts/analytics/*.sql` trên ClickHouse (mặc định tất cả) |
| `make web-install` / `make web-test` / `make web-build` | `npm ci` / Vitest / build production cho `web/app` (cần Node 20.19+) |
| `make web-dev` | Vite dev server http://localhost:5173 (hot reload), proxy `/api` và `/cdn` sang nginx `:8088` (cần `make up`) |
| `make sign-url ID=…` | In URL CDN đã ký (2 giờ) cho video `ID` (dòng đầu là URL, dòng sau là thời điểm hết hạn), dùng với dev player hoặc video từ `make transcode` |
| `make sample` | Sinh `tmp/sample.mp4` |
| `make build` | Build `bin/api`, `bin/transcoder`, `bin/analytics` |
| `make run-api` / `make run-worker` / `make run-analytics` | Chạy api / worker / analytics trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder analytics`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |

`make up` chạy `up -d --build` rồi `up -d --wait` cho các service chạy lâu. Các container one-shot (`minio-init`, `kafka-init`) thoát với mã 0 nên không dùng `--wait` cho chúng. `api` có healthcheck (`/healthz`) nên `--wait` đợi tới khi healthy; `transcoder` không có healthcheck nên `--wait` chỉ đợi container chạy. `api` tự chạy migration khi khởi động.

Xoá sạch dữ liệu: `docker compose -f deploy/docker-compose.yml down -v`.

## Cổng và giao diện

| Dịch vụ | Địa chỉ | Đăng nhập |
|---|---|---|
| **Web người xem** | http://localhost:8088 | admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD`, hoặc tự đăng ký viewer |
| api qua nginx | http://localhost:8088/api/… | cookie sau `POST /api/auth/login` |
| api trực tiếp | http://localhost:8000 (`/healthz`) | như trên (dùng cho script, debug) |
| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |
| Vite dev server | http://localhost:5173 (`make web-dev`) | như web người xem |
| Dev player | http://localhost:8088/dev/player.html?src={URL ký, đã URL-encode} | |
| MinIO API (presigned URL) | http://localhost:9000 | bucket `hls` đọc ẩn danh trong dev ([ADR 0002](decisions/0002-dev-hls-bucket-read.md)) |
| MinIO Console | http://localhost:9001 | `S3_ACCESS_KEY` / `S3_SECRET_KEY` trong `.env` |
| Redpanda Console (Kafka) | http://localhost:8080 | |
| ClickHouse HTTP | http://localhost:8123 | `streamfilm` / `POSTGRES_PASSWORD` |
```

   bằng:

```markdown
| `make e2e-m4` | E2E: SPA và fallback route qua nginx, `/api/` cùng origin (cookie), upload video tiêu đề tiếng Việt, tìm không dấu / có dấu / gõ sai / lọc thể loại, `/play` cùng origin |
| `make e2e-m5` | E2E: poster/sprite, phụ đề, sự kiện xem → "Xem tiếp", `playback.events` trong Kafka (`KAFKA_DOWN=1`: thử Kafka chết) |
| `make e2e-m6` | E2E: sự kiện xem và log api/transcoder vào ClickHouse, record hỏng bị bỏ qua, SQL mẫu có kết quả (`CH_DOWN=1`: dừng ClickHouse, analytics thử lại, không mất sự kiện) |
| `make e2e-m7` | E2E: header bảo mật nginx, preflight CORS của MinIO (origin SPA được phép, origin lạ bị chặn), upload như trình duyệt, `DELETE` (đang xử lý → 409, uploading/ready/failed → 204, HLS bị xoá), `PATCH` → tìm kiếm thấy tiêu đề mới, file chỉ có tiếng → `failed` → retry, viewer → 403 |
| `make e2e` | Chạy lần lượt `e2e-m3` (gồm `e2e-m2`), `e2e-m4`, `e2e-m5`, `e2e-m6`, `e2e-m7`, in bảng tổng kết (PASS/FAIL, thời gian), log từng kịch bản ở `tmp/e2e-logs/`; `ONLY="m4 m7"` để chạy một phần. Mất khoảng 3 phút |
| `make test-all` | `go test ./...` (cần Docker cho integration test) rồi Vitest của web (tự `npm ci` nếu chưa có `node_modules`) |
| `make analytics-sql [Q=tên]` | Chạy SQL mẫu `scripts/analytics/*.sql` trên ClickHouse (mặc định tất cả) |
| `make test` / `make test-short` | `go test ./...` (gồm integration test bằng testcontainers, cần Docker và FFmpeg) / chỉ unit test |
| `make web-install` / `make web-test` / `make web-build` | `npm ci` / Vitest / build production cho `web/app` (cần Node 20.19+) |
| `make web-dev` | Vite dev server http://localhost:5173 (hot reload), proxy `/api` và `/cdn` sang nginx `:8088` (cần `make up`) |
| `make sign-url ID=…` | In URL CDN đã ký (2 giờ) cho video `ID` (dòng đầu là URL, dòng sau là thời điểm hết hạn), dùng với dev player hoặc video từ `make transcode` |
| `make sample` | Sinh `tmp/sample.mp4` (30 giây 1080p có tiếng) |
| `make env` | Tạo `.env` từ `.env.example`, hoặc thêm khoá mới còn thiếu (không ghi đè giá trị cũ); `make up` tự chạy |
| `make build` | Build `bin/api`, `bin/transcoder`, `bin/analytics` |
| `make run-api` / `make run-worker` / `make run-analytics` | Chạy api / worker / analytics trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder analytics`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |

`make up` chạy `up -d --build` rồi `up -d --wait` cho các service chạy lâu. Các container one-shot (`minio-init`, `kafka-init`) thoát với mã 0 nên không dùng `--wait` cho chúng. `api` có healthcheck (`/healthz`) nên `--wait` đợi tới khi healthy; `transcoder` không có healthcheck nên `--wait` chỉ đợi container chạy. `api` tự chạy migration khi khởi động.

Xoá sạch dữ liệu: `docker compose -f deploy/docker-compose.yml --env-file .env down -v` (lần `make up` sau dựng lại từ đầu: bucket, topic, migration, bảng ClickHouse đều tự tạo; chạy lại `make seed-admin`).

## Cổng và giao diện

| Dịch vụ | Địa chỉ | Đăng nhập |
|---|---|---|
| **Web người xem** | http://localhost:8088 | admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD`, hoặc tự đăng ký viewer |
| **Trang quản trị** | http://localhost:8088/admin | chỉ admin (viewer thấy trang 403) |
| api qua nginx | http://localhost:8088/api/… | cookie sau `POST /api/auth/login` |
| api trực tiếp | http://localhost:8000 (`/healthz`) | như trên (dùng cho script, debug) |
| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |
| Vite dev server | http://localhost:5173 (`make web-dev`) | như web người xem |
| Dev player | http://localhost:8088/dev/player.html?src={URL ký, đã URL-encode} | |
| MinIO API (presigned URL) | http://localhost:9000 | bucket `hls` đọc ẩn danh trong dev ([ADR 0002](decisions/0002-dev-hls-bucket-read.md)); CORS chỉ cho `MINIO_CORS_ALLOW_ORIGIN` |
| MinIO Console | http://localhost:9001 | `S3_ACCESS_KEY` / `S3_SECRET_KEY` trong `.env` |
| Redpanda Console (Kafka) | http://localhost:8080 | |
| ClickHouse HTTP | http://localhost:8123 | `streamfilm` / `POSTGRES_PASSWORD` |
```

2. Thay:

```markdown

Các giá trị trong `.env.example` chỉ dành cho dev local và đã nằm trong repo: phải sinh secret và mật khẩu mới cho bất kỳ môi trường nào ngoài máy local. Đổi `CDN_SIGNING_SECRET` thì chạy lại `make up` để nginx và api cùng nhận giá trị mới; URL đã phát trước đó thành `403`.

## Phát triển web (M4)

Mã nguồn ở `web/app` (React 18 + Vite + TypeScript, hls.js, react-router). Lần đầu: `make web-install`.
```

   bằng:

```markdown

Các giá trị trong `.env.example` chỉ dành cho dev local và đã nằm trong repo: phải sinh secret và mật khẩu mới cho bất kỳ môi trường nào ngoài máy local. Đổi `CDN_SIGNING_SECRET` thì chạy lại `make up` để nginx và api cùng nhận giá trị mới; URL đã phát trước đó thành `403`.

## Biến môi trường mới (M7)

| Biến | Mặc định (`.env.example`) | Ý nghĩa |
|---|---|---|
| `MINIO_CORS_ALLOW_ORIGIN` | `http://localhost:8088,http://localhost:5173` | Origin được `PUT` part từ trình duyệt lên MinIO (compose đặt `MINIO_API_CORS_ALLOW_ORIGIN`). Phải chứa origin mở trang admin |

`S3_PUBLIC_ENDPOINT` (có từ M2) giờ cũng được compose truyền cho service `api` (mặc định `http://localhost:9000`): host trong presigned URL mà trình duyệt `PUT` tới.

## Trang quản trị (M7)

Đăng nhập admin (`make seed-admin`) rồi mở http://localhost:8088/admin (hoặc link "Quản trị" trên header).

1. **Tải video lên** → chọn file `.mp4/.mov/.mkv/.webm` (≤ 10 GB), sửa tiêu đề (gợi ý từ tên file), mô tả, thể loại (cách nhau bằng dấu phẩy), năm → **Upload**. Thanh tiến độ tính theo byte đã `PUT` lên MinIO (3 part 64 MiB cùng lúc). Đừng rời trang khi đang upload (upload bị huỷ; video dở bị dọn sau 24 giờ hoặc bấm "Xoá video upload dở").
2. Xong upload, trang chuyển sang chi tiết video: chip `Chờ xử lý` → `Đang xử lý` → `Sẵn sàng` (tự cập nhật mỗi 3 giây). Video 30 giây 1080p mất khoảng 20–40 giây.
3. `Sẵn sàng` → **▶ Xem phim**. `Lỗi` → đọc lý do (`fail_reason`), sửa nguyên nhân rồi **Thử lại**.
4. Sửa tiêu đề/mô tả/thể loại/năm → **Lưu**; tìm kiếm thấy ngay. Thêm phụ đề: mã ngôn ngữ (`vi`, `en`, `pt-BR`), nhãn, file `.srt`/`.vtt`.
5. **Xoá** có hỏi xác nhận; không xoá được khi đang `Chờ xử lý`/`Đang xử lý`.

Kiểm tra tay bằng trình duyệt (chưa có test tự động end-to-end cho UI): upload `tmp/sample.mp4` qua trang, theo dõi tab Network: `POST /api/admin/videos` → các `OPTIONS` + `PUT` tới `localhost:9000` trả `200` → `POST …/complete` → poll `GET /api/admin/videos/{id}`.

## Phát triển web (M4)

Mã nguồn ở `web/app` (React 18 + Vite + TypeScript, hls.js, react-router). Lần đầu: `make web-install`.
```

3. Thay:

```markdown
| File | Câu hỏi |
|---|---|
| `top_videos.sql` | Top 10 phim theo lượt play, kèm số người xem khác nhau |
| `completion_rate.sql` | Tỉ lệ xem hết = người có `ended` / người có `play`, theo phim |
| `buffering_by_bitrate.sql` | Số lần, tổng và trung bình thời gian buffering theo `bitrate_kbps` |
| `logs_by_service.sql` | Số dòng log theo service và level trong 24 giờ |

```

   bằng:

```markdown
| File | Câu hỏi |
|---|---|
| `top_videos.sql` | Top 10 phim theo lượt play, kèm số người xem khác nhau |
| `completion_rate.sql` | Tỉ lệ xem hết = người có cả `play` và `ended` / người có `play`, theo phim (luôn ≤ 1) |
| `buffering_by_bitrate.sql` | Số lần, tổng và trung bình thời gian buffering theo `bitrate_kbps` |
| `logs_by_service.sql` | Số dòng log theo service và level trong 24 giờ |

```

4. Thay:

```markdown
| Triệu chứng | Cách xử lý |
|---|---|
| `make up` treo ở kafka | Xem `make logs`; xoá volume `kafkadata` nếu đổi cấu hình listener |
| `/complete` trả `503` | Kafka chưa sẵn sàng: `make ps`, đợi kafka healthy rồi gọi lại `/complete` |
| `/complete` trả `400 multipart upload incomplete` | Chưa `PUT` đủ part, part sai kích thước, tổng byte khác `size_bytes`, hoặc URL đã hết hạn (1 giờ): upload lại part đúng rồi gọi lại, hoặc tạo video mới |
| `PUT` presigned URL trả `403 SignatureDoesNotMatch` | URL được ký cho host khác host đang gọi; kiểm tra `S3_PUBLIC_ENDPOINT` |
```

   bằng:

```markdown
| Triệu chứng | Cách xử lý |
|---|---|
| `make up` treo ở kafka | Xem `make logs`; xoá volume `kafkadata` nếu đổi cấu hình listener |
| Trang admin báo "Upload lỗi: part 1: lỗi mạng (kiểm tra CORS của MinIO và cổng 9000)" | Preflight bị chặn: origin đang mở (ví dụ `http://127.0.0.1:8088` thay vì `localhost`) không nằm trong `MINIO_CORS_ALLOW_ORIGIN`; thêm vào `.env` rồi `make up`. Kiểm tra: `curl -si -X OPTIONS http://localhost:9000/raw/x -H 'Origin: http://localhost:8088' -H 'Access-Control-Request-Method: PUT' \| grep -i access-control-allow-origin` |
| Trang admin báo "Upload lỗi: part N: HTTP 403" | Presigned URL hết hạn (1 giờ) hoặc ký cho host khác (`S3_PUBLIC_ENDPOINT`): xoá video dở, upload lại |
| `/admin` hiện "403 — Không có quyền" | Đang đăng nhập bằng viewer: đăng xuất, đăng nhập admin |
| `DELETE` trả `409 video is being processed` | Video `queued`/`processing`: đợi `ready`/`failed`. Kẹt `queued` lâu (transcoder chết, message mất): kiểm tra `make logs-app`; xoá tay `psql -U streamfilm -c "update videos set status='failed', fail_reason='manual' where id='…'"` rồi xoá qua trang |
| `/complete` trả `503` | Kafka chưa sẵn sàng: `make ps`, đợi kafka healthy rồi gọi lại `/complete` |
| `/complete` trả `400 multipart upload incomplete` | Chưa `PUT` đủ part, part sai kích thước, tổng byte khác `size_bytes`, hoặc URL đã hết hạn (1 giờ): upload lại part đúng rồi gọi lại, hoặc tạo video mới |
| `PUT` presigned URL trả `403 SignatureDoesNotMatch` | URL được ký cho host khác host đang gọi; kiểm tra `S3_PUBLIC_ENDPOINT` |
```

5. Thay:

```markdown
| `playback_events` / `app_logs` không có row mới | `make ps` (analytics chạy chưa?), `make logs-app` tìm `clickhouse insert failed`; Redpanda Console → Consumer Groups `analytics-*` xem lag |
| Log analytics có `skip invalid record` | Record trong topic không đúng schema (JSON hỏng, id không phải UUID): đã bỏ qua và commit, không cần xử lý; `make e2e-m6` cố ý ghi một record như vậy |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
| Video `failed` | Xem `fail_reason` qua `GET /api/admin/videos?status=failed` và message trong DLQ |
| `make run-api` báo cổng 8000 đã dùng | Dừng service `api` trong compose trước |
| Integration test bị skip | Bật Docker Desktop; kiểm tra `ffmpeg` trong `PATH`; không dùng `-short` |
```

   bằng:

```markdown
| `playback_events` / `app_logs` không có row mới | `make ps` (analytics chạy chưa?), `make logs-app` tìm `clickhouse insert failed`; Redpanda Console → Consumer Groups `analytics-*` xem lag |
| Log analytics có `skip invalid record` | Record trong topic không đúng schema (JSON hỏng, id không phải UUID): đã bỏ qua và commit, không cần xử lý; `make e2e-m6` cố ý ghi một record như vậy |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
| Video `failed` | Xem `fail_reason` trên trang `/admin` (hoặc `GET /api/admin/videos?status=failed`) và message trong DLQ; "Thử lại" sau khi sửa nguyên nhân |
| `make e2e` báo FAIL ở một kịch bản | Mở `tmp/e2e-logs/e2e-mN.log` (dòng `FAIL: …` cuối); chạy lại riêng `make e2e ONLY=mN` |
| `make run-api` báo cổng 8000 đã dùng | Dừng service `api` trong compose trước |
| Integration test bị skip | Bật Docker Desktop; kiểm tra `ffmpeg` trong `PATH`; không dùng `-short` |
```

`README.md` (sửa — 2 cặp thay thế):

1. Thay:

````markdown

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **M5** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4); poster, ảnh xem trước khi tua (sprite), phụ đề nhiều ngôn ngữ, hàng "Xem tiếp" và sự kiện xem lên Kafka (M5); service `analytics` đưa sự kiện xem và log của api/transcoder vào ClickHouse, có SQL phân tích mẫu (M6). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2
- FFmpeg (có `ffprobe`) — để sinh video mẫu và chạy test
- `curl`, `jq`, `openssl` — cho script e2e
- Node 20.19+ — chỉ khi phát triển web (`make web-dev`, `make web-test`); `make up` build web trong Docker

## Chạy nhanh

```bash
make up          # dựng hạ tầng + build và chạy api, transcoder, analytics, nginx (kèm web)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make e2e-m4      # upload video tiêu đề tiếng Việt → tìm "tro choi dien tu" → /play qua nginx
make e2e-m5      # poster/sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" (KAFKA_DOWN=1: thử Kafka chết)
make e2e-m6      # sự kiện xem + log vào ClickHouse, SQL mẫu (CH_DOWN=1: thử ClickHouse chết)
make analytics-sql  # top phim, tỉ lệ xem hết, buffering theo bitrate, log theo service
```

Mở http://localhost:8088, đăng nhập bằng `ADMIN_EMAIL` / `ADMIN_PASSWORD` trong `.env` (mặc định `admin@streamfilm.local` / `admin-dev-password`) hoặc bấm "Đăng ký" để tạo tài khoản người xem. Catalog trống cho tới khi có video `ready`: chạy `make e2e-m4` (hoặc `make e2e-m2`) để upload `tmp/sample.mp4`.

`.env.example` chỉ chứa secret dùng cho dev local (`JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin). Muốn dùng ngoài máy local thì phải sinh giá trị mới.

`make e2e-m3` kiểm tra phân quyền và URL ký. Theo dõi message tại Redpanda Console http://localhost:8080.

## Test

```bash
make test-short   # chỉ unit test
make test         # gồm integration test (cần Docker và FFmpeg)
make web-install && make web-test   # Vitest cho web/app
```

## Tài liệu
````

   bằng:

````markdown

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **MVP hoàn chỉnh (M7)** — upload và transcode HLS qua Kafka (M2); đăng nhập bằng cookie JWT, URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem cùng origin sau nginx, tìm kiếm không dấu (M4); poster, sprite khi tua, phụ đề, "Xem tiếp", sự kiện xem lên Kafka (M5); analytics và log vào ClickHouse (M6); trang quản trị upload/sửa/xoá/thử lại video, header bảo mật, e2e một lệnh (M7). Lộ trình: [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn). Giới hạn chấp nhận: [known-limitations](docs/known-limitations.md).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2 (Docker Desktop hoặc Engine), ~6 GB RAM cho Docker
- FFmpeg (có `ffprobe`) — sinh video mẫu, chạy test và e2e
- `curl`, `jq`, `openssl`, `split` — cho script e2e
- Node 20.19+ — chỉ khi phát triển web (`make web-dev`, `make web-test`, `make test-all`); `make up` build web trong Docker
- Cổng trống trên máy: 8088 (web), 8000 (api), 9000/9001 (MinIO), 8080 (Redpanda Console), 8123 (ClickHouse), 5432 (Postgres), 29092 (Kafka)

## Chạy từ đầu

```bash
make up          # tạo .env từ .env.example, build và chạy toàn bộ stack (lần đầu vài phút)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make sample      # sinh tmp/sample.mp4 (30 giây 1080p) để thử upload
```

1. Mở http://localhost:8088, đăng nhập `admin@streamfilm.local` / `admin-dev-password` (giá trị `ADMIN_EMAIL` / `ADMIN_PASSWORD` trong `.env`).
2. Bấm **Quản trị** → **Tải video lên**, chọn `tmp/sample.mp4`, nhập tiêu đề → **Upload**. Thanh tiến độ chạy tới 100%, trang chuyển sang chi tiết video.
3. Đợi chip trạng thái từ `Chờ xử lý` → `Đang xử lý` → `Sẵn sàng` (khoảng 20–40 giây, tự cập nhật).
4. Bấm **▶ Xem phim** (hoặc về **Trang chủ** và chọn phim). Người xem khác: **Đăng ký** ở trang đăng nhập.

Kiểm tra toàn bộ hệ thống bằng một lệnh (khoảng 3 phút, in bảng tổng kết):

```bash
make e2e
```

Analytics: `make analytics-sql` (top phim, tỉ lệ xem hết, buffering theo bitrate, log theo service). Kafka: Redpanda Console http://localhost:8080. Tắt: `make down` (giữ dữ liệu).

`.env.example` chỉ chứa secret dùng cho dev local (`JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin). Muốn dùng ngoài máy local thì phải sinh giá trị mới.

## Test

```bash
make test-all     # go test ./... (gồm integration, cần Docker + FFmpeg) và Vitest của web
make test-short   # chỉ unit test Go
make e2e          # e2e trên stack đang chạy (make up + make seed-admin trước)
```

## Tài liệu
````

2. Thay:

```markdown
- [API](docs/api.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
```

   bằng:

```markdown
- [API](docs/api.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
- [Giới hạn đã biết](docs/known-limitations.md)
```

`docs/known-limitations.md` (mới, toàn văn):

```markdown
# Giới hạn đã biết (MVP)

Streamfilm là dự án học system design chạy local. Các điểm dưới đây được biết và **chấp nhận** ở MVP; mỗi mục ghi hệ quả và hướng làm nếu đi tiếp. Mục có ADR thì xem ADR để biết lý do.

## Đánh đổi thiết kế

| Giới hạn | Hệ quả | Hướng làm tiếp |
|---|---|---|
| Bucket `hls` đọc ẩn danh trên MinIO dev ([ADR 0002](decisions/0002-dev-hls-bucket-read.md)) | Ai tới được `localhost:9000` thì đọc HLS không cần chữ ký; lớp ký chỉ có hiệu lực qua nginx | Bỏ policy `download`, nginx ký request tới MinIO (SigV4) hoặc dùng mạng riêng |
| JWT không trạng thái ([ADR 0010](decisions/0010-jwt-cookie-session.md)) | Logout chỉ xoá cookie; token đã sao chép vẫn dùng được tới khi hết hạn (24 giờ); đổi role/xoá user có hiệu lực khi token hết hạn | Danh sách thu hồi (`jti`) hoặc session lưu server |
| Dual-write Postgres → Kafka ([ADR 0007](decisions/0007-accept-dual-write.md)) | Publish `video.uploaded` lỗi sau khi đã đặt `queued` được bù bằng trả trạng thái; publish "ma" (timeout nhưng thực ra đã gửi) có thể làm transcode thừa một lần | Transactional outbox |
| Không DRM, không mã hoá segment | Ai có URL ký còn hạn (2 giờ) tải được toàn bộ segment | HLS AES-128 / SAMPLE-AES, Widevine/FairPlay |
| URL ký theo thư mục `/hls/{id}/` ([ADR 0009](decisions/0009-signed-url-secure-link-md5.md)) | Một URL ký mở được mọi file của video đó (playlist, segment, phụ đề, sprite) tới khi hết hạn; MD5 là yêu cầu của `secure_link_md5` | CDN thật với token HMAC-SHA256 |
| Cache nginx 7 ngày, key `{id}/{file}` | Transcode lại (retry) hoặc xoá video: segment/playlist cũ còn trong cache tới khi hết hạn hoặc bị đẩy ra; người còn URL ký cũ vẫn tải được bản cũ | Thêm phiên bản vào đường dẫn HLS (`hls/{id}/v{n}/`) hoặc purge cache |
| Sự kiện xem best-effort ([ADR 0015](decisions/0015-playback-events-best-effort.md)) | Kafka chết thì sự kiện bị bỏ (request vẫn `204`); `completion_rate` chỉ tính người có cả `play` và `ended` | Buffer phía client hoặc outbox |
| Analytics at-least-once ([ADR 0017](decisions/0017-analytics-at-least-once.md)) | Có thể có row trùng trong ClickHouse khi analytics chết giữa INSERT và commit; lỗi 4xx vĩnh viễn từ ClickHouse làm pipeline thử lại mãi (chặn partition); record hỏng bị bỏ, không có dead-letter | `ReplacingMergeTree` với id sự kiện; phân biệt lỗi vĩnh viễn; topic DLQ cho analytics |
| Log vào `app.logs` bỏ khi đầy ([ADR 0018](decisions/0018-app-logs-async-sink.md)) | Kafka chậm/chết thì `app_logs` thiếu dòng (stdout vẫn đủ) | Ghi file + agent (Vector/Fluent Bit) |
| Xoá video từ chối khi `queued`/`processing` ([ADR 0020](decisions/0020-video-delete-and-edit.md)) | Video kẹt `queued` không xoá được qua API; xoá tay bằng SQL | Trạng thái `deleting` + dọn khi sự kiện đến muộn |
| Upload từ trình duyệt cần tới MinIO trực tiếp ([ADR 0019](decisions/0019-admin-browser-upload.md)) | Origin mở trang admin phải nằm trong `MINIO_CORS_ALLOW_ORIGIN`; presigned URL hết hạn sau 1 giờ, chưa có re-presign cho file rất lớn trên mạng chậm | Endpoint cấp lại URL cho part chưa xong |
| Mọi thứ một node | Kafka 1 broker (RF 1), Postgres/MinIO/ClickHouse đơn, transcoder tối đa 3 worker (3 partition) | Cluster thật, thêm partition |
| Secret dev trong `.env.example` | `JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin, MinIO, Postgres (ClickHouse dùng chung mật khẩu Postgres) nằm trong repo | Sinh secret khi `make env`, secret manager |

## Nhận xét review được chấp nhận (không sửa ở MVP)

Gom từ review từng task (M0–M6). Các mục đã sửa ở M7: header bảo mật nginx, wait strategy `/minio/health/ready` cho MinIO testcontainer, `stop_grace_period` 25 giây cho analytics, `completion_rate` ≤ 1, bỏ `COPY migrations` thừa trong Dockerfile transcoder/analytics, JWT kiểm tra `iat`, bcrypt dummy hash tính sẵn lúc khởi động, `ErrorBoundary` reset khi đổi route và dọn listener trong `afterEach`, giới hạn độ dài title/mô tả/thể loại và kiểm tra năm khi tạo video, id video dạng chữ hoa được chuẩn hoá ở complete/retry/delete, `S3_PUBLIC_ENDPOINT` của api đọc từ `.env`.

**API và dữ liệu**
- `httpx.DecodeJSON`: body quá 1 MiB trả `400` (không phải `413`); vài handler cũ trả nguyên thông báo lỗi của decoder; dữ liệu thừa sau object JSON bị bỏ qua.
- `register` trả `409` khi email đã tồn tại (theo spec) — lộ email đã đăng ký.
- Chưa test đồng thời cho chuyển trạng thái; `RevertQueued` không trả số dòng; `fail_reason` cũ vẫn còn khi video `queued` lại sau retry (UI chỉ hiện khi `failed`).
- Presigned URL mọi part cùng TTL 1 giờ; cleaner xoá dòng DB trước khi abort multipart (crash giữa chừng → multipart mồ côi, MinIO tự dọn theo lifecycle nếu cấu hình).
- Upload phụ đề cho video chưa `ready` được chấp nhận ([ADR 0014](decisions/0014-subtitles-srt-to-vtt.md)); upload phụ đề đúng lúc video bị xoá có thể để lại `hls/{id}/subs/*.vtt` mồ côi.
- SRT: hai cue không có dòng trống ở giữa bị gộp; mili-giây lỗi bị bỏ qua; `NOTE` chứa `-->` lọt qua kiểm tra VTT.
- `SaveProgress` last-write-wins; "Xem tiếp" lấy `LIMIT 20` trước khi lọc video `ready`; không rate limit sự kiện xem.
- Signing: regex id không bắt buộc UUID (đường dẫn đã chặn traversal).

**Transcoder và Kafka**
- Publish `video.transcoded` lỗi → làm lại toàn bộ transcode (tốn nhưng đúng); `RebalanceTimeout` 30 phút là trần cứng cho một job.
- Runner chỉ kiểm số tấm sprite ≥ kỳ vọng (không kiểm số frame, poster tồn tại); preview lệch 1–2 giây ở ô cuối với fps lẻ; `ParseProbe` nhận stream ảnh bìa (height 0).
- `Download` để lại file dở khi lỗi; `EnsureBucket` coi `BucketAlreadyExists` là thành công; cắt chuỗi (`truncate`, `msg[:500]`) có thể cắt giữa ký tự UTF-8.
- transcoder không có healthcheck; base image ghim tag, không ghim digest (trừ MinIO).
- ClickHouse client đọc body lỗi không giới hạn; tên bảng trong `Insert` không validate (chỉ dùng hằng); fetch lỗi lặp không backoff.

**Web**
- Player: listener `loadedmetadata` (Safari) không gỡ; `resume_at` mất nếu manifest đầu tiên `403`; seek > 1 giây reset bộ đếm khôi phục media; tải lại `/watch` phát thêm `play`/`seek` (nhiễu analytics); chỉ flush sự kiện khi `pagehide` (chưa dùng `visibilitychange`).
- Preview sprite hết hạn sau 2 giờ không tự làm mới; tràn mép, không hỗ trợ cảm ứng.
- `returnTo` nhận `"/\\"`; form auth đếm byte nhưng thông báo theo "ký tự"; `SearchBox` `autoFocus`; lỗi `/api/genres` bị nuốt; `api.ts` để lộ `TypeError`/`SyntaxError` thô khi mạng lỗi.
- Wiring `Player` và trang admin end-to-end trong trình duyệt chưa có test tự động (kiểm tra tay, xem [operations](operations.md#trang-quản-trị-m7)).
- `/dev/player.html` tải hls.js từ jsdelivr (cần internet).

**Script và công cụ**
- `make transcode` không quote `FILE`/`ID`; `seed-admin -password` lộ trong `ps` (dùng biến môi trường thay thế); README/ops dùng lệnh macOS (`open`) ở vài chỗ.
- `E2E_KAFKA_DOWN=1` khi không có `COMPOSE` bị bỏ qua im lặng; e2e-m6 bước SQL chỉ kiểm kết quả không rỗng.
- `go.mod` khai báo `go 1.27.4` (cần Go mới).
```

- [ ] **Step 2: Đối chiếu tài liệu với code**

Run:
```bash
for t in $(grep -oE '^[a-z0-9-]+:' Makefile | tr -d : | grep -v '^WEB$'); do grep -q "make $t" docs/operations.md || echo "thiếu make $t"; done
grep -n 'Chưa có: trang admin\|tới M6\|(tới M5)' README.md docs/*.md
for f in $(grep -ohE '\]\((decisions/[^)#]+|known-limitations\.md|operations\.md)' docs/*.md | sed 's/^](//'); do test -e "docs/$f" || echo "missing docs/$f"; done
```
Expected: không dòng nào in ra (mọi target Makefile có trong `operations.md`, không còn câu "chưa có trang admin", mọi link ADR/known-limitations tồn tại).

- [ ] **Step 3: Commit**

```bash
git add docs/architecture.md docs/operations.md README.md docs/known-limitations.md
git commit -m "docs: admin UI, operations for M7, README quickstart from zero, known limitations" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Kiểm tra cuối: dựng lại từ trạng thái dừng, e2e và toàn bộ test

Không sửa code. Mục tiêu: chứng minh MVP chạy từ `make down` (giữ volume) → `make up` → `make seed-admin` → `make e2e` → `make test-all`, và trang admin upload được bằng trình duyệt thật.

- [ ] **Step 1: Dựng lại stack**

Run: `make down && make up && make ps`
Expected: `make up` kết thúc không lỗi; `make ps` liệt kê `postgres`, `minio`, `kafka`, `clickhouse`, `nginx`, `api` ở `(healthy)`, `console`, `transcoder`, `analytics` ở `Up`; `minio-init`, `kafka-init` đã `Exited (0)`. `curl -sI http://localhost:8088/ | grep -i x-frame-options` in `X-Frame-Options: DENY`.

- [ ] **Step 2: Admin và e2e**

Run: `make seed-admin && make e2e`
Expected: `admin reset: admin@streamfilm.local …` (hoặc `admin created`), rồi bảng tổng kết 5 dòng `PASS` và `OK  5/5 kịch bản qua`.

- [ ] **Step 3: Toàn bộ test**

Run: `make test-all 2>&1 | grep -E '^(ok|FAIL|---)|Test Files|Tests '`
Expected: mọi package Go `ok`, không có `FAIL`/`--- FAIL`; `Test Files  19 passed (19)`, `Tests  77 passed (77)`.

- [ ] **Step 4: Kiểm tra tay trang admin trên Chrome**

1. `make sample`; mở http://localhost:8088, đăng nhập `admin@streamfilm.local` / `admin-dev-password`. Header có link **Quản trị**.
2. **Quản trị** → **Tải video lên** → chọn `tmp/sample.mp4`; tiêu đề tự điền `sample`; **Upload**. DevTools → Network: `POST /api/admin/videos` `201`, `OPTIONS` + `PUT` tới `localhost:9000/raw/…` `204`/`200`, `POST …/complete` `200`; thanh tiến độ tới 100%, trang chuyển sang `/admin/videos/{id}`.
3. Chip `Chờ xử lý` → `Đang xử lý` → `Sẵn sàng` không cần tải lại trang; xuất hiện **▶ Xem phim**, phát được.
4. Sửa tiêu đề → **Lưu** → `Đã lưu.`; trang **Tìm kiếm** gõ tiêu đề mới không dấu → ra video. Upload phụ đề `vi` (file `.srt` bất kỳ) → hiện trong danh sách và trong menu CC của player.
5. Đăng xuất, đăng ký viewer, mở http://localhost:8088/admin → `403 — Không có quyền`, header không có **Quản trị**.
6. Đăng nhập lại admin, **Xoá** video vừa tạo → xác nhận → biến khỏi danh sách.

- [ ] **Step 5: Kiểm tra repo sạch**

Run: `git status --short && git diff --stat b6fbfb0 -- go.mod go.sum web/app/package.json web/app/package-lock.json migrations`
Expected: cả hai lệnh không in gì: mọi thứ đã commit ở Task 1–11 (`tmp/` nằm trong `.gitignore`), không đổi dependency hay migration so với commit đầu M7 `b6fbfb0`.


---

## Self-review

**Độ phủ spec M7 và toàn MVP:**

| Yêu cầu | Task |
|---|---|
| M7: trang admin — danh sách video, trạng thái, xem lỗi (`fail_reason`), retry (§10) | 7 (`AdminVideosPage`, chip, poll), 8 (`AdminVideoPage` lỗi + thử lại), 9 (e2e failed → retry) |
| §6 `POST /api/admin/videos` + `/complete` từ giao diện (upload presigned) | 6 (`uploadVideo`), 8 (`AdminUploadPage`), 5 (CORS MinIO), 9 (e2e preflight + PUT như trình duyệt), 12 (kiểm tra tay) |
| §6 `PATCH/DELETE /api/admin/videos/{id}` | 2 (repo), 3 (PATCH), 4 (DELETE), 8 (UI), 9 (e2e), 10 (api.md, ADR 0020) |
| §6 `POST /api/admin/videos/{id}/subtitles` từ giao diện | 6 (`adminApi.uploadSubtitle` FormData), 3 (`subtitles` trong chi tiết), 8 (form phụ đề) |
| §8 bảo mật: chỉ admin vào `/api/admin/*`, giao diện chặn viewer | 4 (`TestRouterAuthz` PATCH/DELETE), 7 (`RequireAdmin`), 9 (viewer 403 với PATCH/DELETE/retry), 5 (header bảo mật nginx) |
| §9 `scripts/e2e.sh` một lệnh: sinh video mẫu, upload → ready → `/play` → master + segment 200, chữ ký sai / hết hạn 403 | 9 (`e2e.sh` → `e2e-m3` gồm `e2e-m2` làm đúng chuỗi này; `make e2e` tự `make sample`) |
| §9 unit/integration Go, Vitest frontend | 1–8 (test kèm từng thay đổi), 9/12 (`make test-all`) |
| M7 "e2e xanh" (§10) | 9, 12 |
| §11 bộ tài liệu đủ và khớp code: README (`make up` / `seed-admin` / `e2e`), architecture, flows (sequence), data-model (Postgres 0001–0004 + ClickHouse + topic), api (mọi endpoint), operations (mọi lệnh, cổng, UI, sự cố, SQL mẫu), ADR | 10, 11 (lệnh đối chiếu Makefile ↔ operations, route chi ↔ bảng api, link ADR) |
| Giới hạn chấp nhận nhìn thấy được | 11 (`docs/known-limitations.md`) |

Toàn MVP (M0–M6 đã xong, M7 rà lại): mọi endpoint §6 có mặt trong router và `api.md` (20 route + `/healthz`); mọi luồng §4 có sequence diagram trong `flows.md` (upload/transcode, upload từ trang admin, sửa/xoá, retry/DLQ, đăng nhập, signed URL, xem tiếp, analytics); `e2e.sh` chạy lại m2–m6 nên các yêu cầu cũ được kiểm tra lại mỗi lần.

**Triage nhận xét review (`.superpowers/sdd/minor-findings.md`):** sửa ở M7 — header bảo mật nginx (M4 T4-6), MinIO testcontainer chờ `/minio/health/ready` (M6 T7-9), `stop_grace_period` analytics 25 giây (M6 T7-9), `completion_rate` ≤ 1 (M6 T7-9), bỏ `COPY migrations` thừa (M2 T11-12, M6 T7-9), JWT `WithIssuedAt` (M3 T1-4), dummy hash warm-up (M3 T5-7), `ErrorBoundary` reset theo route (M4 T10-11) và dọn listener ở `afterEach` (M5 T11-12), giới hạn title/genres + kiểm year (M2 T5-8), id chữ hoa được chuẩn hoá ở complete/retry/delete (M2 T5-8 "uuid.Parse nhận dạng không chuẩn"), `S3_PUBLIC_ENDPOINT` không còn hardcode trong compose (M3 T8-10). Mọi mục còn lại được liệt kê trong `docs/known-limitations.md` (Task 11), nhóm theo API, transcoder/Kafka, web, script.

**Quét placeholder:** không có "TBD", "TODO", "tương tự Task N", "xử lý lỗi phù hợp"; mọi bước có code đầy đủ hoặc lệnh kèm kết quả mong đợi.

**Kiểm chứng:** toàn bộ code, tài liệu và các cặp "Thay A bằng B" trong plan được sinh từ một bản sao của `feat/mvp` (commit `b6fbfb0`) đã cài đủ M7, rồi một script áp lại các cặp theo đúng thứ tự task lên nội dung gốc (mỗi A khớp đúng một lần) và so từng byte với bản đã kiểm tra. Trên bản sao: `gofmt -l` rỗng, `go vet ./...`, `go test ./...` (Postgres, MinIO, Redpanda, nginx, ClickHouse, FFmpeg thật) đều `ok`; `npm ci && npm test` (`77 passed`) và `npm run build` xanh. Một stack compose riêng dựng **từ volume rỗng** (project `sfm7`, cổng 18088/18000/19000/19001/18123/15432, image tag `:m7val`, file override chỉ nằm ngoài repo; đã `down -v` và xoá image sau khi chạy, không đụng stack `streamfilm` và image `:dev`): `seed-admin` → `e2e-m7.sh` `OK  M7` → `e2e.sh` `OK  5/5 kịch bản qua` (152 giây). Kiểm tra bằng Chrome headless (Puppeteer) trên stack đó: đăng nhập admin, `/admin/upload` với file 151.6 MB (3 part, `PUT` cross-origin tới MinIO, thanh tiến độ 0→100%), tự chuyển sang trang chi tiết, chip `Đang xử lý` → `Sẵn sàng` qua poll, lưu metadata (`Đã lưu.`), upload phụ đề `vi`, danh sách hiện chip đúng; viewer mở `/admin` thấy `403 — Không có quyền` và không có link **Quản trị**. Preflight từ origin lạ (`http://evil.example`) không nhận `Access-Control-Allow-Origin`.

Phát hiện khi chạy thử và đã sửa trong plan: (1) `VideoJSON.Subtitles` với `omitempty` làm mất `[]` ở chi tiết → dùng `omitzero` (Go 1.24+); (2) form metadata đặt `key={updated_at}` bị remount sau khi lưu (mất thông báo `Đã lưu.`, xoá chữ đang gõ khi poll) → `key={video.id}` và lưu xong gọi lại `load()` (PATCH không trả `renditions`/`subtitles`); (3) `request()` đặt `Content-Type: application/json` cho mọi body kể cả `FormData` → chỉ đặt khi body là chuỗi; (4) hằng `MaxGenreLen`/`MinYear`/`MaxYear` đã có ở `viewer.go` → dùng lại thay vì khai báo trùng.

**Nhất quán chữ ký:**
- `catalog.Metadata` / `NormalizeMetadata` dùng ở `AdminHandler.patch`, `upload.Service.Create`, test; `Repo.UpdateMetadata(ctx, id, Metadata) (Video, error)` ↔ `AdminStore` ↔ `fakeAdminStore` ↔ `emptyStore`.
- `Repo.Delete(ctx, id) (Video, error)` ↔ `upload.Videos.Delete` ↔ `fakeVideos.Delete`; `storage.Store.DeletePrefix(ctx, bucket, prefix) (int, error)` ↔ `upload.Objects.DeletePrefix` ↔ `fakeObjects.DeletePrefix`.
- `catalog.ErrVideoBusy` → `upload.Handler.fail` → `409`; `ErrInvalidMetadata` → `ErrInvalidInput` (create) / `400` (patch).
- JSON: Go `VideoJSON` (`fail_reason`, `size_bytes`, `updated_at`, `subtitles[].vtt_key`) ↔ TS `AdminVideo`/`AdminSubtitle`; `CreateOutput{id,status,upload{upload_id,part_size,parts[{part_number,url}],expires_at}}` ↔ TS `CreateVideoResult`/`UploadPlan`; `PatchInput` ↔ TS `VideoPatch`.
- Hằng số giống nhau giữa code, Global Constraints và tài liệu: 200/5000/10/50/1888–2100 (metadata), 3 part/3 lần/3000 ms (web), 64 MiB/1 giờ/10 GB (upload), 25 giây (grace), 4 header bảo mật.

## Các kế hoạch tiếp theo

MVP hoàn tất sau M7. Hướng mở rộng nằm ở cột "Hướng làm tiếp" của `docs/known-limitations.md` (outbox, DRM, re-presign upload, phiên bản HLS để không dính cache khi transcode lại, cluster Kafka/ClickHouse).
