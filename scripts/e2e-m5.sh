#!/usr/bin/env bash
# E2E M5: poster, sprite và thumbnails.vtt qua URL ký; phụ đề (SRT → VTT) upload bởi admin và phát qua /play;
# sự kiện xem → watch_progress → "Xem tiếp" + resume_at; playback.events có trong Kafka; Kafka chết thì
# sự kiện xem vẫn 204 (tuỳ chọn).
# Dùng: scripts/e2e-m5.sh [file] (mặc định tmp/sample.mp4, 30 giây). Cần: curl, jq, od.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD (make e2e-m5 nạp từ .env); SITE (mặc định http://localhost:8088);
# COMPOSE: lệnh docker compose của stack (make truyền vào) để đọc topic playback.events, bỏ trống thì bỏ qua;
# E2E_KAFKA_DOWN=1: dừng Kafka, gửi sự kiện, bật lại (cần COMPOSE).
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m5)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m5)}"
read -ra COMPOSE_CMD <<< "${COMPOSE:-}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq od; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
KAFKA_STOPPED=0
cleanup() {
  if [ "$KAFKA_STOPPED" = 1 ]; then "${COMPOSE_CMD[@]}" up -d --wait kafka >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
VJAR="$WORK/viewer.jar"
AJAR="$WORK/admin.jar"
EMAIL="viewer-m5-$(date +%s)-$RANDOM@example.com"

# expect CODE curl-args...: gọi curl và so mã HTTP. Khi lỗi chỉ in URL (body có thể chứa mật khẩu).
expect() {
  local want=$1; shift
  local code a url=""
  for a in "$@"; do case $a in http://* | https://*) url=$a ;; esac; done
  code=$(curl -s --max-time 30 -o /dev/null -w '%{http_code}' "$@")
  [ "$code" = "$want" ] || die "$url: HTTP $code, want $want"
}
# fetch URL OUT: tải qua CDN, in Content-Type.
fetch() { curl -fsS --max-time 30 -o "$2" -w '%{content_type}' "$1"; }
play() { curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/videos/$ID/play"; }
# event TYPE POS: gửi sự kiện xem bằng cookie viewer, trả mã HTTP.
event() {
  jq -n --arg id "$ID" --arg t "$1" --argjson p "$2" \
    '{video_id: $id, type: $t, position_s: $p, bitrate_kbps: 2800, buffering_ms: 0}' |
    curl -s --max-time 30 -o /dev/null -w '%{http_code}' -b "$VJAR" -X POST "$SITE/api/playback/events" \
      -H 'Content-Type: application/json' -d @-
}
continue_pos() {
  curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/me/continue-watching" |
    jq -r --arg id "$ID" '[.videos[] | select(.id == $id) | .position_s][0] // "none"'
}
subtitle() { # subtitle LANG LABEL FILE [cookie jar]
  curl -s --max-time 30 -o "$WORK/sub.out" -w '%{http_code}' -b "${4:-$AJAR}" \
    -F "lang=$1" -F "label=$2" -F "file=@$3" "$SITE/api/admin/videos/$ID/subtitles"
}

echo "1/6 admin upload + transcode (scripts/e2e-m2.sh qua $SITE)"
API="$SITE" E2E_TITLE="M5 $(date +%H:%M:%S)" E2E_ID_FILE="$WORK/id" "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")
jq -n --arg e "$ADMIN_EMAIL" --arg p "$ADMIN_PASSWORD" '{email: $e, password: $p}' |
  curl -fsS --max-time 30 -c "$AJAR" -X POST "$SITE/api/auth/login" -H 'Content-Type: application/json' -d @- \
  >/dev/null || die "đăng nhập admin"
jq -n --arg e "$EMAIL" '{email: $e, password: "viewer-password-1"}' |
  curl -fsS --max-time 30 -c "$VJAR" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- \
  >/dev/null || die "đăng ký viewer"

echo "2/6 poster: poster_url ký trong catalog và chi tiết, JPEG qua CDN"
POSTER=$(curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/videos/$ID" | jq -r '.poster_url // empty')
case "$POSTER" in "$SITE/cdn/s/"*"/hls/$ID/poster.jpg") ;; *) die "poster_url = '$POSTER'" ;; esac
curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/videos?page=1" |
  jq -e --arg id "$ID" --arg p "$POSTER" 'any(.videos[]; .id == $id and .poster_url == $p)' >/dev/null ||
  die "catalog thiếu poster_url (hoặc khác trang chi tiết: URL ổn định theo giờ)"
[ "$(fetch "$POSTER" "$WORK/poster.jpg")" = image/jpeg ] || die "poster Content-Type"
[ "$(od -An -tx1 -N2 "$WORK/poster.jpg" | tr -d ' \n')" = ffd8 ] || die "poster không phải JPEG"

echo "3/6 sprite: thumbnails_url trong /play, 3 cue cho video 30 giây, tấm sprite là JPEG"
THUMBS=$(play | jq -r '.thumbnails_url // empty')
case "$THUMBS" in "$SITE/cdn/s/"*"/hls/$ID/thumbnails.vtt") ;; *) die "thumbnails_url = '$THUMBS'" ;; esac
[ "$(fetch "$THUMBS" "$WORK/thumbs.vtt")" = text/vtt ] || die "thumbnails.vtt Content-Type"
[ "$(head -1 "$WORK/thumbs.vtt")" = WEBVTT ] || die "thumbnails.vtt thiếu header WEBVTT"
[ "$(grep -c -- '-->' "$WORK/thumbs.vtt")" = 3 ] || die "thumbnails.vtt: $(grep -c -- '-->' "$WORK/thumbs.vtt") cue, want 3"
grep -q '^sprite_001.jpg#xywh=320,0,160,90$' "$WORK/thumbs.vtt" || die "thumbnails.vtt thiếu ô thứ 3"
SPRITE="${THUMBS%thumbnails.vtt}sprite_001.jpg" # đường dẫn tương đối trong VTT, resolve như trình duyệt
[ "$(fetch "$SPRITE" "$WORK/sprite.jpg")" = image/jpeg ] || die "sprite Content-Type"

echo "4/6 phụ đề: admin upload SRT (BOM, CRLF) và VTT; /play trả URL ký; upload lại thấy bản mới ngay"
printf '\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:04,000\r\nXin chào <i>thế giới</i>\r\n\r\n2\r\n00:00:05,000 --> 00:00:08,000\r\nPhụ đề tiếng Việt\r\n' \
  > "$WORK/vi.srt"
printf 'WEBVTT\n\n00:00:01.000 --> 00:00:04.000\nHello world\n' > "$WORK/en.vtt"
[ "$(subtitle vi 'Tiếng Việt' "$WORK/vi.srt")" = 201 ] || die "upload vi.srt: $(cat "$WORK/sub.out")"
jq -e --arg k "$ID/subs/vi.vtt" '.vtt_key == $k' "$WORK/sub.out" >/dev/null || die "vtt_key: $(cat "$WORK/sub.out")"
[ "$(subtitle en English "$WORK/en.vtt")" = 201 ] || die "upload en.vtt: $(cat "$WORK/sub.out")"
[ "$(subtitle VI 'Sai mã' "$WORK/vi.srt")" = 400 ] || die "lang VI phải 400"
printf 'không phải phụ đề\n' > "$WORK/bad.srt"
[ "$(subtitle fr Français "$WORK/bad.srt")" = 400 ] || die "SRT hỏng phải 400"
[ "$(subtitle fr Français "$WORK/vi.srt" "$VJAR")" = 403 ] || die "viewer upload phụ đề phải 403"
PLAY=$(play)
echo "$PLAY" | jq -e '[.subtitles[] | .lang + ":" + .label] == ["en:English", "vi:Tiếng Việt"]' >/dev/null ||
  die "subtitles: $(echo "$PLAY" | jq -c .subtitles)"
VI=$(echo "$PLAY" | jq -r '.subtitles[] | select(.lang == "vi") | .url')
[ "$(fetch "$VI" "$WORK/vi.vtt")" = text/vtt ] || die "vi.vtt Content-Type"
[ "$(head -1 "$WORK/vi.vtt")" = WEBVTT ] || die "vi.vtt thiếu header"
grep -q '^00:00:01.000 --> 00:00:04.000$' "$WORK/vi.vtt" || die "vi.vtt: mốc thời gian chưa chuyển"
grep -q '^Xin chào <i>thế giới</i>$' "$WORK/vi.vtt" || die "vi.vtt: nội dung"
printf '1\n00:00:01,000 --> 00:00:04,000\nBản sửa\n' > "$WORK/vi2.srt"
[ "$(subtitle vi 'Tiếng Việt' "$WORK/vi2.srt")" = 201 ] || die "upload lại vi"
fetch "$VI" "$WORK/vi.vtt" >/dev/null
grep -q '^Bản sửa$' "$WORK/vi.vtt" || die "CDN vẫn trả phụ đề cũ (subs/ không được cache)"

echo "5/6 sự kiện xem → Xem tiếp + resume_at"
[ "$(continue_pos)" = none ] || die "video chưa xem đã có trong Xem tiếp"
[ "$(play | jq .resume_at)" = 0 ] || die "resume_at trước khi xem phải 0"
[ "$(event play 0)" = 204 ] || die "event play"
[ "$(event heartbeat 12.4)" = 204 ] || die "event heartbeat"
[ "$(event seek 20)" = 204 ] || die "event seek"           # seek không ghi tiến độ
[ "$(event rewind 1)" = 400 ] || die "type sai phải 400"
[ "$(continue_pos)" = 12 ] || die "Xem tiếp: position_s = $(continue_pos), want 12"
[ "$(play | jq .resume_at)" = 12 ] || die "resume_at = $(play | jq .resume_at), want 12"
[ "$(event ended 30)" = 204 ] || die "event ended"
[ "$(continue_pos)" = none ] || die "xem hết vẫn còn trong Xem tiếp"
[ "$(play | jq .resume_at)" = 0 ] || die "xem hết thì resume_at = 0"
[ "$(event pause 15)" = 204 ] || die "event pause"
[ "$(continue_pos)" = 15 ] || die "pause phải ghi tiến độ"

echo "6/6 playback.events trong Kafka (key user_id)"
if [ ${#COMPOSE_CMD[@]} -eq 0 ]; then
  echo "    bỏ qua (không có COMPOSE)"
else
  MSGS=$("${COMPOSE_CMD[@]}" exec -T kafka /opt/kafka/bin/kafka-console-consumer.sh --bootstrap-server kafka:9092 \
    --topic playback.events --from-beginning --timeout-ms 10000 2>/dev/null || true)
  for t in play heartbeat seek ended pause; do
    echo "$MSGS" | jq -e --arg id "$ID" --arg t "$t" 'select(.video_id == $id and .type == $t)' >/dev/null ||
      die "không thấy sự kiện $t của video $ID trong playback.events"
  done
  if [ "${E2E_KAFKA_DOWN:-}" = 1 ]; then
    echo "    dừng Kafka: sự kiện xem vẫn 204 ngay, tiến độ vẫn được ghi"
    KAFKA_STOPPED=1
    "${COMPOSE_CMD[@]}" stop kafka >/dev/null 2>&1
    START=$(date +%s)
    [ "$(event heartbeat 18)" = 204 ] || die "Kafka chết: event phải 204"
    [ $(($(date +%s) - START)) -le 2 ] || die "Kafka chết: event chậm $(($(date +%s) - START))s"
    [ "$(continue_pos)" = 18 ] || die "Kafka chết: tiến độ không được ghi"
    "${COMPOSE_CMD[@]}" up -d --wait kafka >/dev/null 2>&1 || die "bật lại Kafka"
    KAFKA_STOPPED=0
  fi
fi

echo "OK  M5  mở $SITE/watch/$ID"
