import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// `npm run dev` proxies the API to a hub running on :8080.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', ws: true },
      '/install.sh': 'http://127.0.0.1:8080',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: false,
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks: {
          echarts: ['echarts'],
          mantine: ['@mantine/core', '@mantine/hooks', '@mantine/notifications', '@mantine/modals'],
        },
      },
    },
  },
});
