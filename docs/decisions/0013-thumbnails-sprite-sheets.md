# ADR 0013: Poster và sprite xem trước — nhiều tấm 10×10, URL poster ổn định theo giờ

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §4.1: poster là frame ở 10% thời lượng; sprite mỗi 10 giây một ảnh ghép thành lưới, kèm `thumbnails.vtt` để player xem trước khi tua; `video.transcoded` mang `poster_key`, `sprite_key`. Ảnh nằm dưới `hls/{id}/` nên chỉ đọc được qua URL ký (ADR 0009). Một lưới duy nhất cho phim 2 giờ (720 ô) sẽ là ảnh 4800×2430+ px, nặng và vượt giới hạn texture của một số trình duyệt di động.

## Quyết định
- Transcoder chạy FFmpeg thêm hai lần sau HLS: poster (`-ss` 10% thời lượng, rộng 640 px) và sprite (`select='isnan(prev_selected_t)+gte(t-prev_selected_t,10)'`, `scale=160:90:force_original_aspect_ratio=decrease,pad=160:90`, `tile=10x10`, `-fps_mode passthrough`). `select` theo thời gian thay cho `fps=1/10` vì `fps` có thể nhân bản frame cuối ở cuối video, làm số ô lệch với VTT.
- Mỗi tấm 1600×900 chứa 100 ô (16 phút 40 giây); video dài hơn có nhiều tấm `sprite_001.jpg`, `sprite_002.jpg`… (FFmpeg tự ghi tấm cuối thiếu ô). Không giãn khoảng cách ảnh theo độ dài phim: mọi phim đều xem trước được từng 10 giây.
- `thumbnails.vtt` do Go sinh (`transcode.ThumbnailsVTT`) từ thời lượng: `ceil(duration/10)` cue, mỗi cue `sprite_NNN.jpg#xywh=x,y,160,90` với đường dẫn **tương đối** — player resolve theo URL của file VTT nên vẫn ở dưới prefix ký. Test FFmpeg kiểm tra bộ lọc `select` cho đúng số ô này.
- `sprite_key` = `{id}/thumbnails.vtt`: với nhiều tấm, "key của sprite" hợp lý nhất là chỉ mục của chúng. `/play` trả `thumbnails_url` ký từ key này.
- `poster_url` trong catalog ký bằng `Signer.StablePath`: `exp` = now + 2 giờ làm tròn xuống theo giờ. URL giống hệt nhau trong một giờ nên trình duyệt cache ảnh (URL ký theo giây sẽ đổi ở mỗi lần gọi API); hạn còn lại 1–2 giờ, không vượt TTL của spec. Cache CDN không phụ thuộc chữ ký (cache key `{id}/{file}`).
- Lỗi khi sinh ảnh làm job lỗi và thử lại như lỗi HLS (ảnh là một phần của "transcode xong").

## Phương án đã loại
- Một tấm duy nhất với khoảng cách giãn theo độ dài (≤ 100 ô): phim dài xem trước rất thưa.
- Poster công khai ngoài CDN ký (bucket/đường riêng): thêm đường không ký và quy tắc nginx; lợi ích (cache lâu hơn) nhỏ ở MVP.
- Sinh ảnh trong cùng lệnh FFmpeg của HLS: lệnh dài và khó đọc, lỗi ảnh lẫn với lỗi encode, không test riêng được.

## Hệ quả
- Giới hạn đã biết: với video có fps không nguyên (ví dụ 29.97), bộ lọc `select` chọn frame đầu tiên có `t - prev_selected_t >= 10`, nên ô sprite có thể lệch khoảng một frame (~33 ms) mỗi ô so với mốc `10i` giây trong VTT. Không đáng kể khi xem trước; chưa xử lý.
- Video đã `ready` trước M5 không có poster/sprite (web hiện khối màu, không có xem trước) tới khi transcode lại.
- Transcode chậm hơn một lần giải mã toàn bộ video (sprite); poster gần như tức thì nhờ `-ss` trước `-i`.
- Trang mở lâu hơn hạn của URL poster thì ảnh lỗi → web chuyển về khối màu; tải lại trang lấy URL mới.
