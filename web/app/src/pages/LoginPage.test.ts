import { describe, expect, it } from 'vitest';
import { returnTo } from './LoginPage';

describe('returnTo', () => {
  it('chỉ nhận đường dẫn nội bộ', () => {
    expect(returnTo({ from: '/watch/abc?x=1' })).toBe('/watch/abc?x=1');
    expect(returnTo(null)).toBe('/');
    expect(returnTo({ from: 'https://evil.example' })).toBe('/');
    expect(returnTo({ from: '//evil.example' })).toBe('/');
    expect(returnTo({ from: '/\\evil.example' })).toBe('/');
  });
});
