import Hls, { type ErrorData } from 'hls.js';
import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { PlaybackSession, classifyHlsError } from '../player/recovery';

interface Level {
  index: number;
  height: number;
}

// Player phát HLS qua URL ký từ /play. hls.js cho Chrome/Firefox/Edge; Safari dùng HLS gốc của <video>.
// Khi URL hết hạn (403) hoặc lỗi mạng: PlaybackSession gọi lại /play và phát tiếp từ vị trí hiện tại.
// Kiểm tra tay trên Chrome và Safari (spec §9); logic phục hồi có test trong player/recovery.test.ts.
export function Player({ videoId }: { videoId: string }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const hlsRef = useRef<Hls | null>(null);
  const [error, setError] = useState('');
  const [levels, setLevels] = useState<Level[]>([]);
  const [level, setLevel] = useState(-1); // -1 = tự động (ABR)

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    setError('');
    setLevels([]);
    setLevel(-1);

    const session = new PlaybackSession({
      fetchPlay: () => api.play(videoId),
      currentTime: () => video.currentTime,
      onFatal: setError,
      load: (url, startAt) => {
        const resume = !video.paused;
        hlsRef.current?.destroy();
        hlsRef.current = null;
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
              case 'media':
                hls.recoverMediaError();
                break;
              case 'fatal':
                setError('Không phát được video.');
                break;
            }
          });
          hls.loadSource(url);
          hls.attachMedia(video);
        } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = url;
          video.addEventListener(
            'loadedmetadata',
            () => {
              if (startAt > 0) video.currentTime = startAt;
              if (resume) void video.play().catch(() => undefined);
            },
            { once: true },
          );
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
      session.dispose();
      video.removeEventListener('error', onNativeError);
      hlsRef.current?.destroy();
      hlsRef.current = null;
      video.removeAttribute('src');
      video.load();
    };
  }, [videoId]);

  const changeLevel = (value: number) => {
    setLevel(value);
    if (hlsRef.current) hlsRef.current.currentLevel = value;
  };

  return (
    <div className="player">
      <video ref={videoRef} controls playsInline />
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
