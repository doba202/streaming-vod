# Giới hạn đã biết (MVP)

Streamfilm là dự án học system design chạy local. Các điểm dưới đây được biết và **chấp nhận** ở MVP; mỗi mục ghi hệ quả và hướng làm nếu đi tiếp. Mục có ADR thì xem ADR để biết lý do.

## Đánh đổi thiết kế

| Giới hạn | Hệ quả | Hướng làm tiếp |
|---|---|---|
| Bucket `hls` đọc ẩn danh trên MinIO dev ([ADR 0002](decisions/0002-dev-hls-bucket-read.md)) | Ai tới được `localhost:9000` thì đọc HLS không cần chữ ký; lớp ký chỉ có hiệu lực qua nginx | Bỏ policy `download`, nginx ký request tới MinIO (SigV4) hoặc dùng mạng riêng |
| JWT không trạng thái ([ADR 0010](decisions/0010-jwt-cookie-session.md)) | Logout chỉ xoá cookie; token đã sao chép vẫn dùng được tới khi hết hạn (24 giờ); đổi role/xoá user có hiệu lực khi token hết hạn | Danh sách thu hồi (`jti`) hoặc session lưu server |
| Dual-write Postgres → Kafka ([ADR 0007](decisions/0007-accept-dual-write.md)) | Publish `video.uploaded` lỗi sau khi đã đặt `queued` được bù bằng trả trạng thái; publish "ma" (timeout nhưng thực ra đã gửi) có thể làm transcode thừa một lần | Transactional outbox |
| Không DRM, không mã hoá segment | Ai có URL ký còn hạn (2 giờ) tải được toàn bộ segment | HLS AES-128 / SAMPLE-AES, Widevine/FairPlay |
| URL ký theo thư mục `/hls/{id}/` ([ADR 0009](decisions/0009-signed-url-secure-link-md5.md)) | Một URL ký mở được mọi file của video đó (playlist, segment, phụ đề, sprite) tới khi hết hạn; MD5 là yêu cầu của `secure_link_md5` | CDN thật với token HMAC-SHA256 |
| Cache nginx 7 ngày, key `{id}/{file}` | Transcode lại (retry) hoặc xoá video: segment/playlist cũ còn trong cache tới khi hết hạn hoặc bị đẩy ra; người còn URL ký cũ vẫn tải được bản cũ | Thêm phiên bản vào đường dẫn HLS (`hls/{id}/v{n}/`) hoặc purge cache |
| Sự kiện xem best-effort ([ADR 0015](decisions/0015-playback-events-best-effort.md)) | Kafka chết thì sự kiện bị bỏ (request vẫn `204`); `completion_rate` chỉ tính người có cả `play` và `ended` | Buffer phía client hoặc outbox |
| Analytics at-least-once ([ADR 0017](decisions/0017-analytics-at-least-once.md)) | Có thể có row trùng trong ClickHouse khi analytics chết giữa INSERT và commit; lỗi 4xx vĩnh viễn từ ClickHouse làm pipeline thử lại mãi (chặn partition); record hỏng bị bỏ, không có dead-letter | `ReplacingMergeTree` với id sự kiện; phân biệt lỗi vĩnh viễn; topic DLQ cho analytics |
| Log vào `app.logs` bỏ khi đầy ([ADR 0018](decisions/0018-app-logs-async-sink.md)) | Kafka chậm/chết thì `app_logs` thiếu dòng (stdout vẫn đủ) | Ghi file + agent (Vector/Fluent Bit) |
| Xoá video từ chối khi `queued`/`processing` ([ADR 0020](decisions/0020-video-delete-and-edit.md)) | Video kẹt `queued` không xoá được qua API; xoá tay bằng SQL | Trạng thái `deleting` + dọn khi sự kiện đến muộn |
| Upload từ trình duyệt cần tới MinIO trực tiếp ([ADR 0019](decisions/0019-admin-browser-upload.md)) | Origin mở trang admin phải nằm trong `MINIO_CORS_ALLOW_ORIGIN`; presigned URL hết hạn sau 1 giờ, chưa có re-presign cho file rất lớn trên mạng chậm | Endpoint cấp lại URL cho part chưa xong |
| Mọi thứ một node | Kafka 1 broker (RF 1), Postgres/MinIO/ClickHouse đơn, transcoder tối đa 3 worker (3 partition) | Cluster thật, thêm partition |
| Secret dev trong `.env.example` | `JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin, MinIO, Postgres (ClickHouse dùng chung mật khẩu Postgres) nằm trong repo | Sinh secret khi `make env`, secret manager |

## Nhận xét review được chấp nhận (không sửa ở MVP)

Gom từ review từng task (M0–M6). Các mục đã sửa ở M7: header bảo mật nginx, wait strategy `/minio/health/ready` cho MinIO testcontainer, `stop_grace_period` 25 giây cho analytics, `completion_rate` ≤ 1, bỏ `COPY migrations` thừa trong Dockerfile transcoder/analytics, JWT kiểm tra `iat`, bcrypt dummy hash tính sẵn lúc khởi động, `ErrorBoundary` reset khi đổi route và dọn listener trong `afterEach`, giới hạn độ dài title/mô tả/thể loại và kiểm tra năm khi tạo video, id video dạng chữ hoa được chuẩn hoá ở complete/retry/delete, `S3_PUBLIC_ENDPOINT` của api đọc từ `.env`.

**API và dữ liệu**
- `httpx.DecodeJSON`: body quá 1 MiB trả `400` (không phải `413`); vài handler cũ trả nguyên thông báo lỗi của decoder; dữ liệu thừa sau object JSON bị bỏ qua.
- `register` trả `409` khi email đã tồn tại (theo spec) — lộ email đã đăng ký.
- Chưa test đồng thời cho chuyển trạng thái; `RevertQueued` không trả số dòng; `fail_reason` cũ vẫn còn khi video `queued` lại sau retry (UI chỉ hiện khi `failed`).
- Presigned URL mọi part cùng TTL 1 giờ; cleaner xoá dòng DB trước khi abort multipart (crash giữa chừng → multipart mồ côi, MinIO tự dọn theo lifecycle nếu cấu hình).
- Upload phụ đề cho video chưa `ready` được chấp nhận ([ADR 0014](decisions/0014-subtitles-srt-to-vtt.md)); upload phụ đề đúng lúc video bị xoá có thể để lại `hls/{id}/subs/*.vtt` mồ côi.
- SRT: hai cue không có dòng trống ở giữa bị gộp; mili-giây lỗi bị bỏ qua; `NOTE` chứa `-->` lọt qua kiểm tra VTT.
- `SaveProgress` last-write-wins; "Xem tiếp" lấy `LIMIT 20` trước khi lọc video `ready`; không rate limit sự kiện xem.
- Signing: regex id không bắt buộc UUID (đường dẫn đã chặn traversal); `StablePath` không kiểm tra TTL tối thiểu.
- `PATCH` chuẩn hoá lại cả các field không gửi: video cũ có dữ liệu ngoài giới hạn hiện tại (tiêu đề > 200 ký tự, thể loại trùng, năm ngoài 1888–2100) bị sửa hoặc bị `400` khi chỉ đổi một field khác.
- JWT kiểm tra `iat` không có độ lệch đồng hồ cho phép (giả định một instance api, cùng đồng hồ); đồng hồ lệch thì token mới có thể bị từ chối.
- Xoá video `uploading` rồi `video.uploaded` "ma" ([ADR 0007](decisions/0007-accept-dual-write.md)) tới muộn: transcode vẫn chạy và ghi `hls/{id}/` mồ côi (không còn dòng DB; job dọn chỉ xoá `raw/`).
- `Create` bị timeout mơ hồ có thể để lại dòng `uploading` mồ côi (dọn sau 24 giờ); `pg_advisory_unlock` sau migration không kiểm tra lỗi; message `video.uploaded` thiếu `video_id` vào DLQ với key rỗng.
- Repo `users` tin caller đã chuẩn hoá email; `scanUser` lặp ở vài chỗ; `catalog.Handler` panic nếu quên gắn `Progress`; `Host` gửi tới api qua nginx không kèm cổng.

**Transcoder và Kafka**
- Publish `video.transcoded` lỗi → làm lại toàn bộ transcode (tốn nhưng đúng); `RebalanceTimeout` 30 phút là trần cứng cho một job.
- Runner chỉ kiểm số tấm sprite ≥ kỳ vọng (không kiểm số frame, poster tồn tại); preview lệch 1–2 giây ở ô cuối với fps lẻ; `ParseProbe` nhận stream ảnh bìa (height 0).
- `Download` để lại file dở khi lỗi và bỏ qua lỗi `Close`; `EnsureBucket` coi `BucketAlreadyExists` là thành công; cắt chuỗi (`truncate`, `msg[:500]`) có thể cắt giữa ký tự UTF-8.
- Base image ghim tag, không ghim digest (trừ MinIO).
- ClickHouse client đọc body lỗi không giới hạn; tên bảng trong `Insert` không validate (chỉ dùng hằng); fetch lỗi lặp không backoff.

**Web**
- Player: listener `loadedmetadata` (Safari) không gỡ; `resume_at` mất nếu manifest đầu tiên `403`; seek > 1 giây reset bộ đếm khôi phục media; tải lại `/watch` phát thêm `play`/`seek` (nhiễu analytics); chỉ flush sự kiện khi `pagehide` (chưa dùng `visibilitychange`).
- Preview sprite hết hạn sau 2 giờ không tự làm mới; tràn mép, không hỗ trợ cảm ứng.
- Form auth đếm byte nhưng thông báo theo "ký tự"; `SearchBox` `autoFocus`; lỗi `/api/genres` bị nuốt; `api.ts` để lộ `TypeError`/`SyntaxError` thô khi mạng lỗi.
- Trang admin: xoá video dùng `window.confirm` của trình duyệt (không phải hộp thoại của app); thanh tiến độ upload có thể lùi lại khi một part được thử lại (byte của part đó tính lại từ 0).
- Wiring `Player` và trang admin end-to-end trong trình duyệt chưa có test tự động (kiểm tra tay, xem [operations](operations.md#trang-quản-trị-m7)).
- `/dev/player.html` tải hls.js từ jsdelivr (cần internet).

**Script và công cụ**
- `make transcode` không quote `FILE`/`ID`; `seed-admin -password` lộ trong `ps` (dùng biến môi trường thay thế).
- `E2E_KAFKA_DOWN=1` khi không có `COMPOSE` bị bỏ qua im lặng; e2e-m6 bước SQL chỉ kiểm kết quả không rỗng.
- `go.mod` khai báo `go 1.27.4` (cần Go mới). `make down`/`logs`/`ps` cần `.env` (dùng `--env-file`); `make up` liệt kê cứng các service.
- Cấu hình mặc định trong code dùng credential dev; `config_test` không xoá hết biến môi trường liên quan (`S3_BUCKET_RAW`, `FFPROBE_PATH`).
- Buffering trước lần `play` đầu và khi seek vẫn tính là buffering; log `request_id` có thể lồng trong nhóm khi dùng `WithGroup`; `io.MultiWriter` của logger dừng nếu stdout lỗi; so sánh `MaxUint32` với `int` sai trên máy 32-bit.
- Test: `TestRunnerRejectsNonVideo` pass nếu thiếu `ffprobe`; vài test bỏ qua lỗi `MkdirAll`/`WriteFile`; chưa test phân trang `DeletePrefix`, `NewConsumerWithOpts`, `run()` của api, số record bị bỏ qua của analytics.
