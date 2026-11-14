# Streamfilm

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **M3** — upload và transcode qua Kafka (M2); đăng ký/đăng nhập bằng cookie JWT, phân quyền `admin`/`viewer`; `/play` trả URL ký, nginx chặn URL sai hoặc hết hạn. Chưa có web (M4). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2
- FFmpeg (có `ffprobe`) — để sinh video mẫu và chạy test
- `curl`, `jq`, `openssl` — cho script e2e

## Chạy nhanh

```bash
make up          # dựng hạ tầng + build và chạy api (cổng 8000) và transcoder
make seed-admin  # tạo admin từ ADMIN_EMAIL / ADMIN_PASSWORD trong .env
make e2e-m3      # auth + upload → transcode → /play → kiểm tra URL ký qua nginx (gồm cả e2e-m2)
```

`.env.example` chỉ chứa secret dùng cho dev local (`JWT_SECRET`, `CDN_SIGNING_SECRET`, mật khẩu admin). Muốn dùng ngoài máy local thì phải sinh giá trị mới.

Script in ra link dev player để xem video vừa xử lý. Theo dõi message tại Redpanda Console http://localhost:8080.

## Test

```bash
make test-short   # chỉ unit test
make test         # gồm integration test (cần Docker và FFmpeg)
```

## Tài liệu

- [Kiến trúc](docs/architecture.md)
- [Các luồng](docs/flows.md)
- [Data model và sự kiện](docs/data-model.md)
- [API](docs/api.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
