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
