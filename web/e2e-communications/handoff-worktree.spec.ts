// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
// A real offer and recipient read, followed by the console's worktree launch and diff.
// Uses the existing activated, disposable engine setup. No successful request is intercepted.
import { expect, test, type APIRequestContext } from '@playwright/test'
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const BASE = process.env.K3_E2E_BASE ?? ''
const TENANT = process.env.DEMO_TENANT ?? ''
const WORK = process.env.K3_E2E_WORK ?? ''
const PASSWORD = 'handoff-journey-local-passphrase'

function uuidv7() {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  const ms = BigInt(Date.now())
  for (let i = 0; i < 6; i++)
    bytes[i] = Number((ms >> BigInt(8 * (5 - i))) & 0xffn)
  bytes[6] = (bytes[6] & 0x0f) | 0x70
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const h = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

async function api(
  request: APIRequestContext,
  token: string,
  method: string,
  route: string,
  data?: unknown,
  headers: Record<string, string> = {},
) {
  return request.fetch(`${BASE}${route}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      'X-Olivares-Tenant': TENANT,
      ...headers,
    },
    ...(data === undefined ? {} : { data }),
  })
}

test('received handoff opens its SHA in a worktree and displays the committed diff', async ({
  request,
  page,
}) => {
  const evidence = path.join(WORK, 'evidence-handoff-worktree')
  mkdirSync(evidence, { recursive: true })
  const repo = path.join(WORK, 'handoff-repository')
  mkdirSync(repo)
  const git = (...args: string[]) =>
    execFileSync(
      'git',
      [
        '-c',
        'user.name=Journey',
        '-c',
        'user.email=journey@example.invalid',
        '-c',
        'commit.gpgsign=false',
        '-C',
        repo,
        ...args,
      ],
      { encoding: 'utf8' },
    ).trim()
  git('init', '-q', '-b', 'main')
  writeFileSync(path.join(repo, 'README'), 'base\n')
  git('add', '.')
  git('commit', '-qm', 'base')
  const baseSHA = git('rev-parse', 'HEAD')
  git('checkout', '-qb', 'work/handoff')
  writeFileSync(path.join(repo, 'README'), 'base\nhanded-over change\n')
  git('add', '.')
  git('commit', '-qm', 'handoff change')
  const sha = git('rev-parse', 'HEAD')
  git('checkout', '-q', 'main')

  const login = await request.post(`${BASE}/v1/auth/login`, {
    data: { email: 'demo@olivares.local', password: 'olivares-demo-estate' },
  })
  expect(login.status()).toBe(200)
  const tokenAdmin = (await login.json()).token as string
  // The demo superadmin provisions accounts; communication acts require real
  // tenant standing, so both participants are created with their first membership.
  async function actor(email: string, role: string) {
    const created = await api(request, tokenAdmin, 'POST', '/v1/users', {
      email,
      password: PASSWORD,
      tenant: TENANT,
      role,
    })
    expect(created.status()).toBe(201)
    const user = await created.json()
    expect(user.membership).toMatchObject({ tenant: TENANT, role })
    const signedIn = await request.post(`${BASE}/v1/auth/login`, {
      data: { email, password: PASSWORD },
    })
    expect(signedIn.status()).toBe(200)
    return {
      id: user.id as string,
      token: (await signedIn.json()).token as string,
    }
  }
  const sender = await actor('handoff-sender@journey.invalid', 'owner')
  const userA = sender.id
  const tokenA = sender.token
  const emailB = 'handoff-receiver@journey.invalid'
  const recipient = await actor(emailB, 'editor')
  const userB = recipient.id
  const tokenB = recipient.token
  const workspaces = await api(
    request,
    tokenA,
    'GET',
    '/v1/workspaces?limit=100',
  )
  expect(workspaces.status()).toBe(200)
  const workspaceID = (await workspaces.json()).items.find(
    (w: { slug: string }) => w.slug === 'billing',
  ).id as string
  const channel = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/channels',
    {
      workspace_id: workspaceID,
      slug: 'handoff-worktree',
      name: 'Handoff worktree',
      initial_grants: [userA, userB].map((ref) => ({
        subject: { kind: 'user', ref },
        can_read: true,
        can_write: true,
        can_admin: ref === userA,
      })),
    },
  )
  expect(channel.status()).toBe(201)
  const channelID = (await channel.json()).channel.id as string
  const item = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/work-items?mode=apply',
    {
      workspace_id: workspaceID,
      work_kind: 'implementation',
      title: 'Review handed-over branch',
      brief_md: 'Review its committed diff',
      context_refs: [],
      priority: 'p1',
      owner_kind: 'user',
      owner_ref: userA,
      provenance_kind: 'human',
      provenance_ref: 'journey:handoff-worktree',
      acceptance: [
        {
          criterion_key: 'review',
          ordinal: 0,
          statement: 'Receiver opens the handoff commit and sees its diff',
          required: true,
        },
      ],
    },
    { 'Idempotency-Key': uuidv7() },
  )
  expect(item.status()).toBe(200)
  const itemID = (await item.json()).result_id as string
  const ready = await api(
    request,
    tokenA,
    'POST',
    `/v1/m/sessions/work-items/${itemID}/transitions?mode=apply`,
    { command: 'item.ready' },
    { 'If-Match': item.headers().etag, 'Idempotency-Key': uuidv7() },
  )
  expect(ready.status()).toBe(200)
  const content = {
    summary: 'Branch handoff',
    next_action: 'Review the diff',
    branch: 'work/handoff',
    sha,
  }
  const offered = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/handoffs',
    {
      channel_id: channelID,
      work_item_id: itemID,
      recipient: { kind: 'user', ref: userB },
      handoff: content,
      ack_deadline: new Date(Date.now() + 30 * 60_000).toISOString(),
      expected_owner_epoch: 1,
    },
    { 'If-Match': ready.headers().etag, 'Idempotency-Key': uuidv7() },
  )
  expect(offered.status(), 'actual offer succeeds').toBe(201)
  const deliveryID = (await offered.json()).delivery_id as string
  const received = await api(
    request,
    tokenB,
    'GET',
    `/v1/m/sessions/deliveries/${deliveryID}/handoff`,
  )
  expect(received.status(), 'recipient reads the offer').toBe(200)
  expect((await received.json()).content).toMatchObject(content)

  const profileHome = path.join(WORK, 'provider-home')
  const configHome = path.join(profileHome, '.claude')
  mkdirSync(configHome, { recursive: true, mode: 0o700 })
  const profile = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/provider-profiles',
    {
      driver: 'claude',
      config_home: configHome,
      user_home: profileHome,
      display_name: 'Journey CLI',
      auth_source: 'provider_account_home',
    },
  )
  expect(profile.status()).toBe(201)
  const folder = await api(
    request,
    tokenA,
    'POST',
    '/v1/m/sessions/workspaces',
    {
      name: 'Handoff repository',
      root_path: repo,
      mount_mode: 'rw',
      dlp_mode: 'off',
    },
  )
  expect(folder.status()).toBe(201)
  const folderRef = (await folder.json()).workspace_ref as string
  let runRef = ''
  try {
    await page.goto('/login')
    await page.locator('#email').fill(emailB)
    await page.locator('#password').fill(PASSWORD)
    await page.getByRole('button', { name: /^sign in$/i }).click()
    await page.waitForURL((u) => !u.pathname.startsWith('/login'))
    await page.goto('/communications/handoffs')
    const workspacePicker = page
      .getByRole('button', {
        name: /All workspaces|Billing|Default|Workspace not in list/i,
      })
      .first()
    await workspacePicker.click()
    await page
      .getByRole('menuitem', { name: /^Billing/i })
      .first()
      .click()
    await page.getByRole('row').filter({ hasText: itemID }).click()
    const sheet = page
      .getByRole('dialog')
      .filter({ has: page.locator('[data-slot="handoff-detail"]') })
    await expect(sheet.getByText(sha, { exact: true })).toBeVisible()
    await page.screenshot({
      path: path.join(evidence, 'received.png'),
      fullPage: true,
    })
    await sheet
      .getByRole('button', { name: 'Open in a new session worktree' })
      .click()
    const dialog = page
      .getByRole('dialog')
      .filter({ has: page.getByRole('button', { name: 'Start', exact: true }) })
    const start = dialog.getByRole('button', { name: 'Start', exact: true })
    await expect(dialog.getByTestId('create-worktree-from')).toContainText(sha)
    await expect(start).toBeDisabled()
    await expect(
      dialog.getByText('Choose a repository folder for the worktree.'),
    ).toBeVisible()
    await page.screenshot({
      path: path.join(evidence, 'folder-required.png'),
      fullPage: true,
    })
    await dialog.getByLabel('Folder', { exact: true }).click()
    await page.getByRole('option', { name: /Handoff repository/ }).click()
    await expect(start).toBeEnabled()
    const launch = page.waitForResponse(
      (r) =>
        r.request().method() === 'POST' &&
        new URL(r.url()).pathname === '/v1/m/sessions/runs',
    )
    await start.click()
    const launched = await launch
    const run = await launched.json()
    if (typeof run.run_ref === 'string') runRef = run.run_ref
    expect(launched.status()).toBe(201)
    expect(launched.request().postDataJSON()).toMatchObject({
      workspace_ref: folderRef,
      worktree: true,
      worktree_from: sha,
    })
    const head = execFileSync(
      'git',
      ['-C', run.workspace_path, 'rev-parse', 'HEAD'],
      { encoding: 'utf8' },
    ).trim()
    expect(head).toBe(sha)
    expect(git('rev-parse', 'main')).toBe(baseSHA)
    expect(readFileSync(path.join(repo, 'README'), 'utf8')).toBe('base\n')
    // The context pane holds the existing CodeDiff; navigation preserves the run selection.
    await page.goto(`/sessions?session=run%3A${runRef}&pane=context`)
    const changes = page.getByTestId('session-branch-changes')
    await expect(changes).toBeVisible()
    await expect(changes.getByTestId('branch-changes-range')).toContainText(
      sha.slice(0, 12),
    )
    await expect(changes.getByTestId('branch-changes-range')).toContainText(
      baseSHA.slice(0, 12),
    )
    await changes.getByRole('button', { name: /README/ }).click()
    await expect(
      changes.getByLabel('At the base', { exact: true }).locator('.cm-line'),
    ).toHaveText(['base', ''])
    await expect(
      changes
        .getByLabel('At the branch tip', { exact: true })
        .locator('.cm-line'),
    ).toHaveText(['base', 'handed-over change', ''])
    await page.screenshot({
      path: path.join(evidence, 'displayed-diff.png'),
      fullPage: true,
    })
    writeFileSync(
      path.join(evidence, 'result.json'),
      JSON.stringify(
        {
          offer_status: offered.status(),
          recipient_read_status: received.status(),
          launch_status: launched.status(),
          delivery_id: deliveryID,
          received_sha: sha,
          worktree_head: head,
          run_ref: runRef,
          displayed_diff: 'base / base + handed-over change',
          identity: JSON.parse(
            readFileSync(path.join(WORK, 'identity.json'), 'utf8'),
          ),
        },
        null,
        2,
      ),
    )
  } finally {
    if (runRef) {
      try {
        const stopped = await api(
          request,
          tokenB,
          'POST',
          `/v1/m/sessions/runs/${runRef}/stop`,
          {},
        )
        expect(stopped.status()).toBe(200)
      } finally {
        const released = await api(
          request,
          tokenAdmin,
          'POST',
          `/v1/m/sessions/runs/${runRef}/cleanup`,
          { discard_worktree: true },
        )
        expect(released.status()).toBe(200)
      }
      expect(
        git('worktree', 'list', '--porcelain').match(/^worktree /gm),
      ).toHaveLength(1)
      expect(
        git('for-each-ref', '--format=%(refname)', 'refs/heads/olivares/'),
      ).toBe('')
    }
  }
})
