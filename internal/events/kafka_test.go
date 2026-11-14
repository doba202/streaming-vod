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
