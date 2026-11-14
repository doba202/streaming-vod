// formatDuration: 75 → "1:15", 3723 → "1:02:03", 0 → "".
export function formatDuration(totalSeconds: number): string {
  if (!totalSeconds || totalSeconds < 0) return '';
  const s = Math.floor(totalSeconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

// progressPercent: phần trăm đã xem (0–100, làm tròn) cho thanh tiến độ của hàng "Xem tiếp".
export function progressPercent(positionS: number, durationS: number): number {
  if (!durationS || durationS <= 0) return 0;
  return Math.round(Math.min(Math.max(positionS / durationS, 0), 1) * 100);
}

// posterHue: màu ổn định theo id cho poster tạm (video transcode trước M5 hoặc ảnh không tải được).
export function posterHue(id: string): number {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return h;
}

// formatBytes: 0 → "0 B", 1536 → "1.5 KB", 31_457_280 → "30 MB" (cơ số 1024, tối đa một chữ số thập phân).
export function formatBytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${i === 0 ? v : Number(v.toFixed(1))} ${units[i]}`;
}

// parseGenres: "drama, Hài ,, drama" → ["drama", "Hài"] (tách theo dấu phẩy, bỏ rỗng và trùng).
export function parseGenres(s: string): string[] {
  return [...new Set(s.split(',').map((g) => g.trim()).filter(Boolean))];
}
