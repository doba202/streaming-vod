# ADR 0011: nginx là cổng vào duy nhất — SPA, API và CDN cùng origin

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
M4 thêm web người xem (React + Vite). Phiên đăng nhập là cookie `sf_session` `HttpOnly`, `SameSite=Lax` (ADR 0010). Nếu SPA chạy ở một origin (ví dụ `:5173`) và API ở origin khác (`:8000`) thì mọi `fetch` phải bật CORS có `credentials`, server phải trả `Access-Control-Allow-Credentials` và liệt kê origin cụ thể, và cookie phụ thuộc quy tắc same-site của trình duyệt. Player cũng cần URL `/cdn/...` của nginx.

## Quyết định
- nginx (cổng `8088`) phục vụ cả ba: SPA build sẵn ở `/` (fallback `index.html` cho route của react-router), `/api/` proxy nguyên URI tới `api:8000`, `/cdn/` là CDN ký URL (ADR 0009). Trình duyệt chỉ nói chuyện với một origin nên không cần CORS cho API và cookie luôn được gửi kèm.
- `proxy_pass` tới api dùng biến + `resolver 127.0.0.11` (DNS của Docker): nginx khởi động được khi api chưa chạy (test nginx trong container không có api) và theo kịp IP mới khi api được tạo lại.
- SPA được build trong Docker multi-stage (`deploy/docker/nginx.Dockerfile`: `node:20.19.6-alpine3.22` → `nginx:1.27-alpine`), image `streamfilm-nginx:dev`. `make up` build lại như api/transcoder (ADR 0008), máy host không cần Node. Template nginx vẫn mount từ repo.
- Dev frontend: `make web-dev` chạy Vite ở `:5173`, proxy `/api` và `/cdn` sang nginx `:8088`, nên đường đi của request giống hệt bản build.
- `CDN_BASE_URL` (mặc định `http://localhost:8088`) trùng origin của site; `/play` vẫn trả URL tuyệt đối để player chạy được cả khi CDN tách sang domain riêng.
- Asset của Vite có hash trong tên: `/assets/` gửi `Cache-Control: immutable` 1 năm; `index.html` gửi `no-cache` để bản build mới có hiệu lực ngay.

## Phương án đã loại
- **Build ở host (`make web-build`) rồi mount `web/app/dist` vào nginx**: nhanh khi sửa giao diện, nhưng `make up` phụ thuộc Node trên máy và dễ phục vụ bản `dist` cũ. Vẫn có `make web-build` để kiểm tra build cục bộ.
- **CORS có credentials giữa `:5173`/`:8000`**: thêm cấu hình và lỗi khó hiểu, không có lợi gì cho MVP.

## Hệ quả
- Một cổng cho người dùng: http://localhost:8088. Cổng `8000` vẫn mở cho script và debug.
- Sửa giao diện cần build lại image nginx (`make up`) hoặc dùng `make web-dev` khi phát triển.
- Lên production: nginx này tương ứng một reverse proxy/edge (ví dụ Cloudflare) đứng trước SPA tĩnh + API; CDN phát video có thể tách domain mà không đổi code web (URL tuyệt đối từ `/play`).
