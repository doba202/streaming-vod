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
