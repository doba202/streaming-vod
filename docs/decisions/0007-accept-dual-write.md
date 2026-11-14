# ADR 0007: Chấp nhận dual-write Postgres + Kafka ở MVP

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
`/complete` và `/retry` vừa đổi trạng thái trong Postgres vừa publish `video.uploaded`. Hai thao tác không nằm chung một transaction.

## Quyết định
Đặt `queued` trước rồi publish (chờ tối đa 5 giây). Publish lỗi thì trả video về trạng thái trước đó và trả `503` để admin gọi lại. Không dùng outbox pattern ở MVP.

## Hệ quả
- `Producer.Publish` tự chờ theo `ctx` (5 giây): `ProduceSync` của franz-go không trả về đúng hạn khi Kafka chết sau khi client đã kết nối, nên `/complete` từng treo thay vì trả `503`. Nếu ctx hết hạn trước, record có thể vẫn được gửi sau đó; consumer idempotent nên chấp nhận.
- Đặt trạng thái trước đảm bảo api không nhận `video.processing` khi DB còn ở trạng thái cũ.
- Nếu api chết đúng giữa hai bước, video có thể kẹt ở `queued` mà không có sự kiện; admin xử lý thủ công. Bản sau: outbox table + relay publish.
