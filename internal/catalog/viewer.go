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

	"streamfilm/internal/auth"
	"streamfilm/internal/httpx"
)

// ViewerStore là phần của Repo mà endpoint người xem cần.
type ViewerStore interface {
	Search(ctx context.Context, p SearchParams) ([]Video, error)
	Genres(ctx context.Context) ([]string, error)
	Get(ctx context.Context, id string) (Video, error)
	Renditions(ctx context.Context, id string) ([]Rendition, error)
	Subtitles(ctx context.Context, videoID string) ([]Subtitle, error)
	ReadyByIDs(ctx context.Context, ids []string) ([]Video, error)
}

// PlaySigner ký đường dẫn trong thư mục hls/{id}/ (signing.Signer). StablePath cho ảnh poster
// (URL ổn định trong một giờ để trình duyệt cache), Path cho mọi thứ khác.
type PlaySigner interface {
	Path(videoID, file string) (string, time.Time, error)
	StablePath(videoID, file string) (string, time.Time, error)
}

// ViewerHandler: catalog và /play cho user đã đăng nhập (router bọc auth.RequireUser).
type ViewerHandler struct {
	Videos     ViewerStore
	Progress   ProgressSource // playback.Repo: resume_at và hàng "Xem tiếp"
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
	r.Get("/api/me/continue-watching", h.continueWatching)
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
	PosterURL   string                `json:"poster_url,omitempty"` // URL ký; không có với video trước M5
	Renditions  []ViewerRenditionJSON `json:"renditions,omitempty"`
}

type SubtitleJSON struct {
	Lang  string `json:"lang"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// PlayJSON (spec §4.3): mọi URL cùng prefix ký /cdn/s/{sig}/{exp}/hls/{id}/. subtitles luôn là mảng
// ([] khi không có); thumbnails_url bỏ khi video chưa có sprite (transcode trước M5).
type PlayJSON struct {
	URL           string         `json:"url"`
	ExpiresAt     time.Time      `json:"expires_at"`
	Subtitles     []SubtitleJSON `json:"subtitles"`
	ThumbnailsURL string         `json:"thumbnails_url,omitempty"`
	ResumeAt      int            `json:"resume_at"`
}

func toViewerJSON(v Video) ViewerVideoJSON {
	genres := v.Genres
	if genres == nil {
		genres = []string{}
	}
	return ViewerVideoJSON{ID: v.ID, Title: v.Title, Description: v.Description, Genres: genres,
		Year: v.Year, DurationS: v.DurationS, CreatedAt: v.CreatedAt}
}

// ContinueJSON: một video của hàng "Xem tiếp" — thông tin như catalog kèm vị trí đã xem.
type ContinueJSON struct {
	ViewerVideoJSON
	PositionS int       `json:"position_s"`
	WatchedAt time.Time `json:"watched_at"`
}

// fileInDir: key "{id}/poster.jpg" → "poster.jpg". Key không nằm trong thư mục của video → false.
func fileInDir(videoID, key string) (string, bool) {
	f, ok := strings.CutPrefix(key, videoID+"/")
	return f, ok && f != ""
}

// toViewer: toViewerJSON + poster_url đã ký. Ký lỗi thì bỏ poster (web hiện ảnh tạm), không lỗi cả trang.
func (h *ViewerHandler) toViewer(v Video) ViewerVideoJSON {
	out := toViewerJSON(v)
	if f, ok := fileInDir(v.ID, v.PosterKey); ok {
		if p, _, err := h.Signer.StablePath(v.ID, f); err == nil {
			out.PosterURL = h.CDNBaseURL + p
		} else {
			h.Log.Warn("sign poster url", "video_id", v.ID, "err", err)
		}
	}
	return out
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
	for name, v := range map[string]string{"q": p.Query, "genre": p.Genre} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			httpx.WriteError(w, http.StatusBadRequest, name+" must be valid text")
			return
		}
	}
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
		out = append(out, h.toViewer(v))
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
	out := h.toViewer(v)
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
	subs, err := h.Videos.Subtitles(r.Context(), v.ID)
	if err != nil {
		h.Log.Error("list subtitles", "video_id", v.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := PlayJSON{URL: h.CDNBaseURL + path, ExpiresAt: exp, Subtitles: make([]SubtitleJSON, 0, len(subs))}
	for _, s := range subs {
		if u, ok := h.signFile(v.ID, s.VTTKey); ok {
			out.Subtitles = append(out.Subtitles, SubtitleJSON{Lang: s.Lang, Label: s.Label, URL: u})
		}
	}
	out.ThumbnailsURL, _ = h.signFile(v.ID, v.SpriteKey)
	// resume_at lỗi thì phát từ đầu: không vì tiến độ mà chặn việc xem.
	if id, ok := auth.FromContext(r.Context()); ok {
		if at, err := h.Progress.ResumeAt(r.Context(), id.UserID, v.ID); err != nil {
			h.Log.Warn("resume_at", "video_id", v.ID, "err", err)
		} else {
			out.ResumeAt = at
		}
	}
	// URL có hạn dùng riêng cho user này: không cho cache trung gian giữ lại.
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, out)
}

// signFile trả URL tuyệt đối đã ký cho key trong thư mục của video; key rỗng hoặc lạ → false.
func (h *ViewerHandler) signFile(videoID, key string) (string, bool) {
	f, ok := fileInDir(videoID, key)
	if !ok {
		return "", false
	}
	p, _, err := h.Signer.Path(videoID, f)
	if err != nil {
		h.Log.Warn("sign file", "video_id", videoID, "key", key, "err", err)
		return "", false
	}
	return h.CDNBaseURL + p, true
}

// continueWatching: GET /api/me/continue-watching → {"videos": [...]} tối đa ContinueWatchingLimit video
// đang xem dở (5%–95%), mới xem nhất trước (spec §4.4).
func (h *ViewerHandler) continueWatching(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "login required")
		return
	}
	entries, err := h.Progress.ContinueWatching(r.Context(), id.UserID, ContinueWatchingLimit)
	if err != nil {
		h.Log.Error("continue watching", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.VideoID
	}
	vs, err := h.Videos.ReadyByIDs(r.Context(), ids)
	if err != nil {
		h.Log.Error("continue watching videos", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	byID := make(map[string]Video, len(vs))
	for _, v := range vs {
		byID[v.ID] = v
	}
	out := make([]ContinueJSON, 0, len(entries))
	for _, e := range entries { // giữ thứ tự updated_at DESC của watch_progress
		if v, ok := byID[e.VideoID]; ok {
			out = append(out, ContinueJSON{ViewerVideoJSON: h.toViewer(v), PositionS: e.PositionS, WatchedAt: e.UpdatedAt})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"videos": out})
}
