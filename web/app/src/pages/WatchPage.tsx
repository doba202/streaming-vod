import { useEffect, useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { api, type Video } from '../api';
import { Player } from '../components/Player';

export function WatchPage() {
  const { id = '' } = useParams();
  const [video, setVideo] = useState<Video | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .video(id)
      .then((v) => !cancelled && setVideo(v))
      .catch(() => !cancelled && setVideo(null));
    return () => {
      cancelled = true;
    };
  }, [id]);

  return (
    <section className="watch">
      <p>
        <Link to={`/videos/${id}`}>← Quay lại</Link>
      </p>
      {video && <h1>{video.title}</h1>}
      <Player videoId={id} />
    </section>
  );
}
