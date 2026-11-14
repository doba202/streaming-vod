import { fireEvent, render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Poster } from './Poster';

const id = '11111111-1111-1111-1111-111111111111';

describe('Poster', () => {
  it('có poster_url thì hiện ảnh (lazy)', () => {
    const { container } = render(<Poster video={{ id, title: 'Phim', poster_url: '/cdn/s/x/1/hls/v/poster.jpg' }} />);
    const img = container.querySelector('img.poster');
    expect(img).toHaveAttribute('src', '/cdn/s/x/1/hls/v/poster.jpg');
    expect(img).toHaveAttribute('loading', 'lazy');
  });

  it('không có poster_url hoặc ảnh lỗi thì hiện khối màu với chữ cái đầu', () => {
    const { container, rerender } = render(<Poster video={{ id, title: 'đường về' }} large />);
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('.poster.large')).toHaveTextContent('Đ');

    rerender(<Poster video={{ id, title: 'đường về', poster_url: '/cdn/s/x/1/hls/v/poster.jpg' }} />);
    fireEvent.error(container.querySelector('img')!);
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('.poster')).toHaveTextContent('Đ');
  });
});
