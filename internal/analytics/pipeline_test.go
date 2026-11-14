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

// SIGTERM giữa lúc INSERT thử lại: thử thêm đúng một lần với ctx không bị huỷ, cùng token, rồi commit.
func TestPipelineRetriesInFlightBatchOnceOnShutdown(t *testing.T) {
	p, src, sink, j := newTestPipeline(1, time.Hour, 1)
	p.Backoff = func(int) time.Duration { return time.Hour } // đang chờ backoff thì bị huỷ
	stop := start(t, p)
	src.push("a")
	eventually(t, "lần INSERT đầu thất bại", func() bool { c, _, _ := sink.snapshot(); return c >= 1 })
	stop()
	calls, bodies, tokens := sink.snapshot()
	if calls != 2 || len(bodies) != 1 || tokens[0] != "tbl/t/0:0-0" {
		t.Errorf("calls=%d bodies=%q tokens=%q", calls, bodies, tokens)
	}
	if got := strings.Join(j.list(), ","); got != "insert-fail,insert,commit 1" {
		t.Errorf("ops = %s", got)
	}
}
