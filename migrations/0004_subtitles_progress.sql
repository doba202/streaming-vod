-- M5: phụ đề và tiến độ xem (spec §4.2, §4.4, §5). Cột videos.poster_key / sprite_key có từ 0001.

-- Một phụ đề mỗi (video, ngôn ngữ); upload lại cùng lang thì ghi đè. vtt_key là key trong bucket hls:
-- {video_id}/subs/{lang}.vtt.
CREATE TABLE subtitles (
  video_id   uuid NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  lang       text NOT NULL,
  label      text NOT NULL,
  vtt_key    text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (video_id, lang)
);

-- Vị trí xem gần nhất của mỗi user với mỗi video. duration_s chép từ videos lúc ghi để lọc hàng
-- "Xem tiếp" (5% < position/duration < 95%) mà không cần tính lại.
CREATE TABLE watch_progress (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  video_id   uuid NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
  position_s int  NOT NULL CHECK (position_s >= 0),
  duration_s int  NOT NULL CHECK (duration_s >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, video_id)
);

-- Hàng "Xem tiếp": video của một user, mới xem nhất trước.
CREATE INDEX watch_progress_user_updated_idx ON watch_progress (user_id, updated_at DESC);
