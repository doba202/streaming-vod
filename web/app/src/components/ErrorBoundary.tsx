import { Component, type ErrorInfo, type ReactNode } from 'react';

// ErrorBoundary bọc các route tải lazy: sau khi deploy bản mới, chunk cũ không còn trên server nên
// import() lỗi; hiện thông báo và nút tải lại thay vì trang trắng.
export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('route render failed', error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div role="alert" className="status">
        <p>Không tải được trang. Tải lại?</p>
        <button type="button" onClick={() => window.location.reload()}>
          Tải lại
        </button>
      </div>
    );
  }
}
