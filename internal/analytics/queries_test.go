package analytics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Mọi câu SQL mẫu trong scripts/analytics chạy được trên schema thật và trả kết quả đúng với dữ liệu nhỏ.
func TestSampleQueries(t *testing.T) {
	c := startClickHouse(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ts := func(sec int) string { return CHTimeString(now.Add(time.Duration(sec) * time.Second)) }
	const (
		u1, u2 = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333"
		v1     = "22222222-2222-2222-2222-222222222222"
	)
	var rows []string
	for _, e := range []struct {
		user, typ    string
		bitrate, buf int
	}{
		{u1, "play", 2800, 0}, {u2, "play", 1400, 0}, {u1, "buffering", 2800, 1500}, {u2, "buffering", 1400, 500},
		{u2, "buffering", 1400, 700}, {u1, "ended", 2800, 0},
	} {
		b, _ := json.Marshal(map[string]any{"ts": ts(len(rows)), "user_id": e.user, "video_id": v1, "type": e.typ,
			"position_s": 0, "bitrate_kbps": e.bitrate, "buffering_ms": e.buf})
		rows = append(rows, string(b))
	}
	if err := c.Insert(ctx, TablePlaybackEvents, []byte(strings.Join(rows, "\n")), ""); err != nil {
		t.Fatal(err)
	}
	logs := `{"ts":"` + ts(0) + `","service":"api","level":"INFO","msg":"http request","request_id":"r1","video_id":"","attrs":"{}"}
{"ts":"` + ts(1) + `","service":"api","level":"ERROR","msg":"get video","request_id":"r2","video_id":"","attrs":"{}"}`
	if err := c.Insert(ctx, TableAppLogs, []byte(logs), ""); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"top_videos":           `{"video_id":"` + v1 + `","plays":"2","viewers":"2"}`,
		"completion_rate":      `{"video_id":"` + v1 + `","started":"2","completed":"1","completion_rate":0.5}`,
		"buffering_by_bitrate": `{"bitrate_kbps":1400,"buffering_events":"2","total_buffering_ms":"1200","avg_buffering_ms":600}` + "\n" + `{"bitrate_kbps":2800,"buffering_events":"1","total_buffering_ms":"1500","avg_buffering_ms":1500}`,
		"logs_by_service":      `{"service":"api","level":"ERROR","lines":"1","last_seen":"` + ts(1) + `"}` + "\n" + `{"service":"api","level":"INFO","lines":"1","last_seen":"` + ts(0) + `"}`,
	}
	files, err := filepath.Glob("../../scripts/analytics/*.sql")
	if err != nil || len(files) != len(want) {
		t.Fatalf("sql files = %v, %v", files, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".sql")
		q, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got := queryString(t, c, string(q)+"\nFORMAT JSONEachRow")
		if got != want[name] {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want[name])
		}
	}
}
