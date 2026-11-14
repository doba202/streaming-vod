package analytics

import (
	"context"
	"strings"
	"testing"

	"streamfilm/internal/testutil"
)

func startClickHouse(t *testing.T) *Client {
	t.Helper()
	ch := testutil.StartClickHouse(t)
	c := NewClient(ch.URL, ch.DB, ch.User, ch.Password)
	// Chạy hai lần: Migrate phải idempotent (analytics chạy nó mỗi lần khởi động).
	for range 2 {
		if err := Migrate(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func queryString(t *testing.T, c *Client, q string) string {
	t.Helper()
	out, err := c.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestClickHouseSchemaInsertAndDedup(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()

	got := queryString(t, c, "SELECT name, type FROM system.columns WHERE database = currentDatabase() "+
		"AND table = 'playback_events' ORDER BY position FORMAT CSV")
	want := `"ts","DateTime64(3, 'UTC')"
"user_id","UUID"
"video_id","UUID"
"type","LowCardinality(String)"
"position_s","UInt32"
"bitrate_kbps","UInt32"
"buffering_ms","UInt32"`
	if got != want {
		t.Errorf("playback_events columns:\n%s", got)
	}
	if got := queryString(t, c, "SELECT engine_full FROM system.tables WHERE database = currentDatabase() "+
		"AND name = 'app_logs'"); !strings.Contains(got, "ORDER BY (service, ts) TTL toDateTime(ts) + toIntervalDay(30)") {
		t.Errorf("app_logs engine = %s", got)
	}

	body := []byte(`{"ts":"2026-11-15 10:00:00.123","user_id":"11111111-1111-1111-1111-111111111111","video_id":"22222222-2222-2222-2222-222222222222","type":"play","position_s":0,"bitrate_kbps":2800,"buffering_ms":0}
{"ts":"2026-11-15 10:00:10.000","user_id":"11111111-1111-1111-1111-111111111111","video_id":"22222222-2222-2222-2222-222222222222","type":"heartbeat","position_s":10,"bitrate_kbps":2800,"buffering_ms":0}
`)
	// Cùng token gửi hai lần (retry sau timeout) → chỉ ghi một lần; token khác → ghi thêm.
	for _, token := range []string{"t1", "t1", "t2"} {
		if err := c.Insert(ctx, TablePlaybackEvents, body, token); err != nil {
			t.Fatal(err)
		}
	}
	if got := queryString(t, c, "SELECT count() FROM playback_events"); got != "4" {
		t.Errorf("count = %s, want 4 (t1 dedup, t2 mới)", got)
	}
	if got := queryString(t, c, "SELECT toString(max(ts)) FROM playback_events"); got != "2026-11-15 10:00:10.000" {
		t.Errorf("max(ts) = %s", got)
	}

	// Một row sai kiểu làm hỏng cả lô: lỗi trả về có mã HTTP và thông điệp của ClickHouse.
	err := c.Insert(ctx, TablePlaybackEvents, []byte(`{"ts":"2026-11-15 10:00:00.000","user_id":"not-a-uuid"}`+"\n"), "")
	if err == nil || !strings.Contains(err.Error(), "clickhouse: HTTP ") {
		t.Errorf("insert sai kiểu: err = %v", err)
	}
	if err := NewClient(c.base, c.db, c.user, "wrong").Exec(ctx, "SELECT 1"); err == nil {
		t.Error("sai mật khẩu phải lỗi")
	}
}
