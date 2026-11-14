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
