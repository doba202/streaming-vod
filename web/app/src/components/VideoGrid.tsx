import { Link } from 'react-router-dom';
import type { Video } from '../api';
import { formatDuration } from '../format';
import { Poster } from './Poster';

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
            <Poster video={v} />
            <h3>{v.title}</h3>
            <p className="meta">{[v.year, formatDuration(v.duration_s)].filter(Boolean).join(' · ')}</p>
          </Link>
        </li>
      ))}
    </ul>
  );
}
