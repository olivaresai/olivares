// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Page } from '@playwright/test'

export interface BrowserSession {
  csrf_token?: string
}

// page.request shares the page's cookie jar. Unsafe requests also need the
// metadata returned by the real browser login; the bearer stays HttpOnly.
export function browserSessionHeaders(
  session: BrowserSession | undefined,
  tenant: string,
): Record<string, string> {
  if (typeof session?.csrf_token !== 'string' || !session.csrf_token)
    throw new Error('browser session has no CSRF token')
  return {
    'X-Olivares-Session': 'cookie',
    'X-CSRF-Token': session.csrf_token,
    'X-Olivares-Tenant': tenant,
  }
}

export function journeyNowLink(page: Page) {
  return page
    .getByRole('navigation', { name: 'Journeys', exact: true })
    .getByRole('link', { name: 'Now', exact: true })
}
