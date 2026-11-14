import { describe, expect, it } from 'vitest';
import { formatDuration, posterHue } from './format';

describe('formatDuration', () => {
  it('định dạng phút:giây và giờ:phút:giây', () => {
    expect(formatDuration(0)).toBe('');
    expect(formatDuration(30)).toBe('0:30');
    expect(formatDuration(75)).toBe('1:15');
    expect(formatDuration(3723)).toBe('1:02:03');
  });
});

describe('posterHue', () => {
  it('ổn định và nằm trong 0..359', () => {
    const h = posterHue('11111111-1111-1111-1111-111111111111');
    expect(h).toBe(posterHue('11111111-1111-1111-1111-111111111111'));
    expect(h).toBeGreaterThanOrEqual(0);
    expect(h).toBeLessThan(360);
  });
});
