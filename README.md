# Streamfilm

Nền tảng xem phim theo yêu cầu (VOD) dựng để học system design: upload, transcode HLS nhiều độ phân giải, phân phối qua CDN có ký URL, xem tiếp, tìm kiếm, analytics qua Kafka và ClickHouse.

Trạng thái: **M1** — hạ tầng local và pipeline transcode lõi đã chạy. Xem lộ trình trong [spec](docs/superpowers/specs/2026-11-14-streamfilm-vod-mvp-design.md#10-các-giai-đoạn).

## Yêu cầu

- Go 1.23+
- Docker + Docker Compose v2
- FFmpeg (có `ffprobe`)

## Chạy nhanh

```bash
make up                                     # dựng hạ tầng
make sample                                 # sinh tmp/sample.mp4 (30s, 1080p)
make transcode FILE=tmp/sample.mp4 ID=demo  # transcode và upload lên MinIO
open "http://localhost:8088/dev/player.html?id=demo"
```

## Test

```bash
make test-short   # chỉ unit test
make test         # gồm integration test (cần Docker và FFmpeg)
```

## Tài liệu

- [Kiến trúc](docs/architecture.md)
- [Vận hành local](docs/operations.md)
- [Quyết định kiến trúc (ADR)](docs/decisions/)
