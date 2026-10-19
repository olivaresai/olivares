// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: {
    listWorkspaces: async () => ({
      items: [
        {
          workspace_ref: 'ws_1',
          name: 'Research',
          root_path: '/srv/research',
          mount_mode: 'rw',
          state: 'active',
        },
      ],
      has_more: false,
    }),
    getWorkspace: async () => ({ read_only_folders: [] }),
    listFiles: async () => ({ entries: [], has_more: false }),
  },
}))
import { WorkspacesPanel } from './workspaces-panel'

it('offers Wiki beside Files when a registered workspace opens', async () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <WorkspacesPanel />
    </QueryClientProvider>,
  )
  await userEvent
    .setup()
    .click(await screen.findByRole('button', { name: 'Browse files' }))
  expect(await screen.findByRole('tab', { name: 'Wiki' })).toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'Files' })).toHaveAttribute(
    'aria-selected',
    'true',
  )
})
