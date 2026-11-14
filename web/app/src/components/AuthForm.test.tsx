import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api';
import { AuthForm, validateCredentials } from './AuthForm';

function fill(email: string, password: string) {
  fireEvent.change(screen.getByLabelText('Email'), { target: { value: email } });
  fireEvent.change(screen.getByLabelText('Mật khẩu'), { target: { value: password } });
  fireEvent.click(screen.getByRole('button', { name: 'Đăng nhập' }));
}

describe('validateCredentials', () => {
  it('kiểm tra email và độ dài mật khẩu theo byte', () => {
    expect(validateCredentials({ email: 'a@b.vn', password: '12345678' })).toEqual({});
    expect(validateCredentials({ email: 'không-phải-email', password: '12345678' }).email).toBe('Email không hợp lệ.');
    expect(validateCredentials({ email: 'a@b.vn', password: '1234567' }).password).toBeDefined();
    // "đ" là 2 byte UTF-8: 36 ký tự = 72 byte hợp lệ, 37 ký tự = 74 byte quá giới hạn.
    expect(validateCredentials({ email: 'a@b.vn', password: 'đ'.repeat(36) })).toEqual({});
    expect(validateCredentials({ email: 'a@b.vn', password: 'đ'.repeat(37) }).password).toBeDefined();
  });
});

describe('AuthForm', () => {
  it('dữ liệu sai: hiện lỗi, không gửi', () => {
    const onSubmit = vi.fn();
    render(<AuthForm title="Đăng nhập" submitLabel="Đăng nhập" onSubmit={onSubmit} />);
    fill('abc', '123');
    expect(screen.getByText('Email không hợp lệ.')).toBeInTheDocument();
    expect(screen.getByText('Mật khẩu cần 8–72 ký tự.')).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('dữ liệu đúng: gửi email đã trim', async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<AuthForm title="Đăng nhập" submitLabel="Đăng nhập" onSubmit={onSubmit} />);
    fill('  viewer@example.com ', 'viewer-password-1');
    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({ email: 'viewer@example.com', password: 'viewer-password-1' }),
    );
  });

  it('server trả 401: hiện thông báo sai thông tin đăng nhập', async () => {
    const onSubmit = vi.fn().mockRejectedValue(new ApiError(401, 'invalid credentials'));
    render(<AuthForm title="Đăng nhập" submitLabel="Đăng nhập" onSubmit={onSubmit} />);
    fill('viewer@example.com', 'wrong-password');
    expect(await screen.findByRole('alert')).toHaveTextContent('Email hoặc mật khẩu không đúng.');
  });
});
