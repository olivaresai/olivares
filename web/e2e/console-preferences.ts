// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Page } from '@playwright/test'

/** These disposable-engine journeys use one organization; membership supplies its scope. */
export async function consoleTenant(page: Page): Promise<string> {
  return page.evaluate(async () => {
    const response = await fetch('/v1/auth/whoami', {
      credentials: 'same-origin',
    })
    if (!response.ok) throw new Error(`whoami failed (${response.status})`)
    const principal = (await response.json()) as {
      grants?: { tenant: string }[]
    }
    const tenant = principal.grants?.[0]?.tenant
    if (!tenant) throw new Error('the fixture account has no organization')
    return tenant
  })
}

export async function readConsolePreferences(
  page: Page,
): Promise<Record<string, string>> {
  const tenant = await consoleTenant(page)
  return page.evaluate(async (tenant) => {
    const response = await fetch('/v1/m/consoleviews/ui-state', {
      credentials: 'same-origin',
      headers: { 'X-Olivares-Tenant': tenant },
    })
    if (!response.ok)
      throw new Error(`preferences read failed (${response.status})`)
    return (
      ((await response.json()) as { preferences?: Record<string, string> })
        .preferences ?? {}
    )
  }, tenant)
}

/** Credentials stay inside the page. Use the same cookie and CSRF custody as the console. */
export async function setConsoleLanguage(
  page: Page,
  language: string,
): Promise<void> {
  const tenant = await consoleTenant(page)
  await page.evaluate(
    async ({ tenant, language }) => {
      const session = await fetch('/v1/auth/browser-session', {
        credentials: 'same-origin',
      })
      if (!session.ok)
        throw new Error(`session read failed (${session.status})`)
      const metadata = (await session.json()) as { csrf_token: string }
      const response = await fetch('/v1/m/consoleviews/ui-state', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': metadata.csrf_token,
          'X-Olivares-Tenant': tenant,
        },
        body: JSON.stringify({ preferences: { 'olivares.lang': language } }),
      })
      if (!response.ok)
        throw new Error(`language save failed (${response.status})`)
    },
    { tenant, language },
  )
}
