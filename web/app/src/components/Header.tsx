import { Link, NavLink, useNavigate } from 'react-router-dom';
import { useAuth } from '../auth';

export function Header() {
  const { user, logout } = useAuth();
  const navigate = useNavigate();

  const onLogout = async () => {
    await logout();
    navigate('/login', { replace: true });
  };

  return (
    <header className="site-header">
      <Link to="/" className="brand">
        Streamfilm
      </Link>
      {user && (
        <>
          <nav>
            <NavLink to="/" end>
              Trang chủ
            </NavLink>
            <NavLink to="/search">Tìm kiếm</NavLink>
            {user.role === 'admin' && <NavLink to="/admin">Quản trị</NavLink>}
          </nav>
          <div className="user">
            <span title={user.role === 'admin' ? 'Quản trị viên' : 'Người xem'}>{user.email}</span>
            <button type="button" className="link-button" onClick={onLogout}>
              Đăng xuất
            </button>
          </div>
        </>
      )}
    </header>
  );
}
