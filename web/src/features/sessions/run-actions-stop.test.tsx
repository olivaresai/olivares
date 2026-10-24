// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// STOP ASKS FIRST IN EVERY FORM (#2549): the thread header's icon and the session card's
// labelled button read the same setting. The card's Stop used to stop at once, so "Full
// controls" stopped a session the header would have asked about.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { stop: vi.fn(), resume: vi.fn() },
}))
vi.mock('@/features/agentops/turn-interrupt', () => ({
  useTurnInterrupt: () => ({
    offered: false,
    allowed: false,
    pending: false,
    fenceUnavailable: false,
    interrupt: vi.fn(),
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { TooltipProvider } from '@/components/ui/tooltip'
import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { useClientSettings } from '@/features/settings/preferences'
import type { Capability } from './provenance'
import { RunActions } from './run-actions'

const caps: Capability[] = [{ id: 'stop', available: true }]
const running = {
  run_ref: 'run_1',
  state: 'running',
  transport: 'stream-json',
} as unknown as RunDTO

function actions(compact: boolean, offered = caps) {
  return (
    <TooltipProvider>
      <RunActions
        run={running}
        caps={offered}
        compact={compact}
        onClose={vi.fn()}
      />
    </TooltipProvider>
  )
}

function show(compact: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>{actions(compact)}</QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(agentOpsApi.stop).mockResolvedValue({} as never)
  useClientSettings.setState({ confirmStop: true })
})

describe.each([
  ['the thread header (icon)', true],
  ['the session card (labelled button)', false],
])('Stop in %s', (_form, compact) => {
  it('asks first, and stops only on the confirmation', async () => {
    const user = userEvent.setup()
    show(compact)
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    expect(agentOpsApi.stop).not.toHaveBeenCalled()
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(dialog.getByRole('button', { name: 'Stop the session' }))
    await waitFor(() =>
      expect(agentOpsApi.stop).toHaveBeenCalledWith('run_1', undefined),
    )
  })

  it('stops at once when the person turned the question off', async () => {
    const user = userEvent.setup()
    useClientSettings.setState({ confirmStop: false })
    show(compact)
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    await waitFor(() =>
      expect(agentOpsApi.stop).toHaveBeenCalledWith('run_1', undefined),
    )
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('drops a question whose session stopped meanwhile, so it does not come back', async () => {
    const user = userEvent.setup()
    const { rerender } = show(compact)
    await user.click(screen.getByRole('button', { name: 'Stop' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    const qc = new QueryClient()
    // The session ended elsewhere: Stop is no longer offered.
    rerender(
      <QueryClientProvider client={qc}>
        {actions(compact, [])}
      </QueryClientProvider>,
    )
    // It runs again: Stop is offered again, and nobody asked.
    rerender(
      <QueryClientProvider client={qc}>{actions(compact)}</QueryClientProvider>,
    )
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(agentOpsApi.stop).not.toHaveBeenCalled()
  })
})
