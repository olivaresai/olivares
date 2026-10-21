// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The kill switch tables must fit the content area at the widths the console is shown at.
// jsdom has no layout, so this is the only place the fit can be measured: the table's own
// scroller (the element DataTable gives `overflow-x: auto`) must have nothing to scroll, and
// every column header must end inside it and show whole. Measured on the 0.2 release binary before the fix:
// at 1440 and 1920 the Stops table was wider than its container and its last column header
// ("Canceled requests") and value were cut at the right edge.
import { expect, test, type Page } from '@playwright/test'
import { browserSession, fixtureFor } from './fixtures'

const ENGAGED_BY = 'user:01a14405-6b15-7e1a-b79d-48b7c2c7af5b'
// The engine accepts rule names up to 128 characters (modules/governance/policy.go maxMatchLen).
const LONG_RULE =
  'quarantine-production-agents-on-repeated-secret-exposure-findings'

const stops = [
  {
    id: 'ks-01',
    scope_kind: 'agent',
    scope_ref: 'release-bot',
    status: 'active',
    reason:
      'Release bot pushed to a protected branch outside the change window; stopped for review.',
    source: 'operator',
    engaged_by: ENGAGED_BY,
    engaged_aal: 1,
    engaged_at: new Date(Date.now() - 26 * 60_000).toISOString(),
    engage_audit_seq: 41,
    revoked_approvals: 0,
    reviewed: false,
  },
  {
    id: 'ks-02',
    scope_kind: 'estate',
    status: 'active',
    reason:
      'Guardian rule fired: three high-severity findings of the same kind inside ten minutes on production agents.',
    source: 'guardian',
    rule_ref: 'contain-high-severity',
    engaged_by: 'guardian:contain-high-severity',
    engaged_aal: 0,
    engaged_at: new Date(Date.now() - 3 * 3_600_000).toISOString(),
    engage_audit_seq: 40,
    revoked_approvals: 12,
    reenable_approval: 'ap-9',
    reviewed: false,
  },
  {
    id: 'ks-03',
    scope_kind: 'agent',
    scope_ref: 'billing-reconciler',
    agent_external_id: 'billing-reconciler-production-eu-west-1-instance-0042',
    status: 'reenabled',
    reason: 'Drill.',
    source: 'operator',
    engaged_by: ENGAGED_BY,
    engaged_aal: 2,
    engaged_at: new Date(Date.now() - 2 * 86_400_000).toISOString(),
    engage_audit_seq: 30,
    revoked_approvals: 3,
    reenabled_by: ENGAGED_BY,
    reenabled_at: new Date(Date.now() - 86_400_000).toISOString(),
    reviewed: false,
  },
  {
    id: 'ks-04',
    scope_kind: 'agent',
    scope_ref: '01a14405-6b15-7e1a-b79d-48b7c2c7af5b-billing-reconciler',
    status: 'active',
    reason:
      'Guardian rule fired after three secret exposure findings on the same agent inside ten minutes; the agent was stopped before it could reach the production ledger, and a human has to review the findings before it runs again.',
    source: 'guardian',
    rule_ref: LONG_RULE,
    engaged_by: 'guardian:' + LONG_RULE,
    engaged_aal: 0,
    engaged_at: new Date(Date.now() - 59 * 60_000).toISOString(),
    engage_audit_seq: 42,
    revoked_approvals: 128,
    reviewed: false,
  },
]

const guardianRules = [
  {
    id: 'gr-1',
    name: 'contain-high-severity',
    enabled: true,
    match_kinds: 'secret_exposure,policy_bypass,egress_violation',
    min_severity: 'high',
    action: 'stop_agent',
    mode: 'approval',
    agent_tier: 'production',
    created_by: ENGAGED_BY,
    note: 'Stops the agent that produced the finding after one human decision.',
  },
  {
    id: 'gr-2',
    name: LONG_RULE,
    enabled: true,
    match_kinds:
      'secret_exposure,policy_bypass,egress_violation,prompt_injection,tool_abuse,data_exfiltration',
    min_severity: 'critical',
    action: 'stop_estate',
    mode: 'auto',
    agent_tier: 'production',
    created_by: ENGAGED_BY,
  },
]

const guardianActions = [
  {
    id: 'ga-1',
    rule_id: 'gr-1',
    rule_name: 'contain-high-severity',
    finding_kind: 'secret_exposure',
    finding_ref: '9f2a41c07b3e',
    finding_severity: 'high',
    target_kind: 'agent',
    target_ref: 'release-bot',
    action: 'stop_agent',
    mode: 'approval',
    status: 'executed',
    killswitch_id: 'ks-01',
    detail: 'Stopped release-bot after approval ap-9.',
    executed_at: new Date(Date.now() - 26 * 60_000).toISOString(),
  },
  {
    id: 'ga-2',
    rule_id: 'gr-2',
    rule_name: LONG_RULE,
    finding_kind: 'secret_exposure',
    finding_ref: '2b7c90d1e4aa',
    finding_severity: 'critical',
    target_kind: 'agent',
    target_ref: 'agent:billing-reconciler-production-eu-west-1',
    action: 'stop_estate',
    mode: 'approval',
    status: 'pending',
    approval_id: 'ap-01j8k2m4n6p8q0r2s4t6v8w0x2',
    detail:
      'Waiting for one human decision before the whole estate stops: three critical secret exposure findings on production agents inside ten minutes.',
    executed_at: new Date(Date.now() - 4 * 60_000).toISOString(),
  },
]

async function mockApi(page: Page, lang: string) {
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p === '/v1/auth/browser-session')
      return route.fulfill({ json: browserSession })
    if (p.endsWith('/stream')) {
      return route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: ': connected\n\n',
      })
    }
    let payload: unknown
    if (p.endsWith('/killswitch')) payload = { items: stops, has_more: false }
    else if (p.endsWith('/guardian/rules'))
      payload = { items: guardianRules, has_more: false }
    else if (p.endsWith('/guardian/actions'))
      payload = { items: guardianActions, has_more: false }
    else payload = fixtureFor(p) ?? { items: [], has_more: false }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(payload),
    })
  })
  await page.addInitScript(
    ({ lang }) => {
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
      localStorage.setItem('olivares.lang', lang)
      localStorage.setItem('olivares.theme', 'dark')
    },
    { lang },
  )
}

/** Every table on the page whose scroller has something to scroll, or whose header is cut. */
async function cutTables(page: Page) {
  return page.locator('table').evaluateAll((tables) =>
    tables.flatMap((table) => {
      const scroller = table.parentElement as HTMLElement
      const edge = scroller.getBoundingClientRect().right
      const cutHeaders = [...table.querySelectorAll('th')]
        .filter((th) => th.getBoundingClientRect().right > edge + 1)
        .map((th) => th.textContent?.trim() ?? '')
      // A column squeezed below its own header is cut as surely as one past the edge.
      const clippedHeaders = [...table.querySelectorAll('th')]
        .filter((th) => th.scrollWidth > th.clientWidth + 1)
        .map((th) => th.textContent?.trim() ?? '')
      const overflow = scroller.scrollWidth - scroller.clientWidth
      return overflow > 1 || cutHeaders.length || clippedHeaders.length
        ? [
            {
              table: table.getAttribute('aria-label') ?? '(unnamed)',
              overflow,
              cutHeaders,
              clippedHeaders,
              container: scroller.clientWidth,
              columns: [...table.querySelectorAll('th')].map(
                (th) =>
                  `${th.textContent?.trim() || '(actions)'}=${Math.round(th.getBoundingClientRect().width)}`,
              ),
            },
          ]
        : []
    }),
  )
}

for (const lang of ['en', 'de', 'es', 'fr', 'ja', 'ru', 'zh', 'it', 'ko']) {
  for (const width of [1440, 1920]) {
    test(`kill switch tables fit the content area at ${width} in ${lang}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 1080 })
      await mockApi(page, lang)
      await page.goto('/killswitch')
      // Measure only after all three tables hold their rows: a table that is still a skeleton,
      // or absent, has nothing long in it and would pass for the wrong reason.
      // Stops: two operator stops carry the engaged-by handle.
      await expect(page.getByText(ENGAGED_BY, { exact: true })).toHaveCount(2)
      // The long rule name is a cell in each of the three tables: the Source cell of ks-04,
      // the rule's name and the trail's rule. Three means the guardian tables hold their rows.
      await expect(page.getByText(LONG_RULE, { exact: true })).toHaveCount(3)
      await expect(page.locator('table')).toHaveCount(3)
      try {
        expect(
          await cutTables(page),
          'tables wider than their container',
        ).toEqual([])
      } finally {
        await page.screenshot({
          path: testInfo.outputPath(`killswitch-${lang}-${width}.png`),
          fullPage: true,
        })
      }
    })
  }
}
