// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE KEYBOARD OBJECT ACTIONS, measured in a real browser against a live `--seed-demo`
// engine: the keyboard-only walk that approves the flagged request (`j` into the
// queue, Enter opens the row, `a` approves it — zero pointer events anywhere in this
// spec) and opens a session from the palette's numbered shortcut, plus axe on the
// palette open state.
//
// ⛔ IT CARRIES THE DEMO GUARD the other live specs carry, for their measured
//    reason: `scripts/web-e2e.sh` puts every spec that does not fill `#token` on
//    ONE shared engine that nothing sets up, so a spec signing in as
//    `demo@olivares.local` finds no password box, signs nobody in, and then grades
//    the LOGIN PAGE while reporting the walk green. The `DEMO_TENANT` guard turns
//    that silent pass into a visible skip, and `scripts/web-e2e-demo.sh` is where
//    this spec actually runs.
import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Browser, type Page } from '@playwright/test'
import { browserSessionHeaders, type BrowserSession } from './browser-session'

test.use({ trace: 'off' })

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
// Separation of duty: the person who decides cannot be the one who asked, and the seed
// holds nothing pending, so a second person files the request.
const REQUESTER_EMAIL = 'keyboard-requester@olivares.local'
const REQUESTER_PASSWORD = 'keyboard-requester-passphrase'

/** Keyboard-only sign-in: Enter submits the form; no pointer anywhere in this spec. */
async function signIn(
  page: Page,
  email = EMAIL,
  password = PASSWORD,
): Promise<BrowserSession> {
  await page.goto('/login')
  // Fail closed: no password box means this is not the seeded engine, and every
  // assertion after this point would grade the wrong page.
  await expect(
    page.locator('input[type=password]'),
    'the login form must be present — without it nothing below measures a walk',
  ).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(email)
  await page.locator('input[type=password]').first().fill(password)
  const login = page.waitForResponse(
    (response) =>
      response.url().endsWith('/v1/auth/login') &&
      response.request().method() === 'POST',
  )
  await page.keyboard.press('Enter')
  const response = await login
  expect(
    response.status(),
    'demo sign-in must succeed before the keyboard walk',
  ).toBe(200)
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
  return (await response.json()) as BrowserSession
}

/** A real pending request, filed by another person, so the demo member may decide it. */
async function fileRequestFromAnotherPerson(
  page: Page,
  browser: Browser,
  session: BrowserSession,
) {
  const requester = await page.request.post('/v1/users', {
    headers: browserSessionHeaders(session, demoTenant),
    data: {
      email: REQUESTER_EMAIL,
      display_name: 'Keyboard requester',
      password: REQUESTER_PASSWORD,
      superadmin: true,
    },
  })
  expect(requester.status(), await requester.text()).toBe(201)
  const context = await browser.newContext()
  try {
    const other = await context.newPage()
    const otherSession = await signIn(
      other,
      REQUESTER_EMAIL,
      REQUESTER_PASSWORD,
    )
    const created = await other.request.post('/v1/m/governance/approvals', {
      headers: browserSessionHeaders(otherSession, demoTenant),
      data: {
        action: 'network.connect',
        subject_kind: 'network_destination',
        subject_ref: 'registry.terraform.io',
        reason: 'The provider upgrade reaches the Terraform registry.',
      },
    })
    expect(created.status(), await created.text()).toBe(201)
  } finally {
    await context.close()
  }
}

test.describe('keyboard object actions', () => {
  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test('approves the flagged request and opens a session without a pointer', async ({
    page,
    browser,
  }) => {
    const session = await signIn(page)
    await fileRequestFromAnotherPerson(page, browser, session)
    await page.goto('/permissions?tab=approvals')

    const flagged = page.locator('[data-approval-row][data-flagged="true"]')
    await expect(
      flagged,
      'the request the other person filed must be flagged for the demo member',
    ).toHaveCount(1)

    // The first `j` outside a list enters the queue at the open row: the flagged one.
    await page.keyboard.press('j')
    await expect(flagged).toBeFocused()
    await page.keyboard.press('Enter')
    await expect(flagged).toHaveAttribute('aria-current', 'true')

    // `a` approves the open request at once; the engine records the one decision.
    const decided = page.waitForResponse(
      (response) =>
        /\/approvals\/[^/]+\/decisions$/.test(
          new URL(response.url()).pathname,
        ) && response.request().method() === 'POST',
    )
    await page.keyboard.press('a')
    expect((await decided).ok(), 'the approval must be recorded').toBe(true)

    // …and the palette's numbered shortcut opens a session, still no pointer.
    await page.goto('/sessions')
    await expect(page.getByTestId('work-rail')).toBeVisible()
    await page.keyboard.press('ControlOrMeta+k')
    const palette = page.getByRole('dialog')
    await expect(palette.getByRole('combobox')).toBeVisible()
    await palette.getByRole('combobox').press('Tab')
    await expect(palette.getByRole('listbox')).toBeFocused()
    await page.keyboard.press('1')
    await expect(palette).toBeHidden()
    await expect(page).toHaveURL(
      (url) => url.searchParams.get('session') != null,
    )
  })

  test('axe 0 on the palette open state', async ({ page }) => {
    await signIn(page)
    await page.goto('/sessions')
    await expect(page.getByTestId('work-rail')).toBeVisible()
    await page.keyboard.press('ControlOrMeta+k')
    const palette = page.getByRole('dialog')
    await expect(palette.getByRole('combobox')).toBeVisible()
    const axe = await new AxeBuilder({ page })
      .include('[role="dialog"]')
      .analyze()
    expect(axe.violations).toEqual([])
  })

  test('walks the latency chart by keyboard: arrows move the cursor, Enter filters and restores', async ({
    page,
  }) => {
    await signIn(page)
    await page.goto('/voice')
    const bars = page.locator('[data-slot="category-bars"]')
    // The seeded estate carries no voice sessions, so the latency chart renders
    // its empty state and there is no bar to walk. That is a visible skip, not a
    // silent pass: the walk itself is pinned end to end by the unit tests, and
    // this test exercises it the moment the demo estate grows voice data.
    if ((await bars.count()) === 0) {
      test.skip(
        true,
        'demo estate has no voice sessions — the latency chart is empty',
      )
      return
    }
    const surface = bars.locator('svg[tabindex]').first()
    const before = await bars.locator('.recharts-bar-rectangle').count()
    expect(
      before,
      'the seeded chart must have more than one bar',
    ).toBeGreaterThan(1)
    // recharts' accessibility layer owns the walk: focusing the SVG surface
    // places the band cursor on bar 0 (no pointer), and the arrows move it.
    await surface.focus()
    await expect(
      bars.locator('[data-cursor-index="0"]'),
      'focusing the chart must place the cursor on bar 0',
    ).toBeVisible()
    await page.keyboard.press('ArrowRight')
    await expect(
      bars.locator('[data-cursor-index="1"]'),
      'ArrowRight must move the cursor to bar 1',
    ).toBeVisible()
    // The tooltip follows the cursor — reachable without a pointer.
    await expect(bars.locator('.recharts-tooltip-wrapper')).toBeVisible()

    // Enter on the focused bar filters the chart (and its equivalent table) to
    // that one session…
    await page.keyboard.press('Enter')
    await expect
      .poll(() => bars.locator('.recharts-bar-rectangle').count(), {
        timeout: 5_000,
      })
      .toBe(1)
    // …and Enter on the same bar restores the whole set. ArrowLeft first keeps
    // the cursor on the lone bar whatever index recharts kept across the
    // re-render, then Enter reads that bar's category.
    await page.keyboard.press('ArrowLeft')
    await page.keyboard.press('Enter')
    await expect
      .poll(() => bars.locator('.recharts-bar-rectangle').count(), {
        timeout: 5_000,
      })
      .toBe(before)
  })
})
