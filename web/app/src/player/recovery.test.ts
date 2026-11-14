import { describe, expect, it, vi } from 'vitest';
import { ApiError, type PlayInfo } from '../api';
import { MediaRecovery, PlaybackSession, classifyHlsError } from './recovery';

const play = (n: number, resumeAt = 0): PlayInfo => ({
  url: `http://localhost:8088/cdn/s/sig${n}/100${n}/hls/v/master.m3u8`,
  expires_at: '2026-11-15T12:00:00Z',
  subtitles: [],
  resume_at: resumeAt,
});

function setup(overrides: { now?: () => number } = {}) {
  let n = 0;
  const fetchPlay = vi.fn(async () => play(++n, 12));
  const load = vi.fn();
  const onFatal = vi.fn();
  let time = 0;
  const session = new PlaybackSession({
    fetchPlay,
    load,
    onFatal,
    currentTime: () => time,
    ...overrides,
  });
  return { session, fetchPlay, load, onFatal, setTime: (t: number) => (time = t) };
}

describe('classifyHlsError', () => {
  it('403 (URL ký hết hạn) → refresh, kể cả khi chưa fatal', () => {
    expect(classifyHlsError({ type: 'networkError', details: 'fragLoadError', fatal: false, response: { code: 403 } })).toBe('refresh');
    expect(classifyHlsError({ type: 'networkError', details: 'manifestLoadError', fatal: true, response: { code: 403 } })).toBe('refresh');
  });

  it('lỗi mạng fatal → refresh; lỗi media fatal → media; lỗi khác fatal → fatal', () => {
    expect(classifyHlsError({ type: 'networkError', fatal: true })).toBe('refresh');
    expect(classifyHlsError({ type: 'mediaError', fatal: true })).toBe('media');
    expect(classifyHlsError({ type: 'otherError', fatal: true })).toBe('fatal');
  });

  it('lỗi không fatal và không phải 403 → bỏ qua (hls.js tự retry)', () => {
    expect(classifyHlsError({ type: 'networkError', fatal: false, response: { code: 500 } })).toBeNull();
    expect(classifyHlsError({ type: 'mediaError', fatal: false })).toBeNull();
  });
});

describe('PlaybackSession', () => {
  it('start phát URL từ /play tại resume_at', async () => {
    const { session, load } = setup();
    await session.start();
    expect(load).toHaveBeenCalledWith(play(1).url, 12);
  });

  it('refresh gọi lại /play và phát tiếp từ vị trí hiện tại', async () => {
    const { session, fetchPlay, load, setTime } = setup();
    await session.start();
    setTime(42.5);
    await session.refresh();
    expect(fetchPlay).toHaveBeenCalledTimes(2);
    expect(load).toHaveBeenLastCalledWith(play(2).url, 42.5);
  });

  it('nhiều lỗi cùng lúc chỉ gọi /play một lần', async () => {
    const { session, fetchPlay, load } = setup();
    await session.start();
    await Promise.all([session.refresh(), session.refresh(), session.refresh()]);
    expect(fetchPlay).toHaveBeenCalledTimes(2);
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('quá 3 lần trong 60 giây thì báo lỗi; hết cửa sổ thì được thử lại', async () => {
    let now = 1_000_000;
    const { session, fetchPlay, onFatal } = setup({ now: () => now });
    for (let i = 0; i < 3; i++) await session.refresh();
    expect(onFatal).not.toHaveBeenCalled();
    await session.refresh();
    expect(onFatal).toHaveBeenCalledOnce();
    expect(fetchPlay).toHaveBeenCalledTimes(3);
    now += 60_000;
    await session.refresh();
    expect(fetchPlay).toHaveBeenCalledTimes(4);
  });

  it('/play lỗi → thông báo theo mã lỗi', async () => {
    const onFatal = vi.fn();
    const session = new PlaybackSession({
      fetchPlay: () => Promise.reject(new ApiError(401, 'login required')),
      load: vi.fn(),
      currentTime: () => 0,
      onFatal,
    });
    await session.start();
    expect(onFatal).toHaveBeenCalledWith('Phiên đăng nhập đã hết hạn. Vui lòng đăng nhập lại.');
  });

  it('sau dispose không nạp nguồn nữa', async () => {
    const { session, load } = setup();
    const p = session.start();
    session.dispose();
    await p;
    await session.refresh();
    expect(load).not.toHaveBeenCalled();
  });
});

describe('MediaRecovery', () => {
  it('lần 1 recover, lần 2 đổi codec rồi recover, lần 3 báo lỗi; reset cho cứu lại', () => {
    const r = new MediaRecovery();
    expect(r.next()).toBe('recover');
    expect(r.next()).toBe('swap-recover');
    expect(r.next()).toBe('fatal');
    expect(r.next()).toBe('fatal');
    r.reset();
    expect(r.next()).toBe('recover');
  });
});
