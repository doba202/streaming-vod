# Streamfilm M6: Analytics — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Service mới `analytics` consume `playback.events` và `app.logs` từ Kafka, ghi theo lô (1000 record hoặc 1 giây) vào ClickHouse (`playback_events`, `app_logs`), commit offset chỉ sau khi INSERT thành công, ClickHouse lỗi thì thử lại không mất dữ liệu, record hỏng bị bỏ qua và đếm. `api` và `transcoder worker` log JSON `slog` (luôn có `service`, có `request_id` / `video_id` khi có) ra stdout **và** `app.logs` qua một sink không bao giờ chặn (đầy thì bỏ, đếm). Có 4 câu SQL mẫu chạy được (`make analytics-sql`: top phim theo lượt play, tỉ lệ xem hết, buffering theo bitrate, log theo service). Kiểm tra bằng test Go (gồm ClickHouse 24.8 và Redpanda thật qua testcontainers) và `make e2e-m6`.

**Architecture:** Package mới `internal/logging` (`New` = `slog.JSONHandler` ghi vào `io.MultiWriter(stdout, sink)` bọc bởi `contextHandler` thêm `request_id` từ ctx của chi; `KafkaSink` = `io.Writer` gọi `kgo.Client.TryProduce` trên client riêng với buffer 4096 record). `httpx.RequestLogger` thêm `video_id` khi route có `{id}` là UUID; các handler có `r` chuyển sang `…Context(r.Context(), …)`. Package mới `internal/analytics`: `Client` (HTTP interface 8123, `INSERT … FORMAT JSONEachRow`, `insert_deduplication_token`), `Schema`/`Migrate` (`CREATE TABLE IF NOT EXISTS`, chạy mỗi lần khởi động), `MapPlayback`/`MapLog` (record → một dòng JSONEachRow, lỗi `ErrInvalid`), `Pipeline` (poll theo lô có deadline, flush khi đủ 1000 hoặc 1 giây, retry với backoff, commit sau INSERT), `Run` (hai pipeline, hai consumer group). `cmd/analytics` chỉ log ra stdout (tránh vòng phản hồi qua `app.logs`). Compose thêm service `analytics` (đợi `kafka-init` xong và `clickhouse` healthy); SQL mẫu ở `scripts/analytics/*.sql`, chạy bằng `scripts/analytics-sql.sh` qua `curl`.

**Tech Stack:** Go 1.27.4, franz-go v1.22.3 (`TryProduce`, `PollRecords`, `CommitRecords`, `NewErrFetch`), chi v5.5.0, google/uuid v1.6.0, testcontainers-go v0.45.0 (`testcontainers.Run` + `wait.ForHTTP`, cùng module), ClickHouse `clickhouse/clickhouse-server:24.8` (HTTP interface), Kafka `apache/kafka:3.8.0` (compose) / Redpanda v25.2.4 (test). **Không thêm dependency:** `go.mod`, `go.sum` không đổi. Web không đổi.

**Spec:** `docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md` (mục 3.1, 3.3, 4.4, 5, 7, 9, 10, 11)

## Global Constraints

- Go module path: `streamfilm`. Làm trên nhánh `feat/mvp`. Không thêm module Go (`git diff --stat go.mod go.sum` rỗng sau mọi task). Không sửa `web/`.
- **Topic:** `playback.events` (đã có từ M5, key `user_id`), `app.logs` (hằng `events.TopicAppLogs = "app.logs"`, record không key, value là một dòng log JSON không có `\n`). Cả hai đã được `kafka-init` tạo (3 partition) từ M0 — compose không đổi phần Kafka.
- **ClickHouse:** HTTP `http://clickhouse:8123` trong compose, `http://localhost:8123` từ host; database `streamfilm`, user `streamfilm`, mật khẩu = `POSTGRES_PASSWORD` (đã như vậy từ M0). Gửi user/mật khẩu bằng header `X-ClickHouse-User` / `X-ClickHouse-Key`, database bằng tham số URL `database`. Client HTTP timeout `30s`; lỗi HTTP ≠ 200 → `clickhouse: HTTP {code}: {500 ký tự đầu}`.
- **Schema** (đúng như `internal/analytics/schema.go`): `playback_events(ts DateTime64(3, 'UTC'), user_id UUID, video_id UUID, type LowCardinality(String), position_s UInt32, bitrate_kbps UInt32, buffering_ms UInt32) ENGINE = MergeTree ORDER BY (video_id, ts)`; `app_logs(ts DateTime64(3, 'UTC'), service LowCardinality(String), level LowCardinality(String), msg String, request_id String, video_id String, attrs String) ENGINE = MergeTree ORDER BY (service, ts) TTL toDateTime(ts) + INTERVAL 30 DAY`; cả hai `SETTINGS non_replicated_deduplication_window = 1000`; `CREATE TABLE IF NOT EXISTS`, chạy mỗi lần analytics khởi động.
- **Thời gian:** gửi `ts` dạng `"2006-01-02 15:04:05.000"` theo UTC (`CHTime`).
- **Lô:** `DefaultMaxBatch = 1000` record (kể cả record hỏng), `DefaultMaxWait = 1s` tính từ record đầu tiên của lô; `InsertTimeout = 10s` mỗi lần INSERT; `ShutdownTimeout = 10s` ghi nốt lô khi tắt; backoff `events.DefaultBackoff` (1s, 2s, 4s, 8s, 16s, rồi 30s), thử lại mãi tới khi thành công hoặc ctx bị huỷ. Commit offset của mọi record trong lô **sau** INSERT thành công (lô toàn record hỏng: không INSERT, vẫn commit). `insert_deduplication_token` = `{table}` + với mỗi (topic, partition) sắp theo topic rồi partition: `/{topic}/{partition}:{offset nhỏ nhất}-{offset lớn nhất}`.
- **Consumer:** mỗi pipeline một client franz-go: group `analytics-playback` (topic `playback.events` → bảng `playback_events`) và `analytics-logs` (`app.logs` → `app_logs`); `ConsumeResetOffset(AtStart)`, `DisableAutoCommit`, `BlockRebalanceOnPoll`; `AllowRebalance` chỉ khi không giữ record chưa commit; đóng bằng `CloseAllowingRebalance`.
- **Map playback:** `events.Decode[events.PlaybackEvent]`; `user_id`, `video_id` phải là UUID (ghi dạng chữ thường); `ts` khác zero; `type` khác rỗng; `position_s`, `bitrate_kbps`, `buffering_ms` trong `[0, 4294967295]`. Sai → `ErrInvalid`.
- **Map log:** value phải là JSON object (`null`, mảng, chuỗi → `ErrInvalid`); `time` (RFC 3339) → `ts`, thiếu/sai → `Record.Timestamp`; `level`, `msg`, `service`, `request_id`, `video_id` → cột (thiếu/`null` → `""`, không phải chuỗi → JSON của giá trị); còn lại → `attrs` = JSON object, khoá sắp xếp, số giữ nguyên (`UseNumber`), không có gì → `{}`.
- **Logging:** `logging.New(service, stdout, kafka io.Writer)` → JSON `slog`, `.With("service", service)`; thêm `request_id` = `middleware.GetReqID(ctx)` khi khác rỗng và bản ghi chưa có `request_id`. `KafkaSink`: `MaxBuffered = 4096`, `DeliveryTimeout = 5s`, `FlushTimeout = 2s`, `RequiredAcks(LeaderAck())`, `DisableIdempotentWrite()`, `ProducerLinger(50ms)`; `Write` luôn trả `len(p), nil`; promise lỗi (gồm `ErrMaxBuffered`) → `Dropped++`. Tee vào Kafka: `api` (lệnh server) và `transcoder worker`. Chỉ stdout: `api seed-admin`, `api sign`, `transcoder file`, `analytics`. Khi tắt in `app.logs sink closed` kèm `dropped` ra stdout.
- **`httpx.RequestLogger`:** dòng `http request` có `request_id`, `method`, `path`, `status`, `duration_ms`, và `video_id` (UUID chuẩn hoá chữ thường) khi `chi.URLParam(r, "id")` parse được thành UUID. Qua nginx, `request_id` là `$request_id` (32 ký tự hex, nginx đặt `X-Request-Id` từ M4).
- **Biến môi trường (M6):** `CLICKHOUSE_URL` (mặc định `http://localhost:8123`), `CLICKHOUSE_DB` (`streamfilm`), `CLICKHOUSE_USER` (`streamfilm`), `CLICKHOUSE_PASSWORD` (`streamfilm`). Compose đặt cho `analytics`: `http://clickhouse:8123`, `streamfilm`, `streamfilm`, `${POSTGRES_PASSWORD}`.
- Integration test tự `t.Skip` khi `-short` hoặc thiếu Docker (như M2–M5). Tài liệu tiếng Việt, chỉ mô tả những gì đã chạy (spec §11).

## Quyết định thiết kế chính (ghi ADR ở Task 9)

| Quyết định | Lý do |
|---|---|
| ClickHouse qua HTTP interface (8123) bằng `net/http` + `JSONEachRow`, không dùng `clickhouse-go/v2` | Không thêm dependency nặng, không mở thêm cổng native 9000; 1000 row/giây là nhỏ; cùng giao thức với `curl` trong `make analytics-sql` / e2e — dễ học, dễ gỡ lỗi |
| Schema tạo bằng `CREATE TABLE IF NOT EXISTS` khi analytics khởi động, không dùng `docker-entrypoint-initdb.d` | initdb chỉ chạy khi volume rỗng — stack đang chạy đã có volume `chdata` nên sẽ không bao giờ có bảng; tạo trong analytics thì idempotent và test được bằng testcontainers (chạy `Migrate` hai lần) |
| Cột `ts` là `DateTime64(3, 'UTC')`, gửi chuỗi UTC `YYYY-MM-DD hh:mm:ss.sss` | Định dạng mặc định của ClickHouse đọc được; không phụ thuộc múi giờ server; spec chỉ ghi `DateTime64(3)` |
| Giữ `MergeTree` như spec; at-least-once, chấp nhận trùng | Sự kiện không có id duy nhất nên `ReplacingMergeTree` không có khoá đúng (và chỉ khử trùng khi merge/`FINAL`); trùng chỉ xảy ra khi chết giữa INSERT và commit |
| `insert_deduplication_token` theo khoảng offset + `non_replicated_deduplication_window = 1000` | Retry sau timeout (INSERT thực ra đã thành công) không ghi trùng; integration test kiểm tra cùng token → không tăng |
| `Pipeline` riêng thay vì `events.Consumer` | `events.Consumer` commit từng record và thử lại record lỗi mãi; analytics cần gom lô, commit cả lô, và bỏ qua record hỏng (một row sai kiểu làm ClickHouse từ chối cả lô) |
| Hai consumer group, mỗi topic một pipeline | ClickHouse lỗi ở một bảng không giữ offset của bảng kia; code một pipeline dùng lại cho cả hai |
| Poll có deadline = record đầu + 1 giây, giữ `BlockRebalanceOnPoll` tới khi commit | Đúng "1000 record hoặc 1 giây"; offset commit luôn thuộc partition consumer còn giữ (một instance analytics) |
| Sink log = `TryProduce` của franz-go trên client riêng (buffer 4096), không thêm channel | Buffer của franz-go đã là hàng đợi có giới hạn; `TryProduce` từ chối ngay khi đầy — đúng "bỏ khi đầy, không chặn"; test với broker không tồn tại: 1000 dòng < 1 giây |
| Một `JSONHandler` ghi `io.MultiWriter(stdout, sink)` | stdout và Kafka nhận cùng một dòng; `JSONHandler` gọi `Write` một lần mỗi bản ghi nên mỗi `Write` = một record Kafka |
| `request_id` từ ctx qua `contextHandler`; `video_id` trong `RequestLogger` từ `{id}` | Không phải sửa chữ ký handler; mọi dòng `http request` của route `/{id}` có `video_id`; log lỗi trong handler có `request_id` khi gọi `…Context` |
| `analytics` chỉ log ra stdout | Tee vào `app.logs` tạo vòng phản hồi (ClickHouse lỗi → log lỗi → thêm record `app.logs` → thêm lô lỗi) |
| SQL mẫu là file riêng `scripts/analytics/*.sql` + `scripts/analytics-sql.sh` (curl) | Đọc được trên GitHub, chạy được bằng `make`, e2e và test Go (`TestSampleQueries` chạy đúng các file này trên ClickHouse thật) |
| E2E ghi record hỏng dạng JSON hợp lệ (`user_id` không phải UUID) | Kiểm tra đường bỏ qua mà không làm hỏng `e2e-m5` (đọc topic bằng `jq`); bước dừng ClickHouse là tuỳ chọn (`CH_DOWN=1`) vì ảnh hưởng stack đang dùng |

## File Structure

```
streamfilm/
  internal/events/events.go                   # TopicAppLogs
  internal/logging/logging.go                 # mới: New, contextHandler
  internal/logging/kafka.go                   # mới: Producer, KafkaSink, DialKafka, hằng số
  internal/logging/logging_test.go            # mới
  internal/httpx/httpx.go(+_test mới)         # RequestLogger thêm video_id
  internal/catalog/{viewer,handler,subtitles}.go, internal/playback/handler.go   # log …Context(r.Context(), …)
  cmd/api/main.go                             # logging.New + KafkaSink
  cmd/transcoder/{main,worker}.go             # logging.New; worker tee app.logs
  internal/config/config.go(+_test)           # ClickHouseURL/DB/User/Password
  .env.example                                # CLICKHOUSE_*
  internal/testutil/containers.go             # StartClickHouse, ClickHouseImage
  internal/analytics/clickhouse.go(+_test)    # mới: Client (Exec, Query, Insert)
  internal/analytics/schema.go                # mới: TablePlaybackEvents, TableAppLogs, Schema, Migrate
  internal/analytics/rows.go(+_test)          # mới: CHTime, PlaybackRow, LogRow, Mapper, MapPlayback, MapLog, ParsePlayback, ParseLog
  internal/analytics/pipeline.go(+_test)      # mới: Source, Inserter, Pipeline, DedupToken, DialSource
  internal/analytics/service.go               # mới: GroupPlayback, GroupLogs, Run
  internal/analytics/integration_test.go      # mới: Redpanda + ClickHouse
  internal/analytics/queries_test.go          # mới: chạy scripts/analytics/*.sql
  cmd/analytics/main.go                       # mới
  deploy/docker/analytics.Dockerfile          # mới
  deploy/docker-compose.yml                   # service analytics
  scripts/analytics/{top_videos,completion_rate,buffering_by_bitrate,logs_by_service}.sql   # mới
  scripts/analytics-sql.sh, scripts/e2e-m6.sh # mới
  Makefile                                    # build, up, logs-app, run-analytics, analytics-sql, e2e-m6
  README.md, docs/{architecture,data-model,flows,operations}.md
  docs/decisions/0016-clickhouse-http-startup-schema.md, 0017-analytics-at-least-once.md, 0018-app-logs-async-sink.md   # mới
```

Không đổi: `go.mod`, `go.sum`, `migrations/`, `deploy/nginx/`, `web/`, topic trong `kafka-init`, service `clickhouse` trong compose, `docs/api.md` (không có endpoint mới), các script e2e cũ.

Quy ước trình bày code: file mới có toàn văn; file sửa được ghi thành các cặp "thay đoạn A bằng đoạn B" — A khớp đúng một lần trong file, áp theo thứ tự.

---

### Task 1: Package `logging` — logger JSON có `request_id`, sink `app.logs` không chặn

**Files:**
- Create: `internal/logging/logging.go`, `internal/logging/kafka.go`, `internal/logging/logging_test.go`
- Modify: `internal/events/events.go`

**Interfaces:**
- Consumes: `middleware.RequestIDKey` / `middleware.GetReqID` (chi v5), `kgo.Client.TryProduce/Flush/Close`, `kgo.ErrMaxBuffered` (franz-go v1.22.3).
- Produces:
  ```go
  // internal/events
  const TopicAppLogs = "app.logs"

  // internal/logging
  func New(service string, stdout io.Writer, kafka io.Writer) *slog.Logger
  const (
  	MaxBuffered     = 4096
  	DeliveryTimeout = 5 * time.Second
  	FlushTimeout    = 2 * time.Second
  )
  type Producer interface {
  	TryProduce(ctx context.Context, r *kgo.Record, promise func(*kgo.Record, error))
  	Flush(ctx context.Context) error
  	Close()
  }
  type KafkaSink struct{ /* p Producer; topic string; dropped atomic.Int64 */ }
  func NewKafkaSink(p Producer, topic string) *KafkaSink
  func DialKafka(brokers []string) (*KafkaSink, error)   // topic events.TopicAppLogs
  func (s *KafkaSink) Write(p []byte) (int, error)        // không bao giờ chặn, luôn len(p), nil
  func (s *KafkaSink) Dropped() int64
  func (s *KafkaSink) Close()
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

Test dùng `fakeProducer` cho ngữ nghĩa (cùng một dòng ra stdout và Kafka, `request_id` từ ctx, không trùng khoá, đếm lỗi) và một client franz-go thật trỏ tới broker không tồn tại để chứng minh `Write` không chặn (không cần Docker).

`internal/logging/logging_test.go` (mới):

```go
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/twmb/franz-go/pkg/kgo"
)

// fakeProducer ghi lại record; err != nil thì mọi record lỗi (như Kafka chết).
type fakeProducer struct {
	mu      sync.Mutex
	recs    []*kgo.Record
	err     error
	flushed bool
	closed  bool
}

func (p *fakeProducer) TryProduce(_ context.Context, r *kgo.Record, promise func(*kgo.Record, error)) {
	p.mu.Lock()
	p.recs = append(p.recs, r)
	err := p.err
	p.mu.Unlock()
	promise(r, err)
}

func (p *fakeProducer) Flush(context.Context) error { p.flushed = true; return nil }
func (p *fakeProducer) Close()                      { p.closed = true }

func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatalf("not JSON: %q: %v", line, err)
	}
	return m
}

func TestNewTeesSameLineToStdoutAndKafka(t *testing.T) {
	var stdout bytes.Buffer
	fp := &fakeProducer{}
	sink := NewKafkaSink(fp, "app.logs")
	log := New("api", &stdout, sink)

	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "host/abc-000001")
	log.InfoContext(ctx, "get video", "video_id", "v1")

	if len(fp.recs) != 1 {
		t.Fatalf("kafka records = %d, want 1", len(fp.recs))
	}
	rec := fp.recs[0]
	if rec.Topic != "app.logs" || rec.Key != nil {
		t.Errorf("topic/key = %q/%q", rec.Topic, rec.Key)
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != string(rec.Value) {
		t.Errorf("stdout %q != kafka %q", got, rec.Value)
	}
	m := decode(t, rec.Value)
	for k, want := range map[string]string{"service": "api", "request_id": "host/abc-000001",
		"video_id": "v1", "msg": "get video", "level": "INFO"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %q", k, m[k], want)
		}
	}
	if sink.Dropped() != 0 {
		t.Errorf("dropped = %d", sink.Dropped())
	}
}

func TestContextHandlerKeepsExplicitRequestID(t *testing.T) {
	var stdout bytes.Buffer
	log := New("api", &stdout, nil).With("component", "x")
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "from-ctx")
	log.InfoContext(ctx, "explicit", "request_id", "explicit-id")
	log.Info("no ctx")

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d", len(lines))
	}
	if n := strings.Count(lines[0], `"request_id"`); n != 1 {
		t.Errorf("request_id xuất hiện %d lần: %s", n, lines[0])
	}
	if m := decode(t, []byte(lines[0])); m["request_id"] != "explicit-id" || m["component"] != "x" {
		t.Errorf("line 0 = %v", m)
	}
	if m := decode(t, []byte(lines[1])); m["request_id"] != nil || m["service"] != "api" {
		t.Errorf("line 1 = %v", m)
	}
}

func TestKafkaSinkCountsFailedDeliveries(t *testing.T) {
	fp := &fakeProducer{err: errors.New("broker down")}
	sink := NewKafkaSink(fp, "app.logs")
	for range 3 {
		if n, err := sink.Write([]byte("{}\n")); n != 3 || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if sink.Dropped() != 3 {
		t.Errorf("dropped = %d, want 3", sink.Dropped())
	}
	sink.Close()
	if !fp.flushed || !fp.closed {
		t.Error("Close phải Flush rồi Close producer")
	}
}

// Client franz-go thật trỏ tới broker không tồn tại, buffer 2 record: Write không bao giờ chặn (dòng
// thứ 3 trở đi bị từ chối ngay với ErrMaxBuffered), Close trả về trong FlushTimeout, và mọi dòng đều
// được đếm là bỏ (promise chạy bất đồng bộ nên chờ một chút sau Close).
func TestKafkaSinkNeverBlocksWhenKafkaDown(t *testing.T) {
	cl, err := kgo.NewClient(kgo.SeedBrokers("127.0.0.1:1"), kgo.MaxBufferedRecords(2),
		kgo.RecordDeliveryTimeout(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	sink := NewKafkaSink(cl, "app.logs")
	log := New("api", &bytes.Buffer{}, sink)

	start := time.Now()
	for i := range 1000 {
		log.Info("line", "i", i)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("1000 dòng log mất %v: logger bị chặn", d)
	}
	start = time.Now()
	sink.Close()
	if d := time.Since(start); d > FlushTimeout+time.Second {
		t.Errorf("Close mất %v", d)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sink.Dropped() != 1000 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := sink.Dropped(); got != 1000 {
		t.Errorf("dropped = %d, want 1000", got)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/logging/`
Expected: FAIL — biên dịch lỗi `undefined: NewKafkaSink`, `undefined: New`, `undefined: FlushTimeout`.

- [ ] **Step 3: Cài đặt**

`internal/events/events.go` — thay:

```go
	TopicPlaybackEvents   = "playback.events"
)
```

bằng:

```go
	TopicPlaybackEvents   = "playback.events"
	// TopicAppLogs: mỗi record là một dòng log JSON của slog (M6), không có key.
	TopicAppLogs = "app.logs"
)
```

`internal/logging/logging.go` (mới):

```go
// Package logging dựng logger slog JSON cho mọi service (spec §7): mỗi dòng luôn có "service", có
// "request_id" khi ctx của lời gọi *Context mang request id của chi, ghi ra stdout và (tuỳ chọn) đồng
// thời gửi vào Kafka topic app.logs qua KafkaSink — không bao giờ chặn người gọi, đầy thì bỏ.
package logging

import (
	"context"
	"io"
	"log/slog"

	"github.com/go-chi/chi/v5/middleware"
)

// New trả logger JSON của service. Mỗi bản ghi được ghi ra stdout; nếu kafka khác nil thì cùng
// dòng JSON đó được ghi tiếp vào kafka (thường là *KafkaSink). JSONHandler gọi Write đúng một lần
// cho mỗi bản ghi, nên mỗi lần Write là một dòng log hoàn chỉnh.
func New(service string, stdout io.Writer, kafka io.Writer) *slog.Logger {
	w := stdout
	if kafka != nil {
		w = io.MultiWriter(stdout, kafka)
	}
	return slog.New(contextHandler{slog.NewJSONHandler(w, nil)}).With("service", service)
}

// contextHandler thêm request_id từ ctx (middleware.RequestID của chi) khi bản ghi chưa có.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := middleware.GetReqID(ctx); id != "" && !hasAttr(r, "request_id") {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(as)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}
```

`internal/logging/kafka.go` (mới):

```go
package logging

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

// Giới hạn của KafkaSink khi dùng DialKafka.
const (
	MaxBuffered     = 4096            // số dòng log tối đa chờ gửi; vượt thì bỏ dòng mới
	DeliveryTimeout = 5 * time.Second // Kafka chết: dòng đã nhận bị bỏ sau tối đa chừng này
	FlushTimeout    = 2 * time.Second // Close chờ gửi nốt tối đa chừng này
)

// Producer là phần của *kgo.Client mà KafkaSink dùng. TryProduce không bao giờ chặn: buffer đầy
// (MaxBufferedRecords) thì promise được gọi ngay với kgo.ErrMaxBuffered.
type Producer interface {
	TryProduce(ctx context.Context, r *kgo.Record, promise func(*kgo.Record, error))
	Flush(ctx context.Context) error
	Close()
}

// KafkaSink là io.Writer gửi mỗi lần Write (một dòng log JSON) thành một record của topic.
// Write luôn trả thành công ngay: buffer đầy hoặc gửi lỗi thì dòng log bị bỏ và Dropped tăng.
type KafkaSink struct {
	p       Producer
	topic   string
	dropped atomic.Int64
}

func NewKafkaSink(p Producer, topic string) *KafkaSink {
	return &KafkaSink{p: p, topic: topic}
}

// DialKafka tạo KafkaSink ghi vào app.logs bằng một client franz-go riêng: buffer MaxBuffered dòng,
// acks=leader, không idempotent (log mất vài dòng chấp nhận được, đổi lại không chờ ISR). Client
// kết nối lười nên Kafka chưa sẵn sàng lúc khởi động không làm service lỗi.
func DialKafka(brokers []string) (*KafkaSink, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.MaxBufferedRecords(MaxBuffered),
		kgo.RecordDeliveryTimeout(DeliveryTimeout),
		kgo.RequiredAcks(kgo.LeaderAck()),
		kgo.DisableIdempotentWrite(),
		kgo.ProducerLinger(50*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka log sink: %w", err)
	}
	return NewKafkaSink(cl, events.TopicAppLogs), nil
}

func (s *KafkaSink) Write(p []byte) (int, error) {
	// slog tái sử dụng buffer sau Write nên phải chép; bỏ "\n" cuối dòng.
	val := bytes.Clone(bytes.TrimRight(p, "\n"))
	s.p.TryProduce(context.Background(), &kgo.Record{Topic: s.topic, Value: val}, func(_ *kgo.Record, err error) {
		if err != nil {
			s.dropped.Add(1)
		}
	})
	return len(p), nil
}

// Dropped: số dòng log đã bỏ (buffer đầy, Kafka lỗi hoặc sink đã đóng).
func (s *KafkaSink) Dropped() int64 { return s.dropped.Load() }

// Close gửi nốt các dòng đang chờ (tối đa FlushTimeout) rồi đóng client.
func (s *KafkaSink) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), FlushTimeout)
	defer cancel()
	_ = s.p.Flush(ctx)
	s.p.Close()
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal; go vet ./internal/logging/ ./internal/events/ && go test -count=1 ./internal/logging/ ./internal/events/`
Expected: không file nào cần gofmt; `ok  streamfilm/internal/logging` (~3 giây: `TestKafkaSinkNeverBlocksWhenKafkaDown` chờ `Flush` hết `FlushTimeout` 2 giây vì broker không tồn tại), `ok  streamfilm/internal/events`.

- [ ] **Step 5: Commit**

```bash
git add internal/logging internal/events/events.go
git commit -m "feat(logging): JSON slog logger with request_id and non-blocking app.logs sink"
```

---

### Task 2: Nối logging vào `api` và `transcoder`; `video_id` trong log request

**Files:**
- Create: `internal/httpx/httpx_test.go`
- Modify: `internal/httpx/httpx.go`, `cmd/api/main.go`, `cmd/transcoder/main.go`, `cmd/transcoder/worker.go`, `internal/catalog/viewer.go`, `internal/catalog/handler.go`, `internal/catalog/subtitles.go`, `internal/playback/handler.go`

**Interfaces:**
- Consumes: `logging.New`, `logging.DialKafka`, `(*KafkaSink).Close/Dropped` (Task 1); `chi.URLParam`, `middleware.RequestID`; `uuid.Parse`.
- Produces:
  ```go
  // internal/httpx — chữ ký không đổi, dòng "http request" thêm "video_id" khi {id} là UUID
  func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler
  // cmd/api: run() tạo logSink := logging.DialKafka(cfg.KafkaBrokers) (defer đầu tiên → đóng sau cùng),
  //          log = logging.New("api", os.Stdout, logSink); slog.SetDefault(log)
  // cmd/transcoder: runWorker() tạo sink tương tự, slog.SetDefault(logging.New("transcoder", os.Stdout, logSink))
  //          trước khi tạo consumer/worker (chúng dùng slog.Default())
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/httpx/httpx_test.go` (mới):

```go
package httpx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// RequestLogger: request_id lấy từ header X-Request-Id (middleware.RequestID), video_id từ {id} khi là
// UUID (kể cả route con), không có video_id khi {id} không phải UUID hoặc route không có {id}.
func TestRequestLoggerAddsRequestAndVideoID(t *testing.T) {
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(middleware.RequestID, RequestLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	r.Route("/api/videos", func(r chi.Router) {
		r.Get("/{id}/play", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	r.Get("/api/genres", func(w http.ResponseWriter, _ *http.Request) {})

	const id = "0b0c4c1e-8f4a-4a52-9a52-6f1f2b0c9d11"
	for _, path := range []string{"/api/videos/" + strings.ToUpper(id) + "/play", "/api/videos/abc/play", "/api/genres"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Request-Id", "e2e-req-1")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d: %s", len(lines), buf.String())
	}
	want := []any{id, nil, nil}
	for i, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		if m["request_id"] != "e2e-req-1" || m["msg"] != "http request" {
			t.Errorf("line %d = %v", i, m)
		}
		if m["video_id"] != want[i] {
			t.Errorf("line %d video_id = %v, want %v", i, m["video_id"], want[i])
		}
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/httpx/`
Expected: FAIL — `line 0 video_id = <nil>, want 0b0c4c1e-8f4a-4a52-9a52-6f1f2b0c9d11`.

- [ ] **Step 3: Cài đặt**

`internal/httpx/httpx.go` — thay:

```go

	"github.com/go-chi/chi/v5/middleware"
)
```

bằng:

```go

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
)
```

`internal/httpx/httpx.go` — thay:

```go

// RequestLogger log mỗi request kèm request_id (từ middleware.RequestID); bỏ qua /healthz.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
```

bằng:

```go

// RequestLogger log mỗi request kèm request_id (từ middleware.RequestID) và video_id khi route có
// tham số {id} là UUID (M6: lọc log theo video trong ClickHouse); bỏ qua /healthz.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
```

`internal/httpx/httpx.go` — thay:

```go
			next.ServeHTTP(ww, r)
			log.Info("http request", "request_id", middleware.GetReqID(r.Context()),
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds())
		})
```

bằng:

```go
			next.ServeHTTP(ww, r)
			attrs := []any{"request_id", middleware.GetReqID(r.Context()),
				"method", r.Method, "path", r.URL.Path, "status", ww.Status(),
				"duration_ms", time.Since(start).Milliseconds()}
			// chi điền URLParams vào RouteContext trong lúc định tuyến, nên đọc được sau next.
			if id, err := uuid.Parse(chi.URLParam(r, "id")); err == nil {
				attrs = append(attrs, "video_id", id.String())
			}
			log.Info("http request", attrs...)
		})
```

`cmd/api/main.go` — thay:

```go
	"streamfilm/internal/events"
	"streamfilm/internal/playback"
```

bằng:

```go
	"streamfilm/internal/events"
	"streamfilm/internal/logging"
	"streamfilm/internal/playback"
```

`cmd/api/main.go` — thay:

```go
func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	slog.SetDefault(log)
```

bằng:

```go
func main() {
	// Lệnh phụ (seed-admin, sign) chỉ log ra stdout; server (run) tee thêm vào app.logs.
	log := logging.New("api", os.Stdout, nil)
	slog.SetDefault(log)
```

`cmd/api/main.go` — thay:

```go
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
```

bằng:

```go
	}
	// M6: log ra stdout và đồng thời vào app.logs (spec §7). Defer đầu tiên nên sink đóng sau cùng,
	// gom được cả log lúc tắt; Kafka chậm/chết thì dòng log bị bỏ, không chặn request.
	logSink, err := logging.DialKafka(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer func() {
		logSink.Close()
		logging.New("api", os.Stdout, nil).Info("app.logs sink closed", "dropped", logSink.Dropped())
	}()
	log = logging.New("api", os.Stdout, logSink)
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
```

`cmd/transcoder/main.go` — thay:

```go
	"streamfilm/internal/config"
	"streamfilm/internal/storage"
```

bằng:

```go
	"streamfilm/internal/config"
	"streamfilm/internal/logging"
	"streamfilm/internal/storage"
```

`cmd/transcoder/main.go` — thay:

```go
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "transcoder")
	slog.SetDefault(logger)
	if len(os.Args) < 2 {
```

bằng:

```go
func main() {
	// Lệnh file chỉ log ra stdout; worker tee thêm vào app.logs (runWorker).
	slog.SetDefault(logging.New("transcoder", os.Stdout, nil))
	if len(os.Args) < 2 {
```

`cmd/transcoder/worker.go` — thay:

```go
	"log/slog"
	"time"
```

bằng:

```go
	"log/slog"
	"os"
	"time"
```

`cmd/transcoder/worker.go` — thay:

```go
	"streamfilm/internal/events"
	"streamfilm/internal/transcode"
```

bằng:

```go
	"streamfilm/internal/events"
	"streamfilm/internal/logging"
	"streamfilm/internal/transcode"
```

`cmd/transcoder/worker.go` — thay:

```go
func runWorker(ctx context.Context, cfg config.Config) error {
	producer, err := events.NewProducer(cfg.KafkaBrokers)
```

bằng:

```go
func runWorker(ctx context.Context, cfg config.Config) error {
	// M6: log ra stdout và app.logs; đặt trước khi tạo consumer/worker vì chúng dùng slog.Default().
	logSink, err := logging.DialKafka(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer func() {
		logSink.Close()
		logging.New("transcoder", os.Stdout, nil).Info("app.logs sink closed", "dropped", logSink.Dropped())
	}()
	slog.SetDefault(logging.New("transcoder", os.Stdout, logSink))

	producer, err := events.NewProducer(cfg.KafkaBrokers)
```

Log trong handler có `r` chuyển sang biến thể `…Context(r.Context(), …)` để `contextHandler` gắn `request_id` (chạy ở thư mục gốc repo; mỗi lệnh chỉ đổi đúng các dòng có thông điệp liệt kê):

```bash
perl -pi -e 's/h\.Log\.(Error|Warn)\("(search videos|list genres|get video|get renditions|sign play url|list subtitles|resume_at|continue watching|continue watching videos)"/h.Log.$1Context(r.Context(), "$2"/' internal/catalog/viewer.go
perl -pi -e 's/h\.Log\.Error\("(list videos|get video|get renditions)"/h.Log.ErrorContext(r.Context(), "$1"/' internal/catalog/handler.go
perl -pi -e 's/h\.Log\.Error\("(get video|save progress)"/h.Log.ErrorContext(r.Context(), "$1"/' internal/playback/handler.go
perl -pi -e 's/h\.Log\.Info\("subtitle uploaded"/h.Log.InfoContext(r.Context(), "subtitle uploaded"/' internal/catalog/subtitles.go
```

Run: `grep -c 'h\.Log\.[A-Za-z]*Context(r\.Context()' internal/catalog/viewer.go internal/catalog/handler.go internal/playback/handler.go internal/catalog/subtitles.go`
Expected:

```
internal/catalog/viewer.go:9
internal/catalog/handler.go:3
internal/playback/handler.go:2
internal/catalog/subtitles.go:1
```

Các log không có `r` trong phạm vi (`toViewer`, `signFile`, `SubtitleHandler.fail`, consumer, job nền) giữ nguyên: không thuộc request nào hoặc dòng `http request` cùng request đã có `request_id`.

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal cmd; go vet ./... && go test -short -count=1 ./internal/httpx/ ./cmd/... ./internal/catalog/ ./internal/playback/`
Expected: không file nào cần gofmt; `ok` cho `internal/httpx`, `cmd/api`, `internal/catalog`, `internal/playback`; `cmd/transcoder` `[no test files]` (biên dịch được).

Run: `git diff --stat go.mod go.sum`
Expected: rỗng (chi, uuid đã có trong `go.mod`).

- [ ] **Step 5: Commit**

```bash
git add internal/httpx cmd/api/main.go cmd/transcoder internal/catalog internal/playback/handler.go
git commit -m "feat(logging): tee api and transcoder logs to app.logs with request_id and video_id"
```

---

### Task 3: ClickHouse — client HTTP, schema idempotent, cấu hình

**Files:**
- Create: `internal/analytics/clickhouse.go`, `internal/analytics/schema.go`, `internal/analytics/clickhouse_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `internal/testutil/containers.go`, `.env.example`

**Interfaces:**
- Consumes: `testcontainers.Run`, `WithExposedPorts`, `WithEnv`, `WithWaitStrategy`, `wait.ForHTTP`, `(*DockerContainer).PortEndpoint` (testcontainers-go v0.45.0).
- Produces:
  ```go
  // internal/config
  type Config struct { /* … */ ClickHouseURL, ClickHouseDB, ClickHouseUser, ClickHousePassword string }

  // internal/testutil
  const ClickHouseImage = "clickhouse/clickhouse-server:24.8"
  type ClickHouse struct{ URL, DB, User, Password string }
  func StartClickHouse(t *testing.T) ClickHouse   // db/user/password = streamfilm

  // internal/analytics
  type Client struct{ /* base, db, user, password string; hc *http.Client */ }
  func NewClient(base, db, user, password string) *Client
  func (c *Client) Exec(ctx context.Context, query string) error
  func (c *Client) Query(ctx context.Context, query string) ([]byte, error)
  func (c *Client) Insert(ctx context.Context, table string, body []byte, token string) error
  const (
  	TablePlaybackEvents = "playback_events"
  	TableAppLogs        = "app_logs"
  )
  var Schema []string
  func Migrate(ctx context.Context, c *Client) error
  // test helpers (clickhouse_test.go), dùng lại ở Task 6, 7:
  func startClickHouse(t *testing.T) *Client            // StartClickHouse + Migrate hai lần
  func queryString(t *testing.T, c *Client, q string) string
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

Integration test kiểm tra đúng kiểu cột, engine/TTL của `app_logs`, `Migrate` chạy hai lần, khử trùng theo `insert_deduplication_token`, lỗi trả về khi row sai kiểu và khi sai mật khẩu.

`internal/config/config_test.go` — thay:

```go
		t.Error("COOKIE_SECURE sai định dạng phải về false")
	}
}
```

bằng:

```go
		t.Error("COOKIE_SECURE sai định dạng phải về false")
	}
}

func TestLoadM6ClickHouse(t *testing.T) {
	for _, k := range []string{"CLICKHOUSE_URL", "CLICKHOUSE_DB", "CLICKHOUSE_USER", "CLICKHOUSE_PASSWORD"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.ClickHouseURL != "http://localhost:8123" || c.ClickHouseDB != "streamfilm" ||
		c.ClickHouseUser != "streamfilm" || c.ClickHousePassword != "streamfilm" {
		t.Errorf("defaults = %q %q %q %q", c.ClickHouseURL, c.ClickHouseDB, c.ClickHouseUser, c.ClickHousePassword)
	}
	t.Setenv("CLICKHOUSE_URL", "http://clickhouse:8123")
	t.Setenv("CLICKHOUSE_PASSWORD", "secret")
	c = Load()
	if c.ClickHouseURL != "http://clickhouse:8123" || c.ClickHousePassword != "secret" {
		t.Errorf("override = %q %q", c.ClickHouseURL, c.ClickHousePassword)
	}
}
```

`internal/testutil/containers.go` — thay:

```go
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
```

bằng:

```go
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
```

`internal/testutil/containers.go` — thay:

```go
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
```

bằng:

```go
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
```

`internal/testutil/containers.go` — thay:

```go
	return MinIO{Endpoint: "http://" + addr, User: ctr.Username, Password: ctr.Password}
}
```

bằng:

```go
	return MinIO{Endpoint: "http://" + addr, User: ctr.Username, Password: ctr.Password}
}

// ClickHouseImage: cùng image với service clickhouse trong deploy/docker-compose.yml.
const ClickHouseImage = "clickhouse/clickhouse-server:24.8"

// ClickHouse: thông tin kết nối HTTP interface của container test.
type ClickHouse struct {
	URL, DB, User, Password string
}

// StartClickHouse chạy ClickHouse (database streamfilm, user streamfilm/streamfilm) và trả URL HTTP.
// Skip khi -short hoặc không có Docker.
func StartClickHouse(t *testing.T) ClickHouse {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	ctr, err := testcontainers.Run(ctx, ClickHouseImage,
		testcontainers.WithExposedPorts("8123/tcp"),
		testcontainers.WithEnv(map[string]string{
			"CLICKHOUSE_DB":                        "streamfilm",
			"CLICKHOUSE_USER":                      "streamfilm",
			"CLICKHOUSE_PASSWORD":                  "streamfilm",
			"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT": "1",
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/ping").WithPort("8123/tcp").
			WithStartupTimeout(2*time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "8123/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	return ClickHouse{URL: endpoint, DB: "streamfilm", User: "streamfilm", Password: "streamfilm"}
}
```

`internal/analytics/clickhouse_test.go` (mới):

```go
package analytics

import (
	"context"
	"strings"
	"testing"

	"streamfilm/internal/testutil"
)

func startClickHouse(t *testing.T) *Client {
	t.Helper()
	ch := testutil.StartClickHouse(t)
	c := NewClient(ch.URL, ch.DB, ch.User, ch.Password)
	// Chạy hai lần: Migrate phải idempotent (analytics chạy nó mỗi lần khởi động).
	for range 2 {
		if err := Migrate(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func queryString(t *testing.T, c *Client, q string) string {
	t.Helper()
	out, err := c.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestClickHouseSchemaInsertAndDedup(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()

	got := queryString(t, c, "SELECT name, type FROM system.columns WHERE database = currentDatabase() "+
		"AND table = 'playback_events' ORDER BY position FORMAT CSV")
	want := `"ts","DateTime64(3, 'UTC')"
"user_id","UUID"
"video_id","UUID"
"type","LowCardinality(String)"
"position_s","UInt32"
"bitrate_kbps","UInt32"
"buffering_ms","UInt32"`
	if got != want {
		t.Errorf("playback_events columns:\n%s", got)
	}
	if got := queryString(t, c, "SELECT engine_full FROM system.tables WHERE database = currentDatabase() "+
		"AND name = 'app_logs'"); !strings.Contains(got, "ORDER BY (service, ts) TTL toDateTime(ts) + toIntervalDay(30)") {
		t.Errorf("app_logs engine = %s", got)
	}

	body := []byte(`{"ts":"2026-11-15 10:00:00.123","user_id":"11111111-1111-1111-1111-111111111111","video_id":"22222222-2222-2222-2222-222222222222","type":"play","position_s":0,"bitrate_kbps":2800,"buffering_ms":0}
{"ts":"2026-11-15 10:00:10.000","user_id":"11111111-1111-1111-1111-111111111111","video_id":"22222222-2222-2222-2222-222222222222","type":"heartbeat","position_s":10,"bitrate_kbps":2800,"buffering_ms":0}
`)
	// Cùng token gửi hai lần (retry sau timeout) → chỉ ghi một lần; token khác → ghi thêm.
	for _, token := range []string{"t1", "t1", "t2"} {
		if err := c.Insert(ctx, TablePlaybackEvents, body, token); err != nil {
			t.Fatal(err)
		}
	}
	if got := queryString(t, c, "SELECT count() FROM playback_events"); got != "4" {
		t.Errorf("count = %s, want 4 (t1 dedup, t2 mới)", got)
	}
	if got := queryString(t, c, "SELECT toString(max(ts)) FROM playback_events"); got != "2026-11-15 10:00:10.000" {
		t.Errorf("max(ts) = %s", got)
	}

	// Một row sai kiểu làm hỏng cả lô: lỗi trả về có mã HTTP và thông điệp của ClickHouse.
	err := c.Insert(ctx, TablePlaybackEvents, []byte(`{"ts":"2026-11-15 10:00:00.000","user_id":"not-a-uuid"}`+"\n"), "")
	if err == nil || !strings.Contains(err.Error(), "clickhouse: HTTP ") {
		t.Errorf("insert sai kiểu: err = %v", err)
	}
	if err := NewClient(c.base, c.db, c.user, "wrong").Exec(ctx, "SELECT 1"); err == nil {
		t.Error("sai mật khẩu phải lỗi")
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test ./internal/config/ ./internal/analytics/`
Expected: FAIL — `c.ClickHouseURL undefined (type Config has no field or method ClickHouseURL)` (và `undefined: NewClient`, `undefined: Migrate` trong `internal/analytics`).

- [ ] **Step 3: Cài đặt**

`internal/config/config.go` — thay:

```go
	CookieSecure                                                     bool
}
```

bằng:

```go
	CookieSecure                                                     bool
	// M6: analytics ghi vào ClickHouse qua HTTP interface (cổng 8123).
	ClickHouseURL, ClickHouseDB, ClickHouseUser, ClickHousePassword string
}
```

`internal/config/config.go` — thay:

```go
		CookieSecure: getBool("COOKIE_SECURE", false),
	}
```

bằng:

```go
		CookieSecure: getBool("COOKIE_SECURE", false),
		// Mặc định khớp service clickhouse trong compose (mật khẩu = POSTGRES_PASSWORD của .env.example).
		ClickHouseURL:      get("CLICKHOUSE_URL", "http://localhost:8123"),
		ClickHouseDB:       get("CLICKHOUSE_DB", "streamfilm"),
		ClickHouseUser:     get("CLICKHOUSE_USER", "streamfilm"),
		ClickHousePassword: get("CLICKHOUSE_PASSWORD", "streamfilm"),
	}
```

`.env.example` — thay:

```bash
ADMIN_PASSWORD=admin-dev-password
```

bằng:

```bash
ADMIN_PASSWORD=admin-dev-password

# M6: analytics (make run-analytics) và make analytics-sql đọc ClickHouse qua HTTP. Mật khẩu phải bằng
# POSTGRES_PASSWORD (compose đặt CLICKHOUSE_PASSWORD của service clickhouse từ biến đó).
CLICKHOUSE_URL=http://localhost:8123
CLICKHOUSE_DB=streamfilm
CLICKHOUSE_USER=streamfilm
CLICKHOUSE_PASSWORD=streamfilm
```

`internal/analytics/clickhouse.go` (mới):

```go
// Package analytics đưa playback.events và app.logs từ Kafka vào ClickHouse theo lô (spec §3.1, §4.4).
package analytics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client nói chuyện với ClickHouse qua HTTP interface (cổng 8123): mỗi câu lệnh là một POST,
// dữ liệu insert gửi dạng JSONEachRow. Không cần driver native (ADR 0016).
type Client struct {
	base, db, user, password string
	hc                       *http.Client
}

// NewClient: base ví dụ "http://clickhouse:8123"; db là database mặc định của mọi câu lệnh.
func NewClient(base, db, user, password string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), db: db, user: user, password: password,
		hc: &http.Client{Timeout: 30 * time.Second}}
}

// Exec chạy một câu lệnh không cần kết quả (DDL).
func (c *Client) Exec(ctx context.Context, query string) error {
	_, err := c.do(ctx, url.Values{}, strings.NewReader(query))
	return err
}

// Query chạy một câu SELECT và trả body thô (định dạng theo mệnh đề FORMAT trong câu, mặc định TSV).
func (c *Client) Query(ctx context.Context, query string) ([]byte, error) {
	return c.do(ctx, url.Values{}, strings.NewReader(query))
}

// Insert ghi body (các dòng JSON, mỗi dòng một row) vào table. token khác rỗng được gửi làm
// insert_deduplication_token: gửi lại đúng lô đó (cùng token) thì ClickHouse bỏ qua, nhờ bảng có
// non_replicated_deduplication_window (schema.go).
func (c *Client) Insert(ctx context.Context, table string, body []byte, token string) error {
	q := url.Values{"query": {"INSERT INTO " + table + " FORMAT JSONEachRow"}}
	if token != "" {
		q.Set("insert_deduplication_token", token)
	}
	_, err := c.do(ctx, q, bytes.NewReader(body))
	return err
}

func (c *Client) do(ctx context.Context, q url.Values, body io.Reader) ([]byte, error) {
	q.Set("database", c.db)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/?"+q.Encode(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-ClickHouse-User", c.user)
	req.Header.Set("X-ClickHouse-Key", c.password)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clickhouse: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return nil, fmt.Errorf("clickhouse: HTTP %d: %s", resp.StatusCode, msg)
	}
	return out, nil
}
```

`internal/analytics/schema.go` (mới):

```go
package analytics

import (
	"context"
	"fmt"
)

// Tên bảng ClickHouse (spec §5).
const (
	TablePlaybackEvents = "playback_events"
	TableAppLogs        = "app_logs"
)

// Schema: các câu tạo bảng, idempotent (IF NOT EXISTS), chạy mỗi lần analytics khởi động (ADR 0016).
// ts là DateTime64(3, 'UTC'): analytics luôn gửi giờ UTC dạng "YYYY-MM-DD hh:mm:ss.sss".
// non_replicated_deduplication_window: nhớ hash của 1000 lô gần nhất để lần gửi lại cùng
// insert_deduplication_token (retry sau timeout) không ghi trùng (ADR 0017).
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS playback_events (
	ts DateTime64(3, 'UTC'),
	user_id UUID,
	video_id UUID,
	type LowCardinality(String),
	position_s UInt32,
	bitrate_kbps UInt32,
	buffering_ms UInt32
) ENGINE = MergeTree
ORDER BY (video_id, ts)
SETTINGS non_replicated_deduplication_window = 1000`,
	`CREATE TABLE IF NOT EXISTS app_logs (
	ts DateTime64(3, 'UTC'),
	service LowCardinality(String),
	level LowCardinality(String),
	msg String,
	request_id String,
	video_id String,
	attrs String
) ENGINE = MergeTree
ORDER BY (service, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY
SETTINGS non_replicated_deduplication_window = 1000`,
}

// Migrate tạo các bảng nếu chưa có.
func Migrate(ctx context.Context, c *Client) error {
	for _, q := range Schema {
		if err := c.Exec(ctx, q); err != nil {
			return fmt.Errorf("migrate clickhouse: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal; go vet ./internal/config/ ./internal/analytics/ ./internal/testutil/ && go test -count=1 ./internal/config/ ./internal/analytics/`
Expected: `ok  streamfilm/internal/config`, `ok  streamfilm/internal/analytics` (~15 giây, kéo image `clickhouse/clickhouse-server:24.8` lần đầu).

Run: `go test -short ./internal/analytics/`
Expected: `ok` (test ClickHouse bị skip).

- [ ] **Step 5: Commit**

```bash
git add internal/analytics internal/config internal/testutil/containers.go .env.example
git commit -m "feat(analytics): ClickHouse HTTP client and idempotent schema"
```

---

### Task 4: Ánh xạ record Kafka → row ClickHouse

**Files:**
- Create: `internal/analytics/rows.go`, `internal/analytics/rows_test.go`

**Interfaces:**
- Consumes: `events.Decode[events.PlaybackEvent]`, `events.PlaybackEvent` (M5), `uuid.Parse`, `kgo.Record{Value, Timestamp}`.
- Produces:
  ```go
  var ErrInvalid = errors.New("invalid record")
  type CHTime time.Time
  func (t CHTime) MarshalJSON() ([]byte, error)        // "2006-01-02 15:04:05.000" UTC
  func CHTimeString(t time.Time) string
  type PlaybackRow struct {
  	TS CHTime `json:"ts"`; UserID string `json:"user_id"`; VideoID string `json:"video_id"`; Type string `json:"type"`
  	PositionS uint32 `json:"position_s"`; BitrateKbps uint32 `json:"bitrate_kbps"`; BufferingMs uint32 `json:"buffering_ms"`
  }
  type LogRow struct {
  	TS CHTime `json:"ts"`; Service, Level, Msg, RequestID, VideoID, Attrs string  // json: service, level, msg, request_id, video_id, attrs
  }
  type Mapper func(rec *kgo.Record) ([]byte, error)   // một dòng JSONEachRow, không "\n"
  func MapPlayback(rec *kgo.Record) ([]byte, error)
  func MapLog(rec *kgo.Record) ([]byte, error)
  func ParsePlayback(data []byte) (PlaybackRow, error)
  func ParseLog(data []byte, fallback time.Time) (LogRow, error)
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

`internal/analytics/rows_test.go` (mới):

```go
package analytics

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	userID  = "11111111-1111-1111-1111-111111111111"
	videoID = "22222222-2222-2222-2222-222222222222"
)

func TestCHTimeIsUTCMillis(t *testing.T) {
	loc := time.FixedZone("ICT", 7*3600)
	b, err := json.Marshal(CHTime(time.Date(2026, 11, 15, 17, 4, 5, 123456789, loc)))
	if err != nil || string(b) != `"2026-11-15 10:04:05.123"` {
		t.Errorf("CHTime = %s, %v", b, err)
	}
}

func TestMapPlayback(t *testing.T) {
	rec := &kgo.Record{Value: []byte(`{"ts":"2026-11-15T17:00:00.5+07:00","user_id":"` + userID +
		`","video_id":"22222222-2222-2222-2222-22222222222A","type":"buffering","position_s":12,"bitrate_kbps":2800,"buffering_ms":1500}`)}
	line, err := MapPlayback(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-11-15 10:00:00.500","user_id":"` + userID + `","video_id":"22222222-2222-2222-2222-22222222222a",` +
		`"type":"buffering","position_s":12,"bitrate_kbps":2800,"buffering_ms":1500}`
	if string(line) != want {
		t.Errorf("line =\n%s\nwant\n%s", line, want)
	}
}

func TestParsePlaybackRejectsInvalid(t *testing.T) {
	ok := func(field string) string {
		m := map[string]any{"ts": "2026-11-15T10:00:00Z", "user_id": userID, "video_id": videoID, "type": "play",
			"position_s": 1, "bitrate_kbps": 1, "buffering_ms": 1}
		switch field {
		case "user_id", "video_id":
			m[field] = "abc"
		case "ts", "type":
			delete(m, field)
		case "position_s", "bitrate_kbps", "buffering_ms":
			m[field] = -1
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	cases := map[string]string{
		"not json":     "not-json",
		"array":        "[]",
		"empty object": "{}",
		"bad user_id":  ok("user_id"),
		"bad video_id": ok("video_id"),
		"missing ts":   ok("ts"),
		"missing type": ok("type"),
		"neg position": ok("position_s"),
		"neg bitrate":  ok("bitrate_kbps"),
		"neg buffer":   ok("buffering_ms"),
		"float pos":    `{"ts":"2026-11-15T10:00:00Z","user_id":"` + userID + `","video_id":"` + videoID + `","type":"play","position_s":1.5}`,
	}
	for name, in := range cases {
		if _, err := ParsePlayback([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ParsePlayback([]byte(ok(""))); err != nil {
		t.Errorf("valid: %v", err)
	}
}

func TestMapLog(t *testing.T) {
	fallback := time.Date(2026, 11, 15, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		name, in string
		want     LogRow
	}{
		{"http request",
			`{"time":"2026-11-15T17:00:00.123456+07:00","level":"INFO","msg":"http request","service":"api",` +
				`"request_id":"host/abc-000001","method":"GET","path":"/api/videos","status":200,"duration_ms":3,"video_id":"` + videoID + `"}`,
			LogRow{TS: CHTime(time.Date(2026, 11, 15, 10, 0, 0, 123456000, time.UTC)), Service: "api", Level: "INFO",
				Msg: "http request", RequestID: "host/abc-000001", VideoID: videoID,
				Attrs: `{"duration_ms":3,"method":"GET","path":"/api/videos","status":200}`}},
		{"no time, numeric video_id, nested group",
			`{"level":"ERROR","msg":"x","service":"transcoder","video_id":42,"err":{"code":1.50}}`,
			LogRow{TS: CHTime(fallback), Service: "transcoder", Level: "ERROR", Msg: "x", VideoID: "42",
				Attrs: `{"err":{"code":1.50}}`}},
		{"bad time, null request_id",
			`{"time":"yesterday","msg":"y","request_id":null}`,
			LogRow{TS: CHTime(fallback), Msg: "y", Attrs: `{}`}},
	}
	for _, c := range cases {
		got, err := ParseLog([]byte(c.in), fallback)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !time.Time(got.TS).Equal(time.Time(c.want.TS)) {
			t.Errorf("%s: ts = %v, want %v", c.name, time.Time(got.TS), time.Time(c.want.TS))
		}
		got.TS, c.want.TS = CHTime{}, CHTime{}
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	for _, in := range []string{"not-json", "null", `"str"`, `[1]`} {
		if _, err := ParseLog([]byte(in), fallback); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", in, err)
		}
	}
	line, err := MapLog(&kgo.Record{Value: []byte(`{"msg":"z","service":"api"}`), Timestamp: fallback})
	if err != nil || string(line) != `{"ts":"2026-11-15 01:02:03.000","service":"api","level":"","msg":"z","request_id":"","video_id":"","attrs":"{}"}` {
		t.Errorf("MapLog = %s, %v", line, err)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/analytics/`
Expected: FAIL — `undefined: CHTime`, `undefined: MapPlayback`, `undefined: ParsePlayback`, `undefined: ErrInvalid`, `undefined: LogRow`…

- [ ] **Step 3: Cài đặt**

`internal/analytics/rows.go` (mới):

```go
package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

// ErrInvalid: record không ghi được (JSON hỏng, thiếu trường, sai kiểu). Pipeline bỏ qua và đếm,
// vì một row sai kiểu làm ClickHouse từ chối cả lô.
var ErrInvalid = errors.New("invalid record")

// CHTime mã hoá thời điểm theo dạng DateTime64(3) mà ClickHouse đọc được ở chế độ mặc định
// (date_time_input_format=basic): "YYYY-MM-DD hh:mm:ss.sss", luôn UTC.
type CHTime time.Time

const chTimeLayout = "2006-01-02 15:04:05.000"

func (t CHTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + CHTimeString(time.Time(t)) + `"`), nil
}

// CHTimeString: t theo giờ UTC, dạng "2006-01-02 15:04:05.000".
func CHTimeString(t time.Time) string { return t.UTC().Format(chTimeLayout) }

// PlaybackRow: một dòng của bảng playback_events.
type PlaybackRow struct {
	TS          CHTime `json:"ts"`
	UserID      string `json:"user_id"`
	VideoID     string `json:"video_id"`
	Type        string `json:"type"`
	PositionS   uint32 `json:"position_s"`
	BitrateKbps uint32 `json:"bitrate_kbps"`
	BufferingMs uint32 `json:"buffering_ms"`
}

// LogRow: một dòng của bảng app_logs. Attrs là JSON object của các trường còn lại.
type LogRow struct {
	TS        CHTime `json:"ts"`
	Service   string `json:"service"`
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	RequestID string `json:"request_id"`
	VideoID   string `json:"video_id"`
	Attrs     string `json:"attrs"`
}

// Mapper biến một record Kafka thành một dòng JSONEachRow (không có "\n"); lỗi bọc ErrInvalid.
type Mapper func(rec *kgo.Record) ([]byte, error)

// MapPlayback: Mapper cho playback.events.
func MapPlayback(rec *kgo.Record) ([]byte, error) {
	row, err := ParsePlayback(rec.Value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(row)
}

// MapLog: Mapper cho app.logs; dòng log không có "time" hợp lệ thì lấy timestamp của record.
func MapLog(rec *kgo.Record) ([]byte, error) {
	row, err := ParseLog(rec.Value, rec.Timestamp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(row)
}

// ParsePlayback kiểm tra một events.PlaybackEvent: user_id và video_id là UUID (chuẩn hoá chữ thường),
// có ts và type, các số nằm trong UInt32.
func ParsePlayback(data []byte) (PlaybackRow, error) {
	ev, err := events.Decode[events.PlaybackEvent](data)
	if err != nil {
		return PlaybackRow{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	user, err := uuid.Parse(ev.UserID)
	if err != nil {
		return PlaybackRow{}, fmt.Errorf("%w: user_id %q", ErrInvalid, ev.UserID)
	}
	video, err := uuid.Parse(ev.VideoID)
	if err != nil {
		return PlaybackRow{}, fmt.Errorf("%w: video_id %q", ErrInvalid, ev.VideoID)
	}
	if ev.TS.IsZero() || ev.Type == "" {
		return PlaybackRow{}, fmt.Errorf("%w: missing ts or type", ErrInvalid)
	}
	nums := [...]int{ev.PositionS, ev.BitrateKbps, ev.BufferingMs}
	for _, n := range nums {
		if n < 0 || n > math.MaxUint32 {
			return PlaybackRow{}, fmt.Errorf("%w: number out of UInt32 range: %d", ErrInvalid, n)
		}
	}
	return PlaybackRow{TS: CHTime(ev.TS), UserID: user.String(), VideoID: video.String(), Type: ev.Type,
		PositionS: uint32(nums[0]), BitrateKbps: uint32(nums[1]), BufferingMs: uint32(nums[2])}, nil
}

// ParseLog đọc một dòng log JSON của slog: "time", "level", "msg", "service", "request_id", "video_id"
// thành cột riêng, phần còn lại thành attrs (JSON, khoá sắp xếp). fallback dùng khi thiếu "time".
func ParseLog(data []byte, fallback time.Time) (LogRow, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // giữ nguyên số trong attrs (không đổi 123 thành 1.23e+02)
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return LogRow{}, fmt.Errorf("%w: log line is not a JSON object", ErrInvalid)
	}
	ts := fallback
	if s, ok := m["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			ts = t
		}
	}
	row := LogRow{TS: CHTime(ts), Level: take(m, "level"), Msg: take(m, "msg"), Service: take(m, "service"),
		RequestID: take(m, "request_id"), VideoID: take(m, "video_id")}
	delete(m, "time")
	attrs, err := json.Marshal(m)
	if err != nil {
		return LogRow{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	row.Attrs = string(attrs)
	return row, nil
}

// take lấy và xoá m[key] dưới dạng chuỗi: string giữ nguyên, thiếu/null là "", kiểu khác mã hoá JSON.
func take(m map[string]any, key string) string {
	v, ok := m[key]
	delete(m, key)
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		if !ok {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal; go vet ./internal/analytics/ && go test -short -count=1 ./internal/analytics/`
Expected: `ok  streamfilm/internal/analytics`.

- [ ] **Step 5: Commit**

```bash
git add internal/analytics/rows.go internal/analytics/rows_test.go
git commit -m "feat(analytics): map playback events and slog lines to ClickHouse rows"
```

---

### Task 5: `Pipeline` — gom lô, INSERT, thử lại, commit sau INSERT

**Files:**
- Create: `internal/analytics/pipeline.go`, `internal/analytics/pipeline_test.go`

**Interfaces:**
- Consumes: `Mapper`, `ErrInvalid` (Task 4); `*Client` thoả `Inserter` (Task 3); `events.DefaultBackoff`; `kgo.Fetches`, `kgo.NewErrFetch`, `(*kgo.Client).PollRecords/CommitRecords/AllowRebalance`.
- Produces:
  ```go
  const (
  	DefaultMaxBatch = 1000
  	DefaultMaxWait  = time.Second
  	InsertTimeout   = 10 * time.Second
  	ShutdownTimeout = 10 * time.Second
  )
  type Source interface {
  	PollRecords(ctx context.Context, maxPollRecords int) kgo.Fetches
  	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
  	AllowRebalance()
  }
  type Inserter interface {
  	Insert(ctx context.Context, table string, body []byte, token string) error
  }
  type Pipeline struct {
  	Source Source; Sink Inserter; Table string; Map Mapper
  	MaxBatch int; MaxWait time.Duration
  	Backoff func(attempt int) time.Duration
  	Log *slog.Logger
  	// inserted, skipped atomic.Int64
  }
  func NewPipeline(src Source, sink Inserter, table string, m Mapper, log *slog.Logger) *Pipeline
  func (p *Pipeline) Run(ctx context.Context) error     // nil khi ctx bị huỷ
  func (p *Pipeline) Inserted() int64
  func (p *Pipeline) Skipped() int64
  func DedupToken(table string, recs []*kgo.Record) string
  func DialSource(brokers []string, group, topic string) (*kgo.Client, error)
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

Test dùng `fakeSource` (trả record khi được `push`, chặn tới khi ctx hết hạn như `PollRecords` thật) và `fakeSink` (lỗi N lần đầu), ghi thứ tự thao tác vào `journal` để kiểm tra "INSERT rồi mới commit".

`internal/analytics/pipeline_test.go` (mới):

```go
package analytics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// journal ghi thứ tự các thao tác của fakeSource và fakeSink ("insert-fail", "insert", "commit 3").
type journal struct {
	mu  sync.Mutex
	ops []string
}

func (j *journal) add(op string) { j.mu.Lock(); j.ops = append(j.ops, op); j.mu.Unlock() }
func (j *journal) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.ops...)
}

type fakeSource struct {
	j         *journal
	mu        sync.Mutex
	buf       []*kgo.Record
	notify    chan struct{}
	committed []*kgo.Record
}

func newFakeSource(j *journal) *fakeSource {
	return &fakeSource{j: j, notify: make(chan struct{}, 1)}
}

// push thêm record (partition 0, offset tăng dần) để lần poll sau trả về.
func (s *fakeSource) push(values ...string) {
	s.mu.Lock()
	for _, v := range values {
		off := int64(len(s.committed) + len(s.buf))
		s.buf = append(s.buf, &kgo.Record{Topic: "t", Partition: 0, Offset: off, Value: []byte(v)})
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *fakeSource) PollRecords(ctx context.Context, max int) kgo.Fetches {
	for {
		s.mu.Lock()
		if len(s.buf) > 0 {
			n := min(max, len(s.buf))
			out := s.buf[:n]
			s.buf = s.buf[n:]
			s.mu.Unlock()
			var fs kgo.Fetches
			for _, r := range out {
				fs = append(fs, kgo.Fetch{Topics: []kgo.FetchTopic{{Topic: r.Topic,
					Partitions: []kgo.FetchPartition{{Partition: r.Partition, Records: []*kgo.Record{r}}}}}})
			}
			return fs
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-ctx.Done():
			return kgo.NewErrFetch(ctx.Err())
		}
	}
}

func (s *fakeSource) CommitRecords(_ context.Context, rs ...*kgo.Record) error {
	s.mu.Lock()
	s.committed = append(s.committed, rs...)
	s.mu.Unlock()
	s.j.add(fmt.Sprintf("commit %d", len(rs)))
	return nil
}

func (s *fakeSource) AllowRebalance() {}

func (s *fakeSource) commits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.committed)
}

// fakeSink lỗi fail lần đầu (fail < 0: lỗi mãi), rồi ghi lại body và token của mỗi lần thành công.
type fakeSink struct {
	j      *journal
	mu     sync.Mutex
	fail   int
	calls  int
	bodies []string
	tokens []string
}

func (k *fakeSink) Insert(_ context.Context, table string, body []byte, token string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls++
	if k.fail < 0 || k.calls <= k.fail {
		k.j.add("insert-fail")
		return errors.New("clickhouse down")
	}
	k.bodies = append(k.bodies, string(body))
	k.tokens = append(k.tokens, token)
	k.j.add("insert")
	return nil
}

func (k *fakeSink) snapshot() (calls int, bodies []string, tokens []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls, append([]string(nil), k.bodies...), append([]string(nil), k.tokens...)
}

// testMap: "bad" là record hỏng; còn lại giữ nguyên value làm một dòng.
func testMap(rec *kgo.Record) ([]byte, error) {
	if string(rec.Value) == "bad" {
		return nil, ErrInvalid
	}
	return rec.Value, nil
}

func newTestPipeline(maxBatch int, maxWait time.Duration, sinkFail int) (*Pipeline, *fakeSource, *fakeSink, *journal) {
	j := &journal{}
	src, sink := newFakeSource(j), &fakeSink{j: j, fail: sinkFail}
	p := NewPipeline(src, sink, "tbl", testMap, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.MaxBatch, p.MaxWait = maxBatch, maxWait
	p.Backoff = func(int) time.Duration { return time.Millisecond }
	return p, src, sink, j
}

func start(t *testing.T, p *Pipeline) (cancel func()) {
	t.Helper()
	ctx, cancelCtx := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	return func() {
		cancelCtx()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run không dừng sau khi huỷ ctx")
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout chờ: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPipelineFlushesAtMaxBatchAndOnShutdown(t *testing.T) {
	p, src, sink, j := newTestPipeline(3, time.Hour, 0)
	stop := start(t, p)
	src.push("a", "b", "c", "d", "e", "f", "g")
	eventually(t, "2 lô đầy được ghi và commit", func() bool { return src.commits() == 6 })
	stop() // "g" còn trong lô dở (MaxWait 1h): được ghi nốt rồi commit
	_, bodies, tokens := sink.snapshot()
	want := []string{"a\nb\nc\n", "d\ne\nf\n", "g\n"}
	if strings.Join(bodies, "|") != strings.Join(want, "|") {
		t.Errorf("bodies = %q", bodies)
	}
	if strings.Join(tokens, " ") != "tbl/t/0:0-2 tbl/t/0:3-5 tbl/t/0:6-6" {
		t.Errorf("tokens = %q", tokens)
	}
	if got := strings.Join(j.list(), ","); got != "insert,commit 3,insert,commit 3,insert,commit 1" {
		t.Errorf("ops = %s", got)
	}
	if p.Inserted() != 7 || p.Skipped() != 0 {
		t.Errorf("inserted/skipped = %d/%d", p.Inserted(), p.Skipped())
	}
}

func TestPipelineFlushesAfterMaxWait(t *testing.T) {
	p, src, sink, _ := newTestPipeline(1000, 50*time.Millisecond, 0)
	stop := start(t, p)
	defer stop()
	began := time.Now()
	src.push("a", "b")
	eventually(t, "lô theo thời gian", func() bool { _, b, _ := sink.snapshot(); return len(b) == 1 })
	if d := time.Since(began); d < 40*time.Millisecond {
		t.Errorf("ghi sau %v, phải chờ MaxWait", d)
	}
	eventually(t, "commit", func() bool { return src.commits() == 2 })
}

func TestPipelineRetriesThenCommitsAfterInsert(t *testing.T) {
	p, src, sink, j := newTestPipeline(2, time.Hour, 2)
	stop := start(t, p)
	src.push("a", "b")
	eventually(t, "commit", func() bool { return src.commits() == 2 })
	stop()
	calls, bodies, tokens := sink.snapshot()
	if calls != 3 || len(bodies) != 1 || tokens[0] != "tbl/t/0:0-1" {
		t.Errorf("calls=%d bodies=%q tokens=%q", calls, bodies, tokens)
	}
	if got := strings.Join(j.list(), ","); got != "insert-fail,insert-fail,insert,commit 2" {
		t.Errorf("ops = %s", got)
	}
}

func TestPipelineSkipsInvalidButCommitsThem(t *testing.T) {
	p, src, sink, _ := newTestPipeline(3, time.Hour, 0)
	stop := start(t, p)
	src.push("a", "bad", "b")
	eventually(t, "commit", func() bool { return src.commits() == 3 })
	src.push("bad") // lô chỉ có record hỏng: không INSERT, vẫn commit
	stop()
	calls, bodies, _ := sink.snapshot()
	if calls != 1 || bodies[0] != "a\nb\n" {
		t.Errorf("calls=%d bodies=%q", calls, bodies)
	}
	if src.commits() != 4 || p.Inserted() != 2 || p.Skipped() != 2 {
		t.Errorf("commits=%d inserted=%d skipped=%d", src.commits(), p.Inserted(), p.Skipped())
	}
}

func TestPipelineCancelWhileRetryingDoesNotCommit(t *testing.T) {
	p, src, sink, _ := newTestPipeline(1, time.Hour, -1)
	stop := start(t, p)
	src.push("a")
	eventually(t, "thử lại nhiều lần", func() bool { c, _, _ := sink.snapshot(); return c >= 3 })
	stop()
	if src.commits() != 0 || p.Inserted() != 0 {
		t.Errorf("commits=%d inserted=%d, want 0 (lô sẽ được đọc lại)", src.commits(), p.Inserted())
	}
}

func TestDedupTokenSortsPartitions(t *testing.T) {
	recs := []*kgo.Record{
		{Topic: "b", Partition: 1, Offset: 7}, {Topic: "a", Partition: 2, Offset: 5},
		{Topic: "a", Partition: 0, Offset: 9}, {Topic: "a", Partition: 2, Offset: 3},
	}
	if got := DedupToken("x", recs); got != "x/a/0:9-9/a/2:3-5/b/1:7-7" {
		t.Errorf("token = %s", got)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -short ./internal/analytics/`
Expected: FAIL — `undefined: Pipeline`, `undefined: NewPipeline`, `undefined: DedupToken`.

- [ ] **Step 3: Cài đặt**

`internal/analytics/pipeline.go` (mới):

```go
package analytics

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

// Mặc định theo spec §4.4: ghi một lô khi đủ 1000 record hoặc 1 giây sau record đầu tiên của lô.
const (
	DefaultMaxBatch = 1000
	DefaultMaxWait  = time.Second
	InsertTimeout   = 10 * time.Second // mỗi lần gọi INSERT
	ShutdownTimeout = 10 * time.Second // ghi nốt lô đang dở khi tắt
)

// Source là phần của *kgo.Client (consumer group, BlockRebalanceOnPoll, tắt auto-commit) mà Pipeline dùng.
type Source interface {
	PollRecords(ctx context.Context, maxPollRecords int) kgo.Fetches
	CommitRecords(ctx context.Context, rs ...*kgo.Record) error
	AllowRebalance()
}

// Inserter ghi một lô JSONEachRow vào bảng; *Client thoả.
type Inserter interface {
	Insert(ctx context.Context, table string, body []byte, token string) error
}

// Pipeline đọc một topic, gom record thành lô và ghi vào một bảng ClickHouse. At-least-once: offset
// chỉ được commit sau khi INSERT thành công; ClickHouse lỗi thì thử lại mãi (giãn cách Backoff),
// không bỏ lô. Record không hợp lệ bị bỏ qua (đếm ở Skipped) nhưng vẫn được commit cùng lô.
type Pipeline struct {
	Source   Source
	Sink     Inserter
	Table    string
	Map      Mapper
	MaxBatch int
	MaxWait  time.Duration
	// Backoff trả thời gian chờ trước lần thử INSERT thứ attempt+1 (attempt bắt đầu từ 1).
	Backoff func(attempt int) time.Duration
	Log     *slog.Logger

	inserted, skipped atomic.Int64
}

// NewPipeline tạo Pipeline với MaxBatch, MaxWait mặc định và events.DefaultBackoff (1s, 2s, … tối đa 30s).
func NewPipeline(src Source, sink Inserter, table string, m Mapper, log *slog.Logger) *Pipeline {
	return &Pipeline{Source: src, Sink: sink, Table: table, Map: m, MaxBatch: DefaultMaxBatch,
		MaxWait: DefaultMaxWait, Backoff: events.DefaultBackoff, Log: log.With("table", table)}
}

// Inserted: số row đã ghi thành công. Skipped: số record không hợp lệ đã bỏ qua.
func (p *Pipeline) Inserted() int64 { return p.inserted.Load() }
func (p *Pipeline) Skipped() int64  { return p.skipped.Load() }

type batch struct {
	recs     []*kgo.Record
	body     []byte
	rows     int
	deadline time.Time
}

// Run chạy tới khi ctx bị huỷ (trả nil) hoặc client Kafka bị đóng. Khi tắt, lô đang dở được ghi nốt
// (tối đa ShutdownTimeout) rồi mới commit; ghi không kịp thì không commit, lần chạy sau đọc lại.
// Rebalance bị chặn từ lúc poll tới khi lô được ghi và commit, nên offset commit luôn thuộc partition
// mà consumer còn giữ.
func (p *Pipeline) Run(ctx context.Context) error {
	var b batch
	for {
		pctx, cancel := ctx, context.CancelFunc(func() {})
		if len(b.recs) > 0 {
			pctx, cancel = context.WithDeadline(ctx, b.deadline)
		}
		fetches := p.Source.PollRecords(pctx, p.MaxBatch-len(b.recs))
		cancel()
		if fetches.IsClientClosed() {
			return errors.New("kafka client closed")
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				p.Log.Warn("kafka fetch error", "topic", topic, "partition", partition, "err", err)
			}
		})
		fetches.EachRecord(func(rec *kgo.Record) { p.add(&b, rec) })

		if ctx.Err() != nil {
			if len(b.recs) > 0 {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
				if err := p.flush(sctx, &b); err != nil {
					p.Log.Warn("pending batch not written on shutdown, will be re-read", "records", len(b.recs), "err", err)
				}
				cancel()
			}
			p.Source.AllowRebalance()
			return nil
		}
		if len(b.recs) > 0 && (len(b.recs) >= p.MaxBatch || !time.Now().Before(b.deadline)) {
			if err := p.flush(ctx, &b); err != nil {
				// ctx bị huỷ khi đang thử lại: không commit, lô được đọc lại ở lần chạy sau.
				p.Source.AllowRebalance()
				return nil
			}
			b = batch{}
		}
		if len(b.recs) == 0 {
			p.Source.AllowRebalance()
		}
	}
}

func (p *Pipeline) add(b *batch, rec *kgo.Record) {
	if len(b.recs) == 0 {
		b.deadline = time.Now().Add(p.MaxWait)
	}
	b.recs = append(b.recs, rec)
	line, err := p.Map(rec)
	if err != nil {
		p.skipped.Add(1)
		p.Log.Warn("skip invalid record", "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
		return
	}
	b.body = append(append(b.body, line...), '\n')
	b.rows++
}

// flush ghi lô (thử lại tới khi thành công hoặc ctx bị huỷ) rồi commit offset của mọi record trong lô.
func (p *Pipeline) flush(ctx context.Context, b *batch) error {
	if b.rows > 0 {
		token := DedupToken(p.Table, b.recs)
		for attempt := 1; ; attempt++ {
			ictx, cancel := context.WithTimeout(ctx, InsertTimeout)
			err := p.Sink.Insert(ictx, p.Table, b.body, token)
			cancel()
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			wait := p.Backoff(attempt)
			p.Log.Error("clickhouse insert failed, retrying", "rows", b.rows, "attempt", attempt,
				"retry_in", wait.String(), "err", err)
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		p.inserted.Add(int64(b.rows))
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := p.Source.CommitRecords(cctx, b.recs...); err != nil {
		// Lô đã nằm trong ClickHouse; sau restart có thể bị ghi lại (trùng, chấp nhận — ADR 0017).
		p.Log.Error("commit offsets failed", "records", len(b.recs), "err", err)
	}
	return nil
}

// DedupToken định danh một lô bằng bảng và khoảng offset từng partition, ví dụ
// "playback_events/playback.events/0:10-25/playback.events/2:3-3". Retry cùng lô gửi cùng token.
func DedupToken(table string, recs []*kgo.Record) string {
	type tp struct {
		topic     string
		partition int32
	}
	type span struct{ lo, hi int64 }
	spans := map[tp]span{}
	for _, r := range recs {
		k := tp{r.Topic, r.Partition}
		if s, ok := spans[k]; ok {
			spans[k] = span{min(s.lo, r.Offset), max(s.hi, r.Offset)}
		} else {
			spans[k] = span{r.Offset, r.Offset}
		}
	}
	keys := make([]tp, 0, len(spans))
	for k := range spans {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b tp) int {
		return cmp.Or(strings.Compare(a.topic, b.topic), cmp.Compare(a.partition, b.partition))
	})
	var sb strings.Builder
	sb.WriteString(table)
	for _, k := range keys {
		fmt.Fprintf(&sb, "/%s/%d:%d-%d", k.topic, k.partition, spans[k].lo, spans[k].hi)
	}
	return sb.String()
}

// DialSource tạo consumer group đọc một topic từ đầu (lần đầu), tắt auto-commit, chặn rebalance
// trong lúc giữ record chưa commit. Đóng bằng CloseAllowingRebalance.
func DialSource(brokers []string, group, topic string) (*kgo.Client, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer %s: %w", group, err)
	}
	return cl, nil
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal; go vet ./internal/analytics/ && go test -short -race -count=3 ./internal/analytics/`
Expected: `ok  streamfilm/internal/analytics` (~2 giây).

- [ ] **Step 5: Commit**

```bash
git add internal/analytics/pipeline.go internal/analytics/pipeline_test.go
git commit -m "feat(analytics): batching pipeline with commit after insert and retry"
```

---

### Task 6: Lệnh `analytics` và integration test Kafka → ClickHouse

**Files:**
- Create: `internal/analytics/service.go`, `cmd/analytics/main.go`, `internal/analytics/integration_test.go`

**Interfaces:**
- Consumes: `DialSource`, `NewPipeline`, `MapPlayback`, `MapLog`, `Migrate`, `NewClient` (Task 3–5); `config.Load` (`ClickHouse*`, `KafkaBrokers`); `logging.New`, `logging.DialKafka` (Task 1); `testutil.StartKafka` (M2), `startClickHouse`, `queryString` (Task 3).
- Produces:
  ```go
  // internal/analytics
  const (
  	GroupPlayback = "analytics-playback"
  	GroupLogs     = "analytics-logs"
  )
  func Run(ctx context.Context, brokers []string, sink Inserter, log *slog.Logger) error
  // cmd/analytics: main() → logging.New("analytics", os.Stdout, nil) (không tee) → run(): config.Load,
  //   NewClient, Migrate, Run(ctx, cfg.KafkaBrokers, client, log); SIGINT/SIGTERM huỷ ctx
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

Test chạy Redpanda + ClickHouse thật: 2500 playback event kèm một record `not-json`, 3 dòng log của `api` gửi qua `logging.KafkaSink` thật; kiểm tra đủ row, `request_id`/`video_id`/`attrs`; chạy lại cùng group thì không ghi lại gì (offset đã commit) nhưng record mới vẫn vào.

`internal/analytics/integration_test.go` (mới):

```go
package analytics

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
	"streamfilm/internal/logging"
	"streamfilm/internal/testutil"
)

// Kafka (Redpanda) + ClickHouse thật: 2500 playback event (3 lô 1000/1000/500) cùng một record hỏng,
// 3 dòng log của "api" qua logging.KafkaSink → đủ trong ClickHouse. Chạy lại cùng group: không ghi lại
// gì (offset đã commit sau INSERT), record mới vẫn được ghi.
func TestRunKafkaToClickHouse(t *testing.T) {
	brokers := testutil.StartKafka(t, events.TopicPlaybackEvents, events.TopicAppLogs)
	c := startClickHouse(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	video := uuid.NewString()
	base := time.Now().UTC().Truncate(time.Millisecond)
	produce := func(from, to int) {
		t.Helper()
		var recs []*kgo.Record
		for i := from; i < to; i++ {
			rec, err := events.NewRecord(events.TopicPlaybackEvents, events.PlaybackEvent{TS: base.Add(time.Duration(i) * time.Millisecond),
				UserID: uuid.NewString(), VideoID: video, Type: "heartbeat", PositionS: i, BitrateKbps: 2800})
			if err != nil {
				t.Fatal(err)
			}
			recs = append(recs, rec)
			if i == from+10 {
				recs = append(recs, &kgo.Record{Topic: events.TopicPlaybackEvents, Value: []byte("not-json")})
			}
		}
		if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	produce(0, 2500)

	sink, err := logging.DialKafka(brokers)
	if err != nil {
		t.Fatal(err)
	}
	log := logging.New("api", io.Discard, sink)
	rctx := context.WithValue(ctx, middleware.RequestIDKey, "req-it-1")
	for i := range 3 {
		log.InfoContext(rctx, "http request", "video_id", video, "status", 200, "i", i)
	}
	sink.Close()
	if sink.Dropped() != 0 {
		t.Fatalf("log sink dropped %d", sink.Dropped())
	}

	count := func(q string) int {
		n, err := strconv.Atoi(queryString(t, c, q))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	playback := "SELECT count() FROM playback_events WHERE video_id = '" + video + "'"
	logs := "SELECT count() FROM app_logs WHERE service = 'api' AND request_id = 'req-it-1' AND video_id = '" + video +
		"' AND msg = 'http request' AND JSONExtractInt(attrs, 'status') = 200"
	waitFor := func(q string, want int) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for count(q) != want {
			if time.Now().After(deadline) {
				t.Fatalf("%s = %d, want %d", q, count(q), want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	runFor := func(check func()) {
		t.Helper()
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- Run(rctx, brokers, c, quiet) }()
		check()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Run không dừng")
		}
	}

	runFor(func() {
		waitFor(playback, 2500)
		waitFor(logs, 3)
	})
	runFor(func() {
		time.Sleep(3 * time.Second) // lần chạy thứ hai: không có gì được đọc lại
		if n := count(playback); n != 2500 {
			t.Errorf("sau restart: %d rows, want 2500 (offset chưa commit?)", n)
		}
		produce(2500, 2510)
		waitFor(playback, 2510)
	})
	if n := count("SELECT count() FROM app_logs"); n != 3 {
		t.Errorf("app_logs = %d, want 3", n)
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go vet ./internal/analytics/`
Expected: FAIL — `vet: internal/analytics/integration_test.go:94:23: undefined: Run`.

- [ ] **Step 3: Cài đặt**

`internal/analytics/service.go` (mới):

```go
package analytics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"streamfilm/internal/events"
)

// Consumer group của từng pipeline: hai group riêng nên một bảng lỗi không giữ offset của bảng kia.
const (
	GroupPlayback = "analytics-playback"
	GroupLogs     = "analytics-logs"
)

// Run chạy hai pipeline — playback.events → playback_events, app.logs → app_logs — tới khi ctx bị huỷ
// (trả nil) hoặc một pipeline lỗi (huỷ pipeline còn lại, trả lỗi).
func Run(ctx context.Context, brokers []string, sink Inserter, log *slog.Logger) error {
	specs := []struct {
		group, topic, table string
		m                   Mapper
	}{
		{GroupPlayback, events.TopicPlaybackEvents, TablePlaybackEvents, MapPlayback},
		{GroupLogs, events.TopicAppLogs, TableAppLogs, MapLog},
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		wg    sync.WaitGroup
		pipes []*Pipeline
		errs  = make([]error, len(specs))
	)
	for i, s := range specs {
		cl, err := DialSource(brokers, s.group, s.topic)
		if err != nil {
			cancel()
			wg.Wait()
			return err
		}
		defer cl.CloseAllowingRebalance()
		p := NewPipeline(cl, sink, s.table, s.m, log)
		pipes = append(pipes, p)
		wg.Go(func() {
			if err := p.Run(ctx); err != nil {
				errs[i] = fmt.Errorf("pipeline %s: %w", s.topic, err)
				cancel()
			}
		})
		log.Info("pipeline started", "topic", s.topic, "group", s.group, "table", s.table)
	}
	wg.Wait()
	for _, p := range pipes {
		log.Info("pipeline stopped", "table", p.Table, "inserted", p.Inserted(), "skipped", p.Skipped())
	}
	return errors.Join(errs...)
}
```

`cmd/analytics/main.go` (mới):

```go
// Command analytics consume playback.events và app.logs từ Kafka, ghi theo lô (1000 record hoặc 1 giây)
// vào ClickHouse (M6, spec §3.1, §4.4). Bảng được tạo (IF NOT EXISTS) mỗi lần khởi động.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"streamfilm/internal/analytics"
	"streamfilm/internal/config"
	"streamfilm/internal/logging"
)

func main() {
	// Chỉ log ra stdout, không tee vào app.logs: analytics là consumer của app.logs, log của chính nó
	// (nhất là khi ClickHouse lỗi) đi vào đó sẽ tạo vòng phản hồi (ADR 0018).
	log := logging.New("analytics", os.Stdout, nil)
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("analytics stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ch := analytics.NewClient(cfg.ClickHouseURL, cfg.ClickHouseDB, cfg.ClickHouseUser, cfg.ClickHousePassword)
	if err := analytics.Migrate(ctx, ch); err != nil {
		return err
	}
	log.Info("clickhouse schema ready", "url", cfg.ClickHouseURL, "db", cfg.ClickHouseDB)
	return analytics.Run(ctx, cfg.KafkaBrokers, ch, log)
}
```

- [ ] **Step 4: Chạy test**

Run: `gofmt -l internal cmd; go vet ./... && go build ./cmd/analytics && go test -count=1 -run TestRunKafkaToClickHouse -v ./internal/analytics/ 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected:

```
--- PASS: TestRunKafkaToClickHouse (27.xx s)
ok  	streamfilm/internal/analytics	…
```

Run: `go test -short -count=1 ./...`
Expected: mọi package `ok` (hoặc `[no test files]`).

- [ ] **Step 5: Commit**

```bash
git add internal/analytics/service.go internal/analytics/integration_test.go cmd/analytics
git commit -m "feat(analytics): analytics command consuming playback.events and app.logs into ClickHouse"
```

---

### Task 7: Triển khai — Dockerfile, compose, Makefile, SQL mẫu

**Files:**
- Create: `deploy/docker/analytics.Dockerfile`, `scripts/analytics/top_videos.sql`, `scripts/analytics/completion_rate.sql`, `scripts/analytics/buffering_by_bitrate.sql`, `scripts/analytics/logs_by_service.sql`, `scripts/analytics-sql.sh`, `internal/analytics/queries_test.go`
- Modify: `deploy/docker-compose.yml`, `Makefile`

**Interfaces:**
- Consumes: `cmd/analytics` (Task 6), `CHTimeString`, `TablePlaybackEvents`, `TableAppLogs`, `startClickHouse`, `queryString`; service `clickhouse`, `kafka-init` trong compose.
- Produces:
  ```go
  // internal/analytics (test): TestSampleQueries chạy mọi scripts/analytics/*.sql kèm "FORMAT JSONEachRow"
  // scripts/analytics-sql.sh [tên ...]  — biến CLICKHOUSE_URL/DB/USER/PASSWORD, FORMAT (mặc định PrettyCompactMonoBlock)
  // make analytics-sql [Q=tên], make run-analytics, make build (thêm bin/analytics)
  // compose: service analytics, image streamfilm-analytics:dev
  ```

- [ ] **Step 1: Viết test (thất bại trước)**

Test chèn 6 sự kiện (2 người xem, 2 bitrate) và 2 dòng log rồi so khớp **chính xác** kết quả JSONEachRow của từng file SQL (số nguyên 64-bit được ClickHouse trả dạng chuỗi).

`internal/analytics/queries_test.go` (mới):

```go
package analytics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Mọi câu SQL mẫu trong scripts/analytics chạy được trên schema thật và trả kết quả đúng với dữ liệu nhỏ.
func TestSampleQueries(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ts := func(sec int) string { return CHTimeString(now.Add(time.Duration(sec) * time.Second)) }
	const (
		u1, u2 = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333"
		v1     = "22222222-2222-2222-2222-222222222222"
	)
	var rows []string
	for _, e := range []struct {
		user, typ    string
		bitrate, buf int
	}{
		{u1, "play", 2800, 0}, {u2, "play", 1400, 0}, {u1, "buffering", 2800, 1500}, {u2, "buffering", 1400, 500},
		{u2, "buffering", 1400, 700}, {u1, "ended", 2800, 0},
	} {
		b, _ := json.Marshal(map[string]any{"ts": ts(len(rows)), "user_id": e.user, "video_id": v1, "type": e.typ,
			"position_s": 0, "bitrate_kbps": e.bitrate, "buffering_ms": e.buf})
		rows = append(rows, string(b))
	}
	if err := c.Insert(ctx, TablePlaybackEvents, []byte(strings.Join(rows, "\n")), ""); err != nil {
		t.Fatal(err)
	}
	logs := `{"ts":"` + ts(0) + `","service":"api","level":"INFO","msg":"http request","request_id":"r1","video_id":"","attrs":"{}"}
{"ts":"` + ts(1) + `","service":"api","level":"ERROR","msg":"get video","request_id":"r2","video_id":"","attrs":"{}"}`
	if err := c.Insert(ctx, TableAppLogs, []byte(logs), ""); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"top_videos":           `{"video_id":"` + v1 + `","plays":"2","viewers":"2"}`,
		"completion_rate":      `{"video_id":"` + v1 + `","started":"2","completed":"1","completion_rate":0.5}`,
		"buffering_by_bitrate": `{"bitrate_kbps":1400,"buffering_events":"2","total_buffering_ms":"1200","avg_buffering_ms":600}` + "\n" + `{"bitrate_kbps":2800,"buffering_events":"1","total_buffering_ms":"1500","avg_buffering_ms":1500}`,
		"logs_by_service":      `{"service":"api","level":"ERROR","lines":"1","last_seen":"` + ts(1) + `"}` + "\n" + `{"service":"api","level":"INFO","lines":"1","last_seen":"` + ts(0) + `"}`,
	}
	files, err := filepath.Glob("../../scripts/analytics/*.sql")
	if err != nil || len(files) != len(want) {
		t.Fatalf("sql files = %v, %v", files, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".sql")
		q, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got := queryString(t, c, string(q)+"\nFORMAT JSONEachRow")
		if got != want[name] {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want[name])
		}
	}
}
```

- [ ] **Step 2: Chạy test, xác nhận thất bại**

Run: `go test -count=1 -run TestSampleQueries ./internal/analytics/`
Expected: FAIL — `sql files = [], <nil>` (chưa có `scripts/analytics/*.sql`).

- [ ] **Step 3: Cài đặt**

Mỗi file SQL là một câu, không có `;` cuối và dòng đầu là comment mô tả (script in dòng đó làm tiêu đề, rồi nối `FORMAT …` vào cuối).

`scripts/analytics/top_videos.sql` (mới):

```sql
-- Top 10 phim theo lượt play (mỗi sự kiện play là một lượt) và số người xem khác nhau
SELECT
    video_id,
    count() AS plays,
    uniqExact(user_id) AS viewers
FROM playback_events
WHERE type = 'play'
GROUP BY video_id
ORDER BY plays DESC, video_id
LIMIT 10
```

`scripts/analytics/completion_rate.sql` (mới):

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
LIMIT 10
```

`scripts/analytics/buffering_by_bitrate.sql` (mới):

```sql
-- Tổng thời gian buffering theo bitrate (bitrate_kbps = 0: Safari HLS gốc, không biết bitrate)
SELECT
    bitrate_kbps,
    count() AS buffering_events,
    sum(buffering_ms) AS total_buffering_ms,
    round(avg(buffering_ms)) AS avg_buffering_ms
FROM playback_events
WHERE type = 'buffering'
GROUP BY bitrate_kbps
ORDER BY bitrate_kbps
```

`scripts/analytics/logs_by_service.sql` (mới):

```sql
-- Số dòng log theo service và level trong 24 giờ qua (WARN/ERROR là chỗ cần xem trước)
SELECT
    service,
    level,
    count() AS lines,
    max(ts) AS last_seen
FROM app_logs
WHERE ts > now64(3) - INTERVAL 1 DAY
GROUP BY service, level
ORDER BY service, level
```

`scripts/analytics-sql.sh` (mới):

```bash
#!/usr/bin/env bash
# Chạy các câu SQL phân tích mẫu trong scripts/analytics/*.sql trên ClickHouse qua HTTP interface.
# Dùng: scripts/analytics-sql.sh [tên ...]   (vd. top_videos; không truyền thì chạy tất cả)
# Biến: CLICKHOUSE_URL (mặc định http://localhost:8123), CLICKHOUSE_DB, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD
# (mặc định streamfilm); FORMAT (mặc định PrettyCompactMonoBlock, e2e dùng JSONEachRow để kiểm tra).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)/analytics"
URL="${CLICKHOUSE_URL:-http://localhost:8123}"
FORMAT="${FORMAT:-PrettyCompactMonoBlock}"

names=("$@")
if [ $# -eq 0 ]; then
  for f in "$DIR"/*.sql; do names+=("$(basename "$f" .sql)"); done
fi
for n in "${names[@]}"; do
  f="$DIR/$n.sql"
  [ -f "$f" ] || { echo "không có $f" >&2; exit 1; }
  [ "$FORMAT" = JSONEachRow ] || echo "== $n: $(head -1 "$f" | sed 's/^-- //')"
  { cat "$f"; printf '\nFORMAT %s\n' "$FORMAT"; } |
    curl -sS --fail-with-body --max-time 30 \
      -H "X-ClickHouse-User: ${CLICKHOUSE_USER:-streamfilm}" -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-streamfilm}" \
      --data-binary @- "$URL/?database=${CLICKHOUSE_DB:-streamfilm}"
done
```

`deploy/docker/analytics.Dockerfile` (mới):

```dockerfile
FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/analytics ./cmd/analytics

FROM alpine:3.24.4
RUN adduser -D -u 10001 app
COPY --from=build /out/analytics /usr/local/bin/analytics
USER app
ENTRYPOINT ["/usr/local/bin/analytics"]
```

`deploy/docker-compose.yml` — thay:

```yaml

volumes:
```

bằng:

```yaml

  # M6: playback.events + app.logs → ClickHouse theo lô (1000 record hoặc 1 giây). Tự tạo bảng khi khởi động.
  analytics:
    build:
      context: ..
      dockerfile: deploy/docker/analytics.Dockerfile
    image: streamfilm-analytics:dev
    environment:
      KAFKA_BROKERS: kafka:9092
      CLICKHOUSE_URL: http://clickhouse:8123
      CLICKHOUSE_DB: streamfilm
      CLICKHOUSE_USER: streamfilm
      CLICKHOUSE_PASSWORD: ${POSTGRES_PASSWORD}
    depends_on:
      kafka-init: { condition: service_completed_successfully }
      clickhouse: { condition: service_healthy }
    restart: unless-stopped
    # Đủ cho lần ghi nốt lô đang dở khi tắt (ShutdownTimeout 10 giây).
    stop_grace_period: 15s

volumes:
```

`Makefile` — thay:

```make

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker e2e-m2 e2e-m3 e2e-m4 e2e-m5 seed-admin sign-url \
	web-install web-dev web-test web-build

```

bằng:

```make

.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker run-analytics \
	e2e-m2 e2e-m3 e2e-m4 e2e-m5 e2e-m6 seed-admin sign-url analytics-sql web-install web-dev web-test web-build

```

`Makefile` — thay:

```make
	$(COMPOSE) up -d --build
	$(COMPOSE) up -d --wait postgres minio kafka console clickhouse nginx api transcoder

```

bằng:

```make
	$(COMPOSE) up -d --build
	$(COMPOSE) up -d --wait postgres minio kafka console clickhouse nginx api transcoder analytics

```

`Makefile` — thay:

```make
logs-app:
	$(COMPOSE) logs -f --tail=100 api transcoder nginx

```

bằng:

```make
logs-app:
	$(COMPOSE) logs -f --tail=100 api transcoder analytics nginx

```

`Makefile` — thay:

```make
	go build -o bin/transcoder ./cmd/transcoder

```

bằng:

```make
	go build -o bin/transcoder ./cmd/transcoder
	go build -o bin/analytics ./cmd/analytics

```

`Makefile` — thay:

```make
	set -a; . ./.env; set +a; go run ./cmd/transcoder worker

```

bằng:

```make
	set -a; . ./.env; set +a; go run ./cmd/transcoder worker

# Chạy analytics trên host (dừng service analytics trong compose trước nếu đã có, vì cùng consumer group).
run-analytics: env
	set -a; . ./.env; set +a; go run ./cmd/analytics

# SQL phân tích mẫu (scripts/analytics/*.sql) trên ClickHouse: make analytics-sql [Q=top_videos]
analytics-sql: env
	@set -a; . ./.env; set +a; ./scripts/analytics-sql.sh $(Q)

```

- [ ] **Step 4: Chạy test**

Run: `chmod +x scripts/analytics-sql.sh && bash -n scripts/analytics-sql.sh && go test -count=1 -run TestSampleQueries ./internal/analytics/`
Expected: `ok  streamfilm/internal/analytics`.

Run: `docker compose -f deploy/docker-compose.yml --env-file .env config -q && make build && ls bin`
Expected: không lỗi; `analytics  api  transcoder`.

Run: `make up && make ps`
Expected: thêm dòng `analytics … Up`. `make logs-app` có `"msg":"clickhouse schema ready"` và hai dòng `"msg":"pipeline started"` (`playback_events`, `app_logs`).

Run: `make analytics-sql`
Expected: bốn bảng, mỗi bảng mở đầu bằng dòng `== tên: mô tả`, ví dụ:

```
== top_videos: Top 10 phim theo lượt play (mỗi sự kiện play là một lượt) và số người xem khác nhau
   ┌─video_id─────────────────────────────┬─plays─┬─viewers─┐
1. │ 8d167d16-5812-4b64-87e9-19ba20a7a516 │     1 │       1 │
   └──────────────────────────────────────┴───────┴─────────┘
```

(`logs_by_service` có `api` / `transcoder`; các bảng `playback_events` có dữ liệu ngay vì analytics đọc `playback.events` từ đầu, gồm sự kiện của `make e2e-m5` trước đó.)

- [ ] **Step 5: Commit**

```bash
git add deploy/docker/analytics.Dockerfile deploy/docker-compose.yml Makefile scripts/analytics scripts/analytics-sql.sh internal/analytics/queries_test.go
git commit -m "feat(deploy): analytics service, sample ClickHouse SQL and make analytics-sql"
```

---

### Task 8: E2E M6

**Files:**
- Create: `scripts/e2e-m6.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `scripts/e2e-m2.sh` (`API`, `E2E_TITLE`, `E2E_ID_FILE`), `scripts/analytics-sql.sh` + `scripts/analytics/*.sql` (Task 7), stack `make up` (image mới của api, transcoder, analytics), `ADMIN_EMAIL`/`ADMIN_PASSWORD`/`CLICKHOUSE_*` trong `.env`, `tmp/sample.mp4`, `GET /api/me` (`id`), nginx đặt `X-Request-Id: $request_id`.
- Produces: `make e2e-m6` in `OK  M6  xem: make analytics-sql`; `make e2e-m6 CH_DOWN=1` thêm bước dừng ClickHouse. Biến script: `SITE` (mặc định `http://localhost:8088`), `CLICKHOUSE_URL/DB/USER/PASSWORD`, `COMPOSE`, `E2E_CLICKHOUSE_DOWN`.

- [ ] **Step 1: Script và target**

`Makefile` — thay:

```make

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
```

bằng:

```make

# E2E M6: sự kiện xem + log api/transcoder vào ClickHouse qua analytics, record hỏng bị bỏ qua, SQL mẫu chạy được.
# `make e2e-m6 CH_DOWN=1` thêm bước dừng ClickHouse (analytics thử lại, không mất sự kiện).
e2e-m6: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; COMPOSE="$(COMPOSE)" E2E_CLICKHOUSE_DOWN="$(CH_DOWN)" ./scripts/e2e-m6.sh tmp/sample.mp4

# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
```

`scripts/e2e-m6.sh` (mới):

```bash
#!/usr/bin/env bash
# E2E M6: sự kiện xem (playback.events) và log của api/transcoder (app.logs) vào ClickHouse qua service
# analytics; record hỏng trong playback.events bị bỏ qua; SQL mẫu chạy được; ClickHouse chết thì analytics
# thử lại và không mất sự kiện (tuỳ chọn).
# Dùng: scripts/e2e-m6.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD, CLICKHOUSE_* (make e2e-m6 nạp từ .env); SITE (mặc định http://localhost:8088);
# COMPOSE: lệnh docker compose của stack (make truyền vào) để ghi record hỏng vào Kafka, đọc log analytics và
# dừng ClickHouse, bỏ trống thì bỏ qua các bước đó; E2E_CLICKHOUSE_DOWN=1: dừng ClickHouse, gửi sự kiện, bật lại.
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"
CH_URL="${CLICKHOUSE_URL:-http://localhost:8123}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m6)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m6)}"
read -ra COMPOSE_CMD <<< "${COMPOSE:-}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
CH_STOPPED=0
cleanup() {
  if [ "$CH_STOPPED" = 1 ]; then "${COMPOSE_CMD[@]}" up -d --wait clickhouse >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
VJAR="$WORK/viewer.jar"
EMAIL="viewer-m6-$(date +%s)-$RANDOM@example.com"

# ch SQL: chạy một câu trên ClickHouse qua HTTP, in kết quả (TSV).
ch() {
  curl -sS --fail-with-body --max-time 30 -H "X-ClickHouse-User: ${CLICKHOUSE_USER:-streamfilm}" \
    -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-streamfilm}" --data-binary "$1" \
    "$CH_URL/?database=${CLICKHOUSE_DB:-streamfilm}"
}
# wait_ch SQL WANT [giây]: chờ tới khi kết quả của SQL bằng WANT (analytics ghi theo lô mỗi 1 giây).
wait_ch() {
  local got="" end=$(($(date +%s) + ${3:-60}))
  while [ "$(date +%s)" -le "$end" ]; do
    got=$(ch "$1" 2>/dev/null || true)
    [ "$got" = "$2" ] && return 0
    sleep 1
  done
  die "ClickHouse: '$1' = '$got', want '$2'"
}
# event TYPE POS [BUFFERING_MS]: gửi sự kiện xem bằng cookie viewer, trả mã HTTP.
event() {
  jq -n --arg id "$ID" --arg t "$1" --argjson p "$2" --argjson b "${3:-0}" \
    '{video_id: $id, type: $t, position_s: $p, bitrate_kbps: 2800, buffering_ms: $b}' |
    curl -s --max-time 30 -o /dev/null -w '%{http_code}' -b "$VJAR" -X POST "$SITE/api/playback/events" \
      -H 'Content-Type: application/json' -d @-
}
types() { echo "SELECT arrayStringConcat(arraySort(groupArray(type)), ',') FROM playback_events WHERE video_id = '$ID'"; }

echo "1/6 admin upload + transcode (scripts/e2e-m2.sh qua $SITE), đăng ký viewer"
ch 'SELECT 1' >/dev/null || die "không gọi được ClickHouse ở $CH_URL"
API="$SITE" E2E_TITLE="M6 $(date +%H:%M:%S)" E2E_ID_FILE="$WORK/id" "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")
jq -n --arg e "$EMAIL" '{email: $e, password: "viewer-password-1"}' |
  curl -fsS --max-time 30 -c "$VJAR" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- \
  >/dev/null || die "đăng ký viewer"
USER_ID=$(curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/me" | jq -r .id)

# JSON hợp lệ (để e2e-m5 đọc topic bằng jq vẫn được) nhưng user_id không phải UUID: analytics phải bỏ qua.
echo "2/6 record hỏng vào playback.events (analytics phải bỏ qua và chạy tiếp)"
if [ ${#COMPOSE_CMD[@]} -eq 0 ]; then
  echo "    bỏ qua (không có COMPOSE)"
else
  echo '{"e2e":"m6 poison","user_id":"not-a-uuid"}' | "${COMPOSE_CMD[@]}" exec -T kafka /opt/kafka/bin/kafka-console-producer.sh \
    --bootstrap-server kafka:9092 --topic playback.events >/dev/null 2>&1 || die "ghi record hỏng"
fi

echo "3/6 /play và sự kiện xem: play, heartbeat, buffering, seek, ended"
curl -fsS --max-time 30 -o /dev/null -b "$VJAR" "$SITE/api/videos/$ID/play" || die "/play"
[ "$(event play 0)" = 204 ] || die "event play"
[ "$(event heartbeat 10.2)" = 204 ] || die "event heartbeat"
[ "$(event buffering 11 1500)" = 204 ] || die "event buffering"
[ "$(event seek 25)" = 204 ] || die "event seek"
[ "$(event ended 30)" = 204 ] || die "event ended"

echo "4/6 playback_events trong ClickHouse"
wait_ch "$(types)" "buffering,ended,heartbeat,play,seek"
[ "$(ch "SELECT toString(any(user_id)), uniqExact(user_id) FROM playback_events WHERE video_id = '$ID'")" = "$USER_ID	1" ] ||
  die "user_id trong playback_events khác $USER_ID"
[ "$(ch "SELECT position_s, bitrate_kbps, buffering_ms FROM playback_events WHERE video_id = '$ID' AND type = 'buffering'")" = "11	2800	1500" ] ||
  die "row buffering sai"
if [ ${#COMPOSE_CMD[@]} -gt 0 ]; then
  "${COMPOSE_CMD[@]}" logs --no-log-prefix analytics 2>/dev/null | grep -q '"msg":"skip invalid record"' ||
    die "analytics không log record hỏng"
fi

echo "5/6 app_logs: log api có request_id (nginx sinh) + video_id, log transcoder theo video_id, không có log của analytics"
# nginx đặt X-Request-Id = $request_id (32 ký tự hex) trước khi proxy tới api.
wait_ch "SELECT count() > 0 FROM app_logs WHERE service = 'api' AND video_id = '$ID' AND msg = 'http request' AND JSONExtractString(attrs, 'path') = '/api/videos/$ID/play' AND JSONExtractInt(attrs, 'status') = 200 AND match(request_id, '^[0-9a-f]{32}$')" 1
wait_ch "SELECT count() > 0 FROM app_logs WHERE service = 'transcoder' AND video_id = '$ID'" 1
[ "$(ch "SELECT count() FROM app_logs WHERE service = 'analytics'")" = 0 ] || die "log của analytics lọt vào app_logs"

echo "6/6 SQL mẫu (scripts/analytics/*.sql) trả kết quả"
for f in "$(dirname "$0")"/analytics/*.sql; do
  n=$(basename "$f" .sql)
  out=$(FORMAT=JSONEachRow "$(dirname "$0")/analytics-sql.sh" "$n") || die "SQL $n lỗi"
  [ -n "$out" ] || die "SQL $n không có dòng nào"
  echo "    $n: $(echo "$out" | wc -l | tr -d ' ') dòng"
done

if [ "${E2E_CLICKHOUSE_DOWN:-}" = 1 ]; then
  [ ${#COMPOSE_CMD[@]} -gt 0 ] || die "E2E_CLICKHOUSE_DOWN cần COMPOSE"
  echo "    dừng ClickHouse: sự kiện vẫn 204, analytics thử lại rồi ghi đủ khi ClickHouse sống lại"
  CH_STOPPED=1
  "${COMPOSE_CMD[@]}" stop clickhouse >/dev/null 2>&1
  [ "$(event heartbeat 20)" = 204 ] || die "ClickHouse chết: event phải 204"
  [ "$(event pause 21)" = 204 ] || die "ClickHouse chết: event phải 204"
  sleep 5
  "${COMPOSE_CMD[@]}" up -d --wait clickhouse >/dev/null 2>&1 || die "bật lại ClickHouse"
  CH_STOPPED=0
  wait_ch "$(types)" "buffering,ended,heartbeat,heartbeat,pause,play,seek" 90
  "${COMPOSE_CMD[@]}" logs --no-log-prefix analytics 2>/dev/null | grep -q '"msg":"clickhouse insert failed, retrying"' ||
    die "analytics không log lần thử lại"
fi

echo "OK  M6  xem: make analytics-sql"
```

Nhớ: `chmod +x scripts/e2e-m6.sh`.

- [ ] **Step 2: Chạy**

Run: `chmod +x scripts/e2e-m6.sh && bash -n scripts/e2e-m6.sh && make up && make e2e-m6 CH_DOWN=1`
Expected (rút gọn):

```
1/6 admin upload + transcode (scripts/e2e-m2.sh qua http://localhost:8088), đăng ký viewer
    …
    {"duration_s":30,"renditions":[1080,720,480,360]}
    …
2/6 record hỏng vào playback.events (analytics phải bỏ qua và chạy tiếp)
3/6 /play và sự kiện xem: play, heartbeat, buffering, seek, ended
4/6 playback_events trong ClickHouse
5/6 app_logs: log api có request_id (nginx sinh) + video_id, log transcoder theo video_id, không có log của analytics
6/6 SQL mẫu (scripts/analytics/*.sql) trả kết quả
    buffering_by_bitrate: … dòng
    completion_rate: … dòng
    logs_by_service: … dòng
    top_videos: … dòng
    dừng ClickHouse: sự kiện vẫn 204, analytics thử lại rồi ghi đủ khi ClickHouse sống lại
OK  M6  xem: make analytics-sql
```

Run: `make e2e-m5 && make e2e-m4 && make e2e-m3`
Expected: `OK  M5  …`, `OK  M4  …`, `OK  M3` (không hồi quy; record hỏng của e2e-m6 là JSON hợp lệ nên bước đọc `playback.events` bằng `jq` của e2e-m5 vẫn chạy).

Run: `make logs-app 2>/dev/null | grep -m1 'clickhouse insert failed, retrying'` (Ctrl+C để thoát)
Expected: có ít nhất một dòng của `analytics` từ lúc ClickHouse bị dừng.

- [ ] **Step 3: Kiểm tra tay (spec §10 M6)**

1. Mở http://localhost:8088, đăng nhập, phát một phim ~20 giây, tua, xem hết.
2. `make analytics-sql` → `top_videos` có phim đó, `completion_rate` của nó là `1`, `buffering_by_bitrate` có bitrate đang phát (nếu có buffering).
3. Play UI http://localhost:8123/play (user `streamfilm`, mật khẩu `POSTGRES_PASSWORD`): `SELECT ts, service, msg, attrs FROM app_logs WHERE video_id = '<id>' ORDER BY ts` thấy log transcoder lúc transcode và các dòng `http request` của `/api/videos/<id>…`.
4. Redpanda Console → Consumer Groups: `analytics-playback`, `analytics-logs` lag về 0.

- [ ] **Step 4: Commit**

```bash
git add scripts/e2e-m6.sh Makefile
git commit -m "test(e2e): M6 playback events and app logs in ClickHouse"
```

---

### Task 9: Tài liệu M6 và ADR

**Files:**
- Modify: `README.md`, `docs/architecture.md`, `docs/data-model.md`, `docs/flows.md`, `docs/operations.md`
- Create: `docs/decisions/0016-clickhouse-http-startup-schema.md`, `docs/decisions/0017-analytics-at-least-once.md`, `docs/decisions/0018-app-logs-async-sink.md`

**Interfaces:**
- Consumes: hành vi đã chạy ở Task 1–8 (spec §11: tài liệu chỉ mô tả những gì đã chạy).
- Produces: tài liệu khớp code M6.

Mỗi thay đổi dưới đây là một cặp "thay đoạn cũ (khớp đúng một lần trong file) bằng đoạn mới".

- [ ] **Step 1: README.md**

`README.md` — thay:

```markdown

Trạng thái: **M5** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4); poster, ảnh xem trước khi tua (sprite), phụ đề nhiều ngôn ngữ, hàng "Xem tiếp" và sự kiện xem lên Kafka (M5). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

```

bằng:

```markdown

Trạng thái: **M5** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4); poster, ảnh xem trước khi tua (sprite), phụ đề nhiều ngôn ngữ, hàng "Xem tiếp" và sự kiện xem lên Kafka (M5); service `analytics` đưa sự kiện xem và log của api/transcoder vào ClickHouse, có SQL phân tích mẫu (M6). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

```

`README.md` — thay:

````markdown
```bash
make up          # dựng hạ tầng + build và chạy api, transcoder, nginx (kèm web)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
````

bằng:

````markdown
```bash
make up          # dựng hạ tầng + build và chạy api, transcoder, analytics, nginx (kèm web)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
````

`README.md` — thay:

````markdown
make e2e-m5      # poster/sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" (KAFKA_DOWN=1: thử Kafka chết)
```
````

bằng:

````markdown
make e2e-m5      # poster/sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" (KAFKA_DOWN=1: thử Kafka chết)
make e2e-m6      # sự kiện xem + log vào ClickHouse, SQL mẫu (CH_DOWN=1: thử ClickHouse chết)
make analytics-sql  # top phim, tỉ lệ xem hết, buffering theo bitrate, log theo service
```
````

- [ ] **Step 2: docs/architecture.md**

`docs/architecture.md` — thay:

````markdown
  N -->|"/cdn/s/… secure_link_md5 hợp lệ"| S3
```

Phần đã chạy tới M5: `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu, `/play` kèm phụ đề / thumbnails / `resume_at`, hàng "Xem tiếp", nhận sự kiện xem, upload video và phụ đề cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker` (HLS + poster + sprite), Postgres, Kafka, MinIO, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL), web người xem `web/app`. Chưa có: analytics service và ClickHouse (M6 — `playback.events` đã được publish), trang admin (M7).

````

bằng:

````markdown
  N -->|"/cdn/s/… secure_link_md5 hợp lệ"| S3
  API -->|"playback.events, app.logs"| K
  T -->|app.logs| K
  K --> AN[analytics]
  AN -->|"INSERT theo lô (HTTP 8123)"| CH[(ClickHouse)]
```

Phần đã chạy tới M6: `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu, `/play` kèm phụ đề / thumbnails / `resume_at`, hàng "Xem tiếp", nhận sự kiện xem, upload video và phụ đề cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker` (HLS + poster + sprite), `analytics` (`playback.events` và `app.logs` → ClickHouse), Postgres, Kafka, MinIO, ClickHouse, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL), web người xem `web/app`. Chưa có: trang admin (M7).

````

`docs/architecture.md` — thay:

```markdown
- Ghi Postgres rồi publish Kafka là dual-write, chấp nhận ở MVP ([ADR 0007](decisions/0007-accept-dual-write.md)).

```

bằng:

```markdown
- Ghi Postgres rồi publish Kafka là dual-write, chấp nhận ở MVP ([ADR 0007](decisions/0007-accept-dual-write.md)).
- `analytics` (M6) không dùng `events.Consumer` (commit từng record) mà poll theo lô và commit cả lô sau INSERT; record hỏng được bỏ qua thay vì thử lại mãi, vì dữ liệu phân tích không đáng chặn cả partition ([ADR 0017](decisions/0017-analytics-at-least-once.md)).

```

`docs/architecture.md` — thay:

```markdown
- **Sự kiện xem và "Xem tiếp":** `PlaybackReporter` (web) gửi `play`, `pause`, `seek`, `ended`, `buffering` (thời gian chờ) và `heartbeat` mỗi 10 giây khi đang phát; rời trang thì gửi heartbeat vị trí cuối (`fetch` `keepalive`). api ghi `watch_progress` cho heartbeat/pause/ended, publish mọi sự kiện lên `playback.events` (key `user_id`) ở nền — Kafka chết thì sự kiện bị bỏ, request vẫn `204` ([ADR 0015](decisions/0015-playback-events-best-effort.md)). "Xem tiếp" = `5% < position/duration < 95%`, mới xem nhất trước, tối đa 20; `resume_at` = vị trí đã lưu (0 nếu chưa xem hoặc đã xem ≥ 95%).

```

bằng:

```markdown
- **Sự kiện xem và "Xem tiếp":** `PlaybackReporter` (web) gửi `play`, `pause`, `seek`, `ended`, `buffering` (thời gian chờ) và `heartbeat` mỗi 10 giây khi đang phát; rời trang thì gửi heartbeat vị trí cuối (`fetch` `keepalive`). api ghi `watch_progress` cho heartbeat/pause/ended, publish mọi sự kiện lên `playback.events` (key `user_id`) ở nền — Kafka chết thì sự kiện bị bỏ, request vẫn `204` ([ADR 0015](decisions/0015-playback-events-best-effort.md)). "Xem tiếp" = `5% < position/duration < 95%`, mới xem nhất trước, tối đa 20; `resume_at` = vị trí đã lưu (0 nếu chưa xem hoặc đã xem ≥ 95%).

## Analytics và log (M6)

- **Log:** mọi service dựng logger bằng `logging.New(service, stdout, sink)`: JSON của `slog`, luôn có `service`; `request_id` được thêm từ ctx (middleware `RequestID` của chi, qua nginx là `$request_id`) khi gọi `…Context(r.Context(), …)`; dòng `http request` có thêm `video_id` khi route có `{id}` là UUID. `api` và `transcoder worker` ghi mỗi dòng ra stdout **và** `logging.KafkaSink` → topic `app.logs`: `TryProduce` của franz-go trên client riêng (buffer 4096 dòng, acks leader, hạn giao 5 giây) — buffer đầy hoặc Kafka chết thì dòng bị bỏ và đếm `Dropped`, không bao giờ chặn request. `analytics` chỉ log ra stdout để không tự nuôi `app.logs` ([ADR 0018](decisions/0018-app-logs-async-sink.md)).
- **analytics:** hai pipeline độc lập, mỗi cái một consumer group: `analytics-playback` (`playback.events` → `playback_events`) và `analytics-logs` (`app.logs` → `app_logs`). Gom record thành lô, ghi khi đủ 1000 record hoặc 1 giây sau record đầu tiên của lô, và khi tắt. Ghi bằng `INSERT … FORMAT JSONEachRow` qua HTTP interface (cổng 8123), không dùng driver native ([ADR 0016](decisions/0016-clickhouse-http-startup-schema.md)). Bảng được tạo (`CREATE TABLE IF NOT EXISTS`) mỗi lần khởi động.
- **Giao nhận:** offset chỉ commit sau khi INSERT thành công (at-least-once). ClickHouse lỗi → thử lại cùng lô mãi với giãn cách 1s → 30s, không bỏ; lô gửi lại mang cùng `insert_deduplication_token` nên retry sau timeout không ghi trùng. Record hỏng (JSON sai, `user_id`/`video_id` không phải UUID, số âm) bị bỏ qua, log `skip invalid record`, vẫn commit — một row sai kiểu làm ClickHouse từ chối cả lô ([ADR 0017](decisions/0017-analytics-at-least-once.md)).

```

`docs/architecture.md` — thay:

```markdown
| Client apps (web) | SPA React + hls.js phục vụ bởi nginx cùng origin với API |
```

bằng:

```markdown
| Client apps (web) | SPA React + hls.js phục vụ bởi nginx cùng origin với API |
| Analytics / logging | `analytics` consume `playback.events` + `app.logs`, ghi lô vào ClickHouse |
```

- [ ] **Step 3: docs/data-model.md**

`docs/data-model.md` — thay:

```markdown
| `video.transcoded` | transcoder | `api` | `{"video_id","duration_s","hls_prefix","renditions":[{"height","bitrate_kbps","playlist_key"}],"poster_key","sprite_key"}` |
| `video.failed` | transcoder | `api` | `{"video_id","reason"}` |
| `playback.events` | api | (analytics, M6) | `{"ts","user_id","video_id","type","position_s","bitrate_kbps","buffering_ms"}` |

Key message = `video_id`, riêng `playback.events` key = `user_id` (giữ thứ tự sự kiện của một người xem). Mỗi topic 3 partition. `app.logs` đã tạo sẵn, dùng từ M6. `poster_key` / `sprite_key` có từ M5 (bỏ khỏi JSON khi rỗng).

Ví dụ `playback.events`:
```

bằng:

```markdown
| `video.transcoded` | transcoder | `api` | `{"video_id","duration_s","hls_prefix","renditions":[{"height","bitrate_kbps","playlist_key"}],"poster_key","sprite_key"}` |
| `video.failed` | transcoder | `api` | `{"video_id","reason"}` |
| `playback.events` | api | `analytics-playback` | `{"ts","user_id","video_id","type","position_s","bitrate_kbps","buffering_ms"}` |
| `app.logs` | api, transcoder worker | `analytics-logs` | một dòng log JSON của `slog` (`time`, `level`, `msg`, `service`, `request_id`, `video_id`, …), không có key |

Key message = `video_id`, riêng `playback.events` key = `user_id` (giữ thứ tự sự kiện của một người xem). Mỗi topic 3 partition. `app.logs` có từ M6. `poster_key` / `sprite_key` có từ M5 (bỏ khỏi JSON khi rỗng).

Ví dụ `playback.events`:
```

`docs/data-model.md` — thay:

````markdown
 "error":"transcode: input has no video stream","attempts":1,"failed_at":"2026-11-14T10:00:00Z"}
```
````

bằng:

````markdown
 "error":"transcode: input has no video stream","attempts":1,"failed_at":"2026-11-14T10:00:00Z"}
```

Ví dụ `app.logs`:

```json
{"time":"2026-11-15T10:05:00.123Z","level":"INFO","msg":"http request","service":"api",
 "request_id":"3dff344481165cebcf48ff9be68dcea3","method":"GET","path":"/api/videos/7c1e…/play",
 "status":200,"duration_ms":4,"video_id":"7c1e…"}
```

## ClickHouse (M6)

Database `streamfilm`. `analytics` tạo bảng khi khởi động (`internal/analytics/schema.go`, idempotent):

```sql
CREATE TABLE IF NOT EXISTS playback_events (
  ts DateTime64(3, 'UTC'), user_id UUID, video_id UUID, type LowCardinality(String),
  position_s UInt32, bitrate_kbps UInt32, buffering_ms UInt32
) ENGINE = MergeTree ORDER BY (video_id, ts)
SETTINGS non_replicated_deduplication_window = 1000;

CREATE TABLE IF NOT EXISTS app_logs (
  ts DateTime64(3, 'UTC'), service LowCardinality(String), level LowCardinality(String),
  msg String, request_id String, video_id String, attrs String
) ENGINE = MergeTree ORDER BY (service, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY
SETTINGS non_replicated_deduplication_window = 1000;
```

- `playback_events`: một row cho mỗi sự kiện của `playback.events`, `user_id`/`video_id` chữ thường. Có thể có row trùng khi analytics chết giữa INSERT và commit ([ADR 0017](decisions/0017-analytics-at-least-once.md)).
- `app_logs`: `time` của dòng log → `ts`; `level`, `msg`, `service`, `request_id`, `video_id` thành cột (thiếu thì chuỗi rỗng); mọi trường còn lại thành JSON trong `attrs` (đọc bằng `JSONExtractString(attrs, 'path')`, `JSONExtractInt(attrs, 'status')`…). Giữ 30 ngày.
- Giờ lưu UTC; analytics gửi dạng `YYYY-MM-DD hh:mm:ss.sss`.

SQL mẫu: `scripts/analytics/*.sql` (chạy bằng `make analytics-sql`, xem [operations](operations.md#analytics-và-log-m6)).
````

- [ ] **Step 4: docs/flows.md**

`docs/flows.md` — thay:

```markdown
# Các luồng

Chỉ gồm các luồng đã chạy (tới M3). Xem tiếp và analytics được thêm ở M5–M6.

## Upload và transcode
```

bằng:

```markdown
# Các luồng

Chỉ gồm các luồng đã chạy (tới M6).

## Upload và transcode
```

`docs/flows.md` — thay:

````markdown
  API->>PG: resume_at = position_s (0 nếu ≥ 95%)
```
````

bằng:

````markdown
  API->>PG: resume_at = position_s (0 nếu ≥ 95%)
```

## Analytics và log (M6)

```mermaid
sequenceDiagram
  autonumber
  participant API as api / transcoder
  participant K as Kafka
  participant AN as analytics
  participant CH as ClickHouse
  API-)K: playback.events (BestEffort, M5)
  API-)K: app.logs — mỗi dòng slog (TryProduce, đầy thì bỏ)
  loop mỗi pipeline (analytics-playback, analytics-logs)
    AN->>K: PollRecords (tối đa 1000 - số record đang gom)
    Note over AN: record hỏng → bỏ qua, log "skip invalid record"
    alt đủ 1000 record hoặc 1 giây từ record đầu của lô
      AN->>CH: INSERT … FORMAT JSONEachRow (insert_deduplication_token = khoảng offset)
      alt ClickHouse lỗi
        AN->>AN: chờ 1s, 2s, 4s … 30s rồi gửi lại cùng lô, cùng token
      end
      AN->>K: CommitRecords (cả lô, kể cả record hỏng)
    end
  end
  Note over AN,K: SIGTERM: ghi nốt lô đang dở (tối đa 10 giây) rồi commit; không kịp thì không commit, lần sau đọc lại
```
````

- [ ] **Step 5: docs/operations.md**

`docs/operations.md` — thay:

```markdown
| `make ps` / `make logs` | Trạng thái / log mọi container |
| `make logs-app` | Log của `api`, `transcoder` và `nginx` |
| `make seed-admin` | Tạo/đặt lại admin `ADMIN_EMAIL` / `ADMIN_PASSWORD` (trong `.env`) bên trong container api |
```

bằng:

```markdown
| `make ps` / `make logs` | Trạng thái / log mọi container |
| `make logs-app` | Log của `api`, `transcoder`, `analytics` và `nginx` |
| `make seed-admin` | Tạo/đặt lại admin `ADMIN_EMAIL` / `ADMIN_PASSWORD` (trong `.env`) bên trong container api |
```

`docs/operations.md` — thay:

```markdown
| `make e2e-m4` | E2E: SPA và fallback route qua nginx, `/api/` cùng origin (cookie), upload video tiêu đề tiếng Việt, tìm không dấu / có dấu / gõ sai / lọc thể loại, `/play` cùng origin |
| `make web-install` / `make web-test` / `make web-build` | `npm ci` / Vitest / build production cho `web/app` (cần Node 20.19+) |
```

bằng:

```markdown
| `make e2e-m4` | E2E: SPA và fallback route qua nginx, `/api/` cùng origin (cookie), upload video tiêu đề tiếng Việt, tìm không dấu / có dấu / gõ sai / lọc thể loại, `/play` cùng origin |
| `make e2e-m5` | E2E: poster/sprite, phụ đề, sự kiện xem → "Xem tiếp", `playback.events` trong Kafka (`KAFKA_DOWN=1`: thử Kafka chết) |
| `make e2e-m6` | E2E: sự kiện xem và log api/transcoder vào ClickHouse, record hỏng bị bỏ qua, SQL mẫu có kết quả (`CH_DOWN=1`: dừng ClickHouse, analytics thử lại, không mất sự kiện) |
| `make analytics-sql [Q=tên]` | Chạy SQL mẫu `scripts/analytics/*.sql` trên ClickHouse (mặc định tất cả) |
| `make web-install` / `make web-test` / `make web-build` | `npm ci` / Vitest / build production cho `web/app` (cần Node 20.19+) |
```

`docs/operations.md` — thay:

```markdown
| `make sample` | Sinh `tmp/sample.mp4` |
| `make build` | Build `bin/api`, `bin/transcoder` |
| `make run-api` / `make run-worker` | Chạy api / worker trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |
```

bằng:

```markdown
| `make sample` | Sinh `tmp/sample.mp4` |
| `make build` | Build `bin/api`, `bin/transcoder`, `bin/analytics` |
| `make run-api` / `make run-worker` / `make run-analytics` | Chạy api / worker / analytics trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder analytics`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |
```

`docs/operations.md` — thay:

```markdown

- Đọc `playback.events` (đến M6 chưa có consumer; Redpanda Console → Topics → `playback.events` cũng xem được):

```

bằng:

```markdown

- Đọc `playback.events` thô (Redpanda Console → Topics → `playback.events` cũng xem được; từ M6 dữ liệu đã nằm trong ClickHouse, xem mục dưới):

```

`docs/operations.md` — thay:

```markdown

## Theo dõi Kafka
```

bằng:

````markdown

## Analytics và log (M6)

`make up` chạy thêm `analytics` (không có healthcheck; `--wait` chỉ đợi container chạy). Nó tạo bảng ClickHouse khi khởi động rồi đọc `playback.events` và `app.logs` từ đầu (lần đầu đọc cả sự kiện từ M5).

Biến môi trường (M6, `make env` tự thêm vào `.env`):

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `CLICKHOUSE_URL` | `http://localhost:8123` | HTTP interface; trong compose là `http://clickhouse:8123` |
| `CLICKHOUSE_DB` / `CLICKHOUSE_USER` | `streamfilm` / `streamfilm` | |
| `CLICKHOUSE_PASSWORD` | `streamfilm` | Phải bằng `POSTGRES_PASSWORD` (service `clickhouse` lấy mật khẩu từ biến đó) |

SQL mẫu (`scripts/analytics/`, mỗi file một câu, dòng đầu là mô tả):

| File | Câu hỏi |
|---|---|
| `top_videos.sql` | Top 10 phim theo lượt play, kèm số người xem khác nhau |
| `completion_rate.sql` | Tỉ lệ xem hết = người có `ended` / người có `play`, theo phim |
| `buffering_by_bitrate.sql` | Số lần, tổng và trung bình thời gian buffering theo `bitrate_kbps` |
| `logs_by_service.sql` | Số dòng log theo service và level trong 24 giờ |

```bash
make analytics-sql                 # chạy tất cả, bảng PrettyCompact
make analytics-sql Q=top_videos    # một câu
```

Truy vấn tay (Play UI http://localhost:8123/play, user `streamfilm`, mật khẩu `POSTGRES_PASSWORD`; hoặc `clickhouse-client` trong container):

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec clickhouse clickhouse-client \
  --user streamfilm --password streamfilm -d streamfilm -q \
  "SELECT ts, service, level, msg, request_id FROM app_logs WHERE level IN ('WARN','ERROR') ORDER BY ts DESC LIMIT 20"
```

- Lần theo một request: lấy `request_id` từ dòng `http request` (nginx sinh `$request_id`), rồi `SELECT ts, msg, attrs FROM app_logs WHERE request_id = '…' ORDER BY ts`.
- Mọi log của một video: `SELECT ts, service, msg FROM app_logs WHERE video_id = '…' ORDER BY ts` (dòng `http request` của route `/{id}`, log transcoder, log lỗi của handler).
- Log của chính `analytics` chỉ có ở `make logs-app` (stdout), không có trong `app_logs` ([ADR 0018](decisions/0018-app-logs-async-sink.md)).
- Thử ClickHouse chết: `make e2e-m6 CH_DOWN=1`. Trong lúc ClickHouse tắt, log analytics có `clickhouse insert failed, retrying` (giãn cách tới 30 giây), lag của group `analytics-*` tăng; bật lại thì lô được ghi và lag về 0.

## Theo dõi Kafka
````

`docs/operations.md` — thay:

```markdown
- Redpanda Console → **Topics** → chọn topic → tab Messages: xem key (`video_id`) và JSON.
- **Consumer Groups**: `api` (đọc `video.processing|transcoded|failed`) và `transcoder` (đọc `video.uploaded`). Lag khác 0 kéo dài nghĩa là consumer đang kẹt; xem `make logs-app`.
- DLQ: topic `video.uploaded.dlq`. Mỗi message có `payload` gốc, `error`, `attempts`. Sau khi sửa nguyên nhân, gọi `POST /api/admin/videos/{id}/retry`.
```

bằng:

```markdown
- Redpanda Console → **Topics** → chọn topic → tab Messages: xem key (`video_id`) và JSON.
- **Consumer Groups**: `api` (đọc `video.processing|transcoded|failed`), `transcoder` (đọc `video.uploaded`), `analytics-playback` (đọc `playback.events`) và `analytics-logs` (đọc `app.logs`). Lag khác 0 kéo dài nghĩa là consumer đang kẹt; xem `make logs-app`.
- DLQ: topic `video.uploaded.dlq`. Mỗi message có `payload` gốc, `error`, `attempts`. Sau khi sửa nguyên nhân, gọi `POST /api/admin/videos/{id}/retry`.
```

`docs/operations.md` — thay:

```markdown
| Tìm "tro choi" không ra video vừa upload | Video chưa `ready`, hoặc api chưa chạy migration `0003_search` (xem `schema_migrations`) |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
```

bằng:

```markdown
| Tìm "tro choi" không ra video vừa upload | Video chưa `ready`, hoặc api chưa chạy migration `0003_search` (xem `schema_migrations`) |
| `make analytics-sql` báo `Authentication failed` | `CLICKHOUSE_PASSWORD` trong `.env` khác `POSTGRES_PASSWORD` (mật khẩu của service `clickhouse`) |
| `playback_events` / `app_logs` không có row mới | `make ps` (analytics chạy chưa?), `make logs-app` tìm `clickhouse insert failed`; Redpanda Console → Consumer Groups `analytics-*` xem lag |
| Log analytics có `skip invalid record` | Record trong topic không đúng schema (JSON hỏng, id không phải UUID): đã bỏ qua và commit, không cần xử lý; `make e2e-m6` cố ý ghi một record như vậy |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
```

- [ ] **Step 6: ADR 0016, 0017, 0018**

`docs/decisions/0016-clickhouse-http-startup-schema.md` (mới):

```markdown
# ADR 0016: ClickHouse qua HTTP interface, schema tạo khi analytics khởi động

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
M6 (spec §3.1, §4.4, §5): `analytics` ghi `playback.events` và `app.logs` vào ClickHouse theo lô (1000 record hoặc 1 giây). Service `clickhouse` (image `clickhouse/clickhouse-server:24.8`) đã có từ M0, chỉ mở cổng HTTP 8123 ra host; database `streamfilm` có sẵn nhưng chưa có bảng. Volume `chdata` của stack đang chạy đã được khởi tạo.

## Quyết định
- **Client:** HTTP interface (8123) bằng `net/http`: mỗi lô là một `POST /?query=INSERT INTO {table} FORMAT JSONEachRow` với body là các dòng JSON; user/mật khẩu qua header `X-ClickHouse-User` / `X-ClickHouse-Key`, database qua tham số `database`. Không thêm dependency. Lỗi (HTTP ≠ 200) trả kèm 500 ký tự đầu của thông điệp ClickHouse.
- **Thời gian:** cột `DateTime64(3, 'UTC')`; analytics gửi `YYYY-MM-DD hh:mm:ss.sss` theo UTC (định dạng mặc định `date_time_input_format=basic` đọc được), nên không phụ thuộc múi giờ của server.
- **Schema:** `internal/analytics/schema.go` chứa các câu `CREATE TABLE IF NOT EXISTS`, `analytics.Migrate` chạy mỗi lần khởi động (cùng tinh thần ADR 0005 cho Postgres). Integration test chạy `Migrate` hai lần.
- `app_logs` dùng `TTL toDateTime(ts) + INTERVAL 30 DAY` (spec ghi `ts + INTERVAL 30 DAY`; ép về `DateTime` để biểu thức TTL rõ kiểu).

## Phương án đã loại
- `clickhouse-go/v2` (giao thức native, cổng 9000): nhanh hơn với lô lớn, nhưng thêm dependency nặng và cổng mới; ở 1000 row/giây JSONEachRow qua HTTP là đủ, và dễ thử bằng `curl` — đúng mục tiêu học.
- `docker-entrypoint-initdb.d` mount SQL vào ClickHouse: chỉ chạy khi volume rỗng, nên stack đang có `chdata` sẽ không bao giờ có bảng; đổi schema về sau cũng không chạy lại. Tạo bảng trong analytics thì luôn đúng và test được bằng testcontainers.

## Hệ quả
- Đổi kiểu cột về sau cần `ALTER TABLE` viết tay (chưa có bảng phiên bản migration như Postgres); đủ cho MVP với hai bảng.
- `make analytics-sql` và `scripts/e2e-m6.sh` dùng chính HTTP interface đó bằng `curl`.
- Analytics không khởi động được khi ClickHouse chưa sẵn sàng: compose đợi `clickhouse` healthy, và `restart: unless-stopped` thử lại nếu `Migrate` lỗi.
```

`docs/decisions/0017-analytics-at-least-once.md` (mới):

```markdown
# ADR 0017: Analytics — lô theo partition, commit sau INSERT (at-least-once), bỏ qua record hỏng

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.4: ghi theo lô mỗi 1 giây hoặc 1000 bản ghi. Spec §5: `playback_events` và `app_logs` là `MergeTree`. Sự kiện xem không có id riêng nên không có khoá tự nhiên để khử trùng. Một row sai kiểu (ví dụ `user_id` không phải UUID) làm ClickHouse từ chối cả lô. `events.Consumer` (ADR 0006) commit từng record và thử lại record lỗi mãi — hợp với lệnh transcode, không hợp với ghi lô.

## Quyết định
- `analytics.Pipeline`: mỗi topic một client franz-go, một consumer group (`analytics-playback`, `analytics-logs`), `DisableAutoCommit` + `BlockRebalanceOnPoll`. Poll tới 1000 − (số record đang gom); lô được ghi khi đủ 1000 record hoặc 1 giây sau record đầu tiên (poll có deadline), và khi tắt (tối đa 10 giây).
- Offset của **mọi** record trong lô được commit **sau khi** INSERT thành công. Rebalance bị chặn từ lúc poll tới lúc commit, nên offset commit luôn thuộc partition consumer còn giữ.
- ClickHouse lỗi → thử lại cùng lô mãi (1s, 2s, 4s … tối đa 30s), không bỏ dữ liệu; huỷ ctx giữa chừng thì không commit, lần chạy sau đọc lại.
- Mỗi lô mang `insert_deduplication_token` = bảng + khoảng offset từng partition (`playback_events/playback.events/0:10-25/…`), bảng có `non_replicated_deduplication_window = 1000`: INSERT thực ra đã thành công nhưng trả lỗi (timeout) thì lần gửi lại bị ClickHouse bỏ qua (integration test kiểm tra).
- Record hỏng (`ErrInvalid`: JSON sai, thiếu `ts`/`type`, id không phải UUID, số âm/ngoài `UInt32`) bị bỏ qua, log `skip invalid record`, đếm `Skipped`, vẫn commit cùng lô.
- Giữ `MergeTree` như spec, chấp nhận trùng.

## Phương án đã loại
- `ReplacingMergeTree`: cần khoá sắp xếp định danh duy nhất một sự kiện (không có event id; `(video_id, ts)` có thể trùng thật giữa hai người xem), và chỉ khử trùng khi merge (truy vấn phải `FINAL`).
- Thêm `event_id` vào `playback.events`: đổi schema sự kiện của M5 chỉ để khử trùng — để bản sau.
- Đưa record hỏng vào DLQ: thêm topic và công cụ cho dữ liệu phân tích ít giá trị; log + đếm là đủ cho MVP.
- Một consumer group cho cả hai topic: ClickHouse lỗi ở một bảng sẽ giữ offset của bảng kia.

## Hệ quả
- Trùng vẫn có thể xảy ra khi analytics chết giữa INSERT và commit, hoặc rebalance làm ranh giới lô khác đi (token khác). Đếm lượt xem có thể lệch nhẹ; `uniqExact(user_id)` trong SQL mẫu không bị ảnh hưởng.
- ClickHouse chết lâu: lô hiện tại giữ trong bộ nhớ (≤ 1000 record mỗi pipeline), phần còn lại nằm trong Kafka (lag tăng). Rebalance bị chặn tới khi ghi được — với một instance analytics thì không sao; chạy nhiều instance cần xem lại (`OnPartitionsRevoked`).
- Thứ tự giữa các partition không được giữ trong ClickHouse; truy vấn sắp theo `ts`.
```

`docs/decisions/0018-app-logs-async-sink.md` (mới):

```markdown
# ADR 0018: Log vào `app.logs` qua sink Kafka không chặn; analytics không tự log vào đó

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §7: log `slog` JSON, luôn có `service`, `request_id`, và `video_id` nếu có; ghi ra stdout và đồng thời gửi vào `app.logs` qua một `slog.Handler` bất đồng bộ, bỏ log khi buffer đầy để không chặn request. `analytics` là consumer của `app.logs`.

## Quyết định
- `logging.New(service, stdout, sink)`: một `slog.JSONHandler` ghi vào `io.MultiWriter(stdout, sink)` — `JSONHandler` gọi `Write` đúng một lần cho mỗi bản ghi, nên mỗi `Write` là một dòng hoàn chỉnh và hai đích nhận cùng một dòng. Bọc bởi `contextHandler`: nếu ctx có request id của chi và bản ghi chưa có `request_id` thì thêm vào. Handler của api gọi `…Context(r.Context(), …)`; `httpx.RequestLogger` thêm `video_id` khi route có `{id}` là UUID.
- `logging.KafkaSink.Write` chép dòng log (bỏ `\n`) thành một record `app.logs` không key và gọi `kgo.Client.TryProduce` — buffer của franz-go chính là hàng đợi có giới hạn: client riêng với `MaxBufferedRecords(4096)`, `RecordDeliveryTimeout(5s)`, acks leader, không idempotent, linger 50 ms. Đầy thì promise trả `ErrMaxBuffered` ngay; gửi lỗi (Kafka chết) cũng vào promise; cả hai chỉ tăng `Dropped`. `Write` luôn trả thành công nên stdout không bị ảnh hưởng. Khi tắt: `Flush` tối đa 2 giây, `Close`, in số dòng đã bỏ ra stdout.
- Vòng phản hồi: `analytics` dựng logger **không** có sink (chỉ stdout). Nếu nó tee vào `app.logs`, mỗi lần ClickHouse lỗi sẽ sinh log lỗi → thêm record `app.logs` → thêm lô lỗi. franz-go không log gì khi không cấu hình `WithLogger`, nên sink không tự sinh log.
- Chỉ `api` (lệnh server) và `transcoder worker` tee vào Kafka; lệnh phụ (`seed-admin`, `sign`, `transcoder file`) chỉ log ra stdout.

## Phương án đã loại
- Channel + goroutine riêng trước producer: thêm một tầng buffer nữa mà `TryProduce` đã cho đúng ngữ nghĩa "đầy thì bỏ, không chặn".
- Dùng `events.Producer.Publish`: chờ từng record (tới 10 giây khi broker chết) — chặn request.
- Analytics tee log nhưng lọc `service = "analytics"` khi ghi: vẫn tốn Kafka và vẫn phải lọc; không có lợi gì so với không gửi.

## Hệ quả
- Log của `analytics` chỉ xem được bằng `make logs-app`, không có trong `app_logs`.
- Kafka chết hoặc quá tải: `app_logs` thiếu dòng (stdout vẫn đủ). Số dòng bỏ in ra khi service tắt.
- Log gọi không kèm ctx (job nền, consumer) không có `request_id` — đúng, vì không thuộc request nào; log của transcoder có `video_id`.
```

- [ ] **Step 7: Kiểm tra và commit**

Run: `ls docs/decisions/ | tail -3 && grep -c 'M6' README.md docs/architecture.md docs/data-model.md docs/flows.md docs/operations.md`
Expected: `0016-clickhouse-http-startup-schema.md`, `0017-analytics-at-least-once.md`, `0018-app-logs-async-sink.md`; mỗi file đếm ≥ 1.

Run: `go test -count=1 ./...`
Expected: mọi package `ok` (gồm `internal/analytics` ~90 giây với ClickHouse và Redpanda thật).

```bash
git add README.md docs/architecture.md docs/data-model.md docs/flows.md docs/operations.md docs/decisions
git commit -m "docs: M6 analytics pipeline, app.logs and ClickHouse docs and ADRs"
```

---

## Self-review

**Độ phủ spec M6:**

| Yêu cầu | Task |
|---|---|
| `cmd/analytics` consume `playback.events` và `app.logs`, ghi theo lô vào ClickHouse (§3.1) | 5 (`Pipeline`), 6 (`Run`, `cmd/analytics`, `TestRunKafkaToClickHouse`), 7 (compose) |
| Ghi theo lô mỗi 1 giây hoặc 1000 bản ghi (§4.4 bước 4) | 5 (`DefaultMaxBatch`, `DefaultMaxWait`, `TestPipelineFlushesAtMaxBatchAndOnShutdown`, `TestPipelineFlushesAfterMaxWait`), 6 (2500 event = lô 1000/1000/500) |
| Bảng `playback_events`, `app_logs` đúng cột, engine, `ORDER BY`, TTL 30 ngày (§5) | 3 (`Schema`, `TestClickHouseSchemaInsertAndDedup` kiểm tra kiểu cột và `engine_full`) |
| SQL mẫu: top phim theo lượt play, tỉ lệ xem hết, tổng buffering theo bitrate (§5) | 7 (`scripts/analytics/*.sql`, `TestSampleQueries` so khớp kết quả, `make analytics-sql`), 8 (e2e chạy cả 4 câu) |
| `app.logs` là log JSON từ `slog` của mọi service (§3.3) | 1 (`KafkaSink`), 2 (api, transcoder worker), 6 (đi tới `app_logs`) |
| Log luôn có `service`, `request_id`, `video_id` nếu có (§7) | 1 (`New`, `contextHandler`), 2 (`RequestLogger` + `…Context`), 8 (e2e kiểm tra `request_id` 32 hex + `video_id` của api, `video_id` của transcoder) |
| Log ra stdout và đồng thời `app.logs` qua `slog.Handler` bất đồng bộ, bỏ khi buffer đầy, không chặn request (§7) | 1 (`TestNewTeesSameLineToStdoutAndKafka`, `TestKafkaSinkNeverBlocksWhenKafkaDown`: 1000 dòng < 1 giây khi Kafka không tồn tại) |
| Xử lý lỗi kiểu at-least-once như các consumer khác (§7) | 5 (commit sau INSERT, retry không bỏ lô, `TestPipelineCancelWhileRetryingDoesNotCommit`), 3 (khử trùng retry), 8 (`CH_DOWN=1`) |
| Integration test với testcontainers (§9) | 3, 6, 7 (ClickHouse 24.8, Redpanda) |
| E2E (§9), M6 "có SQL mẫu chạy được" (§10) | 8 |
| Tài liệu: data-model (schema ClickHouse, topic), flows (analytics), operations (ClickHouse, SQL mẫu), architecture, ADR (§11) | 9 |

Ngoài phạm vi M6 (đúng spec): trang admin, xem lỗi trên web (M7); outbox / không mất sự kiện khi Kafka chết (ADR 0007, 0015); khử trùng hoàn toàn (cần `event_id`, ADR 0017); chạy nhiều instance analytics (rebalance khi đang giữ lô, ADR 0017).

**Quét placeholder:** không có "TBD", "TODO", "tương tự Task N", "xử lý lỗi phù hợp"; mọi bước có code đầy đủ hoặc lệnh kèm kết quả mong đợi. Toàn bộ code và các cặp "thay A bằng B" trong plan được sinh từ một bản sao của nhánh `feat/mvp` (commit `86eb3af`) đã cài từng task và chạy thử, rồi được áp lại bằng script lên một bản sao sạch khác theo đúng thứ tự task (A khớp đúng một lần, lệnh `perl` của Task 2 cho ra đúng file đích); kết quả cuối giống hệt từng byte bản đã kiểm tra. Ở mỗi task: `gofmt -l` rỗng, `go vet ./...` và `go test -short ./...` xanh; thông báo lỗi ở các bước "xác nhận thất bại" là output thật. Cuối cùng `go test -count=1 ./...` (gồm FFmpeg, Postgres, MinIO, Redpanda, nginx, ClickHouse 24.8 trong container) đều `ok`; `TestKafkaSinkNeverBlocksWhenKafkaDown` chạy lặp `-count=5`, `-race`, `-cpu 1` đều xanh; test `Pipeline` chạy `-race -count=5`. Một stack compose riêng (project `streamfilm-m6test`, cổng 18088/18000/18123/19000/39092…, image tag `:m6test`, đã gỡ cùng volume và image sau khi chạy) dựng từ bản sao: `e2e-m6.sh` với `E2E_CLICKHOUSE_DOWN=1` → `OK  M6` (ClickHouse dừng 5 giây, analytics log `clickhouse insert failed, retrying`, sau đó đủ 7 sự kiện), rồi `e2e-m5.sh` → `OK  M5`, `e2e-m4.sh` → `OK  M4`, `e2e-m3.sh` → `OK  M3`; `analytics-sql.sh` in đủ 4 bảng.

Phát hiện khi chạy thử và đã sửa trong plan: (1) nginx ghi đè `X-Request-Id` bằng `$request_id`, nên e2e không tự đặt request id mà kiểm tra dòng `http request` của `/api/videos/{id}/play` có `request_id` 32 ký tự hex; (2) record hỏng dạng `not-json` làm bước `jq` của `e2e-m5` lỗi khi đọc lại topic, nên e2e-m6 ghi record JSON hợp lệ với `user_id` không phải UUID; (3) promise của franz-go chạy bất đồng bộ nên test đếm `Dropped` chờ sau `Close` thay vì đọc ngay.

**Nhất quán chữ ký:**
- `logging.New(service, stdout, kafka io.Writer)` dùng ở `cmd/api` (`nil` cho lệnh phụ, `logSink` cho server), `cmd/transcoder` (`nil` cho `file`, sink cho worker), `cmd/analytics` (`nil`), test. `*KafkaSink` thoả `io.Writer`; `*kgo.Client` thoả `logging.Producer` (`TryProduce`, `Flush`, `Close`).
- `analytics.Mapper` = `func(*kgo.Record) ([]byte, error)`: `MapPlayback`, `MapLog`, `testMap`. `*analytics.Client` thoả `Inserter` (`Insert(ctx, table, body, token)`); `*kgo.Client` thoả `Source` (`PollRecords`, `CommitRecords`, `AllowRebalance`), `fakeSource` cũng vậy.
- Tên bảng `TablePlaybackEvents`/`TableAppLogs` dùng ở `Schema` (chuỗi trong DDL), `Run`, test; tên group `GroupPlayback`/`GroupLogs` khớp tài liệu (`analytics-playback`, `analytics-logs`).
- JSON ↔ ClickHouse: `PlaybackRow` (`ts`, `user_id`, `video_id`, `type`, `position_s`, `bitrate_kbps`, `buffering_ms`) và `LogRow` (`ts`, `service`, `level`, `msg`, `request_id`, `video_id`, `attrs`) trùng tên và thứ tự cột của `Schema`; `CHTime` ↔ `DateTime64(3, 'UTC')`; `events.PlaybackEvent` (M5) là đầu vào của `ParsePlayback`; khoá `slog` (`time`, `level`, `msg`) là đầu vào của `ParseLog`.
- `config.Config.ClickHouse{URL,DB,User,Password}` ↔ env `CLICKHOUSE_{URL,DB,USER,PASSWORD}` ↔ `.env.example` ↔ compose service `analytics` ↔ `scripts/analytics-sql.sh` / `scripts/e2e-m6.sh` (cùng tên biến, cùng mặc định `streamfilm`).
- Hằng số: 1000 / 1 giây / 10 giây / 4096 / 5 giây / 2 giây giống nhau giữa code, Global Constraints, ADR và `docs/`.

## Các kế hoạch tiếp theo

1. **M7** — Trang admin trong `web/app` (danh sách video, upload video và phụ đề, retry, xem lỗi và DLQ, có thể đọc `app_logs` theo `video_id`), `scripts/e2e.sh` đầy đủ chạy tất cả e2e, rà toàn bộ tài liệu mục 11 cho khớp code.
