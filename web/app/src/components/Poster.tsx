import { useState } from 'react';
import type { Video } from '../api';
import { posterHue } from '../format';

export interface PosterProps {
  video: Pick<Video, 'id' | 'title' | 'poster_url'>;
  large?: boolean;
}

// Poster: ảnh poster.jpg qua URL ký (M5). Video chưa có poster hoặc ảnh lỗi (ví dụ URL ký đã hết hạn khi
// trang mở quá lâu) thì hiện khối màu theo id kèm chữ cái đầu như M4.
export function Poster({ video, large = false }: PosterProps) {
  const [failedUrl, setFailedUrl] = useState('');
  const className = large ? 'poster large' : 'poster';
  if (video.poster_url && video.poster_url !== failedUrl) {
    return (
      <img
        className={className}
        src={video.poster_url}
        alt=""
        loading="lazy"
        onError={() => setFailedUrl(video.poster_url ?? '')}
      />
    );
  }
  return (
    <div className={className} style={{ background: `hsl(${posterHue(video.id)} 45% 30%)` }} aria-hidden="true">
      {video.title.charAt(0).toUpperCase()}
    </div>
  );
}
