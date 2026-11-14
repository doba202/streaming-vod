# ADR 0001: Modular monolith + worker qua Kafka

- Trạng thái: Chấp nhận (2026-11-14)

## Bối cảnh
Dự án để học system design. Muốn trải nghiệm xử lý bất đồng bộ, hàng đợi, retry, DLQ như kiến trúc tham khảo của Hygraph, nhưng không muốn tốn công vận hành 8 microservice.

## Quyết định
Ba chương trình Go trong một repo: `api` (chia module auth, catalog, upload, playback), `transcoder`, `analytics`. Giao tiếp bất đồng bộ qua Kafka. Chỉ `api` ghi dữ liệu video vào Postgres.

## Hệ quả
- Ít thứ phải deploy và debug; vẫn học đủ event-driven.
- Ranh giới module rõ nên có thể tách microservice sau.
- Ghi Postgres rồi publish Kafka là dual-write; chấp nhận ở MVP, bản sau dùng outbox pattern.
