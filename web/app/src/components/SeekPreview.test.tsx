import { act, fireEvent, render, screen } from '@testing-library/react';
import { createRef } from 'react';
import { describe, expect, it } from 'vitest';
import type { ThumbCue } from '../player/thumbnails';
import { SeekPreview } from './SeekPreview';

const cues: ThumbCue[] = [
  { start: 0, end: 10, url: 'http://x/hls/v/sprite_001.jpg', x: 0, y: 0, w: 160, h: 90 },
  { start: 10, end: 20, url: 'http://x/hls/v/sprite_001.jpg', x: 160, y: 0, w: 160, h: 90 },
  { start: 20, end: 30, url: 'http://x/hls/v/sprite_001.jpg', x: 320, y: 0, w: 160, h: 90 },
];

// fakeVideo: jsdom không phát video; đặt duration/currentTime bằng defineProperty.
function fakeVideo(duration: number) {
  const video = document.createElement('video');
  let t = 0;
  Object.defineProperty(video, 'duration', { configurable: true, get: () => duration });
  Object.defineProperty(video, 'currentTime', {
    configurable: true,
    get: () => t,
    set: (v: number) => {
      t = v;
    },
  });
  return video;
}

function renderPreview(duration = 30) {
  const video = fakeVideo(duration);
  const ref = createRef<HTMLVideoElement>();
  (ref as { current: HTMLVideoElement | null }).current = video;
  render(<SeekPreview videoRef={ref} cues={cues} />);
  const bar = screen.getByRole('slider', { name: 'Tua video' });
  bar.getBoundingClientRect = () => ({ left: 100, width: 300, top: 0, height: 10, right: 400, bottom: 10, x: 100, y: 0, toJSON: () => ({}) });
  return { video, bar };
}

describe('SeekPreview', () => {
  it('rê chuột hiện ô sprite đúng thời điểm và mốc thời gian', () => {
    const { bar } = renderPreview();
    fireEvent.mouseMove(bar, { clientX: 100 + 150 }); // giữa thanh = 15s → ô thứ 2
    const thumb = screen.getByTestId('seek-preview').querySelector('.scrub-thumb') as HTMLElement;
    expect(thumb.style.backgroundPosition).toBe('-160px 0px');
    expect(thumb.style.backgroundImage).toContain('sprite_001.jpg');
    expect(screen.getByText('0:15')).toBeInTheDocument();
    fireEvent.mouseLeave(bar);
    expect(screen.queryByTestId('seek-preview')).toBeNull();
  });

  it('bấm để tua, phím mũi tên tua 10 giây', () => {
    const { video, bar } = renderPreview();
    fireEvent.click(bar, { clientX: 100 + 250 }); // 25s
    expect(video.currentTime).toBe(25);
    act(() => {
      video.dispatchEvent(new Event('timeupdate'));
    });
    expect(bar).toHaveAttribute('aria-valuenow', '25');
    fireEvent.keyDown(bar, { key: 'ArrowRight' });
    expect(video.currentTime).toBe(30); // kẹp ở duration
    fireEvent.keyDown(bar, { key: 'ArrowLeft' });
    expect(video.currentTime).toBe(15); // current trong state vẫn là 25 → 15
  });

  it('chưa biết thời lượng thì không hiện', () => {
    const video = fakeVideo(Number.NaN);
    const ref = { current: video };
    const { container } = render(<SeekPreview videoRef={ref} cues={cues} />);
    expect(container).toBeEmptyDOMElement();
  });
});
