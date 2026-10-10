// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { expect, type Page, test } from '@playwright/test'
import auth from '../src/lib/i18n/locales/en/auth.json' with { type: 'json' }
import {
  expectReleaseVersion,
  RELEASE_VERSION,
  surfaceOptions,
  watchSurface,
} from './surface'

test('release identity is read from the visible shell, including behind a dialog', async ({
  page,
}) => {
  await page.setContent(`<aside aria-label="Primary" aria-hidden="true"><span>0.1</span></aside>
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
  await page.setContent('<aside aria-label="Primary"></aside>')
  await page.evaluate(() => {
    setTimeout(() => {
      document.querySelector('aside')!.innerHTML = '<span>0.1</span>'
    }, 100)
  })
  await expectReleaseVersion(page, '0.1')
})

// A MAJOR.MINOR release is a substring of many other versions: the match is exact.
for (const [name, identity, release] of [
  ['missing', '', '0.1'],
  ['mismatched', '<aside aria-label="Primary"><span>0.2</span></aside>', '0.1'],
  [
    'hidden',
    '<aside aria-label="Primary" hidden><span>0.1</span></aside>',
    '0.1',
  ],
  ['longer', '<aside aria-label="Primary"><span>10.1</span></aside>', '0.1'],
  [
    'three-part',
    '<aside aria-label="Primary"><span>1.0.1</span></aside>',
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
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
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
  await page.setContent(`<aside aria-label="Primary"><span>0.2</span></aside><p>keys.command.session.new</p>
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
  await page.setContent(`<aside aria-label="Primary"><span>0.1</span></aside><p>New session</p>
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
  `<aside aria-label="Primary"><span>${version}</span></aside>`
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
  const behind = `<aside aria-label="Primary" aria-hidden="true"><span>0.2</span></aside>
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
  await page.setContent('<aside aria-label="Primary"></aside>')
  await page.evaluate(watchSurface, surfaceOptions('0.1'))
  await page.evaluate(() => {
    setTimeout(() => {
      document.querySelector('aside')!.innerHTML = '<span>0.2</span>'
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
  const hidden = `<aside aria-label="Primary" hidden><span>0.2</span></aside>`
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
