import path from 'node:path'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// The control plane API listens on 127.0.0.1:8099 by default (HACO_LISTEN_ADDR).
// Override with HACO_API for a control plane on another port or host.
const api = process.env.HACO_API ?? 'http://127.0.0.1:8099'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { '@': path.resolve(import.meta.dirname, 'src') } },
  server: { proxy: { '/v1': { target: api, changeOrigin: true } } },
})
