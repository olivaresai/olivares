// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'

vi.mock('@/components/layout/step-up-state', () => ({
  StepUpRequiredState: ({ onElevated }: { onElevated?: () => void }) => (
    <button type="button" onClick={onElevated}>
      Complete elevation
    </button>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'tenant-l9', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const api = vi.hoisted(() => ({
  listServers: vi.fn(),
  getServer: vi.fn(),
  listTools: vi.fn(),
  listToolPins: vi.fn(),
  sendToolPinIntent: vi.fn(),
  listSkills: vi.fn(),
  wiring: vi.fn(),
  listConfigs: vi.fn(),
  getConfig: vi.fn(),
  createConfig: vi.fn(),
  updateConfig: vi.fn(),
  deleteConfig: vi.fn(),
  listRevisions: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, capabilitiesApi: api }
})

import CapabilitiesView from './capabilities-view'
import { RevisionsSheet } from './revisions'
import { ServerDetailSheet } from './server-detail'
import { ToolPinsTab } from './tool-pins'

const stepUp = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')

function wrap(ui: ReactNode) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false, gcTime: 0 } },
        })
      }
    >
      {ui}
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listServers.mockResolvedValue({ items: [], has_more: false })
  api.listTools.mockResolvedValue({ items: [], has_more: false })
  api.listSkills.mockResolvedValue({ items: [], has_more: false })
  api.listConfigs.mockResolvedValue({ items: [], has_more: false })
})

describe('capabilities re-enters every real read after step-up', () => {
  it('refetches the wiring graph', async () => {
    api.wiring.mockRejectedValueOnce(stepUp()).mockResolvedValue({
      nodes: [],
      edges: [],
      partial: false,
    })
    wrap(<CapabilitiesView />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: /wiring/i }))
    await user.click(
      await screen.findByRole('button', { name: 'Complete elevation' }),
    )
    await waitFor(() => expect(api.wiring).toHaveBeenCalledTimes(2))
  })

  it('refetches a server detail', async () => {
    api.getServer.mockRejectedValueOnce(stepUp()).mockResolvedValue({
      id: 'server-1',
      name: 'github',
      transport: 'stdio',
      status: 'active',
      connection: 'connected',
      tool_count: 0,
      has_config: false,
      config: null,
      health: null,
      tools: [],
      skills: [],
      resources: [],
      consumers: [],
    })
    wrap(
      <ServerDetailSheet
        serverId="server-1"
        open
        onOpenChange={() => undefined}
      />,
    )

    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Complete elevation' }),
    )
    await waitFor(() => expect(api.getServer).toHaveBeenCalledTimes(2))
  })

  it('refetches config revisions', async () => {
    api.listRevisions
      .mockRejectedValueOnce(stepUp())
      .mockResolvedValue({ items: [], has_more: false })
    wrap(
      <RevisionsSheet
        configId="config-1"
        serverRef="github"
        open
        onOpenChange={() => undefined}
      />,
    )

    const user = userEvent.setup()
    await user.click(
      await screen.findByRole('button', { name: 'Complete elevation' }),
    )
    await waitFor(() => expect(api.listRevisions).toHaveBeenCalledTimes(2))
  })

  it('refetches tool pins after its two bounded retries', async () => {
    api.listToolPins
      .mockRejectedValueOnce(stepUp())
      .mockRejectedValueOnce(stepUp())
      .mockRejectedValueOnce(stepUp())
      .mockResolvedValue({ items: [] })
    wrap(<ToolPinsTab canWrite />)

    const user = userEvent.setup()
    await user.click(
      await screen.findByRole(
        'button',
        { name: 'Complete elevation' },
        { timeout: 7_000 },
      ),
    )
    await waitFor(() => expect(api.listToolPins).toHaveBeenCalledTimes(4), {
      timeout: 7_000,
    })
  })
})
