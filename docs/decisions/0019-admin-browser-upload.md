# ADR 0019: Trang admin upload thẳng từ trình duyệt lên MinIO (presigned multipart, CORS giới hạn origin)

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec M7 cần trang admin trong SPA để upload video. API đã có từ M2: `POST /api/admin/videos` trả presigned URL cho từng part 64 MiB (host = `S3_PUBLIC_ENDPOINT`, local `http://localhost:9000`), `/complete` ghép bằng `ListParts` ([ADR 0004](0004-presigned-multipart-upload.md)). SPA chạy ở origin `http://localhost:8088` (nginx), nên `PUT` lên `:9000` là request cross-origin: trình duyệt gửi preflight `OPTIONS` và chỉ cho `PUT` khi MinIO trả `Access-Control-Allow-Origin` khớp origin.

## Quyết định
- Trình duyệt `PUT` từng part thẳng lên presigned URL bằng `XMLHttpRequest` (fetch không báo tiến độ upload), tối đa 3 part cùng lúc, mỗi part thử tối đa 3 lần (chờ 500 ms, 1 s giữa các lần; lỗi 4xx trừ 408/429 không thử lại, 403 báo link hết hạn); một part hết lượt thì huỷ các part đang gửi và báo lỗi kèm id video để admin xoá (`web/app/src/admin/upload.ts`). Nút "Huỷ upload" dừng các part đang gửi rồi xoá video `uploading` bằng `DELETE` (best effort, báo kết quả). Xong thì gọi `/complete`. File không đi qua api hay nginx.
- Không cần đọc header `ETag` của phản hồi `PUT` (api tự `ListParts`), nên không cần `Access-Control-Expose-Headers`. Blob cắt từ `File` có type rỗng nên trình duyệt không gửi `Content-Type`; presigned URL chỉ ký header `host`.
- MinIO mặc định cho mọi origin (`*`). Compose đặt `MINIO_API_CORS_ALLOW_ORIGIN=${MINIO_CORS_ALLOW_ORIGIN}` (mặc định `http://localhost:8088,http://localhost:5173`: SPA qua nginx và Vite dev server); origin khác không nhận được `Access-Control-Allow-Origin` (`scripts/e2e-m7.sh` kiểm tra cả hai chiều).
- `S3_PUBLIC_ENDPOINT` của service api đọc từ `.env` (mặc định `http://localhost:9000`) để một stack chạy ở cổng khác vẫn ký đúng host.

## Phương án đã loại
- Proxy upload qua nginx (`/upload/` → MinIO) để cùng origin: chữ ký SigV4 gắn với header `Host` mà presigned URL đã ký (`localhost:9000`), nginx phải giữ nguyên Host và path — dễ sai, và mọi byte video đi thêm một chặng.
- Upload qua api (multipart form): đi ngược thiết kế "video không đi qua api" (spec §3), api phải giữ tới 10 GB mỗi request.
- `fetch` + `ReadableStream` để có tiến độ: upload streaming cần HTTP/2 và `duplex: 'half'`, MinIO local là HTTP/1.1.

## Hệ quả
- Trình duyệt của admin phải tới được `S3_PUBLIC_ENDPOINT` (cổng 9000). Chạy SPA ở origin khác (đổi cổng nginx, domain khác) phải thêm origin đó vào `MINIO_CORS_ALLOW_ORIGIN` rồi `make up`; quên thì `PUT` báo "lỗi mạng" và preflight không có `Access-Control-Allow-Origin`.
- Rời trang upload giữa chừng thì upload bị huỷ; video ở lại `uploading` và được job dọn xoá sau 24 giờ (hoặc admin xoá ngay trên trang).
- Presigned URL hết hạn sau 1 giờ: file rất lớn trên mạng chậm có thể không kịp (chưa có re-presign, xem [known-limitations](../known-limitations.md)).
