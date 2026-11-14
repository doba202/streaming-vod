# ADR 0008: Chạy api và transcoder trong docker compose

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Spec đặt FFmpeg trong container transcoder và vẽ mọi service trong compose. Khi dev vẫn muốn chạy nhanh trên host.

## Quyết định
Thêm image `api` và `transcoder` (multi-stage `golang:1.27.4-alpine3.24` → `alpine:3.24.4`, transcoder cài `ffmpeg` của Alpine). `make up` build và chạy cả hai, `make e2e-m2` kiểm tra toàn luồng. `make run-api` / `make run-worker` chạy trên host khi dev. Trong compose, api gọi MinIO qua `http://minio:9000` nhưng ký presigned URL cho `http://localhost:9000` (`S3_PUBLIC_ENDPOINT`).

## Hệ quả
- Demo một lệnh, môi trường giống nhau giữa các máy; thử được tình huống restart worker giữa job.
- Đổi code phải `make up` để build lại image.
- Phiên bản FFmpeg trong container theo kho Alpine 3.24, có thể khác FFmpeg trên host.
