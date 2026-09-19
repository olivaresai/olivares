// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE OWNER'S REVIEW SET. Nine console surfaces, photographed against a live
// `--seed-demo` engine at four framings, so a reader can judge the console the way an
// operator meets it: at a desk, on a phone, and on a tablet.
//
// It is DELIBERATELY NOT `docs-captures.spec.ts`. That spec photographs ROUTES — one
// path, one picture, at one desk-sized framing — and it is the public documentation's
// source. This one photographs the surfaces this cycle built, several of which are
// STATES rather than routes (a palette that is open, a field that holds focus, a filter
// that matches nothing) and none of which the route list reaches:
//
//   · a palette is a state of the shell, not a path;
//   · a visible focus ring exists only after a real Tab in a real browser;
//   · an empty state that a filter produced is the only honest one — an unseeded estate
//     would photograph a missing fixture and call it a design;
//   · a phone framing is where a three-pane surface has to prove it folds.
//
// ⛔ THE DEMO GUARD, for the reason every live spec in this directory carries it.
//    `scripts/web-e2e.sh` puts every spec that does not fill `#token` on ONE shared
//    engine that nothing seeds, so a spec signing in as `demo@olivares.local` finds no
//    password box, signs nobody in, and then grades the LOGIN PAGE while reporting each
//    surface green. `DEMO_TENANT` turns that silent pass into a visible skip.
//
// ⛔ EVERY TEST IS TAGGED `@review`, and that is load-bearing rather than tidy. This
//    spec runs on the estate `scripts/docs-captures.sh` seeds — five governed sessions,
//    a provider profile, work items, connectors — so it is invoked THROUGH that script
//    with `--grep @review`, which selects these tests and none of the 142 route takes.
//    Re-seeding a second estate beside it would photograph a poorer console and call the
//    difference a finding.
//
// Output: web/playwright-report/review/<nn>-<slug>-<width>-<theme>.png
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

const OUT = resolve(
  process.env.REVIEW_CAPTURE_DIR ?? 'playwright-report/review',
)

/** The four framings the owner asked to see, and what each one is FOR. */
const FRAMINGS = [
  // The desk. Both themes, because a console is read in both and a token that only
  // works in one is a defect the light picture cannot show.
  { width: 1440, height: 900, theme: 'light' as const },
  { width: 1440, height: 900, theme: 'dark' as const },
  // The phone. Where a three-pane surface has to become one pane and a rail has to
  // get out of the way.
  { width: 390, height: 844, theme: 'light' as const },
  // The tablet: below `xl`, so the work surface folds here too, but with a rail.
  { width: 1024, height: 768, theme: 'light' as const },
]

/** A framing's file-name suffix. */
function suffix(f: (typeof FRAMINGS)[number]) {
  return `${f.width}-${f.theme}`
}

/**
 * Set the persisted preferences BEFORE the first document of this origin. The theme
 * store reads `olivares.theme` RAW (no JSON) before first paint, and the language store
 * reads its own key as a RAW language code.
 *
 * ⛔ AND IT USED TO WRITE THAT KEY AS JSON, WHICH PINNED NOTHING. `olivares.lang` is
 *    `detection.lookupLocalStorage` for i18next's browser detector (`lib/i18n/index.ts`),
 *    and that detector stores a bare code: it read `{"state":{"lang":"en"},…}` as an
 *    unsupported language, fell through to `navigator`, and painted the console in
 *    whatever locale the container happens to have. Measured on this box, 2026-09-18:
 *    every screen came back in Spanish with the JSON pin in place, and in English with
 *    `'en'`. A review set whose language depends on the machine cannot be compared with
 *    the set it is supposed to be compared with.
 */
async function prefer(page: Page, theme: 'light' | 'dark', tenant: string) {
  await page.addInitScript(
    ([t, tn]) => {
      try {
        window.localStorage.setItem('olivares.theme', t)
        window.localStorage.setItem('olivares.lang', 'en')
        window.localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: tn }, version: 0 }),
        )
      } catch {
        /* private mode: the run then photographs the defaults, and the report says so */
      }
    },
    [theme, tenant],
  )
}

async function signIn(page: Page) {
  await page.goto('/login')
  await expect(
    page.locator('input[type=password]'),
    'the login form must be present — without it nothing below photographs a console',
  ).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 30_000,
  })
}

/**
 * Take the picture.
 *
 * ⛔ THE POINTER GOES BACK TO THE CORNER FIRST. Playwright leaves the cursor wherever
 *    the last click landed, and a painted `:hover` is a state the reader cannot
 *    reproduce — the same correction `docs-captures.spec.ts` carries.
 *
 * ⛔ AND THE PICTURE IS VIEWPORT-SIZED, NOT FULL-PAGE. The question the owner asked is
 *    "what does an operator SEE", and a full-page capture answers a different one: it
 *    stretches the viewport, so a fold that is real at 844 px disappears, and every
 *    judgement about dead space below the fold becomes meaningless.
 */
async function shoot(page: Page, name: string) {
  await page.mouse.move(0, 0)
  const path = `${OUT}/${name}.png`
  mkdirSync(dirname(path), { recursive: true })
  await page.screenshot({ path })
}

/** The console shell has painted its navigation. */
async function shellReady(page: Page) {
  // ⛔ THE SCOPE LINE IS NOT A SHELL FACT ANY MORE (C1 §3.1.4). This used to wait for
  //    `shell-scope-line`, which was true when the launcher was docked to the bottom of
  //    every authenticated viewport. The launcher is gone from the shell — it cost about
  //    90 px of all 77 routes to serve the ~17 that can start a run — so the two things
  //    that ARE on every authenticated route are the rail and the header, and those are
  //    what "the shell is up" now means.
  await page.getByRole('navigation').first().waitFor({ timeout: 30_000 })
  await page.locator('[data-slot="topbar"]').waitFor({ timeout: 30_000 })
}

/**
 * Wait until the work composer has DECIDED what it is. It renders the scope line and
 * nothing else while the provider-profile plane is still answering — deliberately, so a
 * deployment with no profile never paints a field it is about to remove. A capture taken
 * inside that window photographs a control that is about to vanish.
 *
 * ⛔ AND IT IS ONLY MOUNTED WHERE STARTING WORK IS THE WORK (C1 §3.1.4): home and the
 *    session work surface. Calling this on any other route would wait 30 s for a control
 *    the product no longer puts there, which is a spec asking for a screen that does not
 *    exist — the same defect the pane-switcher correction fixed in the other direction.
 */
async function composerSettled(page: Page) {
  await page.getByTestId('work-composer').waitFor({ timeout: 30_000 })
  await page
    .locator(
      '[data-testid="launcher-input"], [data-testid="launcher-add-provider"], [data-testid="launcher-blocked"]',
    )
    .first()
    .waitFor({ timeout: 30_000 })
}

/** Below `xl` the rail is behind a control; open it so a phone shot shows the shell. */
async function openRailIfFolded(page: Page) {
  const nav = page.getByRole('navigation').first()
  if (await nav.isVisible()) return
  const opener = page
    .getByRole('button', { name: /menu|navigation|sidebar/i })
    .first()
  if ((await opener.count()) > 0) await opener.click()
}

test.describe('console review captures', () => {
  // Signing in, loading a shell, resolving a session and settling a launcher is three
  // application loads in one case on a loaded box. Raising the budget is the honest fix;
  // weakening the waits would photograph a skeleton and call it a screen.
  test.setTimeout(180_000)

  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/docs-captures.sh',
    )
  })

  for (const framing of FRAMINGS) {
    const sfx = suffix(framing)

    test.describe(() => {
      test.use({
        viewport: { width: framing.width, height: framing.height },
        // 2× renders at twice the pixels and downsamples into the slot, which is what
        // keeps the small type in tables and badges legible when the owner zooms. It is
        // the same criterion the documentation set uses.
        deviceScaleFactor: 2,
      })

      test(`@review 01 the sign-in screen — ${sfx}`, async ({ page }) => {
        await prefer(page, framing.theme, demoTenant)
        await page.goto('/login')
        await expect(page.locator('input[type=password]')).toHaveCount(1)
        // The deployment footer only paints once the server answers; without it the
        // picture shows the screen mid-flight rather than the screen.
        await page
          .getByTestId('deployment-identity')
          .waitFor({ timeout: 30_000 })
          .catch(() => {
            /* an older shell has no footer; the rest of the screen is still the screen */
          })
        await shoot(page, `01-login-${sfx}`)
      })

      test(`@review 02 the first screen after signing in — ${sfx}`, async ({
        page,
      }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/')
        await shellReady(page)
        await composerSettled(page)
        // The next-action row and the recent-work list are two separate reads; the
        // heading alone would photograph the page before either answered.
        await page.waitForLoadState('networkidle').catch(() => {})
        await shoot(page, `02-home-${sfx}`)
      })

      test(`@review 03 the sessions list — ${sfx}`, async ({ page }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/sessions')
        await shellReady(page)
        // The TABLE tab: the list instrument, as distinct from the work surface. The
        // screen keeps both and this capture is of the list.
        const tab = page.getByRole('tab', { name: /table/i }).first()
        await tab.waitFor({ timeout: 30_000 })
        await tab.click()
        await page
          .locator('[data-slot="data-table"] table tbody tr')
          .first()
          .waitFor({ timeout: 30_000 })
          .catch(() => {
            /* an estate with no rows photographs its empty state, which is also true */
          })
        await shoot(page, `03-sessions-list-${sfx}`)
      })

      test(`@review 04 a session's work surface — ${sfx}`, async ({ page }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/sessions')
        await shellReady(page)
        await page.getByTestId('work-surface').waitFor({ timeout: 30_000 })
        const row = page.getByTestId('rail-row').first()
        await row.waitFor({ timeout: 30_000 })
        const address = await row.getAttribute('data-address')
        expect(
          address,
          'every rail row carries the address it opens — without one there is no surface to photograph',
        ).toBeTruthy()
        await row.click()
        // ⛔ BELOW `xl` THE SURFACE SHOWS ONE PANE, AND THAT IS THE DESIGN, NOT A DEFECT.
        //    The three panes sit side by side from 1280 px up and fold to one below it
        //    (`work-surface.tsx:134-183`: `hidden xl:block` on the two panes the address is
        //    not pointing at). Measured 2026-09-18: at 390 and at 1024 this wait resolved 63
        //    times to a HIDDEN `session-narrative` and timed out — the spec was asking a
        //    narrow screen to show a wide screen's layout.
        //
        //    So the narrow framings switch panes the way an operator does: the product paints
        //    a pane switcher below `xl` and this presses its narrative button. The capture is
        //    then of the same thing at every width — the pane that proves the surface
        //    RESOLVED a session — reached by each width's own control.
        const narrative = page.getByTestId('session-narrative')
        if (!(await narrative.isVisible())) {
          await page.getByTestId('pane-button-narrative').click()
        }
        await narrative.waitFor({ timeout: 30_000 })
        await page
          .getByTestId('narrative-loading')
          .waitFor({ state: 'detached', timeout: 30_000 })
          .catch(() => {})
        await composerSettled(page)
        // The launcher, OPEN: `/` is the declared chord that hands it focus, and the
        // focus ring is what "open" looks like on a field that is always present.
        const field = page.getByTestId('launcher-input')
        if ((await field.count()) > 0) {
          await page.keyboard.press('/')
          await expect(field).toBeFocused()
        }
        await shoot(page, `04-work-surface-${sfx}`)
      })

      test(`@review 05 the command palette, open — ${sfx}`, async ({
        page,
      }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/')
        await shellReady(page)
        await composerSettled(page)
        // `Mod+k` is the declared row (`lib/keybindings/table.ts`), resolved by the
        // shell's one keyboard authority. Pressing the real chord is the point: a
        // store call would photograph a dialog the keyboard cannot open.
        await page.keyboard.press('ControlOrMeta+k')
        const dialog = page.getByRole('dialog').first()
        await dialog.waitFor({ timeout: 30_000 })
        // Type enough to make it a RESULT list rather than an empty box: the federated
        // search needs two characters before it fires.
        await page.keyboard.type('sess', { delay: 30 })
        await page.waitForTimeout(900)
        await shoot(page, `05-command-palette-${sfx}`)
      })

      test(`@review 06 a keyboard focus ring on a primary control — ${sfx}`, async ({
        page,
      }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/')
        await shellReady(page)
        await composerSettled(page)
        // ARRIVING FROM THE ADDRESS BAR is exactly a fresh document with focus on the
        // body: the first Tab reaches the skip link, the second the first real control.
        // Nothing is clicked, so `:focus-visible` is the keyboard ring and not a
        // pointer's.
        await page.keyboard.press('Tab')
        const first = await page.evaluate(() => ({
          tag: document.activeElement?.tagName ?? '',
          text: (document.activeElement?.textContent ?? '').slice(0, 60),
        }))
        expect(
          first.tag,
          'a Tab from a fresh document must land on a control, not on the body',
        ).not.toBe('BODY')
        await shoot(page, `06-focus-ring-${sfx}`)
      })

      test(`@review 07 an empty state a filter produced — ${sfx}`, async ({
        page,
      }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/sessions')
        await shellReady(page)
        const tab = page.getByRole('tab', { name: /table/i }).first()
        await tab.waitFor({ timeout: 30_000 })
        await tab.click()
        const search = page
          .locator('[data-slot="data-table"] input[placeholder]')
          .first()
        await search.waitFor({ timeout: 30_000 })
        // A string no reference can contain. The table distinguishes "no data" from
        // "rows hidden by a filter", and this is the second one — the honest empty
        // state, on an estate that DOES have rows.
        await search.fill('zzzz-no-such-session-zzzz')
        await expect(page.getByRole('status').first()).toBeVisible({
          timeout: 30_000,
        })
        await page.mouse.move(0, 0)
        await shoot(page, `07-empty-state-${sfx}`)
      })

      test(`@review 08 settings — ${sfx}`, async ({ page }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/settings')
        await page.getByRole('heading').first().waitFor({ timeout: 30_000 })
        await openRailIfFolded(page)
        await page.waitForLoadState('networkidle').catch(() => {})
        await shoot(page, `08-settings-${sfx}`)
      })

      test(`@review 09 the provider profiles screen — ${sfx}`, async ({
        page,
      }) => {
        await prefer(page, framing.theme, demoTenant)
        await signIn(page)
        await page.goto('/provider-profiles')
        await shellReady(page)
        await page.getByRole('heading').first().waitFor({ timeout: 30_000 })
        await page.waitForLoadState('networkidle').catch(() => {})
        await shoot(page, `09-provider-profiles-${sfx}`)
      })
    })
  }
})
