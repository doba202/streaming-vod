package playback

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"streamfilm/internal/events"
)

// Giá trị mặc định của BestEffort trong api.
const (
	PublishTimeout = 2 * time.Second
	MaxInFlight    = 256
)

type Publisher interface {
	Publish(ctx context.Context, topic string, ev events.Event) error
}

// BestEffort publish playback.events ở nền, không bao giờ chặn request của player (spec §7: Kafka
// không khả dụng → ghi log rồi bỏ sự kiện, không làm hỏng việc xem). Mỗi sự kiện chờ tối đa timeout;
// đang có maxInFlight lần publish chưa xong (Kafka chậm hoặc chết) thì sự kiện mới bị bỏ ngay,
// nên số goroutine và bộ nhớ có giới hạn.
type BestEffort struct {
	pub     Publisher
	timeout time.Duration
	log     *slog.Logger
	sem     chan struct{}
	wg      sync.WaitGroup
	dropped atomic.Int64
}

func NewBestEffort(pub Publisher, timeout time.Duration, maxInFlight int, log *slog.Logger) *BestEffort {
	return &BestEffort{pub: pub, timeout: timeout, log: log, sem: make(chan struct{}, maxInFlight)}
}

// Send trả về ngay: true nếu sự kiện được đưa đi publish, false nếu bị bỏ vì quá nhiều publish đang chờ.
func (b *BestEffort) Send(ev events.PlaybackEvent) bool {
	select {
	case b.sem <- struct{}{}:
	default:
		b.dropped.Add(1)
		b.log.Warn("playback event dropped: too many publishes in flight", "video_id", ev.VideoID, "type", ev.Type)
		return false
	}
	b.wg.Go(func() {
		defer func() { <-b.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
		defer cancel()
		if err := b.pub.Publish(ctx, events.TopicPlaybackEvents, ev); err != nil {
			b.dropped.Add(1)
			b.log.Warn("playback event dropped", "video_id", ev.VideoID, "type", ev.Type, "err", err)
		}
	})
	return true
}

// Wait chờ các publish đang chạy (mỗi cái tối đa timeout). Gọi khi tắt api, trước khi đóng producer.
func (b *BestEffort) Wait() { b.wg.Wait() }

// Dropped: tổng số sự kiện đã bỏ (quá tải hoặc publish lỗi).
func (b *BestEffort) Dropped() int64 { return b.dropped.Load() }
