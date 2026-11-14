# ADR 0020: Xoá video từ chối khi đang xử lý; sửa metadata ghi đè cả bốn cột

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §6 có `PATCH/DELETE /api/admin/videos/{id}`. Một video có dữ liệu ở Postgres (`videos`, và `renditions`, `subtitles`, `watch_progress` đều `ON DELETE CASCADE`), MinIO (`raw/{id}/`, `hls/{id}/`), cache nginx, ClickHouse. Transcoder không đọc Postgres: khi video `queued`/`processing`, nó có thể đang (hoặc sắp) ghi `hls/{id}/` rồi publish `video.transcoded`.

## Quyết định
- `DELETE` khoá dòng (`SELECT … FOR UPDATE`, như mọi chuyển trạng thái) rồi:
  - `queued` / `processing` → `409` (`catalog.ErrVideoBusy`), không đụng gì. Admin đợi `ready`/`failed` rồi xoá.
  - `uploading`, `ready`, `failed` → xoá dòng (cascade), commit, rồi dọn storage: abort multipart nếu còn `uploading`, xoá `raw/{id}/` và `hls/{id}/`. Lỗi storage chỉ log (`delete objects after delete failed`), request vẫn `204`: DB là nguồn sự thật, object mồ côi không làm sai hành vi.
- Sự kiện đến muộn của video đã xoá (`video.transcoded` …) bị `StatusUpdater` bỏ qua như `ErrNotFound` (đã có từ M2).
- `PATCH` nhận `title`, `description`, `genres`, `year` (field vắng giữ nguyên, `year: 0` xoá năm, `genres: []` xoá thể loại), ở mọi trạng thái. Handler đọc video, gộp, chuẩn hoá bằng `catalog.NormalizeMetadata` (dùng chung với tạo video: tiêu đề 1–200 ký tự, mô tả ≤ 5000, ≤ 10 thể loại mỗi cái ≤ 50 ký tự, bỏ trùng; năm 0 hoặc 1888–2100) rồi `UPDATE` cả bốn cột. Trigger `videos_search_update` tính lại cột `search` nên tìm kiếm thấy tiêu đề mới ngay.

## Phương án đã loại
- Cho xoá khi đang xử lý rồi dọn `hls/{id}/` khi `video.transcoded` đến muộn: cần thêm trạng thái `deleting` hoặc bảng tombstone, consumer phải xoá object — nhiều code cho một tình huống hiếm ở MVP.
- Xoá mềm (`deleted_at`): mọi truy vấn catalog/tìm kiếm/xem tiếp phải lọc thêm; không có yêu cầu khôi phục.
- `PATCH` bằng `UPDATE … SET col = COALESCE($n, col)`: không phân biệt được "không gửi" với "xoá" (`year`), và kiểm tra giới hạn vẫn cần giá trị sau gộp.

## Hệ quả
- Video kẹt `queued` (transcoder chết lâu, message mất) không xoá được qua API cho tới khi nó `failed`; xử lý tay bằng SQL (xem [operations](../operations.md#sự-cố-thường-gặp)).
- Segment đã nằm trong cache nginx (7 ngày) vẫn phục vụ được cho ai còn giữ URL ký chưa hết hạn (tối đa 2 giờ); không cấp được URL mới vì `/play` trả `404`.
- Sự kiện xem trong ClickHouse của video đã xoá được giữ lại (lịch sử phân tích).
- Hai admin sửa cùng lúc: bản ghi sau thắng (không có optimistic locking).
