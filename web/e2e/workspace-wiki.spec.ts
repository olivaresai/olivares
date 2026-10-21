// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { sessionToken } from './live-session'
import { signInFixture } from './fixture-sign-in'

// A disposable engine with the existing zero-network conversation driver. These
// captures qualify the console and the real wiki/files/session APIs, not model answers.
process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

test('workspace wiki: create, browse, ingest, ask and lint in the real console', async ({
  page,
}, testInfo) => {
  const folder = process.env.E2E_WIKI_FOLDER
  test.skip(!folder, 'Requires a disposable engine and E2E_WIKI_FOLDER')
  test.setTimeout(150_000)
  await page.setViewportSize({ width: 1280, height: 900 })
  await signInFixture(
    page,
    { email: 'demo@olivares.local', password: 'olivares-demo-estate' },
    20_000,
  )
  const token = await sessionToken(page)
  const who = await page.request.get('/v1/auth/whoami', {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(who.ok()).toBe(true)
  const tenant = (await who.json()).grants[0].tenant as string
  const headers = {
    Authorization: `Bearer ${token}`,
    'X-Olivares-Tenant': tenant,
  }
  const registered = await page.request.post('/v1/m/sessions/workspaces', {
    headers,
    data: {
      name: 'Wiki browser fixture',
      root_path: folder,
      mount_mode: 'rw',
      dlp_mode: 'off',
    },
  })
  expect(registered.ok()).toBe(true)
  const ref = (await registered.json()).workspace_ref as string
  const base = `/v1/m/sessions/workspaces/${encodeURIComponent(ref)}`
  const write = async (path: string, content: string) => {
    const response = await page.request.put(`${base}/files/raw`, {
      headers: { ...headers, 'Content-Type': 'text/plain; charset=utf-8' },
      params: { path },
      data: content,
    })
    expect(response.ok(), `write ${path}: HTTP ${response.status()}`).toBe(true)
  }
  await page.goto('/workspace')
  await page
    .getByRole('button', { name: 'Open wiki for Wiki browser fixture' })
    .click()
  const wiki = page.getByRole('dialog')
  await expect(wiki.getByRole('button', { name: 'Create wiki' })).toBeVisible()
  await expect(
    wiki.getByRole('button', { name: 'Ingest', exact: true }),
  ).toHaveCount(0)
  const init = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === `${base}/wiki` &&
      r.request().method() === 'POST',
  )
  await wiki.getByRole('button', { name: 'Create wiki' }).click()
  expect((await init).ok()).toBe(true)
  await expect(
    wiki.getByRole('button', { name: 'Ingest', exact: true }),
  ).toBeVisible()
  await write('raw/source.md', 'The Earth has one Moon.\n')
  await write(
    'index.md',
    '# Solar system\n\nA small catalog of our sources.\n\n- [Earth](wiki/earth.md) — our planet\n- [Moon](wiki/moon.md) — a natural satellite\n',
  )
  await write(
    'wiki/earth.md',
    '# Earth\n\n**Earth** is our home planet. See the [Moon](moon.md).\n\n[Return to the index](../index.md)\n',
  )
  await write(
    'wiki/moon.md',
    '# Moon\n\nThe Moon is Earth’s natural satellite.\n\n[Earth](earth.md)\n',
  )
  await write(
    'log.md',
    '# Wiki log\n\n' +
      Array.from(
        { length: 12 },
        (_, i) =>
          `## [2026-10-${String(i + 1).padStart(2, '0')}] ingest | Source ${i + 1}\n\nEntry ${i + 1}`,
      ).join('\n\n'),
  )
  await wiki.getByRole('button', { name: 'Refresh', exact: true }).click()
  await wiki.getByRole('link', { name: 'Earth', exact: true }).click()
  await expect(
    wiki.getByText('is our home planet.', { exact: false }),
  ).toBeVisible()
  await wiki.getByRole('link', { name: 'Moon', exact: true }).click()
  await expect(
    wiki.getByText('The Moon is Earth’s natural satellite.'),
  ).toBeVisible()
  await wiki.getByRole('button', { name: 'Index', exact: true }).click()
  await expect(wiki.getByText('Entry 12', { exact: true })).toBeVisible()
  await expect(wiki.getByText('Entry 2', { exact: true })).toHaveCount(0)

  const runRefs: string[] = []
  const start = async (button: string) => {
    const created = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === '/v1/m/sessions/runs' &&
        r.request().method() === 'POST',
    )
    await wiki.getByRole('button', { name: button, exact: true }).click()
    const response = await created
    expect(response.ok(), `launch HTTP ${response.status()}`).toBe(true)
    const run = await response.json()
    expect(run.workspace_ref).toBe(ref)
    runRefs.push(run.run_ref)
    await expect(
      wiki.getByRole('link', { name: 'Open session' }),
    ).toHaveAttribute(
      'href',
      `/sessions?session=run%3A${run.run_ref}&pane=narrative`,
    )
  }
  await wiki.getByRole('button', { name: 'Ingest', exact: true }).click()
  await wiki.getByLabel('Source', { exact: true }).selectOption('raw/source.md')
  await start('Ingest source')
  await wiki.getByRole('button', { name: 'Ingest', exact: true }).click()
  await wiki.getByLabel('Upload source').setInputFiles({
    name: 'another-source.md',
    mimeType: 'text/markdown',
    buffer: Buffer.from('Immutable upload bytes\n'),
  })
  const uploadResponse = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === `${base}/files/raw` &&
      r.request().method() === 'PUT',
  )
  await start('Ingest source')
  const uploaded = await (await uploadResponse).json()
  expect(uploaded.path).toMatch(/^raw\/[\w-]+-another-source\.md$/)
  const read = await page.request.get(`${base}/files/raw`, {
    headers,
    params: { path: uploaded.path },
  })
  expect((await read.json()).content).toBe('Immutable upload bytes\n')
  await wiki.getByRole('button', { name: 'Ask', exact: true }).click()
  await wiki
    .getByRole('textbox', { name: /Question/ })
    .fill('What is known about the Moon?')
  await start('Ask wiki')
  await start('Lint')
  expect(new Set(runRefs).size).toBe(4)
  await wiki.getByRole('button', { name: 'Ask', exact: true }).click()
  await wiki
    .getByRole('textbox', { name: /Question/ })
    .fill('Which sources agree?')
  await expect(page.locator('[data-sonner-toast]')).toHaveCount(0, {
    timeout: 10_000,
  })
  for (const theme of ['light', 'dark']) {
    await page.evaluate((value) => {
      document.documentElement.setAttribute('data-theme', value)
      document.documentElement.classList.toggle('dark', value === 'dark')
    }, theme)
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 })
      await wiki.evaluate((element) => {
        element.scrollTop = 0
      })
      await expect(
        wiki.getByRole('textbox', { name: /Question/ }),
      ).toBeVisible()
      expect(
        await wiki.evaluate(
          (element) => element.scrollWidth <= element.clientWidth,
        ),
      ).toBe(true)
      const axe = await new AxeBuilder({ page })
        .include('[role="dialog"]')
        .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
        .analyze()
      expect(axe.violations).toEqual([])
      await page.screenshot({
        path: testInfo.outputPath(`wiki-${theme}-${width}.png`),
        fullPage: true,
      })
    }
  }
  // A page link and the question box remain usable with the keyboard.
  await wiki.getByRole('textbox', { name: /Question/ }).focus()
  await page.keyboard.press('Tab')
  await expect(wiki.getByRole('button', { name: 'Ask wiki' })).toBeFocused()
})
