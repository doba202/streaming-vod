#!/usr/bin/env bash
# Chạy các câu SQL phân tích mẫu trong scripts/analytics/*.sql trên ClickHouse qua HTTP interface.
# Dùng: scripts/analytics-sql.sh [tên ...]   (vd. top_videos; không truyền thì chạy tất cả)
# Biến: CLICKHOUSE_URL (mặc định http://localhost:8123), CLICKHOUSE_DB, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD
# (mặc định streamfilm); FORMAT (mặc định PrettyCompactMonoBlock, e2e dùng JSONEachRow để kiểm tra).
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)/analytics"
URL="${CLICKHOUSE_URL:-http://localhost:8123}"
FORMAT="${FORMAT:-PrettyCompactMonoBlock}"

names=("$@")
if [ $# -eq 0 ]; then
  for f in "$DIR"/*.sql; do names+=("$(basename "$f" .sql)"); done
fi
for n in "${names[@]}"; do
  f="$DIR/$n.sql"
  [ -f "$f" ] || { echo "không có $f" >&2; exit 1; }
  [ "$FORMAT" = JSONEachRow ] || echo "== $n: $(head -1 "$f" | sed 's/^-- //')"
  { cat "$f"; printf '\nFORMAT %s\n' "$FORMAT"; } |
    curl -sS --fail-with-body --max-time 30 \
      -H "X-ClickHouse-User: ${CLICKHOUSE_USER:-streamfilm}" -H "X-ClickHouse-Key: ${CLICKHOUSE_PASSWORD:-streamfilm}" \
      --data-binary @- "$URL/?database=${CLICKHOUSE_DB:-streamfilm}"
done
