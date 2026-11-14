# API

Base URL local: `http://localhost:8088` (qua nginx, cùng origin với web — [ADR 0011](decisions/0011-same-origin-nginx-gateway.md)) hoặc trực tiếp `http://localhost:8000`. Body và response là JSON. Lỗi có dạng `{"error": "…"}`.

Xác thực bằng cookie `sf_session` (JWT HS256, `HttpOnly`, `SameSite=Lax`, 24 giờ) do `/api/auth/register` hoặc `/api/auth/login` đặt. Quyền: **public** không cần cookie; **user** cần đăng nhập (thiếu/sai/hết hạn → `401`); **admin** cần role `admin` (chưa đăng nhập → `401`, viewer → `403`).

| Method | Path | Quyền | Mô tả |
|---|---|---|---|
| GET | `/healthz` | public | `ok` |
| POST | `/api/auth/register` | public | Tạo tài khoản viewer và đăng nhập |
| POST | `/api/auth/login` | public | Đăng nhập, đặt cookie |
| POST | `/api/auth/logout` | public | Xoá cookie |
| GET | `/api/me` | user | Thông tin user |
| GET | `/api/videos?q=&genre=&year=&page=` | user | Catalog và tìm kiếm video `ready`, 20 video/trang |
| GET | `/api/genres` | user | Các thể loại của video `ready` |
| GET | `/api/videos/{id}` | user | Chi tiết video `ready` |
| GET | `/api/videos/{id}/play` | user | URL ký để phát (hạn 2 giờ), phụ đề, thumbnails, `resume_at` |
| GET | `/api/me/continue-watching` | user | Hàng "Xem tiếp" (tối đa 20) |
| POST | `/api/playback/events` | user | Sự kiện xem (heartbeat 10 giây, play/pause/seek/ended/buffering) |
| POST | `/api/admin/videos/{id}/subtitles` | admin | Upload phụ đề `.srt`/`.vtt` (multipart `lang`, `label`, `file`) |
| POST | `/api/admin/videos` | admin | Tạo video, nhận presigned multipart upload |
| POST | `/api/admin/videos/{id}/complete` | admin | Báo upload xong, đưa vào hàng đợi transcode |
| POST | `/api/admin/videos/{id}/retry` | admin | Đưa video `failed` về `queued` |
| GET | `/api/admin/videos?status=&limit=` | admin | Danh sách video mới nhất trước (`limit` 1–200, mặc định 50) |
| GET | `/api/admin/videos/{id}` | admin | Chi tiết video kèm `renditions`, `subtitles` |
| PATCH | `/api/admin/videos/{id}` | admin | Sửa `title`, `description`, `genres`, `year` (M7) |
| DELETE | `/api/admin/videos/{id}` | admin | Xoá video, rendition, phụ đề, tiến độ xem và object trên MinIO (M7) |

Trang quản trị trên web (`/admin`, M7) dùng đúng các endpoint admin ở trên.

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

Tham số (đều tuỳ chọn):

| Tham số | Ý nghĩa |
|---|---|
| `q` | Từ khoá, tối đa 100 ký tự, có dấu hoặc không dấu, cú pháp websearch (`"cụm từ"`, `-loại`, `or`). Rỗng = duyệt catalog |
| `genre` | Lọc đúng một thể loại (giá trị lấy từ `/api/genres`), tối đa 50 ký tự |
| `year` | Lọc theo năm, 1888–2100 |
| `page` | 1–1000, mặc định 1 |

Sai giới hạn → `400`; `q`/`genre` chứa UTF-8 không hợp lệ hoặc byte NUL cũng bị `400`. Ví dụ `?q=tro+choi&genre=H%C3%A0nh+%C4%91%E1%BB%99ng&page=2` → `200`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"year":2024,
  "duration_s":30,"created_at":"2026-11-14T10:00:00Z"}],"page":2,"has_more":false}
```

Từ M5 mỗi video có thêm `poster_url` (URL ký `…/cdn/s/{sig}/{exp}/hls/{id}/poster.jpg`, `exp` làm tròn theo giờ nên URL ổn định để trình duyệt cache); video transcode trước M5 không có trường này.

Chỉ video `ready`. Không có `q`: mới nhất trước. Có `q`: xếp theo độ liên quan (`ts_rank`, khớp tiêu đề trước mô tả); nếu full-text không ra kết quả nào thì dùng trigram trên tiêu đề để chịu lỗi gõ ("tro chpi" vẫn ra "Trò chơi …") — [ADR 0012](decisions/0012-search-unaccent-trigram.md).

## GET /api/genres

`200`: `{"genres":["Gia đình","Hành động"]}` — các thể loại (không trùng, đã sắp xếp) của video `ready`; `[]` khi chưa có.

## GET /api/videos/{id}

`200`: như một phần tử của `videos` ở trên, thêm `"renditions":[{"height":1080,"bitrate_kbps":5000},…]`. `404` nếu không có hoặc chưa `ready`.

## GET /api/videos/{id}/play

`200` (header `Cache-Control: no-store`):

```json
{"url":"http://localhost:8088/cdn/s/H23K0bGijtyB6JSJpoVT1g/1794736800/hls/7c1e…/master.m3u8",
 "expires_at":"2026-11-15T12:00:00Z",
 "subtitles":[{"lang":"en","label":"English","url":"http://localhost:8088/cdn/s/H23K…/1794736800/hls/7c1e…/subs/en.vtt"},
              {"lang":"vi","label":"Tiếng Việt","url":"http://localhost:8088/cdn/s/H23K…/1794736800/hls/7c1e…/subs/vi.vtt"}],
 "thumbnails_url":"http://localhost:8088/cdn/s/H23K…/1794736800/hls/7c1e…/thumbnails.vtt",
 "resume_at":42}
```

`url` hết hạn sau 2 giờ; playlist con, segment, phụ đề, `thumbnails.vtt` và các tấm sprite dùng chung chữ ký thư mục. nginx trả `403` khi chữ ký sai hoặc hết hạn: gọi lại `/play` (player của web tự làm và phát tiếp từ vị trí hiện tại). `subtitles` sắp theo `lang`, `[]` khi chưa có. `thumbnails_url` không có với video transcode trước M5. `resume_at` = vị trí đã lưu của user (giây), `0` nếu chưa xem hoặc đã xem ≥ 95%. `404` nếu không có hoặc chưa `ready`.

## GET /api/me/continue-watching

`200` (`Cache-Control: no-store`): tối đa 20 video `ready` mà user đang xem dở (`5% < position_s/duration_s < 95%`), mới xem nhất trước. Mỗi phần tử như catalog, thêm `position_s` và `watched_at`:

```json
{"videos":[{"id":"7c1e…","title":"Phim A","description":"","genres":["drama"],"duration_s":30,
  "created_at":"2026-11-15T10:00:00Z","poster_url":"http://localhost:8088/cdn/s/…/poster.jpg",
  "position_s":12,"watched_at":"2026-11-15T10:05:00Z"}]}
```

## POST /api/playback/events

```json
{"video_id":"7c1e…","type":"heartbeat","position_s":12.4,"bitrate_kbps":2800,"buffering_ms":0}
```

`type`: `play`, `pause`, `seek`, `ended`, `buffering`, `heartbeat`. `position_s` 0–172800 (số thực, làm tròn xuống giây và kẹp theo thời lượng video), `bitrate_kbps` 0–100000, `buffering_ms` 0–600000. `heartbeat`, `pause`, `ended` ghi `watch_progress`; mọi loại được publish lên `playback.events`. `204` kể cả khi Kafka không khả dụng (sự kiện bị bỏ và ghi log). `400` input sai, `404` video không có hoặc chưa `ready`.

## POST /api/admin/videos/{id}/subtitles

`multipart/form-data`: `lang` (`^[a-z]{2,3}(-[A-Z]{2})?$`, ví dụ `vi`, `en`, `pt-BR`), `label` (1–50 ký tự, ví dụ `Tiếng Việt`), `file` (`.srt` hoặc `.vtt`, UTF-8, ≤ 1 MiB).

```bash
curl -b admin.jar -F lang=vi -F 'label=Tiếng Việt' -F file=@phim.vi.srt \
  http://localhost:8088/api/admin/videos/7c1e…/subtitles
```

`201`: `{"lang":"vi","label":"Tiếng Việt","vtt_key":"7c1e…/subs/vi.vtt"}`. Upload lại cùng `lang` thì ghi đè (file và nhãn). `400` lang/label sai, thiếu file, đuôi khác `.srt`/`.vtt`, không phải UTF-8, SRT hỏng (thông báo có số dòng) hoặc VTT thiếu header; `413` file > 1 MiB; `404` video không tồn tại. `{id}` phải là UUID hợp lệ (dạng chuẩn; id không hợp lệ cũng `404`) và các trường `lang`, `label` chỉ đọc từ form (không nhận qua query string). Admin có thể upload phụ đề cho video chưa `ready` (đang `queued`/`processing`): phụ đề được lưu ngay và chỉ xuất hiện trong `/play` khi video `ready`.

## POST /api/admin/videos

```json
{"title":"Phim A","description":"","genres":["drama"],"year":2024,"filename":"movie.mp4","size_bytes":157286400}
```

`filename` phải kết thúc bằng `.mp4`, `.mov`, `.mkv` hoặc `.webm`. `size_bytes` từ 1 tới 10 GB. Metadata được chuẩn hoá như `PATCH` (cắt khoảng trắng, bỏ thể loại rỗng/trùng) và kiểm tra cùng giới hạn: `title` 1–200 ký tự, `description` ≤ 5000, tối đa 10 `genres` mỗi cái ≤ 50 ký tự, `year` 0 (không rõ) hoặc 1888–2100.

`201`:

```json
{"id":"7c1e…","status":"uploading","upload":{"upload_id":"…","part_size":67108864,
 "parts":[{"part_number":1,"url":"http://localhost:9000/raw/7c1e…/source.mp4?partNumber=1&uploadId=…&X-Amz-…"},
          {"part_number":2,"url":"…"},{"part_number":3,"url":"…"}],
 "expires_at":"2026-11-14T11:00:00Z"}}
```

Client cắt file thành các khúc `part_size` byte (khúc cuối nhỏ hơn) và `PUT` khúc thứ n vào `url` của `part_number` n. Không cần gửi lại ETag: API tự liệt kê part khi complete. Từ trình duyệt, `PUT` là cross-origin tới MinIO: origin của trang phải nằm trong `MINIO_CORS_ALLOW_ORIGIN` ([ADR 0019](decisions/0019-admin-browser-upload.md)).

Lỗi: `400` body sai / metadata vượt giới hạn / kích thước hoặc đuôi file không hợp lệ.

## POST /api/admin/videos/{id}/complete

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`.

| Mã | Khi nào |
|---|---|
| `400` | Upload chưa đủ hoặc sai kích thước: thiếu part (`multipart upload incomplete: got 1 parts, want 3`), part không cuối khác 64 MiB, hoặc tổng byte khác `size_bytes` (`multipart upload incomplete: uploaded N bytes, declared M`). Video vẫn `uploading` |
| `404` | Không có video |
| `409` | Video không ở `uploading` (`invalid status transition: video is queued`) |
| `503` | Kafka không khả dụng; video vẫn `uploading`, gọi lại sau |

## POST /api/admin/videos/{id}/retry

Không có body. `200`: `{"id":"7c1e…","status":"queued"}`. `409` nếu video không ở `failed`, `404`, `503` như trên. `fail_reason` cũ được giữ tới khi video `ready` (ghi đè nếu lỗi lại).

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
 "renditions":[{"height":1080,"bitrate_kbps":5000,"playlist_key":"7c1e…/1080p/index.m3u8"}],
 "subtitles":[{"lang":"vi","label":"Tiếng Việt","vtt_key":"7c1e…/subs/vi.vtt"}]}
```

`subtitles` sắp theo `lang`, `[]` khi chưa có. `404` nếu không có video hoặc `{id}` không phải UUID.

## PATCH /api/admin/videos/{id}

Gửi field cần đổi; field vắng giữ nguyên. `"year": 0` xoá năm, `"genres": []` xoá thể loại. Dùng được ở mọi trạng thái (kể cả đang transcode).

```json
{"title":"Hạ cánh nơi anh","genres":["Tình cảm","drama"],"year":2019}
```

`200`: video như `GET /api/admin/videos` (không kèm `renditions`, `subtitles`). Tìm kiếm thấy tiêu đề/mô tả mới ngay (trigger cập nhật cột `search`).

| Mã | Khi nào |
|---|---|
| `400` | `{}` (`nothing to update`), JSON sai hoặc có field lạ (ví dụ `status`), metadata vượt giới hạn như khi tạo video |
| `404` | Không có video hoặc `{id}` không phải UUID |

Hai admin sửa cùng lúc: bản ghi sau thắng ([ADR 0020](decisions/0020-video-delete-and-edit.md)).

## DELETE /api/admin/videos/{id}

Không có body. `204` khi video ở `uploading`, `ready` hoặc `failed`: xoá dòng `videos` (cascade `renditions`, `subtitles`, `watch_progress`), rồi huỷ multipart upload dở (nếu còn `uploading`) và xoá `raw/{id}/`, `hls/{id}/` trên MinIO. Lỗi MinIO chỉ ghi log (`delete objects after delete failed`), vẫn `204`.

| Mã | Khi nào |
|---|---|
| `409` | Video đang `queued` hoặc `processing` (`video is being processed: video is queued`): đợi `ready`/`failed` rồi xoá |
| `404` | Không có video (kể cả đã xoá) hoặc `{id}` không phải UUID |

Sau khi xoá, `/play` và `/api/videos/{id}` trả `404`; segment đã nằm trong cache nginx có thể còn phục vụ cho URL ký cũ tới khi hết hạn ([ADR 0020](decisions/0020-video-delete-and-edit.md)).
