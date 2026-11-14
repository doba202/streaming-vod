// Upload video từ trình duyệt (M7, ADR 0019): tạo video → PUT từng part thẳng lên MinIO qua presigned URL
// (song song, có tiến độ) → /complete. File không đi qua api hay nginx. api ghép part bằng ListParts nên
// trình duyệt không cần đọc header ETag của phản hồi PUT.
import { adminApi, type CreateVideoInput, type UploadPlan } from '../api';

// UPLOAD_CONCURRENCY: số part PUT cùng lúc (part 64 MiB → tối đa ~192 MiB đang gửi).
export const UPLOAD_CONCURRENCY = 3;
// PART_ATTEMPTS: mỗi part thử tối đa 3 lần (lỗi mạng ngắn) trước khi bỏ cả upload.
export const PART_ATTEMPTS = 3;
// RETRY_BASE_MS: chờ RETRY_BASE_MS·2^(lần-1) giữa hai lần thử một part (500ms, 1s, ...).
export const RETRY_BASE_MS = 500;

// PutPart gửi một part; gọi onProgress(số byte đã gửi của part này) trong lúc gửi.
export type PutPart = (
  url: string,
  body: Blob,
  onProgress: (loaded: number) => void,
  signal: AbortSignal,
) => Promise<void>;

export interface UploadOptions {
  onProgress?: (loaded: number, total: number) => void;
  concurrency?: number;
  putPart?: PutPart;
  signal?: AbortSignal;
  retryBaseMs?: number;
  // onCreated: gọi ngay sau khi tạo video (để trang admin biết id mà xoá khi huỷ upload).
  onCreated?: (videoId: string) => void;
}

// HttpError: PUT part nhận mã không phải 2xx.
export class HttpError extends Error {
  readonly status: number;

  constructor(status: number) {
    super(`HTTP ${status}`);
    this.name = 'HttpError';
    this.status = status;
  }
}

// retryable: lỗi mạng và 5xx, 408, 429 thì thử lại; các 4xx khác (vd. 403 link hết hạn) thì không.
function retryable(err: unknown): boolean {
  if (!(err instanceof HttpError)) return true;
  return err.status >= 500 || err.status === 408 || err.status === 429;
}

function describeError(err: unknown): string {
  if (err instanceof HttpError) {
    if (err.status === 403) return 'Link upload đã hết hạn hoặc không hợp lệ, hãy tạo lại';
    if (err.status === 413) return 'Phần upload quá lớn';
  }
  return err instanceof Error ? err.message : String(err);
}

// sleep chờ ms mili giây; ném AbortError ngay khi signal bị huỷ.
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    if (signal.aborted) return reject(new DOMException('upload aborted', 'AbortError'));
    const onAbort = () => {
      clearTimeout(t);
      reject(new DOMException('upload aborted', 'AbortError'));
    };
    const t = setTimeout(() => {
      signal.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

export class UploadError extends Error {
  readonly partNumber: number;

  constructor(partNumber: number, message: string) {
    super(message);
    this.name = 'UploadError';
    this.partNumber = partNumber;
  }
}

// xhrPutPart dùng XMLHttpRequest vì fetch không báo tiến độ upload. Blob cắt từ File có type rỗng nên
// trình duyệt không gửi Content-Type (presigned URL chỉ ký header host).
export const xhrPutPart: PutPart = (url, body, onProgress, signal) =>
  new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', url);
    xhr.upload.onprogress = (e) => onProgress(e.loaded);
    xhr.onload = () => (xhr.status >= 200 && xhr.status < 300 ? resolve() : reject(new HttpError(xhr.status)));
    xhr.onerror = () => reject(new Error('lỗi mạng (kiểm tra CORS của MinIO và cổng 9000)'));
    xhr.onabort = () => reject(new DOMException('upload aborted', 'AbortError'));
    if (signal.aborted) {
      reject(new DOMException('upload aborted', 'AbortError'));
      return;
    }
    signal.addEventListener('abort', () => xhr.abort(), { once: true });
    xhr.send(body);
  });

// uploadParts gửi mọi part của plan, tối đa `concurrency` part cùng lúc. Một part lỗi PART_ATTEMPTS lần thì
// huỷ các part đang gửi và ném UploadError. onProgress nhận tổng byte đã gửi (không vượt file.size).
export async function uploadParts(file: Blob, plan: UploadPlan, opts: UploadOptions = {}): Promise<void> {
  opts.signal?.throwIfAborted(); // signal đã huỷ từ trước: addEventListener bên dưới sẽ không bắn nữa
  const put = opts.putPart ?? xhrPutPart;
  const total = file.size;
  const loaded = new Map<number, number>();
  const report = () => {
    let sum = 0;
    for (const n of loaded.values()) sum += n;
    opts.onProgress?.(Math.min(sum, total), total);
  };
  const ctrl = new AbortController();
  const onOuterAbort = () => ctrl.abort();
  opts.signal?.addEventListener('abort', onOuterAbort, { once: true });

  const queue = [...plan.parts].sort((a, b) => a.part_number - b.part_number);
  const sendPart = async (partNumber: number, url: string) => {
    const start = (partNumber - 1) * plan.part_size;
    const body = file.slice(start, Math.min(start + plan.part_size, total));
    for (let attempt = 1; ; attempt++) {
      loaded.set(partNumber, 0);
      try {
        await put(
          url,
          body,
          (n) => {
            loaded.set(partNumber, Math.min(n, body.size));
            report();
          },
          ctrl.signal,
        );
        loaded.set(partNumber, body.size);
        report();
        return;
      } catch (err) {
        if (ctrl.signal.aborted) throw err;
        if (attempt >= PART_ATTEMPTS || !retryable(err)) {
          throw new UploadError(partNumber, `part ${partNumber}: ${describeError(err)}`);
        }
        await sleep((opts.retryBaseMs ?? RETRY_BASE_MS) * 2 ** (attempt - 1), ctrl.signal);
      }
    }
  };
  const worker = async () => {
    for (let p = queue.shift(); p; p = queue.shift()) await sendPart(p.part_number, p.url);
  };
  try {
    const n = Math.max(1, Math.min(opts.concurrency ?? UPLOAD_CONCURRENCY, queue.length));
    await Promise.all(Array.from({ length: n }, worker));
  } catch (err) {
    queue.length = 0; // không bắt đầu part mới
    ctrl.abort(); // dừng các part còn đang gửi
    throw err;
  } finally {
    opts.signal?.removeEventListener('abort', onOuterAbort);
  }
}

export type VideoMeta = Omit<CreateVideoInput, 'filename' | 'size_bytes'>;

// uploadVideo: create → uploadParts → complete. Trả id; lỗi sau bước create kèm id (UploadVideoError)
// để trang admin cho xoá video dở (nếu không, cleaner tự xoá sau STALE_UPLOAD_AFTER = 24h).
export class UploadVideoError extends Error {
  readonly videoId: string;

  constructor(videoId: string, cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause));
    this.name = 'UploadVideoError';
    this.videoId = videoId;
  }
}

export async function uploadVideo(file: File, meta: VideoMeta, opts: UploadOptions = {}): Promise<string> {
  const created = await adminApi.create({
    ...meta,
    filename: file.name,
    size_bytes: file.size,
  });
  opts.onCreated?.(created.id);
  try {
    opts.signal?.throwIfAborted();
    await uploadParts(file, created.upload, opts);
    opts.signal?.throwIfAborted(); // huỷ giữa lúc part cuối xong và complete
    await adminApi.complete(created.id);
  } catch (err) {
    throw new UploadVideoError(created.id, err);
  }
  return created.id;
}
