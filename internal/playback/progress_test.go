package playback

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"streamfilm/internal/catalog"
	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

const (
	userA = "aaaaaaaa-0000-0000-0000-000000000001"
	userB = "aaaaaaaa-0000-0000-0000-000000000002"
)

func videoID(i int) string { return fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", i) }

// setup: Postgres thật, 2 user, n video ready (duration 100 giây).
func setup(t *testing.T, n int) (*Repo, *pgxpool.Pool) {
	t.Helper()
	dsn := testutil.StartPostgres(t)
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for i, u := range []string{userA, userB} {
		if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, $2, 'x', 'viewer')`,
			u, fmt.Sprintf("u%d@example.com", i)); err != nil {
			t.Fatal(err)
		}
	}
	cat := catalog.NewRepo(pool)
	for i := range n {
		id := videoID(i)
		if err := cat.Create(ctx, catalog.Video{ID: id, Title: "V", RawKey: id + "/source.mp4", SizeBytes: 1}); err != nil {
			t.Fatal(err)
		}
		for _, to := range []catalog.Status{catalog.StatusQueued, catalog.StatusProcessing} {
			if _, err := cat.Transition(ctx, id, to); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := cat.MarkReady(ctx, id, catalog.ReadyInfo{DurationS: 100, HLSPrefix: id + "/"}); err != nil {
			t.Fatal(err)
		}
	}
	return NewRepo(pool), pool
}

func TestRepoProgress(t *testing.T) {
	r, _ := setup(t, 25)
	ctx := context.Background()

	if at, err := r.ResumeAt(ctx, userA, videoID(0)); err != nil || at != 0 {
		t.Errorf("chưa xem: resume = %d, err = %v", at, err)
	}
	if err := r.SaveProgress(ctx, userA, videoID(0), 30, 100); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveProgress(ctx, userA, videoID(0), 42, 100); err != nil { // upsert
		t.Fatal(err)
	}
	if at, _ := r.ResumeAt(ctx, userA, videoID(0)); at != 42 {
		t.Errorf("resume = %d, want 42", at)
	}
	if at, _ := r.ResumeAt(ctx, userB, videoID(0)); at != 0 {
		t.Errorf("user khác: resume = %d, want 0", at)
	}
	if err := r.SaveProgress(ctx, userA, videoID(1), 97, 100); err != nil {
		t.Fatal(err)
	}
	if at, _ := r.ResumeAt(ctx, userA, videoID(1)); at != 0 {
		t.Errorf("xem ≥ 95%%: resume = %d, want 0", at)
	}
	if err := r.SaveProgress(ctx, userA, "cccccccc-0000-0000-0000-000000000000", 1, 100); !errors.Is(err, ErrNotFound) {
		t.Errorf("video không tồn tại: err = %v, want ErrNotFound", err)
	}
}

func TestRepoContinueWatching(t *testing.T) {
	r, _ := setup(t, 25)
	ctx := context.Background()
	// Video 0..3: biên 5%/95% (5 và 95 bị loại, 6 và 94 được giữ). Video 4..24: 21 video ở 50%.
	positions := map[int]int{0: 5, 1: 6, 2: 94, 3: 95}
	for i := 4; i < 25; i++ {
		positions[i] = 50
	}
	for i := range 25 { // ghi theo thứ tự 0..24: video 24 mới xem nhất
		if err := r.SaveProgress(ctx, userA, videoID(i), positions[i], 100); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SaveProgress(ctx, userB, videoID(0), 50, 100); err != nil {
		t.Fatal(err)
	}

	got, err := r.ContinueWatching(ctx, userA, catalog.ContinueWatchingLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != catalog.ContinueWatchingLimit {
		t.Fatalf("len = %d, want %d", len(got), catalog.ContinueWatchingLimit)
	}
	if got[0].VideoID != videoID(24) || got[0].PositionS != 50 || got[0].DurationS != 100 || got[0].UpdatedAt.IsZero() {
		t.Errorf("đầu hàng = %+v, want video 24", got[0])
	}
	for i := 1; i < len(got); i++ {
		if got[i].UpdatedAt.After(got[i-1].UpdatedAt) {
			t.Errorf("chưa sắp updated_at giảm dần ở %d", i)
		}
	}
	all, _ := r.ContinueWatching(ctx, userA, 100)
	seen := map[string]bool{}
	for _, e := range all {
		seen[e.VideoID] = true
		if !InContinueRow(e.PositionS, e.DurationS) {
			t.Errorf("%+v không thoả InContinueRow", e)
		}
	}
	if len(all) != 23 || seen[videoID(0)] || !seen[videoID(1)] || !seen[videoID(2)] || seen[videoID(3)] {
		t.Errorf("biên 5%%/95%%: len=%d seen0=%v seen1=%v seen2=%v seen3=%v",
			len(all), seen[videoID(0)], seen[videoID(1)], seen[videoID(2)], seen[videoID(3)])
	}
	// Xem tiếp video 1 → lên đầu hàng; xem hết video 2 → rời hàng.
	r.SaveProgress(ctx, userA, videoID(1), 60, 100)
	r.SaveProgress(ctx, userA, videoID(2), 100, 100)
	got, _ = r.ContinueWatching(ctx, userA, catalog.ContinueWatchingLimit)
	if got[0].VideoID != videoID(1) || got[0].PositionS != 60 {
		t.Errorf("đầu hàng = %+v, want video 1 ở 60s", got[0])
	}
	for _, e := range got {
		if e.VideoID == videoID(2) {
			t.Error("video đã xem hết vẫn trong hàng")
		}
	}
	if b, _ := r.ContinueWatching(ctx, userB, catalog.ContinueWatchingLimit); len(b) != 1 || b[0].VideoID != videoID(0) {
		t.Errorf("user B = %+v", b)
	}
}
