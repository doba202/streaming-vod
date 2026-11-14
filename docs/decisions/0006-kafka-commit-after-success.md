# ADR 0006: Commit offset sau khi xử lý xong, thử lại tại chỗ, DLQ ở transcoder

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Transcoder có thể chết giữa job; DB hoặc Kafka có thể chập chờn. Không được mất sự kiện, không được đảo thứ tự sự kiện của cùng một video.

## Quyết định
- Consumer tắt auto-commit, commit từng record sau khi handler trả về thành công.
- Handler lỗi thì record được thử lại tại chỗ (1s → 30s), không bỏ qua và không crash. Lỗi "nghiệp vụ" (JSON hỏng, chuyển trạng thái không hợp lệ) handler tự log và trả thành công.
- Transcoder thử lại FFmpeg sau 5s, 30s, 2 phút ngay trong handler (offset chưa commit trong lúc chờ). Hết lượt hoặc lỗi vĩnh viễn: publish `video.failed` + `video.uploaded.dlq`, rồi commit.
- Consumer của worker dùng `RebalanceTimeout` 30 phút và `SessionTimeout` 60 giây vì handler chạy lâu (franz-go chặn rebalance trong lúc xử lý).
- Key = `video_id` để giữ thứ tự theo video.

## Hệ quả
- At-least-once: handler phải idempotent (HLS ghi đè `hls/{id}/`, api bỏ qua chuyển trạng thái không hợp lệ).
- Một record lỗi kéo dài chặn các record sau nó trong cùng partition (head-of-line blocking). Chấp nhận ở MVP; bản sau có thể dùng topic retry riêng.
- Mỗi worker xử lý tuần tự một job; tối đa 3 worker song song (3 partition).
