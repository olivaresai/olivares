// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { defineConfig, devices } from '@playwright/test'
import { existsSync, readdirSync } from 'node:fs'
import { homedir } from 'node:os'
import path from 'node:path'

/**
 * The K3 I1 browser journey: a REAL `olivares` binary serving the embedded bundle,
 * booted by `e2e-communications/global-setup.ts` with `--seed-demo` on its own
 * loopback port and data directory, two real authenticated actors, and NO
 * interception of any K3 endpoint on the accepted path. Run with
 *
 *   pnpm exec playwright test -c playwright.communications.config.ts
 *
 * after `task build:web` and a `bin/olivares` built from the same tree.
 */
const port = Number(process.env.K3_E2E_PORT ?? 8490)

/**
 * The Chromium this run drives. Playwright wants the exact build it ships with; when
 * that build is absent from the local browser cache (this box holds an earlier one
 * and downloads nothing during a journey), the NEWEST locally installed Chromium is
 * used instead and its version is recorded in the evidence. `K3_E2E_CHROMIUM` pins
 * one explicitly.
 */
function localChromium(): string | undefined {
  // A launcher script counts as an executable: the control workspace ships a
  // task-local runtime (`toolchains/browser-runtime/launch.sh`) that wraps the
  // cached Chromium with the system libraries this box lacks.
  const pinned = process.env.K3_E2E_CHROMIUM
  if (pinned) return pinned
  const cache =
    process.env.PLAYWRIGHT_BROWSERS_PATH ??
    path.join(homedir(), '.cache', 'ms-playwright')
  if (!existsSync(cache)) return undefined
  // The HEADLESS SHELL first: it is what Playwright itself drives in headless mode,
  // and it needs far fewer system libraries than the full browser (the full
  // Chromium of this cache cannot load libglib on this box).
  const builds = readdirSync(cache)
    .map((d) => /^(chromium_headless_shell|chromium)-(\d+)$/.exec(d))
    .filter((m): m is RegExpExecArray => m !== null)
    .map((m) => ({ dir: m[0], kind: m[1], build: Number(m[2]) }))
    .sort(
      (a, b) =>
        b.build - a.build ||
        (a.kind === 'chromium_headless_shell' ? -1 : 1) -
          (b.kind === 'chromium_headless_shell' ? -1 : 1),
    )
  for (const b of builds) {
    const rels =
      b.kind === 'chromium_headless_shell'
        ? [
            'chrome-headless-shell-linux64/chrome-headless-shell',
            'chrome-headless-shell-linux/chrome-headless-shell',
          ]
        : ['chrome-linux64/chrome', 'chrome-linux/chrome']
    for (const rel of rels) {
      const exe = path.join(cache, b.dir, rel)
      if (existsSync(exe)) return exe
    }
  }
  return undefined
}
const chromium = localChromium()

export default defineConfig({
  testDir: './e2e-communications',
  fullyParallel: false,
  workers: 1,
  timeout: 240_000,
  // The demo ledger makes the console's own audit poll cost seconds on SQLite, and the
  // single-writer store serialises the K3 reads behind it (measured 16 s for one inbox
  // page on this box). The journey asserts state, not latency, so the expectation
  // window is wide; the request durations are in the engine log beside the evidence.
  expect: { timeout: 60_000 },
  retries: 0,
  reporter: [['list']],
  globalSetup: './e2e-communications/global-setup.ts',
  globalTeardown: './e2e-communications/global-teardown.ts',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: chromium ? { executablePath: chromium } : undefined,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
