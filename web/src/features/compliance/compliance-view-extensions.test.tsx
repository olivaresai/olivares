// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The shared Compliance container carries the Community record tabs; Business
// supplies a separate view through the panel seam. Exercise Community here in both
// assembled editions, including the existing optional record-tab extension.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen, userEvent } from '@/test/intel'
import '@/features/_intel'
import type { TabExtension } from '@/features/panels'

let allowed = (_permission: string) => true
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, can: (p: string) => allowed(p) }),
}))

const panels = vi.hoisted(() => ({ complianceTabs: [] as TabExtension[] }))
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      ...actual.PANEL_EXTENSIONS,
      complianceView: undefined,
      get complianceTabs() {
        return panels.complianceTabs
      },
    },
  }
})

const { ComplianceView } = await import('./compliance-view')

const COMMUNITY_TABS = [
  'Evidence',
  'Risk',
  'Residency',
  'Retention',
  'Legal holds',
  'Erasure & DSAR',
]

const fixtureTab: TabExtension = {
  id: 'fixture-tab',
  label: () => 'Fixture tab',
  Component: () => <p>fixture panel</p>,
}

beforeEach(() => {
  allowed = () => true
  vi.stubGlobal(
    'fetch',
    vi.fn((url: unknown) => {
      const path = new URL(String(url), 'https://console.invalid').pathname
      const known = path === '/v1/m/compliance/evidence'
      const body = known
        ? { items: [], disclaimer: 'evidence' }
        : { error: { message: `unrouted ${path}` } }
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: known ? 200 : 404,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    }),
  )
})
afterEach(() => {
  panels.complianceTabs = []
  vi.unstubAllGlobals()
})

async function tabNames() {
  await screen.findByRole('tab', { name: 'Evidence' })
  return screen.getAllByRole('tab').map((tab) => tab.textContent)
}

describe('ComplianceView — extension tabs', () => {
  it('offers only the Community tabs in the default console', async () => {
    renderIntel(<ComplianceView />)
    expect(await tabNames()).toEqual(COMMUNITY_TABS)
    expect(
      vi
        .mocked(fetch)
        .mock.calls.some(([url]) =>
          String(url).includes('/compliance/summary'),
        ),
    ).toBe(false)
  })

  it('mounts an extension tab after the records tabs and paints its panel on click', async () => {
    panels.complianceTabs = [fixtureTab]
    const user = userEvent.setup()
    renderIntel(<ComplianceView />)

    const names = await tabNames()
    expect(names.indexOf('Fixture tab')).toBe(COMMUNITY_TABS.length)
    await user.click(screen.getByRole('tab', { name: 'Fixture tab' }))
    expect(await screen.findByText('fixture panel')).toBeInTheDocument()
  })

  it('does not offer an extension tab without its permission', async () => {
    allowed = (p) => p !== 'fixture:read'
    panels.complianceTabs = [{ ...fixtureTab, permission: 'fixture:read' }]
    renderIntel(<ComplianceView />)
    expect(await tabNames()).toEqual(COMMUNITY_TABS)
  })
})
