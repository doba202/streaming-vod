package catalog

import (
	"context"
	"time"
)

// ContinueWatchingLimit: số video tối đa của hàng "Xem tiếp" (spec §4.4).
const ContinueWatchingLimit = 20

// WatchEntry là tiến độ xem một video của một user (bảng watch_progress, module playback).
type WatchEntry struct {
	VideoID              string
	PositionS, DurationS int
	UpdatedAt            time.Time
}

// ProgressSource: phần của playback.Repo mà catalog cần cho /play (resume_at) và hàng "Xem tiếp".
// Khai báo ở catalog (bên dùng) để catalog không import playback.
type ProgressSource interface {
	ResumeAt(ctx context.Context, userID, videoID string) (int, error)
	ContinueWatching(ctx context.Context, userID string, limit int) ([]WatchEntry, error)
}
