// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { defineConfig, devices } from '@playwright/test'

// Visual e2e for the intelligence views. Unlike the foundation smoke (which
// boots the real binary), this drives the Vite dev server with the API fully mocked
// at the browser layer (page.route) and a seeded session, so each view renders from
// deterministic FIXTURES in light + dark — no backend, no real data required. Kept in
// its own config so it never collides with playwright.config.ts (binary-backed).
const PORT = 5491

export default defineConfig({
  testDir: './e2e-intel',
  testMatch: '**/*.visual.spec.ts',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list']],
  outputDir: 'playwright-report/intel',
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: `pnpm exec vite --port ${PORT} --strictPort --host 127.0.0.1`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
