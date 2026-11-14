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
