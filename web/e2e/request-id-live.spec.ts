// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// El `X-Request-ID` en pantalla, contra un MOTOR REAL.
//
// ⛔ Lo que esto ve y el unit test no: que `client.ts` LEA la cabecera de una respuesta HTTP de
//    verdad (el mock la inyecta ya parseada), que `AsyncSection` monte el `ErrorState` en la ruta
//    real, y que la clave `errors:requestId` RESUELVA en el bundle servido. El fallo del que nace
//    Era justo de esa clase: el dato existía y no llegaba a la pantalla.
import { test, expect } from '@playwright/test'
import type { Page } from '@playwright/test'

const BASE = process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:8489'
const EMAIL = process.env.DEMO_EMAIL ?? 'demo@olivares.local'
const PASSWORD = process.env.DEMO_PASSWORD ?? 'olivares-demo-estate'
const RID = 'rid-e2e-0123456789'

async function entra(page: Page) {
  await page.goto(`${BASE}/login`)
  await page.locator('#email').fill(EMAIL)
  await page.locator('#password').fill(PASSWORD)
  await page.getByRole('button', { name: /^sign in$/i }).click()
  await expect(
    page.getByRole('link', { name: 'Inventory', exact: true }),
  ).toBeVisible({ timeout: 20_000 })
}

test('un 500 CON X-Request-ID enseña el id en pantalla', async ({ page }) => {
  await entra(page)
  let servido = false
  await page.route('**/v1/m/catalog/entries**', async (route) => {
    servido = true
    await route.fulfill({
      status: 500,
      headers: { 'content-type': 'application/json', 'x-request-id': RID },
      body: JSON.stringify({ error: 'boom' }),
    })
  })
  await page.goto(`${BASE}/catalog`, { waitUntil: 'networkidle' })
  expect(servido, 'la consola no llegó a pedir /catalog/entries').toBe(true)
  await expect(page.getByText(RID, { exact: false }).first()).toBeVisible({
    timeout: 15000,
  })
})

test('CONTROL NEGATIVO: un 500 SIN la cabecera no inventa ningún id', async ({
  page,
}) => {
  await entra(page)
  let servido = false
  await page.route('**/v1/m/catalog/entries**', async (route) => {
    servido = true
    await route.fulfill({
      status: 500,
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ error: 'boom' }),
    })
  })
  await page.goto(`${BASE}/catalog`, { waitUntil: 'networkidle' })
  expect(servido, 'la consola no llegó a pedir /catalog/entries').toBe(true)
  // El error TIENE que estar pintado: si no, este control pasaría en vacío.
  await expect(page.getByText(/error|failed|falló/i).first()).toBeVisible({
    timeout: 15000,
  })
  await expect(page.getByText(/rid-e2e/)).toHaveCount(0)
})
