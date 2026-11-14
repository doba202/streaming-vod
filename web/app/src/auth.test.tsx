import { act, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from './api';
import { AuthProvider, useAuth } from './auth';
import { LoginPage } from './pages/LoginPage';

const user = { id: 'u1', email: 'a@b.vn', role: 'viewer' } as never;

function Probe({ onAuth }: { onAuth: (a: ReturnType<typeof useAuth>) => void }) {
  const a = useAuth();
  onAuth(a);
  return <p>{a.user ? 'in' : 'out'}</p>;
}

describe('AuthProvider', () => {
  afterEach(() => vi.restoreAllMocks());

  it('logout lỗi mạng vẫn xoá user và không ném lỗi', async () => {
    vi.spyOn(api, 'me').mockResolvedValue(user);
    vi.spyOn(api, 'logout').mockRejectedValue(new Error('network'));
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    let auth!: ReturnType<typeof useAuth>;
    render(
      <AuthProvider>
        <Probe onAuth={(a) => (auth = a)} />
      </AuthProvider>,
    );
    await screen.findByText('in');
    await act(async () => {
      await auth.logout();
    });
    expect(screen.getByText('out')).toBeInTheDocument();
  });

  it('LoginPage hiện spinner (không hiện form) khi đang hỏi /api/me', async () => {
    vi.spyOn(api, 'me').mockReturnValue(new Promise(() => undefined));
    render(
      <MemoryRouter>
        <AuthProvider>
          <LoginPage />
        </AuthProvider>
      </MemoryRouter>,
    );
    expect(screen.getByText('Đang tải…')).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByLabelText('Email')).toBeNull());
  });
});
