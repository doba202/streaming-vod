import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ApiError, api, type Video } from '../api';
import { formatDuration, posterHue } from '../format';

export function VideoPage() {
  const { id = '' } = useParams();
  const [video, setVideo] = useState<Video | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setVideo(null);
    setError('');
    api
      .video(id)
      .then((v) => !cancelled && setVideo(v))
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof ApiError && err.status === 404 ? 'Không tìm thấy phim.' : 'Không tải được phim.');
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  if (error) return <p role="alert" className="form-error">{error}</p>;
  if (!video) return <p className="status">Đang tải…</p>;
  return (
    <article className="detail">
      <div className="poster large" style={{ background: `hsl(${posterHue(video.id)} 45% 30%)` }} aria-hidden="true">
        {video.title.charAt(0).toUpperCase()}
      </div>
      <div>
        <h1>{video.title}</h1>
        <p className="meta">{[video.year, formatDuration(video.duration_s)].filter(Boolean).join(' · ')}</p>
        {video.genres.length > 0 && (
          <ul className="tags">
            {video.genres.map((g) => (
              <li key={g}>
                <Link to={`/search?genre=${encodeURIComponent(g)}`}>{g}</Link>
              </li>
            ))}
          </ul>
        )}
        {video.description && <p className="description">{video.description}</p>}
        {video.renditions && video.renditions.length > 0 && (
          <p className="meta">Chất lượng: {video.renditions.map((r) => `${r.height}p`).join(', ')}</p>
        )}
        <Link to={`/watch/${video.id}`} className="button">
          ▶ Xem phim
        </Link>
      </div>
    </article>
  );
}
