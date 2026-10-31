// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { sessionToken } from './live-session'

process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
const PASSWORD = 'correct-horse-battery-staple-42'
const PNG =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='

test('workspace files: rendered previews, confined HTML and URL continuity', async ({
  page,
}, testInfo) => {
  test.skip(!setupToken, 'Run via scripts/web-e2e.sh with a fresh engine')
  test.setTimeout(150_000)
  page.setDefaultTimeout(10_000)
  const folder = mkdtempSync(join(tmpdir(), 'olivares-workspace-preview-'))
  const scriptRequests: string[] = []
  page.on('request', (request) => {
    if (new URL(request.url()).pathname === '/__workspace_html_script_ran')
      scriptRequests.push(request.url())
  })
  let ref = ''
  let headers: Record<string, string> | undefined
  try {
    await page.goto('/setup')
    await page.locator('#token').fill(setupToken)
    await page.locator('#setup-email').fill('admin@example.com')
    await page.locator('#setup-password').fill(PASSWORD)
    await page.getByRole('button', { name: /create administrator/i }).click()
    await page.waitForURL('**/onboarding')

    const token = await sessionToken(page)
    const who = await page.request.get('/v1/auth/whoami', {
      headers: { Authorization: `Bearer ${token}` },
    })
    expect(who.ok()).toBe(true)
    const tenant = (await who.json()).grants[0].tenant as string
    headers = { Authorization: `Bearer ${token}`, 'X-Olivares-Tenant': tenant }
    const registered = await page.request.post('/v1/m/sessions/workspaces', {
      headers,
      data: {
        name: 'File preview fixture',
        root_path: folder,
        mount_mode: 'rw',
        dlp_mode: 'off',
      },
    })
    expect(registered.ok()).toBe(true)
    ref = (await registered.json()).workspace_ref as string
    const base = `/v1/m/sessions/workspaces/${encodeURIComponent(ref)}`
    const docs = await page.request.post(`${base}/files/dir`, {
      headers,
      params: { path: 'docs' },
    })
    expect(docs.ok(), `create docs/: HTTP ${docs.status()}`).toBe(true)
    const write = async (path: string, content: string, binary = false) => {
      const response = await page.request.put(`${base}/files/raw`, {
        headers: {
          ...headers,
          'Content-Type': binary
            ? 'application/octet-stream'
            : 'text/plain; charset=utf-8',
        },
        params: { path },
        data: binary ? Buffer.from(content, 'base64') : content,
      })
      expect(response.ok(), `write ${path}: HTTP ${response.status()}`).toBe(
        true,
      )
    }
    await write('docs/README.md', '# Guide\n\nA **rendered** document.\n')
    await write(
      'page.html',
      '<script>document.documentElement.dataset.scriptRan="true";fetch("/__workspace_html_script_ran")</script><h1>Safe HTML</h1>',
    )
    await write('pixel.png', PNG, true)
    await write(
      'vector.svg',
      '<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><rect width="32" height="32" fill="green"/></svg>',
    )
    await write('retry.md', '# Recovered\n')

    await page.goto('/sessions?tab=workspaces')
    await page
      .getByRole('button', { name: 'Browse files', exact: true })
      .click()
    const dialog = page.getByRole('dialog')
    await dialog.getByText('docs', { exact: true }).click()
    expect(new URL(page.url()).searchParams.get('path')).toBe('docs')
    await dialog.getByText('README.md', { exact: true }).click()
    await expect(
      dialog.getByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeVisible()
    expect(new URL(page.url()).searchParams.get('file')).toBe('docs/README.md')
    await page.reload()
    await expect(
      dialog.getByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeVisible()
    await dialog.getByRole('button', { name: 'Source', exact: true }).click()
    await expect(dialog.locator('.cm-content')).toContainText(
      'A **rendered** document.',
    )
    await dialog.getByRole('button', { name: 'Rendered', exact: true }).click()
    await expect(
      dialog.getByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeVisible()
    await page.goBack()
    await expect(
      dialog.getByText('Select a file to view or edit.'),
    ).toBeVisible()
    expect(new URL(page.url()).searchParams.has('file')).toBe(false)
    await page.goForward()
    await expect(
      dialog.getByRole('heading', { name: 'Guide', level: 2 }),
    ).toBeVisible()

    for (const theme of ['light', 'dark']) {
      await page.evaluate((value) => {
        document.documentElement.setAttribute('data-theme', value)
        document.documentElement.classList.toggle('dark', value === 'dark')
      }, theme)
      for (const width of [1280, 390]) {
        await page.setViewportSize({ width, height: 900 })
        const overflow = await dialog.evaluate((element) => ({
          client: element.clientWidth,
          scroll: element.scrollWidth,
          viewport: window.innerWidth,
          offenders: Array.from(element.querySelectorAll<HTMLElement>('*'))
            .filter((child) => child.scrollWidth > child.clientWidth)
            .slice(0, 8)
            .map((child) => ({
              tag: child.tagName,
              className: child.className,
              client: child.clientWidth,
              scroll: child.scrollWidth,
            })),
        }))
        // CSS pixels can round one way for the sheet and another for its content.
        expect(
          overflow.scroll <= overflow.client + 1,
          JSON.stringify(overflow),
        ).toBe(true)
        expect(
          (
            await new AxeBuilder({ page })
              .include('[role="dialog"]')
              .withTags(['wcag2a', 'wcag2aa', 'wcag21aa'])
              .analyze()
          ).violations,
        ).toEqual([])
        await page.screenshot({
          path: testInfo.outputPath(`workspace-files-${theme}-${width}.png`),
          fullPage: true,
        })
      }
    }

    await dialog.getByRole('button', { name: 'root', exact: true }).click()
    await dialog.getByText('page.html', { exact: true }).click()
    const frame = dialog.getByTitle('HTML preview: page.html')
    await expect(frame).toHaveAttribute('sandbox', '')
    await expect(
      page
        .frameLocator('iframe[title="HTML preview: page.html"]')
        .getByRole('heading', {
          name: 'Safe HTML',
        }),
    ).toBeVisible()
    await expect(
      page
        .frameLocator('iframe[title="HTML preview: page.html"]')
        .locator('html'),
    ).not.toHaveAttribute('data-script-ran', 'true')
    expect(scriptRequests).toEqual([])

    await dialog.getByRole('button', { name: 'root', exact: true }).click()
    for (const name of ['pixel.png', 'vector.svg']) {
      await dialog.getByText(name, { exact: true }).click()
      const image = dialog.getByRole('img', { name, exact: true })
      await expect(image).toBeVisible()
      await expect
        .poll(() =>
          image.evaluate((element: HTMLImageElement) => element.naturalWidth),
        )
        .toBeGreaterThan(0)
      await dialog.getByRole('button', { name: 'root', exact: true }).click()
    }

    const removed = await page.request.delete(`${base}/files`, {
      headers,
      params: { path: 'retry.md' },
    })
    expect(removed.ok()).toBe(true)
    await dialog.getByText('retry.md', { exact: true }).click()
    await expect(dialog.getByRole('alert')).toBeVisible()
    await write('retry.md', '# Recovered\n')
    await dialog.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(
      dialog.getByRole('heading', { name: 'Recovered', level: 2 }),
    ).toBeVisible()
  } finally {
    if (ref && headers) {
      const deleted = await page.request.delete(
        `/v1/m/sessions/workspaces/${encodeURIComponent(ref)}`,
        { headers },
      )
      expect
        .soft(deleted.ok(), `cleanup workspace: HTTP ${deleted.status()}`)
        .toBe(true)
    }
    rmSync(folder, { recursive: true, force: true })
  }
})
