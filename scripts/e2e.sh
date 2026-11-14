#!/usr/bin/env bash
# E2E toàn bộ MVP (spec §9): chạy lần lượt các kịch bản theo giai đoạn rồi in bảng tổng kết.
#   m3  auth, phân quyền, upload → transcode → ready (gồm e2e-m2), /play, URL ký sai/hết hạn → 403
#   m4  SPA + /api/ cùng origin qua nginx, tìm kiếm có dấu/không dấu/gõ sai
#   m5  poster, sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp", playback.events trong Kafka
#   m6  playback_events + app_logs trong ClickHouse, record hỏng bị bỏ qua, SQL mẫu
#   m7  header bảo mật, CORS MinIO cho upload từ trình duyệt, PATCH/DELETE, failed → retry
# Kịch bản lỗi không dừng các kịch bản sau; mã thoát 1 nếu có kịch bản lỗi.
# Dùng: scripts/e2e.sh [file] (mặc định tmp/sample.mp4). Biến: như các script e2e-m*.sh (make e2e nạp .env);
# E2E_ONLY="m4 m7" để chạy một phần; log đầy đủ của từng kịch bản ở $E2E_LOG_DIR (mặc định tmp/e2e-logs).
set -uo pipefail

FILE="${1:-tmp/sample.mp4}"
DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="${E2E_LOG_DIR:-tmp/e2e-logs}"
STEPS=(${E2E_ONLY:-m3 m4 m5 m6 m7})
mkdir -p "$LOG_DIR"

declare -a RESULTS=()
failed=0
start_all=$(date +%s)
for s in "${STEPS[@]}"; do
  script="$DIR/e2e-$s.sh"
  [ -x "$script" ] || { echo "không có $script" >&2; exit 2; }
  log="$LOG_DIR/e2e-$s.log"
  echo "=== e2e-$s ==="
  t0=$(date +%s)
  if "$script" "$FILE" 2>&1 | tee "$log"; then
    status=PASS
  else
    status=FAIL
    failed=$((failed + 1))
  fi
  RESULTS+=("$(printf '%-4s %-4s %4ss  %s' "$s" "$status" "$(( $(date +%s) - t0 ))" "$log")")
done

echo
echo "=== Tổng kết e2e ($(( $(date +%s) - start_all ))s) ==="
printf '%s\n' "${RESULTS[@]}"
if [ "$failed" -gt 0 ]; then
  echo "FAIL: $failed/${#STEPS[@]} kịch bản lỗi (xem log ở trên)"
  exit 1
fi
echo "OK  ${#STEPS[@]}/${#STEPS[@]} kịch bản qua"
