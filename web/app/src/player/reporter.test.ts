import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { PlaybackEvent } from '../api';
import { PlaybackReporter } from './reporter';

function setup() {
  const sent: PlaybackEvent[] = [];
  let pos = 0;
  let clock = 1_000;
  const r = new PlaybackReporter({
    videoId: 'v1',
    send: (ev) => sent.push(ev),
    position: () => pos,
    bitrateKbps: () => 2799.6,
    now: () => clock,
  });
  return {
    r,
    sent,
    types: () => sent.map((e) => e.type),
    setPos: (p: number) => (pos = p),
    advance: (ms: number) => (clock += ms),
  };
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('PlaybackReporter', () => {
  it('heartbeat mỗi 10 giây chỉ khi đang phát', () => {
    const { r, types, setPos, sent } = setup();
    r.play();
    setPos(10.12345);
    vi.advanceTimersByTime(10_000);
    setPos(20);
    vi.advanceTimersByTime(10_000);
    expect(types()).toEqual(['play', 'heartbeat', 'heartbeat']);
    expect(sent[1]).toEqual({ video_id: 'v1', type: 'heartbeat', position_s: 10.123, bitrate_kbps: 2800, buffering_ms: 0 });
    r.pause();
    vi.advanceTimersByTime(60_000);
    expect(types()).toEqual(['play', 'heartbeat', 'heartbeat', 'pause']);
    r.play();
    r.play(); // play hai lần không tạo hai interval
    vi.advanceTimersByTime(10_000);
    expect(types().filter((t) => t === 'heartbeat')).toHaveLength(3);
    r.dispose();
  });

  it('seek, ended; pause/seek trước lần play đầu (nạp nguồn, tua tới resume_at) bị bỏ qua', () => {
    const { r, types } = setup();
    r.seeked();
    r.pause();
    expect(types()).toEqual([]);
    r.play();
    r.seeked();
    r.ended();
    vi.advanceTimersByTime(30_000);
    expect(types()).toEqual(['play', 'seek', 'ended']);
  });

  it('buffering: thời gian từ waiting đầu tiên tới playing', () => {
    const { r, sent, advance } = setup();
    r.play();
    r.waiting();
    advance(400);
    r.waiting(); // lần chờ thứ hai trong cùng một đợt không đặt lại mốc
    advance(450);
    r.playing();
    r.playing(); // không chờ thì không gửi
    expect(sent.filter((e) => e.type === 'buffering')).toEqual([
      { video_id: 'v1', type: 'buffering', position_s: 0, bitrate_kbps: 2800, buffering_ms: 850 },
    ]);
  });

  it('flush gửi heartbeat vị trí cuối khi đã xem', () => {
    const { r, types, setPos } = setup();
    r.flush();
    expect(types()).toEqual([]); // chưa xem
    r.play();
    setPos(42);
    r.flush();
    expect(types()).toEqual(['play', 'heartbeat']);
  });
});
