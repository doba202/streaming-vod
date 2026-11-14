// Client cho API Go (cùng origin: nginx proxy /api/ → api, ADR 0011). Cookie phiên sf_session là
// HttpOnly nên JavaScript không đọc được; trình duyệt tự gửi kèm vì cùng origin.

export type Role = 'admin' | 'viewer';

export interface User {
  id: string;
  email: string;
  role: Role;
  created_at: string;
}

export interface Rendition {
  height: number;
  bitrate_kbps: number;
}

export interface Video {
  id: string;
  title: string;
  description: string;
  genres: string[];
  year?: number;
  duration_s: number;
  created_at: string;
  renditions?: Rendition[];
}

export interface VideoPage {
  videos: Video[];
  page: number;
  has_more: boolean;
}

export interface Subtitle {
  lang: string;
  label: string;
  url: string;
}

export interface PlayInfo {
  url: string;
  expires_at: string;
  subtitles: Subtitle[];
  resume_at: number;
}

export interface VideoQuery {
  q?: string;
  genre?: string;
  year?: number;
  page?: number;
}

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
  }
}

let onUnauthorized: (() => void) | null = null;

// setUnauthorizedHandler: AuthProvider đăng ký để mọi 401 (cookie hết hạn) đưa người dùng về trang đăng nhập.
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn;
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (init.body !== undefined) headers.set('Content-Type', 'application/json');
  const res = await fetch(path, { ...init, headers, credentials: 'same-origin' });
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // body không phải JSON: giữ statusText
    }
    if (res.status === 401 && !path.startsWith('/api/auth/')) onUnauthorized?.();
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// videosQueryString bỏ tham số rỗng; page 1 là mặc định nên không ghi.
export function videosQueryString(q: VideoQuery): string {
  const p = new URLSearchParams();
  if (q.q?.trim()) p.set('q', q.q.trim());
  if (q.genre) p.set('genre', q.genre);
  if (q.year) p.set('year', String(q.year));
  if (q.page && q.page > 1) p.set('page', String(q.page));
  const s = p.toString();
  return s ? `?${s}` : '';
}

const json = (body: unknown) => JSON.stringify(body);

export const api = {
  me: () => request<User>('/api/me'),
  login: (email: string, password: string) =>
    request<User>('/api/auth/login', { method: 'POST', body: json({ email, password }) }),
  register: (email: string, password: string) =>
    request<User>('/api/auth/register', { method: 'POST', body: json({ email, password }) }),
  logout: () => request<void>('/api/auth/logout', { method: 'POST' }),
  videos: (q: VideoQuery = {}) => request<VideoPage>(`/api/videos${videosQueryString(q)}`),
  video: (id: string) => request<Video>(`/api/videos/${encodeURIComponent(id)}`),
  play: (id: string) => request<PlayInfo>(`/api/videos/${encodeURIComponent(id)}/play`),
  genres: () => request<{ genres: string[] }>('/api/genres').then((r) => r.genres),
};
