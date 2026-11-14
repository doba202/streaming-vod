# Streamfilm

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **M5** — upload và transcode qua Kafka (M2); đăng nhập bằng cookie JWT, `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn (M3); web người xem (trang chủ, chi tiết, player hls.js, tìm kiếm không dấu) chạy cùng origin sau nginx (M4); poster, ảnh xem trước khi tua (sprite), phụ đề nhiều ngôn ngữ, hàng "Xem tiếp" và sự kiện xem lên Kafka (M5). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2
- FFmpeg (có `ffprobe`) — để sinh video mẫu và chạy test
- `curl`, `jq`, `openssl` — cho script e2e
- Node 20.19+ — chỉ khi phát triển web (`make web-dev`, `make web-test`); `make up` build web trong Docker

## Chạy nhanh

```bash
make up          # dựng hạ tầng + build và chạy api, transcoder, nginx (kèm web)
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make e2e-m4      # upload video tiêu đề tiếng Việt → tìm "tro choi dien tu" → /play qua nginx
make e2e-m5      # poster/sprite, phụ đề SRT → VTT, sự kiện xem → "Xem tiếp" (KAFKA_DOWN=1: thử Kafka chết)
```

Mở http://localhost:8088, đăng nhập bằng `ADMIN_EMAIL` / `ADMIN_PASSWORD` trong `.env` (mặc định `admin@streamfilm.local` / `admin-dev-password`) hoặc bấm "Đăng ký" để tạo tài khoản người xem. Catalog trống cho tới khi có video `ready`: chạy `make e2e-m4` (hoặc `make e2e-m2`) để upload `tmp/sample.mp4`.

`.env.example` chỉ chứa secret dùng cho dev local (`JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin). Muốn dùng ngoài máy local thì phải sinh giá trị mới.

`make e2e-m3` kiểm tra phân quyền và URL ký. Theo dõi message tại Redpanda Console http://localhost:8080.

## Test

```bash
make test-short   # chỉ unit test
make test         # gồm integration test (cần Docker và FFmpeg)
make web-install && make web-test   # Vitest cho web/app
```

## Tài liệu

- [Kiến trúc](docs/architecture.md)
- [Các luồng](docs/flows.md)
- [Data model và sự kiện](docs/data-model.md)
- [API](docs/api.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
