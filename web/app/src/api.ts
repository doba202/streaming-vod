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
  // URL ký (/cdn/s/...) của poster.jpg; không có với video transcode trước M5.
  poster_url?: string;
  renditions?: Rendition[];
}

// Một video của hàng "Xem tiếp" (GET /api/me/continue-watching).
export interface ContinueVideo extends Video {
  position_s: number;
  watched_at: string;
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
  // thumbnails.vtt (sprite xem trước khi tua); không có với video transcode trước M5.
  thumbnails_url?: string;
  resume_at: number;
}

export type PlaybackEventType = 'play' | 'pause' | 'seek' | 'ended' | 'buffering' | 'heartbeat';

// Body của POST /api/playback/events (spec §4.4).
export interface PlaybackEvent {
  video_id: string;
  type: PlaybackEventType;
  position_s: number;
  bitrate_kbps: number;
  buffering_ms: number;
}

export interface VideoQuery {
  q?: string;
  genre?: string;
  year?: number;
  page?: number;
}

// ---- Admin (M7): /api/admin/*, chỉ role admin (server trả 403 cho viewer) ----

export type VideoStatus = 'uploading' | 'queued' | 'processing' | 'ready' | 'failed';

export interface AdminSubtitle {
  lang: string;
  label: string;
  vtt_key: string;
}

// Video như admin thấy (GET /api/admin/videos[/{id}]): mọi trạng thái, có fail_reason.
export interface AdminVideo {
  id: string;
  title: string;
  description: string;
  genres: string[];
  year?: number;
  status: VideoStatus;
  fail_reason?: string;
  duration_s?: number;
  size_bytes: number;
  created_at: string;
  updated_at: string;
  renditions?: Rendition[];
  subtitles?: AdminSubtitle[]; // chỉ có ở chi tiết
}

export interface UploadPart {
  part_number: number;
  url: string;
}

// Kế hoạch upload multipart: mỗi part một presigned URL (PUT thẳng lên MinIO), part_size byte mỗi part
// (part cuối có thể ngắn hơn). URL hết hạn lúc expires_at (1 giờ).
export interface UploadPlan {
  upload_id: string;
  part_size: number;
  parts: UploadPart[];
  expires_at: string;
}

export interface CreateVideoInput {
  title: string;
  description?: string;
  genres?: string[];
  year?: number;
  filename: string;
  size_bytes: number;
}

export interface CreateVideoResult {
  id: string;
  status: VideoStatus;
  upload: UploadPlan;
}

// PATCH: field không có thì giữ nguyên; year 0 xoá năm; genres [] xoá thể loại.
export interface VideoPatch {
  title?: string;
  description?: string;
  genres?: string[];
  year?: number;
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
  // Chỉ body JSON (chuỗi) mới đặt Content-Type; FormData để trình duyệt tự đặt multipart/form-data + boundary.
  if (typeof init.body === 'string') headers.set('Content-Type', 'application/json');
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
  continueWatching: () =>
    request<{ videos: ContinueVideo[] }>('/api/me/continue-watching').then((r) => r.videos),
};

const adminVideo = (id: string) => `/api/admin/videos/${encodeURIComponent(id)}`;

// ADMIN_LIST_LIMIT: số video tối đa của trang quản trị (server cho phép 1–200, mới nhất trước).
export const ADMIN_LIST_LIMIT = 200;

export const adminApi = {
  list: () =>
    request<{ videos: AdminVideo[] }>(`/api/admin/videos?limit=${ADMIN_LIST_LIMIT}`).then((r) => r.videos),
  get: (id: string) => request<AdminVideo>(adminVideo(id)),
  create: (input: CreateVideoInput) =>
    request<CreateVideoResult>('/api/admin/videos', { method: 'POST', body: json(input) }),
  complete: (id: string) =>
    request<{ id: string; status: VideoStatus }>(`${adminVideo(id)}/complete`, { method: 'POST' }),
  retry: (id: string) => request<{ id: string; status: VideoStatus }>(`${adminVideo(id)}/retry`, { method: 'POST' }),
  update: (id: string, patch: VideoPatch) =>
    request<AdminVideo>(adminVideo(id), { method: 'PATCH', body: json(patch) }),
  remove: (id: string) => request<void>(adminVideo(id), { method: 'DELETE' }),
  uploadSubtitle: (id: string, lang: string, label: string, file: File) => {
    const form = new FormData();
    form.set('lang', lang);
    form.set('label', label);
    form.set('file', file);
    return request<AdminSubtitle>(`${adminVideo(id)}/subtitles`, { method: 'POST', body: form });
  },
};

// sendPlaybackEvent gửi sự kiện xem và không chờ kết quả: lỗi (mạng, 401, Kafka chết phía server) bị bỏ qua
// để không bao giờ ảnh hưởng việc xem. keepalive: request vẫn đi khi tab đang đóng (pagehide).
// Không qua request() nên 401 ở đây không đẩy người dùng về trang đăng nhập giữa lúc đang xem.
export function sendPlaybackEvent(ev: PlaybackEvent): void {
  try {
    void fetch('/api/playback/events', {
      method: 'POST',
      credentials: 'same-origin',
      keepalive: true,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(ev),
    }).catch(() => undefined);
  } catch {
    // fetch có thể ném đồng bộ (ví dụ vượt hạn mức keepalive 64 KB): bỏ qua
  }
}
