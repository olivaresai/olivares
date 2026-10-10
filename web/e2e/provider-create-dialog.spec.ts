// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import en from '../src/features/providers/i18n/en.json' with { type: 'json' }

// UI journey with a synthetic signed-in shell and empty provider inventory.
// No credentials, vendor requests or backend registration are involved.
test('provider dialog explains each kind and captures its missing fields', async ({
  page,
}, testInfo) => {
  await page.addInitScript(() => {
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't1' }, version: 0 }),
    )
    localStorage.setItem('olivares.theme', 'light')
  })
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const body = path.endsWith('/killswitch/state')
      ? { estate_stopped: false, active: [] }
      : path.endsWith('/auth/browser-session')
        ? {
            csrf_token: 'synthetic-csrf',
            session_id: 'provider-dialog',
            expires_at: '2999-01-01T00:00:00Z',
          }
        : path.endsWith('/server-info')
          ? {
              version: 'ui-test',
              engine: 'olivares',
              setup_required: false,
              license: { status: 'community' },
            }
          : path.endsWith('/auth/whoami')
            ? {
                kind: 'user',
                user_id: 'u1',
                actor: 'ui@example.com',
                superadmin: true,
                grants: [{ tenant: 't1', role: 'owner' }],
              }
            : { items: [], has_more: false }
    await route.fulfill({ json: body })
  })
  await page.goto('/providers')
  await page.getByRole('button', { name: 'Add your first provider' }).click()
  const dialog = page.getByRole('dialog', { name: 'Add a provider' })
  for (const kind of [
    'anthropic',
    'openai',
    'xai',
    'openai_compatible',
    'ollama',
  ] as const) {
    await dialog.getByRole('combobox', { name: 'Provider' }).click()
    await page
      .getByRole('option', { name: en.kinds[kind], exact: true })
      .click()
    await expect(dialog.getByRole('textbox', { name: 'Name' })).toHaveAttribute(
      'placeholder',
      en.kinds[kind],
    )
    await expect(
      dialog.getByText(en.kindHints[kind], { exact: true }),
    ).toHaveCount(1)
    const submit = dialog.getByRole('button', {
      name: 'Add provider',
      exact: true,
    })
    if (kind === 'ollama') {
      await expect(submit).toBeEnabled()
      await dialog.getByRole('textbox', { name: 'Endpoint' }).clear()
    }
    await expect(submit).toBeDisabled()
    await expect(dialog.getByRole('status')).toBeVisible()
    await expect(submit).toHaveAccessibleDescription(
      kind === 'ollama' ? /Endpoint/ : /API key/,
    )
    const capture = testInfo.outputPath(`provider-${kind}.png`)
    await dialog.screenshot({ path: capture })
    await testInfo.attach(`provider-${kind}`, {
      path: capture,
      contentType: 'image/png',
    })
  }
})
