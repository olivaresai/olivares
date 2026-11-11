// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Controls for openWorkingFold over synthetic DOM, so no engine is booted. The "Working N"
// toggle exists only while a session works: it can vanish between two reads of the helper.
import { expect, test, type Locator, type Page } from '@playwright/test'

import { openWorkingFold } from './open-working-fold'

// Short enough that the old helper's wait for a vanished toggle fails the control quickly.
const PLAZO_CONTROL = 1_500

const RAIL = `<div data-testid="work-rail">
  <button aria-expanded="false" onclick="this.setAttribute('aria-expanded','true')">Working 2</button>
</div>`

const TOGGLE_ROLE = { name: /^Working\s*\d+$/ }

test.beforeEach(async ({ page }) => {
  page.setDefaultTimeout(PLAZO_CONTROL)
  await page.setContent(RAIL)
})

test('opens the Working fold when the toggle is there and shut', async ({
  page,
}) => {
  await openWorkingFold(page)
  await expect(page.getByRole('button', TOGGLE_ROLE)).toHaveAttribute(
    'aria-expanded',
    'true',
  )
})

test('does not fail when the work ends and the toggle goes away after it was read', async ({
  page,
}) => {
  await expect(
    openWorkingFold(removingToggleAfterRead(page)),
  ).resolves.toBeUndefined()
  await expect(page.getByRole('button', TOGGLE_ROLE)).toHaveCount(0)
})

// The same page, except that each read of the toggle (count or attribute) removes it from the
// DOM right after answering: the session finished between two steps of the helper. Everything
// else is the real Playwright locator, so the wait that follows is the real one.
function removingToggleAfterRead(page: Page): Page {
  const drop = () =>
    page.evaluate(() => document.querySelector('button')?.remove())
  const wrap = (locator: Locator): Locator =>
    new Proxy(locator, {
      get(target, key, receiver) {
        if (key === 'first') return () => wrap(target.first())
        if (key === 'count')
          return async () => {
            const n = await target.count()
            await drop()
            return n
          }
        if (key === 'getAttribute')
          return async (...args: Parameters<Locator['getAttribute']>) => {
            const value = await target.getAttribute(...args)
            await drop()
            return value
          }
        return Reflect.get(target, key, receiver)
      },
    })
  return new Proxy(page, {
    get(target, key, receiver) {
      if (key !== 'getByRole') return Reflect.get(target, key, receiver)
      return (...args: Parameters<Page['getByRole']>) =>
        wrap(target.getByRole(...args))
    },
  })
}
