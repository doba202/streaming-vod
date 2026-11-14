import Hls, { type ErrorData } from 'hls.js';
import { useEffect, useRef, useState } from 'react';
import { type PlayInfo, api, sendPlaybackEvent } from '../api';
import { MediaRecovery, PlaybackSession, classifyHlsError } from '../player/recovery';
import { PlaybackReporter } from '../player/reporter';
import { type ThumbCue, parseThumbnailsVtt } from '../player/thumbnails';
import { SeekPreview } from './SeekPreview';

interface Level {
  index: number;
  height: number;
}

// Player phát HLS qua URL ký từ /play. hls.js cho Chrome/Firefox/Edge; Safari dùng HLS gốc của <video>.
// Khi URL hết hạn (403) hoặc lỗi mạng: PlaybackSession gọi lại /play và phát tiếp từ vị trí hiện tại.
// M5: phụ đề bằng <track> (URL ký từ /play), ảnh xem trước khi tua (SeekPreview + thumbnails.vtt), gửi sự kiện
// xem (PlaybackReporter → POST /api/playback/events) để có "Xem tiếp" và analytics.
// Kiểm tra tay trên Chrome và Safari (spec §9); logic có test trong player/*.test.ts.
export function Player({ videoId }: { videoId: string }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);
  const [error, setError] = useState('');
  const [levels, setLevels] = useState<Level[]>([]);
  const [level, setLevel] = useState(-1); // -1 = tự động (ABR)
  const [info, setInfo] = useState<PlayInfo | null>(null);
  const [cues, setCues] = useState<ThumbCue[]>([]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    setError('');
    setLevels([]);
    setLevel(-1);
    setInfo(null);

    const reporter = new PlaybackReporter({
      videoId,
      send: sendPlaybackEvent,
      position: () => video.currentTime,
      // bitrate của mức đang phát (hls.js); Safari HLS gốc không cho biết → 0.
      bitrateKbps: () => {
        const hls = hlsRef.current;
        return hls && hls.currentLevel >= 0 ? (hls.levels[hls.currentLevel]?.bitrate ?? 0) / 1000 : 0;
      },
    });
    const media: Array<[string, () => void]> = [
      ['play', () => reporter.play()],
      ['pause', () => reporter.pause()],
      ['seeked', () => reporter.seeked()],
      ['ended', () => reporter.ended()],
      ['waiting', () => reporter.waiting()],
      ['playing', () => reporter.playing()],
    ];
    media.forEach(([e, fn]) => video.addEventListener(e, fn));
    // Đóng tab / tải lại trang: ghi vị trí cuối (fetch keepalive vẫn gửi được khi trang đang đóng).
    const onPageHide = () => reporter.flush();
    window.addEventListener('pagehide', onPageHide);

    const mediaRecovery = new MediaRecovery();
    let lastProgress = video.currentTime;
    // Phát tiến triển (currentTime tăng) thì coi như đã phục hồi: cho phép cứu lại từ đầu.
    const onTimeUpdate = () => {
      if (video.currentTime > lastProgress + 1) mediaRecovery.reset();
      lastProgress = video.currentTime;
    };
    video.addEventListener('timeupdate', onTimeUpdate);
    let pendingMeta: (() => void) | null = null;
    const clearPendingMeta = () => {
      if (pendingMeta) video.removeEventListener('loadedmetadata', pendingMeta);
      pendingMeta = null;
    };

    const session = new PlaybackSession({
      fetchPlay: () => api.play(videoId),
      onInfo: setInfo,
      currentTime: () => video.currentTime,
      onFatal: setError,
      load: (url, startAt) => {
        const resume = !video.paused;
        hlsRef.current?.destroy();
        hlsRef.current = null;
        clearPendingMeta();
        mediaRecovery.reset();
        lastProgress = startAt;
        // Hls mới luôn bắt đầu ở "Tự động": đưa dropdown về cùng trạng thái.
        setLevel(-1);
        if (Hls.isSupported()) {
          const hls = new Hls({ startPosition: startAt > 0 ? startAt : -1 });
          hlsRef.current = hls;
          hls.on(Hls.Events.MANIFEST_PARSED, (_e, data) => {
            setLevels(data.levels.map((l, index) => ({ index, height: l.height })).sort((a, b) => b.height - a.height));
            if (resume) void video.play().catch(() => undefined);
          });
          hls.on(Hls.Events.ERROR, (_e, data: ErrorData) => {
            switch (classifyHlsError(data)) {
              case 'refresh':
                void session.refresh();
                break;
              case 'media': {
                const step = mediaRecovery.next();
                if (step === 'fatal') {
                  setError('Không phát được video.');
                } else {
                  if (step === 'swap-recover') hls.swapAudioCodec();
                  hls.recoverMediaError();
                }
                break;
              }
              case 'fatal':
                setError('Không phát được video.');
                break;
            }
          });
          hls.loadSource(url);
          hls.attachMedia(video);
        } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = url;
          pendingMeta = () => {
            pendingMeta = null;
            if (startAt > 0) video.currentTime = startAt;
            if (resume) void video.play().catch(() => undefined);
          };
          video.addEventListener('loadedmetadata', pendingMeta, { once: true });
        } else {
          setError('Trình duyệt không hỗ trợ HLS.');
        }
      },
    });

    // Safari (HLS gốc) không cho biết mã HTTP: lỗi nạp nguồn nào cũng thử lấy URL mới.
    const onNativeError = () => {
      if (!hlsRef.current && video.error?.code === MediaError.MEDIA_ERR_NETWORK) void session.refresh();
    };
    video.addEventListener('error', onNativeError);
    void session.start();

    return () => {
      // Rời trang xem trong SPA: ghi vị trí cuối trước khi gỡ listener (video.load() bên dưới phát "pause").
      reporter.flush();
      reporter.dispose();
      media.forEach(([e, fn]) => video.removeEventListener(e, fn));
      window.removeEventListener('pagehide', onPageHide);
      session.dispose();
      video.removeEventListener('error', onNativeError);
      video.removeEventListener('timeupdate', onTimeUpdate);
      clearPendingMeta();
      hlsRef.current?.destroy();
      hlsRef.current = null;
      video.removeAttribute('src');
      video.load();
    };
  }, [videoId]);

  // thumbnails.vtt: tải lại mỗi khi URL ký đổi (lần đầu và sau khi làm mới /play). Lỗi → không có ảnh xem trước.
  const thumbnailsUrl = info?.thumbnails_url;
  useEffect(() => {
    setCues([]);
    if (!thumbnailsUrl) return;
    const ctrl = new AbortController();
    fetch(thumbnailsUrl, { signal: ctrl.signal })
      .then((res) => (res.ok ? res.text() : Promise.reject(new Error(`HTTP ${res.status}`))))
      .then((text) => setCues(parseThumbnailsVtt(text, thumbnailsUrl)))
      .catch(() => undefined);
    return () => ctrl.abort();
  }, [thumbnailsUrl]);

  const changeLevel = (value: number) => {
    setLevel(value);
    if (hlsRef.current) hlsRef.current.currentLevel = value;
  };

  return (
    <div className="player">
      {/* crossOrigin: khi chạy Vite dev (:5173) URL ký là origin khác (:8088); <track> cần CORS
          (nginx trả Access-Control-Allow-Origin: * cho /cdn/). Không gửi cookie tới CDN. */}
      <video ref={videoRef} controls playsInline crossOrigin="anonymous">
        {info?.subtitles.map((s) => (
          <track key={s.lang} kind="subtitles" src={s.url} srcLang={s.lang} label={s.label} />
        ))}
      </video>
      {cues.length > 0 && <SeekPreview videoRef={videoRef} cues={cues} />}
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
      {levels.length > 1 && (
        <label className="quality">
          Chất lượng
          <select value={level} onChange={(e) => changeLevel(Number(e.target.value))}>
            <option value={-1}>Tự động</option>
            {levels.map((l) => (
              <option key={l.index} value={l.index}>
                {l.height}p
              </option>
            ))}
          </select>
        </label>
      )}
    </div>
  );
}
