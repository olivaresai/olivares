// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE OWNER'S REVIEW SET: the console 0.7 capture list. Twenty-six screens, each a route or a state of
// one (a pill that is open, a sheet, a menu), photographed against live engines at 1280 px light (L),
// 1280 px dark (D) and, where the layout changes, 390 px light (M). The owner judges the console on
// these, so nothing here hides what a person would see: no version mark is masked, no date frozen.
//
// It is NOT `docs-captures.spec.ts`, which photographs routes for the public documentation. Run it
// through that harness, which seeds the estate and boots the fresh installs this set needs:
//
//   OLIVARES_BIN=<candidate> scripts/docs-captures.sh e2e/console-review-captures.spec.ts --grep @review
//
// Engines: the seeded Northwind estate (PLAYWRIGHT_BASE_URL, DEMO_TENANT); a fresh install with no AI
// tool signed in (FRESH_BASE_URL); a fresh install whose demo tools are signed in and that has run
// nothing (READY_BASE_URL); a fresh install whose only sessions ended their turn (IDLE_BASE_URL); an
// engine still waiting for first-boot setup (SETUP_BASE_URL).
// Output: web/playwright-report/review/<id>-<slug>[-<part>]-<width>-<theme>.png, and captures.txt
// beside them: one line per picture with the sha256 of the executable the harness runs, which every
// engine of the set runs.
import { expect, test, type Page } from '@playwright/test'
import { appendFileSync, mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { openWorkingFold } from './open-working-fold'

const demoTenant = process.env.DEMO_TENANT ?? ''
const estateOrigin = process.env.PLAYWRIGHT_BASE_URL ?? ''
const binarySha = process.env.DOCS_CAPTURE_BINARY_SHA256 ?? ''
const ENGINES = {
  estate: '',
  fresh: process.env.FRESH_BASE_URL ?? '',
  ready: process.env.READY_BASE_URL ?? '',
  idle: process.env.IDLE_BASE_URL ?? '',
  setup: process.env.SETUP_BASE_URL ?? '',
}
type Engine = keyof typeof ENGINES
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const OUT = resolve(
  process.env.REVIEW_CAPTURE_DIR ?? 'playwright-report/review',
)

const SIZES = {
  L: { width: 1280, height: 800, theme: 'light' },
  D: { width: 1280, height: 800, theme: 'dark' },
  M: { width: 390, height: 844, theme: 'light' },
} as const
type Size = keyof typeof SIZES

/** One review screen. `shot(part)` takes a picture; a screen with two states takes two parts. */
type Ctx = {
  page: Page
  size: Size
  go: (engine: Engine, path: string) => Promise<void>
  shot: (part?: string) => Promise<void>
}
type Screen = {
  id: string
  sizes: string
  slug: string
  engines: Engine[]
  run: (c: Ctx) => Promise<void>
}

/**
 * Language and theme before the first document of every origin. The estate's organization is pinned on
 * the estate's origin only: the fresh installs have one organization of their own.
 */
async function prefer(page: Page, theme: string) {
  await page.addInitScript(
    ([t, tn, origin]) => {
      window.localStorage.setItem('olivares.theme', t)
      // The language detector reads a bare code (lib/i18n/index.ts).
      window.localStorage.setItem('olivares.lang', 'en')
      if (tn && new URL(origin).origin === window.location.origin)
        window.localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: tn }, version: 0 }),
        )
    },
    [theme, demoTenant, estateOrigin],
  )
}

async function signIn(page: Page, base: string) {
  await page.goto(`${base}/login`)
  await expect(page.locator('input[type=password]')).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 30_000,
  })
}

const heading = (page: Page, name: string | RegExp) =>
  page.getByRole('heading', { name, level: 1 }).first()

/** At 390 px the sidebar is a drawer behind the phone bar's More. */
async function openNavigation(c: Ctx) {
  if (c.size === 'M') {
    await c.page
      .locator('[data-slot="phone-navigation"]')
      .getByRole('button', { name: 'More', exact: true })
      .click()
    return c.page.locator('[data-slot="navigation-drawer"]')
  }
  return c.page.locator('[data-slot="sidebar"]')
}

/** The new-session start, with the estate's sessions listed beside it. */
async function openStart(c: Ctx) {
  await c.go('estate', '/sessions')
  const start = c.page.getByTestId('work-start')
  if (!(await start.isVisible())) {
    await c.page
      .locator('#work-pane-rail')
      .getByRole('button', { name: 'New session' })
      .click()
  }
  await start.waitFor({ timeout: 30_000 })
  return start
}

async function openSession(c: Ctx, name: string) {
  await c.go('estate', '/sessions')
  await openWorkingFold(c.page)
  const row = c.page.getByTestId('rail-row').filter({ hasText: name }).first()
  const address = await row.getAttribute('data-address', { timeout: 30_000 })
  expect(address, `the rail row of "${name}" carries its address`).toBeTruthy()
  await c.go('estate', `/sessions?session=${address}`)
  await c.page.getByTestId('session-narrative').waitFor({ timeout: 30_000 })
  return address
}

async function openPage(c: Ctx, path: string, name: string | RegExp) {
  await c.go('estate', path)
  await heading(c.page, name).waitFor({ timeout: 30_000 })
}

const SCREENS: Screen[] = [
  {
    id: 'C01',
    sizes: 'LDM',
    slug: 'home-fresh-no-tool',
    engines: ['fresh'],
    run: async (c) => {
      await c.go('fresh', '/')
      await c.page
        .getByTestId('work-start')
        .getByTestId('tool-claude')
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C02',
    sizes: 'LD',
    slug: 'home-fresh-tool-ready',
    engines: ['ready'],
    run: async (c) => {
      await c.go('ready', '/')
      await c.page.getByTestId('work-start').waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C03',
    sizes: 'LDM',
    slug: 'sessions',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/sessions')
      // The strip is drawn once per layout; the visible one is the screen's.
      await c.page
        .getByTestId('sessions-strip')
        .filter({ visible: true })
        .first()
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C04',
    sizes: 'LDM',
    slug: 'session-changed-files',
    engines: ['estate'],
    run: async (c) => {
      const address = await openSession(c, 'Refactor cart totals in checkout')
      if (c.size !== 'M') return c.shot()
      await c.shot('conversation')
      await c.go('estate', `/sessions?session=${address}&pane=context`)
      await c.page
        .getByRole('dialog', { name: 'Session details' })
        .waitFor({ timeout: 30_000 })
      await c.shot('details')
      // What the Changes tab holds, whichever tab the sheet opened on: the review needs both.
      await c.page.getByRole('radio', { name: 'Changes' }).click()
      await c.shot('changes')
    },
  },
  {
    id: 'C05',
    sizes: 'LDM',
    slug: 'composer-tool-pill',
    engines: ['estate'],
    run: async (c) => {
      await openStart(c)
      await c.page.getByRole('button', { name: /^Tool and account: / }).click()
      // The open menu hides the rest of the page from the accessibility tree, the pill included.
      await c.page.getByRole('menu').first().waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C06',
    sizes: 'L',
    slug: 'more-options',
    engines: ['estate'],
    run: async (c) => {
      const start = await openStart(c)
      await start
        .getByRole('textbox')
        .first()
        .fill('Fix the flaky checkout test')
      await c.page.getByRole('button', { name: /^Folder: / }).click()
      await c.page.getByRole('menuitem', { name: 'More options' }).click()
      await c.page
        .getByRole('dialog', { name: /^New (.+ )?session$/ })
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C07',
    sizes: 'LDM',
    slug: 'sidebar-more',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/sessions')
      const nav = await openNavigation(c)
      // Every place is listed, the administrative ones too: there is no More to open.
      await expect(nav.locator('a[data-place="operations"]')).toBeVisible()
      await c.shot()
    },
  },
  {
    id: 'C08',
    sizes: 'LD',
    slug: 'bell',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/sessions')
      await c.page.getByRole('button', { name: /^Recent activity/ }).click()
      await c.page
        .getByRole('heading', { name: 'Needs you' })
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C09',
    sizes: 'LD',
    slug: 'overview',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/now', 'Overview')
      await c.shot()
    },
  },
  {
    id: 'C10',
    sizes: 'LD',
    slug: 'templates',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/workspace-templates')
      await c.page
        .getByRole('button', { name: 'New template' })
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C11',
    sizes: 'L',
    slug: 'new-template',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/workspace-templates')
      await c.page.getByRole('button', { name: 'New template' }).click()
      await c.page
        .getByRole('dialog', { name: 'New template' })
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C12',
    sizes: 'LD',
    slug: 'ai-tools',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/agent-tools', 'AI tools')
      await c.shot()
    },
  },
  {
    id: 'C13',
    sizes: 'L',
    slug: 'models-more',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/models')
      const row = c.page.locator('[data-slot="destination-sections"]').first()
      await row.getByRole('button', { name: 'More' }).click()
      await c.page.getByRole('menu').first().waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C14',
    sizes: 'LD',
    slug: 'extensions',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/capabilities', 'Extensions')
      await c.shot()
    },
  },
  {
    id: 'C15',
    sizes: 'LD',
    slug: 'permissions',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/permissions', 'Permissions')
      await c.shot()
    },
  },
  {
    id: 'C16',
    sizes: 'LD',
    slug: 'usage',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/usage', 'Usage')
      await c.shot('accounts')
      await openPage(c, '/usage?tab=timeline', 'Usage')
      await c.shot('timeline')
    },
  },
  {
    id: 'C17',
    sizes: 'LD',
    slug: 'audit',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/audit', 'Audit ledger')
      await c.shot()
    },
  },
  {
    id: 'C18',
    sizes: 'LDM',
    slug: 'access-map',
    engines: ['estate'],
    run: async (c) => {
      await c.go('estate', '/access-map')
      // The list's search, at every size: at 390 px "Show as a graph" moves into the page menu.
      await c.page
        .getByPlaceholder(/^Search origin/)
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C19',
    sizes: 'LD',
    slug: 'identity',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/identity', 'Identity')
      await c.shot()
    },
  },
  {
    id: 'C20',
    sizes: 'L',
    slug: 'security-operations',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/security', 'Security')
      await c.shot('security')
      await openPage(c, '/operations', /./)
      await c.shot('operations')
    },
  },
  {
    id: 'C21',
    sizes: 'LD',
    slug: 'settings',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/settings?section=edition', /./)
      await c.shot('edition')
      await c.go('estate', '/settings?section=modules')
      await c.page
        .locator('[data-slot="modules-settings"]')
        .waitFor({ timeout: 30_000 })
      await c.shot('modules')
    },
  },
  {
    id: 'C22',
    sizes: 'LM',
    slug: 'login-setup',
    engines: ['fresh', 'setup'],
    run: async (c) => {
      await c.page.goto(`${ENGINES.fresh}/login`)
      await expect(c.page.locator('input[type=password]')).toHaveCount(1)
      await c.shot('login')
      await c.page.goto(`${ENGINES.setup}/setup`)
      expect(
        new URL(c.page.url()).pathname,
        'the setup engine still waits for first-boot setup',
      ).toBe('/setup')
      await heading(c.page, 'First-boot setup').waitFor({ timeout: 30_000 })
      await c.shot('setup')
    },
  },
  {
    id: 'C23',
    sizes: 'L',
    slug: 'deploy-community',
    engines: ['estate'],
    run: async (c) => {
      await openPage(c, '/deploy', 'Part of Business')
      await c.shot()
    },
  },
  {
    id: 'C24',
    sizes: 'L',
    slug: 'health-module-off',
    engines: ['fresh'],
    run: async (c) => {
      await c.go('fresh', '/health')
      await c.page
        .locator('[data-slot="module-not-enabled"]')
        .waitFor({ timeout: 30_000 })
      await c.shot()
    },
  },
  {
    id: 'C25',
    sizes: 'L',
    slug: 'status-bar',
    engines: ['idle', 'estate'],
    run: async (c) => {
      // Idle first (the fresh install's two sessions ended their turn), then the estate, where sessions work.
      await c.go('idle', '/sessions')
      await c.page
        .locator('[data-slot="status-bar"]')
        .waitFor({ timeout: 30_000 })
      await c.shot('idle')
      await c.go('estate', '/sessions')
      await c.page
        .getByRole('button', { name: 'Stop all' })
        .waitFor({ timeout: 30_000 })
      await c.shot('working')
    },
  },
  {
    id: 'C26',
    sizes: 'L',
    slug: 'workspace-switcher',
    engines: ['estate', 'ready'],
    run: async (c) => {
      // Two workspaces in the estate (Default and Billing), then the fresh install's one. On Overview: the
      // Sessions page folds the sidebar to icons, and the switcher with it.
      await c.go('estate', '/now')
      await c.page
        .locator('[data-slot="scope-switcher"]')
        .waitFor({ timeout: 30_000 })
      await c.shot('two')
      await c.go('ready', '/now')
      await c.page.locator('[data-slot="sidebar"]').waitFor({ timeout: 30_000 })
      await c.shot('one')
    },
  },
]

test.describe('console review captures', () => {
  // Up to two engines, a sign-in each and two states per case on a loaded host.
  test.setTimeout(180_000)

  for (const screen of SCREENS) {
    for (const size of screen.sizes.split('') as Size[]) {
      const f = SIZES[size]
      test.describe(() => {
        test.use({
          viewport: { width: f.width, height: f.height },
          deviceScaleFactor: 2,
        })

        test(`@review ${screen.id} ${screen.slug} — ${f.width}-${f.theme}`, async ({
          page,
        }) => {
          test.skip(
            !demoTenant,
            'DEMO_TENANT not set — run this through scripts/docs-captures.sh',
          )
          for (const e of screen.engines)
            expect(
              e === 'estate' || ENGINES[e],
              `${e} engine: run this through scripts/docs-captures.sh`,
            ).toBeTruthy()
          expect(
            binarySha,
            'every capture is listed with the engine it came from',
          ).toMatch(/^[0-9a-f]{64}$/)
          await prefer(page, f.theme)
          const signed = new Set<Engine>()
          await screen.run({
            page,
            size,
            go: async (engine, path) => {
              const base = ENGINES[engine]
              if (!signed.has(engine)) {
                await signIn(page, base)
                signed.add(engine)
              }
              await page.goto(`${base}${path}`)
              await page
                .locator('[data-slot="topbar"]')
                .waitFor({ timeout: 30_000 })
              // Bounded: the console holds live streams open, so the network may never go idle.
              await page
                .waitForLoadState('networkidle', { timeout: 5_000 })
                .catch(() => {
                  /* a live stream keeps the network busy; the waits above decided the screen */
                })
            },
            shot: async (part) => {
              await page.mouse.move(0, 0)
              const name = [
                screen.id,
                screen.slug,
                part,
                `${f.width}-${f.theme}`,
              ]
                .filter(Boolean)
                .join('-')
              mkdirSync(OUT, { recursive: true })
              await page.screenshot({ path: `${OUT}/${name}.png` })
              appendFileSync(
                `${OUT}/captures.txt`,
                `${name}.png ${binarySha}\n`,
              )
            },
          })
        })
      })
    }
  }
})
