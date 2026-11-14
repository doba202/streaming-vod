package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"S3_ENDPOINT", "S3_BUCKET_HLS", "CDN_BASE_URL", "FFMPEG_PATH"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.S3Endpoint != "http://localhost:9000" {
		t.Errorf("S3Endpoint = %q", c.S3Endpoint)
	}
	if c.BucketHLS != "hls" || c.BucketRaw != "raw" {
		t.Errorf("buckets = %q %q", c.BucketRaw, c.BucketHLS)
	}
	if c.CDNBaseURL != "http://localhost:8088" {
		t.Errorf("CDNBaseURL = %q", c.CDNBaseURL)
	}
	if c.FFmpegPath != "ffmpeg" || c.FFprobePath != "ffprobe" {
		t.Errorf("ffmpeg paths = %q %q", c.FFmpegPath, c.FFprobePath)
	}
}

func TestLoadOverride(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_BUCKET_HLS", "media")
	c := Load()
	if c.S3Endpoint != "http://minio:9000" || c.BucketHLS != "media" {
		t.Errorf("override not applied: %+v", c)
	}
}

func TestLoadM2Defaults(t *testing.T) {
	for _, k := range []string{"S3_ENDPOINT", "S3_PUBLIC_ENDPOINT", "DATABASE_URL", "KAFKA_BROKERS",
		"HTTP_ADDR", "WORK_DIR", "STALE_UPLOAD_AFTER", "CLEANUP_INTERVAL"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.S3PublicEndpoint != "http://localhost:9000" {
		t.Errorf("S3PublicEndpoint = %q", c.S3PublicEndpoint)
	}
	if c.DatabaseURL != "postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable" {
		t.Errorf("DatabaseURL = %q", c.DatabaseURL)
	}
	if len(c.KafkaBrokers) != 1 || c.KafkaBrokers[0] != "localhost:29092" {
		t.Errorf("KafkaBrokers = %v", c.KafkaBrokers)
	}
	if c.HTTPAddr != ":8000" {
		t.Errorf("HTTPAddr = %q", c.HTTPAddr)
	}
	if c.WorkDir != os.TempDir() {
		t.Errorf("WorkDir = %q", c.WorkDir)
	}
	if c.StaleUploadAfter != 24*time.Hour || c.CleanupInterval != time.Hour {
		t.Errorf("durations = %v %v", c.StaleUploadAfter, c.CleanupInterval)
	}
}

func TestLoadM2Override(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "http://minio:9000")
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	t.Setenv("KAFKA_BROKERS", "kafka:9092, other:9092 ,")
	t.Setenv("STALE_UPLOAD_AFTER", "90m")
	t.Setenv("CLEANUP_INTERVAL", "not-a-duration")
	c := Load()
	if c.S3PublicEndpoint != "http://minio:9000" {
		t.Errorf("S3PublicEndpoint phải mặc định bằng S3Endpoint, got %q", c.S3PublicEndpoint)
	}
	if len(c.KafkaBrokers) != 2 || c.KafkaBrokers[0] != "kafka:9092" || c.KafkaBrokers[1] != "other:9092" {
		t.Errorf("KafkaBrokers = %#v", c.KafkaBrokers)
	}
	if c.StaleUploadAfter != 90*time.Minute {
		t.Errorf("StaleUploadAfter = %v", c.StaleUploadAfter)
	}
	if c.CleanupInterval != time.Hour {
		t.Errorf("CleanupInterval sai định dạng phải về mặc định, got %v", c.CleanupInterval)
	}
}

func TestLoadM3(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("CDN_SIGNING_SECRET", "")
	t.Setenv("COOKIE_SECURE", "")
	c := Load()
	if c.JWTSecret != "" || c.CDNSigningSecret != "" || c.CookieSecure {
		t.Errorf("defaults = %q %q %v", c.JWTSecret, c.CDNSigningSecret, c.CookieSecure)
	}
	t.Setenv("JWT_SECRET", "jwt-secret")
	t.Setenv("CDN_SIGNING_SECRET", "cdn-secret")
	t.Setenv("COOKIE_SECURE", "true")
	c = Load()
	if c.JWTSecret != "jwt-secret" || c.CDNSigningSecret != "cdn-secret" || !c.CookieSecure {
		t.Errorf("override = %q %q %v", c.JWTSecret, c.CDNSigningSecret, c.CookieSecure)
	}
	t.Setenv("COOKIE_SECURE", "not-bool")
	if Load().CookieSecure {
		t.Error("COOKIE_SECURE sai định dạng phải về false")
	}
}
