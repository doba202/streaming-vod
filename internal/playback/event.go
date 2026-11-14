package playback

import (
	"errors"
	"fmt"
	"math"

	"github.com/google/uuid"
)

// EventType: loại sự kiện player gửi (spec §4.4).
type EventType string

const (
	TypePlay      EventType = "play"
	TypePause     EventType = "pause"
	TypeSeek      EventType = "seek"
	TypeEnded     EventType = "ended"
	TypeBuffering EventType = "buffering"
	TypeHeartbeat EventType = "heartbeat" // mỗi 10 giây khi đang phát
)

func (t EventType) Valid() bool {
	switch t {
	case TypePlay, TypePause, TypeSeek, TypeEnded, TypeBuffering, TypeHeartbeat:
		return true
	}
	return false
}

// SavesProgress: chỉ heartbeat, pause, ended cập nhật watch_progress (spec §4.4). play/seek/buffering
// chỉ đi vào analytics: seek liên tục khi kéo thanh tua không làm nhiễu "Xem tiếp".
func (t EventType) SavesProgress() bool {
	return t == TypeHeartbeat || t == TypePause || t == TypeEnded
}

// Giới hạn giá trị của sự kiện.
const (
	MaxPositionS   = 48 * 3600 // giây
	MaxBitrateKbps = 100_000
	MaxBufferingMs = 10 * 60 * 1000
)

// EventInput là body của POST /api/playback/events. position_s là currentTime của <video> (số thực).
type EventInput struct {
	VideoID     string    `json:"video_id"`
	Type        EventType `json:"type"`
	PositionS   float64   `json:"position_s"`
	BitrateKbps int       `json:"bitrate_kbps"`
	BufferingMs int       `json:"buffering_ms"`
}

var ErrInvalidEvent = errors.New("invalid playback event")

// Normalize kiểm tra input, chuẩn hoá video_id (uuid dạng chữ thường) và trả vị trí theo giây nguyên.
func (in *EventInput) Normalize() (int, error) {
	id, err := uuid.Parse(in.VideoID)
	if err != nil {
		return 0, fmt.Errorf("%w: video_id must be a UUID", ErrInvalidEvent)
	}
	in.VideoID = id.String()
	if !in.Type.Valid() {
		return 0, fmt.Errorf("%w: type must be one of play, pause, seek, ended, buffering, heartbeat", ErrInvalidEvent)
	}
	if math.IsNaN(in.PositionS) || in.PositionS < 0 || in.PositionS > MaxPositionS {
		return 0, fmt.Errorf("%w: position_s must be between 0 and %d", ErrInvalidEvent, MaxPositionS)
	}
	if in.BitrateKbps < 0 || in.BitrateKbps > MaxBitrateKbps {
		return 0, fmt.Errorf("%w: bitrate_kbps must be between 0 and %d", ErrInvalidEvent, MaxBitrateKbps)
	}
	if in.BufferingMs < 0 || in.BufferingMs > MaxBufferingMs {
		return 0, fmt.Errorf("%w: buffering_ms must be between 0 and %d", ErrInvalidEvent, MaxBufferingMs)
	}
	return int(in.PositionS), nil
}
