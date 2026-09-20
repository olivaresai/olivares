// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
vi.mock('./live-console', () => ({ LiveConsole: () => <div>live</div> }))
vi.mock('./governance-panel', () => ({ GovernancePanel: () => <div>gov</div> }))

const api = vi.hoisted(() => ({ getRun: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { RunDetailSheet } from './run-detail'

const unnamed: RunDTO = {
  run_ref: 'run-anon',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: false,
  critical: false,
}

function wrap(run: RunDTO) {
  api.getRun.mockResolvedValue(run)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunDetailSheet runRef={run.run_ref} onClose={() => undefined} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('RunDetailSheet — what the run is called', () => {
  it('does not name a run by its raw reference', async () => {
    wrap(unnamed)
    expect(await screen.findByText('Untitled session')).toBeInTheDocument()
    expect(screen.getByText('run-anon')).toBeInTheDocument()
    expect(screen.getByText('Untitled session').className).not.toMatch(
      /font-mono/,
    )
  })

  it('paints the operator-given name when one exists', async () => {
    wrap({ ...unnamed, name: 'nightly-indexer' })
    expect(await screen.findByText('nightly-indexer')).toBeInTheDocument()
    expect(screen.queryByText('Untitled session')).toBeNull()
  })
})
