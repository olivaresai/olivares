// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { execFile } from 'node:child_process'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { promisify } from 'node:util'
import { expect, test } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'
import { sessionToken } from './live-session'

process.env.PLAYWRIGHT_NO_COPY_PROMPT = '1'
test.describe.configure({ retries: 0 })
test.use({ screenshot: 'off', trace: 'off', video: 'off' })

test('fresh skills catalog installs a builtin and assigns its immutable revision', async ({
  page,
}, testInfo) => {
  test.setTimeout(180_000)
  const setupToken = process.env.PLAYWRIGHT_SETUP_TOKEN ?? ''
  test.skip(!setupToken, 'Requires a fresh engine via scripts/web-e2e.sh')
  await page.goto('/setup')
  await page.locator('#token').fill(setupToken)
  await page.locator('#setup-email').fill('skills@example.test')
  await page.locator('#setup-password').fill('skills-fixture-password-1515')
  await page.getByRole('button', { name: /create administrator/i }).click()
  await page.waitForURL('**/onboarding')

  const workspaces = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/v1/workspaces' && r.ok(),
  )
  await page.goto('/skills')
  const workspace = (await (await workspaces).json()).items[0]
  expect(workspace.id).toBeTruthy()
  await expect(
    page.getByRole('button', { name: 'Install ecc-security', exact: true }),
  ).toBeVisible()
  await expect(page.getByLabel('Workspace', { exact: true })).toHaveValue(
    workspace.id,
  )
  const published = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === '/v1/m/skills/packs' &&
      r.request().method() === 'POST' &&
      r.ok(),
  )
  const pinned = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === '/v1/m/skills/assignments' &&
      r.request().method() === 'POST' &&
      r.ok(),
  )
  await page
    .getByRole('button', { name: 'Install ecc-security', exact: true })
    .click()
  const publishedResponse = await published
  const installed = await publishedResponse.json()
  const assignment = await (await pinned).json()
  expect(installed.revision.source.kind).toBe('builtin')
  expect(installed.revision.source.source_digest).toMatch(/^[0-9a-f]{64}$/)
  expect(assignment.assignment.pack_revision_id).toBe(installed.revision.id)
  expect(assignment.assignment.target_id).toBe(workspace.id)
  await expect(
    page
      .getByRole('status')
      .filter({ hasText: 'Skill pack installed and assigned' }),
  ).toBeVisible()
  await expect(
    page.getByRole('gridcell', { name: 'ecc-security', exact: true }),
  ).toBeVisible()
  // Public, already-reviewed source pinned by the embedded catalog. No code runs.
  const gitCommit = 'c05b2d6614f62f6db0047669aa4eefb223d478f9'
  await page.getByText('From Git', { exact: true }).focus()
  await page.keyboard.press('Enter')
  await page.getByLabel(/^Git URL/).fill('https://github.com/affaan-m/ECC')
  await page.getByLabel('Git ref', { exact: true }).fill(gitCommit)
  await page
    .getByLabel('Subdirectory', { exact: true })
    .fill('skills/security-review')
  await page.getByLabel('Pack name', { exact: true }).fill('review-console')
  const gitPublished = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === '/v1/m/skills/packs' &&
      r.request().method() === 'POST' &&
      r.ok(),
  )
  const gitPinned = page.waitForResponse(
    (r) =>
      new URL(r.url()).pathname === '/v1/m/skills/assignments' &&
      r.request().method() === 'POST' &&
      r.ok(),
  )
  await page
    .getByRole('button', { name: 'Install from Git', exact: true })
    .click()
  const gitInstalled = await (await gitPublished).json()
  const gitAssignment = await (await gitPinned).json()
  expect(gitAssignment.assignment.pack_revision_id).toBe(
    gitInstalled.revision.id,
  )
  expect(gitAssignment.assignment.target_id).toBe(workspace.id)
  expect(gitInstalled.revision.source.resolved_commit).toBe(gitCommit)
  expect(gitInstalled.revision.source.source_digest).toMatch(/^[0-9a-f]{64}$/)
  await expect(
    page.getByRole('gridcell', { name: 'review-console', exact: true }),
  ).toBeVisible()

  const binary = process.env.OLIVARES_E2E_BINARY
  if (binary) {
    const credentialDir = await mkdtemp(join(tmpdir(), 'skills-cli-'))
    try {
      const tokenFile = join(credentialDir, 'token')
      await writeFile(tokenFile, await sessionToken(page), { mode: 0o600 })
      const tenant = publishedResponse.request().headers()['x-olivares-tenant']
      expect(tenant).toBeTruthy()
      const run = async (args: string[]) => {
        const { stdout } = await promisify(execFile)(
          binary,
          [
            'skills',
            ...args,
            '--server',
            process.env.PLAYWRIGHT_BASE_URL!,
            '--tenant',
            tenant,
            '--token-file',
            tokenFile,
            '-o',
            'json',
          ],
          {
            timeout: 90_000,
            env: {
              ...process.env,
              OLIVARES_CLI_CONFIG: join(credentialDir, 'client.yaml'),
            },
          },
        )
        return JSON.parse(stdout)
      }
      const catalog = await run(['ls', '--builtin'])
      expect(
        catalog.items.some(
          (item: { id: string }) => item.id === 'ecc-security',
        ),
      ).toBe(true)
      const cliBuiltin = await run([
        'install',
        '--builtin',
        'ecc-security',
        '--name',
        'security-cli',
        '--idempotency-key',
        'skills-e2e-builtin',
      ])
      expect(cliBuiltin.revision.source.source_digest).toBe(
        installed.revision.source.source_digest,
      )
      const cliPin = await run([
        'assign',
        cliBuiltin.pack.id,
        '--revision',
        cliBuiltin.revision.id,
        '--workspace',
        workspace.id,
      ])
      expect(cliPin.assignment.pack_revision_id).toBe(cliBuiltin.revision.id)
      const cliGit = await run([
        'install',
        '--git',
        'https://github.com/affaan-m/ECC',
        '--ref',
        gitCommit,
        '--subdir',
        'skills/security-review',
        '--name',
        'review-cli',
        '--idempotency-key',
        'skills-e2e-git',
      ])
      expect(cliGit.revision.source.resolved_commit).toBe(gitCommit)
      // Raw Git pack encoding can vary; the validated file manifest is canonical.
      expect(cliGit.revision.source.source_digest).toMatch(/^[0-9a-f]{64}$/)
      expect(cliGit.revision.manifest_digest).toBe(
        gitInstalled.revision.manifest_digest,
      )
      await expect(
        run([
          'install',
          '--git',
          'https://github.com/affaan-m/ECC',
          '--ref',
          gitCommit,
          '--subdir',
          'skills/security-review',
          '--name',
          'review-digest-refused',
          '--expected-digest',
          '0'.repeat(64),
          '--idempotency-key',
          'skills-e2e-git-digest-refused',
        ]),
      ).rejects.toMatchObject({
        code: 1,
        stderr: expect.stringContaining('"code":"source_changed"'),
      })
      const cliGitPin = await run([
        'assign',
        cliGit.pack.id,
        '--revision',
        cliGit.revision.id,
        '--workspace',
        workspace.id,
      ])
      expect(cliGitPin.assignment.pack_revision_id).toBe(cliGit.revision.id)
      expect(cliGitPin.assignment.target_id).toBe(workspace.id)
      const detail = await run(['get', cliBuiltin.pack.id])
      expect(detail.revisions[0].id).toBe(cliBuiltin.revision.id)
    } finally {
      await rm(credentialDir, { recursive: true, force: true })
    }
  }
  for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width: 1280, height: 900 })
    // The theme is chosen in the account menu (#3161).
    await page.getByRole('button', { name: /, Account$/ }).click()
    await page
      .getByRole('menuitemradio', { name: new RegExp(`^${theme}$`, 'i') })
      .click()
    await page.goto('/skills')
    await expect(
      page.getByRole('gridcell', { name: 'review-console', exact: true }),
    ).toBeVisible()
    await page.getByText('From Git', { exact: true }).focus()
    await page.keyboard.press('Enter')
    const accessibility = await new AxeBuilder({ page })
      .include('section[aria-label="Install a skill pack"]')
      .analyze()
    expect(accessibility.violations).toEqual([])
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 })
      await page.screenshot({
        path: testInfo.outputPath(`skills-${theme}-${width}.png`),
        fullPage: true,
      })
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true)
    }
  }
})
