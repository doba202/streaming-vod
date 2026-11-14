# ADR 0007: Chấp nhận dual-write Postgres + Kafka ở MVP

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
`/complete` và `/retry` vừa đổi trạng thái trong Postgres vừa publish `video.uploaded`. Hai thao tác không nằm chung một transaction.

## Quyết định
Đặt `queued` trước rồi publish (chờ tối đa 5 giây). Publish lỗi thì trả video về trạng thái trước đó và trả `503` để admin gọi lại. Không dùng outbox pattern ở MVP.

## Hệ quả
- `Producer.Publish` tự chờ theo `ctx` (5 giây): `ProduceSync` của franz-go không trả về đúng hạn khi Kafka chết sau khi client đã kết nối, nên `/complete` từng treo thay vì trả `503`.
- **Ghost publish.** Khi ctx hết hạn, `/complete` trả `503` và đưa video về `uploading`, nhưng record `video.uploaded` có thể vẫn nằm trong buffer của franz-go và tới broker muộn (khi Kafka sống lại). Transcoder nhận nó như mọi job: publish `video.processing`, tải raw, transcode, upload `hls/{id}/`, publish `video.transcoded`. Lúc đó video đang `uploading`, nên consumer của api gặp chuyển `uploading → processing` / `uploading → ready` (hoặc `uploading → failed`) không hợp lệ, log và bỏ qua; trạng thái không đổi. Hệ quả: tốn CPU cho một lần transcode thừa và có thể để lại output HLS mồ côi dưới `hls/{id}/` (job dọn upload bỏ dở chỉ xoá object raw và multipart, không xoá `hls/`). Admin gọi lại `/complete` là an toàn: video `uploading → queued`, publish bản mới, transcoder ghi đè `hls/{id}/`, video tới `ready` như bình thường; nếu bản "ma" và bản mới cùng được xử lý thì kết quả vẫn idempotent.
- Đặt trạng thái trước đảm bảo api không nhận `video.processing` khi DB còn ở trạng thái cũ.
- Nếu api chết đúng giữa hai bước, video có thể kẹt ở `queued` mà không có sự kiện; admin xử lý thủ công. Bản sau: outbox table + relay publish.
