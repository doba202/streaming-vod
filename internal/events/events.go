// Package events định nghĩa topic, schema sự kiện (JSON) và helper Kafka dùng chung.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	TopicVideoUploaded    = "video.uploaded"
	TopicVideoUploadedDLQ = "video.uploaded.dlq"
	TopicVideoProcessing  = "video.processing"
	TopicVideoTranscoded  = "video.transcoded"
	TopicVideoFailed      = "video.failed"
)

var VideoTopics = []string{TopicVideoUploaded, TopicVideoUploadedDLQ, TopicVideoProcessing,
	TopicVideoTranscoded, TopicVideoFailed}

// Event là mọi payload publish lên Kafka. Key() là key message (video_id), giữ thứ tự theo video.
type Event interface {
	Key() string
}

type VideoUploaded struct {
	VideoID string `json:"video_id"`
	RawKey  string `json:"raw_key"`
}

type VideoProcessing struct {
	VideoID string `json:"video_id"`
}

type Rendition struct {
	Height      int    `json:"height"`
	BitrateKbps int    `json:"bitrate_kbps"`
	PlaylistKey string `json:"playlist_key"`
}

type VideoTranscoded struct {
	VideoID    string      `json:"video_id"`
	DurationS  int         `json:"duration_s"`
	HLSPrefix  string      `json:"hls_prefix"`
	Renditions []Rendition `json:"renditions"`
	PosterKey  string      `json:"poster_key,omitempty"` // M5
	SpriteKey  string      `json:"sprite_key,omitempty"` // M5
}

type VideoFailed struct {
	VideoID string `json:"video_id"`
	Reason  string `json:"reason"`
}

// DeadLetter ghi vào video.uploaded.dlq: payload gốc + lỗi cuối + số lần đã thử.
type DeadLetter struct {
	VideoID  string          `json:"video_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	Error    string          `json:"error"`
	Attempts int             `json:"attempts"`
	FailedAt time.Time       `json:"failed_at"`
}

func (e VideoUploaded) Key() string   { return e.VideoID }
func (e VideoProcessing) Key() string { return e.VideoID }
func (e VideoTranscoded) Key() string { return e.VideoID }
func (e VideoFailed) Key() string     { return e.VideoID }
func (e DeadLetter) Key() string      { return e.VideoID }

// NewRecord mã hoá ev thành record Kafka với key = ev.Key().
func NewRecord(topic string, ev Event) (*kgo.Record, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", topic, err)
	}
	return &kgo.Record{Topic: topic, Key: []byte(ev.Key()), Value: b}, nil
}

// Decode parse JSON và bắt buộc có video_id.
func Decode[T Event](data []byte) (T, error) {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("decode event: %w", err)
	}
	if v.Key() == "" {
		return v, errors.New("decode event: missing video_id")
	}
	return v, nil
}
