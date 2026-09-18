// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SESSION WORK SURFACE, from a keyboard, in a real browser, against a live
// `--seed-demo` engine. Nothing here is intercepted.
//
// ⛔ IT CARRIES THE DEMO GUARD the other live specs carry, for their measured reason.
//    `scripts/web-e2e.sh`
//    puts every spec that does not fill `#token` on ONE shared engine that nothing sets
//    up, so a spec signing in as `demo@olivares.local` finds no password box, signs
//    nobody in, and then grades the LOGIN PAGE while reporting every route green. The
//    `DEMO_TENANT` guard turns that silent pass into a visible skip, and
//    `scripts/web-e2e-demo.sh` is where this spec actually runs.
//
// ⛔ WHAT IT ASSERTS THAT A UNIT TEST CANNOT, and each was a real hole before it:
//    · a COLD deep link — a browser that has never loaded this console opening a URL
//      that names one session, and the reload/Back/Forward round trip after it. jsdom
//      has no history stack worth the name and no second page load at all;
//    · the three panes side by side at 1280 px and one at a time below it, which is a
//      LAYOUT fact and jsdom has no layout;
//    · that the rail is ONE tab stop in a real tab order, and that `⌘K` hands focus
//      back to the launcher it was opened from — a focus contract the DOM only has in
//      a browser.
import { expect, test, type Page } from '@playwright/test'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

/** The seven the console ships; `lint:i18n` proves the resources exist for each. */
const LANGUAGES = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'] as const

async function signIn(page: Page) {
  await page.goto('/login')
  await expect(
    page.locator('input[type=password]'),
    'the login form must be present — without it nothing below measures a screen',
  ).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

/** Set the two persisted preferences BEFORE the first document of this origin. */
async function prefer(page: Page, theme: 'dark' | 'light', lang: string) {
  await page.context().addInitScript(
    ([t, l]) => {
      try {
        window.localStorage.setItem('olivares.theme', t)
        window.localStorage.setItem('olivares.lang', l)
      } catch {
        /* private mode: the run then measures the defaults, and says so */
      }
    },
    [theme, lang],
  )
}

/** The address of the first row the rail is showing. */
async function firstRowAddress(page: Page): Promise<string> {
  const row = page.getByTestId('rail-row').first()
  await expect(row).toBeVisible()
  const address = await row.getAttribute('data-address')
  expect(address, 'every rail row carries the address it opens').toBeTruthy()
  return address as string
}

/** What the URL says the surface is showing. */
function addressInUrl(page: Page): string | null {
  return new URL(page.url()).searchParams.get('session')
}

/**
 * Wait until the shell launcher has decided what it is. It renders nothing but the
 * scope line while the provider-profile plane is still answering — deliberately, so a
 * deployment with no profile never paints a field it is about to remove — and a test
 * that read `count()` during that window saw a field that then vanished under it. That
 * is exactly how the first run of this spec failed.
 */
async function launcherSettled(page: Page) {
  await page.getByTestId('shell-launcher').waitFor()
  await page
    .locator(
      '[data-testid="launcher-input"], [data-testid="launcher-add-provider"], [data-testid="launcher-blocked"]',
    )
    .first()
    .waitFor()
}

test.describe('the work surface', () => {
  // ⛔ THE DEFAULT 30 s IS NOT ENOUGH FOR THESE, and the first run proved it rather
  //    than suggesting it: signing in, then opening a SECOND browser context that boots
  //    the console from cold, then reloading it, is three full application loads in one
  //    case. Raising the budget is the honest fix; making the assertions weaker would
  //    have hidden exactly what this spec exists to measure.
  test.setTimeout(120_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test('a session is ADDRESSABLE: cold deep link, reload, Back, Forward', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.goto('/sessions')
    const first = await firstRowAddress(page)

    // COLD: a context that has never seen this URL opens it and lands on the session.
    const cold = await page
      .context()
      .browser()!
      .newContext({
        baseURL: new URL(page.url()).origin,
        colorScheme: 'dark',
        // 1600 px ON PURPOSE: below `xl` the rail is behind the pane switcher, and a
        // deep link that names another pane would hide the rows this case goes on to
        // click. The widths themselves are the next test's subject.
        viewport: { width: 1600, height: 1000 },
        storageState: await page.context().storageState(),
      })
    const fresh = await cold.newPage()
    const deepLink = `/sessions?session=${encodeURIComponent(first)}&pane=context&evidence=activity`
    await fresh.goto(deepLink)
    await expect(fresh.getByTestId('session-context')).toBeVisible()
    expect(addressInUrl(fresh)).toBe(first)
    await expect(fresh.getByTestId('pane-button-context')).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    await expect(fresh.getByTestId('evidence-toggle-activity')).toHaveAttribute(
      'aria-expanded',
      'true',
    )

    // RELOAD: the state is in the URL, so it survives a new document.
    await fresh.reload()
    await expect(fresh.getByTestId('session-context')).toBeVisible()
    expect(addressInUrl(fresh)).toBe(first)
    await expect(fresh.getByTestId('pane-button-context')).toHaveAttribute(
      'aria-pressed',
      'true',
    )

    // BACK and FORWARD: a session is a PLACE, so each one is its own history entry.
    const second = await (async () => {
      // NO PANE SWITCH HERE. At 1600 px the switcher is `xl:hidden`, so clicking it
      // waits for an element CSS has hidden — which is how the first two runs of this
      // case spent their whole timeout without saying anything useful.
      const rows = fresh.getByTestId('rail-row')
      await expect(rows.first()).toBeVisible()
      const count = await rows.count()
      expect(
        count,
        'the demo estate has more than one session',
      ).toBeGreaterThan(1)
      const other = rows.nth(1)
      const address = await other.getAttribute('data-address')
      await other.click()
      return address as string
    })()
    await expect(fresh).toHaveURL(new RegExp(encodeURIComponent(second)))

    await fresh.goBack()
    await expect(fresh).toHaveURL(new RegExp(encodeURIComponent(first)))
    await fresh.goForward()
    await expect(fresh).toHaveURL(new RegExp(encodeURIComponent(second)))
    await cold.close()
  })

  test('the front door opens the CARD, not the room', async ({ page }) => {
    // A review named this the single largest gap the front door left. A work row on the
    // home screen now carries the address of the one session it tells.
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.goto('/')
    const row = page.getByTestId('home-recent-row').first()
    await expect(row).toBeVisible()
    const href = await row.getAttribute('href')
    expect(href, 'the row addresses ONE session').toMatch(
      /\/sessions\?session=/,
    )
    await row.click()
    await expect(page.getByTestId('session-narrative')).toBeVisible()
    expect(addressInUrl(page)).toBeTruthy()
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`three panes at 1280 px, one below it (${theme})`, async ({
      page,
    }) => {
      await prefer(page, theme, 'en')
      await signIn(page)
      await page.setViewportSize({ width: 1600, height: 1000 })
      await page.goto('/sessions')
      await expect(page.getByTestId('work-surface')).toBeVisible()

      // THE LAYOUT CLAIM, measured rather than asserted from a class: all three panes
      // are on screen, and they sit side by side rather than stacked.
      const rail = page.locator('#work-pane-rail')
      const narrative = page.locator('#work-pane-narrative')
      const context = page.locator('#work-pane-context')
      for (const pane of [rail, narrative, context])
        await expect(pane).toBeVisible()
      const boxes = await Promise.all(
        [rail, narrative, context].map((p) => p.boundingBox()),
      )
      expect(boxes.every(Boolean)).toBe(true)
      expect(boxes[0]!.x).toBeLessThan(boxes[1]!.x)
      expect(boxes[1]!.x).toBeLessThan(boxes[2]!.x)
      // Same row: a "three-pane" surface that wrapped would be three stacked panes.
      expect(Math.abs(boxes[0]!.y - boxes[2]!.y)).toBeLessThan(4)

      // …and BELOW `xl` exactly one is in front, with the switcher that chooses it.
      await page.setViewportSize({ width: 1024, height: 900 })
      await expect(page.getByTestId('pane-button-rail')).toBeVisible()
      await expect(rail).toBeVisible()
      await expect(narrative).toBeHidden()
      await page.getByTestId('pane-button-narrative').click()
      await expect(narrative).toBeVisible()
      await expect(rail).toBeHidden()
      // The choice is in the address, so it is shareable from a laptop too.
      expect(new URL(page.url()).searchParams.get('pane')).toBe('narrative')
    })
  }

  test('the rail is ONE tab stop, arrows move, Enter opens', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.setViewportSize({ width: 1600, height: 1000 })
    await page.goto('/sessions')
    await expect(page.getByTestId('work-rail')).toBeVisible()

    const rows = page.getByTestId('rail-row')
    await rows.first().focus()
    const before = addressInUrl(page)

    await page.keyboard.press('ArrowDown')
    // MOVING IS NOT CHOOSING: on this screen a navigation is a request, so walking
    // the rail must not fire one per keystroke.
    expect(addressInUrl(page)).toBe(before)
    const focusedAddress = await page.evaluate(() =>
      document.activeElement?.getAttribute('data-address'),
    )
    expect(focusedAddress).toBe(await rows.nth(1).getAttribute('data-address'))

    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(
      new RegExp(encodeURIComponent(focusedAddress as string)),
    )

    // ONE TAB STOP FOR THE WHOLE RAIL — measured on the row the component actually
    // made its tab stop, which is the correction two failed runs taught. The rail is a
    // roving-tabindex listbox: exactly ONE row is tabbable at a time, and it is not
    // necessarily the first — after opening a session it is that session's row.
    // Focusing a DIFFERENT row by DOM and tabbing from it lands on the roving row,
    // still inside the rail, and the assertion then measured a state the test had
    // created rather than the contract.
    const tabbable = page.locator('[data-testid="rail-row"][tabindex="0"]')
    await expect(
      tabbable,
      'exactly one row carries the rail tab stop',
    ).toHaveCount(1)
    await tabbable.focus()
    await page.keyboard.press('Tab')
    const stillInRail = await page.evaluate(
      () => !!document.activeElement?.closest('[data-testid="work-rail"]'),
    )
    expect(stillInRail).toBe(false)
  })

  test('`]` and `[` move between panes, and the URL follows', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.setViewportSize({ width: 1024, height: 900 })
    await page.goto('/sessions')
    await expect(page.getByTestId('pane-button-rail')).toBeVisible()

    await page.keyboard.press(']')
    await expect(page.getByTestId('pane-button-narrative')).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    await page.keyboard.press('[')
    await expect(page.getByTestId('pane-button-rail')).toHaveAttribute(
      'aria-pressed',
      'true',
    )
  })

  test('the shell says what the next action applies to, and offers it honestly', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.goto('/')
    const scope = page.getByTestId('shell-scope-line')
    await expect(scope).toBeVisible()
    await expect(scope).toContainText(demoTenant.slice(0, 8))

    // ⛔ THE WITH-PROVIDER CASE IS NOT ASSERTED HERE, and that is the honest half.
    //    The demo seed registers no provider profile, so what this deployment can show
    //    is the state with none, and that is what is asserted. Starting a session from
    //    the launcher needs a registered provider record and is asserted where one exists.
    const launcher = page.getByTestId('shell-launcher')
    await expect(launcher).toBeVisible()
    await launcherSettled(page)
    const hasField = await page.getByTestId('launcher-input').count()
    if (hasField === 0) {
      // No profile registered: it says so and offers the ONE action. Never a disabled
      // field with no reason.
      await expect(page.getByTestId('launcher-add-provider')).toBeVisible()
      await expect(launcher).toContainText(/provider/i)
    } else {
      // A deployment that DOES have a profile: the field is there and the start is
      // gated on the one thing the server cannot default.
      await expect(page.getByTestId('launcher-profile')).toBeVisible()
      await expect(page.getByTestId('launcher-start')).toBeDisabled()
    }
  })

  test('the palette keeps focus while open and RETURNS it to the launcher', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.goto('/')
    await launcherSettled(page)
    const field = page.getByTestId('launcher-input')
    test.skip(
      (await field.count()) === 0,
      'no provider profile on this deployment, so there is no launcher field to return to',
    )

    await field.focus()
    await page.keyboard.press('ControlOrMeta+k')
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    // KEEPS FOCUS WHILE OPEN: focus is inside the dialog, not left on the page behind.
    const insideDialog = await page.evaluate(
      () => !!document.activeElement?.closest('[role="dialog"]'),
    )
    expect(insideDialog).toBe(true)

    await page.keyboard.press('Escape')
    await expect(dialog).toBeHidden()
    // …AND RETURNS IT. The measured failure this contract exists for is focus landing
    // on <body>, where the next Tab starts the page again from the top.
    await expect(field).toBeFocused()
  })

  test('`/` goes to the launcher from anywhere, and never while typing', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.goto('/sessions')
    await launcherSettled(page)
    const field = page.getByTestId('launcher-input')
    test.skip(
      (await field.count()) === 0,
      'no provider profile on this deployment, so there is no launcher field',
    )
    await page.getByTestId('work-rail').waitFor()
    await page.keyboard.press('/')
    await expect(field).toBeFocused()

    // A `/` typed INTO a field is a slash.
    await page.keyboard.type('a/b')
    await expect(field).toHaveValue('a/b')
  })

  test('the help page prints the table the console resolves', async ({
    page,
  }) => {
    await prefer(page, 'dark', 'en')
    await signIn(page)
    await page.goto('/sessions')
    await page.getByTestId('work-surface').waitFor()
    await page.locator('body').click()
    await page.keyboard.press('?')
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    for (const command of [
      'palette.open',
      'launcher.focus',
      'rail.pin',
      'surface.nextPane',
    ])
      await expect(page.getByTestId(`keybinding-${command}`)).toBeVisible()
    // An invalid rule is reported rather than fatal; the shipped table has none, so
    // the notice must be absent — which is what makes it mean something when it is not.
    await expect(page.getByTestId('keybinding-problems')).toHaveCount(0)
  })

  for (const lang of LANGUAGES) {
    test(`the surface renders in ${lang} with no raw key`, async ({ page }) => {
      // A missing key falls back to the RAW KEY, and the only place that is visible is
      // a running console. The probe walks the whole body, as the first-screens probe does.
      await prefer(page, 'dark', lang)
      await signIn(page)
      await page.setViewportSize({ width: 1600, height: 1000 })
      await page.goto('/sessions')
      await expect(page.getByTestId('work-surface')).toBeVisible()
      const text = (await page.locator('body').innerText()) ?? ''
      const raw = text.match(
        /\b(rail|surface|narrative|evidence|context|launcher|scope|keys)\.[a-zA-Z.]+\b/g,
      )
      expect(raw ?? [], `raw i18n keys on screen in ${lang}`).toEqual([])
    })
  }
})
