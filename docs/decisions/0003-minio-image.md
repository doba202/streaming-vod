# ADR 0003: Image MinIO của Chainguard

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Image `minio/minio` và `minio/mc` trên Docker Hub không còn pull được.

## Quyết định
Dùng `cgr.dev/chainguard/minio` và `cgr.dev/chainguard/minio-client`, ghim theo digest trong `deploy/docker-compose.yml`. Healthcheck dùng `wget` tới `/minio/health/ready`.

## Hệ quả
- Tái lập được nhờ digest; muốn nâng cấp phải đổi digest thủ công.
- Image tối giản nên healthcheck chỉ dùng được công cụ có sẵn (`wget`).
