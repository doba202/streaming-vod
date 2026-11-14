package subtitle

import (
	"errors"
	"strings"
	"testing"
)

func TestFromSRT(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "cơ bản: số thứ tự, dấu phẩy, nhiều dòng",
			in: "1\n00:00:01,000 --> 00:00:03,500\nXin chào\nthế giới\n\n" +
				"2\n00:00:04,250 --> 00:00:06,000\nTạm biệt\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:03.500\nXin chào\nthế giới\n\n" +
				"00:00:04.250 --> 00:00:06.000\nTạm biệt\n",
		},
		{
			name: "BOM và CRLF",
			in:   "\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:02,000\r\nMột\r\n\r\n2\r\n00:00:02,000 --> 00:00:03,000\r\nHai\r\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nMột\n\n00:00:02.000 --> 00:00:03.000\nHai\n",
		},
		{
			name: "chỉ CR (Mac cũ)",
			in:   "1\r00:00:01,000 --> 00:00:02,000\rMột\r",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nMột\n",
		},
		{
			name: "không có số thứ tự, nhiều dòng trống, khoảng trắng thừa, toạ độ sau mốc thời gian",
			in: "\n\n00:00:01,000 --> 00:00:02,000 X1:100 X2:200 Y1:10 Y2:20\nMột   \n\n\n\n" +
				"  \t\n00:00:03.000-->00:00:04.000\nHai\n\n\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nMột\n\n00:00:03.000 --> 00:00:04.000\nHai\n",
		},
		{
			name: "giờ lớn hơn 99 và mili giây",
			in:   "7\n100:59:59,999 --> 101:00:00,001\nDài\n",
			want: "WEBVTT\n\n100:59:59.999 --> 101:00:00.001\nDài\n",
		},
		{
			name: "thẻ: giữ i/b/u, bỏ font và ASS, escape & < > và -->",
			in: "1\n00:00:01,000 --> 00:00:02,000\n{\\an8}<i>Nghiêng</i> <FONT color=\"#ff0\">vàng</FONT> <B>đậm</B>\n" +
				"Tom & Jerry: 1 < 2 --> 3\n",
			want: "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<i>Nghiêng</i> vàng <b>đậm</b>\n" +
				"Tom &amp; Jerry: 1 &lt; 2 --&gt; 3\n",
		},
		{
			name: "cue không có chữ bị bỏ",
			in:   "1\n00:00:01,000 --> 00:00:02,000\n\n2\n00:00:03,000 --> 00:00:04,000\nCòn lại\n",
			want: "WEBVTT\n\n00:00:03.000 --> 00:00:04.000\nCòn lại\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FromSRT([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestFromSRTErrors(t *testing.T) {
	cases := []struct {
		name, in string
		is       error  // lỗi sentinel mong đợi, hoặc
		contains string // chuỗi trong thông báo lỗi
	}{
		{name: "rỗng", in: "", is: ErrNoCues},
		{name: "chỉ dòng trống", in: "\r\n\n  \n", is: ErrNoCues},
		{name: "không phải UTF-8 (Windows-1258)", in: "1\n00:00:01,000 --> 00:00:02,000\nXin ch\xe0o\n", is: ErrNotUTF8},
		{name: "có byte NUL", in: "1\n00:00:01,000 --> 00:00:02,000\nA\x00B\n", is: ErrNotUTF8},
		{name: "khối không có dòng thời gian", in: "1\n00:00:01,000 --> 00:00:02,000\nA\n\n2\nkhông phải mốc\nB\n", contains: "line 6"},
		{name: "mili giây thiếu chữ số", in: "1\n00:00:01,5 --> 00:00:02,000\nA\n", contains: "line 2: invalid timing"},
		{name: "kết thúc trước bắt đầu", in: "1\n00:00:05,000 --> 00:00:02,000\nA\n", contains: "ends before it starts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FromSRT([]byte(c.in))
			if err == nil {
				t.Fatal("want error")
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("err = %v, want %v", err, c.is)
			}
			if c.contains != "" && !strings.Contains(err.Error(), c.contains) {
				t.Errorf("err = %v, want chứa %q", err, c.contains)
			}
		})
	}
}

func TestNormalizeVTT(t *testing.T) {
	got, err := NormalizeVTT([]byte("\xef\xbb\xbfWEBVTT - Tiếng Việt\r\n\r\n00:01.000 --> 00:02.000\r\n<v Nam>Chào</v>"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "WEBVTT - Tiếng Việt\n\n00:01.000 --> 00:02.000\n<v Nam>Chào</v>\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, c := range []struct {
		in   string
		want error
	}{
		{"1\n00:00:01,000 --> 00:00:02,000\nSRT đổi đuôi\n", ErrNoHeader},
		{"WEBVTTX\n\n00:01.000 --> 00:02.000\nA\n", ErrNoHeader},
		{"WEBVTT\n\nNOTE chỉ có ghi chú\n", ErrNoCues},
		{"WEBVTT\n\n00:01.000 --> 00:02.000\nXin ch\xe0o\n", ErrNotUTF8},
	} {
		if _, err := NormalizeVTT([]byte(c.in)); !errors.Is(err, c.want) {
			t.Errorf("NormalizeVTT(%q) err = %v, want %v", c.in, err, c.want)
		}
	}
}

func TestConvert(t *testing.T) {
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\nA\n")
	if got, err := Convert("Phim.VI.SRT", srt); err != nil || !strings.HasPrefix(string(got), "WEBVTT\n\n00:00:01.000") {
		t.Errorf(".SRT: %q, %v", got, err)
	}
	if got, err := Convert("phim.vtt", []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nA\n")); err != nil || !strings.HasPrefix(string(got), "WEBVTT") {
		t.Errorf(".vtt: %q, %v", got, err)
	}
	for _, name := range []string{"phim.ass", "phim", "phim.srt.txt"} {
		if _, err := Convert(name, srt); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", name, err)
		}
	}
}
