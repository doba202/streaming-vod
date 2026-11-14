import { Link } from 'react-router-dom';
import type { ContinueVideo } from '../api';
import { formatDuration, progressPercent } from '../format';
import { Poster } from './Poster';

// ContinueRow: hàng "Xem tiếp" trên trang chủ (spec §4.4). Thẻ dẫn thẳng tới trang xem; player phát tiếp
// từ resume_at mà /play trả về. Không có video nào thì không hiện gì.
export function ContinueRow({ videos }: { videos: ContinueVideo[] }) {
  if (videos.length === 0) return null;
  return (
    <section className="continue" aria-labelledby="continue-heading">
      <h2 id="continue-heading">Xem tiếp</h2>
      <ul className="row">
        {videos.map((v) => {
          const pct = progressPercent(v.position_s, v.duration_s);
          return (
            <li key={v.id}>
              <Link to={`/watch/${v.id}`} className="card">
                <Poster video={v} />
                <div
                  className="progress"
                  role="progressbar"
                  aria-label={`Đã xem ${pct}%`}
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={pct}
                >
                  <span style={{ width: `${pct}%` }} />
                </div>
                <h3>{v.title}</h3>
                <p className="meta">Còn {formatDuration(Math.max(v.duration_s - v.position_s, 0)) || '0:00'}</p>
              </Link>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
