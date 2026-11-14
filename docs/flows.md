# Các luồng

Chỉ gồm các luồng đã chạy (M2). Luồng xem phim với signed URL, xem tiếp và analytics được thêm ở M3–M6.

## Upload và transcode

```mermaid
sequenceDiagram
  autonumber
  participant A as Admin / e2e
  participant API as api
  participant PG as Postgres
  participant S3 as MinIO
  participant K as Kafka
  participant T as transcoder

  A->>API: POST /api/admin/videos {title, filename, size_bytes}
  API->>S3: CreateMultipartUpload raw/{id}/source.ext
  API->>PG: INSERT videos (status=uploading, upload_id)
  Note over API,S3: INSERT lỗi thì api abort multipart vừa mở
  API-->>A: 201 {id, upload: {part_size, parts[{part_number, url}]}}
  loop mỗi part (64 MiB)
    A->>S3: PUT presigned url (hết hạn sau 1 giờ)
  end
  A->>API: POST /api/admin/videos/{id}/complete
  API->>S3: ListParts, kiểm tra kích thước, CompleteMultipartUpload
  API->>PG: status uploading → queued
  API->>K: video.uploaded {video_id, raw_key}
  API-->>A: 200 {status: queued}
  K->>T: video.uploaded
  T->>K: video.processing
  K->>API: video.processing → status processing
  T->>S3: tải raw, FFmpeg, upload hls/{id}/
  T->>K: video.transcoded {duration_s, hls_prefix, renditions[]}
  T->>K: commit offset (group transcoder)
  K->>API: video.transcoded → renditions, duration_s, status ready
```

## Retry và DLQ

```mermaid
sequenceDiagram
  participant K as Kafka
  participant T as transcoder
  participant API as api
  K->>T: video.uploaded
  T->>K: video.processing
  T->>T: lần 1 lỗi → chờ 5s → lần 2 lỗi → chờ 30s → lần 3 lỗi → chờ 2 phút → lần 4 lỗi
  T->>K: video.failed {video_id, reason}
  T->>K: video.uploaded.dlq {payload, error, attempts: 4}
  T->>K: commit offset
  K->>API: video.failed → status failed, fail_reason
  Note over API: Admin gọi POST /{id}/retry: failed → queued, publish lại video.uploaded
```

Lỗi không sửa được bằng thử lại (file không có video stream) đi thẳng tới `video.failed` + DLQ với `attempts: 1`. Message hỏng (không phải JSON, thiếu `video_id`/`raw_key`, hoặc `video_id` không phải UUID dạng chuẩn) đi thẳng vào DLQ với `attempts: 0`.

## Kiểm tra kích thước khi complete

Trước khi ghép, api liệt kê part trên storage và kiểm tra: đủ part 1..N, mọi part trừ part cuối đúng 64 MiB, tổng kích thước đúng bằng `size_bytes` đã khai báo và không vượt 10 GB. Sai thì `/complete` trả `400` (ví dụ `multipart upload incomplete: uploaded 100 bytes, declared 200`), video vẫn `uploading`, client có thể upload lại part rồi gọi lại.

## Kafka không khả dụng khi complete

`/complete` đặt `queued` trước rồi publish (chờ tối đa 5 giây). Publish lỗi hoặc quá hạn thì video được trả về `uploading` và API trả `503` (đo thực tế: khoảng 5 giây); admin gọi lại `/complete` khi Kafka lên. Multipart đã ghép ở lần trước được nhận ra nhờ object `raw/{id}/source.ext` đã tồn tại. `/retry` xử lý tương tự (trả về `failed`).

## Dọn upload bỏ dở

Job trong `api` chạy lúc khởi động và mỗi `CLEANUP_INTERVAL` (mặc định 1 giờ): tìm video `uploading` tạo quá `STALE_UPLOAD_AFTER` (24 giờ), xoá dòng DB (chỉ khi vẫn còn `uploading`), rồi abort multipart upload và xoá object dưới `raw/{id}/`.
