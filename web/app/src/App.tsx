import { Suspense, lazy } from 'react';
import { Link, Outlet, Route, Routes } from 'react-router-dom';
import { RequireAuth } from './auth';
import { Header } from './components/Header';
import { HomePage } from './pages/HomePage';
import { LoginPage } from './pages/LoginPage';
import { RegisterPage } from './pages/RegisterPage';
import { SearchPage } from './pages/SearchPage';
import { VideoPage } from './pages/VideoPage';

// Trang xem tải lazy: hls.js (~500 kB) chỉ được tải khi người dùng mở player.
const WatchPage = lazy(() => import('./pages/WatchPage').then((m) => ({ default: m.WatchPage })));

function Layout() {
  return (
    <>
      <Header />
      <main className="content">
        <Suspense fallback={<p className="status">Đang tải…</p>}>
          <Outlet />
        </Suspense>
      </main>
    </>
  );
}

function NotFound() {
  return (
    <main className="content">
      <h1>Không tìm thấy trang</h1>
      <Link to="/">Về trang chủ</Link>
    </main>
  );
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/register" element={<RegisterPage />} />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route index element={<HomePage />} />
        <Route path="search" element={<SearchPage />} />
        <Route path="videos/:id" element={<VideoPage />} />
        <Route path="watch/:id" element={<WatchPage />} />
      </Route>
      <Route path="*" element={<NotFound />} />
    </Routes>
  );
}
