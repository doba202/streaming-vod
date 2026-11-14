package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"streamfilm/internal/httpx"
	"streamfilm/internal/subtitle"
)

// Subtitle là một phụ đề của video (bảng subtitles). VTTKey là key trong bucket hls.
type Subtitle struct {
	Lang, Label, VTTKey string
}

// Giới hạn upload phụ đề (spec §4.2).
const (
	MaxSubtitleBytes = 1 << 20 // 1 MiB: phụ đề phim 2 giờ thường 50–150 KB
	MaxLabelLen      = 50      // rune
)

// LangRe: mã ngôn ngữ dạng BCP 47 rút gọn — ISO 639-1/2 chữ thường, tuỳ chọn mã vùng ISO 3166 chữ hoa
// ("vi", "en", "fil", "pt-BR"). Đi thẳng vào key object nên không được có ký tự khác.
var LangRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

// SubtitleKey: {video_id}/subs/{lang}.vtt trong bucket hls (spec §4.2), cùng prefix ký với HLS.
func SubtitleKey(videoID, lang string) string { return videoID + "/subs/" + lang + ".vtt" }

// UpsertSubtitle thêm hoặc thay phụ đề (video_id, lang). Video không tồn tại → ErrNotFound.
func (r *Repo) UpsertSubtitle(ctx context.Context, videoID string, s Subtitle) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO subtitles (video_id, lang, label, vtt_key) VALUES ($1, $2, $3, $4)
		ON CONFLICT (video_id, lang) DO UPDATE SET label = EXCLUDED.label, vtt_key = EXCLUDED.vtt_key, updated_at = now()`,
		videoID, s.Lang, s.Label, s.VTTKey)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("upsert subtitle: %w", err)
	}
	return nil
}

// Subtitles trả phụ đề của video, sắp theo lang.
func (r *Repo) Subtitles(ctx context.Context, videoID string) ([]Subtitle, error) {
	rows, err := r.pool.Query(ctx, `SELECT lang, label, vtt_key FROM subtitles WHERE video_id = $1 ORDER BY lang`, videoID)
	if err != nil {
		return nil, fmt.Errorf("list subtitles: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Subtitle, error) {
		var s Subtitle
		err := row.Scan(&s.Lang, &s.Label, &s.VTTKey)
		return s, err
	})
}

type SubtitleStore interface {
	Get(ctx context.Context, id string) (Video, error)
	UpsertSubtitle(ctx context.Context, videoID string, s Subtitle) error
}

// ObjectPutter ghi object nhỏ vào storage (storage.Store).
type ObjectPutter interface {
	PutObject(ctx context.Context, bucket, key, contentType string, body []byte) error
}

// SubtitleHandler: POST /api/admin/videos/{id}/subtitles (router bọc auth.RequireAdmin).
type SubtitleHandler struct {
	Videos    SubtitleStore
	Objects   ObjectPutter
	BucketHLS string
	Log       *slog.Logger
}

func (h *SubtitleHandler) Routes(r chi.Router) {
	r.Post("/api/admin/videos/{id}/subtitles", h.upload)
}

// AdminSubtitleJSON là phản hồi của upload phụ đề.
type AdminSubtitleJSON struct {
	Lang   string `json:"lang"`
	Label  string `json:"label"`
	VTTKey string `json:"vtt_key"`
}

// formOverhead: phần multipart ngoài nội dung file (boundary, header, lang, label).
const formOverhead = 64 << 10

// upload nhận multipart/form-data: lang, label, file (.srt hoặc .vtt, ≤ 1 MiB, UTF-8). SRT được chuyển
// sang WebVTT; lưu vào hls/{id}/subs/{lang}.vtt (ghi đè nếu đã có) rồi upsert bảng subtitles.
func (h *SubtitleHandler) upload(w http.ResponseWriter, r *http.Request) {
	parsed, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	id := parsed.String() // dạng chuẩn (chữ thường): dùng cho key object, DB và phản hồi
	r.Body = http.MaxBytesReader(w, r.Body, MaxSubtitleBytes+formOverhead)
	if err := r.ParseMultipartForm(MaxSubtitleBytes + formOverhead); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "subtitle file must be at most 1 MiB")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "expected multipart/form-data with lang, label and file")
		return
	}
	defer r.MultipartForm.RemoveAll()

	lang := r.PostFormValue("lang")
	if !LangRe.MatchString(lang) {
		httpx.WriteError(w, http.StatusBadRequest, `lang must look like "vi", "en" or "pt-BR"`)
		return
	}
	label := strings.TrimSpace(r.PostFormValue("label"))
	if label == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > MaxLabelLen ||
		strings.IndexFunc(label, unicode.IsControl) >= 0 {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("label must be 1-%d characters of text", MaxLabelLen))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSubtitleBytes+1))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "cannot read file")
		return
	}
	if len(data) > MaxSubtitleBytes {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "subtitle file must be at most 1 MiB")
		return
	}
	vtt, err := subtitle.Convert(hdr.Filename, data)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := h.Videos.Get(r.Context(), id); errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	} else if err != nil {
		h.fail(w, id, "get video", err)
		return
	}
	s := Subtitle{Lang: lang, Label: label, VTTKey: SubtitleKey(id, lang)}
	if err := h.Objects.PutObject(r.Context(), h.BucketHLS, s.VTTKey, "text/vtt", vtt); err != nil {
		h.fail(w, id, "put subtitle", err)
		return
	}
	if err := h.Videos.UpsertSubtitle(r.Context(), id, s); errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	} else if err != nil {
		h.fail(w, id, "save subtitle", err)
		return
	}
	h.Log.InfoContext(r.Context(), "subtitle uploaded", "video_id", id, "lang", lang, "bytes", len(vtt))
	httpx.WriteJSON(w, http.StatusCreated, AdminSubtitleJSON{Lang: s.Lang, Label: s.Label, VTTKey: s.VTTKey})
}

func (h *SubtitleHandler) fail(w http.ResponseWriter, id, msg string, err error) {
	h.Log.Error(msg, "video_id", id, "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}
