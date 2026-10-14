import { svelte } from '@sveltejs/vite-plugin-svelte'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  server: {
    proxy: {
      '/api': {
        // Default targets the live API. The fe2 e2e suites override this via
        // KANBAN_API_TARGET to point at a test-DB API instance instead (see
        // web/e2e/README.md), keeping test writes out of the live board DB.
        target: process.env.KANBAN_API_TARGET || 'http://127.0.0.1:18080',
        changeOrigin: true,
        // SSE-safe proxying (landed in M3): http-proxy
        // keeps the client response open when the upstream dies, so an
        // EventSource would never error and the client would never
        // reconnect/resync in dev. Destroy the client response when an
        // event-stream's upstream connection goes away. Buffering proved
        // not an issue in practice (the stream flushes immediately), so no
        // dedicated '/api/events' entry was needed.
        configure: (proxy) => {
          proxy.on('proxyRes', (proxyRes, _req, res) => {
            if (String(proxyRes.headers['content-type'] ?? '').includes('text/event-stream')) {
              proxyRes.on('close', () => {
                if (!res.writableEnded) res.destroy()
              })
            }
          })
        },
      },
    },
  },
})
