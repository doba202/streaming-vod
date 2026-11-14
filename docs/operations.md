# Vận hành local

## Lệnh

| Lệnh | Việc |
|---|---|
| `make up` / `make down` | Dựng (build lại image api/transcoder/nginx kèm web) / tắt toàn bộ stack |
| `make ps` / `make logs` | Trạng thái / log mọi container |
| `make logs-app` | Log của `api`, `transcoder`, `analytics` và `nginx` |
| `make seed-admin` | Tạo/đặt lại admin `ADMIN_EMAIL` / `ADMIN_PASSWORD` (trong `.env`) bên trong container api |
| `make e2e-m2` | E2E: đăng nhập admin, upload `tmp/sample.mp4` → đợi `ready` → kiểm tra HLS qua URL ký |
| `make e2e-m3` | E2E: 401/403, register/login/logout, `/play`, URL ký → 200, sửa chữ ký / hết hạn / traversal → 403 (chạy kèm `e2e-m2`) |
| `make e2e-m4` | E2E: SPA và fallback route qua nginx, `/api/` cùng origin (cookie), upload video tiêu đề tiếng Việt, tìm không dấu / có dấu / gõ sai / lọc thể loại, `/play` cùng origin |
| `make e2e-m5` | E2E: poster/sprite, phụ đề, sự kiện xem → "Xem tiếp", `playback.events` trong Kafka (`KAFKA_DOWN=1`: thử Kafka chết) |
| `make e2e-m6` | E2E: sự kiện xem và log api/transcoder vào ClickHouse, record hỏng bị bỏ qua, SQL mẫu có kết quả (`CH_DOWN=1`: dừng ClickHouse, analytics thử lại, không mất sự kiện) |
| `make analytics-sql [Q=tên]` | Chạy SQL mẫu `scripts/analytics/*.sql` trên ClickHouse (mặc định tất cả) |
| `make web-install` / `make web-test` / `make web-build` | `npm ci` / Vitest / build production cho `web/app` (cần Node 20.19+) |
| `make web-dev` | Vite dev server http://localhost:5173 (hot reload), proxy `/api` và `/cdn` sang nginx `:8088` (cần `make up`) |
| `make sign-url ID=…` | In URL CDN đã ký (2 giờ) cho video `ID` (dòng đầu là URL, dòng sau là thời điểm hết hạn), dùng với dev player hoặc video từ `make transcode` |
| `make sample` | Sinh `tmp/sample.mp4` |
| `make build` | Build `bin/api`, `bin/transcoder`, `bin/analytics` |
| `make run-api` / `make run-worker` / `make run-analytics` | Chạy api / worker / analytics trên host để dev (dừng service cùng tên trong compose trước: `docker compose -f deploy/docker-compose.yml --env-file .env stop api transcoder analytics`) |
| `make transcode FILE=… ID=…` | Transcode một file không qua Kafka (chế độ M1) |

`make up` chạy `up -d --build` rồi `up -d --wait` cho các service chạy lâu. Các container one-shot (`minio-init`, `kafka-init`) thoát với mã 0 nên không dùng `--wait` cho chúng. `api` có healthcheck (`/healthz`) nên `--wait` đợi tới khi healthy; `transcoder` không có healthcheck nên `--wait` chỉ đợi container chạy. `api` tự chạy migration khi khởi động.

Xoá sạch dữ liệu: `docker compose -f deploy/docker-compose.yml down -v`.

## Cổng và giao diện

| Dịch vụ | Địa chỉ | Đăng nhập |
|---|---|---|
| **Web người xem** | http://localhost:8088 | admin: `ADMIN_EMAIL` / `ADMIN_PASSWORD`, hoặc tự đăng ký viewer |
| api qua nginx | http://localhost:8088/api/… | cookie sau `POST /api/auth/login` |
| api trực tiếp | http://localhost:8000 (`/healthz`) | như trên (dùng cho script, debug) |
| nginx "CDN" | http://localhost:8088/cdn/s/{sig}/{exp}/hls/{id}/… | chỉ URL ký |
| Vite dev server | http://localhost:5173 (`make web-dev`) | như web người xem |
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

## Phát triển web (M4)

Mã nguồn ở `web/app` (React 18 + Vite + TypeScript, hls.js, react-router). Lần đầu: `make web-install`.

- **Sửa giao diện:** `make up` (backend) rồi `make web-dev`, mở http://localhost:5173. Vite proxy `/api` và `/cdn` sang nginx `:8088` nên cookie và URL ký hoạt động như bản thật.
- **Test:** `make web-test` (Vitest + Testing Library: debounce ô tìm kiếm, lưới phim, form đăng nhập, logic gọi lại `/play` của player). Player phát thật kiểm tra tay trên Chrome và Safari: mở `/watch/{id}`, đổi chất lượng, tua.
- **Bản chạy ở :8088** là bản build trong image `streamfilm-nginx:dev`; sau khi sửa `web/app` chạy lại `make up` để build lại.
- Thêm/sửa dependency: `cd web/app && npm install --save-exact <pkg>@<version>` và commit `package-lock.json` (Docker build dùng `npm ci`).

Kiểm tra player tự lấy lại URL khi hết hạn (bằng tay): đang xem thì đổi `CDN_SIGNING_SECRET` trong `.env` và `make up` (URL cũ thành `403`) — player gọi lại `/play` và phát tiếp từ vị trí cũ.

## Phụ đề, poster và xem tiếp (M5)

- Upload phụ đề: xem `docs/api.md` (`POST /api/admin/videos/{id}/subtitles`). File phải là UTF-8; SRT xuất từ phần mềm cũ thường là Windows-1258/1252 → chuyển trước: `iconv -f WINDOWS-1258 -t UTF-8 phim.srt > phim.utf8.srt`.
- Xem tiến độ của mọi user:

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec postgres psql -U streamfilm -c \
  "select u.email, w.video_id, w.position_s, w.duration_s, w.updated_at from watch_progress w join users u on u.id = w.user_id order by w.updated_at desc limit 10"
```

- Đọc `playback.events` thô (Redpanda Console → Topics → `playback.events` cũng xem được; từ M6 dữ liệu đã nằm trong ClickHouse, xem mục dưới):

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka:9092 --topic playback.events --from-beginning --timeout-ms 5000
```

- Thử Kafka chết: `make e2e-m5 KAFKA_DOWN=1` (dừng kafka, gửi sự kiện — vẫn `204`, tiến độ vẫn ghi — rồi bật lại). Log api có `playback event dropped`.

## Analytics và log (M6)

`make up` chạy thêm `analytics` (không có healthcheck; `--wait` chỉ đợi container chạy). Nó tạo bảng ClickHouse khi khởi động rồi đọc `playback.events` và `app.logs` từ đầu (lần đầu đọc cả sự kiện từ M5).

Biến môi trường (M6, `make env` tự thêm vào `.env`):

| Biến | Mặc định | Ý nghĩa |
|---|---|---|
| `CLICKHOUSE_URL` | `http://localhost:8123` | HTTP interface; trong compose là `http://clickhouse:8123` |
| `CLICKHOUSE_DB` / `CLICKHOUSE_USER` | `streamfilm` / `streamfilm` | |
| `CLICKHOUSE_PASSWORD` | `streamfilm` | Phải bằng `POSTGRES_PASSWORD` (service `clickhouse` lấy mật khẩu từ biến đó) |

SQL mẫu (`scripts/analytics/`, mỗi file một câu, dòng đầu là mô tả):

| File | Câu hỏi |
|---|---|
| `top_videos.sql` | Top 10 phim theo lượt play, kèm số người xem khác nhau |
| `completion_rate.sql` | Tỉ lệ xem hết = người có `ended` / người có `play`, theo phim |
| `buffering_by_bitrate.sql` | Số lần, tổng và trung bình thời gian buffering theo `bitrate_kbps` |
| `logs_by_service.sql` | Số dòng log theo service và level trong 24 giờ |

```bash
make analytics-sql                 # chạy tất cả, bảng PrettyCompact
make analytics-sql Q=top_videos    # một câu
```

Truy vấn tay (Play UI http://localhost:8123/play, user `streamfilm`, mật khẩu `POSTGRES_PASSWORD`; hoặc `clickhouse-client` trong container):

```bash
docker compose -f deploy/docker-compose.yml --env-file .env exec clickhouse clickhouse-client \
  --user streamfilm --password streamfilm -d streamfilm -q \
  "SELECT ts, service, level, msg, request_id FROM app_logs WHERE level IN ('WARN','ERROR') ORDER BY ts DESC LIMIT 20"
```

- Lần theo một request: lấy `request_id` từ dòng `http request` (nginx sinh `$request_id`), rồi `SELECT ts, msg, attrs FROM app_logs WHERE request_id = '…' ORDER BY ts`.
- Mọi log của một video: `SELECT ts, service, msg FROM app_logs WHERE video_id = '…' ORDER BY ts` (dòng `http request` của route `/{id}`, log transcoder, log lỗi của handler).
- Log của chính `analytics` chỉ có ở `make logs-app` (stdout), không có trong `app_logs` ([ADR 0018](decisions/0018-app-logs-async-sink.md)).
- Thử ClickHouse chết: `make e2e-m6 CH_DOWN=1`. Trong lúc ClickHouse tắt, log analytics có `clickhouse insert failed, retrying` (giãn cách tới 30 giây), lag của group `analytics-*` tăng; bật lại thì lô được ghi và lag về 0.

## Theo dõi Kafka

- Redpanda Console → **Topics** → chọn topic → tab Messages: xem key (`video_id`) và JSON.
- **Consumer Groups**: `api` (đọc `video.processing|transcoded|failed`), `transcoder` (đọc `video.uploaded`), `analytics-playback` (đọc `playback.events`) và `analytics-logs` (đọc `app.logs`). Lag khác 0 kéo dài nghĩa là consumer đang kẹt; xem `make logs-app`.
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
| `http://localhost:8088/api/...` trả `502` | api chưa chạy hoặc đang khởi động lại: `make ps`, `make logs-app` |
| Web hiện bản cũ sau khi sửa `web/app` | Bản ở `:8088` là image build sẵn: chạy lại `make up`, hoặc dùng `make web-dev` |
| Web báo "Không tải được video" | `/play` lỗi (video chưa `ready`, hết phiên) hoặc URL ký liên tục `403` (`CDN_SIGNING_SECRET` của api và nginx khác nhau): xem tab Network và `make logs-app` |
| Thẻ phim hiện khối màu thay vì poster | Video transcode trước M5 (không có `poster_key`): transcode lại bằng cách upload lại; hoặc trang mở quá 1–2 giờ nên URL poster đã hết hạn (tải lại trang) |
| Không có ảnh xem trước khi tua | `/play` không có `thumbnails_url` (video trước M5) hoặc tải `thumbnails.vtt` lỗi: xem tab Network |
| Upload phụ đề `400 subtitle: file must be UTF-8 text` | Chuyển mã file sang UTF-8 (xem mục M5 ở trên) |
| Phụ đề không hiện | Chọn phụ đề trong menu CC của player; kiểm tra `/play` có `subtitles` và URL `.vtt` trả `200 text/vtt` |
| Hàng "Xem tiếp" trống | Mới xem < 5% hoặc đã ≥ 95% thời lượng, hoặc chưa có heartbeat (cần phát ≥ 10 giây, hoặc pause) |
| Tìm "tro choi" không ra video vừa upload | Video chưa `ready`, hoặc api chưa chạy migration `0003_search` (xem `schema_migrations`) |
| `make analytics-sql` báo `Authentication failed` | `CLICKHOUSE_PASSWORD` trong `.env` khác `POSTGRES_PASSWORD` (mật khẩu của service `clickhouse`) |
| `playback_events` / `app_logs` không có row mới | `make ps` (analytics chạy chưa?), `make logs-app` tìm `clickhouse insert failed`; Redpanda Console → Consumer Groups `analytics-*` xem lag |
| Log analytics có `skip invalid record` | Record trong topic không đúng schema (JSON hỏng, id không phải UUID): đã bỏ qua và commit, không cần xử lý; `make e2e-m6` cố ý ghi một record như vậy |
| Video kẹt ở `queued` | `transcoder` không chạy hoặc lag: `make ps`, `make logs-app`, tab Consumer Groups |
| Video `failed` | Xem `fail_reason` qua `GET /api/admin/videos?status=failed` và message trong DLQ |
| `make run-api` báo cổng 8000 đã dùng | Dừng service `api` trong compose trước |
| Integration test bị skip | Bật Docker Desktop; kiểm tra `ffmpeg` trong `PATH`; không dùng `-short` |
