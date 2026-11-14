// Package playback nhận sự kiện xem phim từ player: ghi tiến độ (watch_progress) cho "Xem tiếp" /
// resume_at và publish playback.events cho analytics (spec §4.4).
package playback

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"streamfilm/internal/catalog"
)

// Ngưỡng hàng "Xem tiếp" (spec §4.4): 5% < position/duration < 95%. Số video tối đa (20) là
// catalog.ContinueWatchingLimit.
const (
	ContinueMinRatio = 0.05
	ContinueMaxRatio = 0.95
)

// ErrNotFound: user hoặc video không còn tồn tại khi ghi tiến độ.
var ErrNotFound = errors.New("user or video not found")

// InContinueRow: video có thuộc hàng "Xem tiếp" không. Khớp điều kiện WHERE của ContinueWatching.
func InContinueRow(positionS, durationS int) bool {
	if durationS <= 0 {
		return false
	}
	r := float64(positionS) / float64(durationS)
	return r > ContinueMinRatio && r < ContinueMaxRatio
}

// ResumePosition: vị trí phát tiếp. Đã xem tới ≥ 95% (coi như xem xong) thì phát lại từ đầu.
func ResumePosition(positionS, durationS int) int {
	if durationS > 0 && float64(positionS) >= ContinueMaxRatio*float64(durationS) {
		return 0
	}
	return max(positionS, 0)
}

// Repo đọc/ghi bảng watch_progress. Thoả catalog.ProgressSource.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// SaveProgress ghi vị trí mới nhất (upsert theo (user_id, video_id)) và chép duration_s của video.
func (r *Repo) SaveProgress(ctx context.Context, userID, videoID string, positionS, durationS int) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO watch_progress (user_id, video_id, position_s, duration_s, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, video_id) DO UPDATE
		SET position_s = EXCLUDED.position_s, duration_s = EXCLUDED.duration_s, updated_at = now()`,
		userID, videoID, positionS, durationS)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("save progress: %w", err)
	}
	return nil
}

// ResumeAt trả vị trí phát tiếp (ResumePosition); chưa xem bao giờ → 0.
func (r *Repo) ResumeAt(ctx context.Context, userID, videoID string) (int, error) {
	var pos, dur int
	err := r.pool.QueryRow(ctx, `SELECT position_s, duration_s FROM watch_progress
		WHERE user_id = $1 AND video_id = $2`, userID, videoID).Scan(&pos, &dur)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resume at: %w", err)
	}
	return ResumePosition(pos, dur), nil
}

// ContinueWatching: hàng "Xem tiếp" của user, dùng index (user_id, updated_at DESC). Chỉ video ready mới
// có tiến độ (event handler từ chối video chưa ready) và ready là trạng thái cuối nên không cần join videos.
func (r *Repo) ContinueWatching(ctx context.Context, userID string, limit int) ([]catalog.WatchEntry, error) {
	rows, err := r.pool.Query(ctx, `SELECT video_id::text, position_s, duration_s, updated_at FROM watch_progress
		WHERE user_id = $1 AND duration_s > 0
		  AND position_s > duration_s * $2::float8 AND position_s < duration_s * $3::float8
		ORDER BY updated_at DESC LIMIT $4`, userID, ContinueMinRatio, ContinueMaxRatio, limit)
	if err != nil {
		return nil, fmt.Errorf("continue watching: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (catalog.WatchEntry, error) {
		var e catalog.WatchEntry
		err := row.Scan(&e.VideoID, &e.PositionS, &e.DurationS, &e.UpdatedAt)
		return e, err
	})
}
