# Vận hành local

## Lệnh

| Lệnh | Việc |
|---|---|
| `make up` / `make down` | Dựng (build lại image api/transcoder) / tắt toàn bộ stack |
| `make ps` / `make logs` | Trạng thái / log mọi container |
| `make logs-app` | Log của `api` và `transcoder` |
| `make seed-admin` | Tạo/đặt lại admin `ADMIN_EMAIL` / `ADMIN_PASSWORD` (trong `.env`) bên trong container api |
| `make e2e-m2` | E2E: đăng nhập admin, upload `tmp/sample.mp4` → đợi `ready` → kiểm tra HLS qua URL ký |
| `make e2e-m3` | E2E: 401/403, register/login/logout, `/play`, URL ký → 200, sửa chữ ký / hết hạn / traversal → 403 (chạy kèm `e2e-m2`) |
| `make sign-url ID=…` | In URL CDN đã ký (2 giờ) cho video `ID` (dòng đầu là URL, dòng sau là thời điểm hết hạn), dùng với dev player hoặc video từ `make transcode` |
| `make sample` | Sinh `tmp/sample.mp4` |
| `make build` | Build `bin/api`, `bin/transcoder` |
| `make run-api` / `make run-worker` | Chạy api / worker trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |

`make up` chạy `up -d --build` rồi `up -d --wait` cho các service chạy lâu. Các container one-shot (`minio-init`, `kafka-init`) thoát với mã 0 nên không dùng `--wait` cho chúng. `api` có healthcheck (`/healthz`) nên `--wait` đợi tới khi healthy; `transcoder` không có healthcheck nên `--wait` chỉ đợi container chạy. `api` tự chạy migration khi khởi động.

Xoá sạch dữ liệu: `docker compose -f deploy/docker-compose.yml down -v`.

## Cổng và giao diện

| Dịch vụ | Địa chỉ | Đăng nhập |
|---|---|---|
| api | http://localhost:8000 (`/healthz`) | cookie sau `POST /api/auth/login`; admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD` |
| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |
| Dev player | http://localhost:8088/dev/player.html?src={URL ký, đã URL-encode} | |
| MinIO API (presigned URL) | http://localhost:9000 | bucket `hls` đọc ẩn danh trong dev ([ADR 0002](decisions/0002-dev-hls-bucket-read.md)) |
| MinIO Console | http://localhost:9001 | `S3_ACCESS_KEY` / `S3_SECRET_KEY` trong `.env` |
| Redpanda Console (Kafka) | http://localhost:8080 | |
| ClickHouse HTTP | http://localhost:8123 | `streamfilm` / `POSTGRES_PASSWORD` |
| Postgres | localhost:5432 | `streamfilm` / `POSTGRES_PASSWORD` |
| Kafka từ máy host | localhost:29092 | |

## Biến môi trường mới (M2)

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `DATABASE_URL` | `postgres://streamfilm:streamfilm@localhost:5432/streamfilm?sslmode=disable` | Postgres |
| `KAFKA_BROKERS` | `localhost:29092` | Danh sách broker, phân tách bằng dấu phẩy |
| `S3_PUBLIC_ENDPOINT` | bằng `S3_ENDPOINT` | Host dùng để ký presigned URL; trong compose là `http://localhost:9000` vì api gọi MinIO qua `http://minio:9000` |
| `HTTP_ADDR` | `:8000` | Địa chỉ lắng nghe của api |
| `WORK_DIR` | thư mục tạm của OS | Nơi transcoder tải raw và ghi HLS tạm |
| `STALE_UPLOAD_AFTER` / `CLEANUP_INTERVAL` | `24h` / `1h` | Job dọn upload bỏ dở |

## Biến môi trường mới (M3)

`make env` (chạy trong `make up`) tự thêm các khoá còn thiếu từ `.env.example` vào `.env`.

| Biến | Mặc định (`.env.example`) | Ý nghĩa |
|---|---|---|
| `JWT_SECRET` | chuỗi dev | Khoá HS256, ≥ 32 ký tự; thiếu thì api không khởi động |
| `CDN_SIGNING_SECRET` | `dev-cdn-secret-change-me` | Secret ký URL, 16–128 ký tự `[A-Za-z0-9_-]`; api và nginx phải cùng giá trị |
| `COOKIE_SECURE` | `false` | Bật thuộc tính `Secure` của cookie khi chạy HTTPS |
| `ADMIN_EMAIL` / `ADMIN_PASSWORD` | `admin@streamfilm.local` / `admin-dev-password` | Dùng bởi `make seed-admin` và script e2e |
| `CDN_BASE_URL` | `http://localhost:8088` | Tiền tố của URL trong `/play` |

Các giá trị trong `.env.example` chỉ dành cho dev local và đã nằm trong repo: phải sinh secret và mật khẩu mới cho bất kỳ môi trường nào ngoài máy local. Đổi `CDN_SIGNING_SECRET` thì chạy lại `make up` để nginx và api cùng nhận giá trị mới; URL đã phát trước đó thành `403`.

## Theo dõi Kafka

- Redpanda Console → **Topics** → chọn topic → tab Messages: xem key (`video_id`) và JSON.
- **Consumer Groups**: `api` (đọc `video.processing|transcoded|failed`) và `transcoder` (đọc `video.uploaded`). Lag khác 0 kéo dài nghĩa là consumer đang kẹt; xem `make logs-app`.
- DLQ: topic `video.uploaded.dlq`. Mỗi message có `payload` gốc, `error`, `attempts`. Sau khi sửa nguyên nhân, gọi `POST /api/admin/videos/{id}/retry`.

## Truy vấn nhanh Postgres

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec postgres \
  psql -U streamfilm -c "select id, status, fail_reason, created_at from videos order by created_at desc limit 10"
```

## Image MinIO

Image `minio/minio` trên Docker Hub không còn pull được, nên stack dùng `cgr.dev/chainguard/minio` và `cgr.dev/chainguard/minio-client`, ghim theo digest. Xem [ADR 0003](decisions/0003-minio-image.md).

## Kiểm tra cache CDN

```bash
URL=$(make -s sign-url ID={video_id} 2>/dev/null | head -1)
curl -sI "${URL%master.m3u8}720p/seg_000.ts" | grep -i x-cache-status
```
Lần đầu `MISS`, các lần sau `HIT`.

## Sự cố thường gặp

| Triệu chứng | Cách xử lý |
|---|---|
| `make up` treo ở kafka | Xem `make logs`; xoá volume `kafkadata` nếu đổi cấu hình listener |
| `/complete` trả `503` | Kafka chưa sẵn sàng: `make ps`, đợi kafka healthy rồi gọi lại `/complete` |
| `/complete` trả `400 multipart upload incomplete` | Chưa `PUT` đủ part, part sai kích thước, tổng byte khác `size_bytes`, hoặc URL đã hết hạn (1 giờ): upload lại part đúng rồi gọi lại, hoặc tạo video mới |
| `PUT` presigned URL trả `403 SignatureDoesNotMatch` | URL được ký cho host khác host đang gọi; kiểm tra `S3_PUBLIC_ENDPOINT` |
| api thoát với `JWT_SECRET must be at least 32 characters` hoặc `CDN_SIGNING_SECRET must be …` | Sửa giá trị trong `.env` (`make env` chỉ thêm khoá thiếu, không ghi đè khoá đã có) |
| `/api/admin/*` trả `401` | Chưa đăng nhập hoặc cookie hết hạn (24 giờ): `POST /api/auth/login`; chưa có admin: `make seed-admin` |
| `/api/admin/*` trả `403` | Tài khoản là viewer; dùng admin của `make seed-admin` |
| URL ký trả `403` | Hết hạn 2 giờ (gọi lại `/play`), URL bị sửa, hoặc `CDN_SIGNING_SECRET` của api và nginx khác nhau (chạy lại `make up`) |
| `http://localhost:8088/hls/...` trả `404` | Đúng thiết kế từ M3: chỉ còn đường `/cdn/s/...` có chữ ký |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
| Video `failed` | Xem `fail_reason` qua `GET /api/admin/videos?status=failed` và message trong DLQ |
| `make run-api` báo cổng 8000 đã dùng | Dừng service `api` trong compose trước |
| Integration test bị skip | Bật Docker Desktop; kiểm tra `ffmpeg` trong `PATH`; không dùng `-short` |
