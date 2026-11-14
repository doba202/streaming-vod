package analytics

import (
	"context"
	"fmt"
)

// Tên bảng ClickHouse (spec §5).
const (
	TablePlaybackEvents = "playback_events"
	TableAppLogs        = "app_logs"
)

// Schema: các câu tạo bảng, idempotent (IF NOT EXISTS), chạy mỗi lần analytics khởi động (ADR 0016).
// ts là DateTime64(3, 'UTC'): analytics luôn gửi giờ UTC dạng "YYYY-MM-DD hh:mm:ss.sss".
// non_replicated_deduplication_window: nhớ hash của 1000 lô gần nhất để lần gửi lại cùng
// insert_deduplication_token (retry sau timeout) không ghi trùng (ADR 0017).
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS playback_events (
	ts DateTime64(3, 'UTC'),
	user_id UUID,
	video_id UUID,
	type LowCardinality(String),
	position_s UInt32,
	bitrate_kbps UInt32,
	buffering_ms UInt32
) ENGINE = MergeTree
ORDER BY (video_id, ts)
SETTINGS non_replicated_deduplication_window = 1000`,
	`CREATE TABLE IF NOT EXISTS app_logs (
	ts DateTime64(3, 'UTC'),
	service LowCardinality(String),
	level LowCardinality(String),
	msg String,
	request_id String,
	video_id String,
	attrs String
) ENGINE = MergeTree
ORDER BY (service, ts)
TTL toDateTime(ts) + INTERVAL 30 DAY
SETTINGS non_replicated_deduplication_window = 1000`,
}

// Migrate tạo các bảng nếu chưa có.
func Migrate(ctx context.Context, c *Client) error {
	for _, q := range Schema {
		if err := c.Exec(ctx, q); err != nil {
			return fmt.Errorf("migrate clickhouse: %w", err)
		}
	}
	return nil
}
