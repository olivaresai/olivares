// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { ContextRing } from './context-ring'
import type { RunDTO } from '@/features/agentops/types'
const api = vi.hoisted(() => ({ activity: vi.fn() }))
vi.mock('@/features/agentops/api', async (original) => ({
  ...(await original<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'tnt-a' }),
}))
const run = {
  run_ref: 'run-a',
  state: 'idle',
  provider_driver: 'claude',
} as RunDTO
it('does not keep another session’s context while its own record loads', async () => {
  api.activity.mockResolvedValueOnce({
    context_tokens: 100000,
    context_window: 200000,
    context_percent: 50,
  })
  const { rerender } = renderIntel(<ContextRing run={run} />)
  await screen.findByTestId('composer-context')
  api.activity.mockImplementation(() => new Promise(() => {}))
  rerender(<ContextRing run={{ ...run, run_ref: 'run-b' }} />)
  expect(screen.queryByTestId('composer-context')).not.toBeInTheDocument()
})
it('shows a readable percentage alongside the meter', async () => {
  api.activity.mockResolvedValue({
    context_tokens: 100000,
    context_window: 200000,
    context_percent: 50,
  })
  renderIntel(<ContextRing run={run} />)
  expect(await screen.findByText('50%')).toBeInTheDocument()
})
it('an ended session keeps its last value on a filled wedge, never a loader-like arc', async () => {
  // A short round-capped arc on a faint track is the shape of a loading spinner: an
  // ended session at 10% read as "still loading" (the composer capture of 2026-11-01).
  api.activity.mockResolvedValue({
    context_tokens: 20000,
    context_window: 200000,
    context_percent: 10,
  })
  renderIntel(<ContextRing run={{ ...run, state: 'failed' }} />)
  const meter = await screen.findByTestId('composer-context')
  expect(meter).toHaveTextContent('10%')
  const value = meter.querySelector('[data-part="value"]')
  expect(value).not.toBeNull()
  // A sector: the stroke runs from the center to the rim, with square ends.
  expect(Number(value?.getAttribute('stroke-width'))).toBe(
    2 * Number(value?.getAttribute('r')),
  )
  expect(value?.getAttribute('stroke-linecap')).not.toBe('round')
})
