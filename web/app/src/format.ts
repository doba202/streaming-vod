// formatDuration: 75 → "1:15", 3723 → "1:02:03", 0 → "".
export function formatDuration(totalSeconds: number): string {
  if (!totalSeconds || totalSeconds < 0) return '';
  const s = Math.floor(totalSeconds);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = String(s % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
}

// posterHue: màu ổn định theo id cho poster tạm (poster thật có ở M5).
export function posterHue(id: string): number {
  let h = 0;
  for (const ch of id) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return h;
}
