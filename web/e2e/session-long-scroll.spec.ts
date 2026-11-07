// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A LONG CONVERSATION HOLDS STILL WHERE IT IS SCROLLED, against a live seeded engine whose
// child is the fixture driver, over the long conversation in the tool's own transcript. The list
// renders only the rows near the screen; at a few positions the browser's scroll anchoring and the
// list's own correction moved the view against each other every frame, and a click there never
// fired. Run through scripts/e2e-session-conversation.sh with
// E2E_MESSAGES_FIXTURE=1 E2E_SPECS=e2e/session-long-scroll.spec.ts.
import { expect, test } from '@playwright/test'
import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { signInFixture } from './fixture-sign-in'

const demoTenant = process.env.DEMO_TENANT ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''
const folder = process.env.E2E_SESSION_FOLDER ?? ''

test('a long conversation holds still at every scroll position near its end', async ({
  page,
}) => {
  test.skip(
    !demoTenant || !runRef,
    'run through scripts/e2e-session-conversation.sh with E2E_MESSAGES_FIXTURE=1',
  )
  test.setTimeout(300_000)
  await page.context().addInitScript(() => {
    window.localStorage.setItem('olivares.theme', 'light')
    window.localStorage.setItem('olivares.lang', 'en')
  })
  if (folder) writeFileSync(join(folder, '.fixture-echo-user'), '')
  await signInFixture(
    page,
    { email: 'demo@olivares.local', password: 'olivares-demo-estate' },
    20_000,
  )
  await page.setViewportSize({ width: 1280, height: 900 })
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative`)
  const turn = page.getByTestId('launcher-input')
  await expect(turn).toBeEnabled({ timeout: 15_000 })
  const replies = page
    .getByTestId('session-conversation')
    .locator('[data-kind="assistant"]', { hasText: 'Two things matter' })
  const before = await replies.count()
  await turn.fill('Read the scroll notes')
  await turn.press('Enter')
  await expect(replies).toHaveCount(before + 1, { timeout: 30_000 })
  await turn.fill(
    Array.from({ length: 20 }, (_, i) => `Note ${i + 1}: keep the view.`).join(
      '\n',
    ),
  )
  await turn.press('Enter')
  await expect(replies).toHaveCount(before + 2, { timeout: 30_000 })
  await page.getByRole('button', { name: 'Collapse navigation' }).click()
  await page.mouse.move(1270, 890)
  await page.waitForTimeout(1000)

  // Every 4 px over the last 2,016 px (the positions that moved recurred about every 504 px):
  // put the view there, let two frames pass, and count the frames of the next twelve in which
  // the view still moves. A view that holds still moves in none of them.
  const moving = await page.evaluate(async () => {
    const frame = () =>
      new Promise((resolve) => requestAnimationFrame(() => resolve(null)))
    const log = () =>
      document.querySelector<HTMLElement>(
        '[data-testid="session-conversation"] [role="log"]',
      )
    const found: string[] = []
    for (let off = 0; off <= 2016; off += 4) {
      const el = log()
      if (!el) return [...found, `the log is gone at ${off} px from the end`]
      el.scrollTop = el.scrollHeight - el.clientHeight - off
      await frame()
      await frame()
      let moves = 0
      let top = el.scrollTop
      for (let i = 0; i < 12; i++) {
        await frame()
        if (el.scrollTop !== top) moves++
        top = el.scrollTop
      }
      if (moves >= 4)
        found.push(`${off} px from the end: moved in ${moves} of 12 frames`)
    }
    return found
  })
  expect(moving, 'scroll positions where the view never settles').toEqual([])
  await expect(page.getByText('This view crashed')).toHaveCount(0)
})
