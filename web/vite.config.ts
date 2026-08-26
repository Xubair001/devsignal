import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath, URL } from 'node:url';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    port: 5173,
    // The Go API runs on 8080. Proxying in dev keeps the browser same-origin, so
    // the session cookie and CORS behave exactly as they will in production
    // behind one host — rather than being a dev-only special case that breaks
    // the first time it is deployed.
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
      '/internal': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
});
