// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { WorkspaceWiki } from './workspace-wiki'
import type { WorkspaceDTO } from './types'

const state = vi.hoisted(() => ({ denied: new Set<string>(), failed: false }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    principal: { user_id: 'u1' },
    can: (permission: string) => !state.denied.has(permission),
  }),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: {
    statFile: async () => ({ type: 'dir' }),
    readFile: async (_ref: string, path: string) => {
      if (state.failed && path === 'index.md')
        throw new ApiError(500, 'internal', 'read failed')
      return { path, content: '# Wiki', encoding: 'utf-8' }
    },
    listFiles: async () => ({ entries: [], has_more: false }),
    toolsReadiness: async () => ({
      tools: [{ driver: 'claude', ready: true }],
    }),
  },
}))
const workspace: WorkspaceDTO = {
  workspace_ref: 'ws_1',
  name: 'Research',
  root_path: '/srv/research',
  mount_mode: 'rw',
  state: 'active',
  max_read_bytes: 1024,
  dlp_mode: 'off',
}
beforeEach(() => {
  state.denied.clear()
  state.failed = false
  useTenantStore.getState().setActiveTenant('t1')
})

it.each(['permission', 'index read'])(
  'forgets an upload after its picker unmounts on %s loss and returns empty',
  async (cause) => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const show = () => (
      <QueryClientProvider client={client}>
        <WorkspaceWiki workspace={workspace} />
      </QueryClientProvider>
    )
    const view = render(show())
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Ingest' }))
    await user.upload(
      screen.getByLabelText('Upload source'),
      new File(['bytes'], 'source.md'),
    )
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Ingest source' }),
      ).toBeEnabled(),
    )
    if (cause === 'permission') {
      state.denied.add('sessions:workspace:write')
      view.rerender(show())
    } else {
      state.failed = true
      await user.click(screen.getByRole('button', { name: 'Refresh' }))
      await screen.findByRole('alert')
    }
    expect(screen.queryByLabelText('Upload source')).not.toBeInTheDocument()
    if (cause === 'permission') {
      state.denied.clear()
      view.rerender(show())
    } else {
      state.failed = false
      await user.click(screen.getByRole('button', { name: 'Retry' }))
    }
    expect(await screen.findByLabelText('Upload source')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Ingest source' })).toBeDisabled()
  },
)
