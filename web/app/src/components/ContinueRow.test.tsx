import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { ContinueVideo } from '../api';
import { ContinueRow } from './ContinueRow';

const cv = (id: string, title: string, position_s: number, duration_s: number): ContinueVideo => ({
  id,
  title,
  description: '',
  genres: [],
  duration_s,
  created_at: '2026-11-15T00:00:00Z',
  position_s,
  watched_at: '2026-11-15T10:00:00Z',
});

describe('ContinueRow', () => {
  it('mỗi video một thẻ dẫn tới trang xem, có thanh tiến độ và thời gian còn lại', () => {
    render(
      <MemoryRouter>
        <ContinueRow
          videos={[
            cv('11111111-1111-1111-1111-111111111111', 'Trò chơi con mực', 1800, 3600),
            cv('22222222-2222-2222-2222-222222222222', 'Đường về nhà', 12, 30),
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { name: 'Xem tiếp' })).toBeInTheDocument();
    const links = screen.getAllByRole('link');
    expect(links[0]).toHaveAttribute('href', '/watch/11111111-1111-1111-1111-111111111111');
    const bars = screen.getAllByRole('progressbar');
    expect(bars[0]).toHaveAttribute('aria-valuenow', '50');
    expect(bars[1]).toHaveAttribute('aria-valuenow', '40');
    expect(screen.getByText('Còn 30:00')).toBeInTheDocument();
    expect(screen.getByText('Còn 0:18')).toBeInTheDocument();
  });

  it('rỗng thì không hiện gì', () => {
    const { container } = render(
      <MemoryRouter>
        <ContinueRow videos={[]} />
      </MemoryRouter>,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
