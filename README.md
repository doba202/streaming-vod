# Streamfilm

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **M2** — upload thẳng lên storage bằng presigned URL, pipeline `video.uploaded → transcoder → video.transcoded → ready` qua Kafka. Chưa có đăng nhập (M3). Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

## Yêu cầu

- Go 1.27+
- Docker + Docker Compose v2
- FFmpeg (có `ffprobe`) — để sinh video mẫu và chạy test
- `curl`, `jq` — cho script e2e

## Chạy nhanh

```bash
make up        # dựng hạ tầng + build và chạy api (cổng 8000) và transcoder
make e2e-m2    # sinh tmp/sample.mp4 nếu chưa có, upload → transcode → kiểm tra HLS qua nginx
```

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
