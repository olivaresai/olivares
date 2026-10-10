// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, test } from '@playwright/test'
import auth from '../src/lib/i18n/locales/en/auth.json' with { type: 'json' }
import { expectReleaseVersion, surfaceOptions, watchSurface } from './surface'

test('release identity is read from the visible shell, including behind a dialog', async ({
  page,
}) => {
  await page.setContent(`<aside aria-label="Primary" aria-hidden="true"><span>26.10.1</span></aside>
    <div role="dialog" aria-label="New session">Start a session</div>`)
  await expectReleaseVersion(page, '26.10.1')
  await page.setContent(
    `<p data-testid="deployment-identity"><span>localhost:8443</span> · <span>26.10.1</span></p>`,
  )
  await expectReleaseVersion(page, '26.10.1')
})

test('release identity waits for the server version to render', async ({
  page,
}) => {
  await page.setContent('<aside aria-label="Primary"></aside>')
  await page.evaluate(() => {
    setTimeout(() => {
      document.querySelector('aside')!.innerHTML = '<span>26.10.1</span>'
    }, 100)
  })
  await expectReleaseVersion(page, '26.10.1')
})

for (const [name, identity] of [
  ['missing', ''],
  ['mismatched', '<aside aria-label="Primary"><span>26.10.0</span></aside>'],
  ['hidden', '<aside aria-label="Primary" hidden><span>26.10.1</span></aside>'],
] as const) {
  test(`release identity rejects a ${name} shell version even when the page mentions the release`, async ({
    page,
  }) => {
    await page.setContent(`${identity}<main>Release notes for 26.10.1</main>`)
    await expect(expectReleaseVersion(page, '26.10.1')).rejects.toThrow()
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
    await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
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
  await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
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
  await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
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

test('first-hour guard rejects unexplained actions even with an ellipsis or hidden reason', async ({
  page,
}) => {
  const findings: string[] = []
  await page.exposeFunction('__firstHourFinding', (message: string) =>
    findings.push(message),
  )
  await page.setContent(`<button type="submit" disabled>Start…</button>
    <button type="submit" disabled aria-describedby="hidden">Continue</button>
    <p id="hidden" hidden>Install a tool first.</p>`)
  await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
  await expect
    .poll(() => findings)
    .toEqual([
      'blank: disabled primary button without a visible reason: Start…',
      'blank: disabled primary button without a visible reason: Continue',
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
  await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
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
  await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
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
  await page.setContent(`<span>26.10.0</span><p>keys.command.session.new</p>
    <p>Plan and Apply answer 503</p><label>Configuration home<input placeholder="/home/user" /></label>
    <button type="submit" disabled>Start</button><div id="loading"><i class="animate-pulse"></i></div>`)
  // A 5 s bound here keeps this test short; the release bound is READY_MS.
  await page.evaluate(watchSurface, {
    ...surfaceOptions('26.10.1'),
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
  await page.setContent(`<span>26.10.1</span><p>New session</p>
    <p>github.com docs.example.com release.tar.gz 127.0.0.1</p>
    <p>https://settings.example.com user@workspaces.example.com</p>
    <p>https://example.com/setup.title?next=login.subtitle user@setup.title</p>
    <p>git:repo#commit tls.crt</p>
    <input placeholder="vault:secret/data/db#dsn" aria-label="Secret reference" />
    <label>Endpoint<input placeholder="http://127.0.0.1:11434" /></label>
    <input placeholder="https://settings.example.com" aria-label="Endpoint" />
    <button type="submit" disabled aria-describedby="reason">Start</button>
    <p id="reason">Install a tool first.</p><button>Ready</button>`)
  await page.evaluate(watchSurface, surfaceOptions('26.10.1'))
  expect(findings).toEqual([])
})
