// Command transcoder biến video thành HLS.
//
//	transcoder file -in <video> -id <video_id>   chạy một lần từ CLI (M1)
//	transcoder worker                             consume video.uploaded từ Kafka (M2)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"streamfilm/internal/config"
	"streamfilm/internal/storage"
	"streamfilm/internal/transcode"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "transcoder")
	slog.SetDefault(logger)
	if len(os.Args) < 2 {
		usage()
	}

	// SIGTERM (docker stop) huỷ ctx: FFmpeg bị kill, offset của job dở không được commit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	cfg := config.Load()
	var err error
	switch os.Args[1] {
	case "file":
		err = fileCmd(ctx, cfg, os.Args[2:])
	case "worker":
		err = runWorker(ctx, cfg)
	default:
		usage()
	}
	stop()
	if err != nil {
		slog.Error("transcoder failed", "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:\n  transcoder file -in <video> -id <video_id>\n  transcoder worker")
	os.Exit(2)
}

func newStore(cfg config.Config) *storage.Store {
	return storage.New(storage.Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
	})
}

func fileCmd(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("file", flag.ExitOnError)
	in := fs.String("in", "", "đường dẫn file video đầu vào")
	id := fs.String("id", "", "video id, dùng làm prefix trong bucket hls")
	fs.Parse(args)
	if *in == "" || *id == "" {
		fs.Usage()
		os.Exit(2)
	}
	return runFile(ctx, cfg, *in, *id)
}

func runFile(ctx context.Context, cfg config.Config, input, id string) error {
	work, err := os.MkdirTemp("", "streamfilm-"+id+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	out := filepath.Join(work, "hls")

	slog.Info("transcode start", "video_id", id, "input", input)
	res, err := transcode.Runner{FFmpegPath: cfg.FFmpegPath, FFprobePath: cfg.FFprobePath}.Transcode(ctx, input, out)
	if err != nil {
		return err
	}
	rungs := make([]string, len(res.Rungs))
	for i, r := range res.Rungs {
		rungs[i] = r.Name
	}
	slog.Info("transcode done", "video_id", id, "duration_s", res.Probe.DurationSec, "rungs", rungs,
		"poster", res.PosterFile, "sprite_sheets", res.SpriteSheets)

	n, err := newStore(cfg).UploadDir(ctx, cfg.BucketHLS, id, out)
	if err != nil {
		return err
	}
	// M3: nginx chỉ phục vụ URL có chữ ký; lấy URL bằng `make sign-url ID=...`.
	slog.Info("upload done", "video_id", id, "files", n, "prefix", cfg.BucketHLS+"/"+id+"/")
	fmt.Printf("uploaded %s/%s/ - signed URL: make sign-url ID=%s\n", cfg.BucketHLS, id, id)
	return nil
}
