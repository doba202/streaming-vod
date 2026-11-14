# Streamfilm

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **MVP hoàn chỉnh (M7)** — upload và transcode HLS qua Kafka (M2); đăng nhập bằng cookie JWT, URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem cùng origin sau nginx, tìm kiếm không dấu (M4); poster, sprite khi tua, phụ đề, "Xem tiếp", sự kiện xem lên Kafka (M5); analytics và log vào ClickHouse (M6); trang quản trị upload/sửa/xoá/thử lại video, header bảo mật, e2e một lệnh (M7). Lộ trình: [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn). Giới hạn chấp nhận: [known-limitations](docs/known-limitations.md).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2 (Docker Desktop hoặc Engine), ~6 GB RAM cho Docker
- FFmpeg (có `ffprobe`) — sinh video mẫu, chạy test và e2e
- `curl`, `jq`, `openssl`, `split` — cho script e2e
- Node 20.19+ — chỉ khi phát triển web (`make web-dev`, `make web-test`, `make test-all`); `make up` build web trong Docker
- Cổng trống trên máy: 8088 (web), 8000 (api), 9000/9001 (MinIO), 8080 (Redpanda Console), 8123 (ClickHouse), 5432 (Postgres), 29092 (Kafka)

## Chạy từ đầu

```bash
make up          # tạo .env từ .env.example, build và chạy toàn bộ stack (lần đầu vài phút)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make sample      # sinh tmp/sample.mp4 (30 giây 1080p) để thử upload
```

1. Mở http://localhost:8088, đăng nhập `admin@streamfilm.local` / `admin-dev-password` (giá trị `ADMIN_EMAIL` / `ADMIN_PASSWORD` trong `.env`).
2. Bấm **Quản trị** → **Tải video lên**, chọn `tmp/sample.mp4`, nhập tiêu đề → **Upload**. Thanh tiến độ chạy tới 100%, trang chuyển sang chi tiết video.
3. Đợi chip trạng thái từ `Chờ xử lý` → `Đang xử lý` → `Sẵn sàng` (khoảng 20–40 giây, tự cập nhật).
4. Bấm **▶ Xem phim** (hoặc về **Trang chủ** và chọn phim). Người xem khác: **Đăng ký** ở trang đăng nhập.

Kiểm tra toàn bộ hệ thống bằng một lệnh (khoảng 3 phút, in bảng tổng kết):

```bash
make e2e
```

Analytics: `make analytics-sql` (top phim, tỉ lệ xem hết, buffering theo bitrate, log theo service). Kafka: Redpanda Console http://localhost:8080. Tắt: `make down` (giữ dữ liệu).

`.env.example` chỉ chứa secret dùng cho dev local (`JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin). Muốn dùng ngoài máy local thì phải sinh giá trị mới.

## Test

```bash
make test-all     # go test ./... (gồm integration, cần Docker + FFmpeg) và Vitest của web
make test-short   # chỉ unit test Go
make e2e          # e2e trên stack đang chạy (make up + make seed-admin trước)
```

## Tài liệu

- [Kiến trúc](docs/architecture.md)
- [Các luồng](docs/flows.md)
- [Data model và sự kiện](docs/data-model.md)
- [API](docs/api.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
- [Giới hạn đã biết](docs/known-limitations.md)
