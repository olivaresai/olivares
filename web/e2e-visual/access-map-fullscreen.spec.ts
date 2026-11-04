// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The access map in a real browser: it fills the window on request, the text it draws is
// at least 12 px at every zoom the page chooses (measured on 50 and on 500 nodes, the
// GRAPH_LIMIT), zooming out groups instead of shrinking, and a phone gets the same map
// full screen with pan and pinch. jsdom has no layout, so these are the measurements.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { accessGraph, browserSession, fixtureFor } from './fixtures'

const MIN_TEXT_PX = 12
const GRAPH_LIMIT = 500

/** `n` nodes: one agent per ten, the rest files of five repositories, one edge each. */
function graphOf(n: number) {
  const agents = Math.max(2, Math.round(n / 10))
  const nodes = Array.from({ length: n }, (_, i) =>
    i < agents
      ? { id: `A${i}`, kind: 'agent', ref: `agent-${i}` }
      : {
          id: `R${i}`,
          kind: 'file',
          ref: `/srv/acme/repo-${i % 5}/src/file_${i}.ts`,
        },
  )
  const template = accessGraph.edges[0] as Record<string, unknown>
  const edges = nodes.slice(agents).map((r, i) => ({
    ...template,
    id: `e${i}`,
    origin_id: `A${i % agents}`,
    origin_ref: `agent-${i % agents}`,
    resource_id: r.id,
    resource_kind: r.kind,
    resource_ref: r.ref,
    permitted: i % 7 !== 0,
  }))
  return { nodes, edges, truncated: false }
}

async function mockApi(page: Page, theme: string, graph: unknown) {
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p === '/v1/auth/browser-session')
      return route.fulfill({ json: browserSession })
    if (p.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    if (p.endsWith('/accessmap/graph')) return route.fulfill({ json: graph })
    return route.fulfill({
      json: fixtureFor(p) ?? { items: [], has_more: false },
    })
  })
  await page.addInitScript(
    ({ theme }) => {
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', theme)
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
    },
    { theme },
  )
}

/** The smallest text React Flow paints, in screen pixels: font size x the viewport zoom. */
async function smallestRenderedText(page: Page) {
  return page.evaluate(() => {
    const viewport = document.querySelector('.react-flow__viewport')
    if (!viewport) return null
    const zoom = new DOMMatrixReadOnly(getComputedStyle(viewport).transform).a
    let smallest = Infinity
    let sample = ''
    const walker = document.createTreeWalker(viewport, NodeFilter.SHOW_TEXT)
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (!n.textContent?.trim() || !n.parentElement) continue
      const size = parseFloat(getComputedStyle(n.parentElement).fontSize) * zoom
      if (size < smallest) {
        smallest = size
        sample = n.textContent.trim()
      }
    }
    return { zoom, smallest, sample }
  })
}

for (const count of [50, GRAPH_LIMIT]) {
  test(`text is at least ${MIN_TEXT_PX} px on a ${count}-node map, grouped and opened`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1280, height: 800 })
    await mockApi(page, 'light', graphOf(count))
    await page.goto('/access-map')
    const nodes = page.locator('.react-flow__node')
    await expect(nodes.first()).toBeVisible()
    await expect(page.getByText(/click one to open it/i)).toBeVisible()
    await page.waitForTimeout(600) // the fit animates for 200 ms
    expect(await nodes.count()).toBeLessThanOrEqual(32)
    const grouped = await smallestRenderedText(page)
    testInfo.annotations.push({
      type: 'measured-grouped',
      description: JSON.stringify({ nodes: await nodes.count(), ...grouped }),
    })
    expect(grouped?.smallest).toBeGreaterThanOrEqual(MIN_TEXT_PX)
    await page.screenshot({ path: testInfo.outputPath(`map-${count}.png`) })

    // A folder opens in place: its files, at most twelve and a "+N more".
    await page
      .locator('.react-flow__node[data-id^="group:dir:"]')
      .first()
      .click()
    await expect(page).toHaveURL(/[?&]group=/)
    await page.waitForTimeout(600)
    expect(await nodes.count()).toBeLessThanOrEqual(32)
    const opened = await smallestRenderedText(page)
    testInfo.annotations.push({
      type: 'measured-opened',
      description: JSON.stringify({ nodes: await nodes.count(), ...opened }),
    })
    expect(opened?.smallest).toBeGreaterThanOrEqual(MIN_TEXT_PX)
    await page.screenshot({
      path: testInfo.outputPath(`map-${count}-opened.png`),
    })
  })
}

test('Full screen fills the window; F and Esc leave; the address holds it', async ({
  page,
}, testInfo) => {
  await page.setViewportSize({ width: 1280, height: 800 })
  await mockApi(page, 'light', graphOf(30))
  await page.goto('/access-map')
  await page.getByRole('button', { name: /^full screen$/i }).click()
  await expect(page).toHaveURL(/[?&]full=1/)
  const exit = page.getByRole('button', { name: /exit full screen/i })
  await expect(exit).toBeFocused()
  const box = await exit.evaluate((el) => {
    const w = el.closest('.fixed')!.getBoundingClientRect()
    return { x: w.x, y: w.y, w: w.width, h: w.height }
  })
  expect(box).toEqual({ x: 0, y: 0, w: 1280, h: 800 })
  await expect(
    page.getByRole('button', { name: /permitted vs observed/i }),
  ).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('full-1280.png') })
  await page.locator('.react-flow__node').first().click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('full-detail-1280.png') })
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page).toHaveURL(/[?&]full=1/)

  // A reload lands on the same view.
  await page.reload()
  await expect(
    page.getByRole('button', { name: /exit full screen/i }),
  ).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page).not.toHaveURL(/full=1/)
  await expect(
    page.getByRole('button', { name: /^full screen$/i }),
  ).toBeFocused()
  await page.keyboard.press('f')
  await expect(page).toHaveURL(/[?&]full=1/)
  await page.keyboard.press('f')
  await expect(page).not.toHaveURL(/full=1/)
})

test('at 390 px the map opens full screen and pans and pinches', async ({
  browser,
}, testInfo) => {
  const context = await browser.newContext({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
  })
  const page = await context.newPage()
  try {
    await mockApi(page, 'light', graphOf(30))
    await page.goto('/access-map')
    await page.screenshot({ path: testInfo.outputPath('narrow-card-390.png') })
    await page
      .getByRole('button', { name: /open the full-screen map/i })
      .click()
    await expect(page.locator('.react-flow__node').first()).toBeVisible()
    await expect(page.getByText(/click one to open it/i)).toBeVisible()
    await expect(page.locator('.react-flow__minimap')).toBeHidden()
    await page.waitForTimeout(600)
    const exitBox = await page
      .getByRole('button', { name: /exit full screen/i })
      .boundingBox()
    const legendBox = await page
      .locator('.react-flow__panel.top.left')
      .boundingBox()
    const groupedBox = await page
      .getByText(/click one to open it/i)
      .boundingBox()
    const overlaps = (
      a: { x: number; y: number; width: number; height: number } | null,
      b: { x: number; y: number; width: number; height: number } | null,
    ) =>
      a !== null &&
      b !== null &&
      a.x < b.x + b.width &&
      a.x + a.width > b.x &&
      a.y < b.y + b.height &&
      a.y + a.height > b.y
    expect(overlaps(exitBox, legendBox)).toBe(false)
    expect(overlaps(groupedBox, legendBox)).toBe(false)
    const clipped = await page
      .locator('.react-flow__node')
      .evaluateAll((nodes) =>
        nodes.flatMap((node) => {
          const rect = node.getBoundingClientRect()
          return rect.left < 0 || rect.right > window.innerWidth
            ? [node.getAttribute('data-id')]
            : []
        }),
      )
    expect(clipped).toEqual([])
    const matrix = () =>
      page.evaluate(() => {
        const m = new DOMMatrixReadOnly(
          getComputedStyle(document.querySelector('.react-flow__viewport')!)
            .transform,
        )
        return { zoom: m.a, x: m.e }
      })
    const before = await matrix()
    const cdp = await context.newCDPSession(page)
    // Pinch out at the middle of the map: the zoom must grow.
    await cdp.send('Input.synthesizePinchGesture', {
      x: 195,
      y: 420,
      scaleFactor: 1.8,
      relativeSpeed: 400,
      gestureSourceType: 'touch',
    })
    await page.waitForTimeout(400)
    const pinched = await matrix()
    expect(pinched.zoom).toBeGreaterThan(before.zoom)
    // After zooming in, the old gesture point may sit on a selectable node.
    // Drag an actual empty pane point, as a user does to pan the read-only map.
    const panPoint = await page
      .locator('.react-flow__pane')
      .evaluate((pane) => {
        const box = pane.getBoundingClientRect()
        for (const fy of [0.45, 0.55, 0.65, 0.75]) {
          for (const fx of [0.7, 0.8, 0.6, 0.4]) {
            const x = box.x + box.width * fx
            const y = box.y + box.height * fy
            if (document.elementFromPoint(x, y) === pane) return { x, y }
          }
        }
        throw new Error(
          'No empty map pane point is available for the pan gesture',
        )
      })
    await cdp.send('Input.dispatchTouchEvent', {
      type: 'touchStart',
      touchPoints: [{ ...panPoint, id: 1 }],
    })
    for (const distance of [20, 40, 60, 80, 100, 120]) {
      await cdp.send('Input.dispatchTouchEvent', {
        type: 'touchMove',
        touchPoints: [{ x: panPoint.x - distance, y: panPoint.y, id: 1 }],
      })
      await page.waitForTimeout(16)
    }
    await cdp.send('Input.dispatchTouchEvent', {
      type: 'touchEnd',
      touchPoints: [],
    })
    await expect.poll(async () => (await matrix()).x).not.toBe(pinched.x)
    testInfo.annotations.push({
      type: 'touch-pan',
      description: JSON.stringify({
        before: pinched,
        after: await matrix(),
        target: panPoint,
      }),
    })
    await page.screenshot({ path: testInfo.outputPath('full-390.png') })
  } finally {
    await context.close()
  }
})

for (const theme of ['light', 'dark']) {
  for (const [width, height] of [
    [1280, 800],
    [390, 844],
  ]) {
    for (const full of [false, true]) {
      test(`axe is clean on the map, ${full ? 'full screen' : 'in the page'}, ${width} px, ${theme}`, async ({
        page,
      }, testInfo) => {
        await page.setViewportSize({ width, height })
        await mockApi(page, theme, graphOf(30))
        await page.goto(full ? '/access-map?full=1' : '/access-map')
        // At 390 px the page shows the card, not the graph; full screen shows the graph.
        if (width > 768 || full)
          await expect(page.locator('.react-flow__node').first()).toBeVisible()
        await page.waitForTimeout(800)
        await page.screenshot({
          path: testInfo.outputPath(
            `map-${full ? 'full' : 'page'}-${width}-${theme}.png`,
          ),
        })
        const results = await new AxeBuilder({ page })
          .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa'])
          .analyze()
        const serious = results.violations.filter(
          (v) => v.impact === 'critical' || v.impact === 'serious',
        )
        for (const v of serious)
          console.log(
            `[axe ${width} ${theme} full=${full}] ${v.impact}: ${v.id} :: ${v.nodes
              .map((n) => n.target.join(' '))
              .join(' | ')}`,
          )
        expect(serious.map((v) => v.id)).toEqual([])
      })
    }
  }
}
