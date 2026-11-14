# ADR 0017: Analytics — lô theo partition, commit sau INSERT (at-least-once), bỏ qua record hỏng

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.4: ghi theo lô mỗi 1 giây hoặc 1000 bản ghi. Spec §5: `playback_events` và `app_logs` là `MergeTree`. Sự kiện xem không có id riêng nên không có khoá tự nhiên để khử trùng. Một row sai kiểu (ví dụ `user_id` không phải UUID) làm ClickHouse từ chối cả lô. `events.Consumer` (ADR 0006) commit từng record và thử lại record lỗi mãi — hợp với lệnh transcode, không hợp với ghi lô.

## Quyết định
- `analytics.Pipeline`: mỗi topic một client franz-go, một consumer group (`analytics-playback`, `analytics-logs`), `DisableAutoCommit` + `BlockRebalanceOnPoll`. Poll tới 1000 − (số record đang gom); lô được ghi khi đủ 1000 record hoặc 1 giây sau record đầu tiên (poll có deadline), và khi tắt (tối đa 10 giây).
- Offset của **mọi** record trong lô được commit **sau khi** INSERT thành công. Rebalance bị chặn từ lúc poll tới lúc commit, nên offset commit luôn thuộc partition consumer còn giữ.
- ClickHouse lỗi → thử lại cùng lô mãi (1s, 2s, 4s … tối đa 30s), không bỏ dữ liệu; huỷ ctx giữa chừng (SIGTERM lúc đang INSERT hoặc chờ backoff) thì thử đúng một lần nữa với `context.WithoutCancel` và `ShutdownTimeout` (10 giây), cùng lô và cùng dedup token; chỉ commit khi lần đó thành công, không thì không commit và lần chạy sau đọc lại.
- Mỗi lô mang `insert_deduplication_token` = bảng + khoảng offset từng partition (`playback_events/playback.events/0:10-25/…`), bảng có `non_replicated_deduplication_window = 1000`: INSERT thực ra đã thành công nhưng trả lỗi (timeout) thì lần gửi lại bị ClickHouse bỏ qua (integration test kiểm tra).
- Record hỏng (`ErrInvalid`: JSON sai, thiếu `ts`/`type`, id không phải UUID, số âm/ngoài `UInt32`) bị bỏ qua, log `skip invalid record`, đếm `Skipped`, vẫn commit cùng lô.
- Giữ `MergeTree` như spec, chấp nhận trùng.
- **Lỗi vĩnh viễn của ClickHouse** (ví dụ HTTP 4xx do schema không khớp) cũng bị thử lại mãi như lỗi tạm thời, nên làm kẹt partition (pipeline) đó: lô không bao giờ được ghi và offset không tiến. Chấp nhận ở MVP vì không phân biệt được lỗi tạm thời với lỗi vĩnh viễn một cách đáng tin; phát hiện qua log `clickhouse insert failed, retrying` lặp lại (kèm thông điệp ClickHouse) và lag của group `analytics-*`.
- **Record hỏng bị bỏ có chủ đích:** record không hợp lệ được bỏ qua và commit (đếm `Skipped`, log `skip invalid record` kèm topic/partition/offset), tức là dữ liệu đó bị loại hẳn. Chưa có dead-letter topic; nội dung gốc vẫn còn trong Kafka tới khi hết retention nên có thể đọc lại bằng offset trong log.

## Phương án đã loại
- `ReplacingMergeTree`: cần khoá sắp xếp định danh duy nhất một sự kiện (không có event id; `(video_id, ts)` có thể trùng thật giữa hai người xem), và chỉ khử trùng khi merge (truy vấn phải `FINAL`).
- Thêm `event_id` vào `playback.events`: đổi schema sự kiện của M5 chỉ để khử trùng — để bản sau.
- Đưa record hỏng vào DLQ: thêm topic và công cụ cho dữ liệu phân tích ít giá trị; log + đếm là đủ cho MVP.
- Một consumer group cho cả hai topic: ClickHouse lỗi ở một bảng sẽ giữ offset của bảng kia.

## Hệ quả
- Trùng vẫn có thể xảy ra khi analytics chết giữa INSERT và commit, hoặc rebalance làm ranh giới lô khác đi (token khác). Đếm lượt xem có thể lệch nhẹ; `uniqExact(user_id)` trong SQL mẫu không bị ảnh hưởng.
- ClickHouse chết lâu: lô hiện tại giữ trong bộ nhớ (≤ 1000 record mỗi pipeline), phần còn lại nằm trong Kafka (lag tăng). Rebalance bị chặn tới khi ghi được — với một instance analytics thì không sao; chạy nhiều instance cần xem lại (`OnPartitionsRevoked`).
- Thứ tự giữa các partition không được giữ trong ClickHouse; truy vấn sắp theo `ts`.
