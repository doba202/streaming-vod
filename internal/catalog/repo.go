package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound          = errors.New("video not found")
	ErrInvalidTransition = errors.New("invalid status transition")
)

type Video struct {
	ID, Title, Description string
	Genres                 []string
	Year                   int // 0 = không rõ
	Status                 Status
	FailReason             string // lý do lỗi gần nhất; xoá khi video ready
	DurationS              int
	RawKey, UploadID       string
	SizeBytes              int64
	HLSPrefix              string
	PosterKey, SpriteKey   string // key trong bucket hls; rỗng với video transcode trước M5
	CreatedAt, UpdatedAt   time.Time
}

type Rendition struct {
	Height      int    `json:"height"`
	BitrateKbps int    `json:"bitrate_kbps"`
	PlaylistKey string `json:"playlist_key"`
}

type ReadyInfo struct {
	DurationS  int
	HLSPrefix  string
	Renditions []Rendition
	PosterKey  string // {id}/poster.jpg
	SpriteKey  string // {id}/thumbnails.vtt (chỉ mục các tấm sprite)
}

type ListFilter struct {
	Status Status // rỗng = mọi trạng thái
	Limit  int    // <= 0 hoặc > 200 thì dùng 50
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

const videoColumns = `id::text, title, description, genres, COALESCE(year, 0), status,
	COALESCE(fail_reason, ''), COALESCE(duration_s, 0), raw_key, COALESCE(upload_id, ''),
	size_bytes, COALESCE(hls_prefix, ''), COALESCE(poster_key, ''), COALESCE(sprite_key, ''),
	created_at, updated_at`

func scanVideo(row pgx.Row) (Video, error) {
	var v Video
	var status string
	err := row.Scan(&v.ID, &v.Title, &v.Description, &v.Genres, &v.Year, &status,
		&v.FailReason, &v.DurationS, &v.RawKey, &v.UploadID, &v.SizeBytes, &v.HLSPrefix,
		&v.PosterKey, &v.SpriteKey, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Video{}, ErrNotFound
	}
	v.Status = Status(status)
	return v, err
}

// Create thêm video mới ở trạng thái uploading.
func (r *Repo) Create(ctx context.Context, v Video) error {
	if v.Genres == nil {
		v.Genres = []string{}
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO videos
		(id, title, description, genres, year, status, raw_key, upload_id, size_bytes)
		VALUES ($1, $2, $3, $4, NULLIF($5, 0), 'uploading', $6, NULLIF($7, ''), $8)`,
		v.ID, v.Title, v.Description, v.Genres, v.Year, v.RawKey, v.UploadID, v.SizeBytes)
	if err != nil {
		return fmt.Errorf("insert video: %w", err)
	}
	return nil
}

func (r *Repo) Get(ctx context.Context, id string) (Video, error) {
	return scanVideo(r.pool.QueryRow(ctx, `SELECT `+videoColumns+` FROM videos WHERE id = $1`, id))
}

func (r *Repo) List(ctx context.Context, f ListFilter) ([]Video, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC LIMIT $2`, string(f.Status), f.Limit)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}

func collectVideos(rows pgx.Rows) ([]Video, error) {
	defer rows.Close()
	var out []Video
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *Repo) Renditions(ctx context.Context, id string) ([]Rendition, error) {
	rows, err := r.pool.Query(ctx, `SELECT height, bitrate_kbps, playlist_key FROM renditions
		WHERE video_id = $1 ORDER BY height DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rendition
	for rows.Next() {
		var rd Rendition
		if err := rows.Scan(&rd.Height, &rd.BitrateKbps, &rd.PlaylistKey); err != nil {
			return nil, err
		}
		out = append(out, rd)
	}
	return out, rows.Err()
}

// transition khoá dòng (FOR UPDATE), kiểm tra máy trạng thái, đổi status và chạy apply trong cùng transaction.
// Trả về trạng thái trước khi đổi.
func (r *Repo) transition(ctx context.Context, id string, to Status, apply func(pgx.Tx) error) (Status, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	var cur string
	err = tx.QueryRow(ctx, `SELECT status FROM videos WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	from := Status(cur)
	if !CanTransition(from, to) {
		return from, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	if _, err := tx.Exec(ctx, `UPDATE videos SET status = $2, updated_at = now() WHERE id = $1`,
		id, string(to)); err != nil {
		return from, err
	}
	if apply != nil {
		if err := apply(tx); err != nil {
			return from, err
		}
	}
	return from, tx.Commit(ctx)
}

func (r *Repo) Transition(ctx context.Context, id string, to Status) (Status, error) {
	return r.transition(ctx, id, to, nil)
}

// MarkReady: processing → ready, ghi thời lượng, prefix HLS, poster/sprite và thay toàn bộ rendition.
func (r *Repo) MarkReady(ctx context.Context, id string, info ReadyInfo) (Status, error) {
	return r.transition(ctx, id, StatusReady, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE videos SET duration_s = $2, hls_prefix = $3, fail_reason = NULL,
			poster_key = NULLIF($4, ''), sprite_key = NULLIF($5, '') WHERE id = $1`,
			id, info.DurationS, info.HLSPrefix, info.PosterKey, info.SpriteKey); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM renditions WHERE video_id = $1`, id); err != nil {
			return err
		}
		for _, rd := range info.Renditions {
			if _, err := tx.Exec(ctx, `INSERT INTO renditions (video_id, height, bitrate_kbps, playlist_key)
				VALUES ($1, $2, $3, $4)`, id, rd.Height, rd.BitrateKbps, rd.PlaylistKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkFailed: queued|processing → failed, ghi lý do.
func (r *Repo) MarkFailed(ctx context.Context, id, reason string) (Status, error) {
	return r.transition(ctx, id, StatusFailed, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE videos SET fail_reason = $2 WHERE id = $1`, id, reason)
		return err
	})
}

// RevertQueued là bước bù trừ (không thuộc máy trạng thái): khi đã đặt queued nhưng publish
// video.uploaded thất bại thì trả video về trạng thái trước đó (uploading hoặc failed).
func (r *Repo) RevertQueued(ctx context.Context, id string, to Status) error {
	_, err := r.pool.Exec(ctx, `UPDATE videos SET status = $2, updated_at = now()
		WHERE id = $1 AND status = 'queued'`, id, string(to))
	return err
}

func (r *Repo) ListStaleUploading(ctx context.Context, before time.Time) ([]Video, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+videoColumns+` FROM videos
		WHERE status = 'uploading' AND created_at < $1 ORDER BY created_at LIMIT 100`, before)
	if err != nil {
		return nil, err
	}
	return collectVideos(rows)
}

// DeleteUploading xoá video chỉ khi còn ở trạng thái uploading; ngược lại trả ErrNotFound.
func (r *Repo) DeleteUploading(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM videos WHERE id = $1 AND status = 'uploading'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

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
