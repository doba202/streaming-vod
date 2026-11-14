package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

const (
	idA = "11111111-1111-1111-1111-111111111111"
	idB = "22222222-2222-2222-2222-222222222222"
	idC = "33333333-3333-3333-3333-333333333333"
	idX = "99999999-9999-9999-9999-999999999999"
)

func newTestRepo(t *testing.T) *Repo {
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
	return NewRepo(pool)
}

func newVideo(id string) Video {
	return Video{ID: id, Title: "Phim " + id[:4], Genres: []string{"drama"}, Year: 2024,
		RawKey: id + "/source.mp4", UploadID: "up-" + id[:4], SizeBytes: 1234}
}

func TestRepo(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("get", func(t *testing.T) {
		v, err := r.Get(ctx, idA)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status != StatusUploading || v.Title != "Phim 1111" || v.Year != 2024 ||
			len(v.Genres) != 1 || v.Genres[0] != "drama" || v.UploadID != "up-1111" ||
			v.RawKey != idA+"/source.mp4" || v.SizeBytes != 1234 || v.CreatedAt.IsZero() {
			t.Errorf("unexpected video: %+v", v)
		}
		if _, err := r.Get(ctx, idX); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get unknown: err = %v, want ErrNotFound", err)
		}
	})

	t.Run("happy path to ready", func(t *testing.T) {
		from, err := r.Transition(ctx, idA, StatusQueued)
		if err != nil || from != StatusUploading {
			t.Fatalf("to queued: from=%s err=%v", from, err)
		}
		if _, err := r.Transition(ctx, idA, StatusReady); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("queued -> ready: err = %v, want ErrInvalidTransition", err)
		}
		if _, err := r.Transition(ctx, idA, StatusProcessing); err != nil {
			t.Fatal(err)
		}
		info := ReadyInfo{DurationS: 30, HLSPrefix: idA + "/", Renditions: []Rendition{
			{Height: 360, BitrateKbps: 800, PlaylistKey: idA + "/360p/index.m3u8"},
			{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"},
		}}
		if _, err := r.MarkReady(ctx, idA, info); err != nil {
			t.Fatal(err)
		}
		v, _ := r.Get(ctx, idA)
		if v.Status != StatusReady || v.DurationS != 30 || v.HLSPrefix != idA+"/" {
			t.Errorf("after ready: %+v", v)
		}
		rs, err := r.Renditions(ctx, idA)
		if err != nil || len(rs) != 2 || rs[0].Height != 720 || rs[1].PlaylistKey != idA+"/360p/index.m3u8" {
			t.Errorf("renditions = %+v, err = %v", rs, err)
		}
		if _, err := r.MarkReady(ctx, idA, info); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("duplicate transcoded: err = %v, want ErrInvalidTransition", err)
		}
		if _, err := r.Transition(ctx, idX, StatusQueued); !errors.Is(err, ErrNotFound) {
			t.Errorf("transition unknown: err = %v", err)
		}
	})

	t.Run("fail, retry and revert", func(t *testing.T) {
		if _, err := r.Transition(ctx, idB, StatusQueued); err != nil {
			t.Fatal(err)
		}
		if _, err := r.MarkFailed(ctx, idB, "ffmpeg: boom"); err != nil {
			t.Fatal(err)
		}
		v, _ := r.Get(ctx, idB)
		if v.Status != StatusFailed || v.FailReason != "ffmpeg: boom" {
			t.Errorf("after failed: %+v", v)
		}
		if _, err := r.Transition(ctx, idB, StatusQueued); err != nil {
			t.Fatal(err)
		}
		if err := r.RevertQueued(ctx, idB, StatusFailed); err != nil {
			t.Fatal(err)
		}
		v, _ = r.Get(ctx, idB)
		if v.Status != StatusFailed || v.FailReason != "ffmpeg: boom" {
			t.Errorf("after revert: %+v", v)
		}
	})

	t.Run("list", func(t *testing.T) {
		all, err := r.List(ctx, ListFilter{})
		if err != nil || len(all) != 2 {
			t.Fatalf("list all = %d, err = %v", len(all), err)
		}
		failed, err := r.List(ctx, ListFilter{Status: StatusFailed})
		if err != nil || len(failed) != 1 || failed[0].ID != idB || failed[0].FailReason != "ffmpeg: boom" {
			t.Errorf("list failed = %+v, err = %v", failed, err)
		}
	})

	t.Run("stale uploads", func(t *testing.T) {
		if err := r.Create(ctx, newVideo(idC)); err != nil {
			t.Fatal(err)
		}
		stale, err := r.ListStaleUploading(ctx, time.Now().Add(time.Hour))
		if err != nil || len(stale) != 1 || stale[0].ID != idC || stale[0].UploadID != "up-3333" {
			t.Fatalf("stale = %+v, err = %v", stale, err)
		}
		none, _ := r.ListStaleUploading(ctx, time.Now().Add(-time.Hour))
		if len(none) != 0 {
			t.Errorf("fresh upload reported stale: %+v", none)
		}
		if err := r.DeleteUploading(ctx, idC); err != nil {
			t.Fatal(err)
		}
		if err := r.DeleteUploading(ctx, idC); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete twice: err = %v", err)
		}
		if err := r.DeleteUploading(ctx, idA); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete ready video must be refused: err = %v", err)
		}
	})
}

func TestRepoListReady(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	for _, id := range []string{idA, idB, idC} {
		if err := r.Create(ctx, newVideo(id)); err != nil {
			t.Fatal(err)
		}
	}
	// idA, idC ready; idB còn uploading.
	for _, id := range []string{idA, idC} {
		for _, to := range []Status{StatusQueued, StatusProcessing} {
			if _, err := r.Transition(ctx, id, to); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.MarkReady(ctx, id, ReadyInfo{DurationS: 30, HLSPrefix: id + "/"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := r.ListReady(ctx, 10, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListReady = %+v, err = %v", all, err)
	}
	for _, v := range all {
		if v.Status != StatusReady || v.ID == idB {
			t.Errorf("non-ready video in catalog: %+v", v)
		}
	}
	page2, err := r.ListReady(ctx, 1, 1)
	if err != nil || len(page2) != 1 || page2[0].ID != all[1].ID {
		t.Errorf("page 2 = %+v, err = %v", page2, err)
	}
}
