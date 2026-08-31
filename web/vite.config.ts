// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { fileURLToPath } from 'node:url'
// defineConfig from vitest/config augments Vite's config with the typed `test` field.
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// The control plane serves the API and the web UI from the SAME origin (the web
// is embedded into the engine binary via go:embed). In production the client
// calls relative paths (/v1, /healthz, /openapi.json). In dev, Vite proxies those
// to a locally running engine so the app talks to a real backend. Point it at a
// different engine with VITE_API_TARGET (default: the engine's TLS-on default).
const apiTarget = process.env.VITE_API_TARGET ?? 'https://127.0.0.1:8443'
const apiProxy = {
  target: apiTarget,
  changeOrigin: true,
  // The engine generates a self-signed cert on first boot; accept it in dev.
  secure: false,
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  // CSP Level 3 (ADM-CORE-05): stamp a placeholder nonce onto the module entry,
  // modulepreload links, styles and the injected csp-nonce meta. The Go server
  // (cmd/olivares/webui.go) replaces `__CSP_NONCE__` with a fresh per-response
  // value and emits the matching `script-src 'nonce-…' 'strict-dynamic'`; Vite's
  // __vitePreload reads the meta nonce to authorize lazily-imported chunks.
  html: { cspNonce: '__CSP_NONCE__' },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/v1': apiProxy,
      '/healthz': apiProxy,
      '/openapi.json': apiProxy,
    },
  },
  build: {
    // Emit the SPA straight into the Go embed directory so `go:embed` bundles it
    // into the single `olivares` binary (see core/internal/webui/embed.go).
    // emptyOutDir is required because the target lives outside this project root.
    outDir: fileURLToPath(
      new URL('../core/internal/webui/dist', import.meta.url),
    ),
    emptyOutDir: true,
    // A control plane is a long-lived operator surface; a slightly larger initial
    // chunk is acceptable. Heavy, route-only deps (React Flow) are code-split.
    chunkSizeWarningLimit: 1200,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    css: false,
    // 30s, not the 5s default: the suite runs in the push gate, where a heavily
    // loaded machine can run an order of magnitude slower than an idle one (the
    // suite measures ~46s wall idle but 10-12x slower under heavy load), and at
    // 5s dozens of component tests died as spurious timeouts. Same reasoning as
    // the Go gate's -timeout 35m (Taskfile `test`): a raised ceiling never hides
    // a real hang, it only stops a slow box from failing a correct suite.
    testTimeout: 30_000,
    hookTimeout: 30_000,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    // Playwright owns e2e; vitest must not try to run it.
    exclude: ['e2e/**', 'node_modules/**', 'dist/**'],
    coverage: {
      provider: 'v8',
      reportsDirectory: './coverage',
      include: ['src/**/*.{ts,tsx}'],
      exclude: [
        'src/**/*.{test,spec}.{ts,tsx}',
        'src/test/**',
        'src/**/*.gen.ts',
      ],
    },
  },
})
