// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE COMMUNITY ROUTE SWEEP (dev#2485). Every top-level route of a Community build, opened on the
// demo estate, must answer without a single 501, and no navigation list may offer a page that
// docs/editions.md places in Business. A 501 is the engine saying "this route is Business": a
// Community page that asks for one has asked for something it cannot have.
//
// It runs on the estate `scripts/docs-captures.sh` seeds, THROUGH that script with `--grep @sweep`
// (the way console-review-captures.spec.ts runs with `--grep @review`), and it skips on a Business
// build, where the same pages load and answer 200.
//
// Output: web/playwright-report/sweep/routes-501.json (every 501 response: route, method, url) and
// one capture of each route under web/playwright-report/sweep/routes/.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import census from '../src/features/route-census.json' with { type: 'json' }
import { AUTH_ROUTES } from '../e2e-visual/routes'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const OUT = resolve(process.env.SWEEP_DIR ?? 'playwright-report/sweep')
// Approval kinds only a Business page acts on (scripts/lib/demo_edition.py): the demo dataset of a
// Community estate holds none.
const BUSINESS_APPROVAL_ACTIONS = ['deploy.promote', 'evidence.export']

interface Refusal {
  route: string
  method: string
  url: string
}

async function signIn(page: Page) {
  await page.addInitScript((tenant) => {
    window.localStorage.setItem('olivares.lang', 'en')
    window.localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: tenant }, version: 0 }),
    )
  }, demoTenant)
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 30_000,
  })
}

/** A file name for a route: `/access-map` is `access-map`, `/` is `home`. */
const slug = (path: string) =>
  path.replace(/[^a-z0-9]+/gi, '-').replace(/^-|-$/g, '') || 'home'

async function settle(page: Page) {
  // Live views poll for ever, so network idle is a courtesy and a timer is the bound.
  await page.waitForLoadState('networkidle', { timeout: 8_000 }).catch(() => {})
  await page.waitForTimeout(1_500)
}

async function buildEdition(page: Page) {
  const info = await page.request.get('/v1/server-info')
  return ((await info.json()) as { edition?: string }).edition
}

test.describe('@sweep Community serves only Community pages', () => {
  test.skip(
    !demoTenant,
    'needs the demo estate that scripts/docs-captures.sh seeds',
  )

  test('@sweep no route asks for a Business route (0 x 501) and no list offers a Business page', async ({
    page,
  }) => {
    test.setTimeout(20 * 60_000)
    await signIn(page)
    const edition = await buildEdition(page)
    test.skip(
      edition !== 'community',
      `edition ${edition}: the Business build serves these pages`,
    )

    const refusals: Refusal[] = []
    let route = ''
    page.on('response', (response) => {
      if (response.status() !== 501) return
      const { pathname, search } = new URL(response.url())
      refusals.push({
        route,
        method: response.request().method(),
        url: pathname + search,
      })
    })

    mkdirSync(resolve(OUT, 'routes'), { recursive: true })
    const offered = new Set<string>()
    for (const path of AUTH_ROUTES) {
      route = path
      await page.goto(path, { waitUntil: 'domcontentloaded' })
      await settle(page)
      await page.screenshot({
        path: resolve(OUT, 'routes', `${slug(path)}.png`),
      })
      // Every link of the navigation landmarks and the area directories is an offer.
      const hrefs = await page
        .locator('nav a[href], [data-slot="area-directory"] a[href]')
        .evaluateAll((links) =>
          links.map((a) => new URL((a as HTMLAnchorElement).href).pathname),
        )
      for (const href of hrefs) offered.add(href)
    }

    mkdirSync(OUT, { recursive: true })
    writeFileSync(
      resolve(OUT, 'routes-501.json'),
      JSON.stringify(
        { edition, routes: AUTH_ROUTES.length, refusals },
        null,
        2,
      ),
    )

    expect(
      refusals,
      `Community pages that made a request the engine refuses with 501:\n${refusals
        .map((r) => `  ${r.route}  ${r.method} ${r.url}`)
        .join('\n')}`,
    ).toEqual([])
    const queue = await page.request.get(
      '/v1/m/governance/approvals?limit=100',
      {
        headers: { 'X-Olivares-Tenant': demoTenant },
      },
    )
    expect(queue.status(), 'the approval queue must be readable').toBe(200)
    const actions = (
      (await queue.json()) as { items: { action: string }[] }
    ).items.map((approval) => approval.action)
    expect(actions.length, 'the demo estate holds approvals').toBeGreaterThan(0)
    expect(
      actions.filter((action) => BUSINESS_APPROVAL_ACTIONS.includes(action)),
      'the Community demo dataset holds an approval only a Business page can act on',
    ).toEqual([])
    expect(
      AUTH_ROUTES.length,
      'the sweep opened too few routes to prove anything',
    ).toBeGreaterThan(50)
    // A published address with a `$param` segment matches any one segment of an offered link.
    const businessPatterns = census.business_paths.map(
      (path) => new RegExp(`^${path.replace(/\$\w+/g, '[^/]+')}$`, 'i'),
    )
    expect(
      [...offered].filter((href) => businessPatterns.some((p) => p.test(href))),
      'a Community navigation list offers a page docs/editions.md places in Business',
    ).toEqual([])
  })

  test('@sweep a bookmarked Business page says it is part of Business and offers no action', async ({
    page,
  }) => {
    await signIn(page)
    const edition = await buildEdition(page)
    test.skip(
      edition !== 'community',
      `edition ${edition}: the Business build serves these pages`,
    )

    for (const path of census.business_paths) {
      await page.goto(path.replace(/\$\w+/g, 'x1'), {
        waitUntil: 'domcontentloaded',
      })
      await expect(
        page.getByRole('heading', { name: 'Part of Business' }),
        path,
      ).toBeVisible()
      await expect(
        page.getByRole('link', { name: 'About Business' }),
        path,
      ).toHaveAttribute('href', 'https://olivares.ai/business')
      await expect(page.getByRole('button'), path).toHaveCount(0)
    }
  })
})
