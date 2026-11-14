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
