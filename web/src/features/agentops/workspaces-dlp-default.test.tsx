// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// Register workspace keeps the engine's folder default (dlp_mode off,
// modules/sessions/workspace.go), the same one New session registers a typed
// folder with (agentops/session-launch.test.ts). A classified read-write folder makes
// every edit session wait for approval, so the dialog must not pick one.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    principal: { aal: 1 },
  }),
}))

const api = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  createWorkspace: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { WorkspacesPanel } from './workspaces-panel'

describe('Register workspace DLP default', () => {
  it('registers a folder with only its root path at the engine default, off, also after a reset from label', async () => {
    api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
    api.createWorkspace.mockImplementation((b) =>
      Promise.resolve({ workspace_ref: 'ws_1', state: 'active', ...b }),
    )
    const user = userEvent.setup()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <WorkspacesPanel />
      </QueryClientProvider>,
    )
    // The second registration picks label; the third reopens the dialog after
    // that reset and must be back at the default.
    for (const [root, pick] of [
      ['/srv/w1', null],
      ['/srv/w2', /label & audit/i],
      ['/srv/w3', null],
    ] as const) {
      await user.click(
        await screen.findByRole('button', { name: /register workspace/i }),
      )
      await user.type(await screen.findByLabelText(/^root path$/i), root)
      if (pick) {
        await user.click(screen.getByRole('combobox', { name: /^dlp mode$/i }))
        await user.click(await screen.findByRole('option', { name: pick }))
      }
      await user.click(screen.getByRole('button', { name: /^register$/i }))
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      )
    }

    expect(api.createWorkspace.mock.calls.map((c) => c[0])).toEqual([
      expect.objectContaining({ root_path: '/srv/w1', dlp_mode: 'off' }),
      expect.objectContaining({ root_path: '/srv/w2', dlp_mode: 'label' }),
      expect.objectContaining({ root_path: '/srv/w3', dlp_mode: 'off' }),
    ])
  })
})
