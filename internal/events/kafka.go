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
