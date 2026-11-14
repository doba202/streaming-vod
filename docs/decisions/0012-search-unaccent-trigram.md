# ADR 0012: Tìm kiếm tiếng Việt bằng Postgres FTS + unaccent, dự phòng trigram

- Trạng thái: Chấp nhận (2026-11-15)

## Bối cảnh
Spec §5: người xem gõ có dấu hoặc không dấu ("tro choi" phải ra "Trò chơi"), chịu được lỗi gõ, lọc theo thể loại và năm, chỉ thấy video `ready`. Dữ liệu nhỏ (catalog vài nghìn phim), không muốn thêm Elasticsearch/Meilisearch.

## Quyết định
- Migration `0003_search.sql`: extension `unaccent` và `pg_trgm`; hàm `immutable_unaccent(text)` gọi `public.unaccent('public.unaccent'::regdictionary, $1)` và khai báo `IMMUTABLE` (bản gốc `unaccent()` là `STABLE` nên không dùng được trong index). `unaccent` đổi cả `đ/Đ` thành `d/D`.
- Cột `videos.search tsvector` = `setweight(to_tsvector('simple', immutable_unaccent(title)), 'A') || setweight(…description…, 'B')`, cập nhật bằng trigger `BEFORE INSERT OR UPDATE OF title, description`; migration backfill video cũ. Cấu hình `simple` (không stemming, không stopword) vì tiếng Việt là ngôn ngữ đơn âm tiết.
- Index: GIN trên `search`; GIN `gin_trgm_ops` trên `immutable_unaccent(title)`.
- Truy vấn (`catalog.Repo.Search`): `websearch_to_tsquery('simple', immutable_unaccent(q))`, xếp hạng `ts_rank` (khớp tiêu đề cao hơn mô tả). **Chỉ khi full-text không có kết quả nào** (sau khi lọc `genre`, `year`, `status = 'ready'`) mới dùng trigram: `immutable_unaccent(q) <% immutable_unaccent(title)`, xếp theo `word_similarity`. Cả hai nhánh nằm trong một câu SQL (CTE) nên phân trang nhất quán.
- Dùng `word_similarity` / toán tử `<%` (ngưỡng mặc định 0.6) thay cho `similarity()` trong spec: từ khoá thường ngắn hơn tiêu đề nhiều, `similarity()` so cả chuỗi nên điểm thấp; `<%` vẫn dùng được index GIN trigram.
- `GET /api/videos` từ chối `q`/`genre` chứa UTF-8 không hợp lệ hoặc NUL bằng `400` (Postgres không nhận NUL trong text).
- `genre` khớp đúng một phần tử của `genres` (web lấy danh sách từ `GET /api/genres`); `year` khớp đúng năm.

## Hệ quả
- Gõ không dấu, có dấu, viết hoa đều như nhau; cú pháp websearch (`"cụm từ"`, `-loại trừ`, `or`) dùng được.
- Không có tìm theo tiền tố trong nhánh full-text: gõ dở "tro ch" rơi vào nhánh trigram (vẫn ra kết quả gần đúng). Bỏ dấu làm "ma" và "mà" trùng nhau — chấp nhận cho MVP.
- Khi catalog lớn hoặc cần gợi ý/tìm theo tiền tố, cân nhắc search engine riêng (Meilisearch/OpenSearch) được đồng bộ qua Kafka.
