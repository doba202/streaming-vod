-- M4: tìm kiếm tiếng Việt có dấu và không dấu (spec §5, ADR 0012).
-- unaccent() là STABLE (phụ thuộc search_path và từ điển) nên không dùng trực tiếp trong index được;
-- immutable_unaccent ghim từ điển public.unaccent và khai báo IMMUTABLE.
CREATE EXTENSION IF NOT EXISTS unaccent;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE FUNCTION immutable_unaccent(text) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT
  RETURN public.unaccent('public.unaccent'::regdictionary, $1);

-- Tiêu đề trọng số A, mô tả trọng số B (ts_rank ưu tiên khớp tiêu đề). Cấu hình 'simple': không stemming,
-- không stopword — hợp với tiếng Việt (từ đơn âm tiết) sau khi bỏ dấu.
CREATE FUNCTION videos_search_tsv(title text, description text) RETURNS tsvector
  LANGUAGE sql IMMUTABLE PARALLEL SAFE
  RETURN setweight(to_tsvector('simple', immutable_unaccent(coalesce(title, ''))), 'A')
      || setweight(to_tsvector('simple', immutable_unaccent(coalesce(description, ''))), 'B');

ALTER TABLE videos ADD COLUMN search tsvector;

CREATE FUNCTION videos_search_trigger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.search := videos_search_tsv(NEW.title, NEW.description);
  RETURN NEW;
END
$$;

CREATE TRIGGER videos_search_update
  BEFORE INSERT OR UPDATE OF title, description ON videos
  FOR EACH ROW EXECUTE FUNCTION videos_search_trigger();

-- Backfill các video có từ trước migration.
UPDATE videos SET search = videos_search_tsv(title, description);

CREATE INDEX videos_search_idx ON videos USING gin (search);
-- Trigram trên tiêu đề đã bỏ dấu: dự phòng chịu lỗi gõ (toán tử <% / word_similarity).
CREATE INDEX videos_title_trgm_idx ON videos USING gin (immutable_unaccent(title) gin_trgm_ops);
