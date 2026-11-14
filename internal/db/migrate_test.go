package db

import (
	"context"
	"testing"
	"testing/fstest"

	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

func TestMigrate(t *testing.T) {
	dsn := testutil.StartPostgres(t)
	ctx := context.Background()
	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	t.Run("applies embedded migrations once", func(t *testing.T) {
		applied, err := Migrate(ctx, pool, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) == 0 || applied[0] != "0001_videos" {
			t.Fatalf("applied = %v", applied)
		}
		again, err := Migrate(ctx, pool, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 0 {
			t.Errorf("second run applied %v, want none", again)
		}
		var n int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name IN ('videos', 'renditions', 'users', 'subtitles', 'watch_progress')`).Scan(&n)
		if err != nil || n != 5 {
			t.Errorf("tables = %d, err = %v", n, err)
		}
	})

	t.Run("failed file is rolled back", func(t *testing.T) {
		fsys := fstest.MapFS{
			"0001_ok.sql":  {Data: []byte("CREATE TABLE mig_ok (id int);")},
			"0002_bad.sql": {Data: []byte("CREATE TABLE mig_bad (id int); SELECT no_such_column;")},
		}
		applied, err := Migrate(ctx, pool, fsys)
		if err == nil {
			t.Fatal("want error from 0002_bad.sql")
		}
		if len(applied) != 1 || applied[0] != "0001_ok" {
			t.Errorf("applied = %v", applied)
		}
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('mig_bad') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Error("mig_bad must not exist after rollback")
		}
	})
}
