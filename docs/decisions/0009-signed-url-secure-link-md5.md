# ADR 0009: Signed URL theo prefix thư mục, kiểm tra bằng nginx `secure_link_md5`

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Segment HLS phát thẳng từ "CDN" (nginx), không đi qua api. Cần chặn người không đăng nhập và URL bị chia sẻ lâu dài, nhưng không thể ký từng segment (một video có hàng trăm file, playlist dùng đường dẫn tương đối). nginx `secure_link` gốc chỉ hỗ trợ MD5; HMAC-SHA256 cần njs hoặc OpenResty.

## Quyết định
- URL: `/cdn/s/{sig}/{exp}/hls/{id}/{file}`, `exp` = now + 2 giờ (Unix giây).
- `sig` = base64url không padding của `MD5("{exp}/hls/{id}/ {secret}")`, ký theo prefix thư mục `/hls/{id}/` nên mọi file trong thư mục dùng chung chữ ký và đường dẫn tương đối trong playlist tự mang chữ ký.
- nginx: `secure_link $sl_sig,$sl_exp; secure_link_md5 "$sl_exp/hls/$sl_vid/ ${CDN_SIGNING_SECRET}";` với các biến bắt từ regex của location. `$secure_link` rỗng (sai) hoặc `"0"` (hết hạn) đều trả `403`. Hợp lệ thì bỏ prefix chữ ký, proxy tới MinIO, cache theo `{id}/{file}`.
- Secret dùng chung qua biến `CDN_SIGNING_SECRET`, chỉ gồm `[A-Za-z0-9_-]` (16–128 ký tự) để chèn an toàn vào cấu hình nginx bằng envsubst của image chính thức.
- `Signer.Path` từ chối tên file không an toàn (rỗng, bắt đầu bằng `/`, chứa `..`, `?`, `#`, `\` hoặc khoảng trắng). Chữ ký chỉ phủ thư mục nên cách chặn truy cập chéo video là nginx chuẩn hoá URI và chữ ký lệch: đã kiểm tra `../` và `%2e%2e` từ URL của video A sang video B đều trả `403` (có trong `e2e-m3`).
- `internal/signing` là nguồn duy nhất của công thức phía Go; `internal/signing/nginx_test.go` chạy đúng template nginx của repo trong container để chứng minh hai bên khớp; script e2e tính lại chữ ký độc lập bằng `openssl`.

## Hệ quả
- `/play` trả URL tuyệt đối (`CDN_BASE_URL` + path), khác ví dụ tương đối trong spec §4.3, vì web M4 chạy khác origin với nginx.
- Không thêm module nginx; giống cơ chế token của nhiều CDN thương mại.
- MD5 với secret ghép chuỗi không phải MAC chuẩn (dễ bị length-extension về lý thuyết; ở đây chuỗi có cấu trúc cố định và secret ở cuối). Đủ cho mục đích học; production nên dùng HMAC-SHA256 (njs/OpenResty hoặc token auth của CDN thật).
- URL không gắn với user: ai có URL thì xem được tới khi hết hạn (2 giờ). Thu ngắn TTL hoặc gắn IP/user vào chuỗi ký là hướng nâng cấp.
- Đổi secret làm mọi URL đang phát trả `403`; player gọi lại `/play`.
