import { type KeyboardEvent, type MouseEvent, type RefObject, useEffect, useState } from 'react';
import { formatDuration } from '../format';
import { type ThumbCue, findThumb } from '../player/thumbnails';

export interface SeekPreviewProps {
  videoRef: RefObject<HTMLVideoElement | null>;
  cues: ThumbCue[];
}

interface Hover {
  t: number;
  left: number; // px trong thanh
}

// SeekPreview: thanh tua phụ dưới video. Rê chuột → ảnh xem trước (một ô của tấm sprite, cắt bằng
// background-position) và mốc thời gian; bấm → tua tới đó; phím ←/→ tua 10 giây. Thanh điều khiển gốc của
// <video> vẫn giữ nguyên (trình duyệt không cho gắn ảnh xem trước vào thanh gốc).
export function SeekPreview({ videoRef, cues }: SeekPreviewProps) {
  const [duration, setDuration] = useState(0);
  const [current, setCurrent] = useState(0);
  const [hover, setHover] = useState<Hover | null>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const sync = () => {
      setDuration(Number.isFinite(video.duration) ? video.duration : 0);
      setCurrent(video.currentTime);
    };
    sync();
    const events = ['loadedmetadata', 'durationchange', 'timeupdate', 'seeked'] as const;
    events.forEach((e) => video.addEventListener(e, sync));
    return () => events.forEach((e) => video.removeEventListener(e, sync));
  }, [videoRef]);

  if (duration <= 0) return null;

  const timeAt = (e: MouseEvent<HTMLDivElement>): Hover => {
    const rect = e.currentTarget.getBoundingClientRect();
    const left = Math.min(Math.max(e.clientX - rect.left, 0), rect.width);
    return { t: rect.width > 0 ? (left / rect.width) * duration : 0, left };
  };
  const seek = (t: number) => {
    const video = videoRef.current;
    if (video) video.currentTime = Math.min(Math.max(t, 0), duration);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'ArrowRight') seek(current + 10);
    else if (e.key === 'ArrowLeft') seek(current - 10);
    else return;
    e.preventDefault();
  };

  const thumb = hover ? findThumb(cues, hover.t) : null;
  return (
    <div
      className="scrub"
      role="slider"
      tabIndex={0}
      aria-label="Tua video"
      aria-valuemin={0}
      aria-valuemax={Math.round(duration)}
      aria-valuenow={Math.round(current)}
      aria-valuetext={formatDuration(current) || '0:00'}
      onMouseMove={(e) => setHover(timeAt(e))}
      onMouseLeave={() => setHover(null)}
      onClick={(e) => seek(timeAt(e).t)}
      onKeyDown={onKeyDown}
    >
      <div className="scrub-played" style={{ width: `${(current / duration) * 100}%` }} />
      {hover && (
        <div className="scrub-preview" style={{ left: hover.left }} data-testid="seek-preview">
          {thumb && (
            <div
              className="scrub-thumb"
              style={{
                width: thumb.w,
                height: thumb.h,
                backgroundImage: `url("${thumb.url}")`,
                backgroundPosition: `${-thumb.x}px ${-thumb.y}px`,
              }}
            />
          )}
          <span>{formatDuration(hover.t) || '0:00'}</span>
        </div>
      )}
    </div>
  );
}
