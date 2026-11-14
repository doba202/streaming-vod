import { useEffect, useState } from 'react';
import { api, type ContinueVideo, type Video } from '../api';
import { ContinueRow } from '../components/ContinueRow';
import { VideoGrid } from '../components/VideoGrid';

export function HomePage() {
  const [videos, setVideos] = useState<Video[]>([]);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [continueVideos, setContinueVideos] = useState<ContinueVideo[]>([]);

  // Hàng "Xem tiếp": lỗi thì chỉ ẩn hàng, không chặn catalog.
  useEffect(() => {
    let cancelled = false;
    api
      .continueWatching()
      .then((vs) => !cancelled && setContinueVideos(vs))
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    api
      .videos({ page })
      .then((res) => {
        if (cancelled) return;
        setVideos((prev) => (page === 1 ? res.videos : [...prev, ...res.videos]));
        setHasMore(res.has_more);
        setError('');
      })
      .catch(() => !cancelled && setError('Không tải được danh sách phim.'))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [page]);

  return (
    <section>
      <ContinueRow videos={continueVideos} />
      <h1>Phim mới</h1>
      {error && <p role="alert" className="form-error">{error}</p>}
      {!loading || videos.length > 0 ? <VideoGrid videos={videos} /> : <p className="status">Đang tải…</p>}
      {hasMore && (
        <button type="button" className="more" disabled={loading} onClick={() => setPage((p) => p + 1)}>
          {loading ? 'Đang tải…' : 'Xem thêm'}
        </button>
      )}
    </section>
  );
}
