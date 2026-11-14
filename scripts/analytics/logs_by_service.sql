-- Số dòng log theo service và level trong 24 giờ qua (WARN/ERROR là chỗ cần xem trước)
SELECT
    service,
    level,
    count() AS lines,
    max(ts) AS last_seen
FROM app_logs
WHERE ts > now64(3) - INTERVAL 1 DAY
GROUP BY service, level
ORDER BY service, level
