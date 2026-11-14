import { act, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type AdminVideo } from '../api';
import { AdminVideosPage, POLL_MS } from './AdminVideosPage';

const video = (id: string, status: AdminVideo['status'], extra: Partial<AdminVideo> = {}): AdminVideo => ({
  id,
  title: `Phim ${id}`,
  description: '',
  genres: [],
  status,
  size_bytes: 30 * 1024 * 1024,
  created_at: '2026-11-15T10:00:00Z',
  updated_at: '2026-11-15T10:00:00Z',
  ...extra,
});

function renderPage() {
  render(
    <MemoryRouter>
      <AdminVideosPage />
    </MemoryRouter>,
  );
}

describe('AdminVideosPage', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('hiện trạng thái, lý do lỗi, nút thử lại chỉ cho video failed, không cho xoá video đang xử lý', async () => {
    vi.spyOn(adminApi, 'list').mockResolvedValue([
      video('a', 'failed', { fail_reason: 'transcode: input has no video stream' }),
      video('b', 'ready'),
      video('c', 'processing'),
    ]);
    renderPage();
    expect(await screen.findByText('Lỗi')).toHaveClass('chip', 'chip-failed');
    expect(screen.getByText('Sẵn sàng')).toHaveClass('chip-ready');
    expect(screen.getByText('Đang xử lý')).toHaveClass('chip-processing');
    expect(screen.getByText('transcode: input has no video stream')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Phim a' })).toHaveAttribute('href', '/admin/videos/a');
    expect(screen.getAllByRole('button', { name: 'Thử lại' })).toHaveLength(1);
    expect(screen.getAllByRole('button', { name: 'Xoá' })).toHaveLength(2); // a, b; không có c
    expect(screen.getAllByText('30 MB')).toHaveLength(3);
  });

  it('thử lại gọi API rồi tải lại danh sách', async () => {
    const list = vi
      .spyOn(adminApi, 'list')
      .mockResolvedValueOnce([video('a', 'failed', { fail_reason: 'boom' })])
      .mockResolvedValue([video('a', 'queued')]);
    const retry = vi.spyOn(adminApi, 'retry').mockResolvedValue({ id: 'a', status: 'queued' });
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Thử lại' }));
    expect(await screen.findByText('Chờ xử lý')).toBeInTheDocument();
    expect(retry).toHaveBeenCalledWith('a');
    expect(list).toHaveBeenCalledTimes(2);
  });

  it('xoá cần xác nhận', async () => {
    vi.spyOn(adminApi, 'list').mockResolvedValue([video('b', 'ready')]);
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage();
    const button = await screen.findByRole('button', { name: 'Xoá' });
    fireEvent.click(button);
    expect(remove).not.toHaveBeenCalled();
    fireEvent.click(button);
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(remove).toHaveBeenCalledWith('b');
  });

  it('poll mỗi POLL_MS khi còn video queued/processing, dừng khi tất cả xong', async () => {
    vi.useFakeTimers();
    const list = vi
      .spyOn(adminApi, 'list')
      .mockResolvedValueOnce([video('a', 'processing')])
      .mockResolvedValueOnce([video('a', 'ready')])
      .mockResolvedValue([video('a', 'ready')]);
    renderPage();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText('Đang xử lý')).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS);
    });
    expect(screen.getByText('Sẵn sàng')).toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS * 3);
    });
    expect(list).toHaveBeenCalledTimes(2);
  });
});
