// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the `?tab=` seam on the Claude Code governance view.
//
// The `?tab=` seam itself: with the tab as plain local state seeded at 'drift', a link here
// landed on a different surface. The panels are stubbed: what is under test is which tab a
// URL selects, not what any panel renders.
//
// ⚠ — THIS COMMENT USED TO SAY the access map links a `policy` edge straight here, and
// production now does the OPPOSITE: a bare `policy` signal is a generic declared permit
// (GitHub, Vault, an IdP, a deployment) and the map deliberately offers NO target for it
// (access-map/authority.ts). The seam is still worth having and still worth pinning — it is
// a general deep-link contract — but its stated cause was stale, which is the same class of
// defect this session exists to remove.
//
// THE SPY IS CAPTURED. `useNavigate: () => vi.fn()` minted a fresh mock per call, so
// the URL write was unobservable: deleting it from setTab (claude-policy-view.tsx) left the
// whole web suite — 164 files, 1644 tests — green.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/recordings/recording-notice', () => ({
  RecordingNotice: () => null,
}))
vi.mock('./drift', () => ({ DriftView: () => <div>DriftView mounted</div> }))
vi.mock('./cedar-opa-view', () => ({
  CedarOpaView: () => <div>CedarOpaView mounted</div>,
}))
vi.mock('./policy-authoring-panel', () => ({
  PolicyAuthoringPanel: ({ surface }: { surface: string }) => (
    <div>PolicyAuthoringPanel {surface} mounted</div>
  ),
}))
vi.mock('./managed-agents-hitl', () => ({
  ManagedAgentsHitl: () => <div>ManagedAgentsHitl mounted</div>,
}))

import ClaudePolicyView from './claude-policy-view'

function renderAt(search: string) {
  window.history.replaceState({}, '', `/claude-policy${search}`)
  return render(<ClaudePolicyView />)
}

beforeEach(() => {
  window.history.replaceState({}, '', '/claude-policy')
  navigate.mockClear()
})

describe('ClaudePolicyView — ?tab= deep link', () => {
  it('opens policy-as-code for ?tab=policy-as-code', () => {
    renderAt('?tab=policy-as-code')
    expect(screen.getByText('CedarOpaView mounted')).toBeInTheDocument()
    expect(screen.queryByText('DriftView mounted')).toBeNull()
  })

  it('falls back to drift with no tab parameter', () => {
    // Non-firing direction: a seam honouring any value would pass the case above and
    // break every plain visit to /claude-policy.
    renderAt('')
    expect(screen.getByText('DriftView mounted')).toBeInTheDocument()
  })

  it('falls back to drift for a tab that does not exist', () => {
    renderAt('?tab=nope')
    expect(screen.getByText('DriftView mounted')).toBeInTheDocument()
  })
})

describe('ClaudePolicyView — the URL follows a manual tab change', () => {
  it('writes the new tab, REPLACING the history entry rather than pushing one', async () => {
    const user = userEvent.setup()
    renderAt('')
    await user.click(screen.getByRole('tab', { name: /policy.as.code/i }))

    expect(navigate).toHaveBeenCalledTimes(1)
    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
      replace: boolean
    }
    expect(arg.replace).toBe(true)
    expect(arg.search({})).toEqual({ tab: 'policy-as-code' })
  })

  it('PRESERVES the parameters already on the URL instead of clearing them', async () => {
    const user = userEvent.setup()
    renderAt('?tab=drift')
    await user.click(screen.getByRole('tab', { name: /policy.as.code/i }))

    const arg = navigate.mock.calls[0]![0] as {
      search: (p: Record<string, unknown>) => Record<string, unknown>
    }
    expect(arg.search({ surface: 'managed', tab: 'drift' })).toEqual({
      surface: 'managed',
      tab: 'policy-as-code',
    })
  })

  it('does NOT navigate on the initial deep-linked render', () => {
    // Non-firing direction: writing the URL while reading it would fight the caller's own
    // navigation and could loop.
    renderAt('?tab=policy-as-code')
    expect(navigate).not.toHaveBeenCalled()
  })
})
