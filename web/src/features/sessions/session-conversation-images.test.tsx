// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A sent message keeps its images (#2158): each is a thumbnail read from the engine's
// copy that opens full size, and a reloaded page shows the same thumbnails because they
// come from the stored attachment, not from the tool's echo.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { SessionConversation } from './session-conversation'
import './i18n'

const onFrameRef = vi.hoisted(() => ({
  current: null as
    ((f: { seq: number; stream: string; line: string }) => void) | null,
}))
vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: (opts: {
    onFrame: (f: { seq: number; stream: string; line: string }) => void
  }) => {
    onFrameRef.current = opts.onFrame
    return {
      status: 'open',
      ended: false,
      ioUnavailable: null,
      retry: () => {},
    }
  },
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, activeTenant: 'demo' }),
}))

const api = vi.hoisted(() => ({ attachmentImage: vi.fn() }))
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    ...(await importOriginal<typeof import('@/features/agentops/api')>())
      .agentOpsApi,
    ...api,
  },
}))

const run: RunDTO = {
  run_ref: 'run_1',
  name: 'session',
  authz_workspace_id: 'default-workspace',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
}

const message = JSON.stringify({
  type: 'user',
  isReplay: true,
  message: {
    role: 'user',
    content: [{ type: 'text', text: 'what is wrong here?' }],
  },
  attachments: [{ id: 'att_1', media_type: 'image/png' }],
})

function mount() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={qc}>
      <SessionConversation run={run} />
    </QueryClientProvider>,
  )
  act(() => onFrameRef.current?.({ seq: 1, stream: 'stdout', line: message }))
}

beforeEach(() => {
  cleanup()
  vi.clearAllMocks()
  api.attachmentImage.mockResolvedValue(
    new Blob([new Uint8Array([1, 2, 3])], { type: 'image/png' }),
  )
})

describe('SessionConversation — images in a sent message', () => {
  it('shows a thumbnail under the message that opens full size', async () => {
    const user = userEvent.setup()
    mount()
    expect(await screen.findByText('what is wrong here?')).toBeInTheDocument()
    const open = await screen.findByRole('button', {
      name: 'Open Image 1 full size',
    })
    expect(api.attachmentImage).toHaveBeenCalledWith(
      'run_1',
      'att_1',
      expect.anything(),
    )
    expect(screen.getByRole('img', { name: 'Image 1' })).toHaveAttribute(
      'src',
      'data:image/png;base64,AQID',
    )
    await user.click(open)
    const dialog = await screen.findByRole('dialog')
    expect(dialog.querySelector('img')).toHaveAttribute(
      'src',
      'data:image/png;base64,AQID',
    )
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('a reloaded page shows the same thumbnail from the stored image', async () => {
    mount()
    await screen.findByRole('button', { name: 'Open Image 1 full size' })
    cleanup()
    api.attachmentImage.mockClear()
    mount()
    await screen.findByRole('button', { name: 'Open Image 1 full size' })
    expect(api.attachmentImage).toHaveBeenCalledTimes(1)
  })

  it('says so when the stored image can no longer be read', async () => {
    api.attachmentImage.mockRejectedValue(new Error('gone'))
    mount()
    expect(await screen.findByText('Image not available')).toBeInTheDocument()
  })
})
