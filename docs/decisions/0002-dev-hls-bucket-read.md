# ADR 0002: Bucket `hls` cho đọc ẩn danh trong môi trường dev

- Trạng thái: Chấp nhận (2026-11-14), cập nhật M3 (2026-11-15)

## Bối cảnh
nginx cần đọc segment từ MinIO. Ký request S3 trong nginx phức tạp và không phải trọng tâm bài học.

## Quyết định
Bucket `hls` đặt chính sách `download` ẩn danh. MinIO chỉ bind `127.0.0.1`, nên chỉ máy local và mạng Docker truy cập được. Bucket `raw` vẫn private.

## Hệ quả
- Từ M3, nginx chỉ phục vụ HLS qua URL ký (`/cdn/s/...`, ADR 0009); đường `/hls/` không ký trên nginx đã bị bỏ.
- Bucket vẫn đọc ẩn danh trực tiếp trên MinIO (`http://localhost:9000/hls/...`), nên trên máy local vẫn vượt qua được lớp ký. Đây là thoả hiệp dev có chủ đích: MinIO chỉ bind `127.0.0.1`, và ký request S3 trong nginx không phải trọng tâm bài học.
- Production: bucket private, CDN đọc origin bằng credential riêng (R2 + Cloudflare, S3 + CloudFront OAC), người xem chỉ đi qua CDN.
