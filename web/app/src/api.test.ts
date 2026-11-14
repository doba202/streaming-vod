import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, api, sendPlaybackEvent, setUnauthorizedHandler, videosQueryString } from './api';

afterEach(() => {
  vi.unstubAllGlobals();
  setUnauthorizedHandler(null);
});

describe('videosQueryString', () => {
  it('bỏ tham số rỗng và page 1', () => {
    expect(videosQueryString({})).toBe('');
    expect(videosQueryString({ q: '  ', genre: '', page: 1 })).toBe('');
  });

  it('mã hoá từ khoá, thể loại, năm, trang', () => {
    expect(videosQueryString({ q: ' tro choi ', genre: 'Hành động', year: 2021, page: 2 })).toBe(
      '?q=tro+choi&genre=H%C3%A0nh+%C4%91%E1%BB%99ng&year=2021&page=2',
    );
  });
});

describe('request', () => {
  it('ném ApiError với thông báo lỗi của server và gọi handler khi 401', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: 'login required' }), { status: 401 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);

    const err = await api.videos({ q: 'tro choi' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
    expect((err as ApiError).message).toBe('login required');
    expect(onUnauthorized).toHaveBeenCalledOnce();
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/videos?q=tro+choi',
      expect.objectContaining({ credentials: 'same-origin' }),
    );
  });

  it('401 khi đăng nhập sai không coi là hết phiên', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{"error":"bad"}', { status: 401 })));
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    await expect(api.login('a@b.vn', 'x')).rejects.toBeInstanceOf(ApiError);
    expect(onUnauthorized).not.toHaveBeenCalled();
  });

  it('gửi JSON khi có body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: 'u', email: 'a@b.vn', role: 'viewer', created_at: '' }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    await api.login('a@b.vn', 'secret-123');
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe('POST');
    expect(init.body).toBe('{"email":"a@b.vn","password":"secret-123"}');
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json');
  });
});

describe('M5', () => {
  it('continueWatching trả mảng videos', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ videos: [{ id: 'v1', position_s: 42 }] }), { status: 200 }),
    );
    vi.stubGlobal('fetch', fetchMock);
    const vs = await api.continueWatching();
    expect(vs).toEqual([{ id: 'v1', position_s: 42 }]);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/me/continue-watching');
  });

  it('sendPlaybackEvent: POST JSON keepalive, lỗi và 401 bị bỏ qua', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response('{"error":"login required"}', { status: 401 }));
    vi.stubGlobal('fetch', fetchMock);
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    const ev = { video_id: 'v1', type: 'heartbeat' as const, position_s: 12.5, bitrate_kbps: 2800, buffering_ms: 0 };
    sendPlaybackEvent(ev);
    await Promise.resolve();
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/playback/events');
    expect(init).toMatchObject({ method: 'POST', keepalive: true, credentials: 'same-origin' });
    expect(JSON.parse(init.body as string)).toEqual(ev);
    expect(onUnauthorized).not.toHaveBeenCalled();

    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('network down')));
    expect(() => sendPlaybackEvent(ev)).not.toThrow();
  });
});
