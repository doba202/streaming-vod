import { describe, expect, it } from 'vitest';
import { formatDuration, posterHue, progressPercent } from './format';

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

describe('progressPercent', () => {
  it('làm tròn và kẹp trong 0..100', () => {
    expect(progressPercent(42, 100)).toBe(42);
    expect(progressPercent(10, 30)).toBe(33);
    expect(progressPercent(150, 100)).toBe(100);
    expect(progressPercent(-5, 100)).toBe(0);
    expect(progressPercent(10, 0)).toBe(0);
  });
});
