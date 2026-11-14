// Command analytics consume playback.events và app.logs từ Kafka, ghi theo lô (1000 record hoặc 1 giây)
// vào ClickHouse (M6, spec §3.1, §4.4). Bảng được tạo (IF NOT EXISTS) mỗi lần khởi động.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"streamfilm/internal/analytics"
	"streamfilm/internal/config"
	"streamfilm/internal/logging"
)

func main() {
	// Chỉ log ra stdout, không tee vào app.logs: analytics là consumer của app.logs, log của chính nó
	// (nhất là khi ClickHouse lỗi) đi vào đó sẽ tạo vòng phản hồi (ADR 0018).
	log := logging.New("analytics", os.Stdout, nil)
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("analytics stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ch := analytics.NewClient(cfg.ClickHouseURL, cfg.ClickHouseDB, cfg.ClickHouseUser, cfg.ClickHousePassword)
	if err := analytics.Migrate(ctx, ch); err != nil {
		return err
	}
	log.Info("clickhouse schema ready", "url", cfg.ClickHouseURL, "db", cfg.ClickHouseDB)
	return analytics.Run(ctx, cfg.KafkaBrokers, ch, log)
}
