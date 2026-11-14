package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"streamfilm/internal/auth"
	"streamfilm/internal/config"
	"streamfilm/internal/db"
	"streamfilm/internal/signing"
	"streamfilm/migrations"
)

// seedAdmin: `api seed-admin` — tạo admin đầu tiên (make seed-admin). Chạy lại thì đặt lại mật khẩu.
func seedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ContinueOnError)
	email := fs.String("email", os.Getenv("ADMIN_EMAIL"), "email admin (mặc định $ADMIN_EMAIL)")
	password := fs.String("password", os.Getenv("ADMIN_PASSWORD"), "mật khẩu, 8-72 byte (mặc định $ADMIN_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		return err
	}
	svc := &auth.Service{Users: auth.NewRepo(pool), NewID: uuid.NewString}
	u, created, err := svc.SeedAdmin(ctx, *email, *password)
	if err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}
	if created {
		fmt.Printf("admin created: %s (%s)\n", u.Email, u.ID)
	} else {
		fmt.Printf("admin updated: %s (%s)\n", u.Email, u.ID)
	}
	return nil
}

// signURL: `api sign -id ID` — in URL CDN đã ký (dùng với dev player hoặc video từ `transcoder file`).
func signURL(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	id := fs.String("id", "", "video id (thư mục hls/{id}/)")
	file := fs.String("file", "master.m3u8", "file trong thư mục video")
	ttl := fs.Duration("ttl", signing.TTL, "thời hạn URL (âm để tạo URL đã hết hạn khi thử)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Load()
	s, err := signing.NewSigner(cfg.CDNSigningSecret, *ttl, time.Now)
	if err != nil {
		return err
	}
	path, exp, err := s.Path(*id, *file)
	if err != nil {
		return err
	}
	fmt.Println(cfg.CDNBaseURL + path)
	fmt.Fprintf(os.Stderr, "expires %s\n", exp.Format(time.RFC3339))
	return nil
}
