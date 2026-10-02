// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { useModulesStore } from '@/stores/modules'

const auth = vi.hoisted(() => ({ isSuperadmin: false }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...auth, activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
  Toaster: () => null,
}))

import { ModuleGate, QueryErrorState } from './query-error-state'

afterEach(() => {
  auth.isSuperadmin = false
  useModulesStore.getState().setOff([])
})

// 08b's real answers (EU/captures/refresh08b): a group forbid on agent:read, and a read
// of the dormant models module.
const forbidden = new ApiError(403, 'forbidden', 'forbidden')
const moduleOff = new ApiError(
  404,
  'module_not_enabled',
  'The models module is not enabled on this node. An administrator can enable it in the module settings.',
)

describe('QueryErrorState: one mapping from a failed read to what a panel says (EU18, EU20)', () => {
  it('a 403 says who can grant it and offers no Retry', () => {
    const retry = vi.fn()
    renderIntel(
      <QueryErrorState error={forbidden} subject="agents" retry={retry} />,
    )
    expect(
      screen.getByText('You do not have access to agents.'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Your roles or a group policy do not allow it. An owner or administrator of this organization can grant it.',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
    expect(screen.queryByText('Something went wrong')).toBeNull()
  })

  it('a 403 that names the policy denying it says which', () => {
    renderIntel(
      <QueryErrorState
        error={
          new ApiError(403, 'forbidden', 'forbidden', undefined, {
            policy_name: 'Contractors',
          })
        }
      />,
    )
    expect(screen.getByText('You do not have access to this.')).toBeVisible()
    expect(
      screen.getByText(
        'The policy “Contractors” denies it. An owner or administrator of this organization can change it.',
      ),
    ).toBeInTheDocument()
  })

  it('module_not_enabled says the module is off, with the administrator’s enable action and no Retry', async () => {
    auth.isSuperadmin = true
    renderIntel(
      <QueryErrorState error={moduleOff} module="models" retry={vi.fn()} />,
    )
    expect(
      screen.getByText('Models is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(
      await screen.findByRole('button', { name: 'Turn on Models' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
  })

  it('a person who cannot choose modules reads the line without the action', () => {
    renderIntel(<QueryErrorState error={moduleOff} module="models" />)
    expect(
      screen.getByText('Models is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('a capability this build does not include (501) is said calmly, without Retry (09 signing)', () => {
    // R1 09 Community, GET /v1/m/reporting/signing, as answered.
    renderIntel(
      <QueryErrorState
        error={
          new ApiError(
            501,
            'Not Implemented',
            'evidence bundle signing is an enterprise capability and is not wired in this build',
          )
        }
        subject="report signing"
        retry={vi.fn()}
      />,
    )
    expect(
      screen.getByText('This edition does not include report signing.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
    expect(screen.queryByText('Something went wrong')).toBeNull()
  })

  it('a 5xx is the failure, with Retry and its request id', () => {
    renderIntel(
      <QueryErrorState
        error={new ApiError(500, 'internal', 'boom', 'req-08b-1')}
        retry={vi.fn()}
      />,
    )
    expect(screen.getByText('Something went wrong')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    expect(screen.getByText('req-08b-1')).toBeInTheDocument()
  })

  it('a network failure says the engine could not be reached, with Retry', () => {
    renderIntel(
      <QueryErrorState error={new NetworkError('down')} retry={vi.fn()} />,
    )
    expect(screen.getByText('Control plane unreachable')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
  })
})

describe('ModuleGate: a panel that reads another module (EU18)', () => {
  it('does not mount its reads or its New while the module is off', () => {
    useModulesStore.getState().setOff(['models'])
    renderIntel(
      <ModuleGate module="models">
        <button type="button">New model group</button>
      </ModuleGate>,
    )
    expect(
      screen.getByText('Models is not enabled on this installation.'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New model group' })).toBeNull()
  })

  it('mounts the panel where the module runs', () => {
    renderIntel(
      <ModuleGate module="models">
        <button type="button">New model group</button>
      </ModuleGate>,
    )
    expect(
      screen.getByRole('button', { name: 'New model group' }),
    ).toBeInTheDocument()
  })
})
