import { fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ErrorBoundary } from './ErrorBoundary';

function Boom(): never {
  throw new Error('Failed to fetch dynamically imported module');
}

describe('ErrorBoundary', () => {
  afterEach(() => vi.restoreAllMocks());

  it('hiện thông báo và nút tải lại khi con ném lỗi', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    // jsdom báo lỗi chưa bắt lên window ('error' event → "Error: Uncaught [...]"); chặn để output sạch.
    const quiet = (e: Event) => e.preventDefault();
    window.addEventListener('error', quiet);
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
    vi.unstubAllGlobals();
    window.removeEventListener('error', quiet);
  });

  it('render bình thường khi không lỗi', () => {
    render(
      <ErrorBoundary>
        <p>ok</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText('ok')).toBeInTheDocument();
  });
});
