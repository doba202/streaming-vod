// Command transcoder biến video thành HLS. M1: chế độ "file" chạy một lần từ CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"streamfilm/internal/config"
	"streamfilm/internal/storage"
	"streamfilm/internal/transcode"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "transcoder")
	slog.SetDefault(logger)

	if len(os.Args) < 2 || os.Args[1] != "file" {
		fmt.Fprintln(os.Stderr, "usage: transcoder file -in <video> -id <video_id>")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("file", flag.ExitOnError)
	in := fs.String("in", "", "đường dẫn file video đầu vào")
	id := fs.String("id", "", "video id, dùng làm prefix trong bucket hls")
	fs.Parse(os.Args[2:])
	if *in == "" || *id == "" {
		fs.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runFile(ctx, config.Load(), *in, *id); err != nil {
		slog.Error("transcode failed", "video_id", *id, "err", err)
		os.Exit(1)
	}
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
	slog.Info("transcode done", "video_id", id, "duration_s", res.Probe.DurationSec, "rungs", rungs)

	store := storage.New(storage.Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
	})
	n, err := store.UploadDir(ctx, cfg.BucketHLS, id, out)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/hls/%s/master.m3u8", cfg.CDNBaseURL, id)
	slog.Info("upload done", "video_id", id, "files", n, "url", url)
	fmt.Println(url)
	return nil
}
