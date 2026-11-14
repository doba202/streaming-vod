// Gửi sự kiện xem phim (spec §4.4): heartbeat mỗi 10 giây khi đang phát, và khi play, pause, seek, ended,
// buffering. Tách khỏi React và <video> để test được bằng fake timer; Player chỉ nối sự kiện DOM vào đây.
import type { PlaybackEvent, PlaybackEventType } from '../api';

export const HEARTBEAT_MS = 10_000;
// Giới hạn của server (POST /api/playback/events): vượt là 400.
export const MAX_BUFFERING_MS = 600_000;
export const MAX_BITRATE_KBPS = 100_000;

export interface ReporterDeps {
  videoId: string;
  send: (ev: PlaybackEvent) => void;
  position: () => number; // currentTime của <video>
  bitrateKbps: () => number; // bitrate của mức chất lượng đang phát (0 nếu không biết)
  now?: () => number;
  heartbeatMs?: number;
}

export class PlaybackReporter {
  private readonly deps: Required<ReporterDeps>;
  private timer: ReturnType<typeof setInterval> | null = null;
  private bufferingSince: number | null = null;
  private started = false;

  constructor(deps: ReporterDeps) {
    this.deps = { now: Date.now, heartbeatMs: HEARTBEAT_MS, ...deps };
  }

  // play: người xem bấm phát (hoặc tự phát lại sau khi nạp URL mới). Bắt đầu heartbeat.
  play(): void {
    this.started = true;
    this.emit('play');
    this.stopHeartbeat();
    this.timer = setInterval(() => this.emit('heartbeat'), this.deps.heartbeatMs);
  }

  pause(): void {
    this.stopHeartbeat();
    this.bufferingSince = null;
    if (this.started) this.emit('pause');
  }

  seeked(): void {
    if (this.started) this.emit('seek');
  }

  ended(): void {
    this.stopHeartbeat();
    this.bufferingSince = null;
    this.emit('ended');
  }

  // waiting: hết dữ liệu, player đứng hình chờ tải. Chỉ tính từ lần chờ đầu tiên cho tới khi phát lại.
  waiting(): void {
    if (this.bufferingSince === null) this.bufferingSince = this.deps.now();
  }

  // playing: phát lại sau khi chờ → một sự kiện buffering với thời gian đã chờ.
  playing(): void {
    if (this.bufferingSince === null) return;
    const ms = Math.min(MAX_BUFFERING_MS, Math.max(0, Math.round(this.deps.now() - this.bufferingSince)));
    this.bufferingSince = null;
    this.emit('buffering', ms);
  }

  // flush: rời trang / đóng tab / chuyển trang khi đã xem → ghi lại vị trí cuối (heartbeat lưu watch_progress).
  flush(): void {
    if (this.started && this.deps.position() > 0) this.emit('heartbeat');
  }

  dispose(): void {
    this.stopHeartbeat();
    this.bufferingSince = null;
  }

  private stopHeartbeat(): void {
    if (this.timer !== null) clearInterval(this.timer);
    this.timer = null;
  }

  private emit(type: PlaybackEventType, bufferingMs = 0): void {
    this.deps.send({
      video_id: this.deps.videoId,
      type,
      position_s: Math.round(this.deps.position() * 1000) / 1000,
      bitrate_kbps: Math.min(MAX_BITRATE_KBPS, Math.max(0, Math.round(this.deps.bitrateKbps()))),
      buffering_ms: Math.min(MAX_BUFFERING_MS, bufferingMs),
    });
  }
}
