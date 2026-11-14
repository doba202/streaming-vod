# ADR 0015: Sự kiện xem — ghi tiến độ đồng bộ, publish Kafka best-effort ở nền

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.4: player gửi `POST /api/playback/events` mỗi 10 giây và khi play/pause/seek/ended/buffering; api upsert `watch_progress` chỉ với heartbeat/pause/ended rồi publish `playback.events` (key `user_id`). Spec §7: Kafka không khả dụng thì ghi log và bỏ sự kiện, không làm hỏng việc xem. `events.Producer.Publish` chờ kết quả (có thể tới 10 giây khi broker chết).

## Quyết định
- Handler ghi `watch_progress` đồng bộ (nguồn của "Xem tiếp" và `resume_at`, phải bền), rồi giao sự kiện cho `playback.BestEffort` và trả `204` ngay.
- `BestEffort.Send` không chặn: mỗi sự kiện publish trong goroutine riêng với timeout 2 giây; semaphore 256 publish đang chờ — đầy thì bỏ sự kiện ngay (log `playback event dropped`). Bộ nhớ và số goroutine có giới hạn khi Kafka chết. Khi tắt api, `Wait()` trước khi đóng producer.
- Chỉ video `ready` nhận sự kiện (`404` cho video khác) để analytics không có rác; `position_s` làm tròn xuống giây và kẹp theo thời lượng.
- "Xem tiếp": `5% < position/duration < 95%` (SQL trong `playback.Repo.ContinueWatching` và hàm Go `InContinueRow` cùng ngưỡng, test biên 5/6/94/95), `updated_at` mới nhất trước, tối đa 20, index `(user_id, updated_at DESC)`. `duration_s` chép vào `watch_progress` khi ghi để lọc không cần join; `ready` là trạng thái cuối nên không cần join `videos` để kiểm tra lại. `resume_at` = vị trí đã lưu, `0` nếu ≥ 95%.
- Catalog (bên dùng) khai báo `ProgressSource`; `playback` import `catalog`, không ngược lại.
- Web: `PlaybackReporter` thuần (test bằng fake timer); gửi bằng `fetch` `keepalive` (không dùng `sendBeacon` để một đường code cho cả lúc đang xem lẫn lúc đóng tab, và đặt được `Content-Type: application/json`); lỗi/401 bị bỏ qua, không đẩy người xem về trang đăng nhập giữa phim.

## Phương án đã loại
- Publish đồng bộ với timeout ngắn: Kafka chết làm mỗi heartbeat chậm thêm timeout.
- Outbox trong Postgres: đúng hơn (không mất sự kiện) nhưng ngoài phạm vi MVP (spec §1, ADR 0007). Mất một phần sự kiện analytics khi Kafka chết là chấp nhận được; tiến độ xem không mất vì nằm ở Postgres.

## Hệ quả
- `SaveProgress` là last-write-wins theo thiết kế: heartbeat/pause/ended cuối cùng thắng, nên vị trí đã lưu có thể lùi lại sau khi người xem tua ngược (hoặc hai tab cùng xem). Không dùng `GREATEST` vì xem lại từ đầu phải đặt lại "Xem tiếp".
- Sự kiện có thể mất khi Kafka chết hoặc api tắt giữa chừng (tối đa 2 giây chờ); số bị bỏ ghi vào log.
- Thứ tự sự kiện của một user trong Kafka gần đúng (publish song song trong goroutine); analytics (M6) sắp theo `ts`.
- Mỗi heartbeat là một upsert Postgres — đủ cho MVP; khi tải lớn có thể gom ghi theo lô hoặc dùng Redis.
