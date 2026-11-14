// Đọc thumbnails.vtt do transcoder sinh (spec §4.1): mỗi cue ánh xạ một khoảng thời gian tới một ô trong
// tấm sprite, dạng "sprite_001.jpg#xywh=x,y,w,h". Đường dẫn sprite tương đối → resolve theo URL của file VTT
// nên giữ nguyên prefix ký /cdn/s/{sig}/{exp}/hls/{id}/.

export interface ThumbCue {
  start: number; // giây
  end: number;
  url: string; // URL tuyệt đối của tấm sprite
  x: number;
  y: number;
  w: number;
  h: number;
}

// parseVttTime: "01:02:03.500" hoặc "02:03.500" → giây; sai dạng → null.
export function parseVttTime(s: string): number | null {
  const m = /^(?:(\d+):)?([0-5]\d):([0-5]\d)\.(\d{3})$/.exec(s.trim());
  if (!m) return null;
  return Number(m[1] ?? 0) * 3600 + Number(m[2]) * 60 + Number(m[3]) + Number(m[4]) / 1000;
}

const xywhRe = /^(.+)#xywh=(\d+),(\d+),(\d+),(\d+)$/;

export function parseThumbnailsVtt(text: string, baseUrl: string): ThumbCue[] {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  const cues: ThumbCue[] = [];
  for (let i = 0; i < lines.length; i++) {
    const [a, b] = lines[i].split('-->');
    if (b === undefined) continue;
    const start = parseVttTime(a);
    const end = parseVttTime(b.trim().split(/\s+/)[0]);
    const m = xywhRe.exec((lines[i + 1] ?? '').trim());
    if (start === null || end === null || !m) continue;
    let url: string;
    try {
      url = new URL(m[1], baseUrl).toString();
    } catch {
      continue;
    }
    cues.push({ start, end, url, x: Number(m[2]), y: Number(m[3]), w: Number(m[4]), h: Number(m[5]) });
  }
  return cues.sort((p, q) => p.start - q.start);
}

// findThumb: cue chứa thời điểm t (start ≤ t < end); sau cue cuối thì trả cue cuối. Tìm nhị phân.
export function findThumb(cues: ThumbCue[], t: number): ThumbCue | null {
  if (cues.length === 0 || t < cues[0].start) return null;
  let lo = 0;
  let hi = cues.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (cues[mid].start <= t) lo = mid;
    else hi = mid - 1;
  }
  return cues[lo];
}
