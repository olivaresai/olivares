// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import assert from 'node:assert/strict'
import AxeBuilder from '@axe-core/playwright'
import type { Page } from '@playwright/test'
import type { RunDTO, RunActivityDTO } from '../src/features/agentops/types.ts'
import type { ApprovalDTO } from '../src/features/governance/types.ts'

// API fixtures only: the surfaces, their markers and the stylesheet are the real console.
export async function auditTrust(page: Page, origin: string) {
  const run: RunDTO = {
    run_ref: 'trust-run',
    name: 'Review project files',
    provider_driver: 'claude',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    workspace_path: '/srv/project',
    state: 'running',
    pending_approval_ref: 'trust-approval',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: true,
    critical: false,
  }
  const approval: ApprovalDTO = {
    id: 'trust-approval',
    session_ref: 'osn_trust',
    action: 'claude.tool.use',
    subject_kind: 'claude.tool',
    subject_ref: run.run_ref,
    requested_by: 'session:osn_trust',
    status: 'pending',
    required_approvals: 1,
    approve_count: 0,
    reject_count: 0,
    review: { tool: 'Bash', text: 'pnpm install' },
    reason: 'Install project dependencies.',
    escalated: false,
  }
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    if (p === '/v1/m/governance/approvals')
      return route.fulfill({ json: { items: [approval], has_more: false } })
    if (p === '/v1/m/governance/approvals/trust-approval')
      return route.fulfill({ json: approval })
    if (p === '/v1/m/sessions/runs')
      return route.fulfill({ json: { items: [run], has_more: false } })
    if (p === '/v1/m/sessions/runs/trust-run/activity')
      return route.fulfill({
        json: {
          driver: 'claude',
          transcript: 'read',
          context_tokens: 0,
          compactions: [],
          todos: [],
          agents: [],
          tasks: [],
          files: [],
          skills: [],
          mcp_servers: [],
        } satisfies RunActivityDTO,
      })
    if (p === '/v1/m/sessions/runs/trust-run')
      return route.fulfill({ json: run })
    if (p === '/v1/m/sessions/live')
      return route.fulfill({ json: { items: [], has_more: false } })
    return route.fallback()
  })
  page.setDefaultTimeout(20_000)
  await page.goto(origin + '/sessions?session=run%3Atrust-run')
  await page.addStyleTag({
    content:
      '*, *::before, *::after { transition: none !important; animation: none !important; }',
  })
  const card = '[data-testid="conversation-approval"]'
  await page
    .locator(card)
    .getByText(approval.reason!, { exact: true })
    .waitFor({ state: 'visible' })

  const accents = [
    '--accent',
    '--on-accent',
    '--accent-text',
    '--accent-soft',
    '--accent-line',
    '--accent-border',
  ]
  async function measure(selector: string) {
    const root = page.locator(selector)
    assert.equal(
      await root.count(),
      1,
      `missing/ambiguous trust surface: ${selector}`,
    )
    assert.equal(
      await root.getAttribute('data-trust-chrome'),
      '',
      `unmarked surface: ${selector}`,
    )
    const scan = async () => {
      const result = await new AxeBuilder({ page })
        .include(selector)
        .withRules(['color-contrast'])
        .analyze()
      // axe labels identical foreground/background as incomplete, but it has measured 1:1.
      const equal = result.incomplete.flatMap((v) =>
        v.nodes.map((n) => {
          assert(
            n.any.some(
              (c) =>
                c.data?.messageKey === 'equalRatio' &&
                c.data.contrastRatio === 1,
            ),
            `unmeasured contrast: ${selector}: ${n.failureSummary}`,
          )
          return `${v.id}:${n.target.join(' ')}`
        }),
      )
      return [
        ...result.violations.flatMap((v) =>
          v.nodes.map((n) => `${v.id}:${n.target.join(' ')}`),
        ),
        ...equal,
      ].sort()
    }
    const baseline = await scan()
    const paint = () =>
      root.evaluate((el) =>
        [el, ...el.querySelectorAll('*')]
          .filter((n) => n.getClientRects().length)
          .map((n) => {
            const s = getComputedStyle(n)
            return [s.color, s.backgroundColor, s.borderColor]
          }),
      )
    const before = await paint()
    await page.evaluate((names) => {
      for (const name of names)
        document.documentElement.style.setProperty(name, 'var(--canvas)')
    }, accents)
    // No new failing pair is allowed, including a changed paint on the marked root itself.
    assert.deepEqual(
      await paint(),
      before,
      `hostile accent changed ${selector}`,
    )
    const hostile = await scan()
    assert.deepEqual(
      hostile,
      baseline,
      `new failing contrast pair: ${selector}`,
    )
    // Remove the real markers (including nested ones) as the plain control, never add a probe.
    await root.evaluate((el) => {
      for (const n of [el, ...el.querySelectorAll('[data-trust-chrome]')]) {
        if (n.hasAttribute('data-trust-chrome')) {
          n.setAttribute('data-at-trust-control', '')
          n.removeAttribute('data-trust-chrome')
        }
      }
    })
    const plain = await scan()
    await page.evaluate((names) => {
      for (const n of document.querySelectorAll('[data-at-trust-control]')) {
        n.setAttribute('data-trust-chrome', '')
        n.removeAttribute('data-at-trust-control')
      }
      for (const name of names)
        document.documentElement.style.removeProperty(name)
    }, accents)
    console.log(
      `  trust ${selector}: baseline=${baseline.length}, hostile=${hostile.length}, plain=${plain.length}`,
    )
    return { selector, baseline, hostile, plain }
  }
  const results = [await measure(card)]
  await page
    .locator(card)
    .getByRole('button', { name: 'Deny', exact: true })
    .click()
  await page.getByRole('dialog').waitFor({ state: 'visible' })
  results.push(await measure('[role="dialog"]'))
  await page.keyboard.press('Escape')
  await page.goto(origin + '/sessions?tab=table')
  await page.addStyleTag({
    content:
      '*, *::before, *::after { transition: none !important; animation: none !important; }',
  })
  // The table's provenance chip is a real painted span, not a token probe.
  const chip = page
    .getByRole('row')
    .filter({ has: page.locator('[data-address="run:trust-run"]') })
    .locator('span[data-trust-chrome][title]')
    .first()
  await chip.waitFor({ state: 'visible' })
  await chip.evaluate((el) => el.setAttribute('data-at-provenance', ''))
  results.push(await measure('[data-at-provenance]'))
  assert(
    results.some((r) => r.plain.some((p) => !r.baseline.includes(p))),
    'plain control must introduce a failing contrast pair',
  )
  return results
}
