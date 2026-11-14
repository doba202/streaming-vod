#!/usr/bin/env bash
# E2E M2 (cập nhật M3): đăng nhập admin → tạo video → upload multipart qua presigned URL → complete
# → đợi ready → kiểm tra HLS qua nginx bằng URL ký từ /play.
# Dùng: scripts/e2e-m2.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, split.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m2 nạp từ .env); E2E_ID_FILE: nếu đặt, ghi video id vào file đó.
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
API="${API:-http://localhost:8000}"
TIMEOUT="${TIMEOUT:-300}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m2)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m2)}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
[ -f "$FILE" ] || die "không thấy $FILE (chạy: make sample)"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
JAR="$WORK/admin.jar"
# api: gọi API với cookie admin, lỗi HTTP thì dừng.
api() { curl -fsS --max-time 30 -b "$JAR" "$@"; }

SIZE=$(wc -c < "$FILE" | tr -d ' ')
NAME=$(basename "$FILE")

echo "0/5 đăng nhập admin ($ADMIN_EMAIL)"
jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$JAR" -X POST "$API/api/auth/login" -H 'Content-Type: application/json' -d @- |
  jq -e '.role == "admin"' >/dev/null || die "đăng nhập admin thất bại (chạy: make seed-admin)"

echo "1/5 tạo video ($NAME, $SIZE bytes)"
BODY=$(jq -n --arg t "E2E $(date +%H:%M:%S)" --arg f "$NAME" --argjson s "$SIZE" \
  '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}')
CREATE=$(api -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d "$BODY")
ID=$(echo "$CREATE" | jq -r .id)
PART_SIZE=$(echo "$CREATE" | jq -r .upload.part_size)
PARTS=$(echo "$CREATE" | jq -r '.upload.parts | length')
echo "    id=$ID parts=$PARTS part_size=$PART_SIZE"

echo "2/5 upload $PARTS part qua presigned URL"
split -b "$PART_SIZE" -a 4 "$FILE" "$WORK/part."
i=0
for part in "$WORK"/part.*; do
  i=$((i + 1))
  URL=$(echo "$CREATE" | jq -r --argjson n "$i" '.upload.parts[] | select(.part_number == $n) | .url')
  code=$(curl -sS --max-time 300 -o /dev/null -w '%{http_code}' -X PUT --upload-file "$part" "$URL")
  [ "$code" = 200 ] || die "upload part $i: HTTP $code"
done
[ "$i" = "$PARTS" ] || die "đã upload $i part, API cần $PARTS"

echo "3/5 complete"
api -X POST "$API/api/admin/videos/$ID/complete" | jq -c .

echo "4/5 đợi status=ready (tối đa ${TIMEOUT}s)"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  V=$(api "$API/api/admin/videos/$ID")
  STATUS=$(echo "$V" | jq -r .status)
  echo "    status=$STATUS"
  case "$STATUS" in
    ready) break ;;
    failed) die "transcode lỗi: $(echo "$V" | jq -r .fail_reason)" ;;
  esac
  [ "$(date +%s)" -lt "$deadline" ] || die "hết thời gian chờ"
  sleep 3
done
echo "$V" | jq -c '{duration_s, renditions: [.renditions[].height]}'

echo "5/5 kiểm tra HLS qua nginx bằng URL ký"
MASTER=$(api "$API/api/videos/$ID/play" | jq -r .url)
BASE="${MASTER%master.m3u8}" # .../cdn/s/{sig}/{exp}/hls/{id}/
curl -fsS --max-time 30 "$MASTER" | grep -q '#EXTM3U' || die "master.m3u8 không hợp lệ"
for key in $(echo "$V" | jq -r '.renditions[].playlist_key'); do
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$BASE${key#"$ID"/}")
  [ "$code" = 200 ] || die "$key: HTTP $code"
done
PLAYLIST=$(echo "$V" | jq -r '.renditions[0].playlist_key')
REL="${PLAYLIST#"$ID"/}" # 1080p/index.m3u8
SEG=$(curl -fsS --max-time 30 "$BASE$REL" | grep -m1 '\.ts$')
code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$BASE$(dirname "$REL")/$SEG")
[ "$code" = 200 ] || die "segment $SEG: HTTP $code"

[ -z "${E2E_ID_FILE:-}" ] || echo "$ID" > "$E2E_ID_FILE"
PLAYER="${MASTER%%/cdn/*}/dev/player.html?src=$(jq -rn --arg u "$MASTER" '$u | @uri')"
echo "OK  $PLAYER"
