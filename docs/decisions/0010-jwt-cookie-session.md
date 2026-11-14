# ADR 0010: Phiên đăng nhập bằng JWT HS256 trong cookie HttpOnly

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §8: mật khẩu bcrypt, JWT HS256 trong cookie `HttpOnly`, `SameSite=Lax`, 24 giờ; role `admin` / `viewer`. Web (M4) và player cần gửi kèm phiên tự động.

## Quyết định
- Thư viện `github.com/golang-jwt/jwt/v5` (v5.3.1); bcrypt từ `golang.org/x/crypto` (cost 12).
- Claims: `sub` = user id, `role`, `iss` = `streamfilm`, `iat`, `exp` (24 giờ). Parse chỉ nhận `HS256`, bắt buộc `exp` và đúng `iss`.
- Cookie `sf_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` theo `COOKIE_SECURE`.
- Middleware `Authenticate` chỉ đọc JWT (không truy vấn DB); `RequireUser` → 401, `RequireAdmin` → 401/403. `/api/me` là chỗ duy nhất đọc DB để kiểm tra tài khoản còn tồn tại.
- Register tạo `viewer` và đăng nhập luôn. Login sai email vẫn chạy bcrypt với hash giả để thời gian phản hồi không lộ email đã đăng ký.
- Admin đầu tiên: `make seed-admin` → `api seed-admin` (upsert theo email, đặt lại mật khẩu, nâng role).

## Hệ quả
- Secret trong `.env.example` chỉ dành cho dev local; phải sinh `JWT_SECRET` mới ở mọi môi trường khác.
- Stateless, nhanh, dễ scale nhiều instance api (chỉ cần chung `JWT_SECRET`).
- Không thu hồi được token (giới hạn đã biết của JWT stateless): logout chỉ xoá cookie phía trình duyệt; đổi role hoặc xoá user có hiệu lực khi token hết hạn (tối đa 24 giờ). Bản sau: TTL ngắn + refresh token, hoặc danh sách thu hồi.
- `SameSite=Lax` chặn cookie trong POST cross-site nên đủ chống CSRF ở mức MVP (các cổng khác nhau của `localhost` vẫn là same-site).
- Chưa giới hạn tốc độ đăng nhập (brute force); ghi nhận cho bản sau.
