import { useState, type FormEvent, type ReactNode } from 'react';
import { ApiError } from '../api';

export interface Credentials {
  email: string;
  password: string;
}

export interface CredentialErrors {
  email?: string;
  password?: string;
}

// Cùng giới hạn với server (internal/auth: mật khẩu 8–72 byte, vì bcrypt chỉ dùng 72 byte đầu).
export const MIN_PASSWORD_BYTES = 8;
export const MAX_PASSWORD_BYTES = 72;

export function validateCredentials(c: Credentials): CredentialErrors {
  const errors: CredentialErrors = {};
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(c.email.trim())) errors.email = 'Email không hợp lệ.';
  const bytes = new TextEncoder().encode(c.password).length;
  if (bytes < MIN_PASSWORD_BYTES || bytes > MAX_PASSWORD_BYTES) {
    errors.password = `Mật khẩu cần ${MIN_PASSWORD_BYTES}–${MAX_PASSWORD_BYTES} byte (ký tự có dấu tính nhiều byte).`;
  }
  return errors;
}

function serverMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return 'Email hoặc mật khẩu không đúng.';
    if (err.status === 409) return 'Email này đã được đăng ký.';
    if (err.status === 400) return 'Thông tin không hợp lệ.';
  }
  return 'Có lỗi xảy ra, vui lòng thử lại.';
}

export interface AuthFormProps {
  title: string;
  submitLabel: string;
  onSubmit: (c: Credentials) => Promise<void>;
  footer?: ReactNode;
  mode?: 'login' | 'register'; // register: trình duyệt gợi ý mật khẩu mới thay vì điền mật khẩu cũ
}

export function AuthForm({ title, submitLabel, onSubmit, footer, mode = 'login' }: AuthFormProps) {
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [errors, setErrors] = useState<CredentialErrors>({});
  const [serverError, setServerError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const c = { email: email.trim(), password };
    const v = validateCredentials(c);
    setErrors(v);
    setServerError('');
    if (v.email || v.password) return;
    setSubmitting(true);
    try {
      await onSubmit(c);
    } catch (err) {
      setServerError(serverMessage(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form className="auth-form" onSubmit={submit} noValidate>
      <h1>{title}</h1>
      <label>
        Email
        <input type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)}
          aria-invalid={Boolean(errors.email)} />
      </label>
      {errors.email && <p className="field-error">{errors.email}</p>}
      <label>
        Mật khẩu
        <input type="password" autoComplete={mode === 'register' ? 'new-password' : 'current-password'} value={password}
          onChange={(e) => setPassword(e.target.value)} aria-invalid={Boolean(errors.password)} />
      </label>
      {errors.password && <p className="field-error">{errors.password}</p>}
      {serverError && <p role="alert" className="form-error">{serverError}</p>}
      <button type="submit" disabled={submitting}>
        {submitting ? 'Đang gửi…' : submitLabel}
      </button>
      {footer}
    </form>
  );
}
