import { afterEach, describe, expect, it, vi } from 'vitest';
import { adminApi, type UploadPlan } from '../api';
import { PART_ATTEMPTS, UploadError, UploadVideoError, uploadParts, uploadVideo, type PutPart } from './upload';

afterEach(() => vi.restoreAllMocks());

// plan cho file `size` byte, part `partSize` byte; URL = "u{n}".
function planFor(size: number, partSize: number): UploadPlan {
  const n = Math.ceil(size / partSize);
  return {
    upload_id: 'up-1',
    part_size: partSize,
    parts: Array.from({ length: n }, (_, i) => ({ part_number: i + 1, url: `u${i + 1}` })),
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
    await uploadParts(file, planFor(10, 3), { putPart: put, onProgress: (l, t) => progress.push(l / t) });
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
    await uploadParts(new Blob(['abcdef']), planFor(6, 3), { putPart: put, concurrency: 1 });
    expect(calls).toEqual(['u1', 'u2', 'u2']);
  });

  it(`lỗi ${PART_ATTEMPTS} lần thì ném UploadError và huỷ part đang gửi`, async () => {
    const aborted: string[] = [];
    // part 1 luôn lỗi; part 2 treo tới khi bị huỷ.
    const put: PutPart = (url, _body, _p, signal) =>
      new Promise<void>((_resolve, reject) => {
        if (url === 'u1') {
          reject(new Error('HTTP 403'));
          return;
        }
        signal.addEventListener('abort', () => {
          aborted.push(url);
          reject(new DOMException('aborted', 'AbortError'));
        });
      });
    const err = await uploadParts(new Blob(['abcdef']), planFor(6, 3), { putPart: put }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UploadError);
    expect((err as UploadError).partNumber).toBe(1);
    expect((err as UploadError).message).toBe('part 1: HTTP 403');
    expect(aborted).toEqual(['u2']);
  });
});

describe('uploadVideo', () => {
  it('create → PUT các part → complete, trả id', async () => {
    const order: string[] = [];
    vi.spyOn(adminApi, 'create').mockImplementation(async (input) => {
      order.push(`create ${input.filename} ${input.size_bytes} ${input.title}`);
      return { id: 'v1', status: 'uploading', upload: planFor(input.size_bytes, 4) };
    });
    vi.spyOn(adminApi, 'complete').mockImplementation(async (id) => {
      order.push(`complete ${id}`);
      return { id, status: 'queued' };
    });
    const put: PutPart = async (url) => {
      order.push(`put ${url}`);
    };
    const file = new File(['12345678'], 'phim.mp4');
    expect(await uploadVideo(file, { title: 'Phim' }, { putPart: put, concurrency: 1 })).toBe('v1');
    expect(order).toEqual(['create phim.mp4 8 Phim', 'put u1', 'put u2', 'complete v1']);
  });

  it('lỗi sau khi đã tạo video: UploadVideoError mang id để xoá', async () => {
    vi.spyOn(adminApi, 'create').mockResolvedValue({ id: 'v9', status: 'uploading', upload: planFor(4, 4) });
    const complete = vi.spyOn(adminApi, 'complete');
    const put: PutPart = async () => {
      throw new Error('lỗi mạng');
    };
    const err = await uploadVideo(new File(['1234'], 'a.mp4'), { title: 'A' }, { putPart: put }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UploadVideoError);
    expect((err as UploadVideoError).videoId).toBe('v9');
    expect(complete).not.toHaveBeenCalled();
  });
});
