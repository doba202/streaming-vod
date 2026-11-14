package catalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Giới hạn metadata của video, dùng chung cho tạo video (POST /api/admin/videos) và sửa (PATCH).
// Độ dài thể loại và khoảng năm dùng chung hằng MaxGenreLen, MinYear, MaxYear với bộ lọc tìm kiếm (viewer.go).
const (
	MaxTitleLen       = 200  // rune
	MaxDescriptionLen = 5000 // rune
	MaxGenres         = 10
)

// ErrInvalidMetadata: tiêu đề, mô tả, thể loại hoặc năm không hợp lệ (HTTP 400).
var ErrInvalidMetadata = errors.New("invalid metadata")

// Metadata là phần admin sửa được của video. Year = 0 nghĩa là không rõ (NULL trong DB).
type Metadata struct {
	Title       string
	Description string
	Genres      []string
	Year        int
}

func invalidMetadata(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidMetadata, fmt.Sprintf(format, args...))
}

func hasControl(s string, allowNewline bool) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && !(allowNewline && (r == '\n' || r == '\r' || r == '\t'))
	}) >= 0
}

// NormalizeMetadata cắt khoảng trắng đầu/cuối, bỏ thể loại rỗng hoặc trùng (giữ thứ tự) và kiểm tra giới hạn.
// Genres luôn khác nil trong kết quả.
func NormalizeMetadata(m Metadata) (Metadata, error) {
	m.Title = strings.TrimSpace(m.Title)
	m.Description = strings.TrimSpace(m.Description)
	if m.Title == "" || !utf8.ValidString(m.Title) || utf8.RuneCountInString(m.Title) > MaxTitleLen || hasControl(m.Title, false) {
		return Metadata{}, invalidMetadata("title must be 1-%d characters of text", MaxTitleLen)
	}
	if !utf8.ValidString(m.Description) || utf8.RuneCountInString(m.Description) > MaxDescriptionLen || hasControl(m.Description, true) {
		return Metadata{}, invalidMetadata("description must be at most %d characters of text", MaxDescriptionLen)
	}
	genres := make([]string, 0, len(m.Genres))
	seen := map[string]bool{}
	for _, g := range m.Genres {
		g = strings.TrimSpace(g)
		if g == "" || seen[g] {
			continue
		}
		if !utf8.ValidString(g) || utf8.RuneCountInString(g) > MaxGenreLen || hasControl(g, false) {
			return Metadata{}, invalidMetadata("each genre must be 1-%d characters of text", MaxGenreLen)
		}
		seen[g] = true
		genres = append(genres, g)
	}
	if len(genres) > MaxGenres {
		return Metadata{}, invalidMetadata("at most %d genres", MaxGenres)
	}
	m.Genres = genres
	if m.Year != 0 && (m.Year < MinYear || m.Year > MaxYear) {
		return Metadata{}, invalidMetadata("year must be 0 (unknown) or between %d and %d", MinYear, MaxYear)
	}
	return m, nil
}
