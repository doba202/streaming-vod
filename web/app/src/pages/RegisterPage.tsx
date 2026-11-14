import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth';
import { AuthForm } from '../components/AuthForm';
import { returnTo } from './LoginPage';

export function RegisterPage() {
  const { user, loading, register } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const next = returnTo(location.state);
  if (loading) return <p className="status">Đang tải…</p>;
  if (user) return <Navigate to={next} replace />;
  return (
    <main className="auth-page">
      <AuthForm
        title="Tạo tài khoản"
        submitLabel="Đăng ký"
        mode="register"
        onSubmit={async ({ email, password }) => {
          // Đăng ký xong server đặt cookie luôn (M3), không cần đăng nhập lại.
          await register(email, password);
          navigate(next, { replace: true });
        }}
        footer={
          <p>
            Đã có tài khoản? <Link to="/login" state={location.state}>Đăng nhập</Link>
          </p>
        }
      />
    </main>
  );
}
