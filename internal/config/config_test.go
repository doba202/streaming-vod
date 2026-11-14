package config

import "testing"

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
