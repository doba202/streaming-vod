import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { Video } from '../api';
import { VideoGrid } from './VideoGrid';

const video = (id: string, title: string, extra: Partial<Video> = {}): Video => ({
  id,
  title,
  description: '',
  genres: [],
  duration_s: 0,
  created_at: '2026-11-15T00:00:00Z',
  ...extra,
});

describe('VideoGrid', () => {
  it('hiện mỗi phim một thẻ dẫn tới trang chi tiết', () => {
    render(
      <MemoryRouter>
        <VideoGrid
          videos={[
            video('11111111-1111-1111-1111-111111111111', 'Trò chơi con mực', { year: 2021, duration_s: 3723 }),
            video('22222222-2222-2222-2222-222222222222', 'Đường về nhà'),
          ]}
        />
      </MemoryRouter>,
    );
    const links = screen.getAllByRole('link');
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveAttribute('href', '/videos/11111111-1111-1111-1111-111111111111');
    expect(screen.getByRole('heading', { name: 'Trò chơi con mực' })).toBeInTheDocument();
    expect(screen.getByText('2021 · 1:02:03')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Đường về nhà' })).toBeInTheDocument();
  });

  it('danh sách rỗng hiện thông báo', () => {
    render(
      <MemoryRouter>
        <VideoGrid videos={[]} emptyText="Không tìm thấy phim nào." />
      </MemoryRouter>,
    );
    expect(screen.getByText('Không tìm thấy phim nào.')).toBeInTheDocument();
    expect(screen.queryByRole('list')).not.toBeInTheDocument();
  });
});
