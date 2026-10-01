// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test, type Locator, type Page } from '@playwright/test'
import { accessGraph, fixtureFor, sessionsLive } from './fixtures'

async function mockApi(page: Page, theme: string, longLabels = false) {
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p.endsWith('/stream')) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    let payload = fixtureFor(p) ?? { items: [], has_more: false }
    if (longLabels && p.endsWith('/accessmap/graph')) {
      const label = (id: unknown) =>
        `${id}:production.customer.billing.reconciliation.archive.2026-09-30.region-europe-west.primary.replica.long-reference`
      payload = {
        ...accessGraph,
        nodes: accessGraph.nodes.map((n) => ({ ...n, ref: label(n.id) })),
        edges: accessGraph.edges.map((e: Record<string, unknown>) => ({
          ...e,
          origin_ref:
            e.origin_kind === 'session' ? 'sess-9f2a' : label(e.origin_id),
          resource_ref: label(e.resource_id),
        })),
      }
    }
    if (longLabels && p.endsWith('/sessions/live'))
      payload = {
        ...sessionsLive,
        items: sessionsLive.items.map((s) => ({
          ...s,
          goal: 'Reconcile customer billing records across production databases and archive the verified results for the monthly compliance review',
        })),
      }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(payload),
    })
  })
  await page.addInitScript(
    ({ theme }) => {
      localStorage.setItem(
        'olivares.session',
        JSON.stringify({
          state: {
            token: 'olvs_demo',
            sessionId: 's1',
            expiresAt: '2030-01-01T00:00:00Z',
          },
          version: 0,
        }),
      )
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', theme)
    },
    { theme },
  )
}

for (const theme of ['light', 'dark']) {
  for (const width of [1440, 1280]) {
    test(`long graph labels do not overlap at ${width} in ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 })
      await mockApi(page, theme, true)
      await page.goto('/access-map')
      await expect(page.locator('.react-flow__node')).toHaveCount(8)
      await expect(
        page
          .locator('.react-flow__node')
          .filter({ hasText: 'Reconcile customer billing' }),
      ).toHaveCount(1)
      const overlaps = () =>
        page.locator('.react-flow__node').evaluateAll((nodes) =>
          nodes.flatMap((a, i) =>
            nodes.slice(i + 1).flatMap((b) => {
              const ar = a.getBoundingClientRect(),
                br = b.getBoundingClientRect()
              return ar.left < br.right - 1 &&
                br.left < ar.right - 1 &&
                ar.top < br.bottom - 1 &&
                br.top < ar.bottom - 1
                ? [`${a.getAttribute('data-id')}/${b.getAttribute('data-id')}`]
                : []
            }),
          ),
        )
      try {
        await expect.poll(overlaps).toEqual([])
        await expectFullText(page.locator('.react-flow__node [title]'))
      } finally {
        await page.screenshot({
          path: testInfo.outputPath('long-labels.png'),
          fullPage: true,
        })
      }
    })
  }
}

async function expectFullText(locator: Locator) {
  const clipped = await locator.evaluateAll((elements) =>
    elements.flatMap((el) => {
      const box = el.getBoundingClientRect()
      if (!box.width || !box.height) return []
      return el.scrollWidth > el.clientWidth + 1 ||
        el.scrollHeight > el.clientHeight + 1
        ? [el.textContent]
        : []
    }),
  )
  expect(clipped, 'full text fits its rendered box').toEqual([])
}

for (const theme of ['light', 'dark']) {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 1280, height: 800 },
    { width: 390, height: 844 },
  ]) {
    test(`page text remains readable at ${viewport.width} in ${theme}`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport)
      await mockApi(page, theme)
      await page.goto('/console')
      const consoleTitle = page.getByRole('heading', {
        level: 1,
        name: 'Control console',
      })
      await expect(consoleTitle).toBeVisible()
      const titleBox = await consoleTitle.boundingBox()
      expect(titleBox).not.toBeNull()
      expect(titleBox!.width).toBeGreaterThan(180)
      expect(titleBox!.height).toBeLessThan(70)
      const usersHeading = page.getByRole('heading', {
        name: 'Users',
        exact: true,
      })
      await expect(usersHeading).toBeInViewport()
      await page.goto('/agentcore-export')
      await expect(page.locator('main h1')).toBeVisible()
      await expectFullText(
        page.locator(
          '[data-slot="page-header"] h1, [data-slot="page-header"] p',
        ),
      )
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(viewport.width)
      await page.goto('/access-map')
      await page.getByRole('button', { name: /permitted vs observed/i }).click()
      await expect(
        page
          .locator('section header')
          .getByText('Unexpected access', { exact: true }),
      ).toBeVisible()
      const scopeNote = page.getByText(
        'The drift list covers the whole organization; map filters do not apply to it',
        { exact: true },
      )
      await expect(scopeNote).toHaveCount(0)
      await page
        .getByRole('textbox', { name: /search origin, resource, tool/i })
        .fill('orchestrator')
      await expect(scopeNote).toBeVisible()
      await expect(
        page
          .locator('main aside')
          .getByText('ingest-worker', { exact: true })
          .first(),
      ).toBeVisible()
      await expectFullText(scopeNote)
      await expectFullText(page.locator('main ul button span.font-mono'))
      if (viewport.width >= 768) {
        await expect(page.locator('.react-flow__node').first()).toBeVisible()
        await expectFullText(page.locator('.react-flow__node [title]'))
        const button = page.locator('.react-flow__controls-fitview')
        const legend = page
          .locator('.react-flow__panel')
          .filter({ hasText: 'Approximate' })
        const a = await button.boundingBox(),
          b = await legend.boundingBox()
        expect(a).not.toBeNull()
        expect(b).not.toBeNull()
        if (a && b)
          expect(
            a.x + a.width <= b.x ||
              b.x + b.width <= a.x ||
              a.y + a.height <= b.y ||
              b.y + b.height <= a.y,
            'graph legend leaves Fit View uncovered',
          ).toBe(true)
      }
    })
  }
}
