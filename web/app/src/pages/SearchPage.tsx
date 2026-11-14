import { useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { api, type VideoPage } from '../api';
import { SearchBox } from '../components/SearchBox';
import { VideoGrid } from '../components/VideoGrid';

// Trạng thái tìm kiếm nằm trên URL (/search?q=&genre=) nên tải lại trang hoặc chia sẻ link vẫn giữ kết quả.
export function SearchPage() {
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const genre = params.get('genre') ?? '';
  const [genres, setGenres] = useState<string[]>([]);
  const [result, setResult] = useState<VideoPage | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    api.genres().then(setGenres).catch(() => setGenres([]));
  }, []);

  useEffect(() => {
    let cancelled = false; // bỏ kết quả của truy vấn cũ về muộn
    api
      .videos({ q, genre })
      .then((res) => {
        if (!cancelled) {
          setResult(res);
          setError('');
        }
      })
      .catch(() => !cancelled && setError('Tìm kiếm thất bại, vui lòng thử lại.'));
    return () => {
      cancelled = true;
    };
  }, [q, genre]);

  const update = (next: { q?: string; genre?: string }) => {
    const merged = { q, genre, ...next };
    const p = new URLSearchParams();
    if (merged.q) p.set('q', merged.q);
    if (merged.genre) p.set('genre', merged.genre);
    setParams(p, { replace: true });
  };

  return (
    <section>
      <h1>Tìm kiếm</h1>
      <div className="search-bar">
        <SearchBox initial={q} onSearch={(value) => update({ q: value })} />
        <select aria-label="Thể loại" value={genre} onChange={(e) => update({ genre: e.target.value })}>
          <option value="">Mọi thể loại</option>
          {genres.map((g) => (
            <option key={g} value={g}>
              {g}
            </option>
          ))}
        </select>
      </div>
      {error && <p role="alert" className="form-error">{error}</p>}
      {result ? (
        <>
          {q && (
            <p className="status">
              {result.videos.length === 0
                ? `Không có kết quả cho “${q}”.`
                : `Kết quả cho “${q}”${result.has_more ? ' (hiện 20 phim đầu)' : ''}:`}
            </p>
          )}
          <VideoGrid videos={result.videos} emptyText="Không tìm thấy phim nào." />
        </>
      ) : (
        !error && <p className="status">Đang tìm…</p>
      )}
    </section>
  );
}
