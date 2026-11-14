package analytics

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	userID  = "11111111-1111-1111-1111-111111111111"
	videoID = "22222222-2222-2222-2222-222222222222"
)

func TestCHTimeIsUTCMillis(t *testing.T) {
	loc := time.FixedZone("ICT", 7*3600)
	b, err := json.Marshal(CHTime(time.Date(2026, 11, 15, 17, 4, 5, 123456789, loc)))
	if err != nil || string(b) != `"2026-11-15 10:04:05.123"` {
		t.Errorf("CHTime = %s, %v", b, err)
	}
}

func TestMapPlayback(t *testing.T) {
	rec := &kgo.Record{Value: []byte(`{"ts":"2026-11-15T17:00:00.5+07:00","user_id":"` + userID +
		`","video_id":"22222222-2222-2222-2222-22222222222A","type":"buffering","position_s":12,"bitrate_kbps":2800,"buffering_ms":1500}`)}
	line, err := MapPlayback(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ts":"2026-11-15 10:00:00.500","user_id":"` + userID + `","video_id":"22222222-2222-2222-2222-22222222222a",` +
		`"type":"buffering","position_s":12,"bitrate_kbps":2800,"buffering_ms":1500}`
	if string(line) != want {
		t.Errorf("line =\n%s\nwant\n%s", line, want)
	}
}

func TestParsePlaybackRejectsInvalid(t *testing.T) {
	ok := func(field string) string {
		m := map[string]any{"ts": "2026-11-15T10:00:00Z", "user_id": userID, "video_id": videoID, "type": "play",
			"position_s": 1, "bitrate_kbps": 1, "buffering_ms": 1}
		switch field {
		case "user_id", "video_id":
			m[field] = "abc"
		case "ts", "type":
			delete(m, field)
		case "position_s", "bitrate_kbps", "buffering_ms":
			m[field] = -1
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	cases := map[string]string{
		"not json":     "not-json",
		"array":        "[]",
		"empty object": "{}",
		"bad user_id":  ok("user_id"),
		"bad video_id": ok("video_id"),
		"missing ts":   ok("ts"),
		"missing type": ok("type"),
		"neg position": ok("position_s"),
		"neg bitrate":  ok("bitrate_kbps"),
		"neg buffer":   ok("buffering_ms"),
		"float pos":    `{"ts":"2026-11-15T10:00:00Z","user_id":"` + userID + `","video_id":"` + videoID + `","type":"play","position_s":1.5}`,
	}
	for name, in := range cases {
		if _, err := ParsePlayback([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ParsePlayback([]byte(ok(""))); err != nil {
		t.Errorf("valid: %v", err)
	}
}

func TestMapLog(t *testing.T) {
	fallback := time.Date(2026, 11, 15, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		name, in string
		want     LogRow
	}{
		{"http request",
			`{"time":"2026-11-15T17:00:00.123456+07:00","level":"INFO","msg":"http request","service":"api",` +
				`"request_id":"host/abc-000001","method":"GET","path":"/api/videos","status":200,"duration_ms":3,"video_id":"` + videoID + `"}`,
			LogRow{TS: CHTime(time.Date(2026, 11, 15, 10, 0, 0, 123456000, time.UTC)), Service: "api", Level: "INFO",
				Msg: "http request", RequestID: "host/abc-000001", VideoID: videoID,
				Attrs: `{"duration_ms":3,"method":"GET","path":"/api/videos","status":200}`}},
		{"no time, numeric video_id, nested group",
			`{"level":"ERROR","msg":"x","service":"transcoder","video_id":42,"err":{"code":1.50}}`,
			LogRow{TS: CHTime(fallback), Service: "transcoder", Level: "ERROR", Msg: "x", VideoID: "42",
				Attrs: `{"err":{"code":1.50}}`}},
		{"bad time, null request_id",
			`{"time":"yesterday","msg":"y","request_id":null}`,
			LogRow{TS: CHTime(fallback), Msg: "y", Attrs: `{}`}},
	}
	for _, c := range cases {
		got, err := ParseLog([]byte(c.in), fallback)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !time.Time(got.TS).Equal(time.Time(c.want.TS)) {
			t.Errorf("%s: ts = %v, want %v", c.name, time.Time(got.TS), time.Time(c.want.TS))
		}
		got.TS, c.want.TS = CHTime{}, CHTime{}
		if got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
	for _, in := range []string{"not-json", "null", `"str"`, `[1]`} {
		if _, err := ParseLog([]byte(in), fallback); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", in, err)
		}
	}
	line, err := MapLog(&kgo.Record{Value: []byte(`{"msg":"z","service":"api"}`), Timestamp: fallback})
	if err != nil || string(line) != `{"ts":"2026-11-15 01:02:03.000","service":"api","level":"","msg":"z","request_id":"","video_id":"","attrs":"{}"}` {
		t.Errorf("MapLog = %s, %v", line, err)
	}
}
