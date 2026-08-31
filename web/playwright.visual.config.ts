// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { defineConfig, devices } from '@playwright/test'

// Visual e2e for the visibility views. Unlike the smoke (which boots the real
// binary), this runs against `vite dev` with ALL /v1 calls mocked by Playwright
// route fixtures (e2e-visual/fixtures.ts), so every view — including the access-map
// R/RW graph (the marketing asset) — renders deterministically without a populated
// engine, in a real browser (so React Flow actually lays out, unlike jsdom).
const PORT = Number(process.env.VISUAL_PORT ?? 5199)

export default defineConfig({
  testDir: './e2e-visual',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 90_000,
  use: {
    baseURL: `http://localhost:${PORT}`,
    ignoreHTTPSErrors: true,
    viewport: { width: 1440, height: 900 },
    trace: 'retain-on-failure',
  },
  webServer: {
    command: `pnpm exec vite --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    stdout: 'ignore',
    stderr: 'pipe',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
