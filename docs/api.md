# API

Base URL local: `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.

> M2: các endpoint `/api/admin/*` **chưa có xác thực**. M3 thêm đăng nhập (cookie JWT) và chỉ cho role `admin`.

| Method | Path | Mô tả |
|---|---|---|
| GET | `/healthz` | `ok` |
| POST | `/api/admin/videos` | Tạo video, nhận presigned multipart upload |
| POST | `/api/admin/videos/{id}/complete` | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | Chi tiết video kèm `renditions` |

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
