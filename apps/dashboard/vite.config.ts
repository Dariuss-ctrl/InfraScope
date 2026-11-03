/// <reference types="vitest/config" />
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  build: {
    // The charts chunk (ECharts, about 520 kB) loads only with the first
    // chart. The limit sits just above it, so any other chunk growing that
    // large still warns.
    chunkSizeWarningLimit: 560,
  },
  server: {
    // Mirrors the nginx proxy in production. Run the API locally on :8081
    // (`go run ./cmd/api`, or `make up-debug`, which publishes it).
    proxy: {
      '/api': 'http://localhost:8081',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
  },
})
