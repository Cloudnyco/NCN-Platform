import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import compression from 'vite-plugin-compression'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [
    vue(),
    // Pre-compute .gz alongside every asset > 1KB. nginx serves these
    // via gzip_static at zero runtime CPU cost. Ratio is identical to
    // runtime gzip (level 9 default) but the wall-clock per request is
    // ~0.
    compression({
      algorithm: 'gzip',
      ext: '.gz',
      threshold: 1024,
      deleteOriginFile: false,
    }),
    // Same for brotli — pre-computed .br at the highest practical level
    // (11 takes minutes per build but only runs in CI/local). nginx
    // serves these via brotli_static. ~15-25% smaller than gzip for
    // JS/CSS bundles in practice, especially the bigger vendor chunks.
    compression({
      algorithm: 'brotliCompress',
      ext: '.br',
      threshold: 1024,
      deleteOriginFile: false,
      compressionOptions: { level: 11 },
    }),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  build: {
    // Code-splitting hints for first-paint priorities.
    //   `vendor-vue`  — vue + vue-router + pinia: ~70 KB raw, very
    //      cacheable, almost never changes between deploys.
    //   `vendor-i18n` — vue-i18n: ~25 KB raw, separated so the home
    //      page does not need to pull anything else from app code.
    //   Other deps stay in their lazy chunks (xterm in Terminal,
    //   webauthn helpers in Login, etc.)
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules')) {
            if (id.includes('vue-i18n') || id.includes('@intlify')) return 'vendor-i18n'
            if (id.includes('/vue/')   || id.includes('/vue-router/') || id.includes('/pinia/')) return 'vendor-vue'
          }
        },
      },
    },
    // Source maps add ~50% to the bundle and are pulled by browsers
    // even when DevTools is closed. CI rebuilds for debugging instead.
    sourcemap: false,
    // esbuild minifier is fast + reliable; terser saves another ~5-8%
    // but is 10x slower and we already gzip/brotli before serving, so
    // the wire savings are mostly absorbed.
    minify: 'esbuild',
  },
  server: {
    host: 'localhost',
    port: 5173,
    strictPort: false,
    proxy: {
      // Reverse-proxy /api/* to the Go backend during dev.
      // In prod the same path is proxied by nginx (see deploy/nginx-*.conf).
      '/api': {
        target: 'http://127.0.0.1:9000',
        changeOrigin: false
      }
    }
  }
})
