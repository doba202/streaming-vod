import { fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type AdminVideo } from '../api';
import { AdminVideoPage } from './AdminVideoPage';

const base: AdminVideo = {
  id: 'v1',
  title: 'Phim cũ',
  description: 'mô tả',
  genres: ['drama'],
  year: 2020,
  status: 'ready',
  duration_s: 30,
  size_bytes: 1024,
  created_at: '2026-11-15T10:00:00Z',
  updated_at: '2026-11-15T10:00:00Z',
  renditions: [{ height: 720, bitrate_kbps: 2800 }],
  subtitles: [{ lang: 'vi', label: 'Tiếng Việt', vtt_key: 'v1/subs/vi.vtt' }],
};

function renderPage() {
  render(
    <MemoryRouter initialEntries={['/admin/videos/v1']}>
      <Routes>
        <Route path="/admin/videos/:id" element={<AdminVideoPage />} />
        <Route path="/admin" element={<p>danh sách</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('AdminVideoPage', () => {
  afterEach(() => vi.restoreAllMocks());

  it('hiện chi tiết, sửa metadata gửi PATCH đủ field', async () => {
    vi.spyOn(adminApi, 'get').mockResolvedValue(base);
    const update = vi.spyOn(adminApi, 'update').mockResolvedValue({ ...base, title: 'Phim mới' });
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Phim cũ' })).toBeInTheDocument();
    expect(screen.getByText('1 KB · 0:30 · 720p')).toBeInTheDocument();
    expect(screen.getByText('Tiếng Việt (vi)')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '▶ Xem phim' })).toHaveAttribute('href', '/watch/v1');

    const form = screen.getByRole('form', { name: 'Thông tin phim' });
    fireEvent.change(within(form).getByLabelText('Tiêu đề'), { target: { value: ' Phim mới ' } });
    fireEvent.change(within(form).getByLabelText(/Thể loại/), { target: { value: 'drama, hài, ' } });
    fireEvent.change(within(form).getByLabelText('Năm'), { target: { value: '' } });
    fireEvent.click(within(form).getByRole('button', { name: 'Lưu' }));
    expect(await within(form).findByText('Đã lưu.')).toBeInTheDocument();
    expect(update).toHaveBeenCalledWith('v1', { title: 'Phim mới', description: 'mô tả', genres: ['drama', 'hài'], year: 0 });
  });

  it('video failed: hiện lý do lỗi và thử lại', async () => {
    const get = vi
      .spyOn(adminApi, 'get')
      .mockResolvedValueOnce({ ...base, status: 'failed', fail_reason: 'transcode: input has no video stream' })
      .mockResolvedValue({ ...base, status: 'queued' });
    const retry = vi.spyOn(adminApi, 'retry').mockResolvedValue({ id: 'v1', status: 'queued' });
    renderPage();
    expect(await screen.findByText('transcode: input has no video stream')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Thử lại' }));
    expect(await screen.findByText('Chờ xử lý')).toBeInTheDocument();
    expect(retry).toHaveBeenCalledWith('v1');
    expect(get).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole('button', { name: 'Xoá video' })).toBeNull(); // queued: không xoá được
  });

  it('upload phụ đề kiểm tra mã ngôn ngữ rồi gọi API', async () => {
    vi.spyOn(adminApi, 'get').mockResolvedValue(base);
    const up = vi.spyOn(adminApi, 'uploadSubtitle').mockResolvedValue({ lang: 'en', label: 'English', vtt_key: 'v1/subs/en.vtt' });
    renderPage();
    const form = await screen.findByRole('form', { name: 'Phụ đề' });
    fireEvent.change(within(form).getByLabelText('Mã ngôn ngữ'), { target: { value: 'English' } });
    fireEvent.click(within(form).getByRole('button', { name: 'Upload phụ đề' }));
    expect(within(form).getByRole('status')).toHaveTextContent('Mã ngôn ngữ dạng');
    fireEvent.change(within(form).getByLabelText('Mã ngôn ngữ'), { target: { value: 'en' } });
    fireEvent.change(within(form).getByLabelText('Nhãn'), { target: { value: 'English' } });
    const file = new File(['WEBVTT\n'], 'en.vtt');
    fireEvent.change(within(form).getByLabelText(/File phụ đề/), { target: { files: [file] } });
    fireEvent.click(within(form).getByRole('button', { name: 'Upload phụ đề' }));
    expect(await within(form).findByText('Đã upload phụ đề en.')).toBeInTheDocument();
    expect(up).toHaveBeenCalledWith('v1', 'en', 'English', file);
  });

  it('xoá xong quay về danh sách', async () => {
    vi.spyOn(adminApi, 'get').mockResolvedValue(base);
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Xoá video' }));
    expect(await screen.findByText('danh sách')).toBeInTheDocument();
    expect(remove).toHaveBeenCalledWith('v1');
  });
});
