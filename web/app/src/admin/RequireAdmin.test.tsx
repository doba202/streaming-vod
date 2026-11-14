import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, type User } from '../api';
import { AuthProvider } from '../auth';
import { Header } from '../components/Header';
import { RequireAdmin } from './RequireAdmin';

const user = (role: User['role']): User => ({ id: 'u1', email: `${role}@x.vn`, role, created_at: '' });

function renderAs(role: User['role']) {
  vi.spyOn(api, 'me').mockResolvedValue(user(role));
  render(
    <MemoryRouter>
      <AuthProvider>
        <Header />
        <RequireAdmin>
          <p>khu vực admin</p>
        </RequireAdmin>
      </AuthProvider>
    </MemoryRouter>,
  );
}

describe('RequireAdmin', () => {
  afterEach(() => vi.restoreAllMocks());

  it('viewer thấy trang 403, không thấy link Quản trị', async () => {
    renderAs('viewer');
    expect(await screen.findByRole('heading', { name: '403 — Không có quyền' })).toBeInTheDocument();
    expect(screen.queryByText('khu vực admin')).toBeNull();
    expect(screen.queryByRole('link', { name: 'Quản trị' })).toBeNull();
  });

  it('admin thấy nội dung và link Quản trị trên header', async () => {
    renderAs('admin');
    expect(await screen.findByText('khu vực admin')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Quản trị' })).toHaveAttribute('href', '/admin');
  });
});
