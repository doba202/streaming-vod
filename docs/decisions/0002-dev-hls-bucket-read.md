# ADR 0002: Bucket `hls` cho đọc ẩn danh trong môi trường dev

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
nginx cần đọc segment từ MinIO. Ký request S3 trong nginx phức tạp và không phải trọng tâm bài học.

## Quyết định
Bucket `hls` đặt chính sách `download` ẩn danh. MinIO chỉ bind `127.0.0.1`, nên chỉ máy local và mạng Docker truy cập được. Bucket `raw` vẫn private.

## Hệ quả
- Ở M1 ai biết URL `/hls/...` trên máy local đều xem được; M3 thêm signed URL tại nginx.
- Production: bucket private, CDN dùng origin auth (R2 + Cloudflare, S3 + CloudFront OAC).
