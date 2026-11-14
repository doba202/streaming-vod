-- Top 10 phim theo lượt play (mỗi sự kiện play là một lượt) và số người xem khác nhau
SELECT
    video_id,
    count() AS plays,
    uniqExact(user_id) AS viewers
FROM playback_events
WHERE type = 'play'
GROUP BY video_id
ORDER BY plays DESC, video_id
LIMIT 10
