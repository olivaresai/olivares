// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Visual e2e for the intelligence views. Drives the real SPA (shell + router +
// RBAC + lazy Suspense + themed charts) against a FULLY MOCKED API: a seeded session
// + page.route interception that answers every /v1 call with the views' own fixtures.
// No backend, no real data — so each view renders deterministically in light AND
// dark, and we screenshot all nine. Run: pnpm exec playwright test --config playwright.intel.config.ts
import { expect, test } from '@playwright/test'
// Relative imports (Playwright's loader does not resolve the Vite `@` alias). The
// fixtures are plain data modules (they import only their own ./types, erased at
// build) so pulling them into the test runner is safe and dependency-free.
import * as finops from '../src/features/finops/fixtures'
import * as models from '../src/features/models/fixtures'
import * as evals from '../src/features/evals/fixtures'
import * as sandbox from '../src/features/sandbox/fixtures'
import * as security from '../src/features/security/fixtures'
import * as redteam from '../src/features/redteam/fixtures'
import * as compliance from '../src/features/compliance/fixtures'
import * as orchestration from '../src/features/orchestration/fixtures'
import * as voice from '../src/features/voice/fixtures'

const list = (items: unknown[]) => ({ items, cursor: '', has_more: false })

const SERVER_INFO = {
  version: '0.0.0-e2e',
  engine: 'olivares',
  setup_required: false,
  license: { status: 'active', licensee: 'e2e' },
}
const WHOAMI = {
  kind: 'user',
  user_id: 'u-e2e',
  actor: 'operator@example.com',
  display_name: 'Operator',
  superadmin: true, // → can() passes for every view permission
  grants: [],
}

// Map any /v1 path to a fixture-shaped body. Specific suffixes first; a permissive
// bag is the fallback so an unmapped path never hangs or crashes a view.
function bodyFor(p: string): unknown {
  if (p.endsWith('/v1/server-info')) return SERVER_INFO
  if (p.endsWith('/v1/auth/whoami')) return WHOAMI
  if (p.includes('/v1/auth/')) return {}

  if (p.includes('/m/finops/')) {
    if (p.includes('/spend/summary')) return finops.summaryFixture
    if (p.includes('/spend/trend')) return finops.trendFixture
    if (p.includes('/forecast')) return finops.forecastFixture
    if (p.includes('/recommendations'))
      return { recommendations: finops.recommendationsFixture }
    if (p.includes('/budgets/') && p.endsWith('/status')) {
      const id = p.split('/budgets/')[1]?.split('/')[0] ?? ''
      return (
        finops.budgetStatusFixtures[id] ??
        Object.values(finops.budgetStatusFixtures)[0]
      )
    }
    if (p.endsWith('/budgets')) return list(finops.budgetsFixture)
    if (p.includes('/alerts')) return list(finops.alertsFixture)
    if (p.includes('/spend'))
      return {
        dimension: 'model',
        total_micro_usd: 0,
        buckets: [],
        truncated: false,
      }
  }

  if (p.includes('/m/models/')) {
    if (p.includes('/catalog')) return models.catalogFixture
    if (p.includes('/routing-policies'))
      return list(models.routingPoliciesFixture)
    if (p.includes('/keys')) return list(models.keyRefsFixture)
    if (p.includes('/models')) return list(models.governedModelsFixture)
  }

  if (p.includes('/m/evals/')) {
    if (p.includes('/scorecards')) return list(evals.scorecardsFixture)
    if (p.includes('/suites')) return list(evals.suitesFixture)
    if (p.includes('/results')) return list(evals.caseResultsFixture)
    if (p.includes('/ab')) return evals.abResultFixture
    if (p.includes('/runs')) return list(evals.runsFixture)
  }

  if (p.includes('/m/sandbox/')) {
    if (p.includes('/outputs'))
      return list(Object.values(sandbox.outputsFixture).flat())
    if (p.includes('/scenarios')) return list(sandbox.scenariosFixture)
    if (p.includes('/comparisons')) return list(sandbox.comparisonsFixture)
    if (p.includes('/runs')) return list(sandbox.runsFixture)
  }

  if (p.includes('/m/security/')) {
    if (p.includes('/findings')) return list(security.findingsFixture)
    if (p.includes('/enforcement'))
      return { items: security.enforcementFixture }
    if (p.includes('/anomalies')) return { items: security.anomaliesFixture }
    if (p.includes('/cases/') && p.endsWith('/timeline'))
      return security.caseTimelineFixture
    if (p.includes('/cases')) return list(security.casesFixture)
    if (p.includes('/integrity/verify'))
      return security.integrityVerifiedFixture
  }

  if (p.includes('/m/redteam/')) {
    if (p.includes('/catalog')) return redteam.catalogFixture
    if (p.includes('/results')) return list(redteam.resultsFixture)
    if (p.includes('/targets')) return list(redteam.targetsFixture)
    if (p.includes('/runs')) return list(redteam.runsFixture)
  }

  if (p.includes('/m/compliance/')) {
    if (p.endsWith('/status')) return compliance.statusFixture
    if (p.endsWith('/gaps')) return compliance.gapsFixture
    if (p.endsWith('/frameworks')) return compliance.frameworksFixture
    if (p.includes('/summary')) return compliance.summaryFixture
    if (p.includes('/evidence'))
      return { items: compliance.evidenceFixture, has_more: false }
    if (p.includes('/risk')) return list(compliance.riskFixture)
    if (p.includes('/residency')) return list(compliance.residencyFixture)
  }

  if (p.includes('/m/orchestration/')) {
    if (p.includes('/graph')) return orchestration.graphFixture
    if (p.includes('/flows')) return list(orchestration.flowsFixture)
    if (p.includes('/timeline')) return list(orchestration.timelineFixture)
    if (p.includes('/schedules/') && p.endsWith('/decisions'))
      return list(orchestration.decisionsFixture)
    if (p.includes('/schedules')) return list(orchestration.schedulesFixture)
  }

  if (p.includes('/m/voice/')) {
    if (p.includes('/policies')) return list(voice.policiesFixture)
    if (p.includes('/sessions')) return list(voice.sessionsFixture)
  }

  // Permissive fallback covering the common envelope shapes the views read.
  return {
    items: [],
    has_more: false,
    nodes: [],
    edges: [],
    frameworks: [],
    recommendations: [],
  }
}

const ROUTES = [
  'finops',
  'models',
  'evals',
  'sandbox',
  'security',
  'red-team',
  'compliance',
  'orchestration',
  'voice',
]

test.beforeEach(async ({ context, page }) => {
  // Seed a session so the shell guard treats us as authenticated (whoami is mocked).
  await context.addInitScript(() => {
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({
        state: {
          token: 'olvs_e2e',
          sessionId: 's-e2e',
          expiresAt: '2099-01-01T00:00:00Z',
        },
        version: 0,
      }),
    )
  })
  await page.route('**/v1/**', async (route) => {
    const p = new URL(route.request().url()).pathname
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(bodyFor(p)),
    })
  })
  await page.route('**/healthz', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '{"status":"ok"}',
    }),
  )
})

for (const theme of ['light', 'dark'] as const) {
  test(`intelligence views render in ${theme}`, async ({ page }) => {
    // Set the theme before navigation; the no-FOUC bootstrap applies it pre-paint.
    await page.goto('/')
    await page.evaluate((t) => localStorage.setItem('olivares.theme', t), theme)

    for (const route of ROUTES) {
      await page.goto(`/${route}`)
      // The view mounted: its IntelPage h1 is visible (not the Suspense spinner,
      // not the login redirect, not the route-error boundary).
      await expect(page.locator('html')).toHaveClass(
        theme === 'dark' ? /dark/ : /^(?!.*\bdark\b).*$/,
      )
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible({
        timeout: 15_000,
      })
      expect(page.url()).toContain(`/${route}`)
      await page.screenshot({
        path: `playwright-report/intel/${route}-${theme}.png`,
        fullPage: true,
      })
    }
  })
}
