// Command api là HTTP API của Streamfilm.
// M2: upload (presigned multipart), danh sách video cho admin, consumer cập nhật trạng thái,
// job dọn upload bỏ dở. Chưa có auth: M3 thêm middleware role admin cho nhóm /api/admin.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"streamfilm/internal/catalog"
	"streamfilm/internal/config"
	"streamfilm/internal/db"
	"streamfilm/internal/events"
	"streamfilm/internal/httpx"
	"streamfilm/internal/storage"
	"streamfilm/internal/upload"
	"streamfilm/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "api")
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		return err
	}
	log.Info("migrations applied", "versions", applied)

	store := storage.New(storage.Config{
		Endpoint: cfg.S3Endpoint, PublicEndpoint: cfg.S3PublicEndpoint, Region: cfg.S3Region,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
	})
	producer, err := events.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	consumer, err := events.NewConsumer(cfg.KafkaBrokers, "api", log,
		events.TopicVideoProcessing, events.TopicVideoTranscoded, events.TopicVideoFailed)
	if err != nil {
		return err
	}
	defer consumer.Close()

	repo := catalog.NewRepo(pool)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw,
		Now: time.Now, NewID: uuid.NewString, Log: log}

	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer, httpx.RequestLogger(log))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	(&upload.Handler{Svc: svc, Log: log}).Routes(r)
	(&catalog.AdminHandler{Videos: repo, Log: log}).Routes(r)

	updater := &catalog.StatusUpdater{Store: repo, Log: log}
	cleaner := &upload.Cleaner{Videos: repo, Objects: store, BucketRaw: cfg.BucketRaw,
		MaxAge: cfg.StaleUploadAfter, Now: time.Now, Log: log}

	var (
		wg          sync.WaitGroup
		consumerMu  sync.Mutex
		consumerErr error
	)
	wg.Go(func() {
		if err := consumer.Run(ctx, updater.Handle); err != nil {
			log.Error("status consumer stopped", "err", err)
			consumerMu.Lock()
			consumerErr = err
			consumerMu.Unlock()
			stop()
		}
	})
	wg.Go(func() { cleaner.Run(ctx, cfg.CleanupInterval) })

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		stop() // lần Ctrl+C thứ hai dùng xử lý mặc định (thoát ngay)
		log.Info("shutting down")
	case serveErr = <-errc:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && serveErr == nil {
		serveErr = err
	}
	wg.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	if serveErr != nil {
		return serveErr
	}
	consumerMu.Lock()
	defer consumerMu.Unlock()
	if consumerErr != nil {
		return fmt.Errorf("status consumer: %w", consumerErr)
	}
	return nil
}
