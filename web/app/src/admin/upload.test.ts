import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type UploadPlan } from '../api';
import {
  HttpError,
  PART_ATTEMPTS,
  RETRY_BASE_MS,
  UploadError,
  UploadVideoError,
  uploadParts,
  uploadVideo,
  type PutPart,
} from './upload';

afterEach(() => vi.restoreAllMocks());

// plan cho file `size` byte, part `partSize` byte; URL = "u{n}".
function planFor(size: number, partSize: number): UploadPlan {
  const n = Math.ceil(size / partSize);
  return {
    upload_id: 'up-1',
    part_size: partSize,
    parts: Array.from({ length: n }, (_, i) => ({
      part_number: i + 1,
      url: `u${i + 1}`,
    })),
    expires_at: '2026-11-15T12:00:00Z',
  };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('uploadParts', () => {
  it('cắt đúng khoảng byte, tối đa 3 part cùng lúc, tiến độ tới đủ kích thước file', async () => {
    const file = new Blob(['a'.repeat(10)]);
    const got: Record<string, string> = {};
    let active = 0;
    let maxActive = 0;
    const put: PutPart = async (url, body, onProgress) => {
      active++;
      maxActive = Math.max(maxActive, active);
      onProgress(1);
      await tick();
      got[url] = String(body.size);
      onProgress(body.size);
      active--;
    };
    const progress: number[] = [];
    await uploadParts(file, planFor(10, 3), {
      putPart: put,
      onProgress: (l, t) => progress.push(l / t),
    });
    expect(got).toEqual({ u1: '3', u2: '3', u3: '3', u4: '1' });
    expect(maxActive).toBe(3);
    expect(progress.at(-1)).toBe(1);
    expect(progress.every((p, i) => i === 0 || p >= progress[i - 1])).toBe(true); // không retry: chỉ tăng
  });

  it('thử lại part lỗi, thành công ở lần sau', async () => {
    const calls: string[] = [];
    const put: PutPart = async (url) => {
      calls.push(url);
      if (url === 'u2' && calls.filter((c) => c === 'u2').length === 1) throw new Error('HTTP 500');
    };
    await uploadParts(new Blob(['abcdef']), planFor(6, 3), {
      putPart: put,
      concurrency: 1,
      retryBaseMs: 0,
    });
    expect(calls).toEqual(['u1', 'u2', 'u2']);
  });

  it(`lỗi ${PART_ATTEMPTS} lần thì ném UploadError và huỷ part đang gửi`, async () => {
    const aborted: string[] = [];
    // part 1 luôn lỗi; part 2 treo tới khi bị huỷ.
    const put: PutPart = (url, _body, _p, signal) =>
      new Promise<void>((_resolve, reject) => {
        if (url === 'u1') {
          reject(new Error('HTTP 500'));
          return;
        }
        signal.addEventListener('abort', () => {
          aborted.push(url);
          reject(new DOMException('aborted', 'AbortError'));
        });
      });
    const err = await uploadParts(new Blob(['abcdef']), planFor(6, 3), {
      putPart: put,
      retryBaseMs: 0,
    }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UploadError);
    expect((err as UploadError).partNumber).toBe(1);
    expect((err as UploadError).message).toBe('part 1: HTTP 500');
    expect(aborted).toEqual(['u2']);
  });

  it('chờ 500ms·2^(lần-1) giữa các lần thử', async () => {
    vi.useFakeTimers();
    try {
      let calls = 0;
      const put: PutPart = async () => {
        calls++;
        if (calls < 3) throw new HttpError(503);
      };
      const done = uploadParts(new Blob(['abc']), planFor(3, 3), {
        putPart: put,
      });
      await vi.advanceTimersByTimeAsync(0);
      expect(calls).toBe(1);
      await vi.advanceTimersByTimeAsync(RETRY_BASE_MS - 1);
      expect(calls).toBe(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(calls).toBe(2);
      await vi.advanceTimersByTimeAsync(RETRY_BASE_MS * 2 - 1);
      expect(calls).toBe(2);
      await vi.advanceTimersByTimeAsync(1);
      await done;
      expect(calls).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it('huỷ trong lúc chờ thử lại thì dừng ngay', async () => {
    const ctrl = new AbortController();
    const put: PutPart = async () => {
      throw new HttpError(500);
    };
    const p = uploadParts(new Blob(['abc']), planFor(3, 3), {
      putPart: put,
      signal: ctrl.signal,
    }).catch((e: unknown) => e);
    await tick();
    ctrl.abort();
    const err = await p;
    expect((err as Error).name).toBe('AbortError');
  });

  it.each([
    [403, 'part 1: Link upload đã hết hạn hoặc không hợp lệ, hãy tạo lại'],
    [413, 'part 1: Phần upload quá lớn'],
    [400, 'part 1: HTTP 400'],
  ])('4xx %i không thử lại', async (status, message) => {
    let calls = 0;
    const put: PutPart = async () => {
      calls++;
      throw new HttpError(status);
    };
    const err = await uploadParts(new Blob(['abc']), planFor(3, 3), {
      putPart: put,
      retryBaseMs: 0,
    }).catch((e: unknown) => e);
    expect(calls).toBe(1);
    expect((err as UploadError).message).toBe(message);
  });

  it.each([408, 429, 500])('%i được thử lại', async (status) => {
    let calls = 0;
    const put: PutPart = async () => {
      calls++;
      if (calls === 1) throw new HttpError(status);
    };
    await uploadParts(new Blob(['abc']), planFor(3, 3), {
      putPart: put,
      retryBaseMs: 0,
    });
    expect(calls).toBe(2);
  });
});

describe('uploadVideo', () => {
  it('create → PUT các part → complete, trả id', async () => {
    const order: string[] = [];
    vi.spyOn(adminApi, 'create').mockImplementation(async (input) => {
      order.push(`create ${input.filename} ${input.size_bytes} ${input.title}`);
      return {
        id: 'v1',
        status: 'uploading',
        upload: planFor(input.size_bytes, 4),
      };
    });
    vi.spyOn(adminApi, 'complete').mockImplementation(async (id) => {
      order.push(`complete ${id}`);
      return { id, status: 'queued' };
    });
    const put: PutPart = async (url) => {
      order.push(`put ${url}`);
    };
    const file = new File(['12345678'], 'phim.mp4');
    expect(await uploadVideo(file, { title: 'Phim' }, { putPart: put, concurrency: 1, retryBaseMs: 0 })).toBe('v1');
    expect(order).toEqual(['create phim.mp4 8 Phim', 'put u1', 'put u2', 'complete v1']);
  });

  it('lỗi sau khi đã tạo video: UploadVideoError mang id để xoá', async () => {
    vi.spyOn(adminApi, 'create').mockResolvedValue({
      id: 'v9',
      status: 'uploading',
      upload: planFor(4, 4),
    });
    const complete = vi.spyOn(adminApi, 'complete');
    const put: PutPart = async () => {
      throw new Error('lỗi mạng');
    };
    const err = await uploadVideo(new File(['1234'], 'a.mp4'), { title: 'A' }, { putPart: put, retryBaseMs: 0 }).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(UploadVideoError);
    expect((err as UploadVideoError).videoId).toBe('v9');
    expect(complete).not.toHaveBeenCalled();
  });
});
