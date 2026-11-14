import { Link } from 'react-router-dom';
import type { Video } from '../api';
import { formatDuration, posterHue } from '../format';

export interface VideoGridProps {
  videos: Video[];
  emptyText?: string;
}

export function VideoGrid({ videos, emptyText = 'Chưa có phim nào.' }: VideoGridProps) {
  if (videos.length === 0) return <p className="empty">{emptyText}</p>;
  return (
    <ul className="grid">
      {videos.map((v) => (
        <li key={v.id}>
          <Link to={`/videos/${v.id}`} className="card">
            {/* Poster thật (poster_key) có ở M5; tạm dùng khối màu theo id. */}
            <div className="poster" style={{ background: `hsl(${posterHue(v.id)} 45% 30%)` }} aria-hidden="true">
              {v.title.charAt(0).toUpperCase()}
            </div>
            <h3>{v.title}</h3>
            <p className="meta">{[v.year, formatDuration(v.duration_s)].filter(Boolean).join(' · ')}</p>
          </Link>
        </li>
      ))}
    </ul>
  );
}
