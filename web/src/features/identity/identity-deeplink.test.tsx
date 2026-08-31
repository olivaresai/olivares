// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the `?tab=` seam on Identity & NHI.
//
// The access map sends an operator here to review the origin identity of an observed access,
// and the roster lives on the `inventory` tab. With the tab as plain local state seeded at
// `federation`, that link landed on a different surface — the same "leads nowhere" this work
// removes. The tabs are stubbed: what is under test is which tab a URL selects.
//
// and the seam only READ the parameter. Console and Claude policy write the new tab
// back with `replace: true`; this one did not, and nothing said so, because the suite tested
// only the landing. A shared deep link therefore reopened the roster whatever tab was on
// screen. The write is here now, and so is the case that turns red without it.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))

vi.mock('@/features/recordings/recording-notice', () => ({
  RecordingNotice: () => null,
}))
vi.mock('./federation', () => ({
  FederationTab: () => <div>FederationTab mounted</div>,
}))
vi.mock('./nhi-roster', () => ({
  NhiRosterTab: () => <div>NhiRosterTab mounted</div>,
}))
vi.mock('./nhi-lifecycle', () => ({
  NhiLifecycleTab: () => <div>NhiLifecycleTab mounted</div>,
}))
vi.mock('./mcp-auth', () => ({ McpAuthTab: () => <div>McpAuthTab mounted</div> }))
vi.mock('./wif/wif-graph', () => ({
  WifGraphTab: () => <div>WifGraphTab mounted</div>,
}))
vi.mock('./posture', () => ({ PostureTab: () => <div>PostureTab mounted</div> }))
vi.mock('./privileged-login', () => ({
  PrivilegedLoginTab: () => <div>PrivilegedLoginTab mounted</div>,
}))

import IdentityView from './identity-view'

function renderAt(search: string) {
  window.history.replaceState({}, '', `/identity${search}`)
  return render(<IdentityView />)
}

beforeEach(() => {
  window.history.replaceState({}, '', '/identity')
  navigate.mockClear()
})

describe('IdentityView — ?tab= deep link', () => {
  it('opens the NHI roster when the access map links to ?tab=inventory', () => {
    renderAt('?tab=inventory')
    expect(screen.getByText('NhiRosterTab mounted')).toBeInTheDocument()
    expect(screen.queryByText('FederationTab mounted')).toBeNull()
  })

  it('falls back to federation with no tab parameter', () => {
    // Non-firing direction: a seam honouring any value would pass the case above and
    // change what every plain visit to /identity shows.
    renderAt('')
    expect(screen.getByText('FederationTab mounted')).toBeInTheDocument()
  })

  it('falls back to federation for a tab that does not exist', () => {
    renderAt('?tab=nope')
    expect(screen.getByText('FederationTab mounted')).toBeInTheDocument()
  })
})

describe('IdentityView — the URL follows a manual tab change', () => {
  it('writes the new tab, REPLACING the history entry rather than pushing one', async () => {
    const user = userEvent.setup()
    renderAt('')
    await user.click(screen.getByRole('tab', { name: /inventory|roster/i }))

    expect(navigate).toHaveBeenCalledTimes(1)
    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
      replace: boolean
    }
    expect(arg.replace).toBe(true)
    expect(arg.search({})).toEqual({ tab: 'inventory' })
  })

  it('PRESERVES the parameters already on the URL instead of clearing them', async () => {
    const user = userEvent.setup()
    renderAt('?tab=federation')
    await user.click(screen.getByRole('tab', { name: /inventory|roster/i }))

    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
    }
    expect(arg.search({ q: 'svc', tab: 'federation' })).toEqual({
      q: 'svc',
      tab: 'inventory',
    })
  })

  it('does NOT navigate on the initial deep-linked render', () => {
    // Non-firing direction: writing while reading would fight the caller's own navigation.
    renderAt('?tab=inventory')
    expect(navigate).not.toHaveBeenCalled()
  })
})
