#!/usr/bin/env bash
# E2E M2: tạo video → upload multipart qua presigned URL → complete → đợi ready → kiểm tra HLS qua nginx.
# Dùng: scripts/e2e-m2.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq, split.
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
API="${API:-http://localhost:8000}"
CDN="${CDN:-http://localhost:8088}"
TIMEOUT="${TIMEOUT:-300}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq split; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done
[ -f "$FILE" ] || die "không thấy $FILE (chạy: make sample)"

SIZE=$(wc -c < "$FILE" | tr -d ' ')
NAME=$(basename "$FILE")

echo "1/5 tạo video ($NAME, $SIZE bytes)"
BODY=$(jq -n --arg t "E2E $(date +%H:%M:%S)" --arg f "$NAME" --argjson s "$SIZE" \
  '{title: $t, filename: $f, size_bytes: $s, genres: ["test"], year: 2026}')
CREATE=$(curl -fsS --max-time 30 -X POST "$API/api/admin/videos" -H 'Content-Type: application/json' -d "$BODY")
ID=$(echo "$CREATE" | jq -r .id)
PART_SIZE=$(echo "$CREATE" | jq -r .upload.part_size)
PARTS=$(echo "$CREATE" | jq -r '.upload.parts | length')
echo "    id=$ID parts=$PARTS part_size=$PART_SIZE"

echo "2/5 upload $PARTS part qua presigned URL"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
split -b "$PART_SIZE" -a 4 "$FILE" "$WORK/part."
i=0
for part in $(ls "$WORK"/part.* | sort); do
  i=$((i + 1))
  URL=$(echo "$CREATE" | jq -r --argjson n "$i" '.upload.parts[] | select(.part_number == $n) | .url')
  code=$(curl -sS --max-time 300 -o /dev/null -w '%{http_code}' -X PUT --upload-file "$part" "$URL")
  [ "$code" = 200 ] || die "upload part $i: HTTP $code"
done
[ "$i" = "$PARTS" ] || die "đã upload $i part, API cần $PARTS"

echo "3/5 complete"
curl -fsS --max-time 30 -X POST "$API/api/admin/videos/$ID/complete" | jq -c .

echo "4/5 đợi status=ready (tối đa ${TIMEOUT}s)"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  V=$(curl -fsS --max-time 30 "$API/api/admin/videos/$ID")
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

echo "5/5 kiểm tra HLS qua nginx"
curl -fsS --max-time 30 "$CDN/hls/$ID/master.m3u8" | grep -q '#EXTM3U' || die "master.m3u8 không hợp lệ"
for key in $(echo "$V" | jq -r '.renditions[].playlist_key'); do
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$CDN/hls/$key")
  [ "$code" = 200 ] || die "$key: HTTP $code"
done
PLAYLIST=$(echo "$V" | jq -r '.renditions[0].playlist_key')
SEG=$(curl -fsS --max-time 30 "$CDN/hls/$PLAYLIST" | grep -m1 '\.ts$')
code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$CDN/hls/$(dirname "$PLAYLIST")/$SEG")
[ "$code" = 200 ] || die "segment $SEG: HTTP $code"

echo "OK  $CDN/dev/player.html?id=$ID"
