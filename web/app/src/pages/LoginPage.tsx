import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth';
import { AuthForm } from '../components/AuthForm';

// returnTo: trang người dùng định mở trước khi bị chuyển tới /login (RequireAuth ghi vào state.from).
export function returnTo(state: unknown): string {
  const from = (state as { from?: unknown } | null)?.from;
  return typeof from === 'string' && from.startsWith('/') && !from.startsWith('//') && !from.includes('\\') ? from : '/';
}

export function LoginPage() {
  const { user, loading, login } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const next = returnTo(location.state);
  if (loading) return <p className="status">Đang tải…</p>;
  if (user) return <Navigate to={next} replace />;
  return (
    <main className="auth-page">
      <AuthForm
        title="Đăng nhập"
        submitLabel="Đăng nhập"
        onSubmit={async ({ email, password }) => {
          await login(email, password);
          navigate(next, { replace: true });
        }}
        footer={
          <p>
            Chưa có tài khoản? <Link to="/register" state={location.state}>Đăng ký</Link>
          </p>
        }
      />
    </main>
  );
}
