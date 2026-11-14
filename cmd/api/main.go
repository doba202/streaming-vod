// Command api là HTTP API của Streamfilm.
//
//	api                                   chạy HTTP server (mặc định)
//	api seed-admin [-email E] [-password P] tạo/đặt lại tài khoản admin (mặc định đọc ADMIN_EMAIL, ADMIN_PASSWORD)
//	api sign -id ID [-file F] [-ttl 2h]     in URL CDN đã ký cho một video (debug, dev player)
//
// M3: đăng ký/đăng nhập (JWT cookie), /api/admin/* chỉ cho role admin, catalog và /play cho user.
// M5: phụ đề (admin upload), poster/sprite ký URL, sự kiện xem → watch_progress + playback.events, "Xem tiếp".
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

	"github.com/google/uuid"

	"streamfilm/internal/auth"
	"streamfilm/internal/catalog"
	"streamfilm/internal/config"
	"streamfilm/internal/db"
	"streamfilm/internal/events"
	"streamfilm/internal/logging"
	"streamfilm/internal/playback"
	"streamfilm/internal/signing"
	"streamfilm/internal/storage"
	"streamfilm/internal/upload"
	"streamfilm/migrations"
)

func main() {
	// Lệnh phụ (seed-admin, sign) chỉ log ra stdout; server (run) tee thêm vào app.logs.
	log := logging.New("api", os.Stdout, nil)
	slog.SetDefault(log)
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "seed-admin":
		err = seedAdmin(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "sign":
		err = signURL(os.Args[2:])
	default:
		err = run(log)
	}
	if err != nil {
		log.Error("api stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	// Kiểm tra secret trước khi kết nối gì: thiếu thì dừng ngay với thông báo rõ.
	tokens, err := auth.NewTokens(cfg.JWTSecret, auth.SessionTTL, time.Now)
	if err != nil {
		return err
	}
	signer, err := signing.NewSigner(cfg.CDNSigningSecret, signing.TTL, time.Now)
	if err != nil {
		return err
	}
	// M6: log ra stdout và đồng thời vào app.logs (spec §7). Defer đầu tiên nên sink đóng sau cùng,
	// gom được cả log lúc tắt; Kafka chậm/chết thì dòng log bị bỏ, không chặn request.
	logSink, err := logging.DialKafka(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer func() {
		logSink.Close()
		logging.New("api", os.Stdout, nil).Info("app.logs sink closed", "dropped", logSink.Dropped())
	}()
	log = logging.New("api", os.Stdout, logSink)
	slog.SetDefault(log)
	go auth.WarmUp() // bcrypt dummy hash chạy nền trong lúc kết nối DB, Kafka
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
	progress := playback.NewRepo(pool)
	// playback.events: publish ở nền, Kafka chết thì bỏ sự kiện, không làm hỏng việc xem (spec §7).
	playbackEvents := playback.NewBestEffort(producer, playback.PublishTimeout, playback.MaxInFlight, log)
	svc := &upload.Service{Videos: repo, Objects: store, Pub: producer, BucketRaw: cfg.BucketRaw,
		Now: time.Now, NewID: uuid.NewString, Log: log}

	authSvc := &auth.Service{Users: auth.NewRepo(pool), Tokens: tokens, NewID: uuid.NewString}
	handler := newRouter(log, routes{
		Tokens:    tokens,
		Auth:      &auth.Handler{Svc: authSvc, SecureCookie: cfg.CookieSecure, Log: log},
		Viewer:    &catalog.ViewerHandler{Videos: repo, Progress: progress, Signer: signer, CDNBaseURL: cfg.CDNBaseURL, Log: log},
		Events:    &playback.Handler{Videos: repo, Progress: progress, Events: playbackEvents, Now: time.Now, Log: log},
		Upload:    &upload.Handler{Svc: svc, Log: log},
		Admin:     &catalog.AdminHandler{Videos: repo, Log: log},
		Subtitles: &catalog.SubtitleHandler{Videos: repo, Objects: store, BucketHLS: cfg.BucketHLS, Log: log},
	})

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

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
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
	playbackEvents.Wait() // trước producer.Close (defer): mỗi publish chờ tối đa PublishTimeout
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
