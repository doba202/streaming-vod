import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Boom(): never {
  throw new Error('Failed to fetch dynamically imported module');
}

// jsdom báo lỗi chưa bắt lên window ('error' event → "Error: Uncaught [...]"); chặn để output sạch.
const quiet = (e: Event) => e.preventDefault();

describe('ErrorBoundary', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    window.addEventListener('error', quiet);
  });
  afterEach(() => {
    // Dọn ở afterEach để test lỗi giữa chừng cũng không để listener/stub lại cho test sau.
    window.removeEventListener('error', quiet);
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('hiện thông báo và nút tải lại khi con ném lỗi', () => {
    const reload = vi.fn();
    vi.stubGlobal('location', { ...window.location, reload });
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByText('Không tải được trang. Tải lại?')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Tải lại' }));
    expect(reload).toHaveBeenCalled();
  });

  it('render bình thường khi không lỗi', () => {
    render(
      <ErrorBoundary>
        <p>ok</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('ok')).toBeInTheDocument();
  });

  it('đổi resetKey (sang route khác) thì bỏ trạng thái lỗi', () => {
    const { rerender } = render(
      <ErrorBoundary resetKey="/watch/1">
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByRole('alert')).toBeInTheDocument();
    rerender(
      <ErrorBoundary resetKey="/watch/1">
        <p>trang khác</p>
      </ErrorBoundary>,
    );
    expect(screen.getByRole('alert')).toBeInTheDocument(); // cùng key: vẫn giữ lỗi
    rerender(
      <ErrorBoundary resetKey="/search">
        <p>trang khác</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('trang khác')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
