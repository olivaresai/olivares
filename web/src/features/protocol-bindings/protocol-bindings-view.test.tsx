// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor } from '@/test/intel'
import { useWorkspaceStore } from '@/stores/workspace'
import * as api from './api'
import { ProtocolBindingsView } from './protocol-bindings-view'
import type { ProtocolBinding, ProtocolBindingSpec } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-1',
    can: () => true,
  }),
}))

const spec = {
  id: 'spec-1',
  binding_key: 'peer-work',
  generation: 2,
  protocol: 'a2a',
  protocol_version: '1.0.1',
  peer_authority: 'peer.example',
  state: 'draft',
  validation: { verdict: 'UNKNOWN', code: 'composer_unverified' },
} as ProtocolBindingSpec

const binding = {
  id: 'binding-1',
  protocol: 'a2a',
  protocol_version: '1.0.1',
  peer_authority: 'peer.example',
  external_kind: 'task',
  external_id: 'remote-task-1',
  synthetic_sid: 'sid-1',
  local_state: 'active',
  remote_state: 'working',
  observation_verdict: 'UNKNOWN',
  terminal: false,
} as ProtocolBinding

beforeEach(() => {
  vi.restoreAllMocks()
  useWorkspaceStore.getState().clear()
})

describe('workspace-scoped protocol composer surface', () => {
  it('does not query a tenant-wide fallback when no workspace is selected', () => {
    const listSpecs = vi.spyOn(api, 'listProtocolBindingSpecs')
    const listBindings = vi.spyOn(api, 'listProtocolBindings')
    renderIntel(<ProtocolBindingsView />)
    expect(screen.getByText('Select a workspace')).toBeInTheDocument()
    expect(listSpecs).not.toHaveBeenCalled()
    expect(listBindings).not.toHaveBeenCalled()
  })

  it('lists specifications and instances from the exact selected workspace', async () => {
    useWorkspaceStore
      .getState()
      .setActiveWorkspace('workspace-1', 'Production agents')
    const listSpecs = vi
      .spyOn(api, 'listProtocolBindingSpecs')
      .mockResolvedValue({ items: [spec], has_more: false })
    const listBindings = vi
      .spyOn(api, 'listProtocolBindings')
      .mockResolvedValue({ items: [binding], has_more: false })
    renderIntel(<ProtocolBindingsView />)

    expect(await screen.findByText('peer-work')).toBeInTheDocument()
    expect(listSpecs).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_id: 'workspace-1' }),
      { tenant: 'tenant-1' },
      expect.any(AbortSignal),
    )
    expect(listBindings).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_id: 'workspace-1' }),
      { tenant: 'tenant-1' },
      expect.any(AbortSignal),
    )
    await userEvent.click(screen.getByRole('tab', { name: 'Instances' }))
    expect(await screen.findByText('task:remote-task-1')).toBeInTheDocument()
  })

  it('loads an unfiltered active-spec catalog only when the composer opens', async () => {
    useWorkspaceStore
      .getState()
      .setActiveWorkspace('workspace-1', 'Production agents')
    const listSpecs = vi
      .spyOn(api, 'listProtocolBindingSpecs')
      .mockResolvedValue({ items: [], has_more: false })
    vi.spyOn(api, 'listProtocolBindings').mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderIntel(<ProtocolBindingsView />)
    await screen.findByText('No binding specifications')
    expect(
      listSpecs.mock.calls.some(([params]) => params.state === 'active'),
    ).toBe(false)

    await userEvent.click(screen.getByRole('button', { name: 'New draft' }))
    expect(
      await screen.findByRole('dialog', {
        name: 'Compose protocol binding draft',
      }),
    ).toBeInTheDocument()
    await waitFor(() =>
      expect(
        listSpecs.mock.calls.some(
          ([params]) =>
            params.workspace_id === 'workspace-1' &&
            params.state === 'active' &&
            params.limit === 200 &&
            params.protocol === undefined,
        ),
      ).toBe(true),
    )
  })
})
