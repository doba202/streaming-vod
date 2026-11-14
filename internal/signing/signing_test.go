package signing

import (
	"strings"
	"testing"
	"time"
)

const (
	testSecret = "dev-cdn-secret-change-me"
	testVID    = "0b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"
	testExp    = int64(1794736800) // 2026-11-15T10:00:00Z
)

// Vector tính độc lập bằng openssl (cùng công thức với nginx secure_link_md5):
//
//	printf '%s/hls/%s/ %s' 1794736800 0b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f dev-cdn-secret-change-me \
//	  | openssl dgst -md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
const wantSig = "H23K0bGijtyB6JSJpoVT1g"

func TestSignatureMatchesOpenSSLVector(t *testing.T) {
	if got := Signature(testSecret, Dir(testVID), testExp); got != wantSig {
		t.Errorf("Signature = %q, want %q", got, wantSig)
	}
}

func TestSignedPath(t *testing.T) {
	got := SignedPath(testSecret, testVID, "master.m3u8", testExp)
	want := "/cdn/s/" + wantSig + "/1794736800/hls/" + testVID + "/master.m3u8"
	if got != want {
		t.Errorf("SignedPath = %q, want %q", got, want)
	}
	// Ký theo prefix thư mục: file khác trong cùng thư mục có cùng chữ ký.
	seg := SignedPath(testSecret, testVID, "720p/seg_000.ts", testExp)
	if !strings.HasPrefix(seg, "/cdn/s/"+wantSig+"/1794736800/hls/"+testVID+"/") {
		t.Errorf("segment path = %q", seg)
	}
	// Khác video, khác exp hoặc khác secret thì khác chữ ký.
	others := []string{
		Signature(testSecret, Dir("1b9c2f4e-5d6a-4c1b-9e7f-1a2b3c4d5e6f"), testExp),
		Signature(testSecret, Dir(testVID), testExp+1),
		Signature(testSecret+"x", Dir(testVID), testExp),
	}
	for i, s := range others {
		if s == wantSig {
			t.Errorf("case %d: signature must differ", i)
		}
	}
	if len(wantSig) != 22 || strings.ContainsAny(wantSig, "+/=") {
		t.Errorf("signature phải là base64url 22 ký tự không padding: %q", wantSig)
	}
}

func TestSignerPath(t *testing.T) {
	now := time.Unix(testExp, 0).Add(-TTL)
	s, err := NewSigner(testSecret, TTL, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	p, exp, err := s.Path(testVID, "master.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if p != SignedPath(testSecret, testVID, "master.m3u8", testExp) || exp.Unix() != testExp {
		t.Errorf("Path = %q, %v", p, exp)
	}
	for _, bad := range []string{"", "a/b", "../x", "a b"} {
		if _, _, err := s.Path(bad, "master.m3u8"); err == nil {
			t.Errorf("video id %q: want error", bad)
		}
	}
}

func TestValidateSecret(t *testing.T) {
	if _, err := NewSigner("short", TTL, time.Now); err == nil {
		t.Error("short secret: want error")
	}
	for _, bad := range []string{"has space in it 123", `has"quote-0123456789`, "dollar$sign-0123456789", "semi;colon-0123456789"} {
		if err := ValidateSecret(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	if err := ValidateSecret(testSecret); err != nil {
		t.Error(err)
	}
}

func TestSignerPathRejectsBadFile(t *testing.T) {
	s, err := NewSigner(testSecret, TTL, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"", "/master.m3u8", "../x", "a/../b", "a?b", "a#b", `a\b`, "a b", "a\tb", "a\nb"} {
		if _, _, err := s.Path(testVID, f); err == nil {
			t.Errorf("Path(%q) accepted, want error", f)
		}
	}
	for _, f := range []string{"master.m3u8", "720p/seg_000.ts", "720p/index.m3u8"} {
		if _, _, err := s.Path(testVID, f); err != nil {
			t.Errorf("Path(%q) = %v, want ok", f, err)
		}
	}
}
