// Package subtitle chuyển phụ đề admin upload (.srt hoặc .vtt) thành WebVTT mà thẻ <track> của
// trình duyệt đọc được (spec §4.2). Trình duyệt chỉ hiểu WebVTT; SRT khác ở dấu phẩy trong mốc
// thời gian, số thứ tự cue, không có header và cho phép thẻ <font>.
package subtitle

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupported = errors.New("subtitle: unsupported format, want .srt or .vtt")
	ErrNotUTF8     = errors.New("subtitle: file must be UTF-8 text")
	ErrNoCues      = errors.New("subtitle: no cues found")
	ErrNoHeader    = errors.New("subtitle: WebVTT file must start with WEBVTT")
)

// Convert chọn theo phần mở rộng của filename (không phân biệt hoa thường): .srt → FromSRT, .vtt → NormalizeVTT.
func Convert(filename string, data []byte) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".srt":
		return FromSRT(data)
	case ".vtt":
		return NormalizeVTT(data)
	default:
		return nil, ErrUnsupported
	}
}

// normalize bỏ BOM UTF-8, kiểm tra UTF-8 (không NUL) và đổi CRLF / CR thành LF.
func normalize(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", ErrNotUTF8
	}
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// NormalizeVTT giữ nguyên nội dung WebVTT (để trình duyệt tự parse), chỉ chuẩn hoá BOM / xuống dòng
// và kiểm tra tối thiểu: dòng đầu là "WEBVTT" (có thể kèm tiêu đề sau dấu cách/tab) và có ít nhất một cue.
func NormalizeVTT(data []byte) ([]byte, error) {
	s, err := normalize(data)
	if err != nil {
		return nil, err
	}
	first, _, _ := strings.Cut(s, "\n")
	if first != "WEBVTT" && !strings.HasPrefix(first, "WEBVTT ") && !strings.HasPrefix(first, "WEBVTT\t") {
		return nil, ErrNoHeader
	}
	if !strings.Contains(s, "-->") {
		return nil, ErrNoCues
	}
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return []byte(s), nil
}

// timingRe: "00:01:02,500 --> 00:01:04,000" (SRT dùng dấu phẩy; chấp nhận cả dấu chấm). Phần sau mốc
// kết thúc (toạ độ X1:… Y1:… của một số SRT) bị bỏ.
var timingRe = regexp.MustCompile(`^\s*(\d+):(\d{2}):(\d{2})[,.](\d{3})\s*-->\s*(\d+):(\d{2}):(\d{2})[,.](\d{3})(?:\s.*)?$`)

// assRe: thẻ override kiểu ASS mà nhiều file SRT mang theo, ví dụ {\an8}, {\i1}.
var assRe = regexp.MustCompile(`\{\\[^}]*\}`)

// tagRe: thẻ dạng <tên ...> hoặc </tên>. Chỉ <i>, <b>, <u> (WebVTT hỗ trợ) được giữ; <font ...> và
// thẻ khác bị bỏ. "<" không theo sau bởi chữ cái (ví dụ "1 < 2") là chữ thường và được escape.
var tagRe = regexp.MustCompile(`</?[A-Za-z][^<>]*>`)

type cue struct {
	start, end int64 // mili giây
	text       []string
}

// FromSRT chuyển SRT sang WebVTT. Cue cách nhau bởi một hoặc nhiều dòng trống; dòng số thứ tự có thể
// có hoặc không. Lỗi kèm số dòng khi một khối không có dòng thời gian hợp lệ hoặc kết thúc trước khi bắt đầu.
func FromSRT(data []byte) ([]byte, error) {
	s, err := normalize(data)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(s, "\n")
	var cues []cue
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		start := i // khối từ start tới trước dòng trống kế tiếp
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			i++
		}
		c, err := parseBlock(lines[start:i], start+1)
		if err != nil {
			return nil, err
		}
		if len(c.text) > 0 {
			cues = append(cues, c)
		}
	}
	if len(cues) == 0 {
		return nil, ErrNoCues
	}
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for _, c := range cues {
		fmt.Fprintf(&b, "\n%s --> %s\n%s\n", vttTime(c.start), vttTime(c.end), strings.Join(c.text, "\n"))
	}
	return []byte(b.String()), nil
}

// parseBlock đọc một khối SRT; lineNo là số dòng (1-based) của dòng đầu khối, dùng trong thông báo lỗi.
func parseBlock(block []string, lineNo int) (cue, error) {
	t := 0
	if !strings.Contains(block[0], "-->") && len(block) > 1 { // dòng số thứ tự
		t = 1
	}
	m := timingRe.FindStringSubmatch(block[t])
	if m == nil {
		return cue{}, fmt.Errorf("subtitle: line %d: invalid timing line %q", lineNo+t, truncate(block[t], 60))
	}
	c := cue{start: millis(m[1:5]), end: millis(m[5:9])}
	if c.end < c.start {
		return cue{}, fmt.Errorf("subtitle: line %d: cue ends before it starts", lineNo+t)
	}
	for _, l := range block[t+1:] {
		if l = cleanText(strings.TrimRight(l, " \t")); strings.TrimSpace(l) != "" {
			c.text = append(c.text, l)
		}
	}
	return c, nil
}

// cleanText bỏ thẻ ASS, bỏ thẻ không phải i/b/u và escape &, <, > trong phần chữ (nên "-->" trong lời
// thoại thành "--&gt;", không làm hỏng cấu trúc WebVTT).
func cleanText(s string) string {
	s = assRe.ReplaceAllString(s, "")
	var b strings.Builder
	last := 0
	for _, loc := range tagRe.FindAllStringIndex(s, -1) {
		b.WriteString(escape(s[last:loc[0]]))
		switch tag := strings.ToLower(s[loc[0]:loc[1]]); tag {
		case "<i>", "</i>", "<b>", "</b>", "<u>", "</u>":
			b.WriteString(tag)
		}
		last = loc[1]
	}
	b.WriteString(escape(s[last:]))
	return b.String()
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escape(s string) string { return escaper.Replace(s) }

func millis(p []string) int64 {
	n := make([]int64, 4)
	for i, s := range p {
		n[i], _ = strconv.ParseInt(s, 10, 64) // regex đã bảo đảm là chữ số
	}
	return ((n[0]*60+n[1])*60+n[2])*1000 + n[3]
}

// vttTime: 3723500 → "01:02:03.500".
func vttTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
