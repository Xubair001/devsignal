import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath, URL } from 'node:url';

const apiTarget = process.env.DEVSIGNAL_API_URL ?? 'http://localhost:8090';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  server: {
    // Proxying in dev keeps the browser same-origin, so CORS behaves exactly as
    // it will in production behind one host rather than being a dev-only special
    // case that breaks the first time it is deployed.
    //
    // Both ports are off the defaults, for the same reason the Postgres and
    // Redis ports are: this machine runs other projects, and 5173 and 8080 are
    // already taken by one of them. Override with DEVSIGNAL_WEB_PORT and
    // DEVSIGNAL_API_URL.
    port: Number(process.env.DEVSIGNAL_WEB_PORT ?? 5174),
    strictPort: true,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true },
      '/internal': { target: apiTarget, changeOrigin: true },
    },
  },
});
