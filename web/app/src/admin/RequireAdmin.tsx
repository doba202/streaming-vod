import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useAuth } from '../auth';

// RequireAdmin: đặt bên trong RequireAuth. Viewer thấy trang 403 (server cũng trả 403 cho /api/admin/*,
// đây chỉ là giao diện).
export function RequireAdmin({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  if (user?.role !== 'admin') {
    return (
      <div role="alert" className="status">
        <h1>403 — Không có quyền</h1>
        <p>Trang quản trị chỉ dành cho tài khoản admin.</p>
        <Link to="/">Về trang chủ</Link>
      </div>
    );
  }
  return <>{children}</>;
}
