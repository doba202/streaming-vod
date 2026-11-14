-- M2: video và rendition. Cột search (unaccent + pg_trgm) thêm ở M4, users ở M3, subtitles/watch_progress ở M5.
CREATE TABLE videos (
  id          uuid PRIMARY KEY,
  title       text   NOT NULL,
  description text   NOT NULL DEFAULT '',
  genres      text[] NOT NULL DEFAULT '{}',
  year        int,
  status      text   NOT NULL CHECK (status IN ('uploading','queued','processing','ready','failed')),
  fail_reason text,
  duration_s  int,
  raw_key     text   NOT NULL,
  upload_id   text,
  size_bytes  bigint NOT NULL,
  hls_prefix  text,
  poster_key  text,
  sprite_key  text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX videos_created_at_idx ON videos (created_at DESC);
CREATE INDEX videos_status_created_at_idx ON videos (status, created_at);

CREATE TABLE renditions (
  video_id     uuid NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  height       int  NOT NULL,
  bitrate_kbps int  NOT NULL,
  playlist_key text NOT NULL,
  PRIMARY KEY (video_id, height)
);
