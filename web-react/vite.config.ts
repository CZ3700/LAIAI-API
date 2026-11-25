import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const apiTarget = process.env.VITE_API_TARGET || 'http://127.0.0.1:8080';
const frontendPort = Number(process.env.FRONTEND_PORT || process.env.VITE_PORT || 5174) || 5174;

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    dedupe: ['react', 'react-dom', 'react-is'],
  },
  server: {
    host: '0.0.0.0',
    port: frontendPort,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true },
      '/v1': { target: apiTarget, changeOrigin: true },
      '/v1beta': { target: apiTarget, changeOrigin: true },
      '/anthropic': { target: apiTarget, changeOrigin: true },
      '/mj': { target: apiTarget, changeOrigin: true },
      '/healthz': { target: apiTarget, changeOrigin: true },
    },
  },
  build: {
    assetsDir: 'assets',
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('/locales/') && id.endsWith('.json')) {
            return 'locales';
          }
          if (id.includes('node_modules')) {
            if (id.includes('recharts') || id.includes('d3-')) {
              return 'recharts-vendor';
            }
            if (id.includes('highlight.js') || id.includes('rehype') || id.includes('remark') || id.includes('md-editor-rt')) {
              return 'markdown-vendor';
            }
          }
        },
      },
    },
  },
});
