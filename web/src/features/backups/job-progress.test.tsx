// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import type { DRJob } from './types'

const stream = vi.hoisted(() => ({
  snapshot: undefined as ((job: DRJob) => void) | undefined,
}))
vi.mock('@/features/shared/sse', () => ({
  useLiveStream: (options: { onSnapshot: (job: DRJob) => void }) => {
    stream.snapshot = options.onSnapshot
    return { status: 'open' }
  },
}))
import { JobProgress } from './job-progress'
import { drKeys } from './api'

function show(jobId: string) {
  const client = new QueryClient()
  for (const key of [
    drKeys.backups(),
    drKeys.jobs(),
    drKeys.pending(),
    drKeys.schedule(),
  ]) {
    client.setQueryData(key, { items: [] })
  }
  const onFinished = vi.fn()
  const view = render(
    <QueryClientProvider client={client}>
      <JobProgress jobId={jobId} onFinished={onFinished} />
    </QueryClientProvider>,
  )
  return { client, onFinished, ...view }
}

it('shows the engine custody warning and preservation receipt when restore completes', () => {
  const receipt =
    'Sealer keys NOT restored: secret-store.key; previous state preserved as *.pre-restore-job'
  show('restore-job')
  act(() =>
    stream.snapshot?.({
      id: 'restore-job',
      kind: 'restore',
      status: 'completed',
      phase: 'restart_required',
      progress: 100,
      created_at: '',
      notes: receipt,
    }),
  )
  expect(screen.getByRole('status')).toHaveTextContent(receipt)
})

it.each([
  ['backup', 'completed'],
  ['backup', 'failed'],
  ['restore', 'completed'],
  ['restore', 'failed'],
])(
  'refreshes %s job caches once when %s, not while running',
  (kind, status) => {
    const { client, onFinished } = show('job-1')
    const snapshot: DRJob = {
      id: 'job-1',
      kind,
      status: 'running',
      phase: 'working',
      progress: 50,
      created_at: '',
    }
    const keys = [drKeys.backups(), drKeys.jobs(), drKeys.pending()]
    act(() => stream.snapshot?.(snapshot))
    for (const key of keys)
      expect(client.getQueryState(key)?.isInvalidated).toBe(false)
    expect(onFinished).not.toHaveBeenCalled()
    const terminal = { ...snapshot, status, progress: 100 }
    act(() => stream.snapshot?.(terminal))
    for (const key of keys)
      expect(client.getQueryState(key)?.isInvalidated).toBe(true)
    expect(client.getQueryState(drKeys.schedule())?.isInvalidated).toBe(false)
    // A reconnect can replay the terminal snapshot: it must not refetch again.
    for (const key of keys) client.setQueryData(key, { items: [] })
    act(() => stream.snapshot?.(terminal))
    for (const key of keys)
      expect(client.getQueryState(key)?.isInvalidated).toBe(false)
    expect(onFinished).toHaveBeenCalledTimes(1)
  },
)
