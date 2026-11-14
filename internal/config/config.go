// Package config đọc cấu hình từ biến môi trường.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	S3Endpoint, S3PublicEndpoint, S3Region, S3AccessKey, S3SecretKey string
	BucketRaw, BucketHLS                                             string
	CDNBaseURL                                                       string
	FFmpegPath, FFprobePath                                          string
	DatabaseURL                                                      string
	KafkaBrokers                                                     []string
	HTTPAddr                                                         string
	WorkDir                                                          string
	StaleUploadAfter, CleanupInterval                                time.Duration
	JWTSecret, CDNSigningSecret                                      string
	CookieSecure                                                     bool
}

func Load() Config {
	endpoint := get("S3_ENDPOINT", "http://localhost:9000")
	return Config{
		S3Endpoint: endpoint,
		// Địa chỉ mà client bên ngoài (trình duyệt, curl trên host) dùng để upload bằng presigned URL.
		S3PublicEndpoint: get("S3_PUBLIC_ENDPOINT", endpoint),
		S3Region:         get("S3_REGION", "us-east-1"),
		S3AccessKey:      get("S3_ACCESS_KEY", "streamfilm"),
		S3SecretKey:      get("S3_SECRET_KEY", "streamfilm-secret"),
		BucketRaw:        get("S3_BUCKET_RAW", "raw"),
		BucketHLS:        get("S3_BUCKET_HLS", "hls"),
		CDNBaseURL:       get("CDN_BASE_URL", "http://localhost:8088"),
		FFmpegPath:       get("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:      get("FFPROBE_PATH", "ffprobe"),
		DatabaseURL:      get("DATABASE_URL", "postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable"),
		KafkaBrokers:     splitList(get("KAFKA_BROKERS", "localhost:29092")),
		HTTPAddr:         get("HTTP_ADDR", ":8000"),
		WorkDir:          get("WORK_DIR", os.TempDir()),
		StaleUploadAfter: getDuration("STALE_UPLOAD_AFTER", 24*time.Hour),
		CleanupInterval:  getDuration("CLEANUP_INTERVAL", time.Hour),
		// Không có mặc định: api từ chối khởi động nếu thiếu (auth.NewTokens, signing.NewSigner kiểm tra).
		JWTSecret:        os.Getenv("JWT_SECRET"),
		CDNSigningSecret: os.Getenv("CDN_SIGNING_SECRET"),
		// false khi chạy http://localhost; bật khi có HTTPS.
		CookieSecure: getBool("COOKIE_SECURE", false),
	}
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getDuration đọc duration kiểu Go ("24h", "90m"); rỗng, sai định dạng hoặc <= 0 thì dùng mặc định.
func getDuration(key string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(key))
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// getBool đọc "true"/"false"/"1"/"0"; rỗng hoặc sai định dạng thì dùng mặc định.
func getBool(key string, def bool) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return b
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
