// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// MUST stay the first import: it configures Zod before any module builds a schema
// at import time, which is when Zod's CSP-violating `new Function` probe would run.
import '@/security/zod-jitless'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
// Self-hosted variable fonts — bundled by Vite and served from the same origin
// (no CDN; air-gap-ready, CSP-clean).
import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import '@fontsource-variable/space-grotesk'
import './index.css'
import '@/lib/i18n' // initialize i18next (side-effect) before the first render
import { Providers } from '@/app/providers'
import { router } from '@/app/router'
import { installTrustedTypes } from '@/security/trusted-types'

// CSP L3 (ADM-CORE-05): register the Trusted Types policies before React mounts, so
// any DOM sink is governed from the first paint under `require-trusted-types-for`.
installTrustedTypes()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Providers>
      <RouterProvider router={router} />
    </Providers>
  </StrictMode>,
)
