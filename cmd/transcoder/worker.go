package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/config"
	"streamfilm/internal/events"
	"streamfilm/internal/logging"
	"streamfilm/internal/transcode"
	"streamfilm/internal/worker"
)

// runWorker consume video.uploaded (group "transcoder"), mỗi lần một job.
// Scale ngang bằng cách chạy thêm worker, tối đa bằng số partition (3).
func runWorker(ctx context.Context, cfg config.Config) error {
	// M6: log ra stdout và app.logs; đặt trước khi tạo consumer/worker vì chúng dùng slog.Default().
	logSink, err := logging.DialKafka(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer func() {
		logSink.Close()
		logging.New("transcoder", os.Stdout, nil).Info("app.logs sink closed", "dropped", logSink.Dropped())
	}()
	slog.SetDefault(logging.New("transcoder", os.Stdout, logSink))

	producer, err := events.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	// Handler chạy nhiều phút (transcode + backoff 5s/30s/2m) trong khi consumer chặn rebalance lúc đang poll
	// (BlockRebalanceOnPoll): nới rebalance timeout để group không đá worker ra giữa job. Heartbeat chạy nền
	// nên session timeout chỉ cần đủ rộng cho sự cố mạng ngắn.
	consumer, err := events.NewConsumerWithOpts(cfg.KafkaBrokers, "transcoder", slog.Default(),
		[]string{events.TopicVideoUploaded},
		kgo.RebalanceTimeout(30*time.Minute), kgo.SessionTimeout(60*time.Second))
	if err != nil {
		return err
	}
	defer consumer.Close()

	w := &worker.Worker{
		Transcoder: transcode.Runner{FFmpegPath: cfg.FFmpegPath, FFprobePath: cfg.FFprobePath},
		Store:      newStore(cfg),
		Pub:        producer,
		BucketRaw:  cfg.BucketRaw,
		BucketHLS:  cfg.BucketHLS,
		WorkDir:    cfg.WorkDir,
		Backoffs:   worker.DefaultBackoffs,
		Sleep:      worker.SleepCtx,
		Log:        slog.Default(),
	}
	slog.Info("worker started", "brokers", cfg.KafkaBrokers, "topic", events.TopicVideoUploaded)
	err = consumer.Run(ctx, w.Handle)
	slog.Info("worker stopped")
	return err
}
