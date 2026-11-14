# ADR 0018: Log vào `app.logs` qua sink Kafka không chặn; analytics không tự log vào đó

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §7: log `slog` JSON, luôn có `service`, `request_id`, và `video_id` nếu có; ghi ra stdout và đồng thời gửi vào `app.logs` qua một `slog.Handler` bất đồng bộ, bỏ log khi buffer đầy để không chặn request. `analytics` là consumer của `app.logs`.

## Quyết định
- `logging.New(service, stdout, sink)`: một `slog.JSONHandler` ghi vào `io.MultiWriter(stdout, sink)` — `JSONHandler` gọi `Write` đúng một lần cho mỗi bản ghi, nên mỗi `Write` là một dòng hoàn chỉnh và hai đích nhận cùng một dòng. Bọc bởi `contextHandler`: nếu ctx có request id của chi và bản ghi chưa có `request_id` thì thêm vào. Handler của api gọi `…Context(r.Context(), …)`; `httpx.RequestLogger` thêm `video_id` khi route có `{id}` là UUID.
- `logging.KafkaSink.Write` chép dòng log (bỏ `\n`) thành một record `app.logs` không key và gọi `kgo.Client.TryProduce` — buffer của franz-go chính là hàng đợi có giới hạn: client riêng với `MaxBufferedRecords(4096)`, `RecordDeliveryTimeout(5s)`, acks leader, không idempotent, linger 50 ms. Đầy thì promise trả `ErrMaxBuffered` ngay; gửi lỗi (Kafka chết) cũng vào promise; cả hai chỉ tăng `Dropped`. `Write` luôn trả thành công nên stdout không bị ảnh hưởng. Khi tắt: `Flush` tối đa 2 giây, `Close`, in số dòng đã bỏ ra stdout.
- Vòng phản hồi: `analytics` dựng logger **không** có sink (chỉ stdout). Nếu nó tee vào `app.logs`, mỗi lần ClickHouse lỗi sẽ sinh log lỗi → thêm record `app.logs` → thêm lô lỗi. franz-go không log gì khi không cấu hình `WithLogger`, nên sink không tự sinh log.
- Chỉ `api` (lệnh server) và `transcoder worker` tee vào Kafka; lệnh phụ (`seed-admin`, `sign`, `transcoder file`) chỉ log ra stdout.

## Phương án đã loại
- Channel + goroutine riêng trước producer: thêm một tầng buffer nữa mà `TryProduce` đã cho đúng ngữ nghĩa "đầy thì bỏ, không chặn".
- Dùng `events.Producer.Publish`: chờ từng record (tới 10 giây khi broker chết) — chặn request.
- Analytics tee log nhưng lọc `service = "analytics"` khi ghi: vẫn tốn Kafka và vẫn phải lọc; không có lợi gì so với không gửi.

## Hệ quả
- Log của `analytics` chỉ xem được bằng `make logs-app`, không có trong `app_logs`.
- Kafka chết hoặc quá tải: `app_logs` thiếu dòng (stdout vẫn đủ). Số dòng bỏ in ra khi service tắt.
- Log gọi không kèm ctx (job nền, consumer) không có `request_id` — đúng, vì không thuộc request nào; log của transcoder có `video_id`.
