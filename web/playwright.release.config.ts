// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { defineConfig, devices } from '@playwright/test'
import { join } from 'node:path'
import { READY_MS } from './e2e-release/surface'

// Automatic failure-context DOM snapshots include input values, even when the
// screenshot/video paint is masked. Keep explicit, redacted journey evidence.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'

export default defineConfig({
  testDir: './e2e-release',
  workers: 1,
  retries: 0,
  forbidOnly: true,
  timeout: 180_000,
  // Wait for readiness, never a fixed 5 s: a slower host is not a broken page (#1177).
  expect: { timeout: READY_MS },
  // Playwright clears this directory at startup. Container identities live above it.
  outputDir: join(
    process.env.FIRST_HOUR_ARTIFACTS ?? 'test-results/release-first-hour',
    'browser',
  ),
  // HTML reports embed plaintext fill() arguments in their action metadata.
  // Videos and explicit screenshots remain in outputDir without that report.
  reporter: [['list']],
  use: {
    ...devices['Desktop Chrome'],
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? 'https://127.0.0.1:8443',
    ignoreHTTPSErrors: true,
    locale: 'en-US',
    actionTimeout: READY_MS,
    navigationTimeout: 15_000,
    screenshot: 'only-on-failure',
    video: 'on',
    // Traces and action reports can contain plaintext setup credentials.
    trace: 'off',
  },
})
