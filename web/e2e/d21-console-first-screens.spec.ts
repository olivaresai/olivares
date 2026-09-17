// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// D21 — the first screens, from a keyboard, in both themes and in all seven console
// languages, against a live `--seed-demo` engine. Nothing here is intercepted.
//
// ⛔ IT CARRIES D15's GUARD, AND FOR D15's MEASURED REASON. `scripts/web-e2e.sh` puts
// every spec that does not fill `#token` on ONE shared engine that nothing sets up, so
// a spec signing in as `demo@olivares.local` finds no password box, signs nobody in,
// and then grades the LOGIN PAGE while reporting every route green. The `DEMO_TENANT`
// guard turns that silent pass into a visible skip, and `scripts/web-e2e-demo.sh` is
// where this spec actually runs.
//
// ⛔ WHAT IT ASSERTS THAT A UNIT TEST CANNOT. Three things, and each one was a real
// hole before it existed:
//   · the TAB ORDER of a screen that just grew a second column — jsdom has no layout
//     and no tab order worth the name, and the whole claim of the login redesign is
//     that the statement column costs a keyboard user nothing;
//   · that an empty state PAINTED by a live engine has a description under its title,
//     which is the other half of the static census in `empty-state.census.test.ts`:
//     the census reads source, this reads pixels' DOM;
//   · that seven languages render, because a missing key falls back to the raw key
//     and the only place that is visible is a running console.
import { expect, test, type Page } from '@playwright/test'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'

/** The seven the console ships; `lint:i18n` proves the resources exist for each. */
const LANGUAGES = ['en', 'es', 'de', 'fr', 'ja', 'ru', 'zh'] as const

async function signIn(page: Page) {
  await page.goto('/login')
  // Fail closed: no password box means this is not the seeded engine, and every
  // assertion after this point would grade the wrong page.
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

/** The accessible name of whatever currently has focus. */
function focusedName(page: Page) {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null
    if (!el) return null
    return (
      el.getAttribute('aria-label') ||
      (el.textContent || '').replace(/\s+/g, ' ').trim() ||
      el.tagName.toLowerCase() + ':' + (el.getAttribute('type') ?? '')
    )
  })
}

test.describe('D21 first screens', () => {
  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test('login: the statement column adds no tab stop before the form', async ({
    page,
  }) => {
    // THE CLAIM THE REDESIGN MAKES. The left column is text, not controls, so the
    // path to the password box is exactly what it was: skip link, theme toggle,
    // e-mail, password, submit. A link or a button over there would make the
    // signed-out screen SLOWER from a keyboard than the screen it replaced.
    await page.goto('/login')
    await page.locator('input[type=password]').waitFor()
    await page.keyboard.press('Tab')
    await expect(page.locator('a[href="#main-content"]')).toBeFocused()
    await page.keyboard.press('Tab')
    expect(await focusedName(page)).toBeTruthy()
    await expect(page.locator('button[aria-label]').first()).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(page.locator('input[type=email]')).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(page.locator('input[type=password]')).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(page.locator('button[type=submit]')).toBeFocused()
  })

  test('login: skip-to-content still reaches the card', async ({ page }) => {
    await page.goto('/login')
    await page.locator('a[href="#main-content"]').waitFor()
    await page.keyboard.press('Tab')
    await page.keyboard.press('Enter')
    await expect(page.locator('#main-content')).toBeFocused()
  })

  test('login: the deployment it signs into is named', async ({ page }) => {
    await page.goto('/login')
    const identity = page.getByTestId('deployment-identity')
    await expect(identity).toBeVisible()
    // The address, always. The version only once /v1/server-info answers, so it is
    // not asserted here — an absent version is the documented behaviour, not a gap.
    await expect(identity).toContainText(new URL(page.url()).host)
  })

  for (const theme of ['dark', 'light'] as const) {
    test(`home (${theme}): one h1, the next action, and the work`, async ({
      page,
    }) => {
      await prefer(page, theme, 'en')
      await signIn(page)
      await page.goto('/')
      await page.waitForLoadState('networkidle').catch(() => {})

      // at:gate holds every route to exactly one h1; the statement blocks this lane
      // added are text and section headings, never a second page title.
      await expect(page.locator('#main-content h1')).toHaveCount(1)

      const next = page.getByTestId('home-next-step')
      await expect(next).toBeVisible()
      // The demo operator is a superadmin, so all three verbs are offered.
      for (const id of ['provider', 'agent', 'session']) {
        const card = page.getByTestId(`home-next-step-${id}`)
        await expect(card).toBeVisible()
        await expect(card).toHaveAttribute('href', /\/.+/)
        // A link with no accessible name is a link a screen reader cannot offer.
        await expect(card).not.toHaveText('')
      }

      // The work narrative: the seeded estate has sessions, so rows, not the empty
      // state. Which one appears is data, so accept either and require one of them.
      const rows = page.getByTestId('home-recent-rows')
      await expect(rows).toBeVisible()
      await expect(page.getByTestId('home-recent-all')).toHaveAttribute(
        'href',
        '/sessions',
      )
    })
  }

  test('home: the theme changes colours, not structure', async ({ page }) => {
    // The cheapest guard against a "dark-only" or "light-only" layout: the same
    // landmark, heading and control inventory under both themes. Two throwaway
    // contexts, because the theme is read from localStorage BEFORE the first paint
    // and a mid-session toggle would measure a re-render, not a fresh load.
    //
    // ⛔ THE ORIGIN IS TAKEN FROM A PAGE THAT HAS NAVIGATED. `page.url()` on a fresh
    //    fixture is `about:blank`, and handing that to `baseURL` fails with
    //    "Cannot navigate to invalid URL" — measured on the first run of this spec.
    await page.goto('/login')
    const origin = new URL(page.url()).origin

    const shape = async (theme: 'dark' | 'light') => {
      const ctx = await page
        .context()
        .browser()!
        .newContext({ colorScheme: theme, baseURL: origin })
      await ctx.addInitScript(
        (t) => window.localStorage.setItem('olivares.theme', t),
        theme,
      )
      const p = await ctx.newPage()
      await p.goto('/login')
      await p.locator('input[type=email]').fill(EMAIL)
      await p.locator('input[type=password]').fill(PASSWORD)
      await p.locator('button[type=submit]').click()
      await p.waitForURL((u) => !u.pathname.startsWith('/login'))
      await p.goto('/')
      await p.waitForLoadState('networkidle').catch(() => {})
      await p.getByTestId('home-next-step').waitFor()
      const counts = await p.evaluate(() => {
        const root = document.getElementById('main-content')!
        return {
          h1: root.querySelectorAll('h1').length,
          h2: root.querySelectorAll('h2').length,
          links: root.querySelectorAll('a[href]').length,
          buttons: root.querySelectorAll('button').length,
          status: root.querySelectorAll('[role="status"]').length,
        }
      })
      await ctx.close()
      return counts
    }
    expect(await shape('dark')).toEqual(await shape('light'))
  })

  test('every empty state a live engine paints says what the surface will show', async ({
    page,
  }) => {
    // The live half of the census. `empty-state.census.test.ts` reads the SOURCE and
    // proves no call site is bare; this walks screens that actually render one and
    // proves the description reached the DOM — a t() key that resolves to the empty
    // string would pass the first and fail here.
    await signIn(page)
    const ROUTES = [
      '/',
      '/models',
      '/evals',
      '/reporting',
      '/orchestration',
      '/knowledge',
      '/compliance',
      '/security',
      '/console',
    ]
    const bare: string[] = []
    for (const route of ROUTES) {
      await page.goto(route)
      await page.waitForLoadState('networkidle').catch(() => {})
      const found = await page.evaluate(() => {
        const out: { title: string; hasDescription: boolean }[] = []
        for (const el of document.querySelectorAll(
          '[data-slot="empty-state"]',
        )) {
          const ps = el.querySelectorAll('p')
          out.push({
            title: (ps[0]?.textContent ?? '').trim().slice(0, 60),
            // Title, then description: two paragraphs, the second non-empty.
            hasDescription: (ps[1]?.textContent ?? '').trim().length > 0,
          })
        }
        return out
      })
      for (const f of found) {
        if (!f.hasDescription) bare.push(`${route} :: ${f.title}`)
      }
    }
    expect(bare, 'empty states painted with no description').toEqual([])
  })

  for (const lang of LANGUAGES) {
    test(`the first screens render in ${lang}, with no raw keys`, async ({
      page,
    }) => {
      await prefer(page, 'dark', lang)
      await page.goto('/login')
      await page.locator('input[type=password]').waitFor()
      await expect(page.locator('html')).toHaveAttribute('lang', lang)
      const rawOnLogin = await page.evaluate(rawKeyProbe)
      expect(rawOnLogin, `raw i18n keys on /login in ${lang}`).toEqual([])

      await signIn(page)
      await page.goto('/')
      await page.waitForLoadState('networkidle').catch(() => {})
      const rawOnHome = await page.evaluate(rawKeyProbe)
      expect(rawOnHome, `raw i18n keys on / in ${lang}`).toEqual([])
    })
  }
})

/**
 * Text nodes that look like an unresolved i18n key (`next.provider.title`) rather
 * than a sentence. Deliberately narrow: it wants dotted lowerCamel segments with no
 * spaces, which is what i18next echoes back when a key is missing, and which real
 * copy in seven languages never looks like. Identifiers a screen legitimately shows —
 * a host, a file name, a model reference — carry a slash, a digit or a capital, or
 * live inside `<code>`/`<a>`, so they are excluded rather than whitelisted one by one.
 */
function rawKeyProbe(): string[] {
  const bad: string[] = []
  // ⛔ THE WHOLE BODY, NOT `#main-content`. The first version of this probe walked
  //    the main landmark, which on /login is the CARD — so the statement column this
  //    lane added, the theme toggle and the deployment footer were all outside what
  //    it measured, and a missing `auth:shell.*` key would have rendered its raw key
  //    in seven languages under a green test. Found by re-reading my own instrument,
  //    which is the failure mode D15 names: a check that looks like coverage and
  //    measures less than it claims.
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  const KEYISH = /^[a-z][a-zA-Z]*(\.[a-z][a-zA-Z]*){1,4}$/
  let node: Node | null
  while ((node = walker.nextNode())) {
    const text = (node.textContent ?? '').trim()
    if (!KEYISH.test(text)) continue
    const parent = node.parentElement
    if (!parent) continue
    if (parent.closest('code, pre, a[href], [data-allow-dotted]')) continue
    bad.push(text)
  }
  return bad
}
