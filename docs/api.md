# API

Base URL local: `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.

Xác thực bằng cookie `sf_session` (JWT HS256, `HttpOnly`, `SameSite=Lax`, 24 giờ) do `/api/auth/register` hoặc `/api/auth/login` đặt. Quyền: **public** không cần cookie; **user** cần đăng nhập (thiếu/sai/hết hạn → `401`); **admin** cần role `admin` (chưa đăng nhập → `401`, viewer → `403`).

| Method | Path | Quyền | Mô tả |
|---|---|---|---|
| GET | `/healthz` | public | `ok` |
| POST | `/api/auth/register` | public | Tạo tài khoản viewer và đăng nhập |
| POST | `/api/auth/login` | public | Đăng nhập, đặt cookie |
| POST | `/api/auth/logout` | public | Xoá cookie |
| GET | `/api/me` | user | Thông tin user |
| GET | `/api/videos?page=` | user | Catalog video `ready`, 20 video/trang |
| GET | `/api/videos/{id}` | user | Chi tiết video `ready` |
| GET | `/api/videos/{id}/play` | user | URL ký để phát (hạn 2 giờ) |
| POST | `/api/admin/videos` | admin | Tạo video, nhận presigned multipart upload |
| POST | `/api/admin/videos/{id}/complete` | admin | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | admin | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | admin | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | admin | Chi tiết video kèm `renditions` |

## POST /api/auth/register

`{"email":"an@example.com","password":"password123"}` → `201` + cookie:

```json
{"id":"5f0c…","email":"an@example.com","role":"viewer","created_at":"2026-11-15T10:00:00Z"}
```

Email được chuyển chữ thường. Mật khẩu 8–72 byte. `400` email/mật khẩu không hợp lệ hoặc có field lạ (ví dụ `role`), `409` email đã đăng ký.

## POST /api/auth/login

Body như register. `200` + cookie, body như trên. `401 {"error":"invalid email or password"}` khi sai email hoặc mật khẩu (không phân biệt).

## POST /api/auth/logout

`204`, xoá cookie. JWT không lưu phía server: token đã sao chép ra ngoài vẫn hợp lệ tới khi hết hạn ([ADR 0010](decisions/0010-jwt-cookie-session.md)).

## GET /api/me

`200` body như register. `401` nếu chưa đăng nhập hoặc tài khoản không còn.

## GET /api/videos

`?page=2` (1–1000, mặc định 1) → `200`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,
  "duration_s":30,"created_at":"2026-11-14T10:00:00Z"}],"page":2,"has_more":false}
```

Chỉ video `ready`, mới nhất trước. Tìm kiếm `q` và lọc `genre` có ở M4 (M3 bỏ qua).

## GET /api/videos/{id}

`200`: như một phần tử của `videos` ở trên, thêm `"renditions":[{"height":1080,"bitrate_kbps":5000},…]`. `404` nếu không có hoặc chưa `ready`.

## GET /api/videos/{id}/play

`200` (header `Cache-Control: no-store`):

```json
{"url":"http://localhost:8088/cdn/s/H23K0bGijtyB6JSJpoVT1g/1794736800/hls/7c1e…/master.m3u8",
 "expires_at":"2026-11-15T12:00:00Z","subtitles":[],"resume_at":0}
```

`url` hết hạn sau 2 giờ; playlist con và segment dùng chung chữ ký. nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play`. `subtitles` luôn `[]` và `resume_at` luôn `0` tới M5. `404` nếu không có hoặc chưa `ready`.

## POST /api/admin/videos

```json
{"title":"Phim A","description":"","genres":["drama"],"year":2024,"filename":"movie.mp4","size_bytes":157286400}
```

`filename` phải kết thúc bằng `.mp4`, `.mov`, `.mkv` hoặc `.webm`. `size_bytes` từ 1 tới 10 GB.

`201`:

```json
{"id":"7c1e…","status":"uploading","upload":{"upload_id":"…","part_size":67108864,
 "parts":[{"part_number":1,"url":"http://localhost:9000/raw/7c1e…/source.mp4?partNumber=1&uploadId=…&X-Amz-…"},
          {"part_number":2,"url":"…"},{"part_number":3,"url":"…"}],
 "expires_at":"2026-11-14T11:00:00Z"}}
```

Client cắt file thành các khúc `part_size` byte (khúc cuối nhỏ hơn) và `PUT` khúc thứ n vào `url` của `part_number` n. Không cần gửi lại ETag: API tự liệt kê part khi complete.

Lỗi: `400` body sai / thiếu `title` / kích thước hoặc đuôi file không hợp lệ.

## POST /api/admin/videos/{id}/complete

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`.

| Mã | Khi nào |
|---|---|
| `400` | Upload chưa đủ hoặc sai kích thước: thiếu part (`multipart upload incomplete: got 1 parts, want 3`), part không cuối khác 64 MiB, hoặc tổng byte khác `size_bytes` (`multipart upload incomplete: uploaded N bytes, declared M`). Video vẫn `uploading` |
| `404` | Không có video |
| `409` | Video không ở `uploading` (`invalid status transition: video is queued`) |
| `503` | Kafka không khả dụng; video vẫn `uploading`, gọi lại sau |

## POST /api/admin/videos/{id}/retry

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`. `409` nếu video không ở `failed`, `404`, `503` như trên.

## GET /api/admin/videos

`?status=failed&limit=20` → `200`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,
  "status":"failed","fail_reason":"transcode: input has no video stream","size_bytes":157286400,
  "created_at":"2026-11-14T10:00:00Z","updated_at":"2026-11-14T10:01:00Z"}]}
```

`400` nếu `status` không thuộc `uploading|queued|processing|ready|failed` hoặc `limit` ngoài 1–200.

## GET /api/admin/videos/{id}

`200`:

```json
{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,"status":"ready",
 "duration_s":30,"size_bytes":157286400,"hls_prefix":"7c1e…/",
 "created_at":"…","updated_at":"…",
 "renditions":[{"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"}]}
```
