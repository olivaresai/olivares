// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The session preview panel: the session's ports, a sandboxed frame of the URL the
// engine opened, reload and a new tab.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const allowed = vi.hoisted(() => ({ write: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => p !== 'sessions:run:write' || allowed.write,
  }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { previewPorts: vi.fn(), openPreview: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { SessionPreview } from './session-preview'

const run = { run_ref: 'run_1', state: 'running' } as unknown as RunDTO

function show(shown: RunDTO = run) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <SessionPreview run={shown} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  allowed.write = true
  vi.mocked(agentOpsApi.previewPorts).mockResolvedValue({ ports: [3000, 5173] })
  let n = 0
  vi.mocked(agentOpsApi.openPreview).mockImplementation(async (_r, port) => ({
    url: `/session/preview/tok${++n}/`,
    port,
    expires_at: '2026-10-09T20:00:00Z',
  }))
})

describe('SessionPreview', () => {
  it('opens the chosen port in a sandboxed frame, reloads and offers a new tab', async () => {
    const user = userEvent.setup()
    show()
    await user.selectOptions(await screen.findByLabelText('Port'), '5173')
    await user.clear(screen.getByLabelText('Path'))
    await user.type(screen.getByLabelText('Path'), '/app')
    await user.click(screen.getByRole('button', { name: 'Open' }))
    expect(agentOpsApi.openPreview).toHaveBeenCalledWith('run_1', 5173)
    const frame = await screen.findByTestId('session-preview-frame')
    expect(frame.getAttribute('src')).toBe('/session/preview/tok1/app')
    const sandbox = frame.getAttribute('sandbox') ?? ''
    expect(sandbox).toContain('allow-scripts')
    expect(sandbox).not.toContain('allow-same-origin')
    expect(
      screen
        .getByRole('link', { name: 'Open in a new tab' })
        .getAttribute('href'),
    ).toBe('/session/preview/tok1/app')

    await user.click(screen.getByRole('button', { name: 'Reload' }))
    expect(agentOpsApi.openPreview).toHaveBeenCalledTimes(2)
    expect(
      (await screen.findByTestId('session-preview-frame')).getAttribute('src'),
    ).toBe('/session/preview/tok2/app')
  })

  it('says so when the session serves nothing', async () => {
    vi.mocked(agentOpsApi.previewPorts).mockResolvedValue({ ports: [] })
    show()
    expect(
      await screen.findByText(
        'This session is not serving anything on a local port.',
      ),
    ).toBeTruthy()
  })

  it('asks for no ports while the session is not running', async () => {
    show({ ...run, state: 'stopped' })
    expect(
      await screen.findByText(
        'The session is not running, so it serves nothing.',
      ),
    ).toBeTruthy()
    expect(agentOpsApi.previewPorts).not.toHaveBeenCalled()
  })

  it('opens nothing without permission to operate the session', async () => {
    allowed.write = false
    show()
    expect(
      await screen.findByText(
        'Opening a preview needs permission to operate this session.',
      ),
    ).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Open' })).toBeNull()
  })
})
