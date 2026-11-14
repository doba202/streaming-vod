#!/usr/bin/env bash
# E2E M3: đăng ký/đăng nhập, phân quyền, /play trả URL ký, nginx chặn URL sai hoặc hết hạn.
# Dùng: scripts/e2e-m3.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, openssl, split.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD, CDN_SIGNING_SECRET (make e2e-m3 nạp từ .env).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
API="${API:-http://localhost:8000}"
CDN="${CDN:-http://localhost:8088}"
: "${CDN_SIGNING_SECRET:?cần CDN_SIGNING_SECRET (chạy qua make e2e-m3)}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq openssl split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/viewer.jar"
EMAIL="viewer-$(date +%s)-$RANDOM@example.com"
PASSWORD="viewer-password-1"
NOID="00000000-0000-0000-0000-000000000000"

# expect CODE curl-args...: gọi curl và so mã HTTP.
expect() {
  local want=$1; shift
  local code
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "curl $*: HTTP $code, want $want"
}
creds() { jq -n --arg e "$1" --arg p "$2" '{email: $e, password: $p}'; }
# sign EXP ID: cùng công thức với nginx secure_link_md5 và internal/signing (tính độc lập bằng openssl).
sign() {
  printf '%s/hls/%s/ %s' "$1" "$2" "$CDN_SIGNING_SECRET" |
    openssl dgst -md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
}

echo "1/6 chưa đăng nhập → 401"
expect 401 "$API/api/me"
expect 401 "$API/api/videos"
expect 401 "$API/api/videos/$NOID/play"
expect 401 "$API/api/admin/videos"

echo "2/6 đăng ký viewer ($EMAIL), đăng nhập"
creds "$EMAIL" "$PASSWORD" | curl -fsS --max-time 30 -c "$JAR" -X POST "$API/api/auth/register" \
  -H 'Content-Type: application/json' -d @- | jq -e '.role == "viewer"' >/dev/null || die "register"
expect 409 -X POST "$API/api/auth/register" -H 'Content-Type: application/json' -d "$(creds "$EMAIL" "$PASSWORD")"
expect 401 -X POST "$API/api/auth/login" -H 'Content-Type: application/json' -d "$(creds "$EMAIL" "wrong-password")"
creds "$EMAIL" "$PASSWORD" | curl -fsS --max-time 30 -c "$JAR" -X POST "$API/api/auth/login" \
  -H 'Content-Type: application/json' -d @- >/dev/null || die "login"
curl -fsS --max-time 30 -b "$JAR" "$API/api/me" | jq -e --arg e "$EMAIL" '.email == $e and .role == "viewer"' >/dev/null ||
  die "/api/me"

echo "3/6 viewer gọi admin → 403"
expect 403 -b "$JAR" "$API/api/admin/videos"
expect 403 -b "$JAR" -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d '{}'
expect 403 -b "$JAR" -X POST "$API/api/admin/videos/$NOID/complete"

echo "4/6 admin upload + transcode (scripts/e2e-m2.sh)"
E2E_ID_FILE="$WORK/id" "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")

echo "5/6 viewer: catalog, chi tiết, /play, tải master + segment qua URL ký"
curl -fsS --max-time 30 -b "$JAR" "$API/api/videos" | jq -e --arg id "$ID" 'any(.videos[]; .id == $id)' >/dev/null ||
  die "video $ID không có trong /api/videos"
curl -fsS --max-time 30 -b "$JAR" "$API/api/videos/$ID" | jq -e '.renditions | length > 0' >/dev/null || die "/api/videos/$ID"
PLAY=$(curl -fsS --max-time 30 -b "$JAR" "$API/api/videos/$ID/play")
echo "$PLAY" | jq -e '.resume_at == 0 and .subtitles == []' >/dev/null || die "play body: $PLAY"
URL=$(echo "$PLAY" | jq -r .url)
# URL = $CDN/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8
IFS=/ read -r SIG EXP _ <<< "${URL#"$CDN"/cdn/s/}"
NOW=$(date +%s)
[ $((EXP - NOW)) -gt 7000 ] && [ $((EXP - NOW)) -le 7200 ] || die "exp phải ≈ now + 2h, còn $((EXP - NOW))s"
[ "$SIG" = "$(sign "$EXP" "$ID")" ] || die "chữ ký api khác công thức openssl/nginx"
BASE="${URL%master.m3u8}"
MASTER=$(curl -fsS --max-time 30 "$URL") || die "master.m3u8 qua URL ký"
VARIANT=$(echo "$MASTER" | grep -m1 '\.m3u8$')
SEG=$(curl -fsS --max-time 30 "$BASE$VARIANT" | grep -m1 '\.ts$')
expect 200 "$BASE$(dirname "$VARIANT")/$SEG"

echo "6/6 URL sai hoặc hết hạn → 403, không ký → không xem được"
if [ "${SIG:0:1}" = A ]; then R=B; else R=A; fi
expect 403 "$CDN/cdn/s/$R${SIG:1}/$EXP/hls/$ID/master.m3u8"         # sửa chữ ký
expect 403 "$CDN/cdn/s/$SIG/$((EXP + 3600))/hls/$ID/master.m3u8"     # kéo dài exp, giữ chữ ký
PAST=$((NOW - 60))
expect 403 "$CDN/cdn/s/$(sign "$PAST" "$ID")/$PAST/hls/$ID/master.m3u8" # ký đúng nhưng exp đã qua
expect 403 "$CDN/cdn/s/$SIG/$EXP/hls/$NOID/master.m3u8"              # chữ ký của video khác
expect 403 "$CDN/cdn/s/$SIG/$EXP/hls/$ID/../$NOID/master.m3u8" --path-as-is # traversal sang video khác
expect 403 "$CDN/cdn/s/$SIG/$EXP/hls/$ID/%2e%2e/$NOID/master.m3u8"      # traversal mã hoá
expect 404 "$CDN/hls/$ID/master.m3u8"                                 # đường /hls/ cũ không còn

echo "    logout → cookie bị xoá → /api/me 401"
expect 204 -b "$JAR" -c "$JAR" -X POST "$API/api/auth/logout"
expect 401 -b "$JAR" "$API/api/me"

echo "OK  M3"
