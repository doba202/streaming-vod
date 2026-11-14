package analytics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"streamfilm/internal/events"
)

// ErrInvalid: record không ghi được (JSON hỏng, thiếu trường, sai kiểu). Pipeline bỏ qua và đếm,
// vì một row sai kiểu làm ClickHouse từ chối cả lô.
var ErrInvalid = errors.New("invalid record")

// CHTime mã hoá thời điểm theo dạng DateTime64(3) mà ClickHouse đọc được ở chế độ mặc định
// (date_time_input_format=basic): "YYYY-MM-DD hh:mm:ss.sss", luôn UTC.
type CHTime time.Time

const chTimeLayout = "2006-01-02 15:04:05.000"

func (t CHTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + CHTimeString(time.Time(t)) + `"`), nil
}

// CHTimeString: t theo giờ UTC, dạng "2006-01-02 15:04:05.000".
func CHTimeString(t time.Time) string { return t.UTC().Format(chTimeLayout) }

// PlaybackRow: một dòng của bảng playback_events.
type PlaybackRow struct {
	TS          CHTime `json:"ts"`
	UserID      string `json:"user_id"`
	VideoID     string `json:"video_id"`
	Type        string `json:"type"`
	PositionS   uint32 `json:"position_s"`
	BitrateKbps uint32 `json:"bitrate_kbps"`
	BufferingMs uint32 `json:"buffering_ms"`
}

// LogRow: một dòng của bảng app_logs. Attrs là JSON object của các trường còn lại.
type LogRow struct {
	TS        CHTime `json:"ts"`
	Service   string `json:"service"`
	Level     string `json:"level"`
	Msg       string `json:"msg"`
	RequestID string `json:"request_id"`
	VideoID   string `json:"video_id"`
	Attrs     string `json:"attrs"`
}

// Mapper biến một record Kafka thành một dòng JSONEachRow (không có "\n"); lỗi bọc ErrInvalid.
type Mapper func(rec *kgo.Record) ([]byte, error)

// MapPlayback: Mapper cho playback.events.
func MapPlayback(rec *kgo.Record) ([]byte, error) {
	row, err := ParsePlayback(rec.Value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(row)
}

// MapLog: Mapper cho app.logs; dòng log không có "time" hợp lệ thì lấy timestamp của record.
func MapLog(rec *kgo.Record) ([]byte, error) {
	row, err := ParseLog(rec.Value, rec.Timestamp)
	if err != nil {
		return nil, err
	}
	return json.Marshal(row)
}

// ParsePlayback kiểm tra một events.PlaybackEvent: user_id và video_id là UUID (chuẩn hoá chữ thường),
// có ts và type, các số nằm trong UInt32.
func ParsePlayback(data []byte) (PlaybackRow, error) {
	ev, err := events.Decode[events.PlaybackEvent](data)
	if err != nil {
		return PlaybackRow{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	user, err := uuid.Parse(ev.UserID)
	if err != nil {
		return PlaybackRow{}, fmt.Errorf("%w: user_id %q", ErrInvalid, ev.UserID)
	}
	video, err := uuid.Parse(ev.VideoID)
	if err != nil {
		return PlaybackRow{}, fmt.Errorf("%w: video_id %q", ErrInvalid, ev.VideoID)
	}
	if ev.TS.IsZero() || ev.Type == "" {
		return PlaybackRow{}, fmt.Errorf("%w: missing ts or type", ErrInvalid)
	}
	nums := [...]int{ev.PositionS, ev.BitrateKbps, ev.BufferingMs}
	for _, n := range nums {
		if n < 0 || n > math.MaxUint32 {
			return PlaybackRow{}, fmt.Errorf("%w: number out of UInt32 range: %d", ErrInvalid, n)
		}
	}
	return PlaybackRow{TS: CHTime(ev.TS), UserID: user.String(), VideoID: video.String(), Type: ev.Type,
		PositionS: uint32(nums[0]), BitrateKbps: uint32(nums[1]), BufferingMs: uint32(nums[2])}, nil
}

// ParseLog đọc một dòng log JSON của slog: "time", "level", "msg", "service", "request_id", "video_id"
// thành cột riêng, phần còn lại thành attrs (JSON, khoá sắp xếp). fallback dùng khi thiếu "time".
func ParseLog(data []byte, fallback time.Time) (LogRow, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // giữ nguyên số trong attrs (không đổi 123 thành 1.23e+02)
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return LogRow{}, fmt.Errorf("%w: log line is not a JSON object", ErrInvalid)
	}
	ts := fallback
	if s, ok := m["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			ts = t
		}
	}
	row := LogRow{TS: CHTime(ts), Level: take(m, "level"), Msg: take(m, "msg"), Service: take(m, "service"),
		RequestID: take(m, "request_id"), VideoID: take(m, "video_id")}
	delete(m, "time")
	attrs, err := json.Marshal(m)
	if err != nil {
		return LogRow{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	row.Attrs = string(attrs)
	return row, nil
}

// take lấy và xoá m[key] dưới dạng chuỗi: string giữ nguyên, thiếu/null là "", kiểu khác mã hoá JSON.
func take(m map[string]any, key string) string {
	v, ok := m[key]
	delete(m, key)
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		if !ok {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
}
