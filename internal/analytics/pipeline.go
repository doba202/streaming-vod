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
				if err := p.flush(sctx, &b, 0); err != nil {
					p.Log.Warn("pending batch not written on shutdown, will be re-read", "records", len(b.recs), "err", err)
				}
				cancel()
			}
			p.Source.AllowRebalance()
			return nil
		}
		if len(b.recs) > 0 && (len(b.recs) >= p.MaxBatch || !time.Now().Before(b.deadline)) {
			if err := p.flush(ctx, &b, 0); err != nil {
				// ctx bị huỷ khi đang INSERT/chờ backoff (SIGTERM): thử đúng một lần nữa, cùng lô và cùng
				// dedup token, trong ShutdownTimeout. Không thành công thì không commit, lô được đọc lại.
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
				err = p.flush(sctx, &b, 1)
				cancel()
				if err != nil {
					p.Log.Warn("in-flight batch not written on shutdown, will be re-read", "records", len(b.recs), "err", err)
				}
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

// flush ghi lô (thử lại tới khi thành công, ctx bị huỷ hoặc hết maxAttempts; 0 = không giới hạn) rồi commit offset của mọi record trong lô.
func (p *Pipeline) flush(ctx context.Context, b *batch, maxAttempts int) error {
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
			if maxAttempts > 0 && attempt >= maxAttempts {
				return err
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
