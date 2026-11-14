import { describe, expect, it } from 'vitest';
import { findThumb, parseThumbnailsVtt, parseVttTime } from './thumbnails';

const base = 'http://localhost:8088/cdn/s/SIG/1794736800/hls/v1/thumbnails.vtt';
const vtt =
  'WEBVTT\r\n\r\n00:00:00.000 --> 00:00:10.000\r\nsprite_001.jpg#xywh=0,0,160,90\r\n\r\n' +
  '00:00:10.000 --> 00:00:20.000\r\nsprite_001.jpg#xywh=160,0,160,90\r\n\r\n' +
  '00:16:40.000 --> 00:16:45.500 align:start\r\nsprite_002.jpg#xywh=0,0,160,90\r\n\r\n' +
  '00:00:30.000 --> 00:00:40.000\r\nkhông-có-xywh.jpg\r\n';

describe('parseVttTime', () => {
  it('có và không có giờ; sai dạng → null', () => {
    expect(parseVttTime('01:02:03.500')).toBe(3723.5);
    expect(parseVttTime(' 02:03.250 ')).toBe(123.25);
    expect(parseVttTime('00:00:61.000')).toBeNull();
    expect(parseVttTime('1:2:3')).toBeNull();
  });
});

describe('parseThumbnailsVtt', () => {
  it('đọc cue, resolve sprite theo URL ký của file VTT, bỏ cue thiếu #xywh', () => {
    const cues = parseThumbnailsVtt(vtt, base);
    expect(cues).toHaveLength(3);
    expect(cues[1]).toEqual({
      start: 10,
      end: 20,
      url: 'http://localhost:8088/cdn/s/SIG/1794736800/hls/v1/sprite_001.jpg',
      x: 160,
      y: 0,
      w: 160,
      h: 90,
    });
    expect(cues[2].url).toBe('http://localhost:8088/cdn/s/SIG/1794736800/hls/v1/sprite_002.jpg');
    expect(cues[2].end).toBe(1005.5);
  });

  it('file rỗng hoặc hỏng → []', () => {
    expect(parseThumbnailsVtt('', base)).toEqual([]);
    expect(parseThumbnailsVtt('WEBVTT\n\nrác --> rác\nx\n', base)).toEqual([]);
  });
});

describe('findThumb', () => {
  const cues = parseThumbnailsVtt(vtt, base);
  it('tìm ô chứa thời điểm; ngoài khoảng đầu → null; sau cue cuối → cue cuối', () => {
    expect(findThumb(cues, 0)?.x).toBe(0);
    expect(findThumb(cues, 9.99)?.x).toBe(0);
    expect(findThumb(cues, 10)?.x).toBe(160);
    expect(findThumb(cues, 5000)?.url).toContain('sprite_002.jpg');
    expect(findThumb([], 3)).toBeNull();
    expect(findThumb(cues, -1)).toBeNull();
  });
});
