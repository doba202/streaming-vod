# ADR 0005: Migration SQL nhúng, runner tự viết

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Cần tạo schema Postgres khi dựng stack, không muốn thêm bước chạy công cụ migrate riêng.

## Quyết định
File `migrations/NNNN_*.sql` được nhúng bằng `embed`. Khi khởi động, api lấy `pg_advisory_lock`, áp các file chưa có trong bảng `schema_migrations` theo thứ tự tên, mỗi file trong một transaction. Mỗi giai đoạn chỉ thêm bảng mà code của giai đoạn đó dùng (M2: `videos`, `renditions`).

## Hệ quả
- Không thêm dependency; dễ đọc để học cơ chế migrate.
- Chỉ có migrate tiến, không có down migration. Cần rollback thì viết migration mới.
- Nếu cần tính năng nâng cao (down, migration bằng Go) thì chuyển sang `pressly/goose`, định dạng file tương thích.
