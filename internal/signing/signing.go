// Package signing ký URL phát HLS theo đúng công thức nginx secure_link_md5 trong
// deploy/nginx/templates/default.conf.template:
//
//	secure_link     $sl_sig,$sl_exp;
//	secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";
//
// sig = base64url không padding của MD5("{exp}/hls/{id}/ {secret}"). Ký theo prefix thư mục
// /hls/{id}/ nên một chữ ký dùng được cho master, playlist con và mọi segment (spec §4.3).
// MD5 vì nginx secure_link chỉ hỗ trợ MD5 (ADR 0009).
package signing

import (
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// TTL của URL phát (spec §4.3).
const TTL = 2 * time.Hour

// secretRe: chỉ ký tự an toàn khi chèn vào chuỗi trong cấu hình nginx (không có dấu cách, ", $, ;).
var secretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// videoIDRe khớp segment {id} mà location nginx nhận ([^/]+), thu hẹp cho an toàn.
var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidateSecret(secret string) error {
	if !secretRe.MatchString(secret) {
		return fmt.Errorf("CDN_SIGNING_SECRET must be 16-128 characters of [A-Za-z0-9_-]")
	}
	return nil
}

// Dir là prefix thư mục được ký: "/hls/{id}/".
func Dir(videoID string) string { return "/hls/" + videoID + "/" }

// Signature tính chữ ký cho prefix dir và thời điểm hết hạn exp (Unix giây).
func Signature(secret, dir string, exp int64) string {
	sum := md5.Sum([]byte(strconv.FormatInt(exp, 10) + dir + " " + secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SignedPath trả "/cdn/s/{sig}/{exp}/hls/{id}/{file}".
func SignedPath(secret, videoID, file string, exp int64) string {
	dir := Dir(videoID)
	return "/cdn/s/" + Signature(secret, dir, exp) + "/" + strconv.FormatInt(exp, 10) + dir + file
}

// Signer ký URL với TTL cố định.
type Signer struct {
	secret string
	ttl    time.Duration
	now    func() time.Time
}

func NewSigner(secret string, ttl time.Duration, now func() time.Time) (*Signer, error) {
	if err := ValidateSecret(secret); err != nil {
		return nil, err
	}
	return &Signer{secret: secret, ttl: ttl, now: now}, nil
}

// Path ký file trong thư mục HLS của video; trả path và thời điểm hết hạn.
func (s *Signer) Path(videoID, file string) (string, time.Time, error) {
	if !videoIDRe.MatchString(videoID) {
		return "", time.Time{}, fmt.Errorf("invalid video id %q", videoID)
	}
	exp := s.now().Add(s.ttl).Unix()
	return SignedPath(s.secret, videoID, file, exp), time.Unix(exp, 0).UTC(), nil
}
