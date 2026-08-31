// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { defineConfig, devices } from '@playwright/test'

// The e2e smoke runs against a REAL olivares binary serving the embedded web
// bundle (built by `task build:web` then `task build:bin`), booted in --insecure
// mode on localhost by scripts/web-e2e.sh, which exports PLAYWRIGHT_BASE_URL and
// the one-time setup token. This verifies the go:embed bundle is actually served
// AND that the setup→login→shell flow authenticates against a live API.
const baseURL = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8456'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list']],
  use: {
    baseURL,
    // The engine is TLS-by-default with a self-signed cert; the launcher uses
    // --insecure for e2e, but keep this on so an HTTPS target also works.
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
