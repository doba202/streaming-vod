-- Tỉ lệ xem hết theo phim: số người có sự kiện ended / số người có sự kiện play
SELECT
    video_id,
    uniqExactIf(user_id, type = 'play') AS started,
    uniqExactIf(user_id, type = 'ended') AS completed,
    round(completed / started, 3) AS completion_rate
FROM playback_events
GROUP BY video_id
HAVING started > 0
ORDER BY started DESC, video_id
LIMIT 10
