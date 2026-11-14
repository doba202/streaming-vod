// Package config đọc cấu hình từ biến môi trường.
package config

import "os"

type Config struct {
	S3Endpoint, S3Region, S3AccessKey, S3SecretKey string
	BucketRaw, BucketHLS                           string
	CDNBaseURL                                     string
	FFmpegPath, FFprobePath                        string
}

func Load() Config {
	return Config{
		S3Endpoint:  get("S3_ENDPOINT", "http://localhost:9000"),
		S3Region:    get("S3_REGION", "us-east-1"),
		S3AccessKey: get("S3_ACCESS_KEY", "streamfilm"),
		S3SecretKey: get("S3_SECRET_KEY", "streamfilm-secret"),
		BucketRaw:   get("S3_BUCKET_RAW", "raw"),
		BucketHLS:   get("S3_BUCKET_HLS", "hls"),
		CDNBaseURL:  get("CDN_BASE_URL", "http://localhost:8088"),
		FFmpegPath:  get("FFMPEG_PATH", "ffmpeg"),
		FFprobePath: get("FFPROBE_PATH", "ffprobe"),
	}
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
