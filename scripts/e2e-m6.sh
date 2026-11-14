#!/usr/bin/env bash
# E2E M6: sự kiện xem (playback.events) và log của api/transcoder (app.logs) vào ClickHouse qua service
# analytics; record hỏng trong playback.events bị bỏ qua; SQL mẫu chạy được; ClickHouse chết thì analytics
# thử lại và không mất sự kiện (tuỳ chọn).
# Dùng: scripts/e2e-m6.sh [file] (mặc định tmp/sample.mp4). Cần: curl, jq.
# Biến: ADMIN_EMAIL, ADMIN_PASSWORD, CLICKHOUSE_* (make e2e-m6 nạp từ .env); SITE (mặc định http://localhost:8088);
# COMPOSE: lệnh docker compose của stack (make truyền vào) để ghi record hỏng vào Kafka, đọc log analytics và
# dừng ClickHouse, bỏ trống thì bỏ qua các bước đó; E2E_CLICKHOUSE_DOWN=1: dừng ClickHouse, gửi sự kiện, bật lại.
set -euo pipefail

FILE="${1:-tmp/sample.mp4}"
SITE="${SITE:-http://localhost:8088}"
CH_URL="${CLICKHOUSE_URL:-http://localhost:8123}"
: "${ADMIN_EMAIL:?cần ADMIN_EMAIL (chạy qua make e2e-m6)}"
: "${ADMIN_PASSWORD:?cần ADMIN_PASSWORD (chạy qua make e2e-m6)}"
read -ra COMPOSE_CMD <<< "${COMPOSE:-}"

die() { echo "FAIL: $*" >&2; exit 1; }
for c in curl jq; do command -v "$c" >/dev/null || die "thiếu lệnh $c"; done

WORK=$(mktemp -d)
CH_STOPPED=0
cleanup() {
  if [ "$CH_STOPPED" = 1 ]; then "${COMPOSE_CMD[@]}" up -d --wait clickhouse >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
VJAR="$WORK/viewer.jar"
EMAIL="viewer-m6-$(date +%s)-$RANDOM@example.com"

# ch SQL: chạy một câu trên ClickHouse qua HTTP, in kết quả (TSV).
ch() {
  curl -sS --fail-with-body --max-time 30 -H "X-ClickHouse-User: ${CLICKHOUSE_USER:-streamfilm}" \
    -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-streamfilm}" --data-binary "$1" \
    "$CH_URL/?database=${CLICKHOUSE_DB:-streamfilm}"
}
# wait_ch SQL WANT [giây]: chờ tới khi kết quả của SQL bằng WANT (analytics ghi theo lô mỗi 1 giây).
wait_ch() {
  local got="" end=$(($(date +%s) + ${3:-60}))
  while [ "$(date +%s)" -le "$end" ]; do
    got=$(ch "$1" 2>/dev/null || true)
    [ "$got" = "$2" ] && return 0
    sleep 1
  done
  die "ClickHouse: '$1' = '$got', want '$2'"
}
# event TYPE POS [BUFFERING_MS]: gửi sự kiện xem bằng cookie viewer, trả mã HTTP.
event() {
  jq -n --arg id "$ID" --arg t "$1" --argjson p "$2" --argjson b "${3:-0}" \
    '{video_id: $id, type: $t, position_s: $p, bitrate_kbps: 2800, buffering_ms: $b}' |
    curl -s --max-time 30 -o /dev/null -w '%{http_code}' -b "$VJAR" -X POST "$SITE/api/playback/events" \
      -H 'Content-Type: application/json' -d @-
}
types() { echo "SELECT arrayStringConcat(arraySort(groupArray(type)), ',') FROM playback_events WHERE video_id = '$ID'"; }

echo "1/6 admin upload + transcode (scripts/e2e-m2.sh qua $SITE), đăng ký viewer"
ch 'SELECT 1' >/dev/null || die "không gọi được ClickHouse ở $CH_URL"
API="$SITE" E2E_TITLE="M6 $(date +%H:%M:%S)" E2E_ID_FILE="$WORK/id" "$(dirname "$0")/e2e-m2.sh" "$FILE" | sed 's/^/    /'
ID=$(cat "$WORK/id")
jq -n --arg e "$EMAIL" '{email: $e, password: "viewer-password-1"}' |
  curl -fsS --max-time 30 -c "$VJAR" -X POST "$SITE/api/auth/register" -H 'Content-Type: application/json' -d @- \
  >/dev/null || die "đăng ký viewer"
USER_ID=$(curl -fsS --max-time 30 -b "$VJAR" "$SITE/api/me" | jq -r .id)

# JSON hợp lệ (để e2e-m5 đọc topic bằng jq vẫn được) nhưng user_id không phải UUID: analytics phải bỏ qua.
echo "2/6 record hỏng vào playback.events (analytics phải bỏ qua và chạy tiếp)"
if [ ${#COMPOSE_CMD[@]} -eq 0 ]; then
  echo "    bỏ qua (không có COMPOSE)"
else
  echo '{"e2e":"m6 poison","user_id":"not-a-uuid"}' | "${COMPOSE_CMD[@]}" exec -T kafka /opt/kafka/bin/kafka-console-producer.sh \
    --bootstrap-server kafka:9092 --topic playback.events >/dev/null 2>&1 || die "ghi record hỏng"
fi

echo "3/6 /play và sự kiện xem: play, heartbeat, buffering, seek, ended"
curl -fsS --max-time 30 -o /dev/null -b "$VJAR" "$SITE/api/videos/$ID/play" || die "/play"
[ "$(event play 0)" = 204 ] || die "event play"
[ "$(event heartbeat 10.2)" = 204 ] || die "event heartbeat"
[ "$(event buffering 11 1500)" = 204 ] || die "event buffering"
[ "$(event seek 25)" = 204 ] || die "event seek"
[ "$(event ended 30)" = 204 ] || die "event ended"

echo "4/6 playback_events trong ClickHouse"
wait_ch "$(types)" "buffering,ended,heartbeat,play,seek"
[ "$(ch "SELECT toString(any(user_id)), uniqExact(user_id) FROM playback_events WHERE video_id = '$ID'")" = "$USER_ID	1" ] ||
  die "user_id trong playback_events khác $USER_ID"
[ "$(ch "SELECT position_s, bitrate_kbps, buffering_ms FROM playback_events WHERE video_id = '$ID' AND type = 'buffering'")" = "11	2800	1500" ] ||
  die "row buffering sai"
if [ ${#COMPOSE_CMD[@]} -gt 0 ]; then
  "${COMPOSE_CMD[@]}" logs --no-log-prefix analytics 2>/dev/null | grep -q '"msg":"skip invalid record"' ||
    die "analytics không log record hỏng"
fi

echo "5/6 app_logs: log api có request_id (nginx sinh) + video_id, log transcoder theo video_id, không có log của analytics"
# nginx đặt X-Request-Id = $request_id (32 ký tự hex) trước khi proxy tới api.
wait_ch "SELECT count() > 0 FROM app_logs WHERE service = 'api' AND video_id = '$ID' AND msg = 'http request' AND JSONExtractString(attrs, 'path') = '/api/videos/$ID/play' AND JSONExtractInt(attrs, 'status') = 200 AND match(request_id, '^[0-9a-f]{32}$')" 1
wait_ch "SELECT count() > 0 FROM app_logs WHERE service = 'transcoder' AND video_id = '$ID'" 1
[ "$(ch "SELECT count() FROM app_logs WHERE service = 'analytics'")" = 0 ] || die "log của analytics lọt vào app_logs"

echo "6/6 SQL mẫu (scripts/analytics/*.sql) trả kết quả"
for f in "$(dirname "$0")"/analytics/*.sql; do
  n=$(basename "$f" .sql)
  out=$(FORMAT=JSONEachRow "$(dirname "$0")/analytics-sql.sh" "$n") || die "SQL $n lỗi"
  [ -n "$out" ] || die "SQL $n không có dòng nào"
  echo "    $n: $(echo "$out" | wc -l | tr -d ' ') dòng"
done

if [ "${E2E_CLICKHOUSE_DOWN:-}" = 1 ]; then
  [ ${#COMPOSE_CMD[@]} -gt 0 ] || die "E2E_CLICKHOUSE_DOWN cần COMPOSE"
  echo "    dừng ClickHouse: sự kiện vẫn 204, analytics thử lại rồi ghi đủ khi ClickHouse sống lại"
  CH_STOPPED=1
  "${COMPOSE_CMD[@]}" stop clickhouse >/dev/null 2>&1
  [ "$(event heartbeat 20)" = 204 ] || die "ClickHouse chết: event phải 204"
  [ "$(event pause 21)" = 204 ] || die "ClickHouse chết: event phải 204"
  sleep 5
  "${COMPOSE_CMD[@]}" up -d --wait clickhouse >/dev/null 2>&1 || die "bật lại ClickHouse"
  CH_STOPPED=0
  wait_ch "$(types)" "buffering,ended,heartbeat,heartbeat,pause,play,seek" 90
  "${COMPOSE_CMD[@]}" logs --no-log-prefix analytics 2>/dev/null | grep -q '"msg":"clickhouse insert failed, retrying"' ||
    die "analytics không log lần thử lại"
fi

echo "OK  M6  xem: make analytics-sql"
