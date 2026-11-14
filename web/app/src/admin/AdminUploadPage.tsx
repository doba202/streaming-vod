import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { adminApi } from '../api';
import { formatBytes, parseGenres } from '../format';
import { errorMessage } from './AdminVideosPage';
import { UploadVideoError, uploadVideo } from './upload';

// Cùng danh sách đuôi file và giới hạn với server (internal/upload: .mp4/.mov/.mkv/.webm, tối đa 10 GB).
export const VIDEO_ACCEPT = '.mp4,.mov,.mkv,.webm';
export const MAX_VIDEO_BYTES = 10 * 1024 ** 3;

// titleFromFilename: "tro_choi-con.muc.mp4" → "tro choi con.muc" (gợi ý tiêu đề, admin sửa được).
export function titleFromFilename(name: string): string {
  return name
    .replace(/\.[^.]+$/, '')
    .replace(/[_-]+/g, ' ')
    .trim();
}

// AdminUploadPage (/admin/upload): chọn file + metadata → upload multipart thẳng lên MinIO (thanh tiến độ)
// → complete → chuyển tới trang chi tiết để theo dõi transcode.
export function AdminUploadPage() {
  const navigate = useNavigate();
  const [file, setFile] = useState<File | null>(null);
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [genres, setGenres] = useState('');
  const [year, setYear] = useState('');
  const [progress, setProgress] = useState<{
    loaded: number;
    total: number;
  } | null>(null);
  const [error, setError] = useState('');
  const [failedId, setFailedId] = useState('');
  const abort = useRef<AbortController | null>(null);
  const cancelled = useRef(false);
  const createdId = useRef('');

  useEffect(() => () => abort.current?.abort(), []); // rời trang thì huỷ upload đang chạy

  const onFile = (f: File | null) => {
    setFile(f);
    if (f && !title) setTitle(titleFromFilename(f.name));
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setFailedId('');
    if (!file) return setError('Chọn file video.');
    if (file.size > MAX_VIDEO_BYTES) return setError('File tối đa 10 GB.');
    if (!title.trim()) return setError('Nhập tiêu đề.');
    const y = year.trim() ? Number(year) : 0;
    if (!Number.isInteger(y) || (y !== 0 && (y < 1888 || y > 2100))) return setError('Năm phải từ 1888 đến 2100.');

    abort.current = new AbortController();
    cancelled.current = false;
    createdId.current = '';
    setProgress({ loaded: 0, total: file.size });
    try {
      const id = await uploadVideo(
        file,
        {
          title: title.trim(),
          description: description.trim(),
          genres: parseGenres(genres),
          year: y || undefined,
        },
        {
          signal: abort.current.signal,
          onCreated: (id) => (createdId.current = id),
          onProgress: (loaded, total) => setProgress({ loaded, total }),
        },
      );
      navigate(`/admin/videos/${id}`);
    } catch (err) {
      setProgress(null);
      if (cancelled.current) {
        const id = err instanceof UploadVideoError ? err.videoId : createdId.current;
        if (!id) return setError('Đã huỷ upload.');
        try {
          await adminApi.remove(id);
          setError('Đã huỷ upload và xoá video dở.');
        } catch (e) {
          setFailedId(id);
          setError(`Đã huỷ upload nhưng không xoá được video dở: ${errorMessage(e)}`);
        }
        return;
      }
      if (err instanceof UploadVideoError) {
        setFailedId(err.videoId);
        setError(`Upload lỗi: ${err.message}`);
      } else {
        setError(`Không tạo được video: ${errorMessage(err)}`);
      }
    }
  };

  // cancel: dừng upload; submit() thấy cancelled thì xoá video `uploading` mồ côi (best effort, báo kết quả).
  const cancel = () => {
    cancelled.current = true;
    abort.current?.abort();
  };

  const discard = async () => {
    try {
      await adminApi.remove(failedId);
      setFailedId('');
      setError('Đã xoá video upload dở.');
    } catch (err) {
      setError(`Không xoá được: ${errorMessage(err)}`);
    }
  };

  const uploading = progress !== null;
  const percent = progress && progress.total > 0 ? Math.floor((progress.loaded / progress.total) * 100) : 0;
  return (
    <section className="admin">
      <p>
        <Link to="/admin">← Danh sách video</Link>
      </p>
      <h1>Tải video lên</h1>
      <form className="admin-form" onSubmit={submit} noValidate>
        <label>
          File video ({VIDEO_ACCEPT}, tối đa 10 GB)
          <input
            type="file"
            accept={VIDEO_ACCEPT}
            disabled={uploading}
            onChange={(e) => onFile(e.target.files?.[0] ?? null)}
          />
        </label>
        {file && <p className="meta">{`${file.name} · ${formatBytes(file.size)}`}</p>}
        <label>
          Tiêu đề
          <input value={title} maxLength={200} disabled={uploading} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label>
          Mô tả
          <textarea
            value={description}
            rows={3}
            disabled={uploading}
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        <label>
          Thể loại (cách nhau bằng dấu phẩy)
          <input value={genres} disabled={uploading} onChange={(e) => setGenres(e.target.value)} />
        </label>
        <label>
          Năm
          <input value={year} inputMode="numeric" disabled={uploading} onChange={(e) => setYear(e.target.value)} />
        </label>
        {progress && (
          <div className="upload-progress">
            <progress max={100} value={percent} aria-label="Tiến độ upload" />
            <span>
              {percent}% · {formatBytes(progress.loaded)} / {formatBytes(progress.total)}
            </span>
          </div>
        )}
        {error && (
          <p role="alert" className="form-error">
            {error}
          </p>
        )}
        {failedId && (
          <button type="button" className="danger" onClick={() => void discard()}>
            Xoá video upload dở
          </button>
        )}
        <button type="submit" disabled={uploading}>
          {uploading ? 'Đang upload…' : 'Upload'}
        </button>
        {uploading && (
          <button type="button" className="danger" onClick={cancel}>
            Huỷ upload
          </button>
        )}
      </form>
    </section>
  );
}
