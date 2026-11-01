// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import type { RunDTO } from '../src/features/agentops/types'
import type { ApprovalDTO } from '../src/features/governance/types'
import { fixtureFor } from './fixtures'

// Synthetic API state; the Go integration test separately crosses the real
// approval service, immutable trail and Codex reply codec.
const run: RunDTO = {
  run_ref: 'scope-run',
  name: 'Review project files',
  provider_driver: 'codex',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  workspace_path: '/srv/project',
  state: 'running',
  pending_approval_ref: 'scope-approval',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
}
const approval: ApprovalDTO = {
  id: 'scope-approval',
  session_ref: 'osn_scope',
  action: 'sessions.provider.approval',
  subject_kind: 'session_run',
  subject_ref: 'scope-run',
  requested_by: 'session:osn_scope',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  review: { tool: 'Command', text: 'ls -la' },
  reason: 'List the project files.',
  escalated: false,
}

for (const [surface, path, region] of [
  ['queue', '/permissions?tab=approvals', '[data-slot="approval-panel"]'],
  [
    'conversation',
    '/sessions?session=run%3Ascope-run',
    '[data-testid="conversation-approval"]',
  ],
] as const) {
  test(`${surface}: session approval is keyboard accessible and sends the selected scope`, async ({
    page,
    baseURL,
  }, info) => {
    await page.setViewportSize({ width: 1280, height: 900 })
    let allowed = true
    let approved = false
    let decision: unknown
    const current = () => ({
      ...approval,
      session_scope_allowed: allowed,
      status: approved ? 'approved' : 'pending',
      approve_count: approved ? 1 : 0,
    })
    await page.addInitScript(() => {
      localStorage.setItem('olivares.lang', 'en')
      localStorage.setItem('olivares.theme', 'light')
      localStorage.setItem(
        'olivares.tenant',
        JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
      )
    })
    await page.route('**/*', async (route) => {
      const url = new URL(route.request().url())
      if (url.origin !== baseURL) return route.abort()
      const p = url.pathname
      if (!p.startsWith('/v1/')) return route.continue()
      if (p === '/v1/m/governance/approvals/scope-approval/decisions') {
        if (route.request().method() === 'POST') {
          decision = route.request().postDataJSON()
          approved = true
          return route.fulfill({ json: current() })
        }
        return route.fulfill({
          json: {
            items: approved
              ? [
                  {
                    decision: 'approve',
                    scope: 'session',
                    decider: 'user:admin@demo',
                  },
                ]
              : [],
            has_more: false,
          },
        })
      }
      if (p === '/v1/m/governance/approvals')
        return route.fulfill({ json: { items: [current()], has_more: false } })
      if (p === '/v1/m/governance/approvals/scope-approval')
        return route.fulfill({ json: current() })
      if (p === '/v1/m/sessions/runs')
        return route.fulfill({ json: { items: [run], has_more: false } })
      if (p === '/v1/m/sessions/runs/scope-run')
        return route.fulfill({ json: run })
      if (p === '/v1/m/sessions/live')
        return route.fulfill({ json: { items: [], has_more: false } })
      if (p.endsWith('/stream'))
        return route.fulfill({
          contentType: 'text/event-stream',
          body: ': connected\n\n',
        })
      return route.fulfill({
        json: fixtureFor(p) ?? { items: [], has_more: false },
      })
    })
    await page.goto(path)
    const button = page.getByRole('button', {
      name: 'Approve for this session',
      exact: true,
    })
    await expect(button).toBeVisible()
    expect(
      (await new AxeBuilder({ page }).include(region).analyze()).violations,
    ).toEqual([])
    await page.screenshot({
      path: info.outputPath(`${surface}-1280-light.png`),
      fullPage: true,
    })
    await button.focus()
    await button.press('Enter')
    await expect
      .poll(() => decision)
      .toEqual({ decision: 'approve', scope: 'session' })
    await expect(button).toHaveCount(0)

    allowed = false
    approved = false
    decision = undefined
    await page.reload()
    await expect(
      page.getByRole('button', { name: 'Approve once', exact: true }),
    ).toBeVisible()
    await expect(button).toHaveCount(0)
    expect(decision).toBeUndefined()
  })
}
