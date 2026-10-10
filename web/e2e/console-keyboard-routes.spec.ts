// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// KEYBOARD AND LABELLED CONTROLS on the named console routes, against a live `--seed-demo`
// engine. Nothing on this path is intercepted.
//
// ⛔ THIS SPEC NEEDS THE SEEDED ESTATE, AND IT SAYS SO INSTEAD OF PASSING WITHOUT IT.
// `scripts/web-e2e.sh` puts every spec that does not fill `#token` on ONE shared
// engine that nothing ever sets up, so a spec written against `demo@olivares.local`
// finds no password box there, signs nobody in, and then measures the LOGIN PAGE
// while reporting the ten routes green. That is the failure this repository names
// in `console-walk`: a run that reaches nothing is not clean, it is blind. The
// `DEMO_TENANT` guard turns that silent pass into a visible skip (the same guard
// the console-func lots carry), and `scripts/web-e2e-demo.sh` is where this spec
// actually runs, against `serve --insecure --seed-demo`.
import { expect, test, type Page } from '@playwright/test'
import { rawKeyProbe } from './raw-i18n'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

const ROUTES = [
  '/login',
  '/inventory',
  '/work',
  '/access-map',
  '/routine-policies',
  '/sessions',
  '/finops',
  '/audit',
  '/alerting',
  '/settings',
  '/settings?section=keyboard',
] as const

// Synthetic estate identifiers are data, not translations. Keep exemptions literal
// and route-specific so a new unresolved UI label still fails the browser gate.
const ROUTE_IDENTIFIERS: Record<string, readonly string[]> = {
  '/access-map': [
    'appdb.public.customers',
    'appdb.public.orders',
    'appdb.public.secrets',
    'appdb.public.logs',
    'appdb.public.billing',
    'appdb.public.reviews',
    'appdb.public.archive',
    'postgres.table',
    'agent.task',
    'mcp.server',
    'mcp.tool',
  ],
}

async function signIn(page: Page) {
  await page.goto('/login')
  // Fail closed: no password box means this is not the seeded engine, and every
  // assertion after this point would grade the wrong page.
  await expect(
    page.locator('input[type=password]'),
    'the login form must be present — without it nothing below measures a route',
  ).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  const login = page.waitForResponse(
    (response) =>
      response.url().endsWith('/v1/auth/login') &&
      response.request().method() === 'POST',
  )
  await page.locator('button[type=submit]').first().click()
  expect(
    (await login).status(),
    'demo sign-in must succeed before inspecting routes',
  ).toBe(200)
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

test('raw-key probe catches untranslated text and links', async ({ page }) => {
  await page.setContent(
    '<main><h1>keys.command.session.new</h1><a href="/settings">keys.command.sourceDiff.nextFile</a></main>',
  )
  expect(await page.evaluate(rawKeyProbe)).toEqual([
    'keys.command.session.new',
    'keys.command.sourceDiff.nextFile',
  ])
})

test.describe('keyboard on the named routes', () => {
  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test('skip-to-content is the first tab stop on login and on inventory', async ({
    page,
  }) => {
    await page.goto('/login')
    await page.locator('a[href="#main-content"]').waitFor()
    await page.keyboard.press('Tab')
    const skip = page.locator('a[href="#main-content"]')
    await expect(skip).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page.locator('#main-content')).toBeFocused()

    await signIn(page)
    await page.goto('/inventory')
    await page.keyboard.press('Tab')
    await expect(page.locator('a[href="#main-content"]')).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(page.locator('#main-content')).toBeFocused()
  })

  for (const lang of ['en', 'es']) {
    test(`Keyboard alternatives in ${lang}`, async ({ page }, testInfo) => {
      await page.addInitScript((language) => {
        localStorage.setItem('olivares.lang', language)
      }, lang)
      await signIn(page)
      await page.goto('/settings?section=keyboard')
      await expect(page.locator('html')).toHaveAttribute('lang', lang)
      // prettier-ignore
      const spanishLabel = 'Abrir la sesión' // language-data: Spanish keyboard command label
      const label = lang === 'en' ? 'Open the session' : spanishLabel
      const row = page.getByRole('listitem').filter({ hasText: label })
      await expect(row).toHaveCount(1)
      await expect(row.locator('kbd')).toHaveText(['Enter', 'Space'])
      expect(await page.evaluate(rawKeyProbe)).toEqual([])
      await page.screenshot({
        path: testInfo.outputPath(`keyboard-${lang}.png`),
        fullPage: true,
      })
      await testInfo.attach(`keyboard-${lang}`, {
        path: testInfo.outputPath(`keyboard-${lang}.png`),
        contentType: 'image/png',
      })
    })
  }

  for (const route of ROUTES) {
    test(`labelled controls and resolved translations on ${route}`, async ({
      page,
    }) => {
      if (route !== '/login') await signIn(page)
      await page.goto(route)
      await page.waitForLoadState('domcontentloaded')
      const main = page.locator('#main-content')
      await expect(page).toHaveURL(new URL(route, page.url()).href)
      await expect(main.getByRole('heading').first()).toBeVisible()
      if (route === '/settings?section=keyboard') {
        await expect(
          page.getByText('New session', { exact: true }).last(),
        ).toBeVisible()
      }
      const raw = await page.evaluate(rawKeyProbe)
      expect(
        raw.filter((text) => !ROUTE_IDENTIFIERS[route]?.includes(text)),
        `raw i18n keys on ${route}`,
      ).toEqual([])
      const unlabeled = await page.evaluate(() => {
        const root = document.getElementById('main-content')
        if (!root) return ['#main-content missing']
        const bad: string[] = []
        for (const el of root.querySelectorAll(
          'button, a[href], input, select, textarea',
        )) {
          const node = el as HTMLElement
          const name = (
            node.getAttribute('aria-label') ||
            node.getAttribute('aria-labelledby') ||
            node.getAttribute('title') ||
            (node instanceof HTMLInputElement
              ? node.labels?.[0]?.textContent
              : '') ||
            node.textContent ||
            ''
          )
            .replace(/\s+/g, ' ')
            .trim()
          if (!name) {
            bad.push(
              `${node.tagName.toLowerCase()}${node.id ? '#' + node.id : ''}.${node.className.toString().slice(0, 40)}`,
            )
          }
        }
        return bad
      })
      expect(unlabeled, `unlabeled controls on ${route}`).toEqual([])
    })
  }
})
