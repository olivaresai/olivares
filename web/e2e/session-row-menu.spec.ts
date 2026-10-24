// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SESSION ROW'S ACTIONS MENU (#2795), in a real browser against a live `--seed-demo`
// engine: each action from a right-click, from the row's `⋯` and from the keyboard, and
// each one still true after a reload, because the engine keeps it. Nothing is intercepted.
//
// ⛔ IT CARRIES THE DEMO GUARD of the other live specs (`session-work-surface.spec.ts`
//    says why), and it launches a session of its own through the fixture profile that
//    `scripts/lib/session-conversation-fixture.sh` registers, so it deletes nothing
//    another spec reads. It runs after the other session specs in `scripts/web-e2e-demo.sh` for the same reason.
import { expect, test, type Page } from '@playwright/test'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { sessionToken } from './live-session'

const demoTenant = process.env.DEMO_TENANT ?? ''
const EMAIL = 'demo@olivares.local'
const PASSWORD = 'olivares-demo-estate'
const CAPTURES = join('playwright-report', 'session-row-menu')

async function signIn(page: Page) {
  await page.goto('/login')
  await expect(page.locator('input[type=password]')).toHaveCount(1)
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASSWORD)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), {
    timeout: 20_000,
  })
}

/** The engine's session routes, as the signed-in person. */
async function sessionsApi(page: Page) {
  const token = await sessionToken(page)
  const headers = {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': demoTenant,
  }
  return async (method: string, path: string, data?: unknown) => {
    const res = await page.request.fetch(`/v1/m/sessions/${path}`, {
      method,
      headers,
      data,
    })
    expect(res.ok(), `${method} ${path} -> ${res.status()}`).toBeTruthy()
    return res.json()
  }
}

/** Each launched session's managed live row, once the engine proved one. */
const liveRefs = new Map<string, string>()

/** A session of this spec's own, on the fixture profile and folder. */
async function launch(page: Page, name: string): Promise<string> {
  const api = await sessionsApi(page)
  const profiles = await api('GET', 'provider-profiles')
  const profile = profiles.items.find(
    (p: { display_name?: string }) => p.display_name === 'Fixture Claude',
  )
  const workspaces = await api('GET', 'workspaces')
  const workspace = workspaces.items.find(
    (w: { name?: string }) => w.name === 'Conversation fixture',
  )
  expect(
    profile && workspace,
    'the conversation fixture is seeded',
  ).toBeTruthy()
  const run = await api('POST', 'runs', {
    name,
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    provider_profile_ref: profile.profile_ref,
    workspace_ref: workspace.workspace_ref,
  })
  const runRef = run.run_ref as string
  // The console keys a launched session's row by the managed live row the engine
  // proves for it, and by the run until then (provenance.ts): learn that row.
  await expect
    .poll(async () => {
      const got = await api('GET', `runs/${runRef}`)
      if (got.live_ref) liveRefs.set(runRef, got.live_ref as string)
      return !!got.live_ref || ['failed', 'stopped'].includes(got.state)
    })
    .toBe(true)
  return runRef
}

/** A row's selector by every address the row can have, `extra` added to each. */
const rowSelector = (runRef: string, extra = '') =>
  [
    `run:${runRef}`,
    ...(liveRefs.has(runRef) ? [`live:${liveRefs.get(runRef)}`] : []),
  ]
    .map((a) => `[data-testid="rail-row"][data-address="${a}"]${extra}`)
    .join(', ')

const rowOf = (page: Page, runRef: string) => page.locator(rowSelector(runRef))

/** The link Copy link writes: the Sessions screen at the row's own address. */
const linkOf = async (page: Page, runRef: string) =>
  `${new URL(page.url()).origin}/sessions?session=${encodeURIComponent(
    (await rowOf(page, runRef).getAttribute('data-address')) ?? '',
  )}`

/** The three ways to a row's menu: a right-click, the row's ⋯, the keyboard. */
type Opener = 'right-click' | 'more' | 'keyboard'

async function openRowMenu(page: Page, runRef: string, how: Opener) {
  const row = rowOf(page, runRef)
  if (how === 'right-click') await row.click({ button: 'right' })
  if (how === 'more') {
    // The ⋯ is the row's sibling, never inside the option (work-rail.tsx).
    await row.hover()
    await page
      .locator(rowSelector(runRef, ' ~ [data-testid="rail-row-menu"]'))
      .click()
  }
  if (how === 'keyboard') {
    await row.focus()
    await page.keyboard.press('Shift+F10')
  }
  await expect(page.getByRole('menu')).toBeVisible()
}

/** Chooses an item as the opener's person would: by pointer, or by typing its start. */
async function choose(
  page: Page,
  how: Opener,
  name: string | RegExp,
  typed: string,
) {
  if (how === 'keyboard') {
    // The menu a key opened holds focus, or the keys typed next go somewhere else.
    await expect
      .poll(() =>
        page.evaluate(() => {
          const at = document.activeElement
          return at?.closest('[role="menu"]')
            ? 'menu'
            : `${at?.tagName}:${at?.getAttribute('data-address') ?? at?.getAttribute('aria-label') ?? ''}`
        }),
      )
      .toBe('menu')
    await page.keyboard.type(typed)
    // The typeahead moves focus a moment after the last letter: Enter goes to the item
    // it reached, as a person's would.
    await expect(page.getByRole('menuitem', { name })).toBeFocused()
    await page.keyboard.press('Enter')
  } else {
    await page.getByRole('menuitem', { name }).click()
  }
}

async function capture(page: Page, name: string) {
  mkdirSync(CAPTURES, { recursive: true })
  await page.screenshot({ path: join(CAPTURES, `${name}.png`) })
}

test.describe('a session row has one actions menu', () => {
  test.setTimeout(180_000)

  // The sessions this spec launched, removed after each test whatever it left them in,
  // so a retry starts from the estate the first attempt found.
  const launched: string[] = []

  test.beforeEach(() => {
    test.skip(
      !demoTenant,
      'DEMO_TENANT not set — run this through scripts/web-e2e-demo.sh',
    )
  })

  test.afterEach(async ({ page }) => {
    const api = await sessionsApi(page)
    for (const ref of launched.splice(0)) {
      // A test that got as far as Delete left nothing to remove.
      const runs = await api('GET', 'runs')
      const run = runs.items.find((r: { run_ref: string }) => r.run_ref === ref)
      if (!run) continue
      if (run.pinned) await api('DELETE', `runs/${ref}/pin`)
      if (['running', 'idle', 'pending'].includes(run.state)) {
        await api('POST', `runs/${ref}/stop`)
        await expect
          .poll(async () => (await api('GET', `runs/${ref}`)).state)
          .toBe('stopped')
      }
      if (run.state !== 'cleaned') await api('POST', `runs/${ref}/cleanup`)
      await api('DELETE', `runs/${ref}`)
    }
  })

  test('rename, pin, archive and delete from a right-click, the ⋯ and the keyboard, and each survives a reload', async ({
    page,
    context,
  }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.setViewportSize({ width: 1280, height: 800 })
    await signIn(page)
    const runRef = await launch(page, 'row-menu journey')
    launched.push(runRef)
    await page.goto('/sessions')
    const row = rowOf(page, runRef)
    await expect(row).toBeVisible()
    const clipboard = () => page.evaluate(() => navigator.clipboard.readText())

    // RIGHT-CLICK -> Rename. The browser's own menu is not what opens.
    await row.click({ button: 'right' })
    await expect(page.getByRole('menu')).toBeVisible()
    await capture(page, 'journey-right-click')
    await page.getByRole('menuitem', { name: 'Rename…' }).click()
    const field = page
      .getByRole('dialog')
      .getByRole('textbox', { name: 'Name' })
    await expect(field).toBeFocused()
    await field.fill('release prep')
    await field.press('Enter')
    await expect(page.getByRole('dialog')).toBeHidden()
    await expect(rowOf(page, runRef)).toContainText('release prep')
    await expect(rowOf(page, runRef)).toBeFocused()
    await page.reload()
    await expect(rowOf(page, runRef)).toContainText('release prep')

    // THE ⋯ -> Copy link, then Pin. Pinned sessions are in their own group, first.
    // The `⋯` is the row's sibling, never inside the option (work-rail.tsx).
    const more = () =>
      page.locator(rowSelector(runRef, ' ~ [data-testid="rail-row-menu"]'))
    await rowOf(page, runRef).hover()
    const link = await linkOf(page, runRef)
    await more().click()
    await page.getByRole('menuitem', { name: 'Copy link' }).click()
    await expect.poll(clipboard).toBe(link)
    await rowOf(page, runRef).hover()
    await more().click()
    const pinned = page.getByRole('group', { name: /^Pinned/ })
    await Promise.all([
      page.waitForResponse(
        (r) => r.url().endsWith(`/runs/${runRef}/pin`) && r.ok(),
      ),
      page.getByRole('menuitem', { name: /^Pin/ }).click(),
    ])
    await expect(pinned.locator(rowSelector(runRef))).toBeVisible()
    await page.reload()
    await expect(pinned.locator(rowSelector(runRef))).toBeVisible()

    // THE KEYBOARD: Shift+F10 on the focused row, the menu walked by its own typeahead
    // -> Copy session id; then the menu key -> Archive. Focus comes back to the row.
    await rowOf(page, runRef).focus()
    await page.keyboard.press('Shift+F10')
    await expect(page.getByRole('menu')).toBeVisible()
    await choose(page, 'keyboard', 'Copy session id', 'Copy s')
    await expect.poll(clipboard).toBe(runRef)
    await expect(rowOf(page, runRef)).toBeFocused()
    await page.keyboard.press('ContextMenu')
    await expect(page.getByRole('menu')).toBeVisible()
    await Promise.all([
      page.waitForResponse(
        (r) => r.url().endsWith(`/runs/${runRef}/archive`) && r.ok(),
      ),
      choose(page, 'keyboard', 'Archive', 'Arch'),
    ])
    await expect(rowOf(page, runRef)).toHaveCount(0)
    await page.reload()
    await expect(rowOf(page, runRef)).toHaveCount(0)
    await page.getByRole('button', { name: /^Archived \d+$/ }).click()
    await expect(rowOf(page, runRef)).toBeVisible()
    await rowOf(page, runRef).click({ button: 'right' })
    await Promise.all([
      page.waitForResponse(
        (r) => r.url().endsWith(`/runs/${runRef}/archive`) && r.ok(),
      ),
      page.getByRole('menuitem', { name: 'Unarchive' }).click(),
    ])
    await page.reload()
    await expect(rowOf(page, runRef)).toBeVisible()

    // DELETE is refused while the session runs, and says what to do.
    await rowOf(page, runRef).click({ button: 'right' })
    const del = page.getByRole('menuitem', { name: /Delete/ })
    await expect(del).toHaveAttribute('aria-disabled', 'true')
    await expect(del).toContainText('Stop it first')
    await page.keyboard.press('Escape')
    const api = await sessionsApi(page)
    await api('POST', `runs/${runRef}/stop`)
    await expect
      .poll(async () => (await api('GET', `runs/${runRef}`)).state)
      .toBe('stopped')
    await page.reload()
    await rowOf(page, runRef).click({ button: 'right' })
    await page.getByRole('menuitem', { name: 'Delete…' }).click()
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Delete' })
      .click()
    await expect(page.getByRole('dialog')).toBeHidden()
    await expect(rowOf(page, runRef)).toHaveCount(0)
    await page.reload()
    await expect(rowOf(page, runRef)).toHaveCount(0)
  })

  test('every action from every opener: a right-click, the ⋯ and the keyboard, each surviving a reload', async ({
    page,
    context,
  }) => {
    test.setTimeout(360_000)
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await page.setViewportSize({ width: 1280, height: 800 })
    await signIn(page)
    const openers: Opener[] = ['right-click', 'more', 'keyboard']
    const refs: string[] = []
    for (const how of openers) {
      const ref = await launch(page, `row-menu ${how}`)
      launched.push(ref)
      refs.push(ref)
    }
    const clipboard = () => page.evaluate(() => navigator.clipboard.readText())
    const answered = (ref: string, tail: string) =>
      page.waitForResponse(
        (r) => r.url().endsWith(`/runs/${ref}/${tail}`) && r.ok(),
      )
    const pinned = page.getByRole('group', { name: /^Pinned/ })
    await page.goto('/sessions')

    for (const [i, how] of openers.entries()) {
      const ref = refs[i]
      await expect(rowOf(page, ref)).toBeVisible()

      await openRowMenu(page, ref, how)
      await choose(page, how, 'Rename…', 'Ren')
      const field = page
        .getByRole('dialog')
        .getByRole('textbox', { name: 'Name' })
      await expect(field).toBeFocused()
      await field.fill(`renamed by ${how}`)
      await field.press('Enter')
      await expect(page.getByRole('dialog')).toBeHidden()
      await expect(rowOf(page, ref)).toContainText(`renamed by ${how}`)

      await openRowMenu(page, ref, how)
      await Promise.all([
        answered(ref, 'pin'),
        choose(page, how, /^Pin/, 'Pin'),
      ])
      await expect(pinned.locator(rowSelector(ref))).toBeVisible()
      // The pin is the engine's: it is still there after a reload.
      await page.reload()
      await expect(pinned.locator(rowSelector(ref))).toBeVisible()
      await openRowMenu(page, ref, how)
      await Promise.all([
        answered(ref, 'pin'),
        choose(page, how, /^Unpin/, 'Unp'),
      ])
      await expect(pinned.locator(rowSelector(ref))).toHaveCount(0)

      const link = await linkOf(page, ref)
      await openRowMenu(page, ref, how)
      await choose(page, how, 'Copy link', 'Copy l')
      await expect.poll(clipboard).toBe(link)
      await openRowMenu(page, ref, how)
      await choose(page, how, 'Copy session id', 'Copy s')
      await expect.poll(clipboard).toBe(ref)

      await openRowMenu(page, ref, how)
      await Promise.all([
        answered(ref, 'archive'),
        choose(page, how, 'Archive', 'Arch'),
      ])
      // Archived folds like Snoozed, and a fold the person opened stays open: the row is
      // under Archived now, and out of the list once the page starts folded again.
      await expect(
        page.getByRole('button', { name: /^Archived \d+$/ }),
      ).toBeVisible()
      await page.reload()
      await expect(rowOf(page, ref)).toHaveCount(0)
      await page.getByRole('button', { name: /^Archived \d+$/ }).click()
      await openRowMenu(page, ref, how)
      await Promise.all([
        answered(ref, 'archive'),
        choose(page, how, 'Unarchive', 'Unar'),
      ])
      // With nothing archived left the list is the default one again.
      await expect(
        page.getByRole('button', { name: /^Archived \d+$/ }),
      ).toHaveCount(0)
      await expect(rowOf(page, ref)).toBeVisible()
    }

    await page.reload()
    for (const [i, how] of openers.entries()) {
      await expect(rowOf(page, refs[i])).toContainText(`renamed by ${how}`)
      await expect(pinned.locator(rowSelector(refs[i]))).toHaveCount(0)
    }

    const api = await sessionsApi(page)
    for (const ref of refs) {
      await api('POST', `runs/${ref}/stop`)
      await expect
        .poll(async () => (await api('GET', `runs/${ref}`)).state)
        .toBe('stopped')
    }
    await page.reload()
    for (const [i, how] of openers.entries()) {
      await openRowMenu(page, refs[i], how)
      await choose(page, how, 'Delete…', 'Del')
      await page
        .getByRole('dialog')
        .getByRole('button', { name: 'Delete' })
        .click()
      await expect(page.getByRole('dialog')).toBeHidden()
      await expect(rowOf(page, refs[i])).toHaveCount(0)
    }
    await page.reload()
    for (const ref of refs) await expect(rowOf(page, ref)).toHaveCount(0)
  })

  test('captures: the open menu at 1280 and 390 px, light and dark', async ({
    page,
  }) => {
    await signIn(page)
    const runRef = await launch(page, 'row-menu capture')
    launched.push(runRef)
    for (const width of [1280, 390])
      for (const colorScheme of ['light', 'dark'] as const) {
        await page.setViewportSize({ width, height: width > 400 ? 800 : 844 })
        await page.emulateMedia({ colorScheme })
        await page.goto('/sessions')
        await expect
          .poll(() =>
            page
              .locator('html')
              .evaluate((el) => el.classList.contains('dark')),
          )
          .toBe(colorScheme === 'dark')
        const row = rowOf(page, runRef)
        await expect(row).toBeVisible()
        await row.click({ button: 'right' })
        await expect(page.getByRole('menu')).toBeVisible()
        await capture(page, `menu-${width}-${colorScheme}`)
        await page.keyboard.press('Escape')
      }
  })
})
