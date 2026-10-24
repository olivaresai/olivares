// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { expect, type Page, test } from '@playwright/test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { Skeleton } from '../src/components/ui/skeleton'
import { StatusChip } from '../src/components/ui/status-chip'
import auth from '../src/lib/i18n/locales/en/auth.json' with { type: 'json' }
import {
  expectReleaseVersion,
  openToolInstall,
  RELEASE_VERSION,
  recordSlowLoads,
  surfaceOptions,
  watchSurface,
} from './surface'

const workingSessions = renderToStaticMarkup(
  createElement(
    'main',
    null,
    ...['Claude Code', 'Codex', 'OpenCode'].map((name) =>
      createElement(StatusChip, {
        key: name,
        tone: 'ok',
        children: `${name} working`,
      }),
    ),
  ),
)

test('first-hour guard accepts live working sessions without a pending load', async ({
  page,
}) => {
  const findings: string[] = []
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(workingSessions)
  await expect(page.getByText('Codex working')).toBeVisible()
  await expect(page.locator('[data-slot="status-dot"]')).toHaveCount(3)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  expect(await page.evaluate(() => window.__firstHourLoading?.())).toBe(false)
  await page.clock.runFor(31_000)
  expect(await page.evaluate(() => window.__firstHourLoading?.())).toBe(false)
  expect(findings).toEqual([])
})

test('first-hour guard still rejects a skeleton alongside live working sessions', async ({
  page,
}) => {
  const findings: string[] = []
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  const skeleton = renderToStaticMarkup(
    createElement(Skeleton, {
      id: 'loading',
      style: { width: 100, height: 20 },
    }),
  )
  await page.setContent(workingSessions + skeleton)
  await expect(page.locator('#loading')).toBeVisible()
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  expect(await page.evaluate(() => window.__firstHourLoading?.())).toBe(true)
  await page.clock.runFor(31_000)
  await expect.poll(() => findings).toEqual(['blank: loading longer than 30 s'])
  await page.locator('#loading').evaluate((element) => element.remove())
  await page.clock.runFor(200)
  expect(await page.evaluate(() => window.__firstHourLoading?.())).toBe(false)
  await expect(page.getByText('Codex working')).toBeVisible()
})

test('release identity is read from the visible shell, including behind a dialog', async ({
  page,
}) => {
  await page.setContent(`<p data-testid="deployment-identity" aria-hidden="true"><span>0.1</span></p>
    <div role="dialog" aria-label="New session">Start a session</div>`)
  await expectReleaseVersion(page, '0.1')
  await page.setContent(
    `<p data-testid="deployment-identity"><span>localhost:8443</span> · <span>0.1</span></p>`,
  )
  await expectReleaseVersion(page, '0.1')
})

test('release identity waits for the server version to render', async ({
  page,
}) => {
  await page.setContent('<p data-testid="deployment-identity"></p>')
  await page.evaluate(() => {
    setTimeout(() => {
      document.querySelector('[data-testid="deployment-identity"]')!.innerHTML =
        '<span>0.1</span>'
    }, 100)
  })
  await expectReleaseVersion(page, '0.1')
})

// A MAJOR.MINOR release is a substring of many other versions: the match is exact.
for (const [name, identity, release] of [
  ['missing', '', '0.1'],
  [
    'mismatched',
    '<p data-testid="deployment-identity"><span>0.2</span></p>',
    '0.1',
  ],
  [
    'hidden',
    '<p data-testid="deployment-identity" hidden><span>0.1</span></p>',
    '0.1',
  ],
  [
    'longer',
    '<p data-testid="deployment-identity"><span>10.1</span></p>',
    '0.1',
  ],
  [
    'three-part',
    '<p data-testid="deployment-identity"><span>1.0.1</span></p>',
    '1.0',
  ],
] as const) {
  test(`release identity rejects a ${name} shell version even when the page mentions the release`, async ({
    page,
  }) => {
    await page.setContent(
      `${identity}<main>Release notes for ${release}</main>`,
    )
    await expect(expectReleaseVersion(page, release)).rejects.toThrow()
  })
}

for (const label of [auth.setup.creating, auth.login.signingIn]) {
  test(`first-hour guard accepts ${label} while pending, but times it out`, async ({
    page,
  }) => {
    const findings: string[] = []
    await page.clock.install()
    await page.exposeFunction('__firstHourFinding', (message: string) =>
      findings.push(message),
    )
    await page.setContent(`<button type="submit" disabled>${label}</button>`)
    await page.evaluate(watchSurface, surfaceOptions('0.1'))
    await page.clock.runFor(5200)
    // A slower host is not a hung page: the gate waits for readiness (#1177).
    expect(findings).toEqual([])
    await page.clock.runFor(25_000)
    await expect
      .poll(() => findings)
      .toEqual(['blank: loading longer than 30 s'])
  })
}

test('first-hour guard records a load longer than 5 s with its time, without failing it', async ({
  page,
}) => {
  const findings: string[] = []
  const slow: string[] = []
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.exposeFunction('__firstHourSlow', (message: string) =>
    slow.push(message),
  )
  await page.setContent('<main><i class="animate-spin"></i></main>')
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await page.clock.runFor(6000)
  await page.evaluate(() => document.querySelector('i')!.remove())
  await page.clock.runFor(200)
  await expect
    .poll(() => slow)
    .toEqual([expect.stringMatching(/^blank: loading took 6\.\d s$/)])
  expect(findings).toEqual([])
})

test('first-hour guard records a slow load the page left, and says when it is ready', async ({
  page,
}) => {
  const slow: string[] = []
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', () => undefined)
  await page.exposeFunction('__firstHourSlow', (message: string) =>
    slow.push(message),
  )
  await page.route('https://first-hour.invalid/**', (route) =>
    route.fulfill({
      contentType: 'text/html',
      body: '<main><i class="animate-spin"></i></main>',
    }),
  )
  await page.goto('https://first-hour.invalid/agent-tools')
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await page.clock.runFor(200)
  expect(await page.evaluate(() => window.__firstHourLoading?.())).toBe(true)
  await page.clock.runFor(6000)
  await page.evaluate(() => {
    history.pushState({}, '', '/sessions')
    document.querySelector('i')!.remove()
  })
  await page.clock.runFor(200)
  await expect
    .poll(() => slow)
    .toEqual([
      expect.stringMatching(
        /^\/agent-tools: left while loading after 6\.\d s$/,
      ),
    ])
  expect(await page.evaluate(() => window.__firstHourLoading?.())).toBe(false)
})

// A slow load names the requests the page made while it waited (#2608). The page
// shows a spinner and holds one POST open past the 5 s target, as a session start does.
const SESSIONS_PAGE = 'https://first-hour.invalid/sessions'
const RUNS_PATH = '/v1/m/sessions/runs'

async function holdRunsRequest(page: Page) {
  let release!: () => void
  const held = new Promise<void>((resolve) => (release = resolve))
  await page.route('https://first-hour.invalid/**', async (route) => {
    if (new URL(route.request().url()).pathname !== RUNS_PATH)
      return route.fulfill({
        contentType: 'text/html',
        body: '<main><i class="animate-spin"></i></main>',
      })
    await held
    return route.fulfill({
      status: 201,
      contentType: 'application/json',
      headers: { 'x-request-id': 'req-held-1' },
      body: '{}',
    })
  })
  return release
}

test('first-hour guard names the request a slow load waited on, with its status and engine request id', async ({
  page,
}) => {
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', () => undefined)
  const slowLoads = await recordSlowLoads(page)
  const release = await holdRunsRequest(page)
  await page.goto(SESSIONS_PAGE)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  const answered = page.evaluate(
    (path) =>
      fetch(`${path}?token=must-not-be-kept`, { method: 'POST' }).then(
        (response) => response.status,
      ),
    RUNS_PATH,
  )
  await page.clock.runFor(6000)
  const finished = page.waitForEvent('requestfinished', (request) =>
    request.url().includes(RUNS_PATH),
  )
  release()
  expect(await answered).toBe(201)
  await finished
  await page.evaluate(() => document.querySelector('i')!.remove())
  await page.clock.runFor(200)
  await expect.poll(() => slowLoads.length).toBe(1)
  expect(slowLoads[0].load).toMatch(/^\/sessions: loading took 6\.\d s$/)
  expect(slowLoads[0].requests).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        method: 'POST',
        path: RUNS_PATH,
        type: 'fetch',
        status: 201,
        requestId: 'req-held-1',
        startedAt: expect.stringMatching(/^\d{4}-\d\d-\d\dT[\d:.]+Z$/),
        ms: expect.any(Number),
      }),
    ]),
  )
  expect(JSON.stringify(slowLoads)).not.toContain('must-not-be-kept')
})

test('first-hour guard names a request still open when the page leaves a slow load', async ({
  page,
}) => {
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', () => undefined)
  const slowLoads = await recordSlowLoads(page)
  const release = await holdRunsRequest(page)
  await page.goto(SESSIONS_PAGE)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  void page.evaluate(
    (path) => void fetch(path, { method: 'POST' }).catch(() => undefined),
    RUNS_PATH,
  )
  await page.clock.runFor(6000)
  await page.evaluate(() => {
    history.pushState({}, '', '/sessions?session=run:1')
    document.querySelector('i')!.remove()
  })
  await page.clock.runFor(200)
  await expect.poll(() => slowLoads.length).toBe(1)
  expect(slowLoads[0].load).toMatch(
    /^\/sessions: left while loading after 6\.\d s$/,
  )
  expect(slowLoads[0].requests).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        method: 'POST',
        path: RUNS_PATH,
        pending: true,
      }),
    ]),
  )
  release()
})

test('first-hour guard closes the requests a new document tears down, and only those', async ({
  page,
}) => {
  await page.exposeFunction('__firstHourFinding', () => undefined)
  const slowLoads = await recordSlowLoads(page)
  const release = await holdRunsRequest(page)
  await page.goto(SESSIONS_PAGE)
  const sent = page.waitForRequest((request) =>
    request.url().includes(RUNS_PATH),
  )
  void page.evaluate(
    (path) => void fetch(path, { method: 'POST' }).catch(() => undefined),
    RUNS_PATH,
  )
  await sent
  // A fetch answering and a frame loading its own document do not end the page's
  // other requests: only the main frame's next document does.
  await page.evaluate(async () => {
    await fetch('/v1/other').then((response) => response.text())
    await new Promise<void>((done) => {
      const frame = document.createElement('iframe')
      frame.src = '/frame'
      frame.onload = () => done()
      document.body.append(frame)
    })
  })
  await page.evaluate(() =>
    window.__firstHourSlow?.('/sessions: before', 60_000),
  )
  // The browser reports neither a response nor a failure for a request that the next
  // document tears down, so it must not stay "open" for the rest of the run (#2608).
  await page.goto('https://first-hour.invalid/agent-tools')
  await page.evaluate(() =>
    window.__firstHourSlow?.('/agent-tools: after', 60_000),
  )
  await expect.poll(() => slowLoads.length).toBe(2)
  const [before, after] = slowLoads.map(
    ({ requests }) =>
      new Map(requests.map((request) => [request.path, request])),
  )
  expect(before.get(RUNS_PATH)).toEqual(
    expect.objectContaining({ pending: true }),
  )
  expect(before.get(RUNS_PATH)?.error).toBeUndefined()
  expect(after.get(RUNS_PATH)?.pending).toBeUndefined()
  expect(after.get(RUNS_PATH)?.error).toBe('the page loaded another document')
  expect(after.get('/agent-tools')?.error).toBeUndefined()
  expect(after.get('/v1/other')?.error).toBeUndefined()
  release()
})

test('first-hour guard keeps only the requests that overlapped the slow load, scripts included, and redacts what it writes', async ({
  page,
}) => {
  await page.exposeFunction('__firstHourFinding', () => undefined)
  const slowLoads = await recordSlowLoads(page, (text) =>
    text.replaceAll('during', '<redacted>'),
  )
  await page.route('https://first-hour.invalid/**', (route) =>
    new URL(route.request().url()).pathname === '/asset.js'
      ? route.fulfill({ contentType: 'text/javascript', body: '' })
      : route.fulfill({
          contentType: 'text/html',
          body: '<main>ready</main>',
        }),
  )
  await page.goto(SESSIONS_PAGE)
  await page.evaluate(() => fetch('/v1/before').then((r) => r.status))
  await page.waitForTimeout(800)
  await page.evaluate(
    () =>
      new Promise<void>((done) => {
        const script = document.createElement('script')
        script.src = '/asset.js'
        script.onload = () => done()
        document.head.append(script)
      }),
  )
  await page.evaluate(() => fetch('/v1/during').then((r) => r.status))
  await page.evaluate(() =>
    window.__firstHourSlow?.('/sessions: window during', 400),
  )
  await expect.poll(() => slowLoads.length).toBe(1)
  const { load, requests, omitted } = slowLoads[0]
  expect(load).toBe('/sessions: window <redacted>')
  expect(requests.map(({ path, type }) => [path, type])).toEqual(
    expect.arrayContaining([
      ['/asset.js', 'script'],
      ['/v1/<redacted>', 'fetch'],
    ]),
  )
  const paths = requests.map((request) => request.path)
  expect(paths).not.toContain('/v1/before')
  expect(paths).not.toContain('/sessions')
  expect(omitted).toBe(0)
})

test('first-hour guard lists the requests with the longest share of the wait and counts the rest', async ({
  page,
}) => {
  await page.exposeFunction('__firstHourFinding', () => undefined)
  const slowLoads = await recordSlowLoads(page)
  await page.route('https://first-hour.invalid/**', async (route) => {
    if (new URL(route.request().url()).pathname === '/v1/slow')
      await new Promise((resolve) => setTimeout(resolve, 400))
    return route.fulfill({
      contentType: 'text/html',
      body: '<main>ready</main>',
    })
  })
  await page.goto(SESSIONS_PAGE)
  // Bodies are read so every request has finished, and the browser's finish events
  // have a moment to reach the recorder before the load is reported.
  await page.evaluate(() =>
    Promise.all(
      [...Array.from({ length: 25 }, (_, n) => `/v1/n/${n}`), '/v1/slow'].map(
        (path) => fetch(path).then((response) => response.text()),
      ),
    ).then((all) => all.length),
  )
  await page.waitForTimeout(200)
  await page.evaluate(() => window.__firstHourSlow?.('/sessions: many', 5000))
  await expect.poll(() => slowLoads.length).toBe(1)
  const { requests, omitted } = slowLoads[0]
  expect(requests).toHaveLength(20)
  // The document, 25 fetches and the slow one overlapped the wait.
  expect(requests.length + omitted).toBe(27)
  expect(requests.map((request) => request.path)).toContain('/v1/slow')
  expect(requests.map((request) => request.startedAt)).toEqual(
    requests.map((request) => request.startedAt).sort(),
  )
  expect(requests.every((request) => request.pending === undefined)).toBe(true)
})

test('first-hour guard names a request the browser could not complete', async ({
  page,
}) => {
  await page.clock.install()
  await page.exposeFunction('__firstHourFinding', () => undefined)
  const slowLoads = await recordSlowLoads(page)
  await page.route('https://first-hour.invalid/**', (route) =>
    new URL(route.request().url()).pathname === RUNS_PATH
      ? route.abort('connectionreset')
      : route.fulfill({
          contentType: 'text/html',
          body: '<main><i class="animate-spin"></i></main>',
        }),
  )
  await page.goto(SESSIONS_PAGE)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  const failed = page.waitForEvent('requestfailed')
  await page.evaluate(
    (path) => void fetch(path, { method: 'POST' }).catch(() => undefined),
    RUNS_PATH,
  )
  await failed
  await page.clock.runFor(6000)
  await page.evaluate(() => document.querySelector('i')!.remove())
  await page.clock.runFor(200)
  await expect.poll(() => slowLoads.length).toBe(1)
  expect(slowLoads[0].requests).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        method: 'POST',
        path: RUNS_PATH,
        status: null,
        error: expect.stringContaining('ERR_CONNECTION_RESET'),
      }),
    ]),
  )
})

test('first-hour guard rejects unexplained actions even with an ellipsis or hidden reason', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(`<button type="submit" disabled>Start…</button>
    <button type="submit" disabled aria-describedby="hidden">Continue</button>
    <p id="hidden" hidden>Install a tool first.</p>
    <button type="submit" disabled aria-describedby="invisible">Save</button>
    <p id="invisible" style="visibility: hidden">Choose a model first.</p>`)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await expect
    .poll(() => findings)
    .toEqual([
      'blank: disabled primary button without a visible reason: Start…',
      'blank: disabled primary button without a visible reason: Continue',
      'blank: disabled primary button without a visible reason: Save',
    ])
})

test('first-hour guard ignores a disabled action inside a closed disclosure, and checks it once open', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  // AI tools' "install from a source" form: closed by default, its Review disabled
  // until the inventory loads. Chromium keeps a closed disclosure's content laid out
  // (content-visibility: hidden), so it has boxes but no rendered text (#330).
  await page.setContent(`<details><summary>Install from a source</summary>
    <form><button type="submit" disabled>Review</button></form></details>`)
  // The premise: the closed button still has boxes, so rects alone cannot hide it.
  expect(
    await page.evaluate(
      () => document.querySelector('button')!.getClientRects().length,
    ),
  ).toBeGreaterThan(0)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await page.waitForTimeout(500)
  expect(findings).toEqual([])
  await page.getByText('Install from a source').click()
  await expect
    .poll(() => findings)
    .toEqual([
      'blank: disabled primary button without a visible reason: Review',
    ])
})

test('first-hour guard detects dotted and namespace-qualified fallback keys', async ({
  page,
}) => {
  const findings: string[] = []
  const keys = [
    'nav.general',
    'common:validation.required',
    'workspaces.subtitle',
    'settings:nav.general',
    'common:save',
    'keys.command.session.new',
    'setup.title',
    'login.subtitle',
    'general.scope',
    'secondFactor.codePrompt',
  ]
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(keys.map((key) => `<p>${key}</p>`).join(''))
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await expect
    .poll(() => findings)
    .toEqual(keys.map((key) => `blank: unresolved translation key: ${key}`))
})

test('first-hour guard detects fallback keys in visible text attributes only', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(`
    <input placeholder="setup.tokenPlaceholder" />
    <textarea placeholder="login.emailPlaceholder"></textarea>
    <button aria-label="general.scope">Scope</button>
    <button title="setup.title">Setup</button>
    <img alt="secondFactor.qrAlt" width="50" height="50" />
    <input hidden placeholder="setup.password" />
    <div style="display:none"><input placeholder="setup.email" /></div>
    <input value="setup.invalidToken" placeholder="login.password" />`)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await expect.poll(() => findings.length).toBe(5)
  expect(findings).toEqual(
    [
      'setup.tokenPlaceholder',
      'login.emailPlaceholder',
      'general.scope',
      'setup.title',
      'secondFactor.qrAlt',
    ].map((key) => `blank: unresolved translation key: ${key}`),
  )
})

test('first-hour guard detects the recorded defects, including replaced loaders', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(`<p data-testid="deployment-identity"><span>0.2</span></p><p>keys.command.session.new</p>
    <p>Plan and Apply answer 503</p><label>Configuration home<input placeholder="/home/user" /></label>
    <button type="submit" disabled>Start</button><div id="loading"><i class="animate-pulse"></i></div>`)
  // A 5 s bound here keeps this test short; the release bound is READY_MS.
  await page.evaluate(watchSurface, {
    ...surfaceOptions('0.1'),
    readyMs: 5000,
  })
  await expect.poll(() => findings.join('\n')).toMatch(/translation/)
  expect(findings.join('\n')).toMatch(/HTTP/)
  expect(findings.join('\n')).toMatch(/filesystem/)
  expect(findings.join('\n')).toMatch(/disabled/)
  expect(findings.join('\n')).toMatch(/version/)
  await page.evaluate(() =>
    setInterval(() => {
      document.querySelector('#loading')!.innerHTML =
        '<i class="animate-pulse"></i>'
    }, 200),
  )
  await expect
    .poll(() => findings.join('\n'), { timeout: 7000 })
    .toMatch(/loading longer than 5 s/)
})

test('first-hour guard accepts translated copy and a visible disabled reason', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(`<p data-testid="deployment-identity"><span>0.1</span></p><p>New session</p>
    <p>github.com docs.example.com release.tar.gz 127.0.0.1</p>
    <p>https://settings.example.com user@workspaces.example.com</p>
    <p>https://example.com/setup.title?next=login.subtitle user@setup.title</p>
    <p>git:repo#commit tls.crt</p>
    <input placeholder="vault:secret/data/db#dsn" aria-label="Secret reference" />
    <label>Endpoint<input placeholder="http://127.0.0.1:11434" /></label>
    <input placeholder="https://settings.example.com" aria-label="Endpoint" />
    <button type="submit" disabled aria-describedby="reason">Start</button>
    <p id="reason">Install a tool first.</p><button>Ready</button>`)
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  expect(findings).toEqual([])
})

// What the watcher reports for `html`, once it has provably looked at it. The path
// field below is reported after every version check in one inspection pass, so its
// arrival means a version finding for this page has already been delivered.
async function watch(
  page: Page,
  html: string,
  release: string,
): Promise<{ findings: string[]; versions: string[] | undefined }> {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(
    `${html}<label>Configuration home<input placeholder="/home/user" /></label>`,
  )
  await page.evaluate(watchSurface, surfaceOptions(release))
  await expect.poll(() => findings.some((f) => /filesystem/.test(f))).toBe(true)
  return {
    findings: findings
      .filter((finding) => !/filesystem/.test(finding))
      .map((finding) => finding.replace(/^\S*: /, '')),
    versions: await page.evaluate(() => window.__firstHourVersions?.()),
  }
}

const shell = (version: string) =>
  `<p data-testid="deployment-identity"><span>${version}</span></p>`
const deployment = (version: string) =>
  `<p data-testid="deployment-identity"><span>127.0.0.1:8443</span> · <span>${version}</span></p>`
const stale = (release: string, other: string) => [
  `page version ${other} differs from release ${release}`,
]

test('the release version the shell admits is admitted by the browser gate', () => {
  // The same file and the same comment/blank-line stripping scripts/release-first-hour.sh uses.
  const stamped = readFileSync(
    new URL('../../RELEASE-VERSION', import.meta.url),
    'utf8',
  )
    .split('\n')
    .filter((line) => line !== '' && !line.startsWith('#'))
    .join('')
  expect(stamped).toMatch(RELEASE_VERSION)
  for (const version of ['0.1', '1.0', '1.10']) {
    expect(version).toMatch(RELEASE_VERSION)
  }
  for (const version of ['26.10.1', '1', '1.0.0', 'v1.0', '', '1.', '.1']) {
    expect(version).not.toMatch(RELEASE_VERSION)
  }
})

for (const release of ['0.1', '1.0', '1.10']) {
  for (const [place, identity] of [
    ['shell', shell(release)],
    ['deployment line', deployment(release)],
  ] as const) {
    test(`first-hour guard accepts the ${release} identity in the ${place}`, async ({
      page,
    }) => {
      // `versions` shows the watcher read the identity, not that it saw nothing.
      expect(await watch(page, identity, release)).toEqual({
        findings: [],
        versions: [release],
      })
    })
  }
}

// Strings and not numbers: 1.1 and 1.10 are different releases. A prefix, a
// pre-release and build metadata are still a different identity.
for (const [release, other] of [
  ['0.1', '0.2'],
  ['1.0', '0.9'],
  ['1.10', '1.9'],
  ['1.10', '1.1'],
  ['1.1', '1.10'],
  ['0.1', '26.10.1'],
  ['0.1', 'v0.1'],
  ['0.1', '0.1-rc1'],
  ['1.0', '1.0+abc'],
] as const) {
  for (const [place, identity] of [
    ['shell', shell(other)],
    ['deployment line', deployment(other)],
  ] as const) {
    test(`first-hour guard names a stale ${other} identity in the ${place} against release ${release}`, async ({
      page,
    }) => {
      const { findings } = await watch(page, identity, release)
      expect(findings).toEqual(stale(release, other))
    })
  }
}

// The only case where the watcher alone sees it: the exact release is painted too.
for (const [place, identity] of [
  ['deployment line', `${shell('0.1')}${deployment('0.2')}`],
  ['shell', `${shell('0.2')}${deployment('0.1')}`],
] as const) {
  test(`first-hour guard names a stale ${place} next to a current identity`, async ({
    page,
  }) => {
    const { findings } = await watch(page, identity, '0.1')
    expect(findings).toEqual(stale('0.1', '0.2'))
  })
}

test('first-hour guard names a stale shell behind a dialog', async ({
  page,
}) => {
  const behind = `<p data-testid="deployment-identity" aria-hidden="true"><span>0.2</span></p>
    <div role="dialog" aria-label="New session">Start a session</div>`
  const { findings } = await watch(page, behind, '0.1')
  expect(findings).toEqual(stale('0.1', '0.2'))
})

test('first-hour guard names an identity that is painted after it started', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent('<p data-testid="deployment-identity"></p>')
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await page.evaluate(() => {
    setTimeout(() => {
      document.querySelector('[data-testid="deployment-identity"]')!.innerHTML =
        '<span>0.2</span>'
    }, 300)
  })
  await expect
    .poll(() => findings.map((f) => f.replace(/^\S*: /, '')))
    .toEqual(stale('0.1', '0.2'))
})

test('first-hour guard reads a bare IP host as a host, not as a version', async ({
  page,
}) => {
  // A console served on the default port shows its host without a port.
  const identity = `<p data-testid="deployment-identity"><span>10.0.0.5</span> · <span>0.1</span></p>`
  expect(await watch(page, identity, '0.1')).toEqual({
    findings: [],
    versions: ['0.1'],
  })
})

test('first-hour guard compares only an identity the user can see', async ({
  page,
}) => {
  const hidden = `<p data-testid="deployment-identity" hidden><span>0.2</span></p>`
  expect(await watch(page, `${hidden}${deployment('0.1')}`, '0.1')).toEqual({
    findings: [],
    versions: ['0.1'],
  })
})

test('first-hour guard reports the identity versions as evidence', async ({
  page,
}) => {
  const { versions } = await watch(
    page,
    `${shell('0.2')}${deployment('0.2')}<main>0.1</main>`,
    '0.1',
  )
  expect(versions).toEqual(['0.2'])
})

test('first-hour guard leaves numbers that are not the release identity alone', async ({
  page,
}) => {
  const prose = `<main>
    <p>CVSS v3.1, FOCUS v1.3, SCIM 2.0, CycloneDX 1.6 and pre-1.0 schemas</p>
    <p>Loaded in 0.2 s, 1.5 GB used, 127.0.0.1:8443, 10.0.0.0/8, Release notes for 0.2</p>
    <table><tr><td>0.5</td><td>1.0</td><td>1.10</td></tr></table></main>`
  expect(await watch(page, `${shell('0.1')}${prose}`, '0.1')).toEqual({
    findings: [],
    versions: ['0.1'],
  })
})

// AI tools shows which menu a tool has only once its reads finish (#330: Codex was
// listed as not installed 30 s after the stage had already looked for its options).
const notInstalled = `<p data-testid="not-installed">Not installed: Codex · Ollama
  <button onclick="this.insertAdjacentHTML('afterend', '<div role=menuitem>Install Codex</div>')">Install</button></p>`
const installed = `<section><h2>Codex</h2>
  <button aria-label="Options for Codex" onclick="this.insertAdjacentHTML('afterend', '<div role=menuitem>Update</div>')">…</button></section>`
for (const [state, markup, item] of [
  ['not installed', notInstalled, 'Install Codex'],
  ['installed', installed, 'Update'],
] as const) {
  test(`first-hour install waits for AI tools to list Codex as ${state}`, async ({
    page,
  }) => {
    await page.setContent('<main></main>')
    await page.evaluate((html) => {
      setTimeout(() => {
        document.querySelector('main')!.innerHTML = html
      }, 500)
    }, markup)
    const review = await openToolInstall(page, 'Codex')
    await expect(review).toHaveText(item)
  })
}
