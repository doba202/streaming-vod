# Streamfilm VOD MVP — Design Spec

- Ngày: 2026-11-14
- Trạng thái: Chờ review
- Mục tiêu: dự án **học system design**. Dựng một nền tảng xem phim theo yêu cầu (VOD) tối giản nhưng đủ các khối chính của hệ thống streaming thật, chạy hoàn toàn local bằng Docker.
- Tham khảo: [Hygraph — Architecture for a video streaming service](https://hygraph.com/blog/video-streaming-architecture)

## 1. Phạm vi

### Trong phạm vi

1. Admin upload video (file lớn, upload thẳng lên storage bằng presigned URL).
2. Transcode sang HLS nhiều độ phân giải (adaptive bitrate), sinh poster và sprite thumbnail.
3. Lưu trữ trên storage tương thích S3 (MinIO local).
4. Phân phối qua lớp "CDN" giả lập (nginx) có kiểm tra signed URL.
5. Đăng ký, đăng nhập, phân quyền `admin` / `viewer`.
6. Catalog phim, trang chi tiết, tìm kiếm tiếng Việt có dấu và không dấu.
7. Xem tiếp (continue watching).
8. Phụ đề (`.srt` / `.vtt`), nhiều ngôn ngữ.
9. Kafka tự host làm event bus. Sự kiện xem phim và log ứng dụng được lưu vào ClickHouse để phân tích.

### Ngoài phạm vi (để bản sau)

DRM, thanh toán và gói cước, app mobile/TV, hệ gợi ý, multi-CDN, outbox pattern, live streaming, deploy cloud thật (R2 + Bunny/Cloudflare).

## 2. Công nghệ

| Lớp | Lựa chọn |
|---|---|
| Backend | Go (1.23+), `net/http` + `chi`, `pgx`, `aws-sdk-go-v2` (S3), `franz-go` (Kafka), `slog` |
| Xử lý video | FFmpeg / ffprobe (chạy trong container transcoder) |
| Frontend | React + Vite + TypeScript, `hls.js` |
| Database | PostgreSQL 16 (extensions `unaccent`, `pg_trgm`) |
| Object storage | MinIO (API S3, thay được bằng R2/S3 bằng cấu hình) |
| Event bus | Apache Kafka 1 node, chế độ KRaft, kèm Redpanda Console |
| "CDN" | nginx với `secure_link` và `proxy_cache` |
| Analytics | ClickHouse |
| Môi trường | Docker Compose, Makefile |

## 3. Kiến trúc

Chọn **modular monolith + worker qua Kafka**: 3 chương trình Go trong một repo, các module bên trong `api` có ranh giới rõ để sau này tách ra microservice được.

```
                         ┌──────────── docker compose ─────────────┐
 React+Vite (web) ──────►│ api (Go) ──► Postgres
   hls.js player         │   │  └────► MinIO  (raw/, hls/)
        │                │   └─ Kafka (KRaft)
        │                │        ├─► transcoder (Go + FFmpeg) ──► MinIO hls/
        │                │        └─► analytics (Go) ──► ClickHouse
        └── segments ───►│ nginx "CDN" (secure_link + cache) ──► MinIO hls/
                         └──────────────────────────────────────────┘
```

### 3.1 Thành phần

| Thành phần | Trách nhiệm |
|---|---|
| `cmd/api` | HTTP API: auth, catalog, tìm kiếm, upload (cấp presigned URL), cấp signed URL phát, nhận sự kiện xem. Consume `video.transcoded` / `video.failed` để cập nhật trạng thái video |
| `cmd/transcoder` | Consume `video.uploaded`, chạy FFmpeg, upload HLS lên MinIO, publish kết quả |
| `cmd/analytics` | Consume `playback.events` và `app.logs`, ghi theo lô vào ClickHouse |
| `web/` | SPA React: trang người xem và trang admin |
| nginx | Kiểm tra chữ ký URL, cache segment, proxy tới MinIO bucket `hls` |

### 3.2 Cấu trúc repo dự kiến

```
streamfilm/
  cmd/{api,transcoder,analytics}/main.go
  internal/
    auth/        # đăng ký, đăng nhập, JWT, middleware phân quyền
    catalog/     # video, renditions, subtitles, tìm kiếm
    upload/      # presigned URL, complete
    playback/    # ký URL, watch_progress, nhận sự kiện
    transcode/   # ladder, dựng lệnh FFmpeg, chạy job
    events/      # schema sự kiện, producer/consumer Kafka
    storage/     # wrapper S3
    signing/     # HMAC ký/kiểm tra URL (dùng chung với cấu hình nginx)
    subtitle/    # chuyển SRT → VTT
  migrations/    # SQL Postgres
  deploy/        # docker-compose.yml, nginx.conf, clickhouse init
  web/           # React + Vite
  scripts/       # e2e, sinh video mẫu
  Makefile
```

### 3.3 Kafka topics

| Topic | Producer | Consumer | Payload chính |
|---|---|---|---|
| `video.uploaded` | api | transcoder | `video_id`, `raw_key` |
| `video.uploaded.dlq` | transcoder | (admin xem) | payload gốc + `error`, `attempts` |
| `video.processing` | transcoder | api | `video_id` |
| `video.transcoded` | transcoder | api | `video_id`, `duration_s`, `renditions[]`, `poster_key`, `sprite_key` |
| `video.failed` | transcoder | api | `video_id`, `reason` |
| `playback.events` | api | analytics | `ts`, `user_id`, `video_id`, `type`, `position_s`, `bitrate_kbps`, `buffering_ms` |
| `app.logs` | mọi service | analytics | log JSON từ `slog` |

Key message là `video_id` (với `playback.events` là `user_id`), nhờ vậy các sự kiện của cùng một video hoặc user giữ đúng thứ tự.

## 4. Luồng dữ liệu

### 4.1 Upload và transcode

1. Admin gửi `POST /api/admin/videos` kèm metadata. API tạo bản ghi `videos` với `status=uploading`, trả về presigned multipart upload URL cho key `raw/{id}/source.{ext}`.
2. Trình duyệt upload thẳng lên MinIO, file không đi qua API.
3. Admin gửi `POST /api/admin/videos/{id}/complete`. API kiểm tra object đã tồn tại, đặt `status=queued`, publish `video.uploaded`.
4. Transcoder nhận sự kiện:
   - Publish `video.processing`. API nhận và đặt `status=processing`. Transcoder không bao giờ ghi trực tiếp vào Postgres.
   - Tải file raw về thư mục tạm, chạy `ffprobe` lấy độ phân giải và thời lượng.
   - Ladder: 1080p (5000 kbps), 720p (2800), 480p (1400), 360p (800). Bỏ các bậc cao hơn chiều cao của video gốc (luôn giữ ít nhất bậc thấp nhất).
   - Mã hoá H.264 + AAC, GOP cố định 2 giây (`-g`, `-keyint_min`, `-sc_threshold 0`), segment HLS 6 giây, một lần chạy FFmpeg cho mọi bậc (`-var_stream_map`), sinh `master.m3u8`.
   - Poster: một frame ở mốc 10% thời lượng. Sprite: mỗi 10 giây một ảnh, ghép thành lưới, kèm file `thumbnails.vtt` để player xem trước khi tua.
   - Upload toàn bộ lên `hls/{id}/`, publish `video.transcoded`.
5. API nhận `video.transcoded`, ghi `renditions`, `duration_s`, `poster_key`, `sprite_key`, đặt `status=ready`.

**Trạng thái video:** `uploading → queued → processing → ready`. Từ `queued` hoặc `processing` có thể sang `failed`. Từ `failed` có thể quay lại `queued` khi admin bấm retry.

### 4.2 Phụ đề

Admin upload `.srt` hoặc `.vtt` qua `POST /api/admin/videos/{id}/subtitles` (multipart, kèm `lang`, `label`). API chuyển `.srt` sang `.vtt`, lưu vào `hls/{id}/subs/{lang}.vtt`, ghi bảng `subtitles`. Player nạp phụ đề bằng thẻ `<track>` với URL đã ký.

### 4.3 Xem phim với signed URL

1. Web gọi `GET /api/videos/{id}/play` (cookie JWT).
2. API kiểm tra user đã đăng nhập và video `ready`. Sau đó tính:
   - `exp` = now + 2 giờ
   - `sig` = base64url(HMAC-SHA256(secret, `exp` + `"/hls/{id}/"`))
3. Trả về `{ url: "/cdn/s/{sig}/{exp}/hls/{id}/master.m3u8", subtitles: [{lang,label,url}], thumbnails_url, resume_at }`.
4. nginx nhận request `/cdn/s/{sig}/{exp}/hls/{id}/...`, tính lại chữ ký cho prefix thư mục, so khớp và kiểm tra hạn. Đúng thì proxy tới MinIO (có cache), sai hoặc hết hạn trả `403`.
5. Playlist con và segment dùng đường dẫn tương đối nên tự mang theo prefix có chữ ký.
6. Player bắt lỗi `403` thì gọi lại `/play` để lấy URL mới và phát tiếp từ vị trí hiện tại.

> nginx `secure_link` gốc dùng MD5. Để dùng HMAC-SHA256 thì cần njs (nginx JavaScript module) hoặc OpenResty. **MVP dùng `secure_link_md5`** (đủ cho mục đích học, giống cơ chế token của nhiều CDN), package `signing` sinh chữ ký theo đúng công thức nginx. Đây là điểm cần nâng cấp khi lên production.

### 4.4 Xem tiếp và sự kiện phân tích

1. Player gửi `POST /api/playback/events` mỗi 10 giây, và khi `play`, `pause`, `seek`, `ended`, `buffering`. Nội dung: `video_id`, `type`, `position_s`, `bitrate_kbps`, `buffering_ms`.
2. API upsert `watch_progress` (chỉ khi `type` là `heartbeat`, `pause` hoặc `ended`), sau đó publish `playback.events`.
3. Hàng "Xem tiếp": các video có `5% < position/duration < 95%` của user, sắp theo `updated_at` mới nhất, tối đa 20.
4. Analytics service ghi sự kiện theo lô (mỗi 1 giây hoặc 1000 bản ghi) vào ClickHouse.

## 5. Data model

### Postgres

```sql
users (
  id uuid pk, email text unique, password_hash text,
  role text check (role in ('admin','viewer')), created_at timestamptz
)
videos (
  id uuid pk, title text, description text, genres text[], year int,
  status text check (status in ('uploading','queued','processing','ready','failed')),
  fail_reason text, duration_s int, raw_key text, hls_prefix text,
  poster_key text, sprite_key text,
  search tsvector,   -- unaccent(title) + unaccent(description), cập nhật bằng trigger
  created_at timestamptz, updated_at timestamptz
)
renditions (video_id uuid fk, height int, bitrate_kbps int, playlist_key text, pk(video_id,height))
subtitles  (video_id uuid fk, lang text, label text, vtt_key text, pk(video_id,lang))
watch_progress (user_id uuid fk, video_id uuid fk, position_s int, duration_s int,
                updated_at timestamptz, pk(user_id,video_id))
```

Index: GIN trên `search`, GIN `pg_trgm` trên `unaccent(title)`, index `(user_id, updated_at desc)` trên `watch_progress`.

Tìm kiếm: `websearch_to_tsquery('simple', unaccent($q))`, xếp hạng bằng `ts_rank`. Nếu không có kết quả thì dùng `similarity()` của trigram để chịu lỗi gõ. Lọc theo `genres` và `year`. Chỉ trả video `ready` cho viewer.

### ClickHouse

```sql
playback_events (ts DateTime64(3), user_id UUID, video_id UUID, type LowCardinality(String),
                 position_s UInt32, bitrate_kbps UInt32, buffering_ms UInt32)
  ENGINE = MergeTree ORDER BY (video_id, ts)
app_logs (ts DateTime64(3), service LowCardinality(String), level LowCardinality(String),
          msg String, request_id String, video_id String, attrs String)
  ENGINE = MergeTree ORDER BY (service, ts) TTL ts + INTERVAL 30 DAY
```

Có sẵn các câu SQL mẫu: top phim theo lượt play, tỉ lệ xem hết, tổng thời gian buffering theo bitrate.

## 6. API (tóm tắt)

| Method | Path | Quyền | Mô tả |
|---|---|---|---|
| POST | `/api/auth/register` | public | Tạo tài khoản viewer |
| POST | `/api/auth/login` / `/logout` | public | Đặt/xoá cookie JWT httpOnly |
| GET | `/api/me` | user | Thông tin user |
| GET | `/api/videos?q=&genre=&page=` | user | Catalog và tìm kiếm |
| GET | `/api/videos/{id}` | user | Chi tiết phim |
| GET | `/api/videos/{id}/play` | user | Signed URL, phụ đề, `resume_at` |
| GET | `/api/me/continue-watching` | user | Hàng "Xem tiếp" |
| POST | `/api/playback/events` | user | Sự kiện xem phim |
| POST | `/api/admin/videos` | admin | Tạo video, nhận presigned upload |
| POST | `/api/admin/videos/{id}/complete` | admin | Báo upload xong, đưa vào hàng đợi |
| POST | `/api/admin/videos/{id}/retry` | admin | Retry video `failed` |
| PATCH/DELETE | `/api/admin/videos/{id}` | admin | Sửa metadata / xoá |
| POST | `/api/admin/videos/{id}/subtitles` | admin | Upload phụ đề |

Tài khoản admin đầu tiên được tạo bằng lệnh `make seed-admin`.

## 7. Xử lý lỗi

| Tình huống | Cách xử lý |
|---|---|
| Transcoder chết giữa job | Commit offset Kafka **sau khi** upload và publish thành công, nên job được làm lại khi worker khởi động |
| Xử lý trùng | Idempotent: output ghi đè `hls/{id}/`. API bỏ qua `video.transcoded` nếu video đã `ready` |
| FFmpeg lỗi | Thử lại 3 lần, giãn cách tăng dần (5s, 30s, 2 phút). Hết lượt thì publish `video.failed` và đưa vào `video.uploaded.dlq` |
| Upload bỏ dở | Job định kỳ trong api xoá video `uploading` quá 24 giờ, kèm object raw và multipart upload dở |
| Kafka không khả dụng | `/complete` trả `503`, admin thử lại. Sự kiện xem phim: ghi log rồi bỏ qua, không làm hỏng việc xem |
| Signed URL hết hạn | Player lấy lại URL qua `/play` và phát tiếp |
| Dual-write Postgres + Kafka | Chấp nhận trong MVP. Ghi nhận: bản sau dùng outbox pattern |

Log: `slog` JSON, luôn có `service`, `request_id`, và `video_id` nếu có. Log ra stdout và đồng thời gửi vào `app.logs` (qua một `slog.Handler` bất đồng bộ, bỏ log khi buffer đầy để không chặn request).

## 8. Bảo mật (mức MVP)

- Mật khẩu hash bằng bcrypt. JWT ký HS256, lưu trong cookie `HttpOnly`, `SameSite=Lax`, hết hạn sau 24 giờ.
- Bucket MinIO ở chế độ private. Chỉ nginx (bằng credential riêng hoặc mạng nội bộ) và các service đọc được.
- Signed URL ký theo prefix thư mục, hết hạn sau 2 giờ.
- Presigned upload URL hết hạn sau 1 giờ, chỉ admin được cấp.
- Validate file upload: kiểm tra bằng `ffprobe` trước khi transcode, giới hạn kích thước 10 GB.
- Secrets đọc từ biến môi trường (file `.env` không commit).

## 9. Testing

- **Unit (Go):** sinh ladder và lệnh FFmpeg, ký URL (khớp công thức nginx), SRT → VTT, chuyển trạng thái video, logic "xem tiếp", parse sự kiện.
- **Integration:** `testcontainers-go` dựng Postgres, MinIO, Kafka. Test repository, presigned upload, producer/consumer.
- **E2E (`scripts/e2e.sh`):** FFmpeg sinh video mẫu 30 giây (`testsrc` + `sine`), upload → đợi `ready` → `/play` → tải `master.m3u8` và một segment qua nginx (mong đợi 200). Sửa chữ ký hoặc dùng `exp` quá khứ (mong đợi 403).
- **Frontend:** Vitest cho các component chính. Player kiểm tra tay trên Chrome và Safari.

## 10. Các giai đoạn

| # | Giai đoạn | Tiêu chí hoàn thành |
|---|---|---|
| M0 | Khung dự án | Cài Go. `docker compose up` chạy Postgres, MinIO, Kafka, Redpanda Console, nginx, ClickHouse. Có skeleton `cmd/*` và Makefile |
| M1 | Transcode lõi | CLI biến một file mp4 thành HLS đa độ phân giải trên MinIO, phát được bằng trang test hls.js, đổi được chất lượng |
| M2 | Upload + Kafka | Presigned upload, `video.uploaded` → transcoder → `video.transcoded` → `ready`. Thấy message trong Redpanda Console |
| M3 | Auth + signed URL | Đăng ký/đăng nhập, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn |
| M4 | Web người xem | Trang chủ, chi tiết, player, tìm kiếm không dấu |
| M5 | Xem tiếp, thumbnail, phụ đề | Hàng "Xem tiếp", poster, sprite xem trước khi tua, phụ đề nhiều ngôn ngữ |
| M6 | Analytics | `playback.events` và `app.logs` vào ClickHouse, có SQL mẫu chạy được |
| M7 | Hoàn thiện | Trang admin (danh sách, retry, xem lỗi), e2e xanh, README giải thích kiến trúc |

Ước lượng tổng: 2–3 tuần làm cùng AI.
