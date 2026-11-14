-- Tỉ lệ xem hết theo phim: trong số người có sự kiện play, bao nhiêu người cũng có sự kiện ended.
-- Người chỉ có ended (play bị mất vì best-effort, ADR 0015) không được tính nên tỉ lệ luôn trong [0, 1].
SELECT
    video_id,
    countIf(played) AS started,
    countIf(played AND finished) AS completed,
    round(completed / started, 3) AS completion_rate
FROM
(
    SELECT video_id, user_id, max(type = 'play') AS played, max(type = 'ended') AS finished
    FROM playback_events
    GROUP BY video_id, user_id
)
GROUP BY video_id
HAVING started > 0
ORDER BY started DESC, video_id
