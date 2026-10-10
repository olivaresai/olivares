// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { defineConfig } from '@playwright/test'
import config from './playwright.config'

// Build first with `pnpm build --outDir dist`. A dev server does not exercise
// production chunk loading or the browser's cached failed module imports.
export default defineConfig({
  ...config,
  testMatch: 'shell-import-recovery.spec.ts',
  retries: 0,
  use: { ...config.use, baseURL: 'http://127.0.0.1:8467', locale: 'en-US' },
  webServer: {
    command:
      'pnpm preview --outDir dist --host 127.0.0.1 --port 8467 --strictPort',
    url: 'http://127.0.0.1:8467',
    reuseExistingServer: false,
  },
})
