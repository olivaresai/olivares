// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import AxeBuilder from '@axe-core/playwright'
import { expect, test } from '@playwright/test'
import type { RunDTO } from '../src/features/agentops/types'
import type { ApprovalDTO } from '../src/features/governance/types'
import { fixtureFor } from './fixtures'

// Synthetic API state; the Go integration test separately crosses the real
// approval service, immutable trail and Claude hook reply codec.
const run: RunDTO = {
  run_ref: 'scope-run',
  name: 'Review project files',
  provider_driver: 'claude',
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
  action: 'claude.tool.use',
  subject_kind: 'claude.tool',
  subject_ref: 'scope-run',
  requested_by: 'session:osn_scope',
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  review: { tool: 'Bash', text: 'npm install' },
  reason: 'Install the project dependencies.',
  escalated: false,
}

for (const width of [1280, 390]) {
  for (const theme of ['light', 'dark']) {
    test(`typed decline ${width} ${theme}`, async ({ page, baseURL }, info) => {
      await page.setViewportSize({ width, height: 900 })
      const allowed = false
      let approved = false
      let decision: unknown
      const current = () => ({
        ...approval,
        session_scope_allowed: allowed,
        status: approved ? 'rejected' : 'pending',
        reject_count: approved ? 1 : 0,
      })
      await page.addInitScript((selectedTheme) => {
        localStorage.setItem('olivares.lang', 'en')
        localStorage.setItem('olivares.theme', selectedTheme)
        localStorage.setItem(
          'olivares.tenant',
          JSON.stringify({ state: { activeTenant: 't-demo' }, version: 0 }),
        )
      }, theme)
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
                      decision: 'reject',
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
          return route.fulfill({
            json: { items: [current()], has_more: false },
          })
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
      await page.goto('/sessions?session=run%3Ascope-run')
      const card = page.getByTestId('conversation-approval')
      await expect(
        card.getByText('Install the project dependencies.'),
      ).toBeVisible()
      const input = page.getByTestId('launcher-input')
      await expect(input).toBeEnabled()
      await input.fill('use pnpm instead')
      await expect(
        page.getByRole('button', { name: 'Decline and send this' }),
      ).toBeEnabled()
      expect(
        (
          await new AxeBuilder({ page })
            .include('[data-testid="conversation-approval"]')
            .include('[data-testid="work-composer"]')
            .analyze()
        ).violations,
      ).toEqual([])
      await page.screenshot({
        path: info.outputPath(`card-${width}-${theme}.png`),
        fullPage: true,
      })
      await input.press('Enter')
      await expect
        .poll(() => decision)
        .toEqual({ decision: 'reject', note: 'use pnpm instead' })
      await expect(input).toHaveValue('')
      await page.screenshot({
        path: info.outputPath(`reply-${width}-${theme}.png`),
        fullPage: true,
      })
    })
  }
}
