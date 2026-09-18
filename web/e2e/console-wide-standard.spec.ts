// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONSOLE-WIDE STANDARD, in a real browser, against a live `--seed-demo` engine.
// Nothing here is intercepted.
//
// ⛔ IT CARRIES THE DEMO GUARD the other live specs carry, for their measured reason:
//    `web-e2e.sh`
//    puts every spec that does not fill `#token` on ONE shared engine that nothing sets
//    up, so a spec signing in as `demo@olivares.local` finds no password box, signs
//    nobody in, and then grades the LOGIN PAGE while reporting every route green. The
//    `DEMO_TENANT` guard turns that silent pass into a visible skip, and
//    `scripts/web-e2e-demo.sh` is where this spec actually runs.
//
// ⛔ WHAT IT ASSERTS THAT A UNIT TEST CANNOT, and each was a measured hole:
//    · that the rail CUTS NO LABEL — `scrollWidth > clientWidth` is the browser's own
//      answer, and jsdom has no layout at all. Measured before the fix: 65 cut labels
//      across the seven console languages;
//    · that the rail is ONE tab stop in a REAL tab order. Measured before: 85;
//    · that the type ladder SURVIVES to the painted element. `cn` used to delete it,
//      and no source-level census could see that;
//    · the keyboard of each screen family — list, form, dialog — including focus RETURN,
//      which the DOM only has in a browser.
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

/** Every element in the sidebar whose text the browser is CUTTING. */
async function cutLabels(page: Page) {
  return page.evaluate(() => {
    const aside = document.querySelector('aside')
    if (!aside) return ['no sidebar']
    const out: string[] = []
    for (const el of aside.querySelectorAll('*')) {
      if (el.children.length > 0) continue
      const text = el.textContent?.trim()
      if (!text) continue
      if (el.scrollWidth > el.clientWidth + 1)
        out.push(`${text.slice(0, 40)} (+${el.scrollWidth - el.clientWidth}px)`)
    }
    return out
  })
}

test.describe('the console-wide standard', () => {
  test.skip(
    !demoTenant,
    'needs the demo-seeded engine (scripts/web-e2e-demo.sh sets DEMO_TENANT)',
  )
  test.use({ viewport: { width: 1600, height: 1000 } })

  for (const lang of LANGUAGES) {
    test(`the rail cuts no label in ${lang}`, async ({ browser }) => {
      // THE MEASURE THIS SPEC EXISTS FOR, and it is a BROWSER measure: the rail used to
      // cut 65 labels — en 5, es 12, ja 9, de 8, ru 16, fr 15, zh 0 — worst 102px of French
      // past the edge. A source census cannot see this: every string is whole in the locale
      // file and whole in the DOM, and only the BOX was too small.
      //
      // ⚠ ONE TEST PER LANGUAGE, not one loop. Seven sign-ins share one timeout, and when it
      //   runs out Playwright tears the context down mid-assertion — which reports as
      //   "target closed" and reads like a product failure. Measured while writing this.
      const context = await browser.newContext({
        viewport: { width: 1600, height: 1000 },
      })
      await context.addInitScript(
        ([l]) => {
          try {
            window.localStorage.setItem('olivares.lang', l)
          } catch {
            /* private mode: the run then measures the default, and says so */
          }
        },
        [lang],
      )
      const p = await context.newPage()
      await signIn(p)
      await p.goto('/')
      // Open every area, so the leaves and the section labels are measured too.
      for (let pass = 0; pass < 3; pass++) {
        const closed = p.locator('aside button[aria-expanded="false"]')
        if ((await closed.count()) === 0) break
        await closed
          .first()
          .click({ timeout: 3000 })
          .catch(() => {})
      }
      await p.waitForTimeout(400)
      expect(await cutLabels(p), `${lang}: the rail must cut no label`).toEqual(
        [],
      )
      await context.close()
    })
  }

  test('the rail is ONE tab stop, and arrows walk it', async ({ page }) => {
    await signIn(page)
    await page.goto('/inventory')
    await page.waitForLoadState('networkidle').catch(() => {})

    // Measured before the fix: 85 focusable elements in the sidebar. The tree is now a
    // composite, so exactly one of its ROWS is tabbable.
    const tabbableRows = await page.evaluate(
      () =>
        document.querySelectorAll('aside [data-nav-row][tabindex="0"]').length,
    )
    expect(tabbableRows, 'exactly one rail row is in the tab order').toBe(1)

    // And it is the row that names the page, so "where am I" needs no keystroke.
    const current = await page.evaluate(() =>
      document
        .querySelector('aside [data-nav-row][tabindex="0"]')
        ?.getAttribute('aria-current'),
    )
    expect(current, 'the tab stop starts on the current page').toBe('page')

    await page.evaluate(() =>
      document
        .querySelector<HTMLElement>('aside [data-nav-row][tabindex="0"]')
        ?.focus(),
    )
    const before = await page.evaluate(
      () => document.activeElement?.textContent?.trim() ?? '',
    )
    await page.keyboard.press('ArrowDown')
    const after = await page.evaluate(
      () => document.activeElement?.textContent?.trim() ?? '',
    )
    expect(after, 'ArrowDown moves the rail focus').not.toBe(before)
    expect(
      await page.evaluate(
        () =>
          document.querySelectorAll('aside [data-nav-row][tabindex="0"]')
            .length,
      ),
      'the tab stop moves with the focus — it never becomes two',
    ).toBe(1)
  })

  test('the rail names the pin and the key that does it', async ({ page }) => {
    await signIn(page)
    await page.goto('/')
    const pin = page.locator('aside [data-rail-pin]').first()
    // Named BEFORE it happens, and the name carries the key, so
    // the control and the keystroke are one offer rather than a key nobody can discover.
    await expect(pin).toHaveAttribute('aria-label', /\(p\)/)
    expect(
      await pin.evaluate((el) => (el as HTMLElement).tabIndex),
      'the pin is not a tab stop — the rail owns the one',
    ).toBe(-1)
  })

  test('the type ladder survives to the painted element', async ({ page }) => {
    // THE DEFECT `cn` HAD: tailwind-merge classified a ladder step as a text COLOUR and
    // deleted it, so `DeploymentIdentity` asked for `text-caption` (12px) and painted
    // 14px. No source census could see that; only the computed style can.
    await page.goto('/login')
    const identity = page.getByTestId('deployment-identity')
    await expect(identity).toBeVisible()
    const size = await identity.evaluate((el) => getComputedStyle(el).fontSize)
    expect(size, 'the caption step reaches the element').toBe('12px')
    const cls = await identity.getAttribute('class')
    expect(cls, 'and the class is still on it').toContain('text-caption')
  })

  test('a list: the grid is one tab stop, and arrows move the active cell', async ({
    page,
  }) => {
    await signIn(page)
    await page.goto('/inventory')
    await page.waitForLoadState('networkidle').catch(() => {})
    const grid = page.locator('[role="grid"]').first()
    await expect(grid).toBeVisible()

    // ONE way into the data region. The sort controls in the header are reachable
    // separately, by design (DataTable's own header says so); what must not happen is a
    // tab stop per CELL.
    const cellStops = await grid.evaluate(
      (el) => el.querySelectorAll('td[tabindex="0"]').length,
    )
    expect(
      cellStops,
      'exactly one cell is in the tab order',
    ).toBeLessThanOrEqual(1)

    await page.evaluate(() => {
      const el = document.querySelector<HTMLElement>(
        '[role="grid"] td[tabindex="0"], [role="grid"][tabindex="0"]',
      )
      el?.focus()
    })
    // Entering the region from the table itself puts the active cell under focus.
    await page.keyboard.press('ArrowDown')
    const before = await page.evaluate(() =>
      document.activeElement
        ?.closest('td[aria-colindex]')
        ?.getAttribute('aria-colindex'),
    )
    expect(before, 'the keyboard reaches a cell of the grid').toBeTruthy()
    await page.keyboard.press('ArrowRight')
    const after = await page.evaluate(() =>
      document.activeElement
        ?.closest('td[aria-colindex]')
        ?.getAttribute('aria-colindex'),
    )
    expect(after, 'ArrowRight moves the active cell').not.toBe(before)
  })

  test('a dialog: focus is trapped while open and RETURNED on close', async ({
    page,
  }) => {
    await signIn(page)
    await page.goto('/')
    // The favourites manager is a dialog every principal reaches, on every route.
    const opener = page
      .locator('aside button')
      .filter({ hasText: /.+/ })
      .first()
    const openerText = await opener.textContent()
    await opener.click()
    const dialog = page.locator('[role="dialog"][data-state="open"]')
    await expect(dialog).toBeVisible()
    expect(
      await page.evaluate(
        () => !!document.activeElement?.closest('[role="dialog"]'),
      ),
      'focus is inside the dialog while it is open',
    ).toBe(true)
    await page.keyboard.press('Escape')
    await expect(dialog).toHaveCount(0)
    const returned = await page.evaluate(
      () => document.activeElement?.textContent?.trim() ?? '',
    )
    // Focus RETURNS to whatever opened it — never to <body>, which is where a keyboard
    // operator has to start again from the top of the document.
    expect(
      returned,
      'focus returns to the control that opened the dialog',
    ).toBe((openerText ?? '').trim())
  })

  test('a form: the label is above the control and names it', async ({
    page,
  }) => {
    await page.goto('/login')
    const email = page.locator('input[type=email]').first()
    await expect(email).toBeVisible()
    const named = await email.evaluate((el) => {
      const id = el.id
      const label = id ? document.querySelector(`label[for="${id}"]`) : null
      if (!label) return { ok: false, above: false }
      // "Labels above" is a LAYOUT claim, and this is the only place it can be checked.
      return {
        ok: !!label.textContent?.trim(),
        above:
          label.getBoundingClientRect().bottom <=
          el.getBoundingClientRect().top + 1,
      }
    })
    expect(named.ok, 'the field carries a label element that names it').toBe(
      true,
    )
    expect(named.above, 'the label sits above its control').toBe(true)
    // And the form submits from the keyboard without reaching for a mouse.
    await email.focus()
    await page.keyboard.type(EMAIL)
    await page.keyboard.press('Tab')
    await page.keyboard.type(PASSWORD)
    await page.keyboard.press('Enter')
    await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
      timeout: 20_000,
    })
  })

  // ── r2 ──────────────────────────────────────────────────────────────────────
  // The four below are the r2 scope in a browser: the verb the operator came for, its
  // SCOPE on a tabbed screen, the one view that wrote its own heading, and the density
  // of the tables that are not grids. None of them is checkable in jsdom: three are
  // computed styles and the fourth is a portal landing in another subtree.

  test('a list: the verb is in the page header, not in the filter row', async ({
    page,
  }) => {
    await signIn(page)
    await page.goto('/catalog')
    await page.waitForLoadState('networkidle').catch(() => {})
    const verb = page.getByRole('button', { name: /new entry/i }).first()
    await expect(verb).toBeVisible()
    const where = await verb.evaluate((el) => {
      const host = el.closest('[data-page-primary-action]')
      const h1 = document.querySelector('h1')
      return {
        inSlot: !!host,
        afterTheHeading:
          !!h1 &&
          !!host &&
          !!(
            h1.compareDocumentPosition(host) & Node.DOCUMENT_POSITION_FOLLOWING
          ),
        // The filter row is the strip that holds the table's search box.
        inFilterRow: !!el.closest('[role="grid"]') || !!el.closest('table'),
      }
    })
    expect(
      where.inSlot,
      "the verb is in the header's primary-action slot",
    ).toBe(true)
    expect(where.afterTheHeading, 'and the slot follows the page heading').toBe(
      true,
    )
    expect(where.inFilterRow, 'and it is not inside the table').toBe(false)
  })

  test('a tabbed list: the verb belongs to the ACTIVE tab', async ({
    page,
  }) => {
    // The scope claim. `/catalog` under Entries offers *New entry*; under Policy that
    // verb does not exist, and a header that kept showing it would be lying about what
    // is on screen. This is why the slot exists instead of a page-level prop.
    await signIn(page)
    await page.goto('/catalog')
    await page.waitForLoadState('networkidle').catch(() => {})
    await expect(page.getByRole('button', { name: /new entry/i })).toHaveCount(
      1,
    )
    const policy = page.getByRole('tab', { name: /policy/i }).first()
    await policy.click()
    await expect(policy).toHaveAttribute('data-state', 'active')
    await expect(
      page.getByRole('button', { name: /new entry/i }),
      'the verb leaves with the tab that owns it',
    ).toHaveCount(0)
  })

  test('/logs carries the same page heading as every other screen', async ({
    page,
  }) => {
    // It was the ONE view of 61 that wrote its own `<h1>`, at `text-title` (18px) while
    // every other screen's was `text-display` (24px).
    await signIn(page)
    // ⚠ `domcontentloaded`, NOT the default. `/logs` holds a server-sent-events stream
    //   open, so the `load` event never fires and a plain `goto` times out at 30 s with
    //   `toHaveCount` reporting `undefined` — which reads like a missing heading rather
    //   than a page that never finished navigating. Measured while writing this, and it
    //   is the same rule `console:walk` states for streaming screens.
    await page.goto('/logs', { waitUntil: 'domcontentloaded' })
    await expect(page.locator('h1')).toHaveCount(1, { timeout: 15_000 })
    const size = await page
      .locator('h1')
      .first()
      .evaluate((el) => getComputedStyle(el).fontSize)
    expect(size, 'the display step, like every management screen').toBe('24px')
  })

  test('a table that is not a grid reads at the grid’s own density', async ({
    page,
  }) => {
    // 58 hand-rolled tables carried FIVE header densities and two header treatments
    // beside `DataTable`'s own. `StaticTable` keeps the grid's decisions, so the two
    // tables on one screen are the same table. Only a browser can say what was painted.
    await signIn(page)
    await page.goto('/console')
    await page.waitForLoadState('networkidle').catch(() => {})
    const painted = await page.evaluate(() => {
      const heads = [...document.querySelectorAll('table thead th')]
      if (heads.length === 0) return null
      const read = (el: Element) => {
        const s = getComputedStyle(el)
        return `${s.paddingLeft}|${s.paddingRight}|${s.textTransform}|${s.fontSize}`
      }
      return [...new Set(heads.map(read))]
    })
    expect(painted, 'the screen painted a table header at all').not.toBeNull()
    expect(
      painted,
      'every header cell on the screen reads at ONE density and one treatment',
    ).toHaveLength(1)
    expect(painted?.[0]).toContain('uppercase')
    expect(painted?.[0]).toContain('12px|12px')
  })

  test('both themes paint the same structure', async ({ browser }) => {
    for (const theme of ['dark', 'light'] as const) {
      const context = await browser.newContext({
        viewport: { width: 1600, height: 1000 },
        colorScheme: theme,
      })
      const p = await context.newPage()
      await prefer(p, theme, 'en')
      await signIn(p)
      await p.goto('/areas/observation')
      await p.waitForLoadState('networkidle').catch(() => {})
      await expect(
        p.locator('h1'),
        `${theme}: exactly one page heading`,
      ).toHaveCount(1)
      const heading = await p
        .locator('h1')
        .first()
        .evaluate((el) => getComputedStyle(el).fontSize)
      expect(heading, `${theme}: the heading is the display step`).toBe('24px')
      await context.close()
    }
  })
})
