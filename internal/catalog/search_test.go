package catalog

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"streamfilm/internal/db"
	"streamfilm/internal/testutil"
	"streamfilm/migrations"
)

const (
	idD = "44444444-4444-4444-4444-444444444444"
	idE = "55555555-5555-5555-5555-555555555555"
)

// makeReady đưa video uploading → queued → processing → ready.
func makeReady(t *testing.T, r *Repo, id string) {
	t.Helper()
	ctx := context.Background()
	for _, to := range []Status{StatusQueued, StatusProcessing} {
		if _, err := r.Transition(ctx, id, to); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.MarkReady(ctx, id, ReadyInfo{DurationS: 30, HLSPrefix: id + "/"}); err != nil {
		t.Fatal(err)
	}
}

func ids(vs []Video) []string {
	out := []string{}
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func TestRepoSearch(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	seed := []struct {
		v     Video
		ready bool
	}{
		{Video{ID: idA, Title: "Trò chơi con mực", Description: "Phim Hàn Quốc về những trò chơi sinh tử",
			Genres: []string{"Hành động", "Hàn Quốc"}, Year: 2021}, true},
		{Video{ID: idB, Title: "Đường về nhà", Description: "Hành trình trở về quê ăn Tết",
			Genres: []string{"Gia đình"}, Year: 2020}, true},
		{Video{ID: idC, Title: "Trò chơi vương quyền", Description: "Chưa transcode xong",
			Genres: []string{"Giả tưởng"}, Year: 2011}, false},
		{Video{ID: idD, Title: "Người nhện", Description: "Siêu anh hùng đi học",
			Genres: []string{"Hành động"}, Year: 2019}, true},
		{Video{ID: idE, Title: "Bí mật", Description: "Một trò chơi nhỏ giữa hai đứa trẻ",
			Genres: []string{"Gia đình"}, Year: 2022}, true},
	}
	for _, s := range seed {
		s.v.RawKey, s.v.SizeBytes = s.v.ID+"/source.mp4", 1
		if err := r.Create(ctx, s.v); err != nil {
			t.Fatal(err)
		}
		if s.ready {
			makeReady(t, r, s.v.ID)
		}
	}

	cases := []struct {
		name string
		p    SearchParams
		want []string
	}{
		{"không dấu tìm ra có dấu; video chưa ready bị ẩn", SearchParams{Query: "tro choi"}, []string{idA, idE}},
		{"có dấu", SearchParams{Query: "Trò Chơi"}, []string{idA, idE}},
		{"đ → d", SearchParams{Query: "duong ve nha"}, []string{idB}},
		{"gõ có dấu chữ đ", SearchParams{Query: "đường"}, []string{idB}},
		{"khớp mô tả", SearchParams{Query: "sieu anh hung"}, []string{idD}},
		{"cú pháp websearch: loại trừ từ", SearchParams{Query: "tro choi -con"}, []string{idE}},
		{"gõ sai → trigram", SearchParams{Query: "tro chpi"}, []string{idA}},
		{"gõ sai không dấu", SearchParams{Query: "nguoi nhwn"}, []string{idD}},
		{"không khớp gì", SearchParams{Query: "zzzz qqqq"}, []string{}},
		{"lọc thể loại kèm từ khoá", SearchParams{Query: "tro choi", Genre: "Gia đình"}, []string{idE}},
		{"lọc thể loại không có từ khoá, mới nhất trước", SearchParams{Genre: "Hành động"}, []string{idD, idA}},
		{"lọc năm", SearchParams{Year: 2020}, []string{idB}},
		{"từ khoá + năm không khớp", SearchParams{Query: "tro choi", Year: 2020}, []string{}},
		{"phân trang kết quả tìm kiếm", SearchParams{Query: "tro choi", Offset: 1}, []string{idE}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.p.Limit == 0 {
				c.p.Limit = 10
			}
			got, err := r.Search(ctx, c.p)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ids(got), c.want) {
				t.Errorf("Search(%+v) = %v, want %v", c.p, ids(got), c.want)
			}
		})
	}

	t.Run("sửa tiêu đề cập nhật cột search (trigger)", func(t *testing.T) {
		if _, err := r.pool.Exec(ctx, `UPDATE videos SET title = 'Cô gái đến từ hôm qua' WHERE id = $1`, idB); err != nil {
			t.Fatal(err)
		}
		got, err := r.Search(ctx, SearchParams{Query: "co gai den tu hom qua", Limit: 10})
		if err != nil || !slices.Equal(ids(got), []string{idB}) {
			t.Errorf("after update: %v, err = %v", ids(got), err)
		}
	})

	t.Run("genres chỉ của video ready", func(t *testing.T) {
		got, err := r.Genres(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		want := []string{"Gia đình", "Hàn Quốc", "Hành động"}
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("Genres = %v, want %v", got, want)
		}
	})
}

// TestSearchMigrationBackfill: video tạo trước migration 0003 vẫn tìm được sau khi migrate.
func TestSearchMigrationBackfill(t *testing.T) {
	dsn := testutil.StartPostgres(t)
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") && e.Name() < "0003" {
			body, err := fs.ReadFile(migrations.FS, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			before[e.Name()] = &fstest.MapFile{Data: body}
		}
	}
	if _, err := db.Migrate(ctx, pool, before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO videos (id, title, status, raw_key, size_bytes)
		VALUES ($1, 'Trò chơi cũ', 'ready', 'k', 1)`, idA); err != nil {
		t.Fatal(err)
	}
	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(applied, "0003_search") {
		t.Fatalf("applied = %v", applied)
	}
	got, err := NewRepo(pool).Search(ctx, SearchParams{Query: "tro choi cu", Limit: 10})
	if err != nil || !slices.Equal(ids(got), []string{idA}) {
		t.Errorf("Search after backfill = %v, err = %v", ids(got), err)
	}
}
