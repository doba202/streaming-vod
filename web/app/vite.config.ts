import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// Dev server (npm run dev, cổng 5173): /api và /cdn đi qua nginx của `make up` (cổng 8088),
// giống hệt đường đi của bản build nên cookie phiên và URL ký hoạt động như thật (ADR 0011).
const gateway = 'http://localhost:8088';

export default defineConfig({
  plugins: [react()],
  build: {
    // Chunk của trang xem chứa hls.js (~510 kB, đã tách lazy khỏi bundle chính).
    chunkSizeWarningLimit: 600,
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/api': gateway,
      '/cdn': gateway,
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
  },
});
