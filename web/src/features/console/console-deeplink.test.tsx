// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the `?tab=` seam on the control console.
//
// This console owns the source→scope bindings, so it is where the access map sends an
// operator who has just seen a least-privilege finding. The tab used to be local state
// seeded at 'people': the link arrived and the operator landed on the wrong surface, which
// is the same "leads nowhere" the map itself suffered from.
//
// ⚠ — the tab names below are the SEAM's, not the map's routing. The map sends a
// `scoped_grant` edge to ?tab=bindings (NOT to roles — that is the unrelated RBAC
// system), and sends a pending finding NOWHERE at all, because the engine does not say
// which of its three conditions is unresolved.
//
// The eleven tabs are stubbed on purpose — what is under test is WHICH tab a URL selects,
// not what any tab renders. Mounting the real ones would drag in their API mocks and make
// this case fail for reasons that have nothing to do with the seam.
//
// THE SPY IS CAPTURED, and that is the whole point of this edit. This file used to
// mock `useNavigate: () => vi.fn()`, minting a FRESH mock on every call, so nothing the view
// navigated to could ever be observed. Measured: deleting the URL write from setTab
// (console-view.tsx) left the ENTIRE web suite — 164 files, 1644 tests — green. The seam was
// documented, shipped, and pinned by nothing.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))

// Each factory is written out in full: vi.mock is hoisted above every top-level
// binding, so a shared helper is not in scope by the time it runs.
vi.mock('./people-tab', () => ({
  PeopleTab: () => <div>PeopleTab mounted</div>,
}))
vi.mock('./agents-tab', () => ({
  AgentsTab: () => <div>AgentsTab mounted</div>,
}))
vi.mock('./sso-tab', () => ({ SSOTab: () => <div>SSOTab mounted</div> }))
vi.mock('./scopes-tab', () => ({
  ScopesTab: () => <div>ScopesTab mounted</div>,
}))
vi.mock('./roles-tab', () => ({ RolesTab: () => <div>RolesTab mounted</div> }))
vi.mock('./bindings-tab', () => ({
  BindingsTab: () => <div>BindingsTab mounted</div>,
}))
vi.mock('./secrets-tab', () => ({
  SecretsTab: () => <div>SecretsTab mounted</div>,
}))
vi.mock('./connectors-tab', () => ({
  ConnectorsTab: () => <div>ConnectorsTab mounted</div>,
}))
vi.mock('./workspace-connectors-tab', () => ({
  WorkspaceConnectorsTab: () => <div>WorkspaceConnectorsTab mounted</div>,
}))
vi.mock('./api-keys-tab', () => ({
  ApiKeysTab: () => <div>ApiKeysTab mounted</div>,
}))
vi.mock('./license-tab', () => ({
  LicenseTab: () => <div>LicenseTab mounted</div>,
}))

import ConsoleView from './console-view'

function renderAt(search: string) {
  window.history.replaceState({}, '', `/console${search}`)
  return render(<ConsoleView />)
}

beforeEach(() => {
  window.history.replaceState({}, '', '/console')
  navigate.mockClear()
})

describe('ConsoleView — ?tab= deep link', () => {
  it('opens the RBAC roles surface for ?tab=roles', () => {
    renderAt('?tab=roles')
    expect(screen.getByText('RolesTab mounted')).toBeInTheDocument()
    expect(screen.queryByText('PeopleTab mounted')).toBeNull()
  })

  it('opens the source-bindings surface the access map actually links to (?tab=bindings)', () => {
    renderAt('?tab=bindings')
    expect(screen.getByText('BindingsTab mounted')).toBeInTheDocument()
  })

  it('falls back to People with no tab parameter', () => {
    // Non-firing direction: a seam that honoured ANY value would pass the cases above
    // while breaking every plain visit to /console.
    renderAt('')
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
  })

  it('falls back to People for a tab that does not exist', () => {
    // An unknown value must not render an empty shell — the operator gets the default
    // surface, not a blank console.
    renderAt('?tab=not-a-tab')
    expect(screen.getByText('PeopleTab mounted')).toBeInTheDocument()
  })
})

describe('ConsoleView — the URL follows a manual tab change', () => {
  it('writes the new tab, REPLACING the history entry rather than pushing one', async () => {
    // `replace: true` is not decoration: the console has eleven tabs, and pushing an entry
    // per click turns Back into a walk through them instead of a way out of the view.
    const user = userEvent.setup()
    renderAt('')
    await user.click(screen.getByRole('tab', { name: /bindings/i }))

    expect(navigate).toHaveBeenCalledTimes(1)
    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
      replace: boolean
    }
    expect(arg.replace).toBe(true)
    expect(arg.search({})).toEqual({ tab: 'bindings' })
  })

  it('PRESERVES the parameters already on the URL instead of clearing them', async () => {
    // The updater is a function for exactly this reason. Returning a bare `{tab}` would
    // silently drop every other consumer's query state on the first tab click.
    const user = userEvent.setup()
    renderAt('?tab=roles')
    await user.click(screen.getByRole('tab', { name: /secrets/i }))

    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
    }
    expect(arg.search({ focus: 'svc_pool', tab: 'roles' })).toEqual({
      focus: 'svc_pool',
      tab: 'secrets',
    })
  })

  it('does NOT navigate on the initial deep-linked render', async () => {
    // Non-firing direction: a seam that wrote the URL while reading it would fight the
    // caller's own navigation and could loop.
    renderAt('?tab=roles')
    expect(navigate).not.toHaveBeenCalled()
  })
})
