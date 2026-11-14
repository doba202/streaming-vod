# ADR 0014: Phụ đề — chuyển SRT sang WebVTT phía api, lưu cạnh HLS, không cache ở CDN

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.2: admin upload `.srt` hoặc `.vtt` (multipart, kèm `lang`, `label`), api chuyển SRT → VTT, lưu `hls/{id}/subs/{lang}.vtt`, ghi bảng `subtitles`; player nạp bằng `<track>` với URL ký. Trình duyệt chỉ hiểu WebVTT. File SRT ngoài đời thường có BOM, CRLF, số thứ tự, thẻ `<font>`, thẻ ASS `{\an8}`, đôi khi không phải UTF-8.

## Quyết định
- Package `internal/subtitle` (thuần, không I/O): `FromSRT` (bỏ BOM, CRLF/CR → LF, khối cách nhau bởi dòng trống, số thứ tự tuỳ chọn, mốc `HH:MM:SS,mmm` hoặc `.mmm`, bỏ toạ độ sau mốc, giữ `<i>/<b>/<u>`, bỏ `<font>` và thẻ ASS, escape `& < >` để lời thoại có `-->` không phá cấu trúc VTT, lỗi kèm số dòng); `NormalizeVTT` (chuẩn hoá và kiểm tra tối thiểu: header `WEBVTT`, có cue — phần còn lại để trình duyệt parse). Không UTF-8 → `400` với hướng dẫn chuyển mã; không tự đoán bảng mã.
- Giới hạn: file ≤ 1 MiB (`413`), `lang` khớp `^[a-z]{2,3}(-[A-Z]{2})?$` (đi thẳng vào key object nên không cho ký tự khác), `label` 1–50 ký tự không có ký tự điều khiển.
- Ghi object trước (`storage.PutObject`, `text/vtt`), rồi upsert `subtitles` (PK `(video_id, lang)`): lỗi DB để lại object sẽ bị ghi đè ở lần upload lại; không có phụ đề "ma" trong `/play`.
- nginx không cache `subs/` (`proxy_no_cache`/`proxy_cache_bypass` theo `map $sl_file`): upload lại đè cùng key nên cache 7 ngày như segment sẽ phục vụ bản cũ. Segment, playlist, poster, sprite vẫn cache.
- `<video crossorigin="anonymous">`: khi chạy Vite dev (origin khác `:8088`) `<track>` cần CORS; nginx đã trả `Access-Control-Allow-Origin: *` cho `/cdn/`.

## Hệ quả
- Phụ đề đi chung chữ ký thư mục với HLS, hết hạn và được làm mới cùng `/play`.
- Mỗi lần xem tải phụ đề từ MinIO (không cache) — file nhỏ, chấp nhận ở MVP; production có thể đặt tên file theo phiên bản (`subs/{lang}-{rev}.vtt`) để cache được.
- Chưa có xoá phụ đề (trang admin M7).
