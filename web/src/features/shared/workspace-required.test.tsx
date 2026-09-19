// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SIX ROUTES TOLD THE READER TO USE A CONTROL THAT WAS NOT ON THE SCREEN.
//
// The five communications doors and the protocol bindings make no request until a
// workspace is chosen — correct, and deliberate. Their empty state said "Choose one in
// the workspace switcher", and the switcher returns null whenever the tenant holds
// fewer than two workspaces. The seeded estate holds exactly one, so the instruction
// named a control the reader could not find.
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'demo', can: () => true }),
}))

const api = vi.hoisted(() => ({ listWorkspaces: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...api } }
})

import { useWorkspaceStore } from '@/stores/workspace'
import { WorkspaceRequiredState } from './workspace-required'
import './i18n'

const state = () =>
  screen
    .getByText('Select a workspace')
    .closest('[data-slot="empty-state"]') as HTMLElement

beforeEach(() => {
  vi.clearAllMocks()
  useWorkspaceStore.getState().clear()
})

describe('WorkspaceRequiredState', () => {
  it('offers the only workspace by name, and selecting it is the next action', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [{ id: 'ws-1', name: 'Demo Estate', slug: 'demo' }],
      has_more: false,
    })
    const user = userEvent.setup()
    renderIntel(
      <WorkspaceRequiredState
        title="Select a workspace"
        description="Communications require an explicit workspace."
      />,
    )
    const boton = await within(state()).findByRole('button', {
      name: 'Use Demo Estate',
    })
    await user.click(boton)
    await waitFor(() =>
      expect(useWorkspaceStore.getState().activeWorkspace).toBe('ws-1'),
    )
    expect(useWorkspaceStore.getState().activeWorkspaceName).toBe('Demo Estate')
  })

  it('offers a picker when the tenant holds several', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [
        { id: 'ws-1', name: 'Demo Estate', slug: 'demo' },
        { id: 'ws-2', name: 'Platform', slug: 'platform' },
      ],
      has_more: false,
    })
    renderIntel(
      <WorkspaceRequiredState
        title="Select a workspace"
        description="Communications require an explicit workspace."
      />,
    )
    expect(
      await within(state()).findByRole('combobox', {
        name: 'Choose a workspace',
      }),
    ).toBeInTheDocument()
  })

  it('draws no control at all when there is nothing to choose', async () => {
    // A dead button is worse than none: the sentence still stands on its own.
    api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
    renderIntel(
      <WorkspaceRequiredState
        title="Select a workspace"
        description="Communications require an explicit workspace."
      />,
    )
    await waitFor(() => expect(api.listWorkspaces).toHaveBeenCalled())
    expect(within(state()).queryByRole('button')).toBeNull()
    expect(within(state()).queryByRole('combobox')).toBeNull()
  })
})
