-- Tổng thời gian buffering theo bitrate (bitrate_kbps = 0: Safari HLS gốc, không biết bitrate)
SELECT
    bitrate_kbps,
    count() AS buffering_events,
    sum(buffering_ms) AS total_buffering_ms,
    round(avg(buffering_ms)) AS avg_buffering_ms
FROM playback_events
WHERE type = 'buffering'
GROUP BY bitrate_kbps
ORDER BY bitrate_kbps
