// Logic phục hồi của player, tách khỏi hls.js và React để test được (spec §4.3 bước 6):
// URL ký hết hạn (nginx trả 403) → gọi lại /play lấy URL mới → phát tiếp từ vị trí hiện tại.
import { ApiError, type PlayInfo } from '../api';

// Phần của dữ liệu sự kiện Hls.Events.ERROR mà ta dùng. Giá trị type là Hls.ErrorTypes của hls.js 1.x
// ('networkError', 'mediaError', ...), ghi literal để module này không phải import hls.js.
export interface HlsErrorLike {
  type: string;
  details?: string;
  fatal: boolean;
  response?: { code?: number } | null;
}

// refresh: lấy URL mới qua /play; media: hls.recoverMediaError(); fatal: báo lỗi cho người xem.
export type RecoveryAction = 'refresh' | 'media' | 'fatal';

export function classifyHlsError(e: HlsErrorLike): RecoveryAction | null {
  // 403 từ nginx = chữ ký sai hoặc hết hạn. hls.js không retry lỗi 4xx; với segment có thể chưa fatal
  // nhưng ta làm mới ngay thay vì đợi.
  if (e.response?.code === 403) return 'refresh';
  if (!e.fatal) return null;
  if (e.type === 'networkError') return 'refresh';
  if (e.type === 'mediaError') return 'media';
  return 'fatal';
}

// MediaRecovery giới hạn số lần cứu lỗi media fatal: lần 1 recoverMediaError(), lần 2 swapAudioCodec()
// rồi recoverMediaError(), lần 3 trở đi báo lỗi. reset() khi phát tiến triển hoặc nạp nguồn mới.
export const MAX_MEDIA_RECOVERIES = 2;
export type MediaRecoveryStep = 'recover' | 'swap-recover' | 'fatal';

export class MediaRecovery {
  private attempts = 0;

  next(): MediaRecoveryStep {
    this.attempts++;
    if (this.attempts === 1) return 'recover';
    if (this.attempts <= MAX_MEDIA_RECOVERIES) return 'swap-recover';
    return 'fatal';
  }

  reset(): void {
    this.attempts = 0;
  }
}

export interface PlaybackDeps {
  fetchPlay: () => Promise<PlayInfo>;
  // load nạp URL HLS và bắt đầu phát từ startAt giây (0 = đầu phim).
  load: (url: string, startAt: number) => void;
  currentTime: () => number;
  onFatal: (message: string) => void;
  now?: () => number;
  maxRefreshes?: number; // số lần làm mới tối đa trong refreshWindowMs (mặc định 3)
  refreshWindowMs?: number; // mặc định 60 000
}

export function playErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return 'Phiên đăng nhập đã hết hạn. Vui lòng đăng nhập lại.';
    if (err.status === 404) return 'Không tìm thấy phim hoặc phim chưa sẵn sàng.';
  }
  return 'Không tải được video. Vui lòng thử lại.';
}

export class PlaybackSession {
  private readonly deps: Required<PlaybackDeps>;
  private refreshes: number[] = [];
  private inflight: Promise<void> | null = null;
  private disposed = false;

  constructor(deps: PlaybackDeps) {
    this.deps = { now: Date.now, maxRefreshes: 3, refreshWindowMs: 60_000, ...deps };
  }

  // start gọi /play lần đầu và phát từ resume_at.
  async start(): Promise<void> {
    try {
      const p = await this.deps.fetchPlay();
      if (!this.disposed) this.deps.load(p.url, p.resume_at);
    } catch (err) {
      if (!this.disposed) this.deps.onFatal(playErrorMessage(err));
    }
  }

  // refresh lấy URL ký mới và phát tiếp từ currentTime. Nhiều lỗi cùng lúc (playlist và segment
  // song song) chỉ gây một lần gọi /play. Quá maxRefreshes lần trong refreshWindowMs thì dừng
  // để không lặp vô hạn (ví dụ secret của api và nginx lệch nhau).
  refresh(): Promise<void> {
    if (this.disposed) return Promise.resolve();
    if (this.inflight) return this.inflight;
    const now = this.deps.now();
    this.refreshes = this.refreshes.filter((t) => now - t < this.deps.refreshWindowMs);
    if (this.refreshes.length >= this.deps.maxRefreshes) {
      this.deps.onFatal('Không phát được video: liên kết phát liên tục bị từ chối.');
      return Promise.resolve();
    }
    this.refreshes.push(now);
    const at = this.deps.currentTime();
    this.inflight = this.deps
      .fetchPlay()
      .then((p) => {
        if (!this.disposed) this.deps.load(p.url, at);
      })
      .catch((err: unknown) => {
        if (!this.disposed) this.deps.onFatal(playErrorMessage(err));
      })
      .finally(() => {
        this.inflight = null;
      });
    return this.inflight;
  }

  dispose(): void {
    this.disposed = true;
  }
}
