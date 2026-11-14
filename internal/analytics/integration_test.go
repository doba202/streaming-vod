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
