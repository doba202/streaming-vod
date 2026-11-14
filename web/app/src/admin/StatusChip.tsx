import type { VideoStatus } from '../api';

export const STATUS_LABELS: Record<VideoStatus, string> = {
  uploading: 'Đang upload',
  queued: 'Chờ xử lý',
  processing: 'Đang xử lý',
  ready: 'Sẵn sàng',
  failed: 'Lỗi',
};

// isProcessing: trạng thái do transcoder đẩy đi (queued, processing). Trang admin poll khi còn video ở đây.
// uploading không poll: nó chỉ đổi khi admin gọi /complete (hoặc bị cleaner xoá sau 24 giờ).
export function isProcessing(status: VideoStatus): boolean {
  return status === 'queued' || status === 'processing';
}

export function StatusChip({ status }: { status: VideoStatus }) {
  return <span className={`chip chip-${status}`}>{STATUS_LABELS[status] ?? status}</span>;
}
