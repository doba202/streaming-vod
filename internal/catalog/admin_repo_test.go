package catalog

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestRepoUpdateMetadata(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	if err := r.Create(ctx, newVideo(idA)); err != nil {
		t.Fatal(err)
	}
	makeReady(t, r, idA)
	before, err := r.Get(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}

	v, err := r.UpdateMetadata(ctx, idA, Metadata{Title: "Hạ cánh nơi anh", Description: "phim Hàn",
		Genres: []string{"romance", "drama"}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Title != "Hạ cánh nơi anh" || v.Description != "phim Hàn" || v.Year != 0 ||
		!slices.Equal(v.Genres, []string{"romance", "drama"}) || v.Status != StatusReady ||
		!v.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated = %+v", v)
	}
	// Trigger tính lại cột search: tìm không dấu theo tiêu đề mới, tiêu đề cũ không còn khớp.
	if got, err := r.Search(ctx, SearchParams{Query: "ha canh", Limit: 10}); err != nil || !slices.Equal(ids(got), []string{idA}) {
		t.Errorf("search new title = %v, err = %v", ids(got), err)
	}
	if got, err := r.Search(ctx, SearchParams{Query: "Phim 1111", Limit: 10}); err != nil || len(got) != 0 {
		t.Errorf("search old title = %v, err = %v", ids(got), err)
	}
	if _, err := r.UpdateMetadata(ctx, idX, Metadata{Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v", err)
	}
}

func TestRepoDelete(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB, idC} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}
	// idA ready, có rendition + phụ đề + tiến độ xem; idB queued; idC failed.
	makeReady(t, r, idA)
	if err := r.UpsertSubtitle(ctx, idA, Subtitle{Lang: "vi", Label: "Tiếng Việt", VTTKey: SubtitleKey(idA, "vi")}); err != nil {
		t.Fatal(err)
	}
	const userID = "77777777-7777-7777-7777-777777777777"
	if _, err := r.pool.Exec(ctx, `INSERT INTO users (id, email, password_hash, role) VALUES ($1, 'u@x.vn', 'h', 'viewer')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO watch_progress (user_id, video_id, position_s, duration_s) VALUES ($1, $2, 10, 30)`, userID, idA); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transition(ctx, idB, StatusQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Transition(ctx, idC, StatusQueued); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MarkFailed(ctx, idC, "boom"); err != nil {
		t.Fatal(err)
	}

	v, err := r.Delete(ctx, idA)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != idA || v.Status != StatusReady || v.RawKey != idA+"/source.mp4" {
		t.Errorf("deleted = %+v", v)
	}
	if _, err := r.Get(ctx, idA); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: err = %v", err)
	}
	for _, table := range []string{"renditions", "subtitles", "watch_progress"} {
		var n int
		if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE video_id = $1`, idA).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows after delete = %d, err = %v", table, n, err)
		}
	}
	if _, err := r.Delete(ctx, idA); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: err = %v", err)
	}
	if v, err := r.Delete(ctx, idB); !errors.Is(err, ErrVideoBusy) || v.Status != StatusQueued {
		t.Errorf("delete queued: v = %+v, err = %v", v, err)
	}
	if _, err := r.Get(ctx, idB); err != nil {
		t.Errorf("queued video must still exist: %v", err)
	}
	if v, err := r.Delete(ctx, idC); err != nil || v.Status != StatusFailed || v.FailReason != "boom" {
		t.Errorf("delete failed video: v = %+v, err = %v", v, err)
	}
}
