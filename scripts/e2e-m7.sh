#!/usr/bin/env bash
# E2E M7: header bảo mật của nginx; CORS của MinIO cho upload từ trình duyệt (preflight PUT từ origin của SPA);
# xoá video (uploading → 204, queued/processing → 409, ready → 204 và HLS bị xoá khỏi MinIO); sửa metadata
# (PATCH) → tìm kiếm thấy tiêu đề mới; video lỗi (file chỉ có tiếng) → failed có fail_reason → retry → failed lại;
# viewer bị 403 với PATCH/DELETE/retry.
# Dùng: scripts/e2e-m7.sh [file] (mặc định tmp/sample.mp4, 30 giây 1080p). Cần: curl, jq, split, ffmpeg.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m7 nạp từ .env); SITE (mặc định http://localhost:8088);
# MINIO (mặc định http://localhost:9000, = S3_PUBLIC_ENDPOINT); TIMEOUT (giây chờ transcode, mặc định 300).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"
MINIO="${MINIO:-http://localhost:9000}"
TIMEOUT="${TIMEOUT:-300}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m7)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m7)}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq split ffmpeg; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
[ -f "$FILE" ] || die "không thấy $FILE (chạy: make sample)"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
ADMIN="$WORK/admin.jar"
VIEWER="$WORK/viewer.jar"
RUN="m7run$(date +%s)"

# code ARGS...: in mã HTTP của curl (không dừng khi 4xx/5xx).
code() { curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@"; }
# expect WANT WHAT ARGS...: so mã HTTP; khi lỗi chỉ in mô tả (không in body hay mật khẩu).
expect() {
  local want=$1 what=$2 got
  shift 2
  got=$(code "$@")
  [ "$got" = "$want" ] || die "$what: HTTP $got, want $want"
}
admin() { curl -fsS --max-time 30 -b "$ADMIN" "$@"; }
# create TITLE FILE: tạo video, in JSON (id + kế hoạch upload).
create() {
  jq -n --arg t "$1" --arg f "$(basename "$2")" --argjson s "$(wc -c < "$2" | tr -d ' ')" \
    '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}' |
    admin -X POST "$SITE/api/admin/videos" -H 'Content-Type: application/json' -d @-
}
# put_parts CREATE_JSON FILE: PUT từng part lên presigned URL (như trình duyệt, kèm Origin của SPA).
put_parts() {
  local plan=$1 file=$2 size i=0 part url
  size=$(echo "$plan" | jq -r .upload.part_size)
  rm -f "$WORK"/part.*
  split -b "$size" -a 4 "$file" "$WORK/part."
  for part in "$WORK"/part.*; do
    i=$((i + 1))
    url=$(echo "$plan" | jq -r --argjson n "$i" '.upload.parts[] | select(.part_number == $n) | .url')
    [ "$(curl -s --max-time 300 -o /dev/null -w '%{http_code}' -H "Origin: $SITE" -X PUT --upload-file "$part" "$url")" = 200 ] ||
      die "PUT part $i"
  done
}
# wait_status ID WANT: đợi tới trạng thái WANT (dừng nếu gặp ready/failed khác WANT), in JSON video.
wait_status() {
  local id=$1 want=$2 v s deadline=$(( $(date +%s) + TIMEOUT ))
  while :; do
    v=$(admin "$SITE/api/admin/videos/$id")
    s=$(echo "$v" | jq -r .status)
    [ "$s" = "$want" ] && { echo "$v"; return; }
    case "$s" in ready | failed) die "video $id: status=$s, want $want ($(echo "$v" | jq -r '.fail_reason // ""'))" ;; esac
    [ "$(date +%s)" -lt "$deadline" ] || die "hết thời gian chờ $id → $want"
    sleep 2
  done
}

echo "0/6 đăng nhập admin, đăng ký viewer"
jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$ADMIN" -X POST "$SITE/api/auth/login" -H 'Content-Type: application/json' -d @- |
  jq -e '.role == "admin"' >/dev/null || die "đăng nhập admin thất bại (chạy: make seed-admin)"
jq -n --arg e "viewer-m7-$(date +%s)-$RANDOM@example.com" '{email: $e, password: "viewer-password-1"}' |
  curl -fsS --max-time 30 -c "$VIEWER" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- >/dev/null ||
  die "đăng ký viewer"

echo "1/6 nginx: header bảo mật trên SPA, /api/, /cdn/, /assets/"
ASSET=$(curl -fsS --max-time 30 "$SITE/" | grep -oE '/assets/index-[^"]+\.js' | head -1)
for path in / /admin "$ASSET" /api/me /cdn/x /healthz; do
  H=$(curl -s --max-time 30 -o /dev/null -D - "$SITE$path" | tr -d '\r')
  for want in 'x-content-type-options: nosniff' 'x-frame-options: DENY' \
    "content-security-policy: frame-ancestors 'none'" 'referrer-policy: same-origin'; do
    echo "$H" | grep -qi "^$want\$" || die "GET $path thiếu header \"$want\""
  done
done
curl -fsS --max-time 30 "$SITE/admin/upload" | grep -q '<div id="root"></div>' || die "/admin/upload không trả SPA"

echo "2/6 upload như trình duyệt: preflight CORS của MinIO, PUT các part, complete"
PLAN=$(create "M7 $RUN" "$FILE")
ID=$(echo "$PLAN" | jq -r .id)
PART1=$(echo "$PLAN" | jq -r '.upload.parts[0].url')
PRE=$(curl -s --max-time 30 -o /dev/null -D - -X OPTIONS "$PART1" -H "Origin: $SITE" \
  -H 'Access-Control-Request-Method: PUT' | tr -d '\r')
echo "$PRE" | grep -qi "^access-control-allow-origin: $SITE\$" ||
  die "MinIO không cho PUT từ $SITE (MINIO_CORS_ALLOW_ORIGIN): $(echo "$PRE" | head -1)"
EVIL=$(curl -s --max-time 30 -o /dev/null -D - -X OPTIONS "$PART1" -H 'Origin: http://evil.example' \
  -H 'Access-Control-Request-Method: PUT' | tr -d '\r')
if echo "$EVIL" | grep -qi '^access-control-allow-origin: http://evil.example$'; then
  die "MinIO cho phép origin lạ http://evil.example"
fi
put_parts "$PLAN" "$FILE"
admin -X POST "$SITE/api/admin/videos/$ID/complete" | jq -e '.status == "queued"' >/dev/null || die "complete"
echo "    id=$ID queued"

echo "3/6 xoá: đang xử lý → 409; uploading → 204; viewer → 403"
expect 409 "DELETE video đang xử lý" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$ID"
expect 403 "viewer DELETE" -b "$VIEWER" -X DELETE "$SITE/api/admin/videos/$ID"
TMP_ID=$(create "M7 bỏ dở $RUN" "$FILE" | jq -r .id)
expect 204 "DELETE video uploading" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$TMP_ID"
expect 404 "GET video đã xoá" -b "$ADMIN" "$SITE/api/admin/videos/$TMP_ID"
wait_status "$ID" ready >/dev/null
echo "    $ID ready"

echo "4/6 PATCH metadata → tìm kiếm không dấu thấy tiêu đề mới; dữ liệu sai → 400; viewer → 403"
NEW_TITLE="Hạ cánh khẩn cấp $RUN"
jq -n --arg t "$NEW_TITLE" '{title: $t, genres: ["Tình cảm", "drama"], year: 2019}' |
  admin -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d @- |
  jq -e --arg t "$NEW_TITLE" '.title == $t and .genres == ["Tình cảm", "drama"] and .year == 2019 and .status == "ready"' \
    >/dev/null || die "PATCH"
curl -fsS --max-time 30 -b "$VIEWER" -G "$SITE/api/videos" --data-urlencode "q=ha canh khan cap $RUN" |
  jq -e --arg id "$ID" '[.videos[].id] == [$id]' >/dev/null || die "tìm tiêu đề mới sau PATCH"
expect 400 "PATCH year sai" -b "$ADMIN" -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d '{"year":1500}'
expect 400 "PATCH rỗng" -b "$ADMIN" -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d '{}'
expect 403 "viewer PATCH" -b "$VIEWER" -X PATCH "$SITE/api/admin/videos/$ID" -H 'Content-Type: application/json' -d '{"title":"x"}'

echo "5/6 video lỗi: file chỉ có tiếng → failed + fail_reason; viewer retry → 403; retry → failed lại"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i sine=frequency=440:sample_rate=48000 -t 2 -c:a aac \
  "$WORK/audio-only.mp4"
BAD=$(create "M7 lỗi $RUN" "$WORK/audio-only.mp4")
BAD_ID=$(echo "$BAD" | jq -r .id)
put_parts "$BAD" "$WORK/audio-only.mp4"
admin -X POST "$SITE/api/admin/videos/$BAD_ID/complete" >/dev/null
wait_status "$BAD_ID" failed | jq -e '.fail_reason | test("no video stream")' >/dev/null || die "fail_reason"
admin "$SITE/api/admin/videos?status=failed" | jq -e --arg id "$BAD_ID" '[.videos[].id] | index($id) != null' >/dev/null ||
  die "danh sách ?status=failed thiếu $BAD_ID"
expect 403 "viewer retry" -b "$VIEWER" -X POST "$SITE/api/admin/videos/$BAD_ID/retry"
admin -X POST "$SITE/api/admin/videos/$BAD_ID/retry" | jq -e '.status == "queued"' >/dev/null || die "retry"
expect 409 "retry video không failed" -b "$ADMIN" -X POST "$SITE/api/admin/videos/$ID/retry"
wait_status "$BAD_ID" failed >/dev/null
echo "    $BAD_ID failed lại sau retry"

echo "6/6 xoá video ready và failed: 204, /play 404, HLS bị xoá khỏi MinIO"
expect 200 "master.m3u8 trên MinIO trước khi xoá" "$MINIO/hls/$ID/master.m3u8"
expect 204 "DELETE video ready" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$ID"
expect 204 "DELETE video failed" -b "$ADMIN" -X DELETE "$SITE/api/admin/videos/$BAD_ID"
expect 404 "/play video đã xoá" -b "$VIEWER" "$SITE/api/videos/$ID/play"
expect 404 "master.m3u8 trên MinIO sau khi xoá" "$MINIO/hls/$ID/master.m3u8"
admin "$SITE/api/admin/videos" | jq -e --arg a "$ID" --arg b "$BAD_ID" '[.videos[].id] | (index($a) == null and index($b) == null)' \
  >/dev/null || die "video đã xoá còn trong danh sách admin"

echo "OK  M7  mở $SITE/admin"
