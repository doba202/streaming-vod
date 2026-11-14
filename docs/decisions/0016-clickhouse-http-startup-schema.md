# ADR 0016: ClickHouse qua HTTP interface, schema tạo khi analytics khởi động

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
M6 (spec §3.1, §4.4, §5): `analytics` ghi `playback.events` và `app.logs` vào ClickHouse theo lô (1000 record hoặc 1 giây). Service `clickhouse` (image `clickhouse/clickhouse-server:24.8`) đã có từ M0, chỉ mở cổng HTTP 8123 ra host; database `streamfilm` có sẵn nhưng chưa có bảng. Volume `chdata` của stack đang chạy đã được khởi tạo.

## Quyết định
- **Client:** HTTP interface (8123) bằng `net/http`: mỗi lô là một `POST /?query=INSERT INTO {table} FORMAT JSONEachRow` với body là các dòng JSON; user/mật khẩu qua header `X-ClickHouse-User` / `X-ClickHouse-Key`, database qua tham số `database`. Không thêm dependency. Lỗi (HTTP ≠ 200) trả kèm 500 ký tự đầu của thông điệp ClickHouse.
- **Thời gian:** cột `DateTime64(3, 'UTC')`; analytics gửi `YYYY-MM-DD hh:mm:ss.sss` theo UTC (định dạng mặc định `date_time_input_format=basic` đọc được), nên không phụ thuộc múi giờ của server.
- **Schema:** `internal/analytics/schema.go` chứa các câu `CREATE TABLE IF NOT EXISTS`, `analytics.Migrate` chạy mỗi lần khởi động (cùng tinh thần ADR 0005 cho Postgres). Integration test chạy `Migrate` hai lần.
- `app_logs` dùng `TTL toDateTime(ts) + INTERVAL 30 DAY` (spec ghi `ts + INTERVAL 30 DAY`; ép về `DateTime` để biểu thức TTL rõ kiểu).

## Phương án đã loại
- `clickhouse-go/v2` (giao thức native, cổng 9000): nhanh hơn với lô lớn, nhưng thêm dependency nặng và cổng mới; ở 1000 row/giây JSONEachRow qua HTTP là đủ, và dễ thử bằng `curl` — đúng mục tiêu học.
- `docker-entrypoint-initdb.d` mount SQL vào ClickHouse: chỉ chạy khi volume rỗng, nên stack đang có `chdata` sẽ không bao giờ có bảng; đổi schema về sau cũng không chạy lại. Tạo bảng trong analytics thì luôn đúng và test được bằng testcontainers.

## Hệ quả
- Đổi kiểu cột về sau cần `ALTER TABLE` viết tay (chưa có bảng phiên bản migration như Postgres); đủ cho MVP với hai bảng.
- `make analytics-sql` và `scripts/e2e-m6.sh` dùng chính HTTP interface đó bằng `curl`.
- Analytics không khởi động được khi ClickHouse chưa sẵn sàng: compose đợi `clickhouse` healthy, và `restart: unless-stopped` thử lại nếu `Migrate` lỗi.
