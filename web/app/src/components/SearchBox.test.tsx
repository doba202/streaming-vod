import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SearchBox } from './SearchBox';

describe('SearchBox', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('chỉ tìm một lần với từ khoá cuối, sau khi ngừng gõ 300 ms', () => {
    const onSearch = vi.fn();
    render(<SearchBox onSearch={onSearch} />);
    const input = screen.getByRole('searchbox', { name: 'Tìm phim' });

    for (const v of ['t', 'tr', 'tro', 'tro ', 'tro c']) {
      fireEvent.change(input, { target: { value: v } });
      act(() => {
        vi.advanceTimersByTime(100);
      });
    }
    expect(onSearch).not.toHaveBeenCalled();

    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(onSearch).toHaveBeenCalledTimes(1);
    expect(onSearch).toHaveBeenCalledWith('tro c');
  });

  it('không gọi khi giá trị ban đầu chưa đổi; Enter tìm ngay; không lặp lại cùng từ khoá', () => {
    const onSearch = vi.fn();
    render(<SearchBox initial="phim" onSearch={onSearch} />);
    const input = screen.getByRole('searchbox');
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onSearch).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: 'trò chơi  ' } });
    fireEvent.submit(input);
    expect(onSearch).toHaveBeenCalledWith('trò chơi');
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(onSearch).toHaveBeenCalledTimes(1);
  });
});
