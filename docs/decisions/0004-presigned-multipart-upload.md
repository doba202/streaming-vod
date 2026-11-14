# ADR 0004: Upload bằng presigned multipart, API tự ListParts khi complete

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
File video lớn (tới 10 GB) không được đi qua api. Một presigned PUT bị S3/R2 giới hạn 5 GB.

## Quyết định
`POST /api/admin/videos` mở multipart upload và trả presigned URL cho từng part 64 MiB (tối đa 160 part), hết hạn sau 1 giờ. Client chỉ cần `PUT` từng part. `/complete` không nhận body: api gọi `ListParts` để lấy ETag rồi `CompleteMultipartUpload`, kiểm tra đủ part 1..N, mọi part trừ part cuối đúng 64 MiB, tổng kích thước bằng `size_bytes` đã khai báo và không quá 10 GB (sai thì `400`, video vẫn `uploading`). Nếu INSERT dòng `videos` lỗi sau khi đã mở multipart thì api abort multipart đó. Lưu `upload_id` và `size_bytes` trong bảng `videos`.

## Hệ quả
- Hỗ trợ file tới 10 GB và chạy được trên S3/R2 không cần đổi code.
- Client không phải đọc header `ETag` (trình duyệt cần CORS `ExposeHeaders` mới đọc được).
- Presigned URL ký theo Host nên cần `S3_PUBLIC_ENDPOINT` khi api gọi storage qua địa chỉ nội bộ.
- Multipart dở được dọn sau 24 giờ bởi job trong api.
