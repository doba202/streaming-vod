import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi } from '../api';
import { AdminUploadPage, titleFromFilename } from './AdminUploadPage';
import * as upload from './upload';

function renderPage() {
  render(
    <MemoryRouter initialEntries={['/admin/upload']}>
      <Routes>
        <Route path="/admin/upload" element={<AdminUploadPage />} />
        <Route path="/admin/videos/:id" element={<p>trang chi tiết</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

const pick = (file: File) =>
  fireEvent.change(screen.getByLabelText(/File video/), {
    target: { files: [file] },
  });

describe('AdminUploadPage', () => {
  afterEach(() => vi.restoreAllMocks());

  it('titleFromFilename bỏ đuôi và gạch nối', () => {
    expect(titleFromFilename('tro_choi-con-muc.mp4')).toBe('tro choi con muc');
  });

  it('upload với metadata, hiện tiến độ rồi chuyển tới trang chi tiết', async () => {
    let resolve!: (id: string) => void;
    const spy = vi.spyOn(upload, 'uploadVideo').mockImplementation((_f, _m, opts) => {
      opts?.onProgress?.(5, 10);
      return new Promise((r) => (resolve = r));
    });
    renderPage();
    pick(new File(['0123456789'], 'phim_hay.mp4'));
    expect(screen.getByLabelText('Tiêu đề')).toHaveValue('phim hay');
    fireEvent.change(screen.getByLabelText(/Thể loại/), {
      target: { value: 'drama, hài' },
    });
    fireEvent.change(screen.getByLabelText('Năm'), {
      target: { value: '2024' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));

    expect(await screen.findByText(/^50%/)).toBeInTheDocument();
    expect(screen.getByRole('progressbar', { name: 'Tiến độ upload' })).toHaveAttribute('value', '50');
    expect(screen.getByRole('button', { name: 'Đang upload…' })).toBeDisabled();
    const [file, meta] = spy.mock.calls[0];
    expect(file.name).toBe('phim_hay.mp4');
    expect(meta).toEqual({
      title: 'phim hay',
      description: '',
      genres: ['drama', 'hài'],
      year: 2024,
    });

    resolve('v1');
    expect(await screen.findByText('trang chi tiết')).toBeInTheDocument();
  });

  it('kiểm tra form trước khi gọi API', () => {
    const spy = vi.spyOn(upload, 'uploadVideo');
    renderPage();
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Chọn file video.');
    pick(new File(['x'], 'a.mp4'));
    fireEvent.change(screen.getByLabelText('Năm'), { target: { value: '99' } });
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Năm phải từ 1888 đến 2100.');
    expect(spy).not.toHaveBeenCalled();
  });

  it('upload lỗi sau khi đã tạo video: cho xoá video dở', async () => {
    vi.spyOn(upload, 'uploadVideo').mockRejectedValue(new upload.UploadVideoError('v9', new Error('part 1: HTTP 403')));
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    renderPage();
    pick(new File(['x'], 'a.mp4'));
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Upload lỗi: part 1: HTTP 403');
    fireEvent.click(screen.getByRole('button', { name: 'Xoá video upload dở' }));
    expect(await screen.findByText('Đã xoá video upload dở.')).toBeInTheDocument();
    expect(remove).toHaveBeenCalledWith('v9');
  });

  // uploadVideo giả: treo tới khi signal bị huỷ rồi ném UploadVideoError (hoặc lỗi thường nếu chưa có id).
  const hang = (id: string | null) =>
    vi.spyOn(upload, 'uploadVideo').mockImplementation(
      (_f, _m, opts) =>
        new Promise((_r, reject) => {
          if (id) opts?.onCreated?.(id);
          opts?.signal?.addEventListener('abort', () =>
            reject(
              id
                ? new upload.UploadVideoError(id, new DOMException('aborted', 'AbortError'))
                : new DOMException('aborted', 'AbortError'),
            ),
          );
        }),
    );

  it('Huỷ upload: huỷ rồi xoá video dở', async () => {
    hang('v7');
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    renderPage();
    pick(new File(['x'], 'a.mp4'));
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Huỷ upload' }));
    expect(await screen.findByText('Đã huỷ upload và xoá video dở.')).toBeInTheDocument();
    expect(remove).toHaveBeenCalledWith('v7');
    expect(screen.queryByRole('button', { name: 'Huỷ upload' })).not.toBeInTheDocument();
  });

  it('Huỷ upload: xoá lỗi thì báo và cho xoá lại', async () => {
    hang('v8');
    vi.spyOn(adminApi, 'remove').mockRejectedValue(new Error('boom'));
    renderPage();
    pick(new File(['x'], 'a.mp4'));
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Huỷ upload' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Đã huỷ upload nhưng không xoá được video dở');
    expect(screen.getByRole('button', { name: 'Xoá video upload dở' })).toBeInTheDocument();
  });

  it('Huỷ upload trước khi có video: không gọi xoá', async () => {
    hang(null);
    const remove = vi.spyOn(adminApi, 'remove').mockResolvedValue(undefined);
    renderPage();
    pick(new File(['x'], 'a.mp4'));
    fireEvent.click(screen.getByRole('button', { name: 'Upload' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Huỷ upload' }));
    expect(await screen.findByText('Đã huỷ upload.')).toBeInTheDocument();
    expect(remove).not.toHaveBeenCalled();
  });
});
