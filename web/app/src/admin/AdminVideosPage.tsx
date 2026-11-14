import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, adminApi, type AdminVideo } from '../api';
import { formatBytes } from '../format';
import { StatusChip, isProcessing } from './StatusChip';

// POLL_MS: chu kỳ tải lại danh sách khi còn video queued/processing.
export const POLL_MS = 3000;

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return 'Có lỗi xảy ra, vui lòng thử lại.';
}

// AdminVideosPage (/admin): mọi video mới nhất trước, trạng thái, lý do lỗi, retry và xoá.
export function AdminVideosPage() {
  const [videos, setVideos] = useState<AdminVideo[] | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState<string | null>(null); // id đang retry/xoá

  const load = useCallback(async () => {
    try {
      setVideos(await adminApi.list());
      setError('');
    } catch (err) {
      setError(`Không tải được danh sách: ${errorMessage(err)}`);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const polling = videos?.some((v) => isProcessing(v.status)) ?? false;
  useEffect(() => {
    if (!polling) return;
    const t = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(t);
  }, [polling, load]);

  const act = async (id: string, fn: () => Promise<unknown>) => {
    setBusy(id);
    try {
      await fn();
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  };
  const retry = (v: AdminVideo) => act(v.id, () => adminApi.retry(v.id));
  const remove = (v: AdminVideo) => {
    if (!window.confirm(`Xoá "${v.title}"? Video, HLS, phụ đề và tiến độ xem sẽ bị xoá vĩnh viễn.`)) return;
    void act(v.id, () => adminApi.remove(v.id));
  };

  return (
    <section className="admin">
      <div className="admin-head">
        <h1>Quản trị video</h1>
        <Link to="/admin/upload" className="button">
          Tải video lên
        </Link>
      </div>
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {!videos && !error && <p className="status">Đang tải…</p>}
      {videos && videos.length === 0 && <p className="empty">Chưa có video nào.</p>}
      {videos && videos.length > 0 && (
        <table className="admin-table">
          <thead>
            <tr>
              <th>Tiêu đề</th>
              <th>Trạng thái</th>
              <th>Dung lượng</th>
              <th>Tạo lúc</th>
              <th aria-label="Thao tác" />
            </tr>
          </thead>
          <tbody>
            {videos.map((v) => (
              <tr key={v.id}>
                <td>
                  <Link to={`/admin/videos/${v.id}`}>{v.title}</Link>
                  {v.status === 'failed' && v.fail_reason && <p className="fail-reason">{v.fail_reason}</p>}
                </td>
                <td>
                  <StatusChip status={v.status} />
                </td>
                <td>{formatBytes(v.size_bytes)}</td>
                <td>{new Date(v.created_at).toLocaleString('vi-VN')}</td>
                <td className="actions">
                  {v.status === 'failed' && (
                    <button type="button" disabled={busy === v.id} onClick={() => void retry(v)}>
                      Thử lại
                    </button>
                  )}
                  {!isProcessing(v.status) && (
                    <button type="button" className="danger" disabled={busy === v.id} onClick={() => remove(v)}>
                      Xoá
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {polling && <p className="meta">Đang xử lý video, tự cập nhật mỗi {POLL_MS / 1000} giây…</p>}
    </section>
  );
}
