import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { ApiError, adminApi, type AdminVideo } from '../api';
import { formatBytes, formatDuration, parseGenres } from '../format';
import { POLL_MS, errorMessage } from './AdminVideosPage';
import { StatusChip, isProcessing } from './StatusChip';

// Cùng quy tắc với server (catalog.LangRe): "vi", "en", "pt-BR".
export const LANG_RE = /^[a-z]{2,3}(-[A-Z]{2})?$/;

function MetadataForm({ video, onSaved }: { video: AdminVideo; onSaved: () => void }) {
  const [title, setTitle] = useState(video.title);
  const [description, setDescription] = useState(video.description);
  const [genres, setGenres] = useState(video.genres.join(', '));
  const [year, setYear] = useState(video.year ? String(video.year) : '');
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const y = year.trim() ? Number(year) : 0;
    if (!title.trim()) return setMsg('Nhập tiêu đề.');
    if (!Number.isInteger(y) || (y !== 0 && (y < 1888 || y > 2100))) return setMsg('Năm phải từ 1888 đến 2100.');
    setSaving(true);
    try {
      await adminApi.update(video.id, {
        title: title.trim(),
        description: description.trim(),
        genres: parseGenres(genres),
        year: y,
      });
      onSaved();
      setMsg('Đã lưu.');
    } catch (err) {
      setMsg(`Không lưu được: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form className="admin-form" onSubmit={submit} noValidate aria-label="Thông tin phim">
      <h2>Thông tin phim</h2>
      <label>
        Tiêu đề
        <input value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} />
      </label>
      <label>
        Mô tả
        <textarea value={description} rows={3} onChange={(e) => setDescription(e.target.value)} />
      </label>
      <label>
        Thể loại (cách nhau bằng dấu phẩy)
        <input value={genres} onChange={(e) => setGenres(e.target.value)} />
      </label>
      <label>
        Năm
        <input value={year} inputMode="numeric" onChange={(e) => setYear(e.target.value)} />
      </label>
      {msg && <p role="status">{msg}</p>}
      <button type="submit" disabled={saving}>
        Lưu
      </button>
    </form>
  );
}

function SubtitleForm({ videoId, onUploaded }: { videoId: string; onUploaded: () => void }) {
  const [lang, setLang] = useState('vi');
  const [label, setLabel] = useState('Tiếng Việt');
  const [file, setFile] = useState<File | null>(null);
  const [msg, setMsg] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!LANG_RE.test(lang)) return setMsg('Mã ngôn ngữ dạng "vi", "en" hoặc "pt-BR".');
    if (!label.trim()) return setMsg('Nhập nhãn hiển thị.');
    if (!file) return setMsg('Chọn file .srt hoặc .vtt.');
    setSaving(true);
    try {
      await adminApi.uploadSubtitle(videoId, lang, label.trim(), file);
      setMsg(`Đã upload phụ đề ${lang}.`);
      onUploaded();
    } catch (err) {
      setMsg(`Không upload được: ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form className="admin-form" onSubmit={submit} noValidate aria-label="Phụ đề">
      <h2>Thêm phụ đề</h2>
      <label>
        Mã ngôn ngữ
        <input value={lang} onChange={(e) => setLang(e.target.value)} />
      </label>
      <label>
        Nhãn
        <input value={label} maxLength={50} onChange={(e) => setLabel(e.target.value)} />
      </label>
      <label>
        File phụ đề (.srt, .vtt, tối đa 1 MiB)
        <input type="file" accept=".srt,.vtt" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
      </label>
      {msg && <p role="status">{msg}</p>}
      <button type="submit" disabled={saving}>
        Upload phụ đề
      </button>
    </form>
  );
}

// AdminVideoPage (/admin/videos/:id): trạng thái (tự cập nhật khi đang xử lý), lý do lỗi + thử lại,
// sửa metadata, phụ đề, xoá.
export function AdminVideoPage() {
  const { id = '' } = useParams();
  const navigate = useNavigate();
  const [video, setVideo] = useState<AdminVideo | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setVideo(await adminApi.get(id));
      setError('');
    } catch (err) {
      setError(err instanceof ApiError && err.status === 404 ? 'Không tìm thấy video.' : errorMessage(err));
    }
  }, [id]);

  useEffect(() => {
    setVideo(null);
    void load();
  }, [load]);

  const polling = video ? isProcessing(video.status) : false;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(t);
  }, [polling, load]);

  const retry = async () => {
    try {
      await adminApi.retry(id);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const remove = async () => {
    if (!video || !window.confirm(`Xoá "${video.title}"? Không hoàn tác được.`)) return;
    try {
      await adminApi.remove(id);
      navigate('/admin', { replace: true });
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  if (!video) {
    return error ? (
      <p role="alert" className="form-error">
        {error}
      </p>
    ) : (
      <p className="status">Đang tải…</p>
    );
  }
  return (
    <section className="admin">
      <p>
        <Link to="/admin">← Danh sách video</Link>
      </p>
      <div className="admin-head">
        <h1>{video.title}</h1>
        <StatusChip status={video.status} />
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {video.status === 'failed' && (
        <div className="fail-box">
          <p className="fail-reason">{video.fail_reason || 'Không rõ lý do.'}</p>
          <button type="button" onClick={() => void retry()}>
            Thử lại
          </button>
        </div>
      )}
      {polling && <p className="meta">Đang xử lý, tự cập nhật mỗi {POLL_MS / 1000} giây…</p>}
      <p className="meta">
        {[
          formatBytes(video.size_bytes),
          video.duration_s ? formatDuration(video.duration_s) : '',
          video.renditions?.map((r) => `${r.height}p`).join(', '),
        ]
          .filter(Boolean)
          .join(' · ')}
      </p>
      {video.status === 'ready' && (
        <p>
          <Link to={`/watch/${video.id}`} className="button">
            ▶ Xem phim
          </Link>
        </p>
      )}
      <div className="admin-columns">
        {/* key theo id: poll hay lưu xong không xoá chữ admin đang gõ dở */}
        <MetadataForm key={video.id} video={video} onSaved={() => void load()} />
        <div>
          <h2>Phụ đề</h2>
          {video.subtitles && video.subtitles.length > 0 ? (
            <ul className="subtitle-list">
              {video.subtitles.map((s) => (
                <li key={s.lang}>
                  {s.label} ({s.lang})
                </li>
              ))}
            </ul>
          ) : (
            <p className="meta">Chưa có phụ đề.</p>
          )}
          <SubtitleForm videoId={video.id} onUploaded={() => void load()} />
        </div>
      </div>
      {!isProcessing(video.status) && (
        <p>
          <button type="button" className="danger" onClick={() => void remove()}>
            Xoá video
          </button>
        </p>
      )}
    </section>
  );
}
