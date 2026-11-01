// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// OPEN A CHANGED FILE IN THE PERSON'S EDITOR, against a live seeded engine whose child is the
// fixture driver (no provider account, no model turn). The session's folder is a real git
// repository: README.md is committed, then changed. The real Changes pane lists it, the real
// changes API answers its text and HEAD's, and the link lands on the first changed line in the
// editor chosen in Settings, over SSH when a host is set. The browser cannot start the editor
// here (no URL handler is registered), so the journey asserts the link, not the launch.
//
// Run: E2E_SPECS=e2e/session-open-in-editor.spec.ts scripts/e2e-session-conversation.sh
import { execFileSync } from 'node:child_process'
import { realpathSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from '@playwright/test'
import { signInFixture } from './fixture-sign-in'

const demoTenant = process.env.DEMO_TENANT ?? ''
const fixtureOn = process.env.CONVERSATION_FIXTURE === '1'
const folder = process.env.E2E_SESSION_FOLDER ?? ''
const runRef = process.env.E2E_CAPTURE_RUN_REF ?? ''

const git = (...args: string[]) =>
  execFileSync(
    'git',
    [
      '-c',
      'user.name=Fixture',
      '-c',
      'user.email=fixture@example.invalid',
      '-c',
      'commit.gpgsign=false',
      ...args,
    ],
    { cwd: folder, env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null' } },
  )

test('a changed file opens in the editor chosen in Settings, at the changed line', async ({
  page,
}, testInfo) => {
  test.setTimeout(180_000)
  test.skip(
    !demoTenant || !fixtureOn || !folder || !runRef,
    'run through scripts/e2e-session-conversation.sh',
  )
  await page.context().addInitScript(() => {
    window.localStorage.setItem('olivares.theme', 'light')
    window.localStorage.setItem('olivares.lang', 'en')
  })
  await page.setViewportSize({ width: 1280, height: 900 })

  // The folder the agent worked in: README.md as committed, then as the agent left it.
  git('init', '-q', '-b', 'main')
  writeFileSync(join(folder, 'README.md'), '# Title\n')
  git('add', 'README.md')
  git('commit', '-q', '-m', 'first')
  writeFileSync(join(folder, 'README.md'), '# Title\nchanged by the agent\n')
  const root = realpathSync(folder)

  await signInFixture(
    page,
    { email: 'demo@olivares.local', password: 'olivares-demo-estate' },
    20_000,
  )
  const openChangedFile = async () => {
    await page.goto(
      `/sessions?session=run:${runRef}&pane=narrative&panel=changes`,
    )
    await page.getByTestId('changes-list').getByText('README.md').click()
    await expect(page.getByTestId('changes-file')).toContainText(
      'changed by the agent',
    )
  }

  // The default editor is VS Code, on this machine: the documented file URL, at line 2.
  await openChangedFile()
  await expect(
    page.getByRole('link', { name: 'Open in VS Code' }),
  ).toHaveAttribute('href', `vscode://file${root}/README.md:2`)
  await page.screenshot({
    path: testInfo.outputPath('changes-open-in-editor-1280-light.png'),
  })

  // Settings: another editor, and the host the editor reaches this folder by.
  await page.goto('/settings')
  await page.getByRole('combobox', { name: 'Editor for file links' }).click()
  await page.getByRole('option', { name: 'Zed' }).click()
  await page
    .getByRole('textbox', { name: 'SSH host for the editor' })
    .fill('me@devbox')
  await page.screenshot({
    path: testInfo.outputPath('settings-editor-1280-light.png'),
  })

  // The choice survives a reload and shapes the link: Zed's ssh form.
  await openChangedFile()
  await expect(page.getByRole('link', { name: 'Open in Zed' })).toHaveAttribute(
    'href',
    `zed://ssh/me@devbox${root}/README.md:2`,
  )

  // The Files tab (the workspace browser) offers the same link for the same file.
  await page.goto(`/sessions?session=run:${runRef}&pane=narrative&panel=files`)
  await page.getByRole('button', { name: 'README.md', exact: true }).click()
  const filesLink = page.getByRole('link', { name: 'Open in Zed' })
  await expect(filesLink).toHaveAttribute(
    'href',
    /^zed:\/\/ssh\/me@devbox\/.+\/README\.md:2$/,
  )
  // In the narrow side pane the viewer stacks under the file list: bring it into the shot.
  await filesLink.scrollIntoViewIfNeeded()
  await page.screenshot({
    path: testInfo.outputPath('files-open-in-editor-1280-light.png'),
  })
})
