# Streamfilm M2: Upload + Kafka — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Admin tạo video qua `POST /api/admin/videos`, upload file thẳng lên MinIO bằng presigned multipart URL, gọi `/complete`; `api` publish `video.uploaded`, `transcoder worker` consume, transcode, upload HLS, publish `video.processing` / `video.transcoded` (hoặc `video.failed` + DLQ), `api` consume và đưa video sang `ready`. Toàn bộ chạy bằng `make up` + `make e2e-m2`, thấy message trong Redpanda Console.

**Architecture:** Thêm `cmd/api` (chi + pgx) gồm các module `catalog` (repository + máy trạng thái + consumer cập nhật trạng thái + endpoint đọc), `upload` (presigned multipart, complete, retry, job dọn upload bỏ dở). `internal/events` chứa schema JSON và helper franz-go (producer; consumer commit offset **sau khi** handler thành công, lỗi thì thử lại tại chỗ). `internal/worker` chứa logic job của transcoder (retry 5s/30s/2m, rồi `video.failed` + DLQ). Migration SQL nhúng bằng `embed`, chạy bằng runner tự viết lúc `api` khởi động. `api` và `transcoder` chạy trong docker compose (image riêng, transcoder có FFmpeg). Admin endpoint ở M2 **chưa có auth**; M3 bọc nhóm `/api/admin` bằng middleware yêu cầu role `admin`.

**Tech Stack:** Go 1.27.4, chi v5.5.0, pgx v5.13.0, franz-go v1.22.3 (+ kadm v1.20.0 cho test), google/uuid v1.6.0, aws-sdk-go-v2 s3 v1.114.10 (presign), testcontainers-go v0.45.0 (modules postgres, redpanda, minio), Docker images `golang:1.27.4-alpine3.24`, `alpine:3.24.4`, `postgres:16-alpine`, `docker.redpanda.com/redpandadata/redpanda:v25.2.4` (chỉ cho test).

**Spec:** `docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md` (mục 3, 4.1, 5, 6, 7, 9, 11)

## Global Constraints

- Go module path: `streamfilm`. Go 1.27.4 (theo `go.mod`).
- Trạng thái video: `uploading → queued → processing → ready`. Từ `queued` hoặc `processing` sang `failed`. Từ `failed` về `queued` khi admin retry. Không có chuyển nào khác.
- Transcoder **không bao giờ** ghi Postgres. Chỉ `api` ghi bảng `videos` / `renditions`.
- Kafka topics (đã tạo sẵn bởi `kafka-init`, 3 partition): `video.uploaded` (api → transcoder: `video_id`, `raw_key`), `video.uploaded.dlq` (transcoder: payload gốc + `error`, `attempts`), `video.processing` (transcoder → api: `video_id`), `video.transcoded` (transcoder → api: `video_id`, `duration_s`, `renditions[]`, `poster_key`, `sprite_key` — poster/sprite để trống tới M5), `video.failed` (transcoder → api: `video_id`, `reason`). **Key message = `video_id`.**
- Commit offset Kafka **sau khi** xử lý và publish thành công. Job bị ngắt giữa chừng thì được làm lại khi worker khởi động.
- Idempotent: output ghi đè `hls/{id}/`; api bỏ qua `video.transcoded` nếu video đã `ready` (chuyển trạng thái không hợp lệ thì log và bỏ qua).
- FFmpeg lỗi: thử lại 3 lần, giãn cách 5s, 30s, 2 phút (tổng 4 lần chạy). Hết lượt: publish `video.failed` và đưa vào `video.uploaded.dlq`.
- Upload bỏ dở: job định kỳ trong api xoá video `uploading` quá **24 giờ**, kèm object raw và multipart upload dở.
- Kafka không khả dụng: `/complete` (và `/retry`) trả **503**, admin thử lại.
- Dual-write Postgres + Kafka: chấp nhận ở MVP, ghi nhận outbox pattern cho bản sau.
- Presigned upload URL hết hạn sau **1 giờ**. Giới hạn kích thước file **10 GB** (`10 << 30` byte).
- Storage: bucket `raw` key `{id}/source.{ext}`; bucket `hls` prefix `{id}/` (`{id}/master.m3u8`, `{id}/{rung}/index.m3u8`).
- Log: `slog` JSON ra stdout, luôn có `service`, có `request_id` (HTTP) và `video_id` khi có.
- Cổng local (bind `127.0.0.1`): Postgres 5432, MinIO 9000/9001, Kafka host 29092 (trong mạng compose `kafka:9092`), Redpanda Console 8080, ClickHouse 8123, nginx 8088, **api 8000** (mới).
- Postgres: user/db `streamfilm`, mật khẩu `POSTGRES_PASSWORD`.
- Integration test tự `t.Skip` khi `-short`, thiếu Docker hoặc FFmpeg. Image MinIO trong test: `cgr.dev/chainguard/minio:latest-dev@sha256:5735e8125c4af7dd310dc5780508bfb850f0b68783a1a56089b5c64316a8f603` (`minio/minio` không pull được).
- Tài liệu tiếng Việt, chỉ mô tả những gì đã chạy, cập nhật trong cùng giai đoạn (spec mục 11).

## Quyết định thiết kế chính (ghi ADR ở Task 12)

| Quyết định | Lý do |
|---|---|
| Presigned **multipart** upload, part 64 MiB; `/complete` không cần body, api tự `ListParts` lấy ETag | Single PUT bị S3/R2 giới hạn 5 GB, spec cho phép 10 GB. Tự `ListParts` giúp client chỉ cần `PUT` từng part (không phải đọc header `ETag`, vốn cần cấu hình CORS) |
| Migration runner tự viết (~60 dòng, `embed` + `pg_advisory_lock` + bảng `schema_migrations`) | Không thêm dependency; dự án để học nên thấy rõ cơ chế. Mỗi file chạy trong một transaction |
| M2 chỉ tạo bảng `videos`, `renditions` | Mỗi giai đoạn thêm migration của mình (users ở M3, subtitles/watch_progress ở M5, cột `search` + `unaccent`/`pg_trgm` ở M4). Không tạo bảng chưa có code dùng |
| Thêm cột `upload_id`, `size_bytes` vào `videos` | Cần để complete/abort multipart và tính số part; spec §5 chưa có |
| Consumer thử lại record lỗi **tại chỗ** (backoff 1s→30s), không bỏ qua, không crash | Giữ thứ tự theo `video_id`, không mất sự kiện khi DB/Kafka chập chờn. Lỗi "nghiệp vụ" (JSON hỏng, chuyển trạng thái không hợp lệ) handler tự log và trả `nil` |
| Retry FFmpeg nằm **trong** handler của worker (sleep 5s/30s/2m), offset chưa commit trong lúc chờ | Đơn giản nhất mà vẫn đúng spec; mỗi worker xử lý tuần tự 1 job; scale bằng thêm worker (tối đa 3 = số partition) |
| `/complete`: đặt `queued` **trước**, publish sau; publish lỗi thì trả về `uploading` và 503 | Nếu publish trước, consumer của api có thể nhận `video.processing` khi DB còn `uploading` |
| `api` và `transcoder` chạy trong docker compose, có Makefile target chạy trên host khi dev | Spec §3 vẽ chúng trong compose và yêu cầu FFmpeg trong container transcoder. Presigned URL phải ký bằng host mà client thấy, nên thêm `S3_PUBLIC_ENDPOINT` |
| Test Kafka bằng Redpanda (testcontainers module `redpanda`) | Tương thích giao thức Kafka, khởi động nhanh; module `kafka` của testcontainers gắn với image Confluent |

## File Structure

```
streamfilm/
  .dockerignore                         # mới: thu nhỏ build context
  .env.example                          # + DATABASE_URL, KAFKA_BROKERS, S3_PUBLIC_ENDPOINT, HTTP_ADDR, ...
  Makefile                              # build 2 binary, up --build, run-api, run-worker, e2e-m2
  README.md                             # trạng thái M2
  migrations/
    embed.go                            # package migrations: //go:embed *.sql
    0001_videos.sql                     # videos, renditions
  cmd/api/main.go                       # wiring: migrate, router, consumer, cleaner, graceful shutdown
  cmd/transcoder/main.go                # subcommand file | worker
  cmd/transcoder/worker.go              # runWorker
  internal/config/config.go(+_test)     # thêm trường M2
  internal/db/db.go                     # Connect, Migrate
  internal/db/migrate_test.go
  internal/catalog/status.go(+_test)    # Status, CanTransition, Valid
  internal/catalog/repo.go(+_test)      # Repo (pgx)
  internal/catalog/consumer.go(+_test)  # StatusUpdater (video.processing/transcoded/failed)
  internal/catalog/handler.go(+_test)   # GET /api/admin/videos, /api/admin/videos/{id}
  internal/events/events.go(+_test)     # topic, schema, Event, NewRecord, Decode
  internal/events/kafka.go(+_test)      # Producer, Consumer (commit sau khi thành công)
  internal/storage/multipart.go(+_test) # presign multipart, complete, abort, exists, delete prefix
  internal/storage/s3.go                # Config.PublicEndpoint, presign client
  internal/upload/service.go(+_test)    # Create, Complete, Retry
  internal/upload/handler.go(+_test)    # POST create/complete/retry
  internal/upload/cleanup.go(+_test)    # Cleaner: xoá upload bỏ dở > 24h
  internal/httpx/httpx.go               # WriteJSON, WriteError, DecodeJSON, RequestLogger
  internal/worker/worker.go(+_test)     # Handle video.uploaded: retry, failed, DLQ
  internal/testutil/containers.go       # StartPostgres, StartKafka, StartMinIO
  deploy/docker/api.Dockerfile
  deploy/docker/transcoder.Dockerfile
  deploy/docker-compose.yml             # + service api, transcoder
  scripts/e2e-m2.sh                     # e2e: create → upload → complete → ready → nginx
  docs/architecture.md, docs/operations.md          # cập nhật
  docs/flows.md, docs/data-model.md, docs/api.md     # mới
  docs/decisions/0004..0008-*.md                     # ADR mới
```

---

### Task 1: Cấu hình M2

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `.env.example`

**Interfaces:**
- Consumes: `config.Config` hiện có (M1).
- Produces:
  ```go
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
  }
  func Load() Config
  ```

- [ ] **Step 1: Viết test thất bại** — thêm vào cuối `internal/config/config_test.go`:

```go
func TestLoadM2Defaults(t *testing.T) {
	for _, k := range []string{"S3_ENDPOINT", "S3_PUBLIC_ENDPOINT", "DATABASE_URL", "KAFKA_BROKERS",
		"HTTP_ADDR", "WORK_DIR", "STALE_UPLOAD_AFTER", "CLEANUP_INTERVAL"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.S3PublicEndpoint != "http://localhost:9000" {
		t.Errorf("S3PublicEndpoint = %q", c.S3PublicEndpoint)
	}
	if c.DatabaseURL != "postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", c.DatabaseURL)
	}
	if len(c.KafkaBrokers) != 1 || c.KafkaBrokers[0] != "localhost:29092" {
		t.Errorf("KafkaBrokers = %v", c.KafkaBrokers)
	}
	if c.HTTPAddr != ":8000" {
		t.Errorf("HTTPAddr = %q", c.HTTPAddr)
	}
	if c.WorkDir != os.TempDir() {
		t.Errorf("WorkDir = %q", c.WorkDir)
	}
	if c.StaleUploadAfter != 24*time.Hour || c.CleanupInterval != time.Hour {
		t.Errorf("durations = %v %v", c.StaleUploadAfter, c.CleanupInterval)
	}
}

func TestLoadM2Override(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	t.Setenv("KAFKA_BROKERS", "kafka:9092, other:9092 ,")
	t.Setenv("STALE_UPLOAD_AFTER", "90m")
	t.Setenv("CLEANUP_INTERVAL", "not-a-duration")
	c := Load()
	if c.S3PublicEndpoint != "http://minio:9000" {
		t.Errorf("S3PublicEndpoint phải mặc định bằng S3Endpoint, got %q", c.S3PublicEndpoint)
	}
	if len(c.KafkaBrokers) != 2 || c.KafkaBrokers[0] != "kafka:9092" || c.KafkaBrokers[1] != "other:9092" {
		t.Errorf("KafkaBrokers = %#v", c.KafkaBrokers)
	}
	if c.StaleUploadAfter != 90*time.Minute {
		t.Errorf("StaleUploadAfter = %v", c.StaleUploadAfter)
	}
	if c.CleanupInterval != time.Hour {
		t.Errorf("CleanupInterval sai định dạng phải về mặc định, got %v", c.CleanupInterval)
	}
}
```

Sửa dòng import đầu file thành:

```go
import (
	"os"
	"testing"
	"time"
)
```

- [ ] **Step 2: Chạy test để thấy lỗi**

Run: `go test ./internal/config/`
Expected: FAIL, lỗi biên dịch `c.S3PublicEndpoint undefined`.

- [ ] **Step 3: Viết lại `internal/config/config.go`**

```go
// Package config đọc cấu hình từ biến môi trường.
package config

import (
	"os"
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

- [ ] **Step 5: Cập nhật `.env.example`** — thêm vào cuối file:

```dotenv
# M2: api + transcoder worker khi chạy trên host (make run-api / make run-worker).
# Trong docker compose các giá trị này được đặt riêng trong deploy/docker-compose.yml.
S3_PUBLIC_ENDPOINT=http://localhost:9000
DATABASE_URL=postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable
KAFKA_BROKERS=localhost:29092
HTTP_ADDR=:8000
STALE_UPLOAD_AFTER=24h
CLEANUP_INTERVAL=1h
```

Thêm các dòng này vào `.env` hiện có (file không commit):

Run: `grep -q '^DATABASE_URL=' .env || { echo; tail -n 8 .env.example; } >> .env; grep -c '^KAFKA_BROKERS=' .env`
Expected: `1`. Nếu `POSTGRES_PASSWORD` trong `.env` khác `streamfilm`, sửa mật khẩu trong `DATABASE_URL` cho khớp.

- [ ] **Step 6: Commit**

```bash
git add internal/config .env.example
git commit -m "feat(config): add database, kafka, public S3 endpoint and cleanup settings"
```

---

### Task 2: Migration Postgres và runner nhúng

**Files:**
- Create: `migrations/embed.go`, `migrations/0001_videos.sql`, `internal/db/db.go`, `internal/db/migrate_test.go`, `internal/testutil/containers.go`

**Interfaces:**
- Consumes: `config.Config.DatabaseURL` (Task 1).
- Produces:
  ```go
  // package migrations
  var FS embed.FS
  // package db
  func Connect(ctx context.Context, url string) (*pgxpool.Pool, error)
  func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) // trả các version vừa áp
  // package testutil
  func StartPostgres(t *testing.T) string // DSN; skip khi -short hoặc không có Docker
  ```

- [ ] **Step 1: Thêm dependency**

Run:
```bash
go get github.com/jackc/pgx/v5@v5.13.0 github.com/testcontainers/testcontainers-go/modules/postgres@v0.45.0
```
Expected: `go.mod` có `github.com/jackc/pgx/v5 v5.13.0` và `github.com/testcontainers/testcontainers-go/modules/postgres v0.45.0`.

- [ ] **Step 2: Viết migration `migrations/0001_videos.sql`**

```sql
-- M2: video và rendition. Cột search (unaccent + pg_trgm) thêm ở M4, users ở M3, subtitles/watch_progress ở M5.
CREATE TABLE videos (
  id          uuid PRIMARY KEY,
  title       text   NOT NULL,
  description text   NOT NULL DEFAULT '',
  genres      text[] NOT NULL DEFAULT '{}',
  year        int,
  status      text   NOT NULL CHECK (status IN ('uploading','queued','processing','ready','failed')),
  fail_reason text,
  duration_s  int,
  raw_key     text   NOT NULL,
  upload_id   text,
  size_bytes  bigint NOT NULL,
  hls_prefix  text,
  poster_key  text,
  sprite_key  text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX videos_created_at_idx ON videos (created_at DESC);
CREATE INDEX videos_status_created_at_idx ON videos (status, created_at);

CREATE TABLE renditions (
  video_id     uuid NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  height       int  NOT NULL,
  bitrate_kbps int  NOT NULL,
  playlist_key text NOT NULL,
  PRIMARY KEY (video_id, height)
);
```

- [ ] **Step 3: Viết `migrations/embed.go`**

```go
// Package migrations nhúng các file SQL để api tự migrate khi khởi động.
// Tên file: NNNN_mo_ta.sql, áp theo thứ tự tên; version = tên file bỏ ".sql".
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 4: Viết helper container `internal/testutil/containers.go`**

```go
package testutil

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// StartPostgres chạy Postgres 16 trong container và trả DSN. Skip khi -short hoặc không có Docker.
func StartPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("streamfilm"),
		tcpostgres.WithUsername("streamfilm"),
		tcpostgres.WithPassword("streamfilm"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}
```

- [ ] **Step 5: Viết test thất bại `internal/db/migrate_test.go`**

```go
package db

import (
	"context"
	"testing"
	"testing/fstest"

	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

func TestMigrate(t *testing.T) {
	dsn := testutil.StartPostgres(t)
	ctx := context.Background()
	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	t.Run("applies embedded migrations once", func(t *testing.T) {
		applied, err := Migrate(ctx, pool, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) == 0 || applied[0] != "0001_videos" {
			t.Fatalf("applied = %v", applied)
		}
		again, err := Migrate(ctx, pool, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 0 {
			t.Errorf("second run applied %v, want none", again)
		}
		var n int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('videos', 'renditions')`).Scan(&n)
		if err != nil || n != 2 {
			t.Errorf("tables = %d, err = %v", n, err)
		}
	})

	t.Run("failed file is rolled back", func(t *testing.T) {
		fsys := fstest.MapFS{
			"0001_ok.sql":  {Data: []byte("CREATE TABLE mig_ok (id int);")},
			"0002_bad.sql": {Data: []byte("CREATE TABLE mig_bad (id int); SELECT no_such_column;")},
		}
		applied, err := Migrate(ctx, pool, fsys)
		if err == nil {
			t.Fatal("want error from 0002_bad.sql")
		}
		if len(applied) != 1 || applied[0] != "0001_ok" {
			t.Errorf("applied = %v", applied)
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('mig_bad') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("mig_bad must not exist after rollback")
		}
	})
}
```

- [ ] **Step 6: Chạy test để thấy lỗi**

Run: `go test ./internal/db/`
Expected: FAIL, lỗi biên dịch `undefined: Connect`.

- [ ] **Step 7: Viết `internal/db/db.go`**

```go
// Package db kết nối Postgres và chạy migration nhúng.
package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrateLockID là khoá advisory để nhiều instance api không migrate cùng lúc.
const migrateLockID int64 = 20261114

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// Migrate áp các file *.sql trong fsys theo thứ tự tên, mỗi file trong một transaction,
// và ghi version vào bảng schema_migrations. Trả về các version vừa áp.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockID); err != nil {
		return nil, fmt.Errorf("advisory lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrateLockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		var done bool
		if err := conn.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&done); err != nil {
			return applied, err
		}
		if done {
			continue
		}
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return applied, err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, err
		}
		// Exec không tham số dùng simple protocol nên một file chứa được nhiều câu lệnh.
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("commit migration %s: %w", name, err)
		}
		applied = append(applied, version)
	}
	return applied, nil
}
```

- [ ] **Step 8: Chạy test**

Run: `go test ./internal/db/ -v -run TestMigrate`
Expected: `--- PASS: TestMigrate` với 2 subtest PASS. Với `go test -short ./internal/db/` thì `SKIP`.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum migrations internal/db internal/testutil/containers.go
git commit -m "feat(db): embedded SQL migrations for videos and renditions"
```

---

### Task 3: Catalog — máy trạng thái và repository

**Files:**
- Create: `internal/catalog/status.go`, `internal/catalog/status_test.go`, `internal/catalog/repo.go`, `internal/catalog/repo_test.go`

**Interfaces:**
- Consumes: `db.Connect`, `db.Migrate`, `migrations.FS`, `testutil.StartPostgres` (Task 2).
- Produces:
  ```go
  type Status string
  const (StatusUploading, StatusQueued, StatusProcessing, StatusReady, StatusFailed Status = ...)
  func CanTransition(from, to Status) bool
  func (s Status) Valid() bool

  var ErrNotFound, ErrInvalidTransition error
  type Video struct {
  	ID, Title, Description string
  	Genres                 []string
  	Year                   int
  	Status                 Status
  	FailReason             string
  	DurationS              int
  	RawKey, UploadID       string
  	SizeBytes              int64
  	HLSPrefix              string
  	CreatedAt, UpdatedAt   time.Time
  }
  type Rendition struct { Height int `json:"height"`; BitrateKbps int `json:"bitrate_kbps"`; PlaylistKey string `json:"playlist_key"` }
  type ReadyInfo struct { DurationS int; HLSPrefix string; Renditions []Rendition }
  type ListFilter struct { Status Status; Limit int }

  func NewRepo(pool *pgxpool.Pool) *Repo
  func (r *Repo) Create(ctx context.Context, v Video) error
  func (r *Repo) Get(ctx context.Context, id string) (Video, error)
  func (r *Repo) List(ctx context.Context, f ListFilter) ([]Video, error)
  func (r *Repo) Renditions(ctx context.Context, id string) ([]Rendition, error)
  func (r *Repo) Transition(ctx context.Context, id string, to Status) (Status, error)        // trả trạng thái cũ
  func (r *Repo) MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error)
  func (r *Repo) MarkFailed(ctx context.Context, id, reason string) (Status, error)
  func (r *Repo) RevertQueued(ctx context.Context, id string, to Status) error               // bù trừ khi publish lỗi
  func (r *Repo) ListStaleUploading(ctx context.Context, before time.Time) ([]Video, error)
  func (r *Repo) DeleteUploading(ctx context.Context, id string) error
  ```

- [ ] **Step 1: Viết test thất bại `internal/catalog/status_test.go`**

```go
package catalog

import "testing"

func TestCanTransition(t *testing.T) {
	allowed := [][2]Status{
		{StatusUploading, StatusQueued},
		{StatusQueued, StatusProcessing},
		{StatusQueued, StatusFailed},
		{StatusProcessing, StatusReady},
		{StatusProcessing, StatusFailed},
		{StatusFailed, StatusQueued},
	}
	for _, p := range allowed {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be allowed", p[0], p[1])
		}
	}
	denied := [][2]Status{
		{StatusUploading, StatusProcessing},
		{StatusUploading, StatusReady},
		{StatusQueued, StatusReady},
		{StatusProcessing, StatusQueued},
		{StatusProcessing, StatusProcessing},
		{StatusReady, StatusReady},
		{StatusReady, StatusQueued},
		{StatusReady, StatusFailed},
		{StatusFailed, StatusProcessing},
		{StatusFailed, StatusFailed},
	}
	for _, p := range denied {
		if CanTransition(p[0], p[1]) {
			t.Errorf("%s -> %s should be denied", p[0], p[1])
		}
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range []Status{StatusUploading, StatusQueued, StatusProcessing, StatusReady, StatusFailed} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if Status("deleted").Valid() || Status("").Valid() {
		t.Error("unknown status must be invalid")
	}
}
```

- [ ] **Step 2: Chạy test để thấy lỗi**

Run: `go test -short ./internal/catalog/`
Expected: FAIL, `undefined: Status`.

- [ ] **Step 3: Viết `internal/catalog/status.go`**

```go
// Package catalog quản lý video, rendition và vòng đời trạng thái của video.
package catalog

type Status string

const (
	StatusUploading  Status = "uploading"
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
	StatusFailed     Status = "failed"
)

// transitions: uploading → queued → processing → ready; queued|processing → failed; failed → queued (retry).
var transitions = map[Status][]Status{
	StatusUploading:  {StatusQueued},
	StatusQueued:     {StatusProcessing, StatusFailed},
	StatusProcessing: {StatusReady, StatusFailed},
	StatusFailed:     {StatusQueued},
}

func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

func (s Status) Valid() bool {
	switch s {
	case StatusUploading, StatusQueued, StatusProcessing, StatusReady, StatusFailed:
		return true
	}
	return false
}
```

- [ ] **Step 4: Chạy test**

Run: `go test -short ./internal/catalog/`
Expected: `ok  	streamfilm/internal/catalog`

- [ ] **Step 5: Viết test thất bại `internal/catalog/repo_test.go`**

```go
package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

const (
	idA = "11111111-1111-1111-1111-111111111111"
	idB = "22222222-2222-2222-2222-222222222222"
	idC = "33333333-3333-3333-3333-333333333333"
	idX = "99999999-9999-9999-9999-999999999999"
)

func newTestRepo(t *testing.T) *Repo {
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
	return NewRepo(pool)
}

func newVideo(id string) Video {
	return Video{ID: id, Title: "Phim " + id[:4], Genres: []string{"drama"}, Year: 2024,
		RawKey: id + "/source.mp4", UploadID: "up-" + id[:4], SizeBytes: 1234}
}

func TestRepo(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("get", func(t *testing.T) {
		v, err := r.Get(ctx, idA)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != StatusUploading || v.Title != "Phim 1111" || v.Year != 2024 ||
			len(v.Genres) != 1 || v.Genres[0] != "drama" || v.UploadID != "up-1111" ||
			v.RawKey != idA+"/source.mp4" || v.SizeBytes != 1234 || v.CreatedAt.IsZero() {
			t.Errorf("unexpected video: %+v", v)
		}
		if _, err := r.Get(ctx, idX); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get unknown: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("happy path to ready", func(t *testing.T) {
		from, err := r.Transition(ctx, idA, StatusQueued)
		if err != nil || from != StatusUploading {
			t.Fatalf("to queued: from=%s err=%v", from, err)
		}
		if _, err := r.Transition(ctx, idA, StatusReady); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("queued -> ready: err = %v, want ErrInvalidTransition", err)
		}
		if _, err := r.Transition(ctx, idA, StatusProcessing); err != nil {
			t.Fatal(err)
		}
		info := ReadyInfo{DurationS: 30, HLSPrefix: idA + "/", Renditions: []Rendition{
			{Height: 360, BitrateKbps: 800, PlaylistKey: idA + "/360p/index.m3u8"},
			{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"},
		}}
		if _, err := r.MarkReady(ctx, idA, info); err != nil {
			t.Fatal(err)
		}
		v, _ := r.Get(ctx, idA)
		if v.Status != StatusReady || v.DurationS != 30 || v.HLSPrefix != idA+"/" {
			t.Errorf("after ready: %+v", v)
		}
		rs, err := r.Renditions(ctx, idA)
		if err != nil || len(rs) != 2 || rs[0].Height != 720 || rs[1].PlaylistKey != idA+"/360p/index.m3u8" {
			t.Errorf("renditions = %+v, err = %v", rs, err)
		}
		if _, err := r.MarkReady(ctx, idA, info); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("duplicate transcoded: err = %v, want ErrInvalidTransition", err)
		}
		if _, err := r.Transition(ctx, idX, StatusQueued); !errors.Is(err, ErrNotFound) {
			t.Errorf("transition unknown: err = %v", err)
		}
	})

	t.Run("fail, retry and revert", func(t *testing.T) {
		if _, err := r.Transition(ctx, idB, StatusQueued); err != nil {
			t.Fatal(err)
		}
		if _, err := r.MarkFailed(ctx, idB, "ffmpeg: boom"); err != nil {
			t.Fatal(err)
		}
		v, _ := r.Get(ctx, idB)
		if v.Status != StatusFailed || v.FailReason != "ffmpeg: boom" {
			t.Errorf("after failed: %+v", v)
		}
		if _, err := r.Transition(ctx, idB, StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := r.RevertQueued(ctx, idB, StatusFailed); err != nil {
			t.Fatal(err)
		}
		v, _ = r.Get(ctx, idB)
		if v.Status != StatusFailed || v.FailReason != "ffmpeg: boom" {
			t.Errorf("after revert: %+v", v)
		}
	})

	t.Run("list", func(t *testing.T) {
		all, err := r.List(ctx, ListFilter{})
		if err != nil || len(all) != 2 {
			t.Fatalf("list all = %d, err = %v", len(all), err)
		}
		failed, err := r.List(ctx, ListFilter{Status: StatusFailed})
		if err != nil || len(failed) != 1 || failed[0].ID != idB || failed[0].FailReason != "ffmpeg: boom" {
			t.Errorf("list failed = %+v, err = %v", failed, err)
		}
	})

	t.Run("stale uploads", func(t *testing.T) {
		if err := r.Create(ctx, newVideo(idC)); err != nil {
			t.Fatal(err)
		}
		stale, err := r.ListStaleUploading(ctx, time.Now().Add(time.Hour))
		if err != nil || len(stale) != 1 || stale[0].ID != idC || stale[0].UploadID != "up-3333" {
			t.Fatalf("stale = %+v, err = %v", stale, err)
		}
		none, _ := r.ListStaleUploading(ctx, time.Now().Add(-time.Hour))
		if len(none) != 0 {
			t.Errorf("fresh upload reported stale: %+v", none)
		}
		if err := r.DeleteUploading(ctx, idC); err != nil {
			t.Fatal(err)
		}
		if err := r.DeleteUploading(ctx, idC); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete twice: err = %v", err)
		}
		if err := r.DeleteUploading(ctx, idA); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete ready video must be refused: err = %v", err)
		}
	})
}
```

- [ ] **Step 6: Chạy test để thấy lỗi**

Run: `go test ./internal/catalog/ -run TestRepo`
Expected: FAIL, `undefined: Repo`.

- [ ] **Step 7: Viết `internal/catalog/repo.go`**

```go
package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound          = errors.New("video not found")
	ErrInvalidTransition = errors.New("invalid status transition")
)

type Video struct {
	ID, Title, Description string
	Genres                 []string
	Year                   int // 0 = không rõ
	Status                 Status
	FailReason             string // lý do lỗi gần nhất; xoá khi video ready
	DurationS              int
	RawKey, UploadID       string
	SizeBytes              int64
	HLSPrefix              string
	CreatedAt, UpdatedAt   time.Time
}

type Rendition struct {
	Height      int    `json:"height"`
	BitrateKbps int    `json:"bitrate_kbps"`
	PlaylistKey string `json:"playlist_key"`
}

type ReadyInfo struct {
	DurationS  int
	HLSPrefix  string
	Renditions []Rendition
}

type ListFilter struct {
	Status Status // rỗng = mọi trạng thái
	Limit  int    // <= 0 hoặc > 200 thì dùng 50
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const videoColumns = `id::text, title, description, genres, COALESCE(year, 0), status,
	COALESCE(fail_reason, ''), COALESCE(duration_s, 0), raw_key, COALESCE(upload_id, ''),
	size_bytes, COALESCE(hls_prefix, ''), created_at, updated_at`

func scanVideo(row pgx.Row) (Video, error) {
	var v Video
	var status string
	err := row.Scan(&v.ID, &v.Title, &v.Description, &v.Genres, &v.Year, &status,
		&v.FailReason, &v.DurationS, &v.RawKey, &v.UploadID, &v.SizeBytes, &v.HLSPrefix,
		&v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Video{}, ErrNotFound
	}
	v.Status = Status(status)
	return v, err
}

// Create thêm video mới ở trạng thái uploading.
func (r *Repo) Create(ctx context.Context, v Video) error {
	if v.Genres == nil {
		v.Genres = []string{}
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO videos
		(id, title, description, genres, year, status, raw_key, upload_id, size_bytes)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0), 'uploading', $6, NULLIF($7, ''), $8)`,
		v.ID, v.Title, v.Description, v.Genres, v.Year, v.RawKey, v.UploadID, v.SizeBytes)
	if err != nil {
		return fmt.Errorf("insert video: %w", err)
	}
	return nil
}

func (r *Repo) Get(ctx context.Context, id string) (Video, error) {
	return scanVideo(r.pool.QueryRow(ctx, `SELECT `+videoColumns+` FROM videos WHERE id = $1`, id))
}

func (r *Repo) List(ctx context.Context, f ListFilter) ([]Video, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC LIMIT $2`, string(f.Status), f.Limit)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}

func collectVideos(rows pgx.Rows) ([]Video, error) {
	defer rows.Close()
	var out []Video
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *Repo) Renditions(ctx context.Context, id string) ([]Rendition, error) {
	rows, err := r.pool.Query(ctx, `SELECT height, bitrate_kbps, playlist_key FROM renditions
		WHERE video_id = $1 ORDER BY height DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rendition
	for rows.Next() {
		var rd Rendition
		if err := rows.Scan(&rd.Height, &rd.BitrateKbps, &rd.PlaylistKey); err != nil {
			return nil, err
		}
		out = append(out, rd)
	}
	return out, rows.Err()
}

// transition khoá dòng (FOR UPDATE), kiểm tra máy trạng thái, đổi status và chạy apply trong cùng transaction.
// Trả về trạng thái trước khi đổi.
func (r *Repo) transition(ctx context.Context, id string, to Status, apply func(pgx.Tx) error) (Status, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var cur string
	err = tx.QueryRow(ctx, `SELECT status FROM videos WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	from := Status(cur)
	if !CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	if _, err := tx.Exec(ctx, `UPDATE videos SET status = $2, updated_at = now() WHERE id = $1`,
		id, string(to)); err != nil {
		return from, err
	}
	if apply != nil {
		if err := apply(tx); err != nil {
			return from, err
		}
	}
	return from, tx.Commit(ctx)
}

func (r *Repo) Transition(ctx context.Context, id string, to Status) (Status, error) {
	return r.transition(ctx, id, to, nil)
}

// MarkReady: processing → ready, ghi thời lượng, prefix HLS và thay toàn bộ rendition.
func (r *Repo) MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error) {
	return r.transition(ctx, id, StatusReady, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE videos SET duration_s = $2, hls_prefix = $3, fail_reason = NULL
			WHERE id = $1`, id, info.DurationS, info.HLSPrefix); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM renditions WHERE video_id = $1`, id); err != nil {
			return err
		}
		for _, rd := range info.Renditions {
			if _, err := tx.Exec(ctx, `INSERT INTO renditions (video_id, height, bitrate_kbps, playlist_key)
				VALUES ($1, $2, $3, $4)`, id, rd.Height, rd.BitrateKbps, rd.PlaylistKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkFailed: queued|processing → failed, ghi lý do.
func (r *Repo) MarkFailed(ctx context.Context, id, reason string) (Status, error) {
	return r.transition(ctx, id, StatusFailed, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE videos SET fail_reason = $2 WHERE id = $1`, id, reason)
		return err
	})
}

// RevertQueued là bước bù trừ (không thuộc máy trạng thái): khi đã đặt queued nhưng publish
// video.uploaded thất bại thì trả video về trạng thái trước đó (uploading hoặc failed).
func (r *Repo) RevertQueued(ctx context.Context, id string, to Status) error {
	_, err := r.pool.Exec(ctx, `UPDATE videos SET status = $2, updated_at = now()
		WHERE id = $1 AND status = 'queued'`, id, string(to))
	return err
}

func (r *Repo) ListStaleUploading(ctx context.Context, before time.Time) ([]Video, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE status = 'uploading' AND created_at < $1 ORDER BY created_at LIMIT 100`, before)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}

// DeleteUploading xoá video chỉ khi còn ở trạng thái uploading; ngược lại trả ErrNotFound.
func (r *Repo) DeleteUploading(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM videos WHERE id = $1 AND status = 'uploading'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 8: Chạy test**

Run: `go test ./internal/catalog/ -v`
Expected: `TestCanTransition`, `TestStatusValid`, `TestRepo` (5 subtest) đều PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/catalog
git commit -m "feat(catalog): video status machine and Postgres repository"
```

---

### Task 4: Events — schema và helper Kafka (franz-go)

**Files:**
- Create: `internal/events/events.go`, `internal/events/events_test.go`, `internal/events/kafka.go`, `internal/events/kafka_test.go`
- Modify: `internal/testutil/containers.go`

**Interfaces:**
- Produces:
  ```go
  const TopicVideoUploaded, TopicVideoUploadedDLQ, TopicVideoProcessing, TopicVideoTranscoded, TopicVideoFailed = ...
  var VideoTopics []string
  type Event interface{ Key() string } // Key = video_id
  type VideoUploaded struct{ VideoID, RawKey string }
  type VideoProcessing struct{ VideoID string }
  type Rendition struct{ Height, BitrateKbps int; PlaylistKey string }
  type VideoTranscoded struct{ VideoID string; DurationS int; HLSPrefix string; Renditions []Rendition; PosterKey, SpriteKey string }
  type VideoFailed struct{ VideoID, Reason string }
  type DeadLetter struct{ VideoID string; Payload json.RawMessage; Error string; Attempts int; FailedAt time.Time }
  func NewRecord(topic string, ev Event) (*kgo.Record, error)
  func Decode[T Event](data []byte) (T, error)

  type Producer struct{ /* unexported */ }
  func NewProducer(brokers []string) (*Producer, error)
  func (p *Producer) Publish(ctx context.Context, topic string, ev Event) error
  func (p *Producer) Close()

  type Handler func(ctx context.Context, rec *kgo.Record) error
  type Consumer struct{ Backoff func(attempt int) time.Duration /* + unexported */ }
  func NewConsumer(brokers []string, group string, log *slog.Logger, topics ...string) (*Consumer, error)
  func DefaultBackoff(attempt int) time.Duration
  func (c *Consumer) Run(ctx context.Context, h Handler) error // nil khi ctx bị huỷ
  func (c *Consumer) Close()

  // testutil
  func StartKafka(t *testing.T, topics ...string) []string // Redpanda, mỗi topic 1 partition
  ```

- [ ] **Step 1: Thêm dependency**

Run:
```bash
go get github.com/twmb/franz-go@v1.22.3 github.com/twmb/franz-go/pkg/kadm@v1.20.0 github.com/testcontainers/testcontainers-go/modules/redpanda@v0.45.0
```
Expected: `go.mod` có 3 module trên.

- [ ] **Step 2: Viết test thất bại `internal/events/events_test.go`**

```go
package events

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestTranscodedRecord(t *testing.T) {
	ev := VideoTranscoded{VideoID: "v1", DurationS: 30, HLSPrefix: "v1/", Renditions: []Rendition{
		{Height: 720, BitrateKbps: 2800, PlaylistKey: "v1/720p/index.m3u8"},
	}}
	rec, err := NewRecord(TopicVideoTranscoded, ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"video_id":"v1","duration_s":30,"hls_prefix":"v1/","renditions":[{"height":720,"bitrate_kbps":2800,"playlist_key":"v1/720p/index.m3u8"}]}`
	if string(rec.Value) != want {
		t.Errorf("value:\n got %s\nwant %s", rec.Value, want)
	}
	if string(rec.Key) != "v1" || rec.Topic != "video.transcoded" {
		t.Errorf("key=%q topic=%q", rec.Key, rec.Topic)
	}
	got, err := Decode[VideoTranscoded](rec.Value)
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Errorf("decode = %+v, err = %v", got, err)
	}
}

func TestUploadedJSON(t *testing.T) {
	rec, err := NewRecord(TopicVideoUploaded, VideoUploaded{VideoID: "v1", RawKey: "v1/source.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.Value) != `{"video_id":"v1","raw_key":"v1/source.mp4"}` {
		t.Errorf("value = %s", rec.Value)
	}
}

func TestDecodeErrors(t *testing.T) {
	if _, err := Decode[VideoUploaded]([]byte(`{"raw_key":"x"}`)); err == nil {
		t.Error("missing video_id must fail")
	}
	if _, err := Decode[VideoFailed]([]byte("not json")); err == nil {
		t.Error("invalid JSON must fail")
	}
}

func TestDeadLetterJSON(t *testing.T) {
	dl := DeadLetter{VideoID: "v1", Payload: json.RawMessage(`{"video_id":"v1"}`), Error: "boom",
		Attempts: 4, FailedAt: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
	b, err := json.Marshal(dl)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"video_id":"v1","payload":{"video_id":"v1"},"error":"boom","attempts":4,"failed_at":"2026-11-14T10:00:00Z"}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
	if dl.Key() != "v1" {
		t.Errorf("key = %q", dl.Key())
	}
}

func TestVideoTopics(t *testing.T) {
	want := []string{"video.uploaded", "video.uploaded.dlq", "video.processing", "video.transcoded", "video.failed"}
	if !reflect.DeepEqual(VideoTopics, want) {
		t.Errorf("VideoTopics = %v", VideoTopics)
	}
}
```

- [ ] **Step 3: Chạy test để thấy lỗi**

Run: `go test -short ./internal/events/`
Expected: FAIL, `undefined: VideoTranscoded`.

- [ ] **Step 4: Viết `internal/events/events.go`**

```go
// Package events định nghĩa topic, schema sự kiện (JSON) và helper Kafka dùng chung.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	TopicVideoUploaded    = "video.uploaded"
	TopicVideoUploadedDLQ = "video.uploaded.dlq"
	TopicVideoProcessing  = "video.processing"
	TopicVideoTranscoded  = "video.transcoded"
	TopicVideoFailed      = "video.failed"
)

var VideoTopics = []string{TopicVideoUploaded, TopicVideoUploadedDLQ, TopicVideoProcessing,
	TopicVideoTranscoded, TopicVideoFailed}

// Event là mọi payload publish lên Kafka. Key() là key message (video_id), giữ thứ tự theo video.
type Event interface {
	Key() string
}

type VideoUploaded struct {
	VideoID string `json:"video_id"`
	RawKey  string `json:"raw_key"`
}

type VideoProcessing struct {
	VideoID string `json:"video_id"`
}

type Rendition struct {
	Height      int    `json:"height"`
	BitrateKbps int    `json:"bitrate_kbps"`
	PlaylistKey string `json:"playlist_key"`
}

type VideoTranscoded struct {
	VideoID    string      `json:"video_id"`
	DurationS  int         `json:"duration_s"`
	HLSPrefix  string      `json:"hls_prefix"`
	Renditions []Rendition `json:"renditions"`
	PosterKey  string      `json:"poster_key,omitempty"` // M5
	SpriteKey  string      `json:"sprite_key,omitempty"` // M5
}

type VideoFailed struct {
	VideoID string `json:"video_id"`
	Reason  string `json:"reason"`
}

// DeadLetter ghi vào video.uploaded.dlq: payload gốc + lỗi cuối + số lần đã thử.
type DeadLetter struct {
	VideoID  string          `json:"video_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	Error    string          `json:"error"`
	Attempts int             `json:"attempts"`
	FailedAt time.Time       `json:"failed_at"`
}

func (e VideoUploaded) Key() string   { return e.VideoID }
func (e VideoProcessing) Key() string { return e.VideoID }
func (e VideoTranscoded) Key() string { return e.VideoID }
func (e VideoFailed) Key() string     { return e.VideoID }
func (e DeadLetter) Key() string      { return e.VideoID }

// NewRecord mã hoá ev thành record Kafka với key = ev.Key().
func NewRecord(topic string, ev Event) (*kgo.Record, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", topic, err)
	}
	return &kgo.Record{Topic: topic, Key: []byte(ev.Key()), Value: b}, nil
}

// Decode parse JSON và bắt buộc có video_id.
func Decode[T Event](data []byte) (T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("decode event: %w", err)
	}
	if v.Key() == "" {
		return v, errors.New("decode event: missing video_id")
	}
	return v, nil
}
```

- [ ] **Step 5: Chạy test**

Run: `go test -short ./internal/events/`
Expected: `ok  	streamfilm/internal/events`

- [ ] **Step 6: Thêm `StartKafka` vào `internal/testutil/containers.go`**

Thay khối import bằng:

```go
import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)
```

Thêm vào cuối file:

```go
// StartKafka chạy Redpanda (tương thích giao thức Kafka) và tạo sẵn topics, mỗi topic 1 partition
// để thứ tự record trong test là xác định. Trả danh sách seed broker.
func StartKafka(t *testing.T, topics ...string) []string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v25.2.4")
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := ctr.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	resp, err := kadm.NewClient(cl).CreateTopics(ctx, 1, 1, nil, topics...)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Error(); err != nil {
		t.Fatal(err)
	}
	return []string{broker}
}
```

- [ ] **Step 7: Viết test thất bại `internal/events/kafka_test.go`**

```go
package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/testutil"
)

// Lần chạy 1: v1 thành công (commit), v2 lỗi mãi rồi ctx bị huỷ (không commit).
// Lần chạy 2 cùng group: chỉ nhận lại v2. Chứng minh commit offset chỉ xảy ra sau khi handler thành công.
func TestConsumerCommitsOnlyAfterSuccess(t *testing.T) {
	brokers := testutil.StartKafka(t, VideoTopics...)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	p, err := NewProducer(brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, id := range []string{"v1", "v2"} {
		if err := p.Publish(ctx, TopicVideoUploaded, VideoUploaded{VideoID: id, RawKey: id + "/source.mp4"}); err != nil {
			t.Fatal(err)
		}
	}

	run := func(h func(ctx context.Context, cancel context.CancelFunc, ev VideoUploaded) error) {
		t.Helper()
		c, err := NewConsumer(brokers, "test-group", log, TopicVideoUploaded)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.Backoff = func(int) time.Duration { return 10 * time.Millisecond }
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		err = c.Run(rctx, func(hctx context.Context, rec *kgo.Record) error {
			ev, err := Decode[VideoUploaded](rec.Value)
			if err != nil {
				return err
			}
			if string(rec.Key) != ev.VideoID {
				t.Errorf("key = %q, want %q", rec.Key, ev.VideoID)
			}
			return h(hctx, cancel, ev)
		})
		if err != nil {
			t.Fatal(err)
		}
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			t.Fatal("consumer run timed out")
		}
	}

	var first []string
	v2Attempts := 0
	run(func(_ context.Context, cancel context.CancelFunc, ev VideoUploaded) error {
		if ev.VideoID == "v2" {
			v2Attempts++
			if v2Attempts == 3 {
				cancel()
			}
			return errors.New("boom")
		}
		first = append(first, ev.VideoID)
		return nil
	})
	if len(first) != 1 || first[0] != "v1" || v2Attempts != 3 {
		t.Fatalf("first run: handled=%v v2Attempts=%d", first, v2Attempts)
	}

	var second []string
	run(func(_ context.Context, cancel context.CancelFunc, ev VideoUploaded) error {
		second = append(second, ev.VideoID)
		cancel()
		return nil
	})
	if len(second) != 1 || second[0] != "v2" {
		t.Errorf("second run handled %v, want [v2] (v1 đã commit)", second)
	}
}

func TestDefaultBackoff(t *testing.T) {
	cases := map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 50: 30 * time.Second}
	for n, want := range cases {
		if got := DefaultBackoff(n); got != want {
			t.Errorf("DefaultBackoff(%d) = %v, want %v", n, got, want)
		}
	}
}
```

- [ ] **Step 8: Chạy test để thấy lỗi**

Run: `go test ./internal/events/`
Expected: FAIL, `undefined: NewProducer`.

- [ ] **Step 9: Viết `internal/events/kafka.go`**

```go
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer publish sự kiện; ProduceSync chờ broker xác nhận (acks=all, idempotent mặc định của franz-go).
type Producer struct {
	cl *kgo.Client
}

func NewProducer(brokers []string) (*Producer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		// Kafka chết thì record lỗi sau tối đa 10s thay vì treo mãi (spec: /complete trả 503).
		kgo.RecordDeliveryTimeout(10*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &Producer{cl: cl}, nil
}

func (p *Producer) Publish(ctx context.Context, topic string, ev Event) error {
	rec, err := NewRecord(topic, ev)
	if err != nil {
		return err
	}
	if err := p.cl.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

func (p *Producer) Close() { p.cl.Close() }

// Handler xử lý một record. Trả nil thì offset được commit; trả lỗi thì record được thử lại tại chỗ.
type Handler func(ctx context.Context, rec *kgo.Record) error

type Consumer struct {
	cl  *kgo.Client
	log *slog.Logger
	// Backoff trả thời gian chờ trước lần thử lại thứ attempt (bắt đầu từ 1) khi handler lỗi.
	Backoff func(attempt int) time.Duration
}

func NewConsumer(brokers []string, group string, log *slog.Logger, topics ...string) (*Consumer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topics...),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		// Không cho rebalance chen vào giữa lúc đang xử lý record đã poll.
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer %s: %w", group, err)
	}
	return &Consumer{cl: cl, log: log.With("group", group), Backoff: DefaultBackoff}, nil
}

// DefaultBackoff: 1s, 2s, 4s, 8s, 16s, rồi 30s.
func DefaultBackoff(attempt int) time.Duration {
	d := time.Second << min(attempt-1, 5)
	return min(d, 30*time.Second)
}

// Run poll và gọi h tuần tự cho từng record. Offset chỉ được commit sau khi h trả nil.
// Khi h lỗi, record đó được thử lại (giãn cách theo Backoff) tới khi thành công hoặc ctx bị huỷ;
// record chưa commit sẽ được giao lại ở lần chạy sau. Trả nil khi ctx bị huỷ.
func (c *Consumer) Run(ctx context.Context, h Handler) error {
	for {
		fetches := c.cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if fetches.IsClientClosed() {
			return errors.New("kafka client closed")
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			c.log.Warn("kafka fetch error", "topic", topic, "partition", partition, "err", err)
		})
		iter := fetches.RecordIter()
		for !iter.Done() {
			rec := iter.Next()
			if err := c.handle(ctx, h, rec); err != nil {
				c.cl.AllowRebalance()
				return nil // ctx bị huỷ: không commit record này
			}
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := c.cl.CommitRecords(cctx, rec); err != nil {
				// Record sẽ được xử lý lại sau restart; handler phải idempotent.
				c.log.Error("commit offset failed", "topic", rec.Topic, "partition", rec.Partition,
					"offset", rec.Offset, "err", err)
			}
			cancel()
		}
		c.cl.AllowRebalance()
	}
}

func (c *Consumer) handle(ctx context.Context, h Handler, rec *kgo.Record) error {
	for attempt := 1; ; attempt++ {
		err := h(ctx, rec)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := c.Backoff(attempt)
		c.log.Error("handler failed, retrying record", "topic", rec.Topic, "partition", rec.Partition,
			"offset", rec.Offset, "video_id", string(rec.Key), "attempt", attempt, "retry_in", wait.String(), "err", err)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Close cho phép rebalance đang chờ rồi rời group.
func (c *Consumer) Close() { c.cl.CloseAllowingRebalance() }
```

- [ ] **Step 10: Chạy test**

Run: `go test ./internal/events/ -v`
Expected: tất cả PASS, gồm `TestConsumerCommitsOnlyAfterSuccess` (mất khoảng 10–30 giây do khởi động Redpanda và join group).

- [ ] **Step 11: Commit**

```bash
git add go.mod go.sum internal/events internal/testutil/containers.go
git commit -m "feat(events): event schemas and franz-go producer/consumer with commit-after-success"
```

---
### Task 5: Storage — presigned multipart upload

**Files:**
- Modify: `internal/storage/s3.go`, `internal/testutil/containers.go`
- Create: `internal/storage/multipart.go`, `internal/storage/multipart_test.go`

**Interfaces:**
- Consumes: `storage.Store`, `storage.New`, `storage.Config` (M1).
- Produces:
  ```go
  type Config struct { Endpoint, Region, AccessKey, SecretKey string; PublicEndpoint string } // PublicEndpoint rỗng = Endpoint
  var ErrIncompleteUpload error
  func (s *Store) CreateMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error)
  func (s *Store) PresignUploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int32, ttl time.Duration) (string, error)
  func (s *Store) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int) error
  func (s *Store) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error // NoSuchUpload → nil
  func (s *Store) Exists(ctx context.Context, bucket, key string) (bool, error)
  func (s *Store) DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
  // testutil
  type MinIO struct{ Endpoint, User, Password string }
  func StartMinIO(t *testing.T) MinIO
  ```

- [ ] **Step 1: Thêm `StartMinIO` vào `internal/testutil/containers.go`**

Thêm `tcminio "github.com/testcontainers/testcontainers-go/modules/minio"` vào khối import (ngay trước dòng `tcpostgres …`, theo thứ tự gofmt), rồi thêm vào cuối file:

```go
// MinIOImage: minio/minio trên Docker Hub không pull được, dùng Chainguard ghim digest (ADR 0003).
const MinIOImage = "cgr.dev/chainguard/minio:latest-dev@sha256:5735e8125c4af7dd310dc5780508bfb850f0b68783a1a56089b5c64316a8f603"

type MinIO struct {
	Endpoint, User, Password string
}

// StartMinIO chạy MinIO trong container. Skip khi -short hoặc không có Docker.
func StartMinIO(t *testing.T) MinIO {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := tcminio.Run(ctx, MinIOImage)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return MinIO{Endpoint: "http://" + addr, User: ctr.Username, Password: ctr.Password}
}
```

- [ ] **Step 2: Viết test thất bại `internal/storage/multipart_test.go`**

```go
package storage

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"streamfilm/internal/testutil"
)

func TestPresignUsesPublicEndpoint(t *testing.T) {
	s := New(Config{Endpoint: "http://minio:9000", PublicEndpoint: "http://localhost:9000",
		Region: "us-east-1", AccessKey: "k", SecretKey: "s"})
	u, err := s.PresignUploadPart(context.Background(), "raw", "vid/source.mp4", "up1", 2, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "http://localhost:9000/raw/vid/source.mp4?") {
		t.Errorf("url = %s", u)
	}
	for _, want := range []string{"partNumber=2", "uploadId=up1", "X-Amz-Expires=3600"} {
		if !strings.Contains(u, want) {
			t.Errorf("url thiếu %q: %s", want, u)
		}
	}
}

func putPart(t *testing.T, url string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT part: HTTP %d", resp.StatusCode)
	}
}

func TestMultipartUpload(t *testing.T) {
	m := testutil.StartMinIO(t)
	s := New(Config{Endpoint: m.Endpoint, Region: "us-east-1", AccessKey: m.User, SecretKey: m.Password})
	ctx := context.Background()
	if err := s.EnsureBucket(ctx, "raw"); err != nil {
		t.Fatal(err)
	}
	key := "vid1/source.mp4"
	uploadID, err := s.CreateMultipartUpload(ctx, "raw", key, "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	part1 := bytes.Repeat([]byte("a"), 5<<20) // mọi part trừ part cuối phải >= 5 MiB
	part2 := []byte("tail")
	for i, body := range [][]byte{part1, part2} {
		u, err := s.PresignUploadPart(ctx, "raw", key, uploadID, int32(i+1), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		putPart(t, u, body)
	}

	if ok, err := s.Exists(ctx, "raw", key); err != nil || ok {
		t.Fatalf("before complete: exists=%v err=%v", ok, err)
	}
	if err := s.CompleteMultipartUpload(ctx, "raw", key, uploadID, 3); !errors.Is(err, ErrIncompleteUpload) {
		t.Fatalf("want ErrIncompleteUpload, got %v", err)
	}
	if err := s.CompleteMultipartUpload(ctx, "raw", key, uploadID, 2); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Exists(ctx, "raw", key); err != nil || !ok {
		t.Fatalf("after complete: exists=%v err=%v", ok, err)
	}
	dest := filepath.Join(t.TempDir(), "out.mp4")
	if err := s.Download(ctx, "raw", key, dest); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dest); fi.Size() != int64(len(part1)+len(part2)) {
		t.Errorf("size = %d", fi.Size())
	}

	other, err := s.CreateMultipartUpload(ctx, "raw", "vid2/source.mp4", "video/mp4")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipartUpload(ctx, "raw", "vid2/source.mp4", other); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipartUpload(ctx, "raw", "vid2/source.mp4", other); err != nil {
		t.Errorf("abort twice must be nil (NoSuchUpload), got %v", err)
	}

	n, err := s.DeletePrefix(ctx, "raw", "vid1/")
	if err != nil || n != 1 {
		t.Fatalf("delete prefix: n=%d err=%v", n, err)
	}
	if ok, _ := s.Exists(ctx, "raw", key); ok {
		t.Error("object still exists after DeletePrefix")
	}
}
```

- [ ] **Step 3: Chạy test để thấy lỗi**

Run: `go test ./internal/storage/`
Expected: FAIL, `unknown field PublicEndpoint` / `s.PresignUploadPart undefined`.

- [ ] **Step 4: Sửa `internal/storage/s3.go`** — thay `Config`, `Store`, `New` (giữ nguyên các hàm còn lại):

```go
type Config struct {
	Endpoint, Region, AccessKey, SecretKey string
	// PublicEndpoint là địa chỉ client bên ngoài dùng với presigned URL (chữ ký gắn với Host).
	// Rỗng thì dùng Endpoint.
	PublicEndpoint string
}

type Store struct {
	client  *s3.Client
	presign *s3.PresignClient
}

func newClient(endpoint string, cfg Config) *s3.Client {
	return s3.New(s3.Options{
		Region:                     cfg.Region,
		BaseEndpoint:               aws.String(endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func New(cfg Config) *Store {
	public := cfg.PublicEndpoint
	if public == "" {
		public = cfg.Endpoint
	}
	// Presign chạy offline (không gọi mạng), nên client presign trỏ vào địa chỉ public là đủ.
	return &Store{client: newClient(cfg.Endpoint, cfg), presign: s3.NewPresignClient(newClient(public, cfg))}
}
```

- [ ] **Step 5: Viết `internal/storage/multipart.go`**

```go
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrIncompleteUpload = errors.New("multipart upload incomplete")

func (s *Store) CreateMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error) {
	out, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("create multipart %s: %w", key, err)
	}
	return aws.ToString(out.UploadId), nil
}

// PresignUploadPart trả URL để client PUT thẳng một part lên storage, hết hạn sau ttl.
func (s *Store) PresignUploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int32, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		UploadId: aws.String(uploadID), PartNumber: aws.Int32(partNumber),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign part %d of %s: %w", partNumber, key, err)
	}
	return req.URL, nil
}

// CompleteMultipartUpload tự liệt kê các part đã upload (lấy ETag từ server) rồi ghép object.
// Trả ErrIncompleteUpload nếu không đủ đúng các part 1..wantParts.
func (s *Store) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int) error {
	var parts []types.CompletedPart
	var marker *string
	for {
		out, err := s.client.ListParts(ctx, &s3.ListPartsInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
			PartNumberMarker: marker,
		})
		if err != nil {
			return fmt.Errorf("list parts %s: %w", key, err)
		}
		for _, p := range out.Parts {
			parts = append(parts, types.CompletedPart{ETag: p.ETag, PartNumber: p.PartNumber})
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		marker = out.NextPartNumberMarker
	}
	if len(parts) != wantParts {
		return fmt.Errorf("%w: got %d parts, want %d", ErrIncompleteUpload, len(parts), wantParts)
	}
	for i, p := range parts {
		if aws.ToInt32(p.PartNumber) != int32(i+1) {
			return fmt.Errorf("%w: missing part %d", ErrIncompleteUpload, i+1)
		}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return fmt.Errorf("complete multipart %s: %w", key, err)
	}
	return nil
}

// AbortMultipartUpload huỷ upload dở; upload không còn tồn tại thì coi như xong.
func (s *Store) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	var nsu *types.NoSuchUpload
	if err != nil && !errors.As(err, &nsu) {
		return fmt.Errorf("abort multipart %s: %w", key, err)
	}
	return nil
}

func (s *Store) Exists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return true, nil
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false, nil
	}
	return false, fmt.Errorf("head %s: %w", key, err)
}

// DeletePrefix xoá mọi object dưới prefix (từng object một; số object ở raw/{id}/ rất ít).
func (s *Store) DeletePrefix(ctx context.Context, bucket, prefix string) (int, error) {
	n := 0
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return n, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: obj.Key}); err != nil {
				return n, fmt.Errorf("delete %s: %w", aws.ToString(obj.Key), err)
			}
			n++
		}
	}
	return n, nil
}
```

- [ ] **Step 6: Chạy test**

Run: `go test ./internal/storage/ -v`
Expected: `TestContentTypeFor`, `TestUploadDirAndDownload`, `TestPresignUsesPublicEndpoint`, `TestMultipartUpload` đều PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/storage internal/testutil/containers.go
git commit -m "feat(storage): presigned multipart upload, complete via ListParts, abort and cleanup helpers"
```

---

### Task 6: Upload service — create, complete, retry

**Files:**
- Create: `internal/upload/service.go`, `internal/upload/service_test.go`

**Interfaces:**
- Consumes: `catalog.Video`, `catalog.Status*`, `catalog.CanTransition`, `catalog.ErrNotFound`, `catalog.ErrInvalidTransition` (Task 3); `events.Event`, `events.VideoUploaded`, `events.TopicVideoUploaded` (Task 4); `storage.ErrIncompleteUpload` (Task 5). `*catalog.Repo` thoả `Videos`, `*storage.Store` thoả `Objects`, `*events.Producer` thoả `Publisher`.
- Produces:
  ```go
  const MaxSizeBytes int64 = 10 << 30
  const PartSize int64 = 64 << 20
  const URLTTL = time.Hour
  var ErrInvalidInput, ErrKafkaUnavailable error
  type Videos interface {
  	Create(ctx context.Context, v catalog.Video) error
  	Get(ctx context.Context, id string) (catalog.Video, error)
  	Transition(ctx context.Context, id string, to catalog.Status) (catalog.Status, error)
  	RevertQueued(ctx context.Context, id string, to catalog.Status) error
  }
  type Objects interface {
  	CreateMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error)
  	PresignUploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int32, ttl time.Duration) (string, error)
  	CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int) error
  	Exists(ctx context.Context, bucket, key string) (bool, error)
  }
  type Publisher interface { Publish(ctx context.Context, topic string, ev events.Event) error }
  type Service struct { Videos Videos; Objects Objects; Pub Publisher; BucketRaw string; Now func() time.Time; NewID func() string }
  type CreateInput struct { Title, Description string; Genres []string; Year int; Filename string; SizeBytes int64 }
  type Part struct { PartNumber int32; URL string }
  type UploadPlan struct { UploadID string; PartSize int64; Parts []Part; ExpiresAt time.Time }
  type CreateOutput struct { ID string; Status catalog.Status; Upload UploadPlan }
  func PartCount(size int64) int
  func RawKey(id, ext string) string
  func (s *Service) Create(ctx context.Context, in CreateInput) (CreateOutput, error)
  func (s *Service) Complete(ctx context.Context, id string) (catalog.Video, error)
  func (s *Service) Retry(ctx context.Context, id string) (catalog.Video, error)
  ```

- [ ] **Step 1: Viết test thất bại `internal/upload/service_test.go`** (các fake ở đây được dùng lại trong `handler_test.go` và `cleanup_test.go` cùng package)

```go
package upload

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
	"streamfilm/internal/storage"
)

type fakeVideos struct {
	mu sync.Mutex
	m  map[string]catalog.Video
}

func newFakeVideos() *fakeVideos { return &fakeVideos{m: map[string]catalog.Video{}} }

func (f *fakeVideos) Create(_ context.Context, v catalog.Video) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	v.Status = catalog.StatusUploading
	f.m[v.ID] = v
	return nil
}

func (f *fakeVideos) Get(_ context.Context, id string) (catalog.Video, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[id]
	if !ok {
		return catalog.Video{}, catalog.ErrNotFound
	}
	return v, nil
}

func (f *fakeVideos) Transition(_ context.Context, id string, to catalog.Status) (catalog.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[id]
	if !ok {
		return "", catalog.ErrNotFound
	}
	from := v.Status
	if !catalog.CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", catalog.ErrInvalidTransition, from, to)
	}
	v.Status = to
	f.m[id] = v
	return from, nil
}

func (f *fakeVideos) RevertQueued(_ context.Context, id string, to catalog.Status) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[id]; ok && v.Status == catalog.StatusQueued {
		v.Status = to
		f.m[id] = v
	}
	return nil
}

func (f *fakeVideos) setStatus(id string, s catalog.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.m[id]
	v.Status = s
	f.m[id] = v
}

func (f *fakeVideos) status(id string) catalog.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[id].Status
}

type fakeObjects struct {
	parts         int // số part client đã upload
	exists        bool
	completeCalls int
	created       []string
}

func (f *fakeObjects) CreateMultipartUpload(_ context.Context, bucket, key, contentType string) (string, error) {
	f.created = append(f.created, bucket+"/"+key+"|"+contentType)
	return "up-1", nil
}

func (f *fakeObjects) PresignUploadPart(_ context.Context, bucket, key, uploadID string, n int32, ttl time.Duration) (string, error) {
	return fmt.Sprintf("http://s3.test/%s/%s?uploadId=%s&partNumber=%d&ttl=%s", bucket, key, uploadID, n, ttl), nil
}

func (f *fakeObjects) CompleteMultipartUpload(_ context.Context, bucket, key, uploadID string, want int) error {
	f.completeCalls++
	if f.parts != want {
		return fmt.Errorf("%w: got %d parts, want %d", storage.ErrIncompleteUpload, f.parts, want)
	}
	f.exists = true
	return nil
}

func (f *fakeObjects) Exists(context.Context, string, string) (bool, error) { return f.exists, nil }

type published struct {
	topic string
	ev    events.Event
}

type fakePub struct {
	err error
	got []published
}

func (f *fakePub) Publish(_ context.Context, topic string, ev events.Event) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, published{topic, ev})
	return nil
}

var testNow = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

func newService() (*Service, *fakeVideos, *fakeObjects, *fakePub) {
	vids, objs, pub := newFakeVideos(), &fakeObjects{}, &fakePub{}
	n := 0
	svc := &Service{Videos: vids, Objects: objs, Pub: pub, BucketRaw: "raw",
		Now: func() time.Time { return testNow },
		NewID: func() string {
			n++
			return fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
		}}
	return svc, vids, objs, pub
}

const firstID = "00000000-0000-0000-0000-000000000001"

func TestPartCount(t *testing.T) {
	cases := map[int64]int{1: 1, PartSize: 1, PartSize + 1: 2, MaxSizeBytes: 160}
	for size, want := range cases {
		if got := PartCount(size); got != want {
			t.Errorf("PartCount(%d) = %d, want %d", size, got, want)
		}
	}
}

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
			t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
		}
	}
}

func TestCreateReturnsPresignedParts(t *testing.T) {
	svc, vids, objs, _ := newService()
	out, err := svc.Create(context.Background(), CreateInput{Title: " Phim A ", Filename: "Movie.MP4",
		Genres: []string{"drama"}, Year: 2024, SizeBytes: 2*PartSize + 1})
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != firstID || out.Status != catalog.StatusUploading {
		t.Errorf("out = %+v", out)
	}
	up := out.Upload
	if up.UploadID != "up-1" || up.PartSize != PartSize || len(up.Parts) != 3 ||
		up.Parts[2].PartNumber != 3 || !up.ExpiresAt.Equal(testNow.Add(time.Hour)) {
		t.Errorf("upload plan = %+v", up)
	}
	if !strings.Contains(up.Parts[0].URL, "raw/"+firstID+"/source.mp4") || !strings.Contains(up.Parts[0].URL, "ttl=1h0m0s") {
		t.Errorf("part url = %s", up.Parts[0].URL)
	}
	if len(objs.created) != 1 || objs.created[0] != "raw/"+firstID+"/source.mp4|video/mp4" {
		t.Errorf("created = %v", objs.created)
	}
	v := vids.m[firstID]
	if v.Title != "Phim A" || v.RawKey != firstID+"/source.mp4" || v.UploadID != "up-1" ||
		v.SizeBytes != 2*PartSize+1 || v.Year != 2024 {
		t.Errorf("stored video = %+v", v)
	}
}

func createOne(t *testing.T, svc *Service) string {
	t.Helper()
	out, err := svc.Create(context.Background(), CreateInput{Title: "A", Filename: "a.mp4", SizeBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	return out.ID
}

func TestCompleteQueuesAndPublishes(t *testing.T) {
	svc, vids, objs, pub := newService()
	id := createOne(t, svc)
	objs.parts = 1
	v, err := svc.Complete(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != catalog.StatusQueued || vids.status(id) != catalog.StatusQueued {
		t.Errorf("status = %s / %s", v.Status, vids.status(id))
	}
	if len(pub.got) != 1 || pub.got[0].topic != events.TopicVideoUploaded {
		t.Fatalf("published = %+v", pub.got)
	}
	if ev := pub.got[0].ev.(events.VideoUploaded); ev.VideoID != id || ev.RawKey != id+"/source.mp4" {
		t.Errorf("event = %+v", ev)
	}
}

func TestCompleteIncompleteUpload(t *testing.T) {
	svc, vids, _, pub := newService()
	id := createOne(t, svc)
	if _, err := svc.Complete(context.Background(), id); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
	if vids.status(id) != catalog.StatusUploading || len(pub.got) != 0 {
		t.Errorf("status = %s, published = %d", vids.status(id), len(pub.got))
	}
}

func TestCompleteKafkaDownThenRetry(t *testing.T) {
	svc, vids, objs, pub := newService()
	id := createOne(t, svc)
	objs.parts = 1
	pub.err = errors.New("broker down")
	if _, err := svc.Complete(context.Background(), id); !errors.Is(err, ErrKafkaUnavailable) {
		t.Fatalf("err = %v, want ErrKafkaUnavailable", err)
	}
	if vids.status(id) != catalog.StatusUploading {
		t.Errorf("status = %s, want uploading after failed publish", vids.status(id))
	}
	pub.err = nil
	if _, err := svc.Complete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if objs.completeCalls != 1 {
		t.Errorf("CompleteMultipartUpload called %d times, want 1 (object đã tồn tại)", objs.completeCalls)
	}
	if vids.status(id) != catalog.StatusQueued || len(pub.got) != 1 {
		t.Errorf("status = %s, published = %d", vids.status(id), len(pub.got))
	}
}

func TestCompleteWrongState(t *testing.T) {
	svc, _, objs, _ := newService()
	id := createOne(t, svc)
	objs.parts = 1
	if _, err := svc.Complete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Complete(context.Background(), id); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Errorf("second complete: err = %v", err)
	}
	if _, err := svc.Complete(context.Background(), "00000000-0000-0000-0000-000000000099"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
}

func TestRetry(t *testing.T) {
	svc, vids, _, pub := newService()
	id := createOne(t, svc)
	if _, err := svc.Retry(context.Background(), id); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Errorf("retry uploading: err = %v", err)
	}
	vids.setStatus(id, catalog.StatusFailed)

	pub.err = errors.New("broker down")
	if _, err := svc.Retry(context.Background(), id); !errors.Is(err, ErrKafkaUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if vids.status(id) != catalog.StatusFailed {
		t.Errorf("status = %s, want failed", vids.status(id))
	}

	pub.err = nil
	v, err := svc.Retry(context.Background(), id)
	if err != nil || v.Status != catalog.StatusQueued {
		t.Fatalf("v = %+v, err = %v", v, err)
	}
	if len(pub.got) != 1 || pub.got[0].topic != events.TopicVideoUploaded {
		t.Errorf("published = %+v", pub.got)
	}
}
```

- [ ] **Step 2: Chạy test để thấy lỗi**

Run: `go test ./internal/upload/`
Expected: FAIL, `undefined: Service`.

- [ ] **Step 3: Viết `internal/upload/service.go`**

```go
// Package upload cấp presigned URL upload thẳng lên storage, hoàn tất upload và đưa video vào hàng đợi transcode.
package upload

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"streamfilm/internal/catalog"
	"streamfilm/internal/events"
	"streamfilm/internal/storage"
)

const (
	MaxSizeBytes   int64 = 10 << 30 // 10 GB (spec §8)
	PartSize       int64 = 64 << 20 // 64 MiB → tối đa 160 part cho 10 GB (S3 cho phép 10 000)
	URLTTL               = time.Hour
	publishTimeout       = 5 * time.Second
)

var (
	ErrInvalidInput     = errors.New("invalid input")
	ErrKafkaUnavailable = errors.New("event bus unavailable")
)

var allowedExt = map[string]string{
	".mp4": "video/mp4", ".mov": "video/quicktime", ".mkv": "video/x-matroska", ".webm": "video/webm",
}

type Videos interface {
	Create(ctx context.Context, v catalog.Video) error
	Get(ctx context.Context, id string) (catalog.Video, error)
	Transition(ctx context.Context, id string, to catalog.Status) (catalog.Status, error)
	RevertQueued(ctx context.Context, id string, to catalog.Status) error
}

type Objects interface {
	CreateMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error)
	PresignUploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int32, ttl time.Duration) (string, error)
	CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int) error
	Exists(ctx context.Context, bucket, key string) (bool, error)
}

type Publisher interface {
	Publish(ctx context.Context, topic string, ev events.Event) error
}

type Service struct {
	Videos    Videos
	Objects   Objects
	Pub       Publisher
	BucketRaw string
	Now       func() time.Time
	NewID     func() string
}

type CreateInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Genres      []string `json:"genres"`
	Year        int      `json:"year"`
	Filename    string   `json:"filename"`
	SizeBytes   int64    `json:"size_bytes"`
}

type Part struct {
	PartNumber int32  `json:"part_number"`
	URL        string `json:"url"`
}

type UploadPlan struct {
	UploadID  string    `json:"upload_id"`
	PartSize  int64     `json:"part_size"`
	Parts     []Part    `json:"parts"`
	ExpiresAt time.Time `json:"expires_at"`
}

type CreateOutput struct {
	ID     string         `json:"id"`
	Status catalog.Status `json:"status"`
	Upload UploadPlan     `json:"upload"`
}

func PartCount(size int64) int { return int((size + PartSize - 1) / PartSize) }

// RawKey là key trong bucket raw: {id}/source.{ext}.
func RawKey(id, ext string) string { return id + "/source" + ext }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}

// Create tạo video (uploading), mở multipart upload và trả presigned URL cho từng part.
func (s *Service) Create(ctx context.Context, in CreateInput) (CreateOutput, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return CreateOutput{}, invalid("title is required")
	}
	if in.SizeBytes <= 0 || in.SizeBytes > MaxSizeBytes {
		return CreateOutput{}, invalid("size_bytes must be between 1 and %d", MaxSizeBytes)
	}
	ext := strings.ToLower(path.Ext(in.Filename))
	contentType, ok := allowedExt[ext]
	if !ok {
		return CreateOutput{}, invalid("filename must end with .mp4, .mov, .mkv or .webm")
	}

	id := s.NewID()
	key := RawKey(id, ext)
	uploadID, err := s.Objects.CreateMultipartUpload(ctx, s.BucketRaw, key, contentType)
	if err != nil {
		return CreateOutput{}, err
	}
	n := PartCount(in.SizeBytes)
	parts := make([]Part, n)
	for i := range n {
		num := int32(i + 1)
		url, err := s.Objects.PresignUploadPart(ctx, s.BucketRaw, key, uploadID, num, URLTTL)
		if err != nil {
			return CreateOutput{}, err
		}
		parts[i] = Part{PartNumber: num, URL: url}
	}
	err = s.Videos.Create(ctx, catalog.Video{ID: id, Title: in.Title, Description: in.Description,
		Genres: in.Genres, Year: in.Year, RawKey: key, UploadID: uploadID, SizeBytes: in.SizeBytes})
	if err != nil {
		return CreateOutput{}, err
	}
	return CreateOutput{ID: id, Status: catalog.StatusUploading, Upload: UploadPlan{
		UploadID: uploadID, PartSize: PartSize, Parts: parts, ExpiresAt: s.Now().Add(URLTTL),
	}}, nil
}

// Complete ghép multipart (nếu chưa), đặt queued rồi publish video.uploaded.
// Publish lỗi thì trả video về uploading và báo ErrKafkaUnavailable (HTTP 503) để admin gọi lại.
func (s *Service) Complete(ctx context.Context, id string) (catalog.Video, error) {
	v, err := s.Videos.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if v.Status != catalog.StatusUploading {
		return v, fmt.Errorf("%w: video is %s", catalog.ErrInvalidTransition, v.Status)
	}
	exists, err := s.Objects.Exists(ctx, s.BucketRaw, v.RawKey)
	if err != nil {
		return v, err
	}
	if !exists { // lần gọi trước có thể đã ghép xong nhưng publish lỗi
		err := s.Objects.CompleteMultipartUpload(ctx, s.BucketRaw, v.RawKey, v.UploadID, PartCount(v.SizeBytes))
		if errors.Is(err, storage.ErrIncompleteUpload) {
			return v, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		if err != nil {
			return v, err
		}
	}
	return s.enqueue(ctx, v, catalog.StatusUploading)
}

// Retry đưa video failed về queued và publish lại video.uploaded.
func (s *Service) Retry(ctx context.Context, id string) (catalog.Video, error) {
	v, err := s.Videos.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if v.Status != catalog.StatusFailed {
		return v, fmt.Errorf("%w: only failed videos can be retried, video is %s", catalog.ErrInvalidTransition, v.Status)
	}
	return s.enqueue(ctx, v, catalog.StatusFailed)
}

// enqueue: đặt queued TRƯỚC rồi mới publish, để api không bao giờ nhận video.processing
// khi DB còn ở trạng thái cũ. Đây là dual-write chấp nhận ở MVP (ADR 0007).
func (s *Service) enqueue(ctx context.Context, v catalog.Video, prev catalog.Status) (catalog.Video, error) {
	if _, err := s.Videos.Transition(ctx, v.ID, catalog.StatusQueued); err != nil {
		return v, err
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	ev := events.VideoUploaded{VideoID: v.ID, RawKey: v.RawKey}
	if err := s.Pub.Publish(pctx, events.TopicVideoUploaded, ev); err != nil {
		if rerr := s.Videos.RevertQueued(context.WithoutCancel(ctx), v.ID, prev); rerr != nil {
			return v, errors.Join(fmt.Errorf("%w: %v", ErrKafkaUnavailable, err), rerr)
		}
		return v, fmt.Errorf("%w: %v", ErrKafkaUnavailable, err)
	}
	v.Status = catalog.StatusQueued
	return v, nil
}
```

- [ ] **Step 4: Chạy test**

Run: `go test ./internal/upload/ -v`
Expected: `TestPartCount`, `TestCreateValidates`, `TestCreateReturnsPresignedParts`, `TestCompleteQueuesAndPublishes`, `TestCompleteIncompleteUpload`, `TestCompleteKafkaDownThenRetry`, `TestCompleteWrongState`, `TestRetry` đều PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/upload
git commit -m "feat(upload): presigned multipart create, complete and retry service"
```

---

### Task 7: HTTP admin API (chưa auth)

**Files:**
- Create: `internal/httpx/httpx.go`, `internal/upload/handler.go`, `internal/upload/handler_test.go`, `internal/catalog/handler.go`, `internal/catalog/handler_test.go`

**Interfaces:**
- Consumes: `upload.Service`, `upload.ErrInvalidInput`, `upload.ErrKafkaUnavailable`, fake trong `service_test.go` (Task 6); `catalog.ListFilter`, `catalog.Video`, `catalog.Rendition`, `catalog.Status.Valid` (Task 3).
- Produces:
  ```go
  // package httpx
  func WriteJSON(w http.ResponseWriter, status int, v any)
  func WriteError(w http.ResponseWriter, status int, msg string) // {"error": msg}
  func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error // tối đa 1 MiB, cấm field lạ
  func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler
  // package upload
  type Handler struct{ Svc *Service; Log *slog.Logger }
  func (h *Handler) Routes(r chi.Router) // POST /api/admin/videos, /{id}/complete, /{id}/retry
  // package catalog
  type AdminHandler struct{ Videos AdminStore; Log *slog.Logger }
  type AdminStore interface {
  	List(ctx context.Context, f ListFilter) ([]Video, error)
  	Get(ctx context.Context, id string) (Video, error)
  	Renditions(ctx context.Context, id string) ([]Rendition, error)
  }
  type VideoJSON struct{ ... } // xem code
  func (h *AdminHandler) Routes(r chi.Router) // GET /api/admin/videos, /api/admin/videos/{id}
  ```

- [ ] **Step 1: Thêm dependency**

Run: `go get github.com/go-chi/chi/v5@v5.5.0 github.com/google/uuid@v1.6.0`
Expected: `go.mod` có `github.com/go-chi/chi/v5 v5.5.0` và `github.com/google/uuid v1.6.0`.

- [ ] **Step 2: Viết `internal/httpx/httpx.go`**

```go
// Package httpx chứa helper HTTP dùng chung cho các module của api.
package httpx

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// DecodeJSON đọc body JSON tối đa 1 MiB và từ chối field không biết.
func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

// RequestLogger log mỗi request kèm request_id (từ middleware.RequestID); bỏ qua /healthz.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("http request", "request_id", middleware.GetReqID(r.Context()),
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds())
		})
	}
}
```

- [ ] **Step 3: Viết test thất bại `internal/upload/handler_test.go`**

```go
package upload

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"streamfilm/internal/catalog"
)

func newRouter(svc *Service) http.Handler {
	r := chi.NewRouter()
	(&Handler{Svc: svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateEndpoint(t *testing.T) {
	svc, _, _, _ := newService()
	h := newRouter(svc)
	rec := do(h, http.MethodPost, "/api/admin/videos", `{"title":"A","filename":"a.mp4","size_bytes":100}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	var out CreateOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != firstID || out.Status != catalog.StatusUploading || len(out.Upload.Parts) != 1 {
		t.Errorf("out = %+v", out)
	}
	for _, body := range []string{`{"title":`, `{"title":"A","nope":1}`, `{"title":"A","filename":"a.avi","size_bytes":1}`} {
		if rec := do(h, http.MethodPost, "/api/admin/videos", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: code = %d", body, rec.Code)
		}
	}
}

func TestCompleteEndpoint(t *testing.T) {
	svc, _, objs, pub := newService()
	h := newRouter(svc)
	id := createOne(t, svc)
	objs.parts = 1

	if rec := do(h, http.MethodPost, "/api/admin/videos/not-a-uuid/complete", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/api/admin/videos/00000000-0000-0000-0000-000000000099/complete", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: code = %d", rec.Code)
	}

	pub.err = errors.New("broker down")
	rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/complete", "")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "event bus") {
		t.Errorf("kafka down: code = %d body = %s", rec.Code, rec.Body)
	}

	pub.err = nil
	rec = do(h, http.MethodPost, "/api/admin/videos/"+id+"/complete", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Errorf("ok: code = %d body = %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/complete", ""); rec.Code != http.StatusConflict {
		t.Errorf("twice: code = %d", rec.Code)
	}
}

func TestRetryEndpoint(t *testing.T) {
	svc, vids, _, _ := newService()
	h := newRouter(svc)
	id := createOne(t, svc)
	if rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/retry", ""); rec.Code != http.StatusConflict {
		t.Errorf("retry uploading: code = %d", rec.Code)
	}
	vids.setStatus(id, catalog.StatusFailed)
	rec := do(h, http.MethodPost, "/api/admin/videos/"+id+"/retry", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"queued"`) {
		t.Errorf("retry failed: code = %d body = %s", rec.Code, rec.Body)
	}
}
```

- [ ] **Step 4: Viết test thất bại `internal/catalog/handler_test.go`**

```go
package catalog

import (
	"context"
	"encoding/json"
	"io"
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
	f.filter = lf
	return f.videos, nil
}

func (f *fakeAdminStore) Get(_ context.Context, id string) (Video, error) {
	for _, v := range f.videos {
		if v.ID == id {
			return v, nil
		}
	}
	return Video{}, ErrNotFound
}

func (f *fakeAdminStore) Renditions(context.Context, string) ([]Rendition, error) {
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

func newAdminRouter(store AdminStore) http.Handler {
	r := chi.NewRouter()
	(&AdminHandler{Videos: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestAdminList(t *testing.T) {
	store := &fakeAdminStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusFailed, FailReason: "ffmpeg: boom"},
	}}
	h := newAdminRouter(store)
	rec := get(h, "/api/admin/videos?status=failed&limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if store.filter.Status != StatusFailed || store.filter.Limit != 10 {
		t.Errorf("filter = %+v", store.filter)
	}
	var body struct {
		Videos []VideoJSON `json:"videos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Videos) != 1 || body.Videos[0].FailReason != "ffmpeg: boom" || body.Videos[0].Genres == nil {
		t.Errorf("videos = %+v", body.Videos)
	}
	for _, q := range []string{"?status=bogus", "?limit=0", "?limit=abc", "?limit=201"} {
		if rec := get(h, "/api/admin/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", q, rec.Code)
		}
	}
}

func TestAdminGet(t *testing.T) {
	h := newAdminRouter(&fakeAdminStore{videos: []Video{{ID: idA, Title: "A", Status: StatusReady, DurationS: 30}}})
	rec := get(h, "/api/admin/videos/"+idA)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var v VideoJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusReady || v.DurationS != 30 || len(v.Renditions) != 1 || v.Renditions[0].Height != 720 {
		t.Errorf("video = %+v", v)
	}
	if rec := get(h, "/api/admin/videos/"+idX); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: code = %d", rec.Code)
	}
	if rec := get(h, "/api/admin/videos/xyz"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: code = %d", rec.Code)
	}
}
```

- [ ] **Step 5: Chạy test để thấy lỗi**

Run: `go test -short ./internal/upload/ ./internal/catalog/`
Expected: FAIL, `undefined: Handler` và `undefined: AdminHandler`.

- [ ] **Step 6: Viết `internal/upload/handler.go`**

```go
package upload

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"streamfilm/internal/catalog"
	"streamfilm/internal/httpx"
)

// Handler gắn các endpoint upload của admin.
// M2: CHƯA có auth. M3 bọc nhóm /api/admin bằng middleware yêu cầu role admin.
type Handler struct {
	Svc *Service
	Log *slog.Logger
}

func (h *Handler) Routes(r chi.Router) {
	r.Post("/api/admin/videos", h.create)
	r.Post("/api/admin/videos/{id}/complete", h.complete)
	r.Post("/api/admin/videos/{id}/retry", h.retry)
}

type statusResponse struct {
	ID     string         `json:"id"`
	Status catalog.Status `json:"status"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.Svc.Create(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) {
	id, ok := videoID(w, r)
	if !ok {
		return
	}
	v, err := h.Svc.Complete(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, statusResponse{ID: v.ID, Status: v.Status})
}

func (h *Handler) retry(w http.ResponseWriter, r *http.Request) {
	id, ok := videoID(w, r)
	if !ok {
		return
	}
	v, err := h.Svc.Retry(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
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
	switch {
	case errors.Is(err, ErrInvalidInput):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, catalog.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "video not found")
	case errors.Is(err, catalog.ErrInvalidTransition):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrKafkaUnavailable):
		h.Log.Warn("publish failed", "request_id", middleware.GetReqID(r.Context()), "err", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "event bus unavailable, try again")
	default:
		h.Log.Error("request failed", "request_id", middleware.GetReqID(r.Context()),
			"path", r.URL.Path, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
	}
}
```

- [ ] **Step 7: Viết `internal/catalog/handler.go`**

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

type AdminStore interface {
	List(ctx context.Context, f ListFilter) ([]Video, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
}

// AdminHandler: endpoint đọc cho admin. M2 chưa có auth; M3 thêm middleware role admin.
type AdminHandler struct {
	Videos AdminStore
	Log    *slog.Logger
}

type VideoJSON struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Genres      []string    `json:"genres"`
	Year        int         `json:"year,omitempty"`
	Status      Status      `json:"status"`
	FailReason  string      `json:"fail_reason,omitempty"`
	DurationS   int         `json:"duration_s,omitempty"`
	SizeBytes   int64       `json:"size_bytes"`
	HLSPrefix   string      `json:"hls_prefix,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Renditions  []Rendition `json:"renditions,omitempty"`
}

func toJSON(v Video) VideoJSON {
	genres := v.Genres
	if genres == nil {
		genres = []string{}
	}
	return VideoJSON{ID: v.ID, Title: v.Title, Description: v.Description, Genres: genres, Year: v.Year,
		Status: v.Status, FailReason: v.FailReason, DurationS: v.DurationS, SizeBytes: v.SizeBytes,
		HLSPrefix: v.HLSPrefix, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}

func (h *AdminHandler) Routes(r chi.Router) {
	r.Get("/api/admin/videos", h.list)
	r.Get("/api/admin/videos/{id}", h.get)
}

func (h *AdminHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := ListFilter{Status: Status(q.Get("status"))}
	if f.Status != "" && !f.Status.Valid() {
		httpx.WriteError(w, http.StatusBadRequest, "unknown status")
		return
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			httpx.WriteError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		f.Limit = n
	}
	vs, err := h.Videos.List(r.Context(), f)
	if err != nil {
		h.Log.Error("list videos", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]VideoJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, toJSON(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"videos": out})
}

func (h *AdminHandler) get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	v, err := h.Videos.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if err != nil {
		h.Log.Error("get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	rs, err := h.Videos.Renditions(r.Context(), id)
	if err != nil {
		h.Log.Error("get renditions", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toJSON(v)
	out.Renditions = rs
	httpx.WriteJSON(w, http.StatusOK, out)
}
```

- [ ] **Step 8: Chạy test**

Run: `go test -short ./internal/upload/ ./internal/catalog/ ./internal/httpx/`
Expected: `ok` cho `upload`, `catalog`; `httpx` báo `[no test files]`.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal/httpx internal/upload internal/catalog
git commit -m "feat(api): admin HTTP handlers for create, complete, retry, list and get"
```

---

### Task 8: Consumer cập nhật trạng thái và job dọn upload bỏ dở

**Files:**
- Create: `internal/catalog/consumer.go`, `internal/catalog/consumer_test.go`, `internal/upload/cleanup.go`, `internal/upload/cleanup_test.go`

**Interfaces:**
- Consumes: `events.Decode`, `events.VideoProcessing/VideoTranscoded/VideoFailed`, `events.NewRecord` (Task 4); `Repo.Transition/MarkReady/MarkFailed/ListStaleUploading/DeleteUploading` (Task 3); `Store.AbortMultipartUpload/DeletePrefix` (Task 5).
- Produces:
  ```go
  // package catalog
  type StatusStore interface {
  	Transition(ctx context.Context, id string, to Status) (Status, error)
  	MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error)
  	MarkFailed(ctx context.Context, id, reason string) (Status, error)
  }
  type StatusUpdater struct{ Store StatusStore; Log *slog.Logger }
  func (u *StatusUpdater) Handle(ctx context.Context, rec *kgo.Record) error // khớp events.Handler
  // package upload
  type StaleVideos interface {
  	ListStaleUploading(ctx context.Context, before time.Time) ([]catalog.Video, error)
  	DeleteUploading(ctx context.Context, id string) error
  }
  type StaleObjects interface {
  	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
  	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
  }
  type Cleaner struct{ Videos StaleVideos; Objects StaleObjects; BucketRaw string; MaxAge time.Duration; Now func() time.Time; Log *slog.Logger }
  func (c *Cleaner) RunOnce(ctx context.Context) (int, error)
  func (c *Cleaner) Run(ctx context.Context, every time.Duration)
  ```

- [ ] **Step 1: Viết test thất bại `internal/catalog/consumer_test.go`**

```go
package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

type storeCall struct {
	method, id string
	arg        any
}

type fakeStatusStore struct {
	calls []storeCall
	err   error
}

func (f *fakeStatusStore) Transition(_ context.Context, id string, to Status) (Status, error) {
	f.calls = append(f.calls, storeCall{"Transition", id, to})
	return "", f.err
}

func (f *fakeStatusStore) MarkReady(_ context.Context, id string, info ReadyInfo) (Status, error) {
	f.calls = append(f.calls, storeCall{"MarkReady", id, info})
	return "", f.err
}

func (f *fakeStatusStore) MarkFailed(_ context.Context, id, reason string) (Status, error) {
	f.calls = append(f.calls, storeCall{"MarkFailed", id, reason})
	return "", f.err
}

func record(t *testing.T, topic string, ev events.Event) *kgo.Record {
	t.Helper()
	rec, err := events.NewRecord(topic, ev)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func newUpdater(err error) (*StatusUpdater, *fakeStatusStore) {
	store := &fakeStatusStore{err: err}
	return &StatusUpdater{Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, store
}

func TestStatusUpdaterDispatch(t *testing.T) {
	u, store := newUpdater(nil)
	ctx := context.Background()
	recs := []*kgo.Record{
		record(t, events.TopicVideoProcessing, events.VideoProcessing{VideoID: idA}),
		record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA, DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []events.Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}}),
		record(t, events.TopicVideoFailed, events.VideoFailed{VideoID: idB, Reason: "ffmpeg: boom"}),
	}
	for _, rec := range recs {
		if err := u.Handle(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	want := []storeCall{
		{"Transition", idA, StatusProcessing},
		{"MarkReady", idA, ReadyInfo{DurationS: 30, HLSPrefix: idA + "/",
			Renditions: []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}}},
		{"MarkFailed", idB, "ffmpeg: boom"},
	}
	if !reflect.DeepEqual(store.calls, want) {
		t.Errorf("calls:\n got %+v\nwant %+v", store.calls, want)
	}
}

func TestStatusUpdaterSkipsIgnorableErrors(t *testing.T) {
	ctx := context.Background()
	for _, err := range []error{fmt.Errorf("%w: ready -> ready", ErrInvalidTransition), ErrNotFound} {
		u, _ := newUpdater(err)
		if got := u.Handle(ctx, record(t, events.TopicVideoTranscoded, events.VideoTranscoded{VideoID: idA})); got != nil {
			t.Errorf("err %v must be skipped, got %v", err, got)
		}
	}
	u, store := newUpdater(nil)
	if err := u.Handle(ctx, &kgo.Record{Topic: events.TopicVideoFailed, Value: []byte("not json")}); err != nil {
		t.Errorf("bad JSON must be skipped, got %v", err)
	}
	if len(store.calls) != 0 {
		t.Errorf("bad JSON must not touch store: %+v", store.calls)
	}
}

func TestStatusUpdaterReturnsDBErrors(t *testing.T) {
	dbErr := errors.New("connection refused")
	u, _ := newUpdater(dbErr)
	err := u.Handle(context.Background(), record(t, events.TopicVideoProcessing, events.VideoProcessing{VideoID: idA}))
	if !errors.Is(err, dbErr) {
		t.Errorf("err = %v, want DB error so consumer retries", err)
	}
}
```

- [ ] **Step 2: Viết test thất bại `internal/upload/cleanup_test.go`**

```go
package upload

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"streamfilm/internal/catalog"
)

type fakeStale struct {
	videos  []catalog.Video
	before  time.Time
	gone    map[string]bool
	deleted []string
}

func (f *fakeStale) ListStaleUploading(_ context.Context, before time.Time) ([]catalog.Video, error) {
	f.before = before
	return f.videos, nil
}

func (f *fakeStale) DeleteUploading(_ context.Context, id string) error {
	if f.gone[id] {
		return catalog.ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	return nil
}

type fakeStaleObjects struct {
	aborted, prefixes []string
}

func (f *fakeStaleObjects) AbortMultipartUpload(_ context.Context, bucket, key, uploadID string) error {
	f.aborted = append(f.aborted, bucket+"/"+key+"#"+uploadID)
	return nil
}

func (f *fakeStaleObjects) DeletePrefix(_ context.Context, bucket, prefix string) (int, error) {
	f.prefixes = append(f.prefixes, bucket+"/"+prefix)
	return 1, nil
}

func TestCleanerRunOnce(t *testing.T) {
	vids := &fakeStale{
		videos: []catalog.Video{
			{ID: "v1", RawKey: "v1/source.mp4", UploadID: "up-1"},
			{ID: "v2", RawKey: "v2/source.mp4", UploadID: "up-2"}, // vừa được complete: không còn uploading
		},
		gone: map[string]bool{"v2": true},
	}
	objs := &fakeStaleObjects{}
	c := &Cleaner{Videos: vids, Objects: objs, BucketRaw: "raw", MaxAge: 24 * time.Hour,
		Now: func() time.Time { return testNow }, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	n, err := c.RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if !vids.before.Equal(testNow.Add(-24 * time.Hour)) {
		t.Errorf("cutoff = %v", vids.before)
	}
	if !reflect.DeepEqual(vids.deleted, []string{"v1"}) ||
		!reflect.DeepEqual(objs.aborted, []string{"raw/v1/source.mp4#up-1"}) ||
		!reflect.DeepEqual(objs.prefixes, []string{"raw/v1/"}) {
		t.Errorf("deleted=%v aborted=%v prefixes=%v", vids.deleted, objs.aborted, objs.prefixes)
	}
}
```

- [ ] **Step 3: Chạy test để thấy lỗi**

Run: `go test -short ./internal/catalog/ ./internal/upload/`
Expected: FAIL, `undefined: StatusUpdater` và `undefined: Cleaner`.

- [ ] **Step 4: Viết `internal/catalog/consumer.go`**

```go
package catalog

import (
	"context"
	"errors"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

type StatusStore interface {
	Transition(ctx context.Context, id string, to Status) (Status, error)
	MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error)
	MarkFailed(ctx context.Context, id, reason string) (Status, error)
}

// StatusUpdater consume video.processing / video.transcoded / video.failed và cập nhật Postgres.
// Idempotent: sự kiện trùng hoặc đến sau (chuyển trạng thái không hợp lệ, video đã bị xoá) được log và bỏ qua.
// Lỗi DB được trả về để events.Consumer thử lại record tại chỗ.
type StatusUpdater struct {
	Store StatusStore
	Log   *slog.Logger
}

func (u *StatusUpdater) Handle(ctx context.Context, rec *kgo.Record) error {
	var id string
	var err error
	switch rec.Topic {
	case events.TopicVideoProcessing:
		ev, derr := events.Decode[events.VideoProcessing](rec.Value)
		if derr != nil {
			return u.skipBad(rec, derr)
		}
		id = ev.VideoID
		_, err = u.Store.Transition(ctx, id, StatusProcessing)
	case events.TopicVideoTranscoded:
		ev, derr := events.Decode[events.VideoTranscoded](rec.Value)
		if derr != nil {
			return u.skipBad(rec, derr)
		}
		id = ev.VideoID
		info := ReadyInfo{DurationS: ev.DurationS, HLSPrefix: ev.HLSPrefix}
		for _, r := range ev.Renditions {
			info.Renditions = append(info.Renditions, Rendition{Height: r.Height, BitrateKbps: r.BitrateKbps, PlaylistKey: r.PlaylistKey})
		}
		_, err = u.Store.MarkReady(ctx, id, info)
	case events.TopicVideoFailed:
		ev, derr := events.Decode[events.VideoFailed](rec.Value)
		if derr != nil {
			return u.skipBad(rec, derr)
		}
		id = ev.VideoID
		_, err = u.Store.MarkFailed(ctx, id, ev.Reason)
	default:
		u.Log.Warn("unexpected topic", "topic", rec.Topic)
		return nil
	}

	switch {
	case err == nil:
		u.Log.Info("video status updated", "video_id", id, "topic", rec.Topic)
		return nil
	case errors.Is(err, ErrInvalidTransition), errors.Is(err, ErrNotFound):
		u.Log.Info("event ignored", "video_id", id, "topic", rec.Topic, "reason", err.Error())
		return nil
	default:
		return err
	}
}

func (u *StatusUpdater) skipBad(rec *kgo.Record, err error) error {
	u.Log.Warn("bad event skipped", "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
	return nil
}
```

- [ ] **Step 5: Viết `internal/upload/cleanup.go`**

```go
package upload

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"streamfilm/internal/catalog"
)

type StaleVideos interface {
	ListStaleUploading(ctx context.Context, before time.Time) ([]catalog.Video, error)
	DeleteUploading(ctx context.Context, id string) error
}

type StaleObjects interface {
	AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
}

// Cleaner xoá video uploading quá MaxAge (spec: 24h) cùng multipart upload dở và object raw.
type Cleaner struct {
	Videos    StaleVideos
	Objects   StaleObjects
	BucketRaw string
	MaxAge    time.Duration
	Now       func() time.Time
	Log       *slog.Logger
}

// RunOnce xoá dòng DB trước (có điều kiện status = uploading, nên không đụng video vừa được complete),
// rồi mới dọn storage. Lỗi storage chỉ log: object mồ côi không ảnh hưởng tính đúng.
func (c *Cleaner) RunOnce(ctx context.Context) (int, error) {
	stale, err := c.Videos.ListStaleUploading(ctx, c.Now().Add(-c.MaxAge))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range stale {
		if err := c.Videos.DeleteUploading(ctx, v.ID); err != nil {
			if errors.Is(err, catalog.ErrNotFound) {
				continue
			}
			return n, err
		}
		if v.UploadID != "" {
			if err := c.Objects.AbortMultipartUpload(ctx, c.BucketRaw, v.RawKey, v.UploadID); err != nil {
				c.Log.Warn("abort multipart failed", "video_id", v.ID, "err", err)
			}
		}
		if _, err := c.Objects.DeletePrefix(ctx, c.BucketRaw, v.ID+"/"); err != nil {
			c.Log.Warn("delete raw objects failed", "video_id", v.ID, "err", err)
		}
		c.Log.Info("stale upload removed", "video_id", v.ID, "created_at", v.CreatedAt)
		n++
	}
	return n, nil
}

// Run chạy RunOnce ngay khi khởi động rồi lặp lại mỗi `every` cho tới khi ctx bị huỷ.
func (c *Cleaner) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := c.RunOnce(ctx); err != nil {
			c.Log.Error("stale upload cleanup failed", "err", err)
		} else if n > 0 {
			c.Log.Info("stale upload cleanup done", "removed", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 6: Chạy test**

Run: `go test -short ./internal/catalog/ ./internal/upload/ -v`
Expected: `TestStatusUpdaterDispatch`, `TestStatusUpdaterSkipsIgnorableErrors`, `TestStatusUpdaterReturnsDBErrors`, `TestCleanerRunOnce` cùng các test cũ PASS (`TestRepo` SKIP vì `-short`).

- [ ] **Step 7: Commit**

```bash
git add internal/catalog internal/upload
git commit -m "feat(api): idempotent status updater consumer and stale upload cleaner"
```

---
### Task 9: `cmd/api` — lắp ráp và chạy thử trên host

**Files:**
- Create: `cmd/api/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `config.Load` (Task 1), `db.Connect`, `db.Migrate`, `migrations.FS` (Task 2), `catalog.NewRepo`, `catalog.AdminHandler`, `catalog.StatusUpdater` (Task 3, 7, 8), `events.NewProducer`, `events.NewConsumer`, `events.Topic*` (Task 4), `storage.New` với `PublicEndpoint` (Task 5), `upload.Service`, `upload.Handler`, `upload.Cleaner` (Task 6–8), `httpx.RequestLogger` (Task 7).
- Produces: binary `api` lắng nghe `HTTP_ADDR` (mặc định `:8000`): `GET /healthz`, các route admin; consumer group `api`; job dọn upload mỗi `CLEANUP_INTERVAL`. Target Make `build` (2 binary), `run-api`.

- [ ] **Step 1: Viết `cmd/api/main.go`**

```go
// Command api là HTTP API của Streamfilm.
// M2: upload (presigned multipart), danh sách video cho admin, consumer cập nhật trạng thái,
// job dọn upload bỏ dở. Chưa có auth: M3 thêm middleware role admin cho nhóm /api/admin.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"streamfilm/internal/catalog"
	"streamfilm/internal/config"
	"streamfilm/internal/db"
	"streamfilm/internal/events"
	"streamfilm/internal/httpx"
	"streamfilm/internal/storage"
	"streamfilm/internal/upload"
	"streamfilm/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
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
		Now: time.Now, NewID: uuid.NewString}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, httpx.RequestLogger(log))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	(&upload.Handler{Svc: svc, Log: log}).Routes(r)
	(&catalog.AdminHandler{Videos: repo, Log: log}).Routes(r)

	updater := &catalog.StatusUpdater{Store: repo, Log: log}
	cleaner := &upload.Cleaner{Videos: repo, Objects: store, BucketRaw: cfg.BucketRaw,
		MaxAge: cfg.StaleUploadAfter, Now: time.Now, Log: log}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := consumer.Run(ctx, updater.Handle); err != nil {
			log.Error("status consumer stopped", "err", err)
			stop()
		}
	})
	wg.Go(func() { cleaner.Run(ctx, cfg.CleanupInterval) })

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-ctx.Done():
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
		return nil
	}
	return serveErr
}
```

- [ ] **Step 2: Sửa `Makefile`** — thay dòng `.PHONY` và target `build`, thêm `run-api`:

```makefile
.PHONY: env up down logs ps test test-short build sample transcode run-api

build:
	go build -o bin/api ./cmd/api
	go build -o bin/transcoder ./cmd/transcoder

# Chạy api trên host (cần `make up`; dừng service api trong compose trước nếu đã có, vì cùng cổng 8000).
run-api: env
	set -a; . ./.env; set +a; go run ./cmd/api
```

- [ ] **Step 3: Dọn module và build**

Run: `go mod tidy && go vet ./... && make build && ls bin`
Expected: không lỗi; `bin` có `api` và `transcoder`. `go.mod` có `github.com/google/uuid v1.6.0` trong khối require trực tiếp.

- [ ] **Step 4: Chạy api trên host**

Run (terminal riêng, hạ tầng đang chạy bằng `make up`): `make run-api`
Expected: log JSON `"msg":"migrations applied","versions":["0001_videos"]` (lần chạy sau là `null`) và `"msg":"http listening","addr":":8000"`.

Run: `curl -s localhost:8000/healthz`
Expected: `ok`

- [ ] **Step 5: Smoke test upload tới `queued`**

Run:
```bash
test -f tmp/sample.mp4 || make sample
SIZE=$(wc -c < tmp/sample.mp4 | tr -d ' ')
CREATE=$(curl -s -X POST localhost:8000/api/admin/videos -H 'Content-Type: application/json' \
  -d "{\"title\":\"Smoke M2\",\"filename\":\"sample.mp4\",\"size_bytes\":$SIZE}")
echo "$CREATE" | jq '{id, status, parts: (.upload.parts | length), part_size: .upload.part_size}'
ID=$(echo "$CREATE" | jq -r .id)
curl -s -o /dev/null -w 'PUT %{http_code}\n' -X PUT --upload-file tmp/sample.mp4 "$(echo "$CREATE" | jq -r '.upload.parts[0].url')"
curl -s -X POST localhost:8000/api/admin/videos/$ID/complete; echo
curl -s -X POST localhost:8000/api/admin/videos/$ID/complete; echo
curl -s "localhost:8000/api/admin/videos?status=queued" | jq -r '.videos[].id' | grep -c "$ID"
echo $ID > tmp/smoke-id
```
Expected:
- `{"id":"<uuid>","status":"uploading","parts":1,"part_size":67108864}`
- `PUT 200`
- Lần complete đầu: `{"id":"<uuid>","status":"queued"}`; lần hai: `{"error":"invalid status transition: video is queued"}` (HTTP 409).
- `1`
- Redpanda Console http://localhost:8080 → Topics → `video.uploaded`: có một message, key = `<uuid>`, value `{"video_id":"<uuid>","raw_key":"<uuid>/source.mp4"}`. MinIO Console bucket `raw` có `<uuid>/source.mp4`.

Dừng api bằng Ctrl+C. Expected: log `shutting down`, tiến trình thoát mã 0.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/api Makefile
git commit -m "feat(api): wire HTTP server, migrations, status consumer and cleanup job"
```

---

### Task 10: Transcoder worker — consume, retry, DLQ

**Files:**
- Create: `internal/worker/worker.go`, `internal/worker/worker_test.go`, `cmd/transcoder/worker.go`
- Modify: `cmd/transcoder/main.go`, `Makefile`

**Interfaces:**
- Consumes: `transcode.Runner.Transcode`, `transcode.Result`, `transcode.ErrNoVideo`, `transcode.DefaultLadder` (M1); `storage.Store.Download/UploadDir` (M1); `events.*` (Task 4).
- Produces:
  ```go
  type Transcoder interface { Transcode(ctx context.Context, input, outDir string) (transcode.Result, error) }
  type Storage interface {
  	Download(ctx context.Context, bucket, key, dest string) error
  	UploadDir(ctx context.Context, bucket, prefix, dir string) (int, error)
  }
  type Publisher interface { Publish(ctx context.Context, topic string, ev events.Event) error }
  var DefaultBackoffs = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}
  type Worker struct {
  	Transcoder Transcoder; Store Storage; Pub Publisher
  	BucketRaw, BucketHLS, WorkDir string
  	Backoffs []time.Duration
  	Sleep func(ctx context.Context, d time.Duration) error
  	Log *slog.Logger
  }
  func SleepCtx(ctx context.Context, d time.Duration) error
  func Transcoded(id string, res transcode.Result) events.VideoTranscoded
  func (w *Worker) Handle(ctx context.Context, rec *kgo.Record) error // khớp events.Handler
  ```
  CLI: `transcoder file -in <video> -id <id>` (giữ nguyên M1), `transcoder worker` (mới). Target Make `run-worker`.

- [ ] **Step 1: Viết test thất bại `internal/worker/worker_test.go`**

```go
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
	"streamfilm/internal/transcode"
)

type fakeTranscoder struct {
	errs  []error // lỗi theo từng lần gọi; hết danh sách thì thành công
	calls int
	res   transcode.Result
}

func (f *fakeTranscoder) Transcode(_ context.Context, input, outDir string) (transcode.Result, error) {
	f.calls++
	if f.calls <= len(f.errs) && f.errs[f.calls-1] != nil {
		return transcode.Result{}, f.errs[f.calls-1]
	}
	return f.res, nil
}

type fakeStore struct {
	downloads, uploads []string
}

func (f *fakeStore) Download(_ context.Context, bucket, key, dest string) error {
	f.downloads = append(f.downloads, bucket+"/"+key)
	return os.WriteFile(dest, []byte("raw"), 0o644)
}

func (f *fakeStore) UploadDir(_ context.Context, bucket, prefix, dir string) (int, error) {
	f.uploads = append(f.uploads, bucket+"/"+prefix)
	return 3, nil
}

type published struct {
	topic string
	ev    events.Event
}

type fakePub struct {
	got   []published
	errOn map[string]error
}

func (f *fakePub) Publish(_ context.Context, topic string, ev events.Event) error {
	if err := f.errOn[topic]; err != nil {
		return err
	}
	f.got = append(f.got, published{topic, ev})
	return nil
}

func (f *fakePub) topics() []string {
	var out []string
	for _, p := range f.got {
		out = append(out, p.topic)
	}
	return out
}

var okResult = transcode.Result{
	Probe: transcode.ProbeResult{Width: 1280, Height: 720, DurationSec: 29.6, HasAudio: true},
	Rungs: []transcode.Rung{transcode.DefaultLadder[1], transcode.DefaultLadder[3]},
}

type harness struct {
	w     *Worker
	tr    *fakeTranscoder
	store *fakeStore
	pub   *fakePub
	slept []time.Duration
}

func newHarness(t *testing.T, errs ...error) *harness {
	h := &harness{tr: &fakeTranscoder{errs: errs, res: okResult}, store: &fakeStore{},
		pub: &fakePub{errOn: map[string]error{}}}
	h.w = &Worker{Transcoder: h.tr, Store: h.store, Pub: h.pub, BucketRaw: "raw", BucketHLS: "hls",
		WorkDir: t.TempDir(), Backoffs: DefaultBackoffs,
		Sleep: func(_ context.Context, d time.Duration) error { h.slept = append(h.slept, d); return nil },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil))}
	return h
}

func uploadedRecord(t *testing.T) *kgo.Record {
	t.Helper()
	rec, err := events.NewRecord(events.TopicVideoUploaded, events.VideoUploaded{VideoID: "vid-1", RawKey: "vid-1/source.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestHandleSuccess(t *testing.T) {
	h := newHarness(t)
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err != nil {
		t.Fatal(err)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoProcessing, events.TopicVideoTranscoded}) {
		t.Fatalf("topics = %v", got)
	}
	want := events.VideoTranscoded{VideoID: "vid-1", DurationS: 30, HLSPrefix: "vid-1/", Renditions: []events.Rendition{
		{Height: 720, BitrateKbps: 2800, PlaylistKey: "vid-1/720p/index.m3u8"},
		{Height: 360, BitrateKbps: 800, PlaylistKey: "vid-1/360p/index.m3u8"},
	}}
	if got := h.pub.got[1].ev; !reflect.DeepEqual(got, want) {
		t.Errorf("transcoded = %+v", got)
	}
	if !reflect.DeepEqual(h.store.downloads, []string{"raw/vid-1/source.mp4"}) ||
		!reflect.DeepEqual(h.store.uploads, []string{"hls/vid-1"}) {
		t.Errorf("downloads=%v uploads=%v", h.store.downloads, h.store.uploads)
	}
	if len(h.slept) != 0 {
		t.Errorf("slept %v", h.slept)
	}
	if entries, _ := os.ReadDir(h.w.WorkDir); len(entries) != 0 {
		t.Errorf("work dir not cleaned: %d entries", len(entries))
	}
}

func TestHandleRetriesThenSucceeds(t *testing.T) {
	boom := errors.New("ffmpeg: boom")
	h := newHarness(t, boom, boom)
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err != nil {
		t.Fatal(err)
	}
	if h.tr.calls != 3 || !reflect.DeepEqual(h.slept, []time.Duration{5 * time.Second, 30 * time.Second}) {
		t.Errorf("calls=%d slept=%v", h.tr.calls, h.slept)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoProcessing, events.TopicVideoTranscoded}) {
		t.Errorf("topics = %v", got)
	}
}

func TestHandleExhaustsRetries(t *testing.T) {
	boom := errors.New("ffmpeg: boom")
	h := newHarness(t, boom, boom, boom, boom, boom)
	rec := uploadedRecord(t)
	if err := h.w.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if h.tr.calls != 4 {
		t.Errorf("calls = %d, want 4 (1 lần + 3 lần thử lại)", h.tr.calls)
	}
	if !reflect.DeepEqual(h.slept, DefaultBackoffs) {
		t.Errorf("slept = %v", h.slept)
	}
	want := []string{events.TopicVideoProcessing, events.TopicVideoFailed, events.TopicVideoUploadedDLQ}
	if got := h.pub.topics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("topics = %v", got)
	}
	failed := h.pub.got[1].ev.(events.VideoFailed)
	if failed.VideoID != "vid-1" || !strings.Contains(failed.Reason, "ffmpeg: boom") {
		t.Errorf("failed = %+v", failed)
	}
	dl := h.pub.got[2].ev.(events.DeadLetter)
	if dl.VideoID != "vid-1" || dl.Attempts != 4 || string(dl.Payload) != string(rec.Value) ||
		!strings.Contains(dl.Error, "ffmpeg: boom") || dl.FailedAt.IsZero() {
		t.Errorf("dead letter = %+v", dl)
	}
}

func TestHandlePermanentError(t *testing.T) {
	h := newHarness(t, transcode.ErrNoVideo)
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err != nil {
		t.Fatal(err)
	}
	if h.tr.calls != 1 || len(h.slept) != 0 {
		t.Errorf("calls=%d slept=%v, want no retry", h.tr.calls, h.slept)
	}
	if got := h.pub.topics(); len(got) != 3 || got[2] != events.TopicVideoUploadedDLQ {
		t.Errorf("topics = %v", got)
	}
	if dl := h.pub.got[2].ev.(events.DeadLetter); dl.Attempts != 1 {
		t.Errorf("attempts = %d", dl.Attempts)
	}
}

func TestHandleBadMessageGoesToDLQ(t *testing.T) {
	h := newHarness(t)
	rec := &kgo.Record{Topic: events.TopicVideoUploaded, Value: []byte("not json")}
	if err := h.w.Handle(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoUploadedDLQ}) {
		t.Fatalf("topics = %v", got)
	}
	dl := h.pub.got[0].ev.(events.DeadLetter)
	var s string
	if dl.Attempts != 0 || json.Unmarshal(dl.Payload, &s) != nil || s != "not json" {
		t.Errorf("dead letter = %+v", dl)
	}
	if h.tr.calls != 0 {
		t.Error("transcoder must not run")
	}
}

func TestHandleCancelledDuringBackoff(t *testing.T) {
	h := newHarness(t, errors.New("boom"))
	h.w.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled (không commit)", err)
	}
	if got := h.pub.topics(); !reflect.DeepEqual(got, []string{events.TopicVideoProcessing}) {
		t.Errorf("topics = %v, must not publish failed", got)
	}
}

func TestHandlePublishErrorIsReturned(t *testing.T) {
	h := newHarness(t)
	h.pub.errOn[events.TopicVideoProcessing] = errors.New("broker down")
	if err := h.w.Handle(context.Background(), uploadedRecord(t)); err == nil {
		t.Fatal("want error so the consumer retries the record")
	}
	if h.tr.calls != 0 {
		t.Error("transcoder must not run when processing event cannot be published")
	}
}
```

- [ ] **Step 2: Chạy test để thấy lỗi**

Run: `go test -short ./internal/worker/`
Expected: FAIL, `undefined: Worker`.

- [ ] **Step 3: Viết `internal/worker/worker.go`**

```go
// Package worker là logic job của transcoder: nhận video.uploaded, transcode, publish kết quả.
// Transcoder không ghi Postgres; mọi thay đổi trạng thái đi qua sự kiện Kafka.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
	"streamfilm/internal/transcode"
)

type Transcoder interface {
	Transcode(ctx context.Context, input, outDir string) (transcode.Result, error)
}

type Storage interface {
	Download(ctx context.Context, bucket, key, dest string) error
	UploadDir(ctx context.Context, bucket, prefix, dir string) (int, error)
}

type Publisher interface {
	Publish(ctx context.Context, topic string, ev events.Event) error
}

// DefaultBackoffs: thời gian chờ trước lần thử lại 1, 2, 3 (spec §7).
var DefaultBackoffs = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

type Worker struct {
	Transcoder           Transcoder
	Store                Storage
	Pub                  Publisher
	BucketRaw, BucketHLS string
	WorkDir              string
	Backoffs             []time.Duration
	Sleep                func(ctx context.Context, d time.Duration) error
	Log                  *slog.Logger
}

func SleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Handle xử lý một record video.uploaded. Trả nil khi job đã có kết quả cuối (transcoded, hoặc
// failed + DLQ) để consumer commit offset. Trả lỗi khi không publish được hoặc ctx bị huỷ:
// offset không được commit và record sẽ được xử lý lại.
func (w *Worker) Handle(ctx context.Context, rec *kgo.Record) error {
	ev, err := events.Decode[events.VideoUploaded](rec.Value)
	if err == nil && ev.RawKey == "" {
		err = errors.New("decode event: missing raw_key")
	}
	if err != nil {
		w.Log.Error("bad video.uploaded message, sending to DLQ", "partition", rec.Partition, "offset", rec.Offset, "err", err)
		return w.deadLetter(ctx, ev.VideoID, rec.Value, err, 0)
	}
	log := w.Log.With("video_id", ev.VideoID)

	if err := w.Pub.Publish(ctx, events.TopicVideoProcessing, events.VideoProcessing{VideoID: ev.VideoID}); err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		start := time.Now()
		out, err := w.process(ctx, ev)
		if err == nil {
			if err := w.Pub.Publish(ctx, events.TopicVideoTranscoded, out); err != nil {
				return err
			}
			log.Info("video transcoded", "attempt", attempt, "duration_s", out.DurationS,
				"renditions", len(out.Renditions), "took_ms", time.Since(start).Milliseconds())
			return nil
		}
		if ctx.Err() != nil {
			log.Warn("job interrupted, offset not committed", "attempt", attempt)
			return ctx.Err()
		}
		permanent := errors.Is(err, transcode.ErrNoVideo)
		if permanent || attempt > len(w.Backoffs) {
			log.Error("transcode failed", "attempts", attempt, "permanent", permanent, "err", err)
			failed := events.VideoFailed{VideoID: ev.VideoID, Reason: truncate(err.Error(), 500)}
			if err := w.Pub.Publish(ctx, events.TopicVideoFailed, failed); err != nil {
				return err
			}
			return w.deadLetter(ctx, ev.VideoID, rec.Value, err, attempt)
		}
		wait := w.Backoffs[attempt-1]
		log.Warn("transcode attempt failed, retrying", "attempt", attempt, "retry_in", wait.String(), "err", err)
		if err := w.Sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func (w *Worker) process(ctx context.Context, ev events.VideoUploaded) (events.VideoTranscoded, error) {
	work, err := os.MkdirTemp(w.WorkDir, "job-"+ev.VideoID+"-")
	if err != nil {
		return events.VideoTranscoded{}, err
	}
	defer os.RemoveAll(work)

	src := filepath.Join(work, "source"+path.Ext(ev.RawKey))
	if err := w.Store.Download(ctx, w.BucketRaw, ev.RawKey, src); err != nil {
		return events.VideoTranscoded{}, fmt.Errorf("download raw: %w", err)
	}
	out := filepath.Join(work, "hls")
	res, err := w.Transcoder.Transcode(ctx, src, out)
	if err != nil {
		return events.VideoTranscoded{}, fmt.Errorf("transcode: %w", err)
	}
	// Ghi đè hls/{id}/ nên chạy lại job là idempotent.
	if _, err := w.Store.UploadDir(ctx, w.BucketHLS, ev.VideoID, out); err != nil {
		return events.VideoTranscoded{}, fmt.Errorf("upload hls: %w", err)
	}
	return Transcoded(ev.VideoID, res), nil
}

// Transcoded dựng sự kiện video.transcoded từ kết quả FFmpeg.
func Transcoded(id string, res transcode.Result) events.VideoTranscoded {
	out := events.VideoTranscoded{VideoID: id, DurationS: int(math.Round(res.Probe.DurationSec)), HLSPrefix: id + "/"}
	for _, r := range res.Rungs {
		out.Renditions = append(out.Renditions, events.Rendition{
			Height: r.Height, BitrateKbps: r.VideoKbps, PlaylistKey: id + "/" + r.Name + "/index.m3u8",
		})
	}
	return out
}

func (w *Worker) deadLetter(ctx context.Context, videoID string, payload []byte, cause error, attempts int) error {
	raw := json.RawMessage(payload)
	if !json.Valid(payload) {
		raw, _ = json.Marshal(string(payload)) // payload hỏng: lưu dạng chuỗi JSON
	}
	return w.Pub.Publish(ctx, events.TopicVideoUploadedDLQ, events.DeadLetter{
		VideoID: videoID, Payload: raw, Error: cause.Error(), Attempts: attempts, FailedAt: time.Now().UTC(),
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
```

- [ ] **Step 4: Chạy test**

Run: `go test -short ./internal/worker/ -v`
Expected: 7 test PASS.

- [ ] **Step 5: Viết lại `cmd/transcoder/main.go`**

```go
// Command transcoder biến video thành HLS.
//
//	transcoder file -in <video> -id <video_id>   chạy một lần từ CLI (M1)
//	transcoder worker                             consume video.uploaded từ Kafka (M2)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"streamfilm/internal/config"
	"streamfilm/internal/storage"
	"streamfilm/internal/transcode"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "transcoder")
	slog.SetDefault(logger)
	if len(os.Args) < 2 {
		usage()
	}

	// SIGTERM (docker stop) huỷ ctx: FFmpeg bị kill, offset của job dở không được commit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	cfg := config.Load()
	var err error
	switch os.Args[1] {
	case "file":
		err = fileCmd(ctx, cfg, os.Args[2:])
	case "worker":
		err = runWorker(ctx, cfg)
	default:
		usage()
	}
	stop()
	if err != nil {
		slog.Error("transcoder failed", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:\n  transcoder file -in <video> -id <video_id>\n  transcoder worker")
	os.Exit(2)
}

func newStore(cfg config.Config) *storage.Store {
	return storage.New(storage.Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
	})
}

func fileCmd(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("file", flag.ExitOnError)
	in := fs.String("in", "", "đường dẫn file video đầu vào")
	id := fs.String("id", "", "video id, dùng làm prefix trong bucket hls")
	fs.Parse(args)
	if *in == "" || *id == "" {
		fs.Usage()
		os.Exit(2)
	}
	return runFile(ctx, cfg, *in, *id)
}

func runFile(ctx context.Context, cfg config.Config, input, id string) error {
	work, err := os.MkdirTemp("", "streamfilm-"+id+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	out := filepath.Join(work, "hls")

	slog.Info("transcode start", "video_id", id, "input", input)
	res, err := transcode.Runner{FFmpegPath: cfg.FFmpegPath, FFprobePath: cfg.FFprobePath}.Transcode(ctx, input, out)
	if err != nil {
		return err
	}
	rungs := make([]string, len(res.Rungs))
	for i, r := range res.Rungs {
		rungs[i] = r.Name
	}
	slog.Info("transcode done", "video_id", id, "duration_s", res.Probe.DurationSec, "rungs", rungs)

	n, err := newStore(cfg).UploadDir(ctx, cfg.BucketHLS, id, out)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/hls/%s/master.m3u8", cfg.CDNBaseURL, id)
	slog.Info("upload done", "video_id", id, "files", n, "url", url)
	fmt.Println(url)
	return nil
}
```

- [ ] **Step 6: Viết `cmd/transcoder/worker.go`**

```go
package main

import (
	"context"
	"log/slog"

	"streamfilm/internal/config"
	"streamfilm/internal/events"
	"streamfilm/internal/transcode"
	"streamfilm/internal/worker"
)

// runWorker consume video.uploaded (group "transcoder"), mỗi lần một job.
// Scale ngang bằng cách chạy thêm worker, tối đa bằng số partition (3).
func runWorker(ctx context.Context, cfg config.Config) error {
	producer, err := events.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	consumer, err := events.NewConsumer(cfg.KafkaBrokers, "transcoder", slog.Default(), events.TopicVideoUploaded)
	if err != nil {
		return err
	}
	defer consumer.Close()

	w := &worker.Worker{
		Transcoder: transcode.Runner{FFmpegPath: cfg.FFmpegPath, FFprobePath: cfg.FFprobePath},
		Store:      newStore(cfg),
		Pub:        producer,
		BucketRaw:  cfg.BucketRaw,
		BucketHLS:  cfg.BucketHLS,
		WorkDir:    cfg.WorkDir,
		Backoffs:   worker.DefaultBackoffs,
		Sleep:      worker.SleepCtx,
		Log:        slog.Default(),
	}
	slog.Info("worker started", "brokers", cfg.KafkaBrokers, "topic", events.TopicVideoUploaded)
	err = consumer.Run(ctx, w.Handle)
	slog.Info("worker stopped")
	return err
}
```

- [ ] **Step 7: Thêm target `run-worker` vào `Makefile`** — đổi dòng `.PHONY` thành:

```makefile
.PHONY: env up down logs ps test test-short build sample transcode run-api run-worker
```

và thêm sau target `run-api`:

```makefile
# Chạy transcoder worker trên host (cần FFmpeg trên máy; dừng service transcoder trong compose trước nếu đã có).
run-worker: env
	set -a; . ./.env; set +a; go run ./cmd/transcoder worker
```

- [ ] **Step 8: Build và kiểm tra chế độ file vẫn chạy**

Run: `go vet ./... && make build && ./bin/transcoder; echo "exit=$?"`
Expected: in usage 3 dòng và `exit=2`.

Run: `make transcode FILE=tmp/sample.mp4 ID=demo`
Expected: vẫn in `http://localhost:8088/hls/demo/master.m3u8` như M1.

- [ ] **Step 9: Chạy toàn luồng trên host**

Run: terminal 1 `make run-api`, terminal 2 `make run-worker`.
Expected (terminal 2): log `worker started`, rồi với video smoke ở Task 9 (đang `queued` trong Kafka): `"msg":"video transcoded","video_id":"<uuid>","attempt":1,"duration_s":30,"renditions":4`.

Run:
```bash
ID=$(cat tmp/smoke-id)
curl -s localhost:8000/api/admin/videos/$ID | jq '{status, duration_s, renditions: [.renditions[].height]}'
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8088/hls/$ID/master.m3u8
```
Expected: `{"status":"ready","duration_s":30,"renditions":[1080,720,480,360]}` và `200`. Terminal 1 có log `video status updated` cho `video.processing` và `video.transcoded`.

Dừng cả hai bằng Ctrl+C. Expected: worker log `worker stopped`, thoát mã 0.

- [ ] **Step 10: Commit**

```bash
git add internal/worker cmd/transcoder Makefile
git commit -m "feat(transcoder): Kafka worker with retries, failed event and DLQ"
```

---

### Task 11: Docker compose cho api + transcoder và e2e M2

**Files:**
- Create: `deploy/docker/api.Dockerfile`, `deploy/docker/transcoder.Dockerfile`, `.dockerignore`, `scripts/e2e-m2.sh`
- Modify: `deploy/docker-compose.yml`, `Makefile`

**Interfaces:**
- Consumes: `cmd/api`, `cmd/transcoder worker` (Task 9, 10); endpoint `POST /api/admin/videos`, `POST /{id}/complete`, `GET /api/admin/videos/{id}` (Task 7); nginx `/hls/` (M1).
- Produces: service compose `api` (127.0.0.1:8000) và `transcoder`; `make up` build và chạy cả hai; `make e2e-m2`; `scripts/e2e-m2.sh <file>` (exit 0 khi OK).

- [ ] **Step 1: Viết `deploy/docker/api.Dockerfile`**

```dockerfile
FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.24.4
RUN adduser -D -u 10001 app
COPY --from=build /out/api /usr/local/bin/api
USER app
EXPOSE 8000
ENTRYPOINT ["/usr/local/bin/api"]
```

- [ ] **Step 2: Viết `deploy/docker/transcoder.Dockerfile`**

```dockerfile
FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/transcoder ./cmd/transcoder

FROM alpine:3.24.4
# Gói ffmpeg của Alpine có sẵn libx264 và ffprobe.
RUN apk add --no-cache ffmpeg && adduser -D -u 10001 app
COPY --from=build /out/transcoder /usr/local/bin/transcoder
USER app
ENV WORK_DIR=/tmp
ENTRYPOINT ["/usr/local/bin/transcoder"]
CMD ["worker"]
```

- [ ] **Step 3: Viết `.dockerignore`**

```gitignore
.git
.env
bin
tmp
docs
web/node_modules
```

- [ ] **Step 4: Thêm service vào `deploy/docker-compose.yml`** — chèn ngay trước dòng `volumes:` không thụt lề ở cuối file (khai báo named volume):

```yaml
  api:
    build:
      context: ..
      dockerfile: deploy/docker/api.Dockerfile
    image: streamfilm-api:dev
    environment:
      HTTP_ADDR: ":8000"
      DATABASE_URL: postgres://streamfilm:${POSTGRES_PASSWORD}@postgres:5432/streamfilm?sslmode=disable
      KAFKA_BROKERS: kafka:9092
      S3_ENDPOINT: http://minio:9000
      # Presigned URL được ký cho địa chỉ mà máy host nhìn thấy (chữ ký gắn với Host).
      S3_PUBLIC_ENDPOINT: http://localhost:9000
      S3_REGION: ${S3_REGION}
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
      S3_BUCKET_RAW: ${S3_BUCKET_RAW}
      S3_BUCKET_HLS: ${S3_BUCKET_HLS}
      STALE_UPLOAD_AFTER: 24h
      CLEANUP_INTERVAL: 1h
    ports: ["127.0.0.1:8000:8000"]
    depends_on:
      postgres: { condition: service_healthy }
      minio-init: { condition: service_completed_successfully }
      kafka-init: { condition: service_completed_successfully }
    restart: unless-stopped
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8000/healthz | grep -q ok"]
      interval: 5s
      retries: 20

  transcoder:
    build:
      context: ..
      dockerfile: deploy/docker/transcoder.Dockerfile
    image: streamfilm-transcoder:dev
    command: ["worker"]
    environment:
      KAFKA_BROKERS: kafka:9092
      S3_ENDPOINT: http://minio:9000
      S3_REGION: ${S3_REGION}
      S3_ACCESS_KEY: ${S3_ACCESS_KEY}
      S3_SECRET_KEY: ${S3_SECRET_KEY}
      S3_BUCKET_RAW: ${S3_BUCKET_RAW}
      S3_BUCKET_HLS: ${S3_BUCKET_HLS}
      WORK_DIR: /tmp
    depends_on:
      minio-init: { condition: service_completed_successfully }
      kafka-init: { condition: service_completed_successfully }
    restart: unless-stopped
    stop_grace_period: 30s

```

- [ ] **Step 5: Sửa `Makefile`** — thay dòng `.PHONY` và target `up`, thêm `logs-app`, `e2e-m2`:

```makefile
.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker e2e-m2

# Các service one-shot (minio-init, kafka-init) thoát với mã 0 nên không dùng --wait cho chúng.
up: env
	$(COMPOSE) up -d --build
	$(COMPOSE) up -d --wait postgres minio kafka console clickhouse nginx api transcoder

logs-app:
	$(COMPOSE) logs -f --tail=100 api transcoder

# E2E M2: create → upload presigned → complete → ready → HLS qua nginx.
e2e-m2:
	@test -f tmp/sample.mp4 || $(MAKE) sample
	./scripts/e2e-m2.sh tmp/sample.mp4
```

- [ ] **Step 6: Viết `scripts/e2e-m2.sh`**

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
CREATE=$(curl -fsS -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d "$BODY")
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
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X PUT --upload-file "$part" "$URL")
  [ "$code" = 200 ] || die "upload part $i: HTTP $code"
done
[ "$i" = "$PARTS" ] || die "đã upload $i part, API cần $PARTS"

echo "3/5 complete"
curl -fsS -X POST "$API/api/admin/videos/$ID/complete" | jq -c .

echo "4/5 đợi status=ready (tối đa ${TIMEOUT}s)"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  V=$(curl -fsS "$API/api/admin/videos/$ID")
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
curl -fsS "$CDN/hls/$ID/master.m3u8" | grep -q '#EXTM3U' || die "master.m3u8 không hợp lệ"
for key in $(echo "$V" | jq -r '.renditions[].playlist_key'); do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$CDN/hls/$key")
  [ "$code" = 200 ] || die "$key: HTTP $code"
done
PLAYLIST=$(echo "$V" | jq -r '.renditions[0].playlist_key')
SEG=$(curl -fsS "$CDN/hls/$PLAYLIST" | grep -m1 '\.ts$')
code=$(curl -s -o /dev/null -w '%{http_code}' "$CDN/hls/$(dirname "$PLAYLIST")/$SEG")
[ "$code" = 200 ] || die "segment $SEG: HTTP $code"

echo "OK  $CDN/dev/player.html?id=$ID"
```

Run: `chmod +x scripts/e2e-m2.sh`

- [ ] **Step 7: Kiểm tra image transcoder có FFmpeg đủ codec**

Run: `docker compose -f deploy/docker-compose.yml --env-file .env build api transcoder && docker compose -f deploy/docker-compose.yml --env-file .env run --rm --no-deps --entrypoint ffmpeg transcoder -hide_banner -encoders | grep -E 'libx264|aac'`
Expected: build thành công; có dòng chứa `libx264` và dòng chứa `aac`.

- [ ] **Step 8: Dựng stack và chạy e2e**

Dừng `make run-api` / `make run-worker` nếu còn chạy (cùng cổng 8000 / cùng consumer group).

Run: `make up && make ps`
Expected: `api` ở trạng thái `running (healthy)`, `transcoder` ở `running`.

Run: `make e2e-m2`
Expected (rút gọn):
```
1/5 tạo video (sample.mp4, … bytes)
    id=<uuid> parts=1 part_size=67108864
2/5 upload 1 part qua presigned URL
3/5 complete
{"id":"<uuid>","status":"queued"}
4/5 đợi status=ready (tối đa 300s)
    status=queued|processing …
    status=ready
{"duration_s":30,"renditions":[1080,720,480,360]}
5/5 kiểm tra HLS qua nginx
OK  http://localhost:8088/dev/player.html?id=<uuid>
```
Mở URL cuối trên Chrome: video phát, đổi được chất lượng.

- [ ] **Step 9: Xem message trong Redpanda Console**

Mở http://localhost:8080 → Topics.
Expected: `video.uploaded`, `video.processing`, `video.transcoded` mỗi topic có message với key = `<uuid>` của e2e; value `video.transcoded` có `duration_s`, `hls_prefix`, `renditions[]`. Tab Consumer Groups: `api` và `transcoder` có lag 0.

- [ ] **Step 10: Kiểm tra lỗi vĩnh viễn → `video.failed` + DLQ, rồi retry**

Run:
```bash
ffmpeg -hide_banner -y -f lavfi -i sine=frequency=440:sample_rate=48000 -t 3 -c:a aac tmp/audio-only.mp4
./scripts/e2e-m2.sh tmp/audio-only.mp4; echo "exit=$?"
```
Expected: `FAIL: transcode lỗi: transcode: input has no video stream` và `exit=1`. Redpanda Console topic `video.uploaded.dlq` có message mới: `attempts: 1`, `error` chứa `input has no video stream`, `payload` là message gốc.

Run (thay `<uuid>` bằng id vừa lỗi): `curl -s -X POST localhost:8000/api/admin/videos/<uuid>/retry; echo; sleep 5; curl -s localhost:8000/api/admin/videos/<uuid> | jq -r .status`
Expected: `{"id":"<uuid>","status":"queued"}` rồi `failed` (lỗi lại vì file không có video).

- [ ] **Step 11: Kiểm tra Kafka không khả dụng → `/complete` trả 503**

Run:
```bash
C="docker compose -f deploy/docker-compose.yml --env-file .env"
SIZE=$(wc -c < tmp/sample.mp4 | tr -d ' ')
CREATE=$(curl -s -X POST localhost:8000/api/admin/videos -H 'Content-Type: application/json' \
  -d "{\"title\":\"Kafka down\",\"filename\":\"sample.mp4\",\"size_bytes\":$SIZE}")
ID=$(echo "$CREATE" | jq -r .id)
curl -s -o /dev/null -X PUT --upload-file tmp/sample.mp4 "$(echo "$CREATE" | jq -r '.upload.parts[0].url')"
$C stop kafka
curl -s -w ' HTTP %{http_code} in %{time_total}s\n' -X POST localhost:8000/api/admin/videos/$ID/complete
curl -s localhost:8000/api/admin/videos/$ID | jq -r .status
$C start kafka && $C up -d --wait kafka
curl -s -w ' HTTP %{http_code}\n' -X POST localhost:8000/api/admin/videos/$ID/complete
```
Expected: `{"error":"event bus unavailable, try again"} HTTP 503 in` ≤ 10 giây; status vẫn `uploading`; sau khi Kafka lên lại, complete trả `{"id":"…","status":"queued"} HTTP 200` và video về `ready` trong khoảng một phút (`make logs-app` để theo dõi).

- [ ] **Step 12: Kiểm tra transcoder bị dừng giữa job thì job được làm lại**

Run: terminal 1 `make logs-app`; terminal 2 `./scripts/e2e-m2.sh tmp/sample.mp4`; ngay khi terminal 2 in `status=processing`, chạy terminal 3: `docker compose -f deploy/docker-compose.yml --env-file .env restart transcoder`.
Expected: log transcoder có `job interrupted, offset not committed` và `worker stopped`, rồi `worker started` và `video transcoded` cho **cùng** `video_id`; e2e ở terminal 2 kết thúc `OK`.

- [ ] **Step 13: Commit**

```bash
git add deploy/docker deploy/docker-compose.yml .dockerignore Makefile scripts/e2e-m2.sh
git commit -m "feat(deploy): run api and transcoder in compose, add M2 e2e script"
```

---

### Task 12: Tài liệu M2 và ADR

**Files:**
- Create: `docs/flows.md`, `docs/data-model.md`, `docs/api.md`, `docs/decisions/0004-presigned-multipart-upload.md`, `docs/decisions/0005-embedded-migrations.md`, `docs/decisions/0006-kafka-commit-after-success.md`, `docs/decisions/0007-accept-dual-write.md`, `docs/decisions/0008-services-in-compose.md`
- Modify: `README.md`, `docs/architecture.md`, `docs/operations.md`

**Interfaces:**
- Consumes: mọi thứ đã chạy ở Task 1–11. Chỉ mô tả những gì đã chạy (spec mục 11).

- [ ] **Step 1: Viết lại `README.md`**

````markdown
# Streamfilm

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **M2** — upload thẳng lên storage bằng presigned URL, pipeline `video.uploaded → transcoder → video.transcoded → ready` qua Kafka. Chưa có đăng nhập (M3). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2
- FFmpeg (có `ffprobe`) — để sinh video mẫu và chạy test
- `curl`, `jq` — cho script e2e

## Chạy nhanh

```bash
make up        # dựng hạ tầng + build và chạy api (cổng 8000) và transcoder
make e2e-m2    # sinh tmp/sample.mp4 nếu chưa có, upload → transcode → kiểm tra HLS qua nginx
```

Script in ra link dev player để xem video vừa xử lý. Theo dõi message tại Redpanda Console http://localhost:8080.

## Test

```bash
make test-short   # chỉ unit test
make test         # gồm integration test (cần Docker và FFmpeg)
```

## Tài liệu

- [Kiến trúc](docs/architecture.md)
- [Các luồng](docs/flows.md)
- [Data model và sự kiện](docs/data-model.md)
- [API](docs/api.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
````

- [ ] **Step 2: Viết lại `docs/architecture.md`**

````markdown
# Kiến trúc

## Tổng quan

Modular monolith + worker qua Kafka: `api`, `transcoder`, `analytics` (M6). Video không đi qua `api`: upload thẳng lên storage bằng presigned URL, phát thẳng từ CDN.

```mermaid
flowchart LR
  Admin[Admin / script e2e] -->|metadata, complete| API[api]
  Admin -->|presigned PUT từng part| S3[(MinIO)]
  API --> PG[(Postgres)]
  API -->|video.uploaded| K{{Kafka}}
  K --> T[transcoder worker]
  T -->|raw/ → hls/| S3
  T -->|video.processing / transcoded / failed| K
  K --> API
  Viewer[Web + hls.js] -->|segments| N[nginx CDN]
  N --> S3
```

Phần đã chạy ở M2: `api` (upload, danh sách video cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker`, Postgres, Kafka, MinIO, nginx. Chưa có: đăng nhập và signed URL (M3), web (M4), analytics (M6).

## api

| Module | Trách nhiệm ở M2 |
|---|---|
| `internal/upload` | `POST /api/admin/videos` (tạo video, mở multipart upload, trả presigned URL), `/complete`, `/retry`; job dọn upload bỏ dở quá 24 giờ |
| `internal/catalog` | Repository `videos` / `renditions`, máy trạng thái, `GET /api/admin/videos[/{id}]`, consumer `video.processing` / `video.transcoded` / `video.failed` (group `api`) |
| `internal/events` | Topic, schema JSON, producer/consumer franz-go |
| `internal/db`, `migrations/` | Kết nối pgx, migration SQL nhúng, chạy khi api khởi động |

Chỉ `api` ghi Postgres. Endpoint admin **chưa có auth** ở M2; M3 bọc nhóm `/api/admin` bằng middleware yêu cầu role `admin`.

## Transcoder

`transcoder worker` consume `video.uploaded` (group `transcoder`), xử lý tuần tự từng job:

1. Publish `video.processing`.
2. Tải `raw/{id}/source.{ext}` về thư mục tạm, chạy pipeline FFmpeg (bảng dưới), upload `hls/{id}/` (ghi đè, nên chạy lại là idempotent).
3. Publish `video.transcoded`, rồi mới commit offset.

Lỗi: thử lại sau 5 giây, 30 giây, 2 phút. Hết lượt, hoặc lỗi không thể sửa bằng thử lại (file không có video stream), thì publish `video.failed` và ghi `video.uploaded.dlq`, rồi commit. Khi nhận SIGTERM, FFmpeg bị huỷ, offset không commit, job được làm lại khi worker khởi động. Scale ngang bằng cách chạy thêm worker, tối đa 3 (số partition).

Chế độ `transcoder file -in … -id …` của M1 vẫn dùng được để thử nhanh không qua Kafka.

| Bước | Chi tiết |
|---|---|
| Probe | `ffprobe` lấy chiều cao, thời lượng, có audio hay không. Không có video thì báo lỗi |
| Chọn ladder | 1080p 5000k, 720p 2800k, 480p 1400k, 360p 800k. Không upscale; nguồn nhỏ hơn 360p vẫn ra 360p |
| Encode | Một lần chạy FFmpeg: `split` + `scale` cho mọi bậc, H.264 `veryfast` main, AAC stereo |
| Keyframe | `-force_key_frames expr:gte(t,n_forced*2)` và `-sc_threshold 0`: keyframe đúng mỗi 2 giây ở mọi bậc |
| Đóng gói | HLS VOD, segment 6 giây MPEG-TS, `master.m3u8` + `{bậc}/index.m3u8` |

## Ngữ nghĩa xử lý Kafka

- Key message là `video_id`, nên mọi sự kiện của một video nằm cùng partition và giữ thứ tự.
- Consumer (cả `api` và `transcoder`) tắt auto-commit, commit offset của từng record **sau khi** handler xử lý xong. Nếu handler lỗi (DB hoặc Kafka chập chờn), record đó được thử lại tại chỗ với giãn cách 1s → 30s; không bỏ qua, không làm crash process.
- Do đó giao nhận là *at-least-once*: handler phải idempotent. `api` bỏ qua sự kiện làm chuyển trạng thái không hợp lệ (ví dụ `video.transcoded` khi video đã `ready`).
- Ghi Postgres rồi publish Kafka là dual-write, chấp nhận ở MVP ([ADR 0007](decisions/0007-accept-dual-write.md)).

## Bố cục storage

```
raw/{id}/source.{mp4|mov|mkv|webm}   (upload multipart từ client)
hls/{id}/master.m3u8
hls/{id}/{1080p|720p|480p|360p}/index.m3u8
hls/{id}/{1080p|720p|480p|360p}/seg_000.ts …
```

## Đối chiếu với kiến trúc Hygraph

| Hygraph | Streamfilm |
|---|---|
| Upload service (presigned) | module `upload` trong `api`, presigned multipart lên MinIO |
| Video Processing Service | `transcoder worker` |
| Message queue | Kafka KRaft 1 node, 7 topic tạo sẵn |
| Metadata DB | Postgres (`videos`, `renditions`) |
| S3 | MinIO |
| CDN | nginx `proxy_cache` (ký URL từ M3) |
````

- [ ] **Step 3: Viết `docs/flows.md`**

````markdown
# Các luồng

Chỉ gồm các luồng đã chạy (M2). Luồng xem phim với signed URL, xem tiếp và analytics được thêm ở M3–M6.

## Upload và transcode

```mermaid
sequenceDiagram
  autonumber
  participant A as Admin / e2e
  participant API as api
  participant PG as Postgres
  participant S3 as MinIO
  participant K as Kafka
  participant T as transcoder

  A->>API: POST /api/admin/videos {title, filename, size_bytes}
  API->>S3: CreateMultipartUpload raw/{id}/source.ext
  API->>PG: INSERT videos (status=uploading, upload_id)
  API-->>A: 201 {id, upload: {part_size, parts[{part_number, url}]}}
  loop mỗi part (64 MiB)
    A->>S3: PUT presigned url (hết hạn sau 1 giờ)
  end
  A->>API: POST /api/admin/videos/{id}/complete
  API->>S3: ListParts + CompleteMultipartUpload
  API->>PG: status uploading → queued
  API->>K: video.uploaded {video_id, raw_key}
  API-->>A: 200 {status: queued}
  K->>T: video.uploaded
  T->>K: video.processing
  K->>API: video.processing → status processing
  T->>S3: tải raw, FFmpeg, upload hls/{id}/
  T->>K: video.transcoded {duration_s, hls_prefix, renditions[]}
  T->>K: commit offset (group transcoder)
  K->>API: video.transcoded → renditions, duration_s, status ready
```

## Retry và DLQ

```mermaid
sequenceDiagram
  participant K as Kafka
  participant T as transcoder
  participant API as api
  K->>T: video.uploaded
  T->>K: video.processing
  T->>T: lần 1 lỗi → chờ 5s → lần 2 lỗi → chờ 30s → lần 3 lỗi → chờ 2 phút → lần 4 lỗi
  T->>K: video.failed {video_id, reason}
  T->>K: video.uploaded.dlq {payload, error, attempts: 4}
  T->>K: commit offset
  K->>API: video.failed → status failed, fail_reason
  Note over API: Admin gọi POST /{id}/retry: failed → queued, publish lại video.uploaded
```

Lỗi không sửa được bằng thử lại (file không có video stream) đi thẳng tới `video.failed` + DLQ với `attempts: 1`. Message hỏng (không phải JSON, thiếu `video_id`/`raw_key`) đi thẳng vào DLQ với `attempts: 0`.

## Kafka không khả dụng khi complete

`/complete` đặt `queued` trước rồi publish (chờ tối đa 5 giây). Publish lỗi thì video được trả về `uploading` và API trả `503`; admin gọi lại `/complete` khi Kafka lên. Multipart đã ghép ở lần trước được nhận ra nhờ object `raw/{id}/source.ext` đã tồn tại. `/retry` xử lý tương tự (trả về `failed`).

## Dọn upload bỏ dở

Job trong `api` chạy lúc khởi động và mỗi `CLEANUP_INTERVAL` (mặc định 1 giờ): tìm video `uploading` tạo quá `STALE_UPLOAD_AFTER` (24 giờ), xoá dòng DB (chỉ khi vẫn còn `uploading`), rồi abort multipart upload và xoá object dưới `raw/{id}/`.
````

- [ ] **Step 4: Viết `docs/data-model.md`**

````markdown
# Data model

## Postgres (M2)

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp). Bảng `users` (M3), cột `search` (M4), `subtitles` và `watch_progress` (M5) được thêm bằng migration của giai đoạn đó.

```sql
videos (
  id uuid pk, title text, description text, genres text[], year int,
  status text check (status in ('uploading','queued','processing','ready','failed')),
  fail_reason text,        -- lý do lỗi gần nhất, xoá khi ready
  duration_s int, raw_key text,
  upload_id text,          -- id multipart upload trên S3 (thêm so với spec)
  size_bytes bigint,       -- kích thước khai báo khi tạo, dùng tính số part (thêm so với spec)
  hls_prefix text, poster_key text, sprite_key text,
  created_at timestamptz, updated_at timestamptz
)
renditions (video_id uuid fk on delete cascade, height int, bitrate_kbps int, playlist_key text,
            pk(video_id, height))
```

Index: `videos(created_at desc)`, `videos(status, created_at)`.

## Trạng thái video

```mermaid
stateDiagram-v2
  [*] --> uploading: POST /api/admin/videos
  uploading --> queued: /complete
  queued --> processing: video.processing
  processing --> ready: video.transcoded
  queued --> failed: video.failed
  processing --> failed: video.failed
  failed --> queued: /retry
  uploading --> [*]: dọn sau 24 giờ
```

Mọi chuyển trạng thái đi qua `catalog.CanTransition` trong cùng transaction với `SELECT … FOR UPDATE`. Chuyển không hợp lệ (sự kiện trùng hoặc đến muộn) bị bỏ qua và ghi log. Ngoại lệ duy nhất: khi publish `video.uploaded` lỗi, `queued` được trả về trạng thái trước đó (bước bù trừ).

## Kafka

| Topic | Producer | Consumer group | Payload |
|---|---|---|---|
| `video.uploaded` | api | `transcoder` | `{"video_id","raw_key"}` |
| `video.uploaded.dlq` | transcoder | (xem bằng Redpanda Console) | `{"video_id","payload","error","attempts","failed_at"}` |
| `video.processing` | transcoder | `api` | `{"video_id"}` |
| `video.transcoded` | transcoder | `api` | `{"video_id","duration_s","hls_prefix","renditions":[{"height","bitrate_kbps","playlist_key"}]}` |
| `video.failed` | transcoder | `api` | `{"video_id","reason"}` |

Key message = `video_id`. Mỗi topic 3 partition. `playback.events` và `app.logs` đã tạo sẵn, dùng từ M6. `poster_key` / `sprite_key` trong `video.transcoded` có từ M5.

Ví dụ `video.transcoded`:

```json
{"video_id":"7c1e…","duration_s":30,"hls_prefix":"7c1e…/","renditions":[
  {"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"},
  {"height":720,"bitrate_kbps":2800,"playlist_key":"7c1e…/720p/index.m3u8"}]}
```

Ví dụ `video.uploaded.dlq`:

```json
{"video_id":"7c1e…","payload":{"video_id":"7c1e…","raw_key":"7c1e…/source.mp4"},
 "error":"transcode: input has no video stream","attempts":1,"failed_at":"2026-11-14T10:00:00Z"}
```
````

- [ ] **Step 5: Viết `docs/api.md`**

````markdown
# API

Base URL local: `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.

> M2: các endpoint `/api/admin/*` **chưa có xác thực**. M3 thêm đăng nhập (cookie JWT) và chỉ cho role `admin`.

| Method | Path | Mô tả |
|---|---|---|
| GET | `/healthz` | `ok` |
| POST | `/api/admin/videos` | Tạo video, nhận presigned multipart upload |
| POST | `/api/admin/videos/{id}/complete` | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | Chi tiết video kèm `renditions` |

## POST /api/admin/videos

```json
{"title":"Phim A","description":"","genres":["drama"],"year":2024,"filename":"movie.mp4","size_bytes":157286400}
```

`filename` phải kết thúc bằng `.mp4`, `.mov`, `.mkv` hoặc `.webm`. `size_bytes` từ 1 tới 10 GB.

`201`:

```json
{"id":"7c1e…","status":"uploading","upload":{"upload_id":"…","part_size":67108864,
 "parts":[{"part_number":1,"url":"http://localhost:9000/raw/7c1e…/source.mp4?partNumber=1&uploadId=…&X-Amz-…"},
          {"part_number":2,"url":"…"},{"part_number":3,"url":"…"}],
 "expires_at":"2026-11-14T11:00:00Z"}}
```

Client cắt file thành các khúc `part_size` byte (khúc cuối nhỏ hơn) và `PUT` khúc thứ n vào `url` của `part_number` n. Không cần gửi lại ETag: API tự liệt kê part khi complete.

Lỗi: `400` body sai / thiếu `title` / kích thước hoặc đuôi file không hợp lệ.

## POST /api/admin/videos/{id}/complete

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`.

| Mã | Khi nào |
|---|---|
| `400` | Chưa upload đủ part (`multipart upload incomplete: got 1 parts, want 3`) |
| `404` | Không có video |
| `409` | Video không ở `uploading` (`invalid status transition: video is queued`) |
| `503` | Kafka không khả dụng; video vẫn `uploading`, gọi lại sau |

## POST /api/admin/videos/{id}/retry

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`. `409` nếu video không ở `failed`, `404`, `503` như trên.

## GET /api/admin/videos

`?status=failed&limit=20` → `200`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,
  "status":"failed","fail_reason":"transcode: input has no video stream","size_bytes":157286400,
  "created_at":"2026-11-14T10:00:00Z","updated_at":"2026-11-14T10:01:00Z"}]}
```

`400` nếu `status` không thuộc `uploading|queued|processing|ready|failed` hoặc `limit` ngoài 1–200.

## GET /api/admin/videos/{id}

`200`:

```json
{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,"status":"ready",
 "duration_s":30,"size_bytes":157286400,"hls_prefix":"7c1e…/",
 "created_at":"…","updated_at":"…",
 "renditions":[{"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"}]}
```
````

- [ ] **Step 6: Viết lại `docs/operations.md`**

````markdown
# Vận hành local

## Lệnh

| Lệnh | Việc |
|---|---|
| `make up` / `make down` | Dựng (build lại image api/transcoder) / tắt toàn bộ stack |
| `make ps` / `make logs` | Trạng thái / log mọi container |
| `make logs-app` | Log của `api` và `transcoder` |
| `make e2e-m2` | E2E: upload `tmp/sample.mp4` → đợi `ready` → kiểm tra HLS qua nginx |
| `make sample` | Sinh `tmp/sample.mp4` |
| `make build` | Build `bin/api`, `bin/transcoder` |
| `make run-api` / `make run-worker` | Chạy api / worker trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |

`make up` chạy `up -d --build` rồi `up -d --wait` cho các service chạy lâu. Các container one-shot (`minio-init`, `kafka-init`) thoát với mã 0 nên không dùng `--wait` cho chúng. `api` tự chạy migration khi khởi động.

Xoá sạch dữ liệu: `docker compose -f deploy/docker-compose.yml down -v`.

## Cổng và giao diện

| Dịch vụ | Địa chỉ | Đăng nhập |
|---|---|---|
| api | http://localhost:8000 (`/healthz`) | chưa có auth (M3) |
| nginx "CDN" | http://localhost:8088 | |
| Dev player | http://localhost:8088/dev/player.html?id={video_id} | |
| MinIO API (presigned URL) | http://localhost:9000 | |
| MinIO Console | http://localhost:9001 | `S3_ACCESS_KEY` / `S3_SECRET_KEY` trong `.env` |
| Redpanda Console (Kafka) | http://localhost:8080 | |
| ClickHouse HTTP | http://localhost:8123 | `streamfilm` / `POSTGRES_PASSWORD` |
| Postgres | localhost:5432 | `streamfilm` / `POSTGRES_PASSWORD` |
| Kafka từ máy host | localhost:29092 | |

## Biến môi trường mới (M2)

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `DATABASE_URL` | `postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable` | Postgres |
| `KAFKA_BROKERS` | `localhost:29092` | Danh sách broker, phân tách bằng dấu phẩy |
| `S3_PUBLIC_ENDPOINT` | bằng `S3_ENDPOINT` | Host dùng để ký presigned URL; trong compose là `http://localhost:9000` vì api gọi MinIO qua `http://minio:9000` |
| `HTTP_ADDR` | `:8000` | Địa chỉ lắng nghe của api |
| `WORK_DIR` | thư mục tạm của OS | Nơi transcoder tải raw và ghi HLS tạm |
| `STALE_UPLOAD_AFTER` / `CLEANUP_INTERVAL` | `24h` / `1h` | Job dọn upload bỏ dở |

## Theo dõi Kafka

- Redpanda Console → **Topics** → chọn topic → tab Messages: xem key (`video_id`) và JSON.
- **Consumer Groups**: `api` (đọc `video.processing|transcoded|failed`) và `transcoder` (đọc `video.uploaded`). Lag khác 0 kéo dài nghĩa là consumer đang kẹt; xem `make logs-app`.
- DLQ: topic `video.uploaded.dlq`. Mỗi message có `payload` gốc, `error`, `attempts`. Sau khi sửa nguyên nhân, gọi `POST /api/admin/videos/{id}/retry`.

## Truy vấn nhanh Postgres

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec postgres \
  psql -U streamfilm -c "select id, status, fail_reason, created_at from videos order by created_at desc limit 10"
```

## Image MinIO

Image `minio/minio` trên Docker Hub không còn pull được, nên stack dùng `cgr.dev/chainguard/minio` và `cgr.dev/chainguard/minio-client`, ghim theo digest. Xem [ADR 0003](decisions/0003-minio-image.md).

## Kiểm tra cache CDN

```bash
curl -sI http://localhost:8088/hls/{video_id}/720p/seg_000.ts | grep -i x-cache-status
```
Lần đầu `MISS`, các lần sau `HIT`.

## Sự cố thường gặp

| Triệu chứng | Cách xử lý |
|---|---|
| `make up` treo ở kafka | Xem `make logs`; xoá volume `kafkadata` nếu đổi cấu hình listener |
| `/complete` trả `503` | Kafka chưa sẵn sàng: `make ps`, đợi kafka healthy rồi gọi lại `/complete` |
| `/complete` trả `400 multipart upload incomplete` | Chưa `PUT` đủ part, hoặc URL đã hết hạn (1 giờ): tạo video mới |
| `PUT` presigned URL trả `403 SignatureDoesNotMatch` | URL được ký cho host khác host đang gọi; kiểm tra `S3_PUBLIC_ENDPOINT` |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
| Video `failed` | Xem `fail_reason` qua `GET /api/admin/videos?status=failed` và message trong DLQ |
| `make run-api` báo cổng 8000 đã dùng | Dừng service `api` trong compose trước |
| Integration test bị skip | Bật Docker Desktop; kiểm tra `ffmpeg` trong `PATH`; không dùng `-short` |
````

- [ ] **Step 7: Viết ADR**

`docs/decisions/0004-presigned-multipart-upload.md`:

```markdown
# ADR 0004: Upload bằng presigned multipart, API tự ListParts khi complete

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
File video lớn (tới 10 GB) không được đi qua api. Một presigned PUT bị S3/R2 giới hạn 5 GB.

## Quyết định
`POST /api/admin/videos` mở multipart upload và trả presigned URL cho từng part 64 MiB (tối đa 160 part), hết hạn sau 1 giờ. Client chỉ cần `PUT` từng part. `/complete` không nhận body: api gọi `ListParts` để lấy ETag rồi `CompleteMultipartUpload`, kiểm tra đủ part 1..N. Lưu `upload_id` và `size_bytes` trong bảng `videos`.

## Hệ quả
- Hỗ trợ file tới 10 GB và chạy được trên S3/R2 không cần đổi code.
- Client không phải đọc header `ETag` (trình duyệt cần CORS `ExposeHeaders` mới đọc được).
- Presigned URL ký theo Host nên cần `S3_PUBLIC_ENDPOINT` khi api gọi storage qua địa chỉ nội bộ.
- Multipart dở được dọn sau 24 giờ bởi job trong api.
```

`docs/decisions/0005-embedded-migrations.md`:

```markdown
# ADR 0005: Migration SQL nhúng, runner tự viết

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Cần tạo schema Postgres khi dựng stack, không muốn thêm bước chạy công cụ migrate riêng.

## Quyết định
File `migrations/NNNN_*.sql` được nhúng bằng `embed`. Khi khởi động, api lấy `pg_advisory_lock`, áp các file chưa có trong bảng `schema_migrations` theo thứ tự tên, mỗi file trong một transaction. Mỗi giai đoạn chỉ thêm bảng mà code của giai đoạn đó dùng (M2: `videos`, `renditions`).

## Hệ quả
- Không thêm dependency; dễ đọc để học cơ chế migrate.
- Chỉ có migrate tiến, không có down migration. Cần rollback thì viết migration mới.
- Nếu cần tính năng nâng cao (down, migration bằng Go) thì chuyển sang `pressly/goose`, định dạng file tương thích.
```

`docs/decisions/0006-kafka-commit-after-success.md`:

```markdown
# ADR 0006: Commit offset sau khi xử lý xong, thử lại tại chỗ, DLQ ở transcoder

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Transcoder có thể chết giữa job; DB hoặc Kafka có thể chập chờn. Không được mất sự kiện, không được đảo thứ tự sự kiện của cùng một video.

## Quyết định
- Consumer tắt auto-commit, commit từng record sau khi handler trả về thành công.
- Handler lỗi thì record được thử lại tại chỗ (1s → 30s), không bỏ qua và không crash. Lỗi "nghiệp vụ" (JSON hỏng, chuyển trạng thái không hợp lệ) handler tự log và trả thành công.
- Transcoder thử lại FFmpeg sau 5s, 30s, 2 phút ngay trong handler (offset chưa commit trong lúc chờ). Hết lượt hoặc lỗi vĩnh viễn: publish `video.failed` + `video.uploaded.dlq`, rồi commit.
- Key = `video_id` để giữ thứ tự theo video.

## Hệ quả
- At-least-once: handler phải idempotent (HLS ghi đè `hls/{id}/`, api bỏ qua chuyển trạng thái không hợp lệ).
- Một record lỗi kéo dài chặn các record sau nó trong cùng partition (head-of-line blocking). Chấp nhận ở MVP; bản sau có thể dùng topic retry riêng.
- Mỗi worker xử lý tuần tự một job; tối đa 3 worker song song (3 partition).
```

`docs/decisions/0007-accept-dual-write.md`:

```markdown
# ADR 0007: Chấp nhận dual-write Postgres + Kafka ở MVP

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
`/complete` và `/retry` vừa đổi trạng thái trong Postgres vừa publish `video.uploaded`. Hai thao tác không nằm chung một transaction.

## Quyết định
Đặt `queued` trước rồi publish (chờ tối đa 5 giây). Publish lỗi thì trả video về trạng thái trước đó và trả `503` để admin gọi lại. Không dùng outbox pattern ở MVP.

## Hệ quả
- Đặt trạng thái trước đảm bảo api không nhận `video.processing` khi DB còn ở trạng thái cũ.
- Nếu api chết đúng giữa hai bước, video có thể kẹt ở `queued` mà không có sự kiện; admin xử lý thủ công. Bản sau: outbox table + relay publish.
```

`docs/decisions/0008-services-in-compose.md`:

```markdown
# ADR 0008: Chạy api và transcoder trong docker compose

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Spec đặt FFmpeg trong container transcoder và vẽ mọi service trong compose. Khi dev vẫn muốn chạy nhanh trên host.

## Quyết định
Thêm image `api` và `transcoder` (multi-stage `golang:1.27.4-alpine3.24` → `alpine:3.24.4`, transcoder cài `ffmpeg` của Alpine). `make up` build và chạy cả hai, `make e2e-m2` kiểm tra toàn luồng. `make run-api` / `make run-worker` chạy trên host khi dev. Trong compose, api gọi MinIO qua `http://minio:9000` nhưng ký presigned URL cho `http://localhost:9000` (`S3_PUBLIC_ENDPOINT`).

## Hệ quả
- Demo một lệnh, môi trường giống nhau giữa các máy; thử được tình huống restart worker giữa job.
- Đổi code phải `make up` để build lại image.
- Phiên bản FFmpeg trong container theo kho Alpine 3.24, có thể khác FFmpeg trên host.
```

- [ ] **Step 8: Kiểm tra lần cuối và commit**

Run: `go vet ./... && make test && make e2e-m2`
Expected: `go vet` không cảnh báo; mọi package `ok` (integration test chạy vì không `-short`); e2e in `OK`.

```bash
git add README.md docs/architecture.md docs/operations.md docs/flows.md docs/data-model.md docs/api.md docs/decisions
git commit -m "docs: M2 upload and Kafka pipeline docs and ADRs"
```

---

## Self-review

**Độ phủ spec M2:**

| Yêu cầu | Task |
|---|---|
| Presigned upload (multipart), URL hết hạn 1 giờ, giới hạn 10 GB | 5, 6 |
| `POST /api/admin/videos`, `/complete`, `/retry`, `GET /api/admin/videos` (status, fail_reason) | 6, 7 |
| `/complete` kiểm tra object tồn tại, đặt `queued`, publish `video.uploaded`; Kafka chết → 503 | 6, 11 (Step 11) |
| Transcoder consume `video.uploaded`, publish `video.processing`, tải raw, transcode, upload HLS, publish `video.transcoded {video_id, duration_s, renditions[]}` | 10 |
| Transcoder không ghi Postgres | 10 (chỉ publish) |
| Commit offset sau khi thành công; chết giữa job thì làm lại | 4, 10, 11 (Step 12) |
| Retry 5s/30s/2m rồi `video.failed` + DLQ (`payload`, `error`, `attempts`) | 10, 11 (Step 10) |
| api consume `video.processing|transcoded|failed`, idempotent (bỏ qua khi đã `ready`) | 3, 8 |
| Máy trạng thái `uploading → queued → processing → ready`, `failed` và retry | 3 |
| Dọn upload bỏ dở 24h kèm object raw và multipart | 3, 5, 8, 9 |
| Postgres migrations, bảng `videos`, `renditions` | 2 |
| Key message = `video_id` | 4 |
| Log `slog` JSON có `service`, `request_id`, `video_id` | 7, 8, 9, 10 |
| Thấy message trong Redpanda Console | 9 (Step 5), 11 (Step 9) |
| Integration test testcontainers (Postgres, MinIO, Kafka) | 2, 3, 4, 5 |
| E2E script | 11 |
| Tài liệu architecture/flows/data-model/api/operations + ADR | 12 |
| Admin chưa auth, M3 thêm | 7 (comment), 9, 12 (api.md) |

Ngoài phạm vi M2 (đúng spec): auth/JWT, signed URL nginx (M3); poster, sprite, phụ đề (M5); `app.logs` qua Kafka (M6); PATCH/DELETE video và trang admin (M7).

**Quét placeholder:** không có "TBD", "TODO", "tương tự Task N"; mọi bước có code hoặc lệnh đầy đủ kèm kết quả mong đợi.

**Nhất quán chữ ký:**
- `events.Event` (Key) dùng trong `upload.Publisher`, `worker.Publisher`, `events.Producer.Publish` — cùng chữ ký `Publish(ctx, topic string, ev events.Event) error`.
- `events.Handler` = `func(ctx, *kgo.Record) error` khớp `catalog.StatusUpdater.Handle` và `worker.Worker.Handle`.
- `*catalog.Repo` thoả `upload.Videos` (Create/Get/Transition/RevertQueued), `catalog.AdminStore` (List/Get/Renditions), `catalog.StatusStore` (Transition/MarkReady/MarkFailed), `upload.StaleVideos` (ListStaleUploading/DeleteUploading).
- `*storage.Store` thoả `upload.Objects` (CreateMultipartUpload/PresignUploadPart/CompleteMultipartUpload/Exists), `upload.StaleObjects` (AbortMultipartUpload/DeletePrefix), `worker.Storage` (Download/UploadDir).
- `transcode.Runner` (giá trị) thoả `worker.Transcoder`.
- `events.Rendition` ↔ `catalog.Rendition` cùng trường (`Height`, `BitrateKbps`, `PlaylistKey`), map trong `StatusUpdater`.
- Key raw: `upload.RawKey(id, ext)` = `{id}/source{ext}`; worker đọc `ev.RawKey`; cleaner xoá prefix `{id}/`; e2e/nginx dùng `hls/{id}/…` = `HLSPrefix` + `playlist_key`.

## Các kế hoạch tiếp theo

1. **M3** — Auth (bcrypt, JWT cookie, `make seed-admin`), middleware role `admin` cho `/api/admin/*`, `internal/signing`, `/play`, nginx `secure_link_md5`.
2. **M4** — Web React + Vite: catalog, tìm kiếm không dấu (migration cột `search`, `unaccent`, `pg_trgm`), player.
3. **M5** — Xem tiếp, poster, sprite, phụ đề.
4. **M6** — analytics service, ClickHouse schema, `slog` handler gửi `app.logs`.
5. **M7** — Trang admin, `scripts/e2e.sh` đầy đủ, hoàn thiện tài liệu.
