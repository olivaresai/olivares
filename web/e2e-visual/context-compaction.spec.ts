// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { test, expect } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { fixtureFor } from './fixtures'

test('native compaction keeps the draft and refreshes the context meter', async ({
  page,
}, info) => {
  const run = {
    run_ref: 'context-fixture',
    name: 'Context controls (fixture)',
    state: 'idle',
    provider_driver: 'claude',
    provider_profile_ref: 'profile-fixture',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    last_event_seq: 0,
    record_io: true,
  }
  let compacted = false
  const inputs: string[] = []
  await page.route('**/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/stream'))
      return route.fulfill({
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    if (path.endsWith('/input')) {
      const body = route.request().postDataJSON()
      const text = JSON.parse(body.line).message.content
      inputs.push(text)
      if (text === '/compact') compacted = true
      return route.fulfill({ json: { accepted: true } })
    }
    let json: unknown
    if (path === '/v1/m/sessions/runs') json = { items: [run], has_more: false }
    else if (path === '/v1/m/sessions/runs/context-fixture') json = run
    else if (path === '/v1/m/sessions/live')
      json = { items: [], has_more: false }
    else if (path.endsWith('/commands'))
      json = {
        tool: 'claude',
        reported: true,
        commands: [
          {
            name: 'compact',
            source: 'built-in',
            kind: 'command',
            description: 'Summarize context',
          },
        ],
      }
    else if (path.endsWith('/activity'))
      json = {
        driver: 'claude',
        transcript: 'read',
        context_tokens: compacted ? 20000 : 170000,
        context_window: 200000,
        context_percent: compacted ? 10 : 85,
        compactions: compacted
          ? [{ trigger: 'manual', pre_tokens: 170000, post_tokens: 20000 }]
          : [],
        agents: [],
        tasks: [],
        files: [],
        todos: [],
        skills: [],
        mcp_servers: [],
      }
    else if (path.endsWith('/messages'))
      json = { messages: [], turns: [], has_more: false }
    else json = fixtureFor(path) ?? { items: [], has_more: false }
    return route.fulfill({ json })
  })
  await page.addInitScript(() => {
    localStorage.setItem('olivares.lang', 'en')
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({
        state: {
          token: 'olvs_demo',
          sessionId: 's1',
          expiresAt: '2030-01-01T00:00:00Z',
        },
        version: 0,
      }),
    )
    localStorage.setItem(
      'olivares.tenant',
      JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
    )
    localStorage.setItem(
      'olivares.preferences',
      JSON.stringify({ state: { theme: 'light' }, version: 0 }),
    )
  })
  await page.goto('/sessions?session=run:context-fixture')
  const composer = page.getByRole('textbox', { name: 'Session turn' })
  await composer.fill('Continue after compaction')
  await page.getByRole('button', { name: 'Context', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Compact now' })).toBeEnabled()
  for (const width of [1280, 390]) {
    for (const theme of ['light', 'dark']) {
      await page.setViewportSize({ width, height: 900 })
      await page.evaluate(
        (value) =>
          document.documentElement.classList.toggle('dark', value === 'dark'),
        theme,
      )
      await page.screenshot({
        path: info.outputPath(`context-${width}-${theme}.png`),
      })
    }
  }
  expect(
    (
      await new AxeBuilder({ page })
        .include('[data-radix-popper-content-wrapper]')
        .analyze()
    ).violations,
  ).toEqual([])
  await page.getByRole('checkbox', { name: 'Compact before send' }).check()
  await page.keyboard.press('Escape')
  await page.getByTestId('composer-send').click()
  await expect
    .poll(() => inputs)
    .toEqual(['/compact', 'Continue after compaction'])
  await expect(composer).toHaveValue('')
  await expect(page.getByTestId('composer-context')).toContainText('10%')
})
