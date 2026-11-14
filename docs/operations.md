# Vận hành local

## Lệnh

| Lệnh | Việc |
|---|---|
| `make up` / `make down` | Dựng / tắt hạ tầng |
| `make ps` / `make logs` | Trạng thái / log các container |
| `make sample` | Sinh `tmp/sample.mp4` |
| `make transcode FILE=… ID=…` | Transcode một file và upload |

`make up` chạy `up -d` rồi `up -d --wait` chỉ cho các service chạy lâu (postgres, minio, kafka, console, clickhouse, nginx). Các container one-shot (`minio-init`, `kafka-init`) thoát với mã 0 nên không dùng `--wait` cho chúng, vì compose coi đó là lỗi.

Xoá sạch dữ liệu: `docker compose -f deploy/docker-compose.yml down -v`.

## Cổng và giao diện

| Dịch vụ | Địa chỉ | Đăng nhập |
|---|---|---|
| nginx "CDN" | http://localhost:8088 | |
| Dev player | http://localhost:8088/dev/player.html?id=demo | |
| MinIO Console | http://localhost:9001 | `S3_ACCESS_KEY` / `S3_SECRET_KEY` trong `.env` |
| Redpanda Console (Kafka) | http://localhost:8080 | |
| ClickHouse HTTP | http://localhost:8123 | `streamfilm` / `POSTGRES_PASSWORD` |
| Postgres | localhost:5432 | `streamfilm` / `POSTGRES_PASSWORD` |
| Kafka từ máy host | localhost:29092 | |

## Image MinIO

Image `minio/minio` trên Docker Hub không còn pull được, nên stack dùng `cgr.dev/chainguard/minio` và `cgr.dev/chainguard/minio-client`, ghim theo digest. Healthcheck của MinIO là `wget` tới `/minio/health/ready`. Xem [ADR 0003](decisions/0003-minio-image.md).

## Kiểm tra cache CDN

```bash
curl -sI http://localhost:8088/hls/demo/720p/seg_000.ts | grep -i x-cache-status
```
Lần đầu `MISS`, các lần sau `HIT`.

## Sự cố thường gặp

| Triệu chứng | Cách xử lý |
|---|---|
| `make up` treo ở kafka | Xem `make logs`; xoá volume `kafkadata` nếu đổi cấu hình listener |
| Player báo 404 manifest | Kiểm tra `ID` khi transcode trùng `?id=` trên URL; xem bucket `hls` trong MinIO Console |
| `ffmpeg: … Unknown encoder 'libx264'` | Cài FFmpeg đầy đủ: `brew reinstall ffmpeg` |
| Integration test bị skip | Bật Docker Desktop; kiểm tra `ffmpeg` trong `PATH` |
