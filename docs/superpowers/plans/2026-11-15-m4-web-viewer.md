# Streamfilm M4: Web người xem + tìm kiếm không dấu — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Người xem mở http://localhost:8088, đăng nhập / đăng ký, thấy trang chủ (lưới phim `ready`, poster tạm), trang chi tiết, trang xem (hls.js phát URL ký từ `/play`; gặp `403` hoặc lỗi mạng thì gọi lại `/play` và phát tiếp từ vị trí hiện tại; chọn chất lượng), trang tìm kiếm (debounce, lọc thể loại) — gõ "tro choi" ra "Trò chơi …", gõ sai "tro chpi" vẫn ra nhờ trigram. Kiểm tra bằng Vitest, test Go với Postgres thật, test nginx thật và `make e2e-m4`.

**Architecture:** Backend: migration `0003_search.sql` (extension `unaccent` + `pg_trgm`, hàm `immutable_unaccent`, cột `videos.search` cập nhật bằng trigger, GIN trên `search` và GIN trigram trên `immutable_unaccent(title)`, backfill); `catalog.Repo.Search` (một câu SQL: full-text `websearch_to_tsquery` + `ts_rank`, chỉ khi không có kết quả full-text mới dùng trigram `<%`/`word_similarity`) và `Repo.Genres`; `ViewerHandler` nhận `?q=&genre=&year=&page=` và thêm `GET /api/genres`. Frontend: `web/app` (React 18 + Vite 6 + TypeScript + react-router-dom 7 + hls.js), logic phục hồi player tách thành `PlaybackSession` thuần để test. nginx thành cổng vào duy nhất cùng origin: SPA ở `/` (fallback `index.html`), `/api/` proxy tới `api:8000`, `/cdn/` giữ nguyên CDN ký URL; image nginx build SPA bằng Docker multi-stage nên `make up` không cần Node trên máy.

**Tech Stack:** Go 1.27.4, chi v5.5.0, pgx v5.13.0, testcontainers-go v0.45.0, Postgres 16 (`unaccent`, `pg_trgm` có sẵn trong `postgres:16-alpine`), nginx `1.27-alpine`, Node `20.19.6` (image `node:20.19.6-alpine3.22`), npm 11. npm (ghim chính xác, cài với `--before=2026-11-01` để mọi dependency bắc cầu đã phát hành ≥ 2 tuần, `npm audit` = 0 lỗ hổng): `react` 18.3.1, `react-dom` 18.3.1, `react-router-dom` 7.18.5, `hls.js` 1.6.5; dev: `vite` 6.4.5, `@vitejs/plugin-react` 4.5.2, `typescript` 5.8.3, `vitest` 4.1.12, `jsdom` 26.1.0, `@testing-library/react` 16.3.0, `@testing-library/dom` 10.4.0, `@testing-library/jest-dom` 6.6.3, `@types/react` 18.3.23, `@types/react-dom` 18.3.7.

**Spec:** `docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md` (mục 2, 4.3, 5, 6, 9, 10, 11)

## Global Constraints

- Go module path: `streamfilm`. Mọi lệnh `npm` chạy trong `web/app` với Node ≥ 20.19 (`node -v` → `v20.19.6`). Lockfile `web/app/package-lock.json` được commit; Docker build dùng `npm ci`.
- Tìm kiếm (spec §5): cột `videos.search tsvector` = `setweight(to_tsvector('simple', immutable_unaccent(title)), 'A') || setweight(to_tsvector('simple', immutable_unaccent(description)), 'B')`, cập nhật bằng trigger `BEFORE INSERT OR UPDATE OF title, description`. Index GIN trên `search`; GIN `gin_trgm_ops` trên `immutable_unaccent(title)`. Truy vấn `websearch_to_tsquery('simple', immutable_unaccent($q))` xếp `ts_rank`; **không có kết quả full-text nào** (sau lọc) thì dùng trigram `immutable_unaccent($q) <% immutable_unaccent(title)` xếp `word_similarity` (ngưỡng mặc định 0.6). Chỉ video `ready`.
- `GET /api/videos`: `q` trim, ≤ 100 rune; `genre` trim, ≤ 50 rune, khớp đúng một phần tử `genres`; `year` 1888–2100; `page` 1–1000; 20 video/trang (`PageSize`), `has_more`. Sai → `400`. Không có `q`: mới nhất trước (`created_at DESC, id`).
- `GET /api/genres` (user): `{"genres": [...]}` các thể loại không trùng của video `ready`, sắp xếp, `[]` khi rỗng.
- Cùng origin (ADR 0011): nginx `:8088` phục vụ SPA ở `/` (`try_files $uri /index.html`, `Cache-Control: no-cache`), `/assets/` (`Cache-Control: public, max-age=31536000, immutable`, thiếu file → 404), `/api/` → `proxy_pass $api_upstream` (`http://api:8000`, `resolver 127.0.0.11`), `/cdn/` không đổi, `/dev/` không đổi, `/hls/` → 404. Image `streamfilm-nginx:dev` từ `deploy/docker/nginx.Dockerfile`.
- `CDN_BASE_URL` trong compose: `${CDN_BASE_URL:-http://localhost:8088}` (trùng origin của web).
- Player (spec §4.3): `403` (fatal hay không) hoặc lỗi mạng fatal → gọi lại `/play`, nạp URL mới với `startPosition = currentTime`; nhiều lỗi đồng thời → một lần gọi `/play`; tối đa 3 lần trong 60 giây rồi báo lỗi. Lỗi media fatal → `hls.recoverMediaError()`. Lần đầu phát từ `resume_at`.
- Web: giao diện tiếng Việt, CSS thuần (`src/styles.css`), không UI framework. Cookie `sf_session` HttpOnly: JS không đọc; `fetch` dùng `credentials: 'same-origin'`. Mật khẩu 8–72 **byte** (khớp server).
- Vite dev server `:5173` proxy `/api` và `/cdn` tới `http://localhost:8088`.
- Integration test tự `t.Skip` khi `-short` hoặc thiếu Docker (như M2/M3). Tài liệu tiếng Việt, chỉ mô tả những gì đã chạy (spec §11).

## Quyết định thiết kế chính (ghi ADR ở Task 11)

| Quyết định | Lý do |
|---|---|
| `immutable_unaccent(text)` = `RETURN public.unaccent('public.unaccent'::regdictionary, $1)`, khai báo `IMMUTABLE` (SQL-standard body của PG14+) | `unaccent()` là `STABLE` nên không dùng được trong index biểu thức; ghim từ điển + schema để kết quả không phụ thuộc `search_path` |
| Cột `search` + trigger (đúng spec) thay vì generated column; migration backfill | Spec §5 ghi trigger; backfill để video có từ M2/M3 tìm được ngay |
| Cấu hình `simple` | Tiếng Việt đơn âm tiết, không stemming/stopword; bỏ dấu trước khi tách từ |
| Full-text và trigram trong **một câu SQL** (CTE `fts`, `trgm` với `NOT EXISTS (SELECT 1 FROM fts)`) | Quyết định "dự phòng" dựa trên toàn bộ kết quả (không theo trang) nên phân trang nhất quán; một round-trip |
| `word_similarity` / `<%` thay cho `similarity()` của spec | Từ khoá ngắn hơn tiêu đề nhiều; `similarity()` so cả chuỗi nên điểm thấp. `<%` dùng được index GIN trigram |
| `GET /api/genres` | Web dựng dropdown thể loại từ dữ liệu thật; `genre` khớp chính xác nên cần giá trị đúng |
| `ListReady` (M3) được thay bằng `Search` (`Query` rỗng = duyệt catalog) | Một đường dữ liệu cho catalog và tìm kiếm; không để lại code chết |
| nginx là cổng vào duy nhất, cùng origin (ADR 0011) | Cookie HttpOnly hoạt động không cần CORS; một URL cho người dùng |
| `proxy_pass` qua biến + `resolver 127.0.0.11` | nginx khởi động được khi chưa có api (test nginx dùng đúng template trong container không có api thật) và theo kịp IP mới của api |
| SPA build trong Docker multi-stage, không mount `dist` | `make up` không cần Node trên máy, không phục vụ nhầm `dist` cũ; giống cách build api/transcoder (ADR 0008) |
| Vite dev proxy `/api`, `/cdn` → nginx `:8088` | Dev đi đúng đường của bản build |
| React 18 + react-router-dom 7.18.5 (không dùng v6) | react-router 6.x còn advisory chưa vá (open redirect, GHSA-wrjc-x8rr-h8h6); 7.18.5 hỗ trợ React ≥ 18, API `BrowserRouter`/`Routes` giữ nguyên |
| Vitest 4.1.12 (không dùng 3.x) | Vitest ≤ 4.1.10 có advisory (tinypool, @vitest/mocker) |
| Logic phục hồi player là class thuần `PlaybackSession` + `classifyHlsError` | Test được bằng Vitest mà không cần hls.js / MSE; component `Player` chỉ nối dây |
| Trang xem tải lazy (`React.lazy`) | hls.js ~510 kB chỉ tải khi mở player; bundle chính ~196 kB |
| Trạng thái tìm kiếm trên URL (`/search?q=&genre=`) | Tải lại / chia sẻ link giữ kết quả; nút Back hoạt động |
| `e2e-m4.sh` dùng `curl` (không Playwright) | Kiểm tra phần hạ tầng (SPA fallback, proxy, cookie, tìm kiếm, `/play` cùng origin); player kiểm tra tay theo spec §9 |

## File Structure

```
streamfilm/
  migrations/0003_search.sql                 # mới: unaccent, pg_trgm, immutable_unaccent, cột search + trigger + index + backfill
  internal/catalog/repo.go                   # + SearchParams, Search, Genres; bỏ ListReady (Task 3)
  internal/catalog/search_test.go            # mới: tìm kiếm với Postgres thật, backfill migration
  internal/catalog/repo_test.go              # TestRepoListReady → TestRepoSearchBrowse
  internal/catalog/viewer.go(+_test)         # ?q=&genre=&year=, GET /api/genres
  cmd/api/router_test.go                     # emptyStore theo ViewerStore mới, route /api/genres
  deploy/nginx/templates/default.conf.template  # SPA, /assets/, /api/ proxy, /hls/ 404
  internal/signing/nginx_test.go             # + proxy /api/, SPA fallback, asset 404
  deploy/docker/nginx.Dockerfile             # mới: node build → nginx
  deploy/docker-compose.yml                  # nginx build image; CDN_BASE_URL từ env
  .dockerignore                              # + web/app/node_modules, web/app/dist
  Makefile                                   # e2e-m4, web-install, web-dev, web-test, web-build; logs-app thêm nginx
  scripts/e2e-m2.sh                          # E2E_TITLE, E2E_GENRE
  scripts/e2e-m3.sh                          # expect() không in body; bỏ split
  scripts/e2e-m4.sh                          # mới
  web/app/package.json, package-lock.json, tsconfig.json, vite.config.ts, index.html
  web/app/src/main.tsx, App.tsx, auth.tsx, api.ts(+test), format.ts(+test), styles.css, test/setup.ts
  web/app/src/components/SearchBox.tsx(+test), VideoGrid.tsx(+test), AuthForm.tsx(+test), Header.tsx, Player.tsx
  web/app/src/player/recovery.ts(+test)
  web/app/src/pages/LoginPage.tsx(+test), RegisterPage.tsx, HomePage.tsx, SearchPage.tsx, VideoPage.tsx, WatchPage.tsx
  README.md, docs/architecture.md, docs/api.md, docs/operations.md, docs/data-model.md, docs/flows.md
  docs/decisions/0011-same-origin-nginx-gateway.md, 0012-search-unaccent-trigram.md (mới)
```

`web/dev/player.html` giữ nguyên (vẫn phục vụ ở `/dev/`).

---

### Task 1: Carry-over từ review M3 + tham số cho e2e

**Files:**
- Modify: `scripts/e2e-m3.sh`, `scripts/e2e-m2.sh`, `deploy/docker-compose.yml`

**Interfaces:**
- Consumes: không đổi code chạy.
- Produces: `scripts/e2e-m2.sh` nhận thêm biến `E2E_TITLE` (mặc định `E2E HH:MM:SS`) và `E2E_GENRE` (mặc định `test`) — Task 10 dùng. Compose: `CDN_BASE_URL: ${CDN_BASE_URL:-http://localhost:8088}`.

- [ ] **Step 1: `expect()` trong `scripts/e2e-m3.sh` không in body (có thể chứa mật khẩu)**

Thay

```bash
# expect CODE curl-args...: gọi curl và so mã HTTP.
expect() {
  local want=$1; shift
  local code
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "curl $*: HTTP $code, want $want"
}
```

bằng

```bash
# expect CODE curl-args...: gọi curl và so mã HTTP. Khi lỗi chỉ in URL, không in tham số khác
# (body -d có thể chứa mật khẩu).
expect() {
  local want=$1; shift
  local code a url=""
  for a in "$@"; do case $a in http://* | https://*) url=$a ;; esac; done
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "$url: HTTP $code, want $want"
}
```

- [ ] **Step 2: Bỏ `split` khỏi `scripts/e2e-m3.sh`** (script này không gọi `split`; `e2e-m2.sh` tự kiểm tra)

Thay dòng `# Dùng: scripts/e2e-m3.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, openssl, split.` bằng

```bash
# Dùng: scripts/e2e-m3.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, openssl.
```

và dòng `for c in curl jq openssl split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done` bằng

```bash
for c in curl jq openssl; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
```

- [ ] **Step 3: `scripts/e2e-m2.sh` nhận tiêu đề và thể loại**

Thay dòng chú thích

```bash
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m2 nạp từ .env); E2E_ID_FILE: nếu đặt, ghi video id vào file đó.
```

bằng

```bash
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m2 nạp từ .env); E2E_ID_FILE: nếu đặt, ghi video id vào file đó;
# E2E_TITLE, E2E_GENRE: tiêu đề và thể loại của video tạo ra (e2e-m4 dùng tiêu đề tiếng Việt để kiểm tra tìm kiếm).
```

và

```bash
BODY=$(jq -n --arg t "E2E $(date +%H:%M:%S)" --arg f "$NAME" --argjson s "$SIZE" \
  '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}')
```

bằng

```bash
BODY=$(jq -n --arg t "${E2E_TITLE:-E2E $(date +%H:%M:%S)}" --arg g "${E2E_GENRE:-test}" --arg f "$NAME" \
  --argjson s "$SIZE" '{title: $t, filename: $f, size_bytes: $s, genres: [$g], year: 2026}')
```

- [ ] **Step 4: Compose đọc `CDN_BASE_URL` từ `.env`**

Trong `deploy/docker-compose.yml`, service `api`, thay dòng `      CDN_BASE_URL: http://localhost:8088` bằng

```yaml
      # Origin mà trình duyệt mở (nginx); /play trả URL tuyệt đối CDN_BASE_URL + /cdn/s/...
      CDN_BASE_URL: ${CDN_BASE_URL:-http://localhost:8088}
```

- [ ] **Step 5: Kiểm tra**

Run: `bash -n scripts/e2e-m2.sh && bash -n scripts/e2e-m3.sh && grep -c 'split' scripts/e2e-m3.sh; docker compose -f deploy/docker-compose.yml --env-file .env config | grep CDN_BASE_URL`
Expected: không lỗi cú pháp; `0`; `CDN_BASE_URL: http://localhost:8088`.

Run (stack đang chạy): `make e2e-m3`
Expected: dòng cuối `OK  M3` (video tạo ra có tiêu đề `E2E HH:MM:SS`, thể loại `test` như trước).

- [ ] **Step 6: Commit**

```bash
git add scripts/e2e-m2.sh scripts/e2e-m3.sh deploy/docker-compose.yml
git commit -m "chore(e2e): hide request bodies in e2e-m3 failures; title/genre params; CDN_BASE_URL from env"
```

---

### Task 2: Migration tìm kiếm và `Repo.Search` / `Repo.Genres`

**Files:**
- Create: `migrations/0003_search.sql`, `internal/catalog/search_test.go`
- Modify: `internal/catalog/repo.go`

**Interfaces:**
- Consumes: `catalog.Repo`, `Video`, `ReadyInfo`, `Status*`, `videoColumns`, `collectVideos`; helper test `newTestRepo`, `idA`, `idB`, `idC` (M2); `db.Connect`, `db.Migrate`, `migrations.FS`, `testutil.StartPostgres`.
- Produces:
  ```go
  type SearchParams struct {
  	Query  string // đã trim; rỗng = duyệt catalog, mới nhất trước
  	Genre  string // rỗng = mọi thể loại
  	Year   int    // 0 = mọi năm
  	Limit  int
  	Offset int
  }
  func (r *Repo) Search(ctx context.Context, p SearchParams) ([]Video, error)
  func (r *Repo) Genres(ctx context.Context) ([]string, error) // không bao giờ nil
  ```
  SQL: hàm `immutable_unaccent(text) text`, `videos_search_tsv(title, description) tsvector`, trigger `videos_search_update`, index `videos_search_idx`, `videos_title_trgm_idx`. `ListReady` **vẫn giữ** tới Task 3 để mọi task đều build được.

- [ ] **Step 1: Viết test thất bại** — `internal/catalog/search_test.go`:

```go
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
```

- [ ] **Step 2: Chạy để thấy lỗi**

Run: `go test ./internal/catalog -run 'TestRepoSearch$|TestSearchMigrationBackfill'`
Expected: lỗi biên dịch `undefined: SearchParams` và `r.Search undefined`.

- [ ] **Step 3: Migration** — `migrations/0003_search.sql`:

```sql
-- M4: tìm kiếm tiếng Việt có dấu và không dấu (spec §5, ADR 0012).
-- unaccent() là STABLE (phụ thuộc search_path và từ điển) nên không dùng trực tiếp trong index được;
-- immutable_unaccent ghim từ điển public.unaccent và khai báo IMMUTABLE.
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE FUNCTION immutable_unaccent(text) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
  RETURN public.unaccent('public.unaccent'::regdictionary, $1);

-- Tiêu đề trọng số A, mô tả trọng số B (ts_rank ưu tiên khớp tiêu đề). Cấu hình 'simple': không stemming,
-- không stopword — hợp với tiếng Việt (từ đơn âm tiết) sau khi bỏ dấu.
CREATE FUNCTION videos_search_tsv(title text, description text) RETURNS tsvector
  LANGUAGE sql IMMUTABLE PARALLEL SAFE
  RETURN setweight(to_tsvector('simple', immutable_unaccent(coalesce(title, ''))), 'A')
      || setweight(to_tsvector('simple', immutable_unaccent(coalesce(description, ''))), 'B');

ALTER TABLE videos ADD COLUMN search tsvector;

CREATE FUNCTION videos_search_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.search := videos_search_tsv(NEW.title, NEW.description);
  RETURN NEW;
END
$$;

CREATE TRIGGER videos_search_update
  BEFORE INSERT OR UPDATE OF title, description ON videos
  FOR EACH ROW EXECUTE FUNCTION videos_search_trigger();

-- Backfill các video có từ trước migration.
UPDATE videos SET search = videos_search_tsv(title, description);

CREATE INDEX videos_search_idx ON videos USING gin (search);
-- Trigram trên tiêu đề đã bỏ dấu: dự phòng chịu lỗi gõ (toán tử <% / word_similarity).
CREATE INDEX videos_title_trgm_idx ON videos USING gin (immutable_unaccent(title) gin_trgm_ops);
```

- [ ] **Step 4: Repository** — thêm vào cuối `internal/catalog/repo.go` (giữ nguyên `ListReady`):

```go
// SearchParams: tham số catalog / tìm kiếm cho người xem. Chỉ video ready được trả về.
type SearchParams struct {
	Query  string // đã trim; rỗng = duyệt catalog, mới nhất trước
	Genre  string // rỗng = mọi thể loại; khớp đúng một phần tử của genres
	Year   int    // 0 = mọi năm
	Limit  int
	Offset int
}

// browseSQL: không có từ khoá, mới nhất trước.
const browseSQL = `SELECT ` + videoColumns + ` FROM videos
	WHERE status = 'ready' AND ($1 = '' OR $1 = ANY(genres)) AND ($2 = 0 OR year = $2)
	ORDER BY created_at DESC, id LIMIT $3 OFFSET $4`

// searchSQL: full-text (websearch_to_tsquery trên cột search, xếp hạng ts_rank). Chỉ khi full-text
// không có kết quả nào (sau khi lọc) mới dùng trigram trên tiêu đề đã bỏ dấu để chịu lỗi gõ:
// toán tử <% (word_similarity >= pg_trgm.word_similarity_threshold, mặc định 0.6) dùng được index GIN.
const searchSQL = `WITH q AS (
		SELECT websearch_to_tsquery('simple', immutable_unaccent($1)) AS tsq, immutable_unaccent($1) AS uq
	), base AS (
		SELECT id, title, search FROM videos
		WHERE status = 'ready' AND ($2 = '' OR $2 = ANY(genres)) AND ($3 = 0 OR year = $3)
	), fts AS (
		SELECT b.id, ts_rank(b.search, q.tsq) AS score FROM base b, q WHERE b.search @@ q.tsq
	), trgm AS (
		SELECT b.id, word_similarity(q.uq, immutable_unaccent(b.title)) AS score FROM base b, q
		WHERE NOT EXISTS (SELECT 1 FROM fts) AND q.uq <% immutable_unaccent(b.title)
	), hits AS (
		SELECT id, score FROM fts UNION ALL SELECT id, score FROM trgm
	)
	SELECT ` + videoColumns + ` FROM hits JOIN videos USING (id)
	ORDER BY hits.score DESC, videos.created_at DESC, id LIMIT $4 OFFSET $5`

// Search trả video ready theo từ khoá (có dấu hoặc không dấu), thể loại và năm, phân trang limit/offset.
func (r *Repo) Search(ctx context.Context, p SearchParams) ([]Video, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if p.Query == "" {
		rows, err = r.pool.Query(ctx, browseSQL, p.Genre, p.Year, p.Limit, p.Offset)
	} else {
		rows, err = r.pool.Query(ctx, searchSQL, p.Query, p.Genre, p.Year, p.Limit, p.Offset)
	}
	if err != nil {
		return nil, fmt.Errorf("search videos: %w", err)
	}
	return collectVideos(rows)
}

// Genres trả các thể loại (không trùng, sắp xếp) của video ready, để web dựng bộ lọc.
func (r *Repo) Genres(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT g FROM videos, unnest(genres) AS g
		WHERE status = 'ready' ORDER BY g`)
	if err != nil {
		return nil, fmt.Errorf("list genres: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if out == nil {
		out = []string{}
	}
	return out, err
}
```

- [ ] **Step 5: Chạy test**

Run: `go test ./internal/catalog -run 'TestRepoSearch$|TestSearchMigrationBackfill' -v 2>&1 | grep -E '^(---|ok|FAIL)|--- FAIL'`
Expected:

```
--- PASS: TestRepoSearch (…)
--- PASS: TestSearchMigrationBackfill (…)
ok  	streamfilm/internal/catalog	…
```

(14 case con + 2 subtest đều PASS: "tro choi" → `[idA idE]` (tiêu đề xếp trước mô tả, `idC` chưa ready bị ẩn), "đường" → `idB`, "tro chpi" và "nguoi nhwn" đi nhánh trigram, lọc thể loại/năm, trigger khi sửa tiêu đề, `Genres` không có thể loại của video chưa ready.)

Run: `go test ./internal/db ./internal/catalog`
Expected: `ok` cả hai (`TestMigrate` vẫn qua với migration mới).

- [ ] **Step 6: Commit**

```bash
git add migrations/0003_search.sql internal/catalog/repo.go internal/catalog/search_test.go
git commit -m "feat(catalog): unaccent full-text search with trigram fallback"
```

---

### Task 3: API người xem — `?q=&genre=&year=` và `GET /api/genres`

**Files:**
- Modify: `internal/catalog/viewer.go`, `internal/catalog/viewer_test.go`, `internal/catalog/repo.go`, `internal/catalog/repo_test.go`, `cmd/api/router_test.go`

**Interfaces:**
- Consumes: `Repo.Search`, `Repo.Genres`, `SearchParams` (Task 2); `httpx.WriteJSON`, `httpx.WriteError`; helper test `get` (`handler_test.go`), `fakeSigner`, `newViewerRouter`.
- Produces:
  ```go
  type ViewerStore interface {
  	Search(ctx context.Context, p SearchParams) ([]Video, error)
  	Genres(ctx context.Context) ([]string, error)
  	Get(ctx context.Context, id string) (Video, error)
  	Renditions(ctx context.Context, id string) ([]Rendition, error)
  } // *Repo thoả
  const ( MaxQueryLen = 100; MaxGenreLen = 50; MinYear = 1888; MaxYear = 2100 )
  // GET /api/videos?q=&genre=&year=&page= → {"videos":[ViewerVideoJSON],"page":n,"has_more":bool} | 400
  // GET /api/genres → {"genres":[string]}
  ```
  Xoá `Repo.ListReady`. `cmd/api/main.go` không đổi (`*catalog.Repo` thoả interface mới).

- [ ] **Step 1: Viết test thất bại** — ghi đè `internal/catalog/viewer_test.go`:

```go
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

type fakeViewerStore struct {
	videos      []Video // thứ tự = thứ tự catalog
	genres      []string
	last        SearchParams
	limit, offs int
}

// Search giả: chỉ lọc ready và phân trang; tham số nhận được ghi vào last.
func (f *fakeViewerStore) Search(_ context.Context, p SearchParams) ([]Video, error) {
	f.last, f.limit, f.offs = p, p.Limit, p.Offset
	var ready []Video
	for _, v := range f.videos {
		if v.Status == StatusReady {
			ready = append(ready, v)
		}
	}
	if p.Offset >= len(ready) {
		return nil, nil
	}
	return ready[p.Offset:min(p.Offset+p.Limit, len(ready))], nil
}

func (f *fakeViewerStore) Genres(context.Context) ([]string, error) { return f.genres, nil }

func (f *fakeViewerStore) Get(_ context.Context, id string) (Video, error) {
	for _, v := range f.videos {
		if v.ID == id {
			return v, nil
		}
	}
	return Video{}, ErrNotFound
}

func (f *fakeViewerStore) Renditions(context.Context, string) ([]Rendition, error) {
	return []Rendition{{Height: 720, BitrateKbps: 2800, PlaylistKey: idA + "/720p/index.m3u8"}}, nil
}

type fakeSigner struct{ exp time.Time }

func (s fakeSigner) Path(id, file string) (string, time.Time, error) {
	return "/cdn/s/SIG/" + fmt.Sprint(s.exp.Unix()) + "/hls/" + id + "/" + file, s.exp, nil
}

func newViewerRouter(store ViewerStore) http.Handler {
	r := chi.NewRouter()
	(&ViewerHandler{Videos: store, Signer: fakeSigner{exp: time.Unix(1794736800, 0).UTC()},
		CDNBaseURL: "http://cdn.test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Routes(r)
	return r
}

func TestViewerList(t *testing.T) {
	store := &fakeViewerStore{}
	for i := range PageSize + 3 {
		store.videos = append(store.videos, Video{ID: fmt.Sprintf("v%02d", i), Title: "T", Status: StatusReady})
	}
	store.videos = append(store.videos, Video{ID: "hidden", Status: StatusProcessing})
	h := newViewerRouter(store)

	var body struct {
		Videos  []ViewerVideoJSON `json:"videos"`
		Page    int               `json:"page"`
		HasMore bool              `json:"has_more"`
	}
	rec := get(h, "/api/videos")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != PageSize || body.Page != 1 || !body.HasMore || store.limit != PageSize+1 || store.offs != 0 {
		t.Errorf("page 1: n=%d page=%d more=%v limit=%d off=%d", len(body.Videos), body.Page, body.HasMore, store.limit, store.offs)
	}
	if body.Videos[0].Genres == nil {
		t.Error("genres phải là [] chứ không phải null")
	}
	if strings.Contains(rec.Body.String(), `"status"`) || strings.Contains(rec.Body.String(), "raw_key") {
		t.Error("catalog người xem không được lộ status/raw_key")
	}

	rec = get(h, "/api/videos?page=2")
	body.Videos = nil
	json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Videos) != 3 || body.HasMore || store.offs != PageSize {
		t.Errorf("page 2: n=%d more=%v off=%d", len(body.Videos), body.HasMore, store.offs)
	}
	for _, q := range []string{"?page=0", "?page=abc", "?page=1001"} {
		if rec := get(h, "/api/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", q, rec.Code)
		}
	}
}

func TestViewerGetAndPlay(t *testing.T) {
	store := &fakeViewerStore{videos: []Video{
		{ID: idA, Title: "A", Status: StatusReady, DurationS: 30},
		{ID: idB, Title: "B", Status: StatusProcessing},
	}}
	h := newViewerRouter(store)

	rec := get(h, "/api/videos/"+idA)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"renditions":[{"height":720,"bitrate_kbps":2800}]`) {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}

	rec = get(h, "/api/videos/"+idA+"/play")
	if rec.Code != http.StatusOK {
		t.Fatalf("play: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("play phải có Cache-Control: no-store")
	}
	var p PlayJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.URL != "http://cdn.test/cdn/s/SIG/1794736800/hls/"+idA+"/master.m3u8" ||
		!p.ExpiresAt.Equal(time.Unix(1794736800, 0)) || p.ResumeAt != 0 || p.Subtitles == nil {
		t.Errorf("play = %+v", p)
	}
	if !strings.Contains(rec.Body.String(), `"subtitles":[]`) {
		t.Errorf("subtitles phải là []: %s", rec.Body)
	}

	for _, path := range []string{
		"/api/videos/" + idB, "/api/videos/" + idB + "/play", // chưa ready
		"/api/videos/" + idX + "/play", "/api/videos/not-a-uuid/play",
	} {
		if rec := get(h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: code = %d, want 404", path, rec.Code)
		}
	}
}

func TestViewerSearchParams(t *testing.T) {
	store := &fakeViewerStore{}
	h := newViewerRouter(store)

	rec := get(h, "/api/videos?q=%20tro%20choi%20&genre=H%C3%A0nh%20%C4%91%E1%BB%99ng&year=2021&page=3")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d %s", rec.Code, rec.Body)
	}
	want := SearchParams{Query: "tro choi", Genre: "Hành động", Year: 2021, Limit: PageSize + 1, Offset: 2 * PageSize}
	if store.last != want {
		t.Errorf("params = %+v, want %+v", store.last, want)
	}
	if !strings.Contains(rec.Body.String(), `"videos":[]`) {
		t.Errorf("không có kết quả thì videos phải là []: %s", rec.Body)
	}

	get(h, "/api/videos")
	if store.last != (SearchParams{Limit: PageSize + 1}) {
		t.Errorf("không tham số: %+v", store.last)
	}

	long := strings.Repeat("ă", MaxQueryLen+1)
	for _, q := range []string{"?year=abc", "?year=1800", "?year=2101", "?q=" + long,
		"?genre=" + strings.Repeat("g", MaxGenreLen+1)} {
		if rec := get(h, "/api/videos"+q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", q, rec.Code)
		}
	}
	if rec := get(h, "/api/videos?q="+strings.Repeat("ă", MaxQueryLen)); rec.Code != http.StatusOK {
		t.Errorf("q dài đúng %d ký tự phải hợp lệ: %d", MaxQueryLen, rec.Code)
	}
}

func TestViewerGenres(t *testing.T) {
	rec := get(newViewerRouter(&fakeViewerStore{}), "/api/genres")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"genres":[]}` {
		t.Errorf("empty: %d %s", rec.Code, rec.Body)
	}
	rec = get(newViewerRouter(&fakeViewerStore{genres: []string{"Gia đình", "Hành động"}}), "/api/genres")
	if strings.TrimSpace(rec.Body.String()) != `{"genres":["Gia đình","Hành động"]}` {
		t.Errorf("genres: %s", rec.Body)
	}
}
```

Trong `cmd/api/router_test.go`, thay

```go
func (emptyStore) ListReady(context.Context, int, int) ([]catalog.Video, error)      { return nil, nil }
```

bằng

```go
func (emptyStore) Search(context.Context, catalog.SearchParams) ([]catalog.Video, error) {
	return nil, nil
}
func (emptyStore) Genres(context.Context) ([]string, error) { return nil, nil }
```

và thêm ngay sau case `{"GET", "/api/videos", viewer, 200},`:

```go
		{"GET", "/api/videos?q=tro+choi&genre=drama&year=2024", viewer, 200},
		{"GET", "/api/genres", "", 401},
		{"GET", "/api/genres", viewer, 200},
```

Chạy `gofmt -w cmd/api/router_test.go`.

- [ ] **Step 2: Chạy để thấy lỗi**

Run: `go vet ./internal/catalog ./cmd/api`
Expected: lỗi `*fakeViewerStore does not implement ViewerStore (missing method ListReady)` (và tương tự cho `emptyStore`).

- [ ] **Step 3: Handler** — ghi đè `internal/catalog/viewer.go`:

```go
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"streamfilm/internal/httpx"
)

// ViewerStore là phần của Repo mà endpoint người xem cần.
type ViewerStore interface {
	Search(ctx context.Context, p SearchParams) ([]Video, error)
	Genres(ctx context.Context) ([]string, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
}

// PlaySigner ký đường dẫn HLS (signing.Signer).
type PlaySigner interface {
	Path(videoID, file string) (string, time.Time, error)
}

// ViewerHandler: catalog và /play cho user đã đăng nhập (router bọc auth.RequireUser).
type ViewerHandler struct {
	Videos     ViewerStore
	Signer     PlaySigner
	CDNBaseURL string // ví dụ http://localhost:8088; URL phát = CDNBaseURL + path đã ký
	Log        *slog.Logger
}

// PageSize: số video mỗi trang của GET /api/videos.
const PageSize = 20

// Giới hạn tham số tìm kiếm của GET /api/videos.
const (
	MaxQueryLen = 100 // rune
	MaxGenreLen = 50  // rune
	MinYear     = 1888
	MaxYear     = 2100
)

func (h *ViewerHandler) Routes(r chi.Router) {
	r.Get("/api/videos", h.list)
	r.Get("/api/genres", h.genres)
	r.Get("/api/videos/{id}", h.get)
	r.Get("/api/videos/{id}/play", h.play)
}

type ViewerRenditionJSON struct {
	Height      int `json:"height"`
	BitrateKbps int `json:"bitrate_kbps"`
}

// ViewerVideoJSON không lộ thông tin nội bộ (status, raw_key, size_bytes, fail_reason).
type ViewerVideoJSON struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Description string                `json:"description"`
	Genres      []string              `json:"genres"`
	Year        int                   `json:"year,omitempty"`
	DurationS   int                   `json:"duration_s"`
	CreatedAt   time.Time             `json:"created_at"`
	Renditions  []ViewerRenditionJSON `json:"renditions,omitempty"`
}

type SubtitleJSON struct {
	Lang  string `json:"lang"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// PlayJSON: subtitles luôn là [] và resume_at luôn 0 tới M5 (giữ nguyên hình dạng cho web M4).
type PlayJSON struct {
	URL       string         `json:"url"`
	ExpiresAt time.Time      `json:"expires_at"`
	Subtitles []SubtitleJSON `json:"subtitles"`
	ResumeAt  int            `json:"resume_at"`
}

func toViewerJSON(v Video) ViewerVideoJSON {
	genres := v.Genres
	if genres == nil {
		genres = []string{}
	}
	return ViewerVideoJSON{ID: v.ID, Title: v.Title, Description: v.Description, Genres: genres,
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}

// list: GET /api/videos?q=&genre=&year=&page=. Không có q thì duyệt catalog (mới nhất trước);
// có q thì tìm không dấu, xếp theo độ liên quan (Repo.Search).
func (h *ViewerHandler) list(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	page := 1
	if s := qs.Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			httpx.WriteError(w, http.StatusBadRequest, "page must be between 1 and 1000")
			return
		}
		page = n
	}
	p := SearchParams{Query: strings.TrimSpace(qs.Get("q")), Genre: strings.TrimSpace(qs.Get("genre"))}
	if utf8.RuneCountInString(p.Query) > MaxQueryLen {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("q must be at most %d characters", MaxQueryLen))
		return
	}
	if utf8.RuneCountInString(p.Genre) > MaxGenreLen {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("genre must be at most %d characters", MaxGenreLen))
		return
	}
	if s := qs.Get("year"); s != "" {
		y, err := strconv.Atoi(s)
		if err != nil || y < MinYear || y > MaxYear {
			httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("year must be between %d and %d", MinYear, MaxYear))
			return
		}
		p.Year = y
	}
	// Lấy dư 1 bản ghi để biết còn trang sau.
	p.Limit, p.Offset = PageSize+1, (page-1)*PageSize
	vs, err := h.Videos.Search(r.Context(), p)
	if err != nil {
		h.Log.Error("search videos", "q", p.Query, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	hasMore := len(vs) > PageSize
	if hasMore {
		vs = vs[:PageSize]
	}
	out := make([]ViewerVideoJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, toViewerJSON(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"videos": out, "page": page, "has_more": hasMore})
}

// genres: GET /api/genres → {"genres": [...]} các thể loại của video ready.
func (h *ViewerHandler) genres(w http.ResponseWriter, r *http.Request) {
	gs, err := h.Videos.Genres(r.Context())
	if err != nil {
		h.Log.Error("list genres", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if gs == nil {
		gs = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"genres": gs})
}

// readyVideo trả video nếu tồn tại và ready; ngược lại ghi 404 (người xem không thấy video chưa ready).
func (h *ViewerHandler) readyVideo(w http.ResponseWriter, r *http.Request) (Video, bool) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return Video{}, false
	}
	v, err := h.Videos.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) || (err == nil && v.Status != StatusReady) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return Video{}, false
	}
	if err != nil {
		h.Log.Error("get video", "video_id", id, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return Video{}, false
	}
	return v, true
}

func (h *ViewerHandler) get(w http.ResponseWriter, r *http.Request) {
	v, ok := h.readyVideo(w, r)
	if !ok {
		return
	}
	rs, err := h.Videos.Renditions(r.Context(), v.ID)
	if err != nil {
		h.Log.Error("get renditions", "video_id", v.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := toViewerJSON(v)
	for _, rd := range rs {
		out.Renditions = append(out.Renditions, ViewerRenditionJSON{Height: rd.Height, BitrateKbps: rd.BitrateKbps})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *ViewerHandler) play(w http.ResponseWriter, r *http.Request) {
	v, ok := h.readyVideo(w, r)
	if !ok {
		return
	}
	path, exp, err := h.Signer.Path(v.ID, "master.m3u8")
	if err != nil {
		h.Log.Error("sign play url", "video_id", v.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, PlayJSON{URL: h.CDNBaseURL + path, ExpiresAt: exp,
		Subtitles: []SubtitleJSON{}, ResumeAt: 0})
}
```

- [ ] **Step 4: Bỏ `ListReady`** — trong `internal/catalog/repo.go` xoá nguyên hàm

```go
// ListReady trả video ready (catalog cho người xem), mới nhất trước, phân trang bằng limit/offset.
// Tìm kiếm (q, genre) thêm ở M4.
func (r *Repo) ListReady(ctx context.Context, limit, offset int) ([]Video, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE status = 'ready' ORDER BY created_at DESC, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}
```

Trong `internal/catalog/repo_test.go`: đổi `func TestRepoListReady(` thành `func TestRepoSearchBrowse(`; `all, err := r.ListReady(ctx, 10, 0)` thành `all, err := r.Search(ctx, SearchParams{Limit: 10})`; `t.Fatalf("ListReady = %+v, err = %v", all, err)` thành `t.Fatalf("Search = %+v, err = %v", all, err)`; `page2, err := r.ListReady(ctx, 1, 1)` thành `page2, err := r.Search(ctx, SearchParams{Limit: 1, Offset: 1})`.

- [ ] **Step 5: Chạy test**

Run: `gofmt -l . ; go vet ./... && grep -rn ListReady --include='*.go' . ; go test ./internal/catalog ./cmd/api`
Expected: `gofmt`/`grep` không in gì; `ok  streamfilm/internal/catalog`, `ok  streamfilm/cmd/api`.

- [ ] **Step 6: Commit**

```bash
git add internal/catalog cmd/api/router_test.go
git commit -m "feat(api): search params on GET /api/videos and GET /api/genres"
```

---

### Task 4: nginx — cổng vào cùng origin (SPA, `/api/`, `/cdn/`)

**Files:**
- Modify: `deploy/nginx/templates/default.conf.template`, `internal/signing/nginx_test.go`

**Interfaces:**
- Consumes: template M3 (location `/cdn/s/...`, `/cdn/`, `/dev/`), `signing.SignedPath`, `Signature`, `Dir`, `TTL`, hằng test `testSecret`, `testVID`.
- Produces: location `/api/` (proxy nguyên URI tới `api:8000`), `/hls/` → 404, `/assets/` (cache 1 năm, thiếu → 404), `/` (SPA fallback `index.html`). Test nginx thêm container giả `api` (alias `api`, cổng 8000) và helper `getBody(t, url) string`.

- [ ] **Step 1: Viết test thất bại** — ghi đè `internal/signing/nginx_test.go`:

```go
package signing

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// nginxImage phải trùng image của service nginx trong deploy/docker-compose.yml.
const nginxImage = "nginx:1.27-alpine"

// originConf: nginx thứ hai đóng vai MinIO (alias "minio", cổng 9000), phục vụ file tĩnh
// dưới /srv/hls/... giống bucket hls cho đọc ẩn danh (ADR 0002), và đóng vai api (alias "api",
// cổng 8000) trả lại URI nhận được để kiểm tra proxy /api/ giữ nguyên đường dẫn và query.
const originConf = `server { listen 9000; root /srv; }
server { listen 8000; location / { return 200 "api:$request_uri"; } }`

// TestNginxSecureLinkParity chạy đúng template nginx của repo và kiểm tra nginx chấp nhận
// URL do SignedPath sinh ra, từ chối URL sửa chữ ký / hết hạn / khác video.
func TestNginxSecureLinkParity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()

	nw, err := network.New(ctx)
	testcontainers.CleanupNetwork(t, nw)
	if err != nil {
		t.Fatal(err)
	}

	origin, err := testcontainers.Run(ctx, nginxImage,
		network.WithNetwork([]string{"minio", "api"}, nw),
		testcontainers.WithExposedPorts("9000/tcp", "8000/tcp"),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: strings.NewReader(originConf),
				ContainerFilePath: "/etc/nginx/conf.d/default.conf", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("#EXTM3U\n"),
				ContainerFilePath: "/srv/hls/" + testVID + "/master.m3u8", FileMode: 0o644},
			testcontainers.ContainerFile{Reader: strings.NewReader("segment-bytes"),
				ContainerFilePath: "/srv/hls/" + testVID + "/720p/seg_000.ts", FileMode: 0o644},
		),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("9000/tcp"), wait.ForListeningPort("8000/tcp")),
	)
	testcontainers.CleanupContainer(t, origin)
	if err != nil {
		t.Fatal(err)
	}

	cdn, err := testcontainers.Run(ctx, nginxImage,
		network.WithNetwork([]string{"cdn"}, nw),
		testcontainers.WithExposedPorts("80/tcp"),
		testcontainers.WithEnv(map[string]string{
			"CDN_SIGNING_SECRET":    testSecret,
			"NGINX_ENVSUBST_FILTER": "^CDN_",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      "../../deploy/nginx/templates/default.conf.template",
			ContainerFilePath: "/etc/nginx/templates/default.conf.template", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("80/tcp")),
	)
	testcontainers.CleanupContainer(t, cdn)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := cdn.PortEndpoint(ctx, "80/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}

	exp := time.Now().Add(TTL).Unix()
	past := time.Now().Add(-time.Minute).Unix()
	master := SignedPath(testSecret, testVID, "master.m3u8", exp)
	sig := Signature(testSecret, Dir(testVID), exp)
	flipped := "A"
	if sig[0] == 'A' {
		flipped = "B"
	}
	tampered := strings.Replace(master, sig, flipped+sig[1:], 1)
	otherVID := "1b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"

	cases := []struct {
		name, path string
		want       int
		body       string
	}{
		{"signed master", master, 200, "#EXTM3U\n"},
		{"signed segment (same prefix)", SignedPath(testSecret, testVID, "720p/seg_000.ts", exp), 200, "segment-bytes"},
		{"tampered signature", tampered, 403, ""},
		{"expired", SignedPath(testSecret, testVID, "master.m3u8", past), 403, ""},
		{"exp changed, old signature", fmt.Sprintf("/cdn/s/%s/%d/hls/%s/master.m3u8", sig, exp+3600, testVID), 403, ""},
		{"signature of another video", fmt.Sprintf("/cdn/s/%s/%d/hls/%s/master.m3u8", sig, exp, otherVID), 403, ""},
		{"wrong secret", SignedPath(testSecret+"x", testVID, "master.m3u8", exp), 403, ""},
		{"non-numeric exp", fmt.Sprintf("/cdn/s/%s/abc/hls/%s/master.m3u8", sig, testVID), 403, ""},
		{"malformed cdn path", "/cdn/s/" + sig, 403, ""},
		{"unsigned hls path", "/hls/" + testVID + "/master.m3u8", 404, ""},
		// M4: cùng origin với SPA và API (ADR 0011).
		{"api proxied with path and query", "/api/videos?q=tro%20choi&page=2", 200, "api:/api/videos?q=tro%20choi&page=2"},
		{"spa root", "/", 200, ""},
		{"spa fallback route", "/watch/" + testVID, 200, ""},
		{"missing asset", "/assets/missing.js", 404, ""},
	}
	for _, c := range cases {
		resp, err := http.Get(endpoint + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: GET %s = %d, want %d", c.name, c.path, resp.StatusCode, c.want)
			continue
		}
		if c.body != "" && string(body) != c.body {
			t.Errorf("%s: body = %q, want %q", c.name, body, c.body)
		}
		if strings.HasPrefix(c.path, "/cdn/") && resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: thiếu CORS header (player cần đọc được mã 403)", c.name)
		}
	}

	// Fallback SPA trả đúng index.html (react-router xử lý route phía trình duyệt).
	if root, route := getBody(t, endpoint+"/"), getBody(t, endpoint+"/search?q=x"); root == "" || root != route {
		t.Errorf("SPA fallback body khác index.html: %q vs %q", route, root)
	}

	// Lần hai cùng file (kể cả với chữ ký khác) là cache HIT: cache key không chứa chữ ký.
	other := SignedPath(testSecret, testVID, "master.m3u8", exp+60)
	resp, err := http.Get(endpoint + other)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("X-Cache-Status"); got != "HIT" {
		t.Errorf("X-Cache-Status = %q, want HIT", got)
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

- [ ] **Step 2: Chạy để thấy lỗi**

Run: `go test ./internal/signing -run Nginx`
Expected: FAIL với `api proxied with path and query: GET /api/videos?q=tro%20choi&page=2 = 404, want 200`, `spa root: GET / = 404, want 200`, `spa fallback route: GET /watch/… = 404, want 200` (template M3 trả 404 cho mọi đường khác `/cdn/`, `/dev/`).

- [ ] **Step 3: Template** — ghi đè `deploy/nginx/templates/default.conf.template`:

```nginx
# Cổng vào duy nhất của trình duyệt (M4, ADR 0011): SPA người xem ở /, API ở /api/ (proxy tới api:8000),
# "CDN" giả lập ở /cdn/ (chỉ phục vụ HLS qua URL có chữ ký, M3). Cùng origin nên cookie HttpOnly
# của API dùng được mà không cần CORS.
# Đây là template: lúc khởi động, image nginx chạy envsubst (chỉ với biến khớp NGINX_ENVSUBST_FILTER=^CDN_)
# rồi ghi ra /etc/nginx/conf.d/default.conf. Biến nginx ($sl_exp, $uri, ...) giữ nguyên.
proxy_cache_path /var/cache/nginx/hls levels=1:2 keys_zone=hls:10m max_size=1g inactive=7d use_temp_path=off;

server {
    listen 80;

    location = /healthz { return 200 "ok\n"; }

    # /cdn/s/{sig}/{exp}/hls/{id}/{file}
    # sig = base64url(MD5("{exp}/hls/{id}/ {secret}")) không padding; khớp internal/signing (ADR 0009).
    location ~ ^/cdn/s/(?<sl_sig>[^/]+)/(?<sl_exp>[^/]+)/hls/(?<sl_vid>[^/]+)/(?<sl_file>.+)$ {
        secure_link $sl_sig,$sl_exp;
        secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";

        add_header Access-Control-Allow-Origin * always;
        add_header X-Cache-Status $upstream_cache_status always;

        # $secure_link: "" = chữ ký sai (hoặc exp không phải số), "0" = hết hạn, "1" = hợp lệ.
        # Spec §4.3: sai hoặc hết hạn đều trả 403.
        if ($secure_link = "") { return 403; }
        if ($secure_link = "0") { return 403; }

        # Bỏ prefix chữ ký rồi proxy tới MinIO: /hls/{id}/{file} = bucket hls, key {id}/{file}.
        rewrite ^ /hls/$sl_vid/$sl_file break;
        proxy_pass http://minio:9000;
        proxy_cache hls;
        proxy_cache_valid 200 7d;
        # Không đưa chữ ký vào cache key: mọi người xem dùng chung một bản cache của segment.
        proxy_cache_key "$sl_vid/$sl_file";
    }

    # URL /cdn/ sai dạng (thiếu sig/exp/id): coi như chữ ký sai.
    location /cdn/ {
        add_header Access-Control-Allow-Origin * always;
        return 403;
    }

    # API: chuyển nguyên URI (/api/...) tới service api. proxy_pass dùng biến + resolver DNS của Docker
    # nên nginx vẫn khởi động khi api chưa chạy và theo kịp IP mới khi api được tạo lại.
    location /api/ {
        resolver 127.0.0.11 valid=10s ipv6=off;
        set $api_upstream http://api:8000;
        proxy_pass $api_upstream;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Request-Id $request_id;
    }

    # Trang test hls.js (M1): /dev/player.html?src=<URL ký từ /api/videos/{id}/play>.
    location /dev/ {
        root /usr/share/nginx/html;
    }

    # Không có đường /hls/ không ký (kể cả fallback SPA).
    location /hls/ { return 404; }

    # File build của Vite có hash trong tên: cache lâu dài.
    location /assets/ {
        root /usr/share/nginx/html;
        add_header Cache-Control "public, max-age=31536000, immutable";
        try_files $uri =404;
    }

    # SPA: file tĩnh nếu có, còn lại trả index.html để react-router xử lý (/watch/{id}, /search, ...).
    location / {
        root /usr/share/nginx/html;
        add_header Cache-Control "no-cache";
        try_files $uri /index.html;
    }
}
```

- [ ] **Step 4: Chạy test**

Run: `go test ./internal/signing -run Nginx -v 2>&1 | grep -E '^(---|ok|FAIL)'`
Expected: `--- PASS: TestNginxSecureLinkParity`, `ok  streamfilm/internal/signing`.

- [ ] **Step 5: Commit**

```bash
git add deploy/nginx/templates/default.conf.template internal/signing/nginx_test.go
git commit -m "feat(nginx): serve SPA and proxy /api/ on the CDN origin"
```

---
### Task 5: Khung `web/app` — Vite, TypeScript, client API

**Files:**
- Create: `web/app/package.json`, `web/app/package-lock.json` (sinh bởi npm), `web/app/tsconfig.json`, `web/app/vite.config.ts`, `web/app/index.html`, `web/app/src/test/setup.ts`, `web/app/src/api.ts`, `web/app/src/api.test.ts`, `web/app/src/format.ts`, `web/app/src/format.test.ts`

**Interfaces:**
- Consumes: JSON của API Go: `auth.UserJSON`, `catalog.ViewerVideoJSON`, `PlayJSON`, `{"error": …}`.
- Produces (`src/api.ts`):
  ```ts
  export type Role = 'admin' | 'viewer';
  export interface User { id: string; email: string; role: Role; created_at: string }
  export interface Rendition { height: number; bitrate_kbps: number }
  export interface Video { id: string; title: string; description: string; genres: string[]; year?: number; duration_s: number; created_at: string; renditions?: Rendition[] }
  export interface VideoPage { videos: Video[]; page: number; has_more: boolean }
  export interface Subtitle { lang: string; label: string; url: string }
  export interface PlayInfo { url: string; expires_at: string; subtitles: Subtitle[]; resume_at: number }
  export interface VideoQuery { q?: string; genre?: string; year?: number; page?: number }
  export class ApiError extends Error { readonly status: number }
  export function setUnauthorizedHandler(fn: (() => void) | null): void
  export function videosQueryString(q: VideoQuery): string
  export const api: {
    me(): Promise<User>; login(email: string, password: string): Promise<User>; register(email: string, password: string): Promise<User>;
    logout(): Promise<void>; videos(q?: VideoQuery): Promise<VideoPage>; video(id: string): Promise<Video>;
    play(id: string): Promise<PlayInfo>; genres(): Promise<string[]>;
  }
  ```
  `src/format.ts`: `formatDuration(totalSeconds: number): string`, `posterHue(id: string): number`.
  Script npm: `dev`, `build` (`tsc --noEmit && vite build`), `preview`, `test` (`vitest run`), `test:watch`.

- [ ] **Step 1: Cấu hình dự án**

`web/app/package.json`:

```json
{
  "name": "streamfilm-web",
  "private": true,
  "version": "0.4.0",
  "type": "module",
  "engines": {
    "node": ">=20.19"
  },
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "test:watch": "vitest"
  },
  "dependencies": {
    "hls.js": "1.6.5",
    "react": "18.3.1",
    "react-dom": "18.3.1",
    "react-router-dom": "7.18.5"
  },
  "devDependencies": {
    "@testing-library/dom": "10.4.0",
    "@testing-library/jest-dom": "6.6.3",
    "@testing-library/react": "16.3.0",
    "@types/react": "18.3.23",
    "@types/react-dom": "18.3.7",
    "@vitejs/plugin-react": "4.5.2",
    "jsdom": "26.1.0",
    "typescript": "5.8.3",
    "vite": "6.4.5",
    "vitest": "4.1.12"
  }
}
```

`web/app/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "isolatedModules": true,
    "skipLibCheck": true,
    "noEmit": true,
    "types": ["vite/client"]
  },
  "include": ["src"]
}
```

`web/app/vite.config.ts`:

```ts
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// Dev server (npm run dev, cổng 5173): /api và /cdn đi qua nginx của `make up` (cổng 8088),
// giống hệt đường đi của bản build nên cookie phiên và URL ký hoạt động như thật (ADR 0011).
const gateway = 'http://localhost:8088';

export default defineConfig({
  plugins: [react()],
  build: {
    // Chunk của trang xem chứa hls.js (~510 kB, đã tách lazy khỏi bundle chính).
    chunkSizeWarningLimit: 600,
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': gateway,
      '/cdn': gateway,
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
  },
});
```

`web/app/index.html`:

```html
<!doctype html>
<html lang="vi">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Streamfilm</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`web/app/src/test/setup.ts`:

```ts
import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

afterEach(() => {
  cleanup();
});
```

- [ ] **Step 2: Cài dependency, sinh lockfile**

Run: `cd web/app && node -v && npm install --no-audit --no-fund --before=2026-11-01 && npm audit && npm ls --depth=0`
Expected: `v20.19.6`; `added 166 packages`; `found 0 vulnerabilities`; `npm ls` liệt kê đúng 14 package với đúng version trong `package.json` (`react-router-dom@7.18.5`, `vitest@4.1.12`, `vite@6.4.5`, …). `--before` giữ mọi dependency bắc cầu ở bản phát hành trước 2026-11-01. `node_modules/` và `dist/` đã nằm trong `.gitignore` gốc.

- [ ] **Step 3: Viết test thất bại**

`web/app/src/api.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, setUnauthorizedHandler, videosQueryString } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(null);
});

describe('videosQueryString', () => {
  it('bỏ tham số rỗng và page 1', () => {
    expect(videosQueryString({})).toBe('');
    expect(videosQueryString({ q: '  ', genre: '', page: 1 })).toBe('');
  });

  it('mã hoá từ khoá, thể loại, năm, trang', () => {
    expect(videosQueryString({ q: ' tro choi ', genre: 'Hành động', year: 2021, page: 2 })).toBe(
      '?q=tro+choi&genre=H%C3%A0nh+%C4%91%E1%BB%99ng&year=2021&page=2',
    );
  });
});

describe('request', () => {
  it('ném ApiError với thông báo lỗi của server và gọi handler khi 401', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: 'login required' }), { status: 401 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);

    const err = await api.videos({ q: 'tro choi' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
    expect((err as ApiError).message).toBe('login required');
    expect(onUnauthorized).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/videos?q=tro+choi',
      expect.objectContaining({ credentials: 'same-origin' }),
    );
  });

  it('401 khi đăng nhập sai không coi là hết phiên', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":"bad"}', { status: 401 })));
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    await expect(api.login('a@b.vn', 'x')).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it('gửi JSON khi có body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: 'u', email: 'a@b.vn', role: 'viewer', created_at: '' }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    await api.login('a@b.vn', 'secret-123');
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe('POST');
    expect(init.body).toBe('{"email":"a@b.vn","password":"secret-123"}');
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json');
  });
});
```

`web/app/src/format.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { formatDuration, posterHue } from './format';

describe('formatDuration', () => {
  it('định dạng phút:giây và giờ:phút:giây', () => {
    expect(formatDuration(0)).toBe('');
    expect(formatDuration(30)).toBe('0:30');
    expect(formatDuration(75)).toBe('1:15');
    expect(formatDuration(3723)).toBe('1:02:03');
  });
});

describe('posterHue', () => {
  it('ổn định và nằm trong 0..359', () => {
    const h = posterHue('11111111-1111-1111-1111-111111111111');
    expect(h).toBe(posterHue('11111111-1111-1111-1111-111111111111'));
    expect(h).toBeGreaterThanOrEqual(0);
    expect(h).toBeLessThan(360);
  });
});
```

Run: `cd web/app && npm test`
Expected: FAIL — `Failed to resolve import "./api"` và `"./format"`.

- [ ] **Step 4: Cài đặt**

`web/app/src/api.ts`:

```ts
// Client cho API Go (cùng origin: nginx proxy /api/ → api, ADR 0011). Cookie phiên sf_session là
// HttpOnly nên JavaScript không đọc được; trình duyệt tự gửi kèm vì cùng origin.

export type Role = 'admin' | 'viewer';

export interface User {
  id: string;
  email: string;
  role: Role;
  created_at: string;
}

export interface Rendition {
  height: number;
  bitrate_kbps: number;
}

export interface Video {
  id: string;
  title: string;
  description: string;
  genres: string[];
  year?: number;
  duration_s: number;
  created_at: string;
  renditions?: Rendition[];
}

export interface VideoPage {
  videos: Video[];
  page: number;
  has_more: boolean;
}

export interface Subtitle {
  lang: string;
  label: string;
  url: string;
}

export interface PlayInfo {
  url: string;
  expires_at: string;
  subtitles: Subtitle[];
  resume_at: number;
}

export interface VideoQuery {
  q?: string;
  genre?: string;
  year?: number;
  page?: number;
}

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

let onUnauthorized: (() => void) | null = null;

// setUnauthorizedHandler: AuthProvider đăng ký để mọi 401 (cookie hết hạn) đưa người dùng về trang đăng nhập.
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body !== undefined) headers.set('Content-Type', 'application/json');
  const res = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // body không phải JSON: giữ statusText
    }
    if (res.status === 401 && !path.startsWith('/api/auth/')) onUnauthorized?.();
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// videosQueryString bỏ tham số rỗng; page 1 là mặc định nên không ghi.
export function videosQueryString(q: VideoQuery): string {
  const p = new URLSearchParams();
  if (q.q?.trim()) p.set('q', q.q.trim());
  if (q.genre) p.set('genre', q.genre);
  if (q.year) p.set('year', String(q.year));
  if (q.page && q.page > 1) p.set('page', String(q.page));
  const s = p.toString();
  return s ? `?${s}` : '';
}

const json = (body: unknown) => JSON.stringify(body);

export const api = {
  me: () => request<User>('/api/me'),
  login: (email: string, password: string) =>
    request<User>('/api/auth/login', { method: 'POST', body: json({ email, password }) }),
  register: (email: string, password: string) =>
    request<User>('/api/auth/register', { method: 'POST', body: json({ email, password }) }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  videos: (q: VideoQuery = {}) => request<VideoPage>(`/api/videos${videosQueryString(q)}`),
  video: (id: string) => request<Video>(`/api/videos/${encodeURIComponent(id)}`),
  play: (id: string) => request<PlayInfo>(`/api/videos/${encodeURIComponent(id)}/play`),
  genres: () => request<{ genres: string[] }>('/api/genres').then((r) => r.genres),
};
```

`web/app/src/format.ts`:

```ts
// formatDuration: 75 → "1:15", 3723 → "1:02:03", 0 → "".
export function formatDuration(totalSeconds: number): string {
  if (!totalSeconds || totalSeconds < 0) return '';
  const s = Math.floor(totalSeconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

// posterHue: màu ổn định theo id cho poster tạm (poster thật có ở M5).
export function posterHue(id: string): number {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return h;
}
```

- [ ] **Step 5: Chạy test và type-check**

Run: `cd web/app && npx tsc --noEmit && npm test`
Expected: `tsc` không in gì; `Test Files  2 passed (2)`, `Tests  7 passed (7)`.

- [ ] **Step 6: Commit**

```bash
git add web/app/package.json web/app/package-lock.json web/app/tsconfig.json web/app/vite.config.ts web/app/index.html web/app/src
git commit -m "feat(web): scaffold React + Vite app with typed API client"
```

---

### Task 6: Component — ô tìm kiếm (debounce), lưới phim, form đăng nhập

**Files:**
- Create: `web/app/src/components/SearchBox.tsx`, `SearchBox.test.tsx`, `VideoGrid.tsx`, `VideoGrid.test.tsx`, `AuthForm.tsx`, `AuthForm.test.tsx` (đều trong `web/app/src/components/`)

**Interfaces:**
- Consumes: `Video`, `ApiError` (`src/api.ts`); `formatDuration`, `posterHue` (`src/format.ts`); `Link` (`react-router-dom`).
- Produces:
  ```ts
  export interface SearchBoxProps { initial?: string; delay?: number; onSearch: (q: string) => void }
  export function SearchBox(props: SearchBoxProps): JSX.Element // debounce 300 ms mặc định, Enter tìm ngay, không gọi lại khi từ khoá (đã trim) không đổi
  export interface VideoGridProps { videos: Video[]; emptyText?: string }
  export function VideoGrid(props: VideoGridProps): JSX.Element // thẻ link /videos/{id}
  export interface Credentials { email: string; password: string }
  export interface CredentialErrors { email?: string; password?: string }
  export const MIN_PASSWORD_BYTES = 8; export const MAX_PASSWORD_BYTES = 72;
  export function validateCredentials(c: Credentials): CredentialErrors
  export interface AuthFormProps { title: string; submitLabel: string; onSubmit: (c: Credentials) => Promise<void>; footer?: ReactNode }
  export function AuthForm(props: AuthFormProps): JSX.Element // ApiError 401 → "Email hoặc mật khẩu không đúng.", 409 → "Email này đã được đăng ký."
  ```

- [ ] **Step 1: Viết test thất bại**

`web/app/src/components/SearchBox.test.tsx`:

```tsx
import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SearchBox } from './SearchBox';

describe('SearchBox', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('chỉ tìm một lần với từ khoá cuối, sau khi ngừng gõ 300 ms', () => {
    const onSearch = vi.fn();
    render(<SearchBox onSearch={onSearch} />);
    const input = screen.getByRole('searchbox', { name: 'Tìm phim' });

    for (const v of ['t', 'tr', 'tro', 'tro ', 'tro c']) {
      fireEvent.change(input, { target: { value: v } });
      act(() => {
        vi.advanceTimersByTime(100);
      });
    }
    expect(onSearch).not.toHaveBeenCalled();

    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(onSearch).toHaveBeenCalledTimes(1);
    expect(onSearch).toHaveBeenCalledWith('tro c');
  });

  it('không gọi khi giá trị ban đầu chưa đổi; Enter tìm ngay; không lặp lại cùng từ khoá', () => {
    const onSearch = vi.fn();
    render(<SearchBox initial="phim" onSearch={onSearch} />);
    const input = screen.getByRole('searchbox');
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onSearch).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: 'trò chơi  ' } });
    fireEvent.submit(input);
    expect(onSearch).toHaveBeenCalledWith('trò chơi');
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onSearch).toHaveBeenCalledTimes(1);
  });
});
```

`web/app/src/components/VideoGrid.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { Video } from '../api';
import { VideoGrid } from './VideoGrid';

const video = (id: string, title: string, extra: Partial<Video> = {}): Video => ({
  id,
  title,
  description: '',
  genres: [],
  duration_s: 0,
  created_at: '2026-11-15T00:00:00Z',
  ...extra,
});

describe('VideoGrid', () => {
  it('hiện mỗi phim một thẻ dẫn tới trang chi tiết', () => {
    render(
      <MemoryRouter>
        <VideoGrid
          videos={[
            video('11111111-1111-1111-1111-111111111111', 'Trò chơi con mực', { year: 2021, duration_s: 3723 }),
            video('22222222-2222-2222-2222-222222222222', 'Đường về nhà'),
          ]}
        />
      </MemoryRouter>,
    );
    const links = screen.getAllByRole('link');
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveAttribute('href', '/videos/11111111-1111-1111-1111-111111111111');
    expect(screen.getByRole('heading', { name: 'Trò chơi con mực' })).toBeInTheDocument();
    expect(screen.getByText('2021 · 1:02:03')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Đường về nhà' })).toBeInTheDocument();
  });

  it('danh sách rỗng hiện thông báo', () => {
    render(
      <MemoryRouter>
        <VideoGrid videos={[]} emptyText="Không tìm thấy phim nào." />
      </MemoryRouter>,
    );
    expect(screen.getByText('Không tìm thấy phim nào.')).toBeInTheDocument();
    expect(screen.queryByRole('list')).not.toBeInTheDocument();
  });
});
```

`web/app/src/components/AuthForm.test.tsx`:

```tsx
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api';
import { AuthForm, validateCredentials } from './AuthForm';

function fill(email: string, password: string) {
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: email } });
  fireEvent.change(screen.getByLabelText('Mật khẩu'), { target: { value: password } });
  fireEvent.click(screen.getByRole('button', { name: 'Đăng nhập' }));
}

describe('validateCredentials', () => {
  it('kiểm tra email và độ dài mật khẩu theo byte', () => {
    expect(validateCredentials({ email: 'a@b.vn', password: '12345678' })).toEqual({});
    expect(validateCredentials({ email: 'không-phải-email', password: '12345678' }).email).toBe('Email không hợp lệ.');
    expect(validateCredentials({ email: 'a@b.vn', password: '1234567' }).password).toBeDefined();
    // "đ" là 2 byte UTF-8: 36 ký tự = 72 byte hợp lệ, 37 ký tự = 74 byte quá giới hạn.
    expect(validateCredentials({ email: 'a@b.vn', password: 'đ'.repeat(36) })).toEqual({});
    expect(validateCredentials({ email: 'a@b.vn', password: 'đ'.repeat(37) }).password).toBeDefined();
  });
});

describe('AuthForm', () => {
  it('dữ liệu sai: hiện lỗi, không gửi', () => {
    const onSubmit = vi.fn();
    render(<AuthForm title="Đăng nhập" submitLabel="Đăng nhập" onSubmit={onSubmit} />);
    fill('abc', '123');
    expect(screen.getByText('Email không hợp lệ.')).toBeInTheDocument();
    expect(screen.getByText('Mật khẩu cần 8–72 ký tự.')).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('dữ liệu đúng: gửi email đã trim', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<AuthForm title="Đăng nhập" submitLabel="Đăng nhập" onSubmit={onSubmit} />);
    fill('  viewer@example.com ', 'viewer-password-1');
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({ email: 'viewer@example.com', password: 'viewer-password-1' }),
    );
  });

  it('server trả 401: hiện thông báo sai thông tin đăng nhập', async () => {
    const onSubmit = vi.fn().mockRejectedValue(new ApiError(401, 'invalid credentials'));
    render(<AuthForm title="Đăng nhập" submitLabel="Đăng nhập" onSubmit={onSubmit} />);
    fill('viewer@example.com', 'wrong-password');
    expect(await screen.findByRole('alert')).toHaveTextContent('Email hoặc mật khẩu không đúng.');
  });
});
```

Run: `cd web/app && npm test`
Expected: 3 file FAIL với `Failed to resolve import "./SearchBox"` / `"./VideoGrid"` / `"./AuthForm"`; 2 file của Task 5 vẫn PASS.

- [ ] **Step 2: Cài đặt**

`web/app/src/components/SearchBox.tsx`:

```tsx
import { useEffect, useRef, useState, type FormEvent } from 'react';

export interface SearchBoxProps {
  initial?: string;
  delay?: number; // ms chờ sau lần gõ cuối rồi mới tìm (debounce)
  onSearch: (q: string) => void;
}

// SearchBox gọi onSearch với từ khoá đã trim, sau khi người dùng ngừng gõ `delay` ms, hoặc ngay khi
// nhấn Enter. Không gọi lại nếu từ khoá không đổi.
export function SearchBox({ initial = '', delay = 300, onSearch }: SearchBoxProps) {
  const [value, setValue] = useState(initial);
  const lastEmitted = useRef(initial.trim());
  const onSearchRef = useRef(onSearch);
  onSearchRef.current = onSearch;

  const emit = (raw: string) => {
    const q = raw.trim();
    if (q === lastEmitted.current) return;
    lastEmitted.current = q;
    onSearchRef.current(q);
  };

  useEffect(() => {
    const t = setTimeout(() => emit(value), delay);
    return () => clearTimeout(t);
  }, [value, delay]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    emit(value);
  };

  return (
    <form role="search" className="search-box" onSubmit={submit}>
      <input
        type="search"
        aria-label="Tìm phim"
        placeholder="Tìm phim (gõ có dấu hoặc không dấu)…"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        autoFocus
      />
    </form>
  );
}
```

`web/app/src/components/VideoGrid.tsx`:

```tsx
import { Link } from 'react-router-dom';
import type { Video } from '../api';
import { formatDuration, posterHue } from '../format';

export interface VideoGridProps {
  videos: Video[];
  emptyText?: string;
}

export function VideoGrid({ videos, emptyText = 'Chưa có phim nào.' }: VideoGridProps) {
  if (videos.length === 0) return <p className="empty">{emptyText}</p>;
  return (
    <ul className="grid">
      {videos.map((v) => (
        <li key={v.id}>
          <Link to={`/videos/${v.id}`} className="card">
            {/* Poster thật (poster_key) có ở M5; tạm dùng khối màu theo id. */}
            <div className="poster" style={{ background: `hsl(${posterHue(v.id)} 45% 30%)` }} aria-hidden="true">
              {v.title.charAt(0).toUpperCase()}
            </div>
            <h3>{v.title}</h3>
            <p className="meta">{[v.year, formatDuration(v.duration_s)].filter(Boolean).join(' · ')}</p>
          </Link>
        </li>
      ))}
    </ul>
  );
}
```

`web/app/src/components/AuthForm.tsx`:

```tsx
import { useState, type FormEvent, type ReactNode } from 'react';
import { ApiError } from '../api';

export interface Credentials {
  email: string;
  password: string;
}

export interface CredentialErrors {
  email?: string;
  password?: string;
}

// Cùng giới hạn với server (internal/auth: mật khẩu 8–72 byte, vì bcrypt chỉ dùng 72 byte đầu).
export const MIN_PASSWORD_BYTES = 8;
export const MAX_PASSWORD_BYTES = 72;

export function validateCredentials(c: Credentials): CredentialErrors {
  const errors: CredentialErrors = {};
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(c.email.trim())) errors.email = 'Email không hợp lệ.';
  const bytes = new TextEncoder().encode(c.password).length;
  if (bytes < MIN_PASSWORD_BYTES || bytes > MAX_PASSWORD_BYTES) {
    errors.password = `Mật khẩu cần ${MIN_PASSWORD_BYTES}–${MAX_PASSWORD_BYTES} ký tự.`;
  }
  return errors;
}

function serverMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return 'Email hoặc mật khẩu không đúng.';
    if (err.status === 409) return 'Email này đã được đăng ký.';
    if (err.status === 400) return 'Thông tin không hợp lệ.';
  }
  return 'Có lỗi xảy ra, vui lòng thử lại.';
}

export interface AuthFormProps {
  title: string;
  submitLabel: string;
  onSubmit: (c: Credentials) => Promise<void>;
  footer?: ReactNode;
}

export function AuthForm({ title, submitLabel, onSubmit, footer }: AuthFormProps) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [errors, setErrors] = useState<CredentialErrors>({});
  const [serverError, setServerError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const c = { email: email.trim(), password };
    const v = validateCredentials(c);
    setErrors(v);
    setServerError('');
    if (v.email || v.password) return;
    setSubmitting(true);
    try {
      await onSubmit(c);
    } catch (err) {
      setServerError(serverMessage(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form className="auth-form" onSubmit={submit} noValidate>
      <h1>{title}</h1>
      <label>
        Email
        <input type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)}
          aria-invalid={Boolean(errors.email)} />
      </label>
      {errors.email && <p className="field-error">{errors.email}</p>}
      <label>
        Mật khẩu
        <input type="password" autoComplete="current-password" value={password}
          onChange={(e) => setPassword(e.target.value)} aria-invalid={Boolean(errors.password)} />
      </label>
      {errors.password && <p className="field-error">{errors.password}</p>}
      {serverError && <p role="alert" className="form-error">{serverError}</p>}
      <button type="submit" disabled={submitting}>
        {submitting ? 'Đang gửi…' : submitLabel}
      </button>
      {footer}
    </form>
  );
}
```

- [ ] **Step 3: Chạy test**

Run: `cd web/app && npx tsc --noEmit && npm test`
Expected: `Test Files  5 passed (5)`, `Tests  15 passed (15)`.

- [ ] **Step 4: Commit**

```bash
git add web/app/src/components
git commit -m "feat(web): debounced search box, video grid and auth form"
```

---

### Task 7: Player — phục hồi khi URL ký hết hạn

**Files:**
- Create: `web/app/src/player/recovery.ts`, `web/app/src/player/recovery.test.ts`, `web/app/src/components/Player.tsx`

**Interfaces:**
- Consumes: `api.play`, `ApiError`, `PlayInfo` (`src/api.ts`); `Hls`, `ErrorData` (`hls.js` 1.6.5).
- Produces (`src/player/recovery.ts`):
  ```ts
  export interface HlsErrorLike { type: string; details?: string; fatal: boolean; response?: { code?: number } | null }
  export type RecoveryAction = 'refresh' | 'media' | 'fatal';
  export function classifyHlsError(e: HlsErrorLike): RecoveryAction | null
  export interface PlaybackDeps {
    fetchPlay: () => Promise<PlayInfo>; load: (url: string, startAt: number) => void; currentTime: () => number;
    onFatal: (message: string) => void; now?: () => number; maxRefreshes?: number; refreshWindowMs?: number;
  }
  export function playErrorMessage(err: unknown): string
  export class PlaybackSession { constructor(deps: PlaybackDeps); start(): Promise<void>; refresh(): Promise<void>; dispose(): void }
  ```
  `src/components/Player.tsx`: `export function Player({ videoId }: { videoId: string }): JSX.Element` — hls.js (Chrome/Firefox/Edge) hoặc HLS gốc (Safari), chọn chất lượng (`hls.currentLevel`, `-1` = tự động).

- [ ] **Step 1: Viết test thất bại** — `web/app/src/player/recovery.test.ts`:

```ts
import { describe, expect, it, vi } from 'vitest';
import { ApiError, type PlayInfo } from '../api';
import { PlaybackSession, classifyHlsError } from './recovery';

const play = (n: number, resumeAt = 0): PlayInfo => ({
  url: `http://localhost:8088/cdn/s/sig${n}/100${n}/hls/v/master.m3u8`,
  expires_at: '2026-11-15T12:00:00Z',
  subtitles: [],
  resume_at: resumeAt,
});

function setup(overrides: { now?: () => number } = {}) {
  let n = 0;
  const fetchPlay = vi.fn(async () => play(++n, 12));
  const load = vi.fn();
  const onFatal = vi.fn();
  let time = 0;
  const session = new PlaybackSession({
    fetchPlay,
    load,
    onFatal,
    currentTime: () => time,
    ...overrides,
  });
  return { session, fetchPlay, load, onFatal, setTime: (t: number) => (time = t) };
}

describe('classifyHlsError', () => {
  it('403 (URL ký hết hạn) → refresh, kể cả khi chưa fatal', () => {
    expect(classifyHlsError({ type: 'networkError', details: 'fragLoadError', fatal: false, response: { code: 403 } })).toBe('refresh');
    expect(classifyHlsError({ type: 'networkError', details: 'manifestLoadError', fatal: true, response: { code: 403 } })).toBe('refresh');
  });

  it('lỗi mạng fatal → refresh; lỗi media fatal → media; lỗi khác fatal → fatal', () => {
    expect(classifyHlsError({ type: 'networkError', fatal: true })).toBe('refresh');
    expect(classifyHlsError({ type: 'mediaError', fatal: true })).toBe('media');
    expect(classifyHlsError({ type: 'otherError', fatal: true })).toBe('fatal');
  });

  it('lỗi không fatal và không phải 403 → bỏ qua (hls.js tự retry)', () => {
    expect(classifyHlsError({ type: 'networkError', fatal: false, response: { code: 500 } })).toBeNull();
    expect(classifyHlsError({ type: 'mediaError', fatal: false })).toBeNull();
  });
});

describe('PlaybackSession', () => {
  it('start phát URL từ /play tại resume_at', async () => {
    const { session, load } = setup();
    await session.start();
    expect(load).toHaveBeenCalledWith(play(1).url, 12);
  });

  it('refresh gọi lại /play và phát tiếp từ vị trí hiện tại', async () => {
    const { session, fetchPlay, load, setTime } = setup();
    await session.start();
    setTime(42.5);
    await session.refresh();
    expect(fetchPlay).toHaveBeenCalledTimes(2);
    expect(load).toHaveBeenLastCalledWith(play(2).url, 42.5);
  });

  it('nhiều lỗi cùng lúc chỉ gọi /play một lần', async () => {
    const { session, fetchPlay, load } = setup();
    await session.start();
    await Promise.all([session.refresh(), session.refresh(), session.refresh()]);
    expect(fetchPlay).toHaveBeenCalledTimes(2);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('quá 3 lần trong 60 giây thì báo lỗi; hết cửa sổ thì được thử lại', async () => {
    let now = 1_000_000;
    const { session, fetchPlay, onFatal } = setup({ now: () => now });
    for (let i = 0; i < 3; i++) await session.refresh();
    expect(onFatal).not.toHaveBeenCalled();
    await session.refresh();
    expect(onFatal).toHaveBeenCalledOnce();
    expect(fetchPlay).toHaveBeenCalledTimes(3);
    now += 60_000;
    await session.refresh();
    expect(fetchPlay).toHaveBeenCalledTimes(4);
  });

  it('/play lỗi → thông báo theo mã lỗi', async () => {
    const onFatal = vi.fn();
    const session = new PlaybackSession({
      fetchPlay: () => Promise.reject(new ApiError(401, 'login required')),
      load: vi.fn(),
      currentTime: () => 0,
      onFatal,
    });
    await session.start();
    expect(onFatal).toHaveBeenCalledWith('Phiên đăng nhập đã hết hạn. Vui lòng đăng nhập lại.');
  });

  it('sau dispose không nạp nguồn nữa', async () => {
    const { session, load } = setup();
    const p = session.start();
    session.dispose();
    await p;
    await session.refresh();
    expect(load).not.toHaveBeenCalled();
  });
});
```

Run: `cd web/app && npx vitest run src/player`
Expected: FAIL — `Failed to resolve import "./recovery"`.

- [ ] **Step 2: Logic phục hồi** — `web/app/src/player/recovery.ts`:

```ts
// Logic phục hồi của player, tách khỏi hls.js và React để test được (spec §4.3 bước 6):
// URL ký hết hạn (nginx trả 403) → gọi lại /play lấy URL mới → phát tiếp từ vị trí hiện tại.
import { ApiError, type PlayInfo } from '../api';

// Phần của dữ liệu sự kiện Hls.Events.ERROR mà ta dùng. Giá trị type là Hls.ErrorTypes của hls.js 1.x
// ('networkError', 'mediaError', ...), ghi literal để module này không phải import hls.js.
export interface HlsErrorLike {
  type: string;
  details?: string;
  fatal: boolean;
  response?: { code?: number } | null;
}

// refresh: lấy URL mới qua /play; media: hls.recoverMediaError(); fatal: báo lỗi cho người xem.
export type RecoveryAction = 'refresh' | 'media' | 'fatal';

export function classifyHlsError(e: HlsErrorLike): RecoveryAction | null {
  // 403 từ nginx = chữ ký sai hoặc hết hạn. hls.js không retry lỗi 4xx; với segment có thể chưa fatal
  // nhưng ta làm mới ngay thay vì đợi.
  if (e.response?.code === 403) return 'refresh';
  if (!e.fatal) return null;
  if (e.type === 'networkError') return 'refresh';
  if (e.type === 'mediaError') return 'media';
  return 'fatal';
}

export interface PlaybackDeps {
  fetchPlay: () => Promise<PlayInfo>;
  // load nạp URL HLS và bắt đầu phát từ startAt giây (0 = đầu phim).
  load: (url: string, startAt: number) => void;
  currentTime: () => number;
  onFatal: (message: string) => void;
  now?: () => number;
  maxRefreshes?: number; // số lần làm mới tối đa trong refreshWindowMs (mặc định 3)
  refreshWindowMs?: number; // mặc định 60 000
}

export function playErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return 'Phiên đăng nhập đã hết hạn. Vui lòng đăng nhập lại.';
    if (err.status === 404) return 'Không tìm thấy phim hoặc phim chưa sẵn sàng.';
  }
  return 'Không tải được video. Vui lòng thử lại.';
}

export class PlaybackSession {
  private readonly deps: Required<PlaybackDeps>;
  private refreshes: number[] = [];
  private inflight: Promise<void> | null = null;
  private disposed = false;

  constructor(deps: PlaybackDeps) {
    this.deps = { now: Date.now, maxRefreshes: 3, refreshWindowMs: 60_000, ...deps };
  }

  // start gọi /play lần đầu và phát từ resume_at.
  async start(): Promise<void> {
    try {
      const p = await this.deps.fetchPlay();
      if (!this.disposed) this.deps.load(p.url, p.resume_at);
    } catch (err) {
      if (!this.disposed) this.deps.onFatal(playErrorMessage(err));
    }
  }

  // refresh lấy URL ký mới và phát tiếp từ currentTime. Nhiều lỗi cùng lúc (playlist và segment
  // song song) chỉ gây một lần gọi /play. Quá maxRefreshes lần trong refreshWindowMs thì dừng
  // để không lặp vô hạn (ví dụ secret của api và nginx lệch nhau).
  refresh(): Promise<void> {
    if (this.disposed) return Promise.resolve();
    if (this.inflight) return this.inflight;
    const now = this.deps.now();
    this.refreshes = this.refreshes.filter((t) => now - t < this.deps.refreshWindowMs);
    if (this.refreshes.length >= this.deps.maxRefreshes) {
      this.deps.onFatal('Không phát được video: liên kết phát liên tục bị từ chối.');
      return Promise.resolve();
    }
    this.refreshes.push(now);
    const at = this.deps.currentTime();
    this.inflight = this.deps
      .fetchPlay()
      .then((p) => {
        if (!this.disposed) this.deps.load(p.url, at);
      })
      .catch((err: unknown) => {
        if (!this.disposed) this.deps.onFatal(playErrorMessage(err));
      })
      .finally(() => {
        this.inflight = null;
      });
    return this.inflight;
  }

  dispose(): void {
    this.disposed = true;
  }
}
```

Run: `cd web/app && npx vitest run src/player`
Expected: `Tests  9 passed (9)`.

- [ ] **Step 3: Component player** — `web/app/src/components/Player.tsx` (nối hls.js với `PlaybackSession`; không có unit test vì cần MSE, kiểm tra tay ở Task 10 Step 4 theo spec §9):

```tsx
import Hls, { type ErrorData } from 'hls.js';
import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { PlaybackSession, classifyHlsError } from '../player/recovery';

interface Level {
  index: number;
  height: number;
}

// Player phát HLS qua URL ký từ /play. hls.js cho Chrome/Firefox/Edge; Safari dùng HLS gốc của <video>.
// Khi URL hết hạn (403) hoặc lỗi mạng: PlaybackSession gọi lại /play và phát tiếp từ vị trí hiện tại.
// Kiểm tra tay trên Chrome và Safari (spec §9); logic phục hồi có test trong player/recovery.test.ts.
export function Player({ videoId }: { videoId: string }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);
  const [error, setError] = useState('');
  const [levels, setLevels] = useState<Level[]>([]);
  const [level, setLevel] = useState(-1); // -1 = tự động (ABR)

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    setError('');
    setLevels([]);
    setLevel(-1);

    const session = new PlaybackSession({
      fetchPlay: () => api.play(videoId),
      currentTime: () => video.currentTime,
      onFatal: setError,
      load: (url, startAt) => {
        const resume = !video.paused;
        hlsRef.current?.destroy();
        hlsRef.current = null;
        if (Hls.isSupported()) {
          const hls = new Hls({ startPosition: startAt > 0 ? startAt : -1 });
          hlsRef.current = hls;
          hls.on(Hls.Events.MANIFEST_PARSED, (_e, data) => {
            setLevels(data.levels.map((l, index) => ({ index, height: l.height })).sort((a, b) => b.height - a.height));
            if (resume) void video.play().catch(() => undefined);
          });
          hls.on(Hls.Events.ERROR, (_e, data: ErrorData) => {
            switch (classifyHlsError(data)) {
              case 'refresh':
                void session.refresh();
                break;
              case 'media':
                hls.recoverMediaError();
                break;
              case 'fatal':
                setError('Không phát được video.');
                break;
            }
          });
          hls.loadSource(url);
          hls.attachMedia(video);
        } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = url;
          video.addEventListener(
            'loadedmetadata',
            () => {
              if (startAt > 0) video.currentTime = startAt;
              if (resume) void video.play().catch(() => undefined);
            },
            { once: true },
          );
        } else {
          setError('Trình duyệt không hỗ trợ HLS.');
        }
      },
    });

    // Safari (HLS gốc) không cho biết mã HTTP: lỗi nạp nguồn nào cũng thử lấy URL mới.
    const onNativeError = () => {
      if (!hlsRef.current && video.error?.code === MediaError.MEDIA_ERR_NETWORK) void session.refresh();
    };
    video.addEventListener('error', onNativeError);
    void session.start();

    return () => {
      session.dispose();
      video.removeEventListener('error', onNativeError);
      hlsRef.current?.destroy();
      hlsRef.current = null;
      video.removeAttribute('src');
      video.load();
    };
  }, [videoId]);

  const changeLevel = (value: number) => {
    setLevel(value);
    if (hlsRef.current) hlsRef.current.currentLevel = value;
  };

  return (
    <div className="player">
      <video ref={videoRef} controls playsInline />
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {levels.length > 1 && (
        <label className="quality">
          Chất lượng
          <select value={level} onChange={(e) => changeLevel(Number(e.target.value))}>
            <option value={-1}>Tự động</option>
            {levels.map((l) => (
              <option key={l.index} value={l.index}>
                {l.height}p
              </option>
            ))}
          </select>
        </label>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Type-check và test**

Run: `cd web/app && npx tsc --noEmit && npm test`
Expected: `tsc` không in gì; `Test Files  6 passed (6)`, `Tests  24 passed (24)`.

- [ ] **Step 5: Commit**

```bash
git add web/app/src/player web/app/src/components/Player.tsx
git commit -m "feat(web): hls.js player that re-fetches /play on 403 and resumes"
```

---

### Task 8: Trang, đăng nhập và định tuyến

**Files:**
- Create: `web/app/src/auth.tsx`, `web/app/src/components/Header.tsx`, `web/app/src/pages/LoginPage.tsx`, `LoginPage.test.ts`, `RegisterPage.tsx`, `HomePage.tsx`, `SearchPage.tsx`, `VideoPage.tsx`, `WatchPage.tsx` (trong `web/app/src/pages/`), `web/app/src/App.tsx`, `web/app/src/main.tsx`, `web/app/src/styles.css`

**Interfaces:**
- Consumes: `api`, `ApiError`, `setUnauthorizedHandler`, các type (`src/api.ts`); `SearchBox`, `VideoGrid`, `AuthForm`, `Player` (Task 6–7); `formatDuration`, `posterHue`.
- Produces:
  ```ts
  // src/auth.tsx
  export function AuthProvider({ children }: { children: ReactNode }): JSX.Element
  export function useAuth(): { user: User | null; loading: boolean; login(e: string, p: string): Promise<void>; register(e: string, p: string): Promise<void>; logout(): Promise<void> }
  export function RequireAuth({ children }: { children: ReactNode }): JSX.Element // chưa đăng nhập → /login, state.from = trang đang mở
  // src/pages/LoginPage.tsx
  export function returnTo(state: unknown): string // chỉ nhận đường dẫn nội bộ bắt đầu bằng "/" (không "//")
  export function LoginPage(): JSX.Element
  // RegisterPage, HomePage, SearchPage, VideoPage, WatchPage, Header, App: component không props
  ```
  Route: `/login`, `/register` (public); `/`, `/search?q=&genre=`, `/videos/:id`, `/watch/:id` (bọc `RequireAuth` + `Header`); `*` → "Không tìm thấy trang". `WatchPage` tải lazy.

- [ ] **Step 1: Viết test thất bại** — `web/app/src/pages/LoginPage.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { returnTo } from './LoginPage';

describe('returnTo', () => {
  it('chỉ nhận đường dẫn nội bộ', () => {
    expect(returnTo({ from: '/watch/abc?x=1' })).toBe('/watch/abc?x=1');
    expect(returnTo(null)).toBe('/');
    expect(returnTo({ from: 'https://evil.example' })).toBe('/');
    expect(returnTo({ from: '//evil.example' })).toBe('/');
  });
});
```

Run: `cd web/app && npx vitest run src/pages`
Expected: FAIL — `Failed to resolve import "./LoginPage"`.

- [ ] **Step 2: Phiên đăng nhập** — `web/app/src/auth.tsx`:

```tsx
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { ApiError, api, setUnauthorizedHandler, type User } from './api';

interface AuthState {
  user: User | null;
  loading: boolean;
  login: (email: string, password: string) => Promise<void>;
  register: (email: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

// AuthProvider hỏi /api/me một lần khi tải trang để biết cookie phiên còn hợp lệ không.
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setUnauthorizedHandler(() => setUser(null));
    api
      .me()
      .then(setUser)
      .catch((err: unknown) => {
        if (!(err instanceof ApiError && err.status === 401)) console.error('GET /api/me', err);
        setUser(null);
      })
      .finally(() => setLoading(false));
    return () => setUnauthorizedHandler(null);
  }, []);

  const login = useCallback(async (email: string, password: string) => {
    setUser(await api.login(email, password));
  }, []);
  const register = useCallback(async (email: string, password: string) => {
    setUser(await api.register(email, password));
  }, []);
  const logout = useCallback(async () => {
    await api.logout();
    setUser(null);
  }, []);

  const value = useMemo(() => ({ user, loading, login, register, logout }), [user, loading, login, register, logout]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>');
  return ctx;
}

// RequireAuth: chưa đăng nhập thì chuyển tới /login, nhớ trang đang mở để quay lại sau khi đăng nhập.
export function RequireAuth({ children }: { children: ReactNode }) {
  const { user, loading } = useAuth();
  const location = useLocation();
  if (loading) return <p className="status">Đang tải…</p>;
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname + location.search }} />;
  return <>{children}</>;
}
```

- [ ] **Step 3: Header** — `web/app/src/components/Header.tsx`:

```tsx
import { Link, NavLink, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth';

export function Header() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

  const onLogout = async () => {
    await logout();
    navigate('/login', { replace: true });
  };

  return (
    <header className="site-header">
      <Link to="/" className="brand">
        Streamfilm
      </Link>
      {user && (
        <>
          <nav>
            <NavLink to="/" end>
              Trang chủ
            </NavLink>
            <NavLink to="/search">Tìm kiếm</NavLink>
          </nav>
          <div className="user">
            <span title={user.role === 'admin' ? 'Quản trị viên' : 'Người xem'}>{user.email}</span>
            <button type="button" className="link-button" onClick={onLogout}>
              Đăng xuất
            </button>
          </div>
        </>
      )}
    </header>
  );
}
```

- [ ] **Step 4: Trang**

`web/app/src/pages/LoginPage.tsx`:

```tsx
import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth';
import { AuthForm } from '../components/AuthForm';

// returnTo: trang người dùng định mở trước khi bị chuyển tới /login (RequireAuth ghi vào state.from).
export function returnTo(state: unknown): string {
  const from = (state as { from?: unknown } | null)?.from;
  return typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') ? from : '/';
}

export function LoginPage() {
  const { user, login } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const next = returnTo(location.state);
  if (user) return <Navigate to={next} replace />;
  return (
    <main className="auth-page">
      <AuthForm
        title="Đăng nhập"
        submitLabel="Đăng nhập"
        onSubmit={async ({ email, password }) => {
          await login(email, password);
          navigate(next, { replace: true });
        }}
        footer={
          <p>
            Chưa có tài khoản? <Link to="/register" state={location.state}>Đăng ký</Link>
          </p>
        }
      />
    </main>
  );
}
```

`web/app/src/pages/RegisterPage.tsx`:

```tsx
import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth';
import { AuthForm } from '../components/AuthForm';
import { returnTo } from './LoginPage';

export function RegisterPage() {
  const { user, register } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const next = returnTo(location.state);
  if (user) return <Navigate to={next} replace />;
  return (
    <main className="auth-page">
      <AuthForm
        title="Tạo tài khoản"
        submitLabel="Đăng ký"
        onSubmit={async ({ email, password }) => {
          // Đăng ký xong server đặt cookie luôn (M3), không cần đăng nhập lại.
          await register(email, password);
          navigate(next, { replace: true });
        }}
        footer={
          <p>
            Đã có tài khoản? <Link to="/login" state={location.state}>Đăng nhập</Link>
          </p>
        }
      />
    </main>
  );
}
```

`web/app/src/pages/HomePage.tsx`:

```tsx
import { useEffect, useState } from 'react';
import { api, type Video } from '../api';
import { VideoGrid } from '../components/VideoGrid';

export function HomePage() {
  const [videos, setVideos] = useState<Video[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    api
      .videos({ page })
      .then((res) => {
        if (cancelled) return;
        setVideos((prev) => (page === 1 ? res.videos : [...prev, ...res.videos]));
        setHasMore(res.has_more);
        setError('');
      })
      .catch(() => !cancelled && setError('Không tải được danh sách phim.'))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [page]);

  return (
    <section>
      <h1>Phim mới</h1>
      {error && <p role="alert" className="form-error">{error}</p>}
      {!loading || videos.length > 0 ? <VideoGrid videos={videos} /> : <p className="status">Đang tải…</p>}
      {hasMore && (
        <button type="button" className="more" disabled={loading} onClick={() => setPage((p) => p + 1)}>
          {loading ? 'Đang tải…' : 'Xem thêm'}
        </button>
      )}
    </section>
  );
}
```

`web/app/src/pages/SearchPage.tsx`:

```tsx
import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { api, type VideoPage } from '../api';
import { SearchBox } from '../components/SearchBox';
import { VideoGrid } from '../components/VideoGrid';

// Trạng thái tìm kiếm nằm trên URL (/search?q=&genre=) nên tải lại trang hoặc chia sẻ link vẫn giữ kết quả.
export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const genre = params.get('genre') ?? '';
  const [genres, setGenres] = useState<string[]>([]);
  const [result, setResult] = useState<VideoPage | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    api.genres().then(setGenres).catch(() => setGenres([]));
  }, []);

  useEffect(() => {
    let cancelled = false; // bỏ kết quả của truy vấn cũ về muộn
    api
      .videos({ q, genre })
      .then((res) => {
        if (!cancelled) {
          setResult(res);
          setError('');
        }
      })
      .catch(() => !cancelled && setError('Tìm kiếm thất bại, vui lòng thử lại.'));
    return () => {
      cancelled = true;
    };
  }, [q, genre]);

  const update = (next: { q?: string; genre?: string }) => {
    const merged = { q, genre, ...next };
    const p = new URLSearchParams();
    if (merged.q) p.set('q', merged.q);
    if (merged.genre) p.set('genre', merged.genre);
    setParams(p, { replace: true });
  };

  return (
    <section>
      <h1>Tìm kiếm</h1>
      <div className="search-bar">
        <SearchBox initial={q} onSearch={(value) => update({ q: value })} />
        <select aria-label="Thể loại" value={genre} onChange={(e) => update({ genre: e.target.value })}>
          <option value="">Mọi thể loại</option>
          {genres.map((g) => (
            <option key={g} value={g}>
              {g}
            </option>
          ))}
        </select>
      </div>
      {error && <p role="alert" className="form-error">{error}</p>}
      {result ? (
        <>
          {q && (
            <p className="status">
              {result.videos.length === 0
                ? `Không có kết quả cho “${q}”.`
                : `Kết quả cho “${q}”${result.has_more ? ' (hiện 20 phim đầu)' : ''}:`}
            </p>
          )}
          <VideoGrid videos={result.videos} emptyText="Không tìm thấy phim nào." />
        </>
      ) : (
        !error && <p className="status">Đang tìm…</p>
      )}
    </section>
  );
}
```

`web/app/src/pages/VideoPage.tsx`:

```tsx
import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api, type Video } from '../api';
import { formatDuration, posterHue } from '../format';

export function VideoPage() {
  const { id = '' } = useParams();
  const [video, setVideo] = useState<Video | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setVideo(null);
    setError('');
    api
      .video(id)
      .then((v) => !cancelled && setVideo(v))
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof ApiError && err.status === 404 ? 'Không tìm thấy phim.' : 'Không tải được phim.');
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  if (error) return <p role="alert" className="form-error">{error}</p>;
  if (!video) return <p className="status">Đang tải…</p>;
  return (
    <article className="detail">
      <div className="poster large" style={{ background: `hsl(${posterHue(video.id)} 45% 30%)` }} aria-hidden="true">
        {video.title.charAt(0).toUpperCase()}
      </div>
      <div>
        <h1>{video.title}</h1>
        <p className="meta">{[video.year, formatDuration(video.duration_s)].filter(Boolean).join(' · ')}</p>
        {video.genres.length > 0 && (
          <ul className="tags">
            {video.genres.map((g) => (
              <li key={g}>
                <Link to={`/search?genre=${encodeURIComponent(g)}`}>{g}</Link>
              </li>
            ))}
          </ul>
        )}
        {video.description && <p className="description">{video.description}</p>}
        {video.renditions && video.renditions.length > 0 && (
          <p className="meta">Chất lượng: {video.renditions.map((r) => `${r.height}p`).join(', ')}</p>
        )}
        <Link to={`/watch/${video.id}`} className="button">
          ▶ Xem phim
        </Link>
      </div>
    </article>
  );
}
```

`web/app/src/pages/WatchPage.tsx`:

```tsx
import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { api, type Video } from '../api';
import { Player } from '../components/Player';

export function WatchPage() {
  const { id = '' } = useParams();
  const [video, setVideo] = useState<Video | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .video(id)
      .then((v) => !cancelled && setVideo(v))
      .catch(() => !cancelled && setVideo(null));
    return () => {
      cancelled = true;
    };
  }, [id]);

  return (
    <section className="watch">
      <p>
        <Link to={`/videos/${id}`}>← Quay lại</Link>
      </p>
      {video && <h1>{video.title}</h1>}
      <Player videoId={id} />
    </section>
  );
}
```

- [ ] **Step 5: App, entry, CSS**

`web/app/src/App.tsx`:

```tsx
import { Suspense, lazy } from 'react';
import { Link, Outlet, Route, Routes } from 'react-router-dom';
import { RequireAuth } from './auth';
import { Header } from './components/Header';
import { HomePage } from './pages/HomePage';
import { LoginPage } from './pages/LoginPage';
import { RegisterPage } from './pages/RegisterPage';
import { SearchPage } from './pages/SearchPage';
import { VideoPage } from './pages/VideoPage';

// Trang xem tải lazy: hls.js (~500 kB) chỉ được tải khi người dùng mở player.
const WatchPage = lazy(() => import('./pages/WatchPage').then((m) => ({ default: m.WatchPage })));

function Layout() {
  return (
    <>
      <Header />
      <main className="content">
        <Suspense fallback={<p className="status">Đang tải…</p>}>
          <Outlet />
        </Suspense>
      </main>
    </>
  );
}

function NotFound() {
  return (
    <main className="content">
      <h1>Không tìm thấy trang</h1>
      <Link to="/">Về trang chủ</Link>
    </main>
  );
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/register" element={<RegisterPage />} />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route index element={<HomePage />} />
        <Route path="search" element={<SearchPage />} />
        <Route path="videos/:id" element={<VideoPage />} />
        <Route path="watch/:id" element={<WatchPage />} />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
  );
}
```

`web/app/src/main.tsx`:

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { App } from './App';
import { AuthProvider } from './auth';
import './styles.css';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <AuthProvider>
        <App />
      </AuthProvider>
    </BrowserRouter>
  </StrictMode>,
);
```

`web/app/src/styles.css`:

```css
:root {
  --bg: #111418;
  --surface: #1b2027;
  --border: #2c333d;
  --text: #e8eaed;
  --muted: #9aa3ad;
  --accent: #e5484d;
  --radius: 8px;
  color-scheme: dark;
  font-family: system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
  line-height: 1.5;
}

* {
  box-sizing: border-box;
}

body {
  margin: 0;
  background: var(--bg);
  color: var(--text);
}

a {
  color: inherit;
}

h1 {
  font-size: 1.5rem;
  margin: 0 0 1rem;
}

input,
select,
button {
  font: inherit;
  color: inherit;
}

input,
select {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 0.5rem 0.75rem;
}

button,
.button {
  display: inline-block;
  background: var(--accent);
  border: 0;
  border-radius: var(--radius);
  padding: 0.55rem 1.1rem;
  color: #fff;
  text-decoration: none;
  cursor: pointer;
}

button:disabled {
  opacity: 0.6;
  cursor: default;
}

.link-button {
  background: none;
  padding: 0;
  color: var(--muted);
  text-decoration: underline;
}

.site-header {
  display: flex;
  align-items: center;
  gap: 1.5rem;
  padding: 0.75rem 1.5rem;
  border-bottom: 1px solid var(--border);
}

.brand {
  font-weight: 700;
  font-size: 1.2rem;
  color: var(--accent);
  text-decoration: none;
}

.site-header nav {
  display: flex;
  gap: 1rem;
}

.site-header nav a {
  color: var(--muted);
  text-decoration: none;
}

.site-header nav a.active {
  color: var(--text);
}

.user {
  margin-left: auto;
  display: flex;
  gap: 1rem;
  align-items: center;
  color: var(--muted);
}

.content {
  max-width: 1200px;
  margin: 0 auto;
  padding: 1.5rem;
}

.grid {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 1.25rem;
}

.card {
  display: block;
  text-decoration: none;
}

.card h3 {
  font-size: 1rem;
  margin: 0.5rem 0 0;
}

.poster {
  aspect-ratio: 16 / 9;
  border-radius: var(--radius);
  display: grid;
  place-items: center;
  font-size: 2.5rem;
  font-weight: 700;
  color: rgb(255 255 255 / 0.85);
}

.card:hover .poster {
  outline: 2px solid var(--accent);
}

.poster.large {
  width: min(480px, 100%);
}

.meta,
.status,
.empty {
  color: var(--muted);
  margin: 0.25rem 0;
}

.more {
  display: block;
  margin: 1.5rem auto 0;
}

.search-bar {
  display: flex;
  gap: 0.75rem;
  margin-bottom: 1.25rem;
}

.search-box {
  flex: 1;
}

.search-box input {
  width: 100%;
}

.detail {
  display: grid;
  grid-template-columns: minmax(0, 480px) 1fr;
  gap: 2rem;
}

@media (max-width: 720px) {
  .detail {
    grid-template-columns: 1fr;
  }
}

.tags {
  list-style: none;
  padding: 0;
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.tags a {
  display: inline-block;
  padding: 0.15rem 0.6rem;
  border: 1px solid var(--border);
  border-radius: 999px;
  text-decoration: none;
  font-size: 0.875rem;
}

.description {
  white-space: pre-line;
}

.player video {
  width: 100%;
  max-height: 75vh;
  background: #000;
  border-radius: var(--radius);
}

.quality {
  display: inline-flex;
  gap: 0.5rem;
  align-items: center;
  margin-top: 0.75rem;
  color: var(--muted);
}

.auth-page {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 1.5rem;
}

.auth-form {
  width: min(360px, 100%);
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 2rem;
}

.auth-form label {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.field-error,
.form-error {
  color: #ff8a8d;
  margin: 0;
  font-size: 0.9rem;
}
```

- [ ] **Step 6: Test và build**

Run: `cd web/app && npm test && npm run build`
Expected: `Test Files  7 passed (7)`, `Tests  25 passed (25)`; build in `dist/index.html`, `dist/assets/index-*.css`, `dist/assets/index-*.js` (~196 kB), `dist/assets/WatchPage-*.js` (~513 kB, chứa hls.js), `✓ built in …`, không cảnh báo chunk.

- [ ] **Step 7: Commit**

```bash
git add web/app/src
git commit -m "feat(web): viewer pages, auth context and routing"
```

---
### Task 9: Image nginx kèm SPA, compose và Makefile

**Files:**
- Create: `deploy/docker/nginx.Dockerfile`
- Modify: `deploy/docker-compose.yml`, `.dockerignore`, `Makefile`

**Interfaces:**
- Consumes: `web/app` (Task 5–8, `npm ci`, `npm run build`), template nginx (Task 4).
- Produces: image `streamfilm-nginx:dev` (SPA ở `/usr/share/nginx/html`, giữ `/usr/share/nginx/html/dev` mount từ `web/dev`). Target Makefile: `web-install`, `web-dev`, `web-test`, `web-build`; `logs-app` thêm `nginx`. `make up` → http://localhost:8088 là web.

- [ ] **Step 1: Dockerfile** — `deploy/docker/nginx.Dockerfile`:

```dockerfile
# nginx = cổng vào của trình duyệt (ADR 0011): build SPA (web/app) bằng Node rồi chép vào image nginx.
# Template cấu hình (deploy/nginx/templates) vẫn mount từ compose để sửa không cần build lại.
FROM node:20.19.6-alpine3.22 AS web
WORKDIR /app
COPY web/app/package.json web/app/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/app/ ./
RUN npm run build

FROM nginx:1.27-alpine
COPY --from=web /app/dist /usr/share/nginx/html
```

Thêm vào cuối `.dockerignore` (context build là gốc repo; không gửi `node_modules`/`dist` của máy host):

```
web/app/node_modules
web/app/dist
```

- [ ] **Step 2: Compose build image nginx**

Trong `deploy/docker-compose.yml` thay

```yaml
  nginx:
    image: nginx:1.27-alpine
    ports: ["127.0.0.1:8088:80"]
```

bằng

```yaml
  # Cổng vào duy nhất cho trình duyệt (ADR 0011): SPA ở /, proxy /api/ → api, HLS ký ở /cdn/.
  nginx:
    build:
      context: ..
      dockerfile: deploy/docker/nginx.Dockerfile
    image: streamfilm-nginx:dev
    ports: ["127.0.0.1:8088:80"]
```

(phần `environment`, `volumes`, `depends_on`, `healthcheck` của nginx giữ nguyên; không thêm `depends_on: api` vì `/api/` phân giải tên lúc có request).

- [ ] **Step 3: Makefile**

Thay dòng `.PHONY` đầu file bằng

```make
.PHONY: env up down logs logs-app ps test test-short build sample transcode run-api run-worker e2e-m2 e2e-m3 e2e-m4 seed-admin sign-url \
	web-install web-dev web-test web-build
```

Thay

```make
logs-app:
	$(COMPOSE) logs -f --tail=100 api transcoder
```

bằng

```make
logs-app:
	$(COMPOSE) logs -f --tail=100 api transcoder nginx
```

Thêm vào cuối `Makefile`:

```make
# Web người xem (web/app, cần Node 20.19+). `make up` tự build SPA trong image nginx, không cần Node trên máy.
WEB := web/app

web-install:
	cd $(WEB) && npm ci

# Vite dev server http://localhost:5173 (hot reload); /api và /cdn proxy tới nginx :8088 nên cần `make up`.
web-dev:
	cd $(WEB) && npm run dev

web-test:
	cd $(WEB) && npm test

web-build:
	cd $(WEB) && npm run build
```

- [ ] **Step 4: Build và chạy**

Run: `docker compose -f deploy/docker-compose.yml --env-file .env build nginx && make up && make ps | grep nginx`
Expected: build xong (stage `web` chạy `npm ci` + `vite build`); `make up` kết thúc khi mọi service healthy; `nginx` là image `streamfilm-nginx:dev`, trạng thái `healthy`.

Run: `curl -s http://localhost:8088/ | grep -c '<div id="root"></div>'; curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8088/watch/abc; curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8088/api/me; curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8088/hls/x/master.m3u8`
Expected: `1`, `200`, `401`, `404`.

Run: `make web-test`
Expected: `Tests  25 passed (25)`.

- [ ] **Step 5: Commit**

```bash
git add deploy/docker/nginx.Dockerfile deploy/docker-compose.yml .dockerignore Makefile
git commit -m "build(nginx): bake the SPA into the nginx image; web make targets"
```

---

### Task 10: E2E M4

**Files:**
- Create: `scripts/e2e-m4.sh`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `scripts/e2e-m2.sh` với `API`, `E2E_TITLE`, `E2E_GENRE`, `E2E_ID_FILE` (Task 1); stack từ Task 9; `ADMIN_EMAIL`/`ADMIN_PASSWORD` trong `.env`.
- Produces: `make e2e-m4` in `OK  M4  mở http://localhost:8088/watch/{id}`. Biến `SITE` (mặc định `http://localhost:8088`).

- [ ] **Step 1: Script** — `scripts/e2e-m4.sh` (nhớ `chmod +x scripts/e2e-m4.sh`):

```bash
#!/usr/bin/env bash
# E2E M4: nginx là cổng vào duy nhất — phục vụ SPA ở / (fallback index.html cho route của react-router),
# proxy /api/ tới api cùng origin (cookie sf_session), HLS ký ở /cdn/. Tìm kiếm không dấu, có dấu,
# gõ sai và lọc thể loại trả đúng video vừa transcode.
# Dùng: scripts/e2e-m4.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m4 nạp từ .env); SITE (mặc định http://localhost:8088).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/viewer.jar"
EMAIL="viewer-m4-$(date +%s)-$RANDOM@example.com"
PASSWORD="viewer-password-1"
NOID="00000000-0000-0000-0000-000000000000"
RUN="m4run$(date +%s)" # từ duy nhất của lần chạy này, để kết quả tìm kiếm không lẫn video của lần trước
TITLE="Trò chơi điện tử $RUN"
GENRE="Hành động"

# expect CODE curl-args...: gọi curl và so mã HTTP. Khi lỗi chỉ in URL (body có thể chứa mật khẩu).
expect() {
  local want=$1; shift
  local code a url=""
  for a in "$@"; do case $a in http://* | https://*) url=$a ;; esac; done
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "$url: HTTP $code, want $want"
}
# search Q [GENRE]: GET /api/videos qua nginx bằng cookie viewer.
search() {
  local args=(--data-urlencode "q=$1")
  [ -z "${2:-}" ] || args+=(--data-urlencode "genre=$2")
  curl -fsS --max-time 30 -b "$JAR" -G "$SITE/api/videos" "${args[@]}"
}

echo "1/5 SPA: / và các route phía client trả index.html; asset có cache dài"
for path in / /login /search?q=tro+choi "/videos/$NOID" "/watch/$NOID"; do
  curl -fsS --max-time 30 "$SITE$path" | grep -q '<div id="root"></div>' || die "GET $path không trả index.html của SPA"
done
ASSET=$(curl -fsS --max-time 30 "$SITE/" | grep -oE '/assets/index-[^"]+\.js' | head -1)
[ -n "$ASSET" ] || die "index.html không trỏ tới /assets/index-*.js"
curl -fsS --max-time 30 -D "$WORK/headers" -o /dev/null "$SITE$ASSET" || die "GET $ASSET"
grep -qi '^cache-control:.*immutable' "$WORK/headers" || die "$ASSET thiếu Cache-Control immutable"
expect 404 "$SITE/assets/khong-co.js"
expect 404 "$SITE/hls/$NOID/master.m3u8"

echo "2/5 /api/ cùng origin: chưa đăng nhập → 401; đăng ký đặt cookie sf_session"
expect 401 "$SITE/api/me"
expect 401 "$SITE/api/videos"
jq -n --arg e "$EMAIL" --arg p "$PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$JAR" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- |
  jq -e '.role == "viewer"' >/dev/null || die "đăng ký qua $SITE/api"
grep -q 'sf_session' "$JAR" || die "cookie sf_session không được đặt qua nginx"
curl -fsS --max-time 30 -b "$JAR" "$SITE/api/me" | jq -e --arg e "$EMAIL" '.email == $e' >/dev/null || die "/api/me"

echo "3/5 admin upload video \"$TITLE\" (scripts/e2e-m2.sh qua $SITE)"
API="$SITE" E2E_TITLE="$TITLE" E2E_GENRE="$GENRE" E2E_ID_FILE="$WORK/id" \
  "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")

echo "4/5 tìm kiếm: không dấu, có dấu, gõ sai, lọc thể loại"
search "tro choi dien tu $RUN" | jq -e --arg id "$ID" '[.videos[].id] == [$id]' >/dev/null || die "không dấu"
search "Trò Chơi Điện Tử $RUN" | jq -e --arg id "$ID" '[.videos[].id] == [$id]' >/dev/null || die "có dấu"
search "tro choj dien tu $RUN" | jq -e --arg id "$ID" '.videos[0].id == $id' >/dev/null || die "gõ sai (trigram)"
search "$RUN" "$GENRE" | jq -e --arg id "$ID" '[.videos[].id] == [$id]' >/dev/null || die "lọc thể loại"
search "$RUN" "Gia đình" | jq -e '.videos == []' >/dev/null || die "lọc thể loại khác phải rỗng"
curl -fsS --max-time 30 -b "$JAR" "$SITE/api/genres" | jq -e --arg g "$GENRE" '.genres | index($g) != null' >/dev/null ||
  die "/api/genres thiếu $GENRE"

echo "5/5 /play: URL ký cùng origin với SPA, master tải được; logout"
URL=$(curl -fsS --max-time 30 -b "$JAR" "$SITE/api/videos/$ID/play" | jq -r .url)
case "$URL" in "$SITE/cdn/s/"*) ;; *) die "URL phát $URL không cùng origin $SITE (kiểm tra CDN_BASE_URL)" ;; esac
curl -fsS --max-time 30 "$URL" | grep -q '#EXTM3U' || die "master.m3u8 qua URL ký"
expect 204 -b "$JAR" -c "$JAR" -X POST "$SITE/api/auth/logout"
expect 401 -b "$JAR" "$SITE/api/me"

echo "OK  M4  mở $SITE/watch/$ID"
```

- [ ] **Step 2: Target Makefile** — thêm ngay sau target `e2e-m3`:

```make
# E2E M4: SPA + /api/ cùng origin qua nginx, tìm kiếm không dấu (chạy kèm luồng M2 qua nginx).
e2e-m4: seed-admin
	@test -f tmp/sample.mp4 || $(MAKE) sample
	set -a; . ./.env; set +a; ./scripts/e2e-m4.sh tmp/sample.mp4
```

- [ ] **Step 3: Chạy**

Run: `chmod +x scripts/e2e-m4.sh && make up && make e2e-m4`
Expected (rút gọn):

```
1/5 SPA: / và các route phía client trả index.html; asset có cache dài
2/5 /api/ cùng origin: chưa đăng nhập → 401; đăng ký đặt cookie sf_session
3/5 admin upload video "Trò chơi điện tử m4run…" (scripts/e2e-m2.sh qua http://localhost:8088)
    …
    {"duration_s":30,"renditions":[1080,720,480,360]}
    …
4/5 tìm kiếm: không dấu, có dấu, gõ sai, lọc thể loại
5/5 /play: URL ký cùng origin với SPA, master tải được; logout
OK  M4  mở http://localhost:8088/watch/<id>
```

Run: `make e2e-m3`
Expected: `OK  M3` (không hồi quy).

- [ ] **Step 4: Kiểm tra tay trên trình duyệt (spec §9: player kiểm tra tay trên Chrome và Safari)**

1. Mở http://localhost:8088 → chuyển tới `/login`. Đăng nhập `admin@streamfilm.local` / `admin-dev-password`.
2. Trang chủ "Phim mới" có thẻ "Trò chơi điện tử m4run…".
3. "Tìm kiếm": gõ `tro choi` → sau ~0,3 giây URL thành `/search?q=tro+choi`, có kết quả; chọn thể loại "Hành động" → vẫn có; gõ `tro chpi` → vẫn có (trigram).
4. Bấm thẻ → trang chi tiết (năm, thời lượng, thể loại, chất lượng) → "▶ Xem phim" → video phát; dropdown "Chất lượng" có `Tự động/1080p/720p/480p/360p`, đổi được.
5. Phục hồi 403: đang phát ở giây ~10, chạy `sed -i.bak 's/^CDN_SIGNING_SECRET=.*/CDN_SIGNING_SECRET=dev-cdn-secret-rotated-01/' .env && make up` (api và nginx nhận secret mới, URL cũ thành 403) → player gọi lại `/play` (tab Network) và phát tiếp gần vị trí cũ. Khôi phục: `mv .env.bak .env && make up`.
6. Lặp bước 4 trên Safari (HLS gốc).
7. "Đăng xuất" → về `/login`; mở `/watch/<id>` → chuyển về `/login`, đăng nhập lại → quay đúng `/watch/<id>`.

- [ ] **Step 5: Commit**

```bash
git add scripts/e2e-m4.sh Makefile
git commit -m "test(e2e): M4 same-origin SPA, API proxy and accent-insensitive search"
```

---

### Task 11: Tài liệu M4 và ADR

**Files:**
- Modify: `README.md`, `docs/architecture.md`, `docs/api.md`, `docs/operations.md`, `docs/data-model.md`, `docs/flows.md`
- Create: `docs/decisions/0011-same-origin-nginx-gateway.md`, `docs/decisions/0012-search-unaccent-trigram.md`

**Interfaces:**
- Consumes: hành vi đã chạy ở Task 1–10.
- Produces: tài liệu khớp code (spec §11).

- [ ] **Step 1: `README.md`**

Thay

````markdown
Trạng thái: **M3** — upload và transcode qua Kafka (M2); đăng ký/đăng nhập bằng cookie JWT, phân quyền `admin`/`viewer`; `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn. Chưa có web (M4). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).
````

bằng

````markdown
Trạng thái: **M4** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).
````

Thay

````markdown
- `curl`, `jq`, `openssl` — cho script e2e
````

bằng

````markdown
- `curl`, `jq`, `openssl` — cho script e2e
- Node 20.19+ — chỉ khi phát triển web (`make web-dev`, `make web-test`); `make up` build web trong Docker
````

Thay

````markdown
```bash
make up          # dựng hạ tầng + build và chạy api (cổng 8000) và transcoder
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make e2e-m3      # auth + upload → transcode → /play → kiểm tra URL ký qua nginx (gồm cả e2e-m2)
```
````

bằng

````markdown
```bash
make up          # dựng hạ tầng + build và chạy api, transcoder, nginx (kèm web)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make e2e-m4      # upload video tiêu đề tiếng Việt → tìm "tro choi dien tu" → /play qua nginx
```

Mở http://localhost:8088, đăng nhập bằng `ADMIN_EMAIL` / `ADMIN_PASSWORD` trong `.env` (mặc định `admin@streamfilm.local` / `admin-dev-password`) hoặc bấm "Đăng ký" để tạo tài khoản người xem. Catalog trống cho tới khi có video `ready`: chạy `make e2e-m4` (hoặc `make e2e-m2`) để upload `tmp/sample.mp4`.
````

Thay

````markdown
Script in ra link dev player để xem video vừa xử lý. Theo dõi message tại Redpanda Console http://localhost:8080.
````

bằng

````markdown
`make e2e-m3` kiểm tra phân quyền và URL ký. Theo dõi message tại Redpanda Console http://localhost:8080.
````

Thay

````markdown
make test         # gồm integration test (cần Docker và FFmpeg)
```
````

bằng

````markdown
make test         # gồm integration test (cần Docker và FFmpeg)
make web-install && make web-test   # Vitest cho web/app
```
````

- [ ] **Step 2: `docs/architecture.md`**

Thay

````markdown
  Viewer[Web + hls.js] -->|login, /api/videos/:id/play| API
  Viewer -->|/cdn/s/sig/exp/hls/id/… segments| N[nginx CDN]
  N -->|secure_link_md5 hợp lệ| S3
````

bằng

````markdown
  Viewer[Trình duyệt: SPA React + hls.js] -->|"/ , /api/… , /cdn/…"| N[nginx :8088]
  N -->|"/api/ (proxy)"| API
  N -->|"/cdn/s/… secure_link_md5 hợp lệ"| S3
````

Thay

````markdown
Phần đã chạy tới M3: `api` (đăng ký/đăng nhập, catalog và `/play` cho người xem, upload và danh sách video cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker`, Postgres, Kafka, MinIO, nginx kiểm tra signed URL. Chưa có: web (M4), xem tiếp và phụ đề (M5), analytics (M6).
````

bằng

````markdown
Phần đã chạy tới M4: `api` (đăng ký/đăng nhập, catalog, tìm kiếm không dấu và `/play` cho người xem, upload và danh sách video cho admin, consumer trạng thái, job dọn upload bỏ dở), `transcoder worker`, Postgres, Kafka, MinIO, nginx (cổng vào duy nhất: SPA, proxy API, CDN ký URL), web người xem `web/app`. Chưa có: xem tiếp, poster/sprite và phụ đề (M5), analytics (M6), trang admin (M7).
````

Thay

````markdown
| Module | Trách nhiệm (tới M3) |
````

bằng

````markdown
| Module | Trách nhiệm (tới M4) |
````

Thay

````markdown
`GET /api/videos[/{id}]` và `/play` cho người xem (chỉ video `ready`),
````

bằng

````markdown
`GET /api/videos[/{id}]` (tìm kiếm `q`, lọc `genre`/`year`), `GET /api/genres` và `/play` cho người xem (chỉ video `ready`),
````

Thay

````markdown
user (`/api/me`, `/api/videos/*`; `RequireUser`
````

bằng

````markdown
user (`/api/me`, `/api/videos/*`, `/api/genres`; `RequireUser`
````

Thay

````markdown
## CDN và signed URL

`/api/videos/{id}/play` trả `CDN_BASE_URL + /cdn/s/{sig}/{exp}/hls/{id}/master.m3u8` (URL tuyệt đối, vì web M4 chạy khác origin với nginx),
````

bằng

````markdown
## Web người xem và nginx

`web/app` là SPA React 18 + Vite + TypeScript, router `react-router-dom`, player `hls.js` (Safari dùng HLS gốc). Trang: `/login`, `/register`, `/` (phim mới), `/search?q=&genre=` (debounce 300 ms, trạng thái nằm trên URL), `/videos/{id}` (chi tiết), `/watch/{id}` (player, tải lazy cùng hls.js). Poster là khối màu tạm tới M5.

nginx là cổng vào duy nhất ở http://localhost:8088 ([ADR 0011](decisions/0011-same-origin-nginx-gateway.md)): `/` phục vụ bản build của SPA (route không phải file → `index.html`), `/api/` proxy nguyên URI tới `api:8000`, `/cdn/` là CDN ký URL. Cùng origin nên cookie `sf_session` (HttpOnly) tự đi kèm mọi `fetch`, không cần CORS. Image `streamfilm-nginx:dev` build SPA trong Docker (`deploy/docker/nginx.Dockerfile`).

Player: gọi `/play`, nạp URL ký vào hls.js. Lỗi `403` (URL hết hạn hoặc sai) hay lỗi mạng fatal → `PlaybackSession.refresh()` gọi lại `/play` và phát tiếp từ `currentTime`; nhiều lỗi cùng lúc chỉ gọi `/play` một lần, quá 3 lần trong 60 giây thì báo lỗi (`web/app/src/player/recovery.ts`). Lỗi media fatal → `hls.recoverMediaError()`.

## Tìm kiếm

`GET /api/videos?q=` tìm trên cột `videos.search` (tsvector của tiêu đề, trọng số A, và mô tả, trọng số B, đã bỏ dấu bằng `immutable_unaccent`, trigger cập nhật). Có kết quả full-text thì xếp theo `ts_rank`; không có thì dự phòng trigram `<%` trên tiêu đề đã bỏ dấu để chịu lỗi gõ. Lọc `genre`, `year`; chỉ video `ready`. Chi tiết và lý do: [ADR 0012](decisions/0012-search-unaccent-trigram.md).

## CDN và signed URL

`/api/videos/{id}/play` trả `CDN_BASE_URL + /cdn/s/{sig}/{exp}/hls/{id}/master.m3u8` (URL tuyệt đối; local `CDN_BASE_URL` trùng origin của web nên player tải cùng origin),
````

Thay

````markdown
| User/Auth service | module `auth` trong `api` (bcrypt, JWT trong cookie HttpOnly) |
````

bằng

````markdown
| User/Auth service | module `auth` trong `api` (bcrypt, JWT trong cookie HttpOnly) |
| Search | Postgres full-text + `unaccent` + `pg_trgm` trong module `catalog` |
| Client apps (web) | SPA React + hls.js phục vụ bởi nginx cùng origin với API |
````

- [ ] **Step 3: `docs/api.md`**

Thay

````markdown
Base URL local: `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.
````

bằng

````markdown
Base URL local: `http://localhost:8088` (qua nginx, cùng origin với web — [ADR 0011](decisions/0011-same-origin-nginx-gateway.md)) hoặc trực tiếp `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.
````

Thay

````markdown
| GET | `/api/videos?page=` | user | Catalog video `ready`, 20 video/trang |
````

bằng

````markdown
| GET | `/api/videos?q=&genre=&year=&page=` | user | Catalog và tìm kiếm video `ready`, 20 video/trang |
| GET | `/api/genres` | user | Các thể loại của video `ready` |
````

Thay

````markdown
`?page=2` (1–1000, mặc định 1) → `200`:
````

bằng

````markdown
Tham số (đều tuỳ chọn):

| Tham số | Ý nghĩa |
|---|---|
| `q` | Từ khoá, tối đa 100 ký tự, có dấu hoặc không dấu, cú pháp websearch (`"cụm từ"`, `-loại`, `or`). Rỗng = duyệt catalog |
| `genre` | Lọc đúng một thể loại (giá trị lấy từ `/api/genres`), tối đa 50 ký tự |
| `year` | Lọc theo năm, 1888–2100 |
| `page` | 1–1000, mặc định 1 |

Sai giới hạn → `400`. Ví dụ `?q=tro+choi&genre=H%C3%A0nh+%C4%91%E1%BB%99ng&page=2` → `200`:
````

Thay

````markdown
Chỉ video `ready`, mới nhất trước. Tìm kiếm `q` và lọc `genre` có ở M4 (M3 bỏ qua).
````

bằng

````markdown
Chỉ video `ready`. Không có `q`: mới nhất trước. Có `q`: xếp theo độ liên quan (`ts_rank`, khớp tiêu đề trước mô tả); nếu full-text không ra kết quả nào thì dùng trigram trên tiêu đề để chịu lỗi gõ ("tro chpi" vẫn ra "Trò chơi …") — [ADR 0012](decisions/0012-search-unaccent-trigram.md).

## GET /api/genres

`200`: `{"genres":["Gia đình","Hành động"]}` — các thể loại (không trùng, đã sắp xếp) của video `ready`; `[]` khi chưa có.
````

Thay

````markdown
nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play`.
````

bằng

````markdown
nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play` (player của web tự làm và phát tiếp từ vị trí hiện tại).
````

- [ ] **Step 4: `docs/operations.md`**

Thay

````markdown
| `make up` / `make down` | Dựng (build lại image api/transcoder) / tắt toàn bộ stack |
````

bằng

````markdown
| `make up` / `make down` | Dựng (build lại image api/transcoder/nginx kèm web) / tắt toàn bộ stack |
````

Thay

````markdown
| `make logs-app` | Log của `api` và `transcoder` |
````

bằng

````markdown
| `make logs-app` | Log của `api`, `transcoder` và `nginx` |
````

Thay

````markdown
| `make sign-url ID=…` |
````

bằng

````markdown
| `make e2e-m4` | E2E: SPA và fallback route qua nginx, `/api/` cùng origin (cookie), upload video tiêu đề tiếng Việt, tìm không dấu / có dấu / gõ sai / lọc thể loại, `/play` cùng origin |
| `make web-install` / `make web-test` / `make web-build` | `npm ci` / Vitest / build production cho `web/app` (cần Node 20.19+) |
| `make web-dev` | Vite dev server http://localhost:5173 (hot reload), proxy `/api` và `/cdn` sang nginx `:8088` (cần `make up`) |
| `make sign-url ID=…` |
````

Thay

````markdown
| api | http://localhost:8000 (`/healthz`) | cookie sau `POST /api/auth/login`; admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD` |
| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |
````

bằng

````markdown
| **Web người xem** | http://localhost:8088 | admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD`, hoặc tự đăng ký viewer |
| api qua nginx | http://localhost:8088/api/… | cookie sau `POST /api/auth/login` |
| api trực tiếp | http://localhost:8000 (`/healthz`) | như trên (dùng cho script, debug) |
| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |
| Vite dev server | http://localhost:5173 (`make web-dev`) | như web người xem |
````

Thay

````markdown
## Theo dõi Kafka
````

bằng

````markdown
## Phát triển web (M4)

Mã nguồn ở `web/app` (React 18 + Vite + TypeScript, hls.js, react-router). Lần đầu: `make web-install`.

- **Sửa giao diện:** `make up` (backend) rồi `make web-dev`, mở http://localhost:5173. Vite proxy `/api` và `/cdn` sang nginx `:8088` nên cookie và URL ký hoạt động như bản thật.
- **Test:** `make web-test` (Vitest + Testing Library: debounce ô tìm kiếm, lưới phim, form đăng nhập, logic gọi lại `/play` của player). Player phát thật kiểm tra tay trên Chrome và Safari: mở `/watch/{id}`, đổi chất lượng, tua.
- **Bản chạy ở :8088** là bản build trong image `streamfilm-nginx:dev`; sau khi sửa `web/app` chạy lại `make up` để build lại.
- Thêm/sửa dependency: `cd web/app && npm install --save-exact <pkg>@<version>` và commit `package-lock.json` (Docker build dùng `npm ci`).

Kiểm tra player tự lấy lại URL khi hết hạn (bằng tay): đang xem thì đổi `CDN_SIGNING_SECRET` trong `.env` và `make up` (URL cũ thành `403`) — player gọi lại `/play` và phát tiếp từ vị trí cũ.

## Theo dõi Kafka
````

Thay

````markdown
| `http://localhost:8088/hls/...` trả `404` | Đúng thiết kế từ M3: chỉ còn đường `/cdn/s/...` có chữ ký |
````

bằng

````markdown
| `http://localhost:8088/hls/...` trả `404` | Đúng thiết kế từ M3: chỉ còn đường `/cdn/s/...` có chữ ký |
| `http://localhost:8088/api/...` trả `502` | api chưa chạy hoặc đang khởi động lại: `make ps`, `make logs-app` |
| Web hiện bản cũ sau khi sửa `web/app` | Bản ở `:8088` là image build sẵn: chạy lại `make up`, hoặc dùng `make web-dev` |
| Web báo "Không tải được video" | `/play` lỗi (video chưa `ready`, hết phiên) hoặc URL ký liên tục `403` (`CDN_SIGNING_SECRET` của api và nginx khác nhau): xem tab Network và `make logs-app` |
| Tìm "tro choi" không ra video vừa upload | Video chưa `ready`, hoặc api chưa chạy migration `0003_search` (xem `schema_migrations`) |
````

- [ ] **Step 5: `docs/data-model.md`**

Thay

````markdown
## Postgres (tới M3)

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp). Cột `search` (M4), `subtitles` và `watch_progress` (M5) được thêm bằng migration của giai đoạn đó.
````

bằng

````markdown
## Postgres (tới M4)

Migration nằm trong `migrations/*.sql`, nhúng vào binary `api` và tự chạy khi khởi động (bảng `schema_migrations` ghi version đã áp). `subtitles` và `watch_progress` (M5) được thêm bằng migration của giai đoạn đó.
````

Thay

````markdown
  hls_prefix text, poster_key text, sprite_key text,
  created_at timestamptz, updated_at timestamptz
)
````

bằng

````markdown
  hls_prefix text, poster_key text, sprite_key text,
  search tsvector,         -- M4, migrations/0003_search.sql: unaccent(title) trọng số A + unaccent(description) B, trigger cập nhật
  created_at timestamptz, updated_at timestamptz
)
````

Thay

````markdown
Index: `videos(created_at desc)`, `videos(status, created_at)`.
````

bằng

````markdown
Index: `videos(created_at desc)`, `videos(status, created_at)`, GIN `videos(search)`, GIN `pg_trgm` trên `immutable_unaccent(title)`.

Hàm (M4): `immutable_unaccent(text)` — bản `IMMUTABLE` của `unaccent` (extension `unaccent`, `pg_trgm`); `videos_search_tsv(title, description)`; trigger `videos_search_update` (`BEFORE INSERT OR UPDATE OF title, description`). Truy vấn tìm kiếm: [ADR 0012](decisions/0012-search-unaccent-trigram.md).
````

- [ ] **Step 6: `docs/flows.md`**

Thay

````markdown
  Note over P,API: Player gặp 403 thì gọi lại /play và phát tiếp (web M4, chưa có)
```
````

bằng

````markdown
  Note over P,N: Sau 2 giờ (hoặc chữ ký sai) nginx trả 403
  P->>API: GET /api/videos/{id}/play (PlaybackSession.refresh, một lần dù nhiều lỗi cùng lúc)
  API-->>P: URL ký mới
  P->>N: nạp lại master.m3u8, phát tiếp từ currentTime
```

Web gọi `/api/...` và tải `/cdn/...` qua cùng một origin (nginx `:8088`), nên cookie `sf_session` đi kèm request API mà không cần CORS ([ADR 0011](decisions/0011-same-origin-nginx-gateway.md)). Quá 3 lần lấy lại URL trong 60 giây thì player dừng và báo lỗi.
````


- [ ] **Step 7: ADR mới**

`docs/decisions/0011-same-origin-nginx-gateway.md`:

```markdown
# ADR 0011: nginx là cổng vào duy nhất — SPA, API và CDN cùng origin

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
M4 thêm web người xem (React + Vite). Phiên đăng nhập là cookie `sf_session` `HttpOnly`, `SameSite=Lax` (ADR 0010). Nếu SPA chạy ở một origin (ví dụ `:5173`) và API ở origin khác (`:8000`) thì mọi `fetch` phải bật CORS có `credentials`, server phải trả `Access-Control-Allow-Credentials` và liệt kê origin cụ thể, và cookie phụ thuộc quy tắc same-site của trình duyệt. Player cũng cần URL `/cdn/...` của nginx.

## Quyết định
- nginx (cổng `8088`) phục vụ cả ba: SPA build sẵn ở `/` (fallback `index.html` cho route của react-router), `/api/` proxy nguyên URI tới `api:8000`, `/cdn/` là CDN ký URL (ADR 0009). Trình duyệt chỉ nói chuyện với một origin nên không cần CORS cho API và cookie luôn được gửi kèm.
- `proxy_pass` tới api dùng biến + `resolver 127.0.0.11` (DNS của Docker): nginx khởi động được khi api chưa chạy (test nginx trong container không có api) và theo kịp IP mới khi api được tạo lại.
- SPA được build trong Docker multi-stage (`deploy/docker/nginx.Dockerfile`: `node:20.19.6-alpine3.22` → `nginx:1.27-alpine`), image `streamfilm-nginx:dev`. `make up` build lại như api/transcoder (ADR 0008), máy host không cần Node. Template nginx vẫn mount từ repo.
- Dev frontend: `make web-dev` chạy Vite ở `:5173`, proxy `/api` và `/cdn` sang nginx `:8088`, nên đường đi của request giống hệt bản build.
- `CDN_BASE_URL` (mặc định `http://localhost:8088`) trùng origin của site; `/play` vẫn trả URL tuyệt đối để player chạy được cả khi CDN tách sang domain riêng.
- Asset của Vite có hash trong tên: `/assets/` gửi `Cache-Control: immutable` 1 năm; `index.html` gửi `no-cache` để bản build mới có hiệu lực ngay.

## Phương án đã loại
- **Build ở host (`make web-build`) rồi mount `web/app/dist` vào nginx**: nhanh khi sửa giao diện, nhưng `make up` phụ thuộc Node trên máy và dễ phục vụ bản `dist` cũ. Vẫn có `make web-build` để kiểm tra build cục bộ.
- **CORS có credentials giữa `:5173`/`:8000`**: thêm cấu hình và lỗi khó hiểu, không có lợi gì cho MVP.

## Hệ quả
- Một cổng cho người dùng: http://localhost:8088. Cổng `8000` vẫn mở cho script và debug.
- Sửa giao diện cần build lại image nginx (`make up`) hoặc dùng `make web-dev` khi phát triển.
- Lên production: nginx này tương ứng một reverse proxy/edge (ví dụ Cloudflare) đứng trước SPA tĩnh + API; CDN phát video có thể tách domain mà không đổi code web (URL tuyệt đối từ `/play`).
```

`docs/decisions/0012-search-unaccent-trigram.md`:

```markdown
# ADR 0012: Tìm kiếm tiếng Việt bằng Postgres FTS + unaccent, dự phòng trigram

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §5: người xem gõ có dấu hoặc không dấu ("tro choi" phải ra "Trò chơi"), chịu được lỗi gõ, lọc theo thể loại và năm, chỉ thấy video `ready`. Dữ liệu nhỏ (catalog vài nghìn phim), không muốn thêm Elasticsearch/Meilisearch.

## Quyết định
- Migration `0003_search.sql`: extension `unaccent` và `pg_trgm`; hàm `immutable_unaccent(text)` gọi `public.unaccent('public.unaccent'::regdictionary, $1)` và khai báo `IMMUTABLE` (bản gốc `unaccent()` là `STABLE` nên không dùng được trong index). `unaccent` đổi cả `đ/Đ` thành `d/D`.
- Cột `videos.search tsvector` = `setweight(to_tsvector('simple', immutable_unaccent(title)), 'A') || setweight(…description…, 'B')`, cập nhật bằng trigger `BEFORE INSERT OR UPDATE OF title, description`; migration backfill video cũ. Cấu hình `simple` (không stemming, không stopword) vì tiếng Việt là ngôn ngữ đơn âm tiết.
- Index: GIN trên `search`; GIN `gin_trgm_ops` trên `immutable_unaccent(title)`.
- Truy vấn (`catalog.Repo.Search`): `websearch_to_tsquery('simple', immutable_unaccent(q))`, xếp hạng `ts_rank` (khớp tiêu đề cao hơn mô tả). **Chỉ khi full-text không có kết quả nào** (sau khi lọc `genre`, `year`, `status = 'ready'`) mới dùng trigram: `immutable_unaccent(q) <% immutable_unaccent(title)`, xếp theo `word_similarity`. Cả hai nhánh nằm trong một câu SQL (CTE) nên phân trang nhất quán.
- Dùng `word_similarity` / toán tử `<%` (ngưỡng mặc định 0.6) thay cho `similarity()` trong spec: từ khoá thường ngắn hơn tiêu đề nhiều, `similarity()` so cả chuỗi nên điểm thấp; `<%` vẫn dùng được index GIN trigram.
- `genre` khớp đúng một phần tử của `genres` (web lấy danh sách từ `GET /api/genres`); `year` khớp đúng năm.

## Hệ quả
- Gõ không dấu, có dấu, viết hoa đều như nhau; cú pháp websearch (`"cụm từ"`, `-loại trừ`, `or`) dùng được.
- Không có tìm theo tiền tố trong nhánh full-text: gõ dở "tro ch" rơi vào nhánh trigram (vẫn ra kết quả gần đúng). Bỏ dấu làm "ma" và "mà" trùng nhau — chấp nhận cho MVP.
- Khi catalog lớn hoặc cần gợi ý/tìm theo tiền tố, cân nhắc search engine riêng (Meilisearch/OpenSearch) được đồng bộ qua Kafka.
```

- [ ] **Step 8: Kiểm tra lần cuối và commit**

Run: `go vet ./... && make test && make web-test && make e2e-m4`
Expected: `go vet` không cảnh báo; mọi package Go `ok` (gồm `TestRepoSearch`, `TestSearchMigrationBackfill`, `TestNginxSecureLinkParity`); `Tests  25 passed (25)`; `OK  M4`.

```bash
git add README.md docs/architecture.md docs/api.md docs/operations.md docs/data-model.md docs/flows.md docs/decisions
git commit -m "docs: M4 web viewer, same-origin gateway and search docs and ADRs"
```

---

## Self-review

**Độ phủ spec M4:**

| Yêu cầu | Task |
|---|---|
| Web React + Vite + TypeScript, hls.js (§2) | 5–8 |
| Trang chủ: catalog video `ready` (§10 M4) | 8 (`HomePage`), 3 (`GET /api/videos`) |
| Trang chi tiết | 8 (`VideoPage`) |
| Player: `/play` → hls.js; 403 → gọi lại `/play`, phát tiếp từ vị trí hiện tại (§4.3 bước 6, §7) | 7 (`PlaybackSession`, test), 10 Step 4 (tay) |
| Tìm kiếm không dấu: cột `search` tsvector `unaccent(title)+unaccent(description)` bằng trigger, GIN, GIN `pg_trgm` trên `unaccent(title)` (§5) | 2 |
| `websearch_to_tsquery('simple', unaccent(q))` + `ts_rank`, dự phòng trigram khi không có kết quả, lọc `genres` và `year`, chỉ `ready` (§5) | 2 (test với Postgres thật), 3 |
| `GET /api/videos?q=&genre=&page=` (§6) | 3, 10 |
| Đăng nhập / đăng ký trên web, cookie HttpOnly (§8) | 6 (`AuthForm`), 8 (`auth.tsx`), 4/9 (cùng origin), 10 |
| Vitest cho component chính; player kiểm tra tay (§9) | 5–8; 10 Step 4 |
| Tài liệu architecture/api/operations/data-model/flows, README, ADR (§11) | 11 |
| Carry-over M3: `expect()` không in body; bỏ `split`; `CDN_BASE_URL` từ env | 1 |

Ngoài phạm vi M4 (đúng spec): poster/sprite thật, phụ đề, `resume_at` thật, hàng "Xem tiếp", gửi `playback.events` (M5–M6); trang admin (M7). `/play` vẫn trả `subtitles: []`, `resume_at: 0`; player đã dùng `resume_at` nên M5 chỉ cần đổi server.

**Quét placeholder:** không có "TBD", "TODO", "tương tự Task N"; mọi bước có code hoặc lệnh đầy đủ kèm kết quả mong đợi. Toàn bộ code trong plan đã được áp lên một bản sao của nhánh `feat/mvp` và chạy: `go vet ./...`, `go test ./...` (gồm `TestRepoSearch`, `TestSearchMigrationBackfill`, `TestNginxSecureLinkParity` với Postgres/nginx thật) đều `ok`; trong `web/app`: `npm install --before=2026-11-01` (0 lỗ hổng), `npx tsc --noEmit`, `npm test` (25 test), `npm run build`; `docker build -f deploy/docker/nginx.Dockerfile` thành công; một stack compose riêng (project khác, cổng khác) dựng từ bản sao chạy `make e2e-m4` → `OK  M4` và `make e2e-m3` → `OK  M3`; trên Chrome headless: đăng nhập, `/search?q=tro choi` ra video, trang xem phát được (`readyState=4`) với 4 mức chất lượng.

**Nhất quán chữ ký:**
- `catalog.SearchParams{Query, Genre, Year, Limit, Offset}` dùng ở `Repo.Search`, `ViewerHandler.list`, `fakeViewerStore.Search`, `emptyStore.Search`, `search_test.go`, `repo_test.go`.
- `*catalog.Repo` thoả `ViewerStore` (Search / Genres / Get / Renditions) và `AdminStore` (M2); `emptyStore` trong `router_test.go` thoả cả hai.
- Hàm SQL `immutable_unaccent` dùng giống hệt ở `videos_search_tsv`, index `videos_title_trgm_idx` (`immutable_unaccent(title)`) và `searchSQL` (`immutable_unaccent(b.title)`) — cùng biểu thức nên index trigram dùng được.
- TS: `PlayInfo` (`api.ts`) khớp `catalog.PlayJSON` (`url`, `expires_at`, `subtitles`, `resume_at`); `Video` khớp `ViewerVideoJSON` (`year` có `omitempty` → `year?`); `VideoPage` khớp `{"videos","page","has_more"}`; `api.genres()` đọc `{"genres"}`.
- `PlaybackSession` (`start`, `refresh`, `dispose`) và `classifyHlsError` dùng trong `Player.tsx` và `recovery.test.ts` với cùng chữ ký; `HlsErrorLike` tương thích `ErrorData` của hls.js 1.6.5 (đã type-check).
- Cổng/đường dẫn thống nhất: nginx `:8088` (`CDN_BASE_URL`, `SITE` trong e2e-m4, Vite proxy `gateway`), api `:8000` (`$api_upstream`), cookie `sf_session` (e2e-m4 kiểm tra trong cookie jar).

## Các kế hoạch tiếp theo

1. **M5** — `watch_progress` + `resume_at` thật, `POST /api/playback/events` từ player, hàng "Xem tiếp" trên trang chủ, poster (`poster_key`) thay khối màu, sprite + `thumbnails_url` khi tua, phụ đề (URL ký trong `subtitles`, `<track>`).
2. **M6** — analytics service, ClickHouse schema, `slog` handler gửi `app.logs`.
3. **M7** — Trang admin trong `web/app` (danh sách, upload, retry, xem lỗi), `scripts/e2e.sh` đầy đủ, hoàn thiện tài liệu.
