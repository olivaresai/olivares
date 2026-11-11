// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The session preview panel: the session's ports, a sandboxed frame of the URL the
// engine opened, reload and a new tab.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
  agentOpsApi: {
    previewPorts: vi.fn(),
    openPreview: vi.fn(),
    listRuns: vi.fn(),
    input: vi.fn(),
    inputText: vi.fn(),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import '@/features/agentops/i18n'
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
  vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
    has_more: false,
    items: [],
  })
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

  it('accepts only an asked opaque-frame pick and sends to the chosen live session', async () => {
    vi.mocked(agentOpsApi.input).mockResolvedValue({ accepted: true })
    vi.mocked(agentOpsApi.inputText).mockResolvedValue({ accepted: true })
    const user = userEvent.setup()
    const source = {
      ...run,
      name: 'alpha',
      workspace_ref: 'ws1',
      provider_driver: 'claude',
    } as RunDTO
    const other = { ...source, run_ref: 'run_2', name: 'beta' }
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      has_more: false,
      items: [
        other,
        { ...other, run_ref: 'stopped', state: 'stopped' },
        { ...other, run_ref: 'elsewhere', workspace_ref: 'ws2' },
      ],
    })
    show(source)
    await user.click(await screen.findByRole('button', { name: 'Open' }))
    const frame = (await screen.findByTestId(
      'session-preview-frame',
    )) as HTMLIFrameElement
    const told = vi.spyOn(frame.contentWindow!, 'postMessage')
    const report = (
      origin: string,
      source: MessageEventSource | null = frame.contentWindow,
    ) =>
      window.dispatchEvent(
        new MessageEvent('message', {
          origin,
          source,
          data: {
            type: 'olivares-element',
            selector: 'button#save',
            text: 'Save',
            path: '/session/preview/tok1/',
          },
        }),
      )
    // The page cannot open a note or send a turn before the person asks.
    await act(async () => {
      report('null')
    })
    expect(screen.queryByRole('textbox', { name: 'Note' })).toBeNull()
    // Annotate waits for the page: its first load would cancel an earlier request.
    expect(
      (screen.getByRole('button', { name: 'Annotate' }) as HTMLButtonElement)
        .disabled,
    ).toBe(true)
    fireEvent.load(frame)
    await user.click(screen.getByRole('button', { name: 'Annotate' }))
    await act(async () => {
      report('https://untrusted.example')
    })
    expect(screen.queryByRole('textbox', { name: 'Note' })).toBeNull()
    // The page is told to pick; its origin is opaque, so no target origin can name it.
    expect(told).toHaveBeenCalledWith(
      { type: 'olivares-annotate', on: true },
      '*',
    )
    const clicked = {
      type: 'olivares-element',
      selector: 'button#save',
      text: 'Save',
      // The page reports the engine's address: its token is a credential.
      path: '/session/preview/tok1/',
    }
    // Only the frame speaks for the page.
    await act(async () => {
      window.dispatchEvent(
        new MessageEvent('message', {
          data: clicked,
          origin: 'null',
          source: window,
        }),
      )
    })
    expect(screen.queryByRole('textbox', { name: 'Note' })).toBeNull()
    await act(async () => {
      window.dispatchEvent(
        new MessageEvent('message', {
          data: clicked,
          origin: 'null',
          source: frame.contentWindow,
        }),
      )
      // A second response in the same event batch cannot replace the first quote.
      window.dispatchEvent(
        new MessageEvent('message', {
          data: { ...clicked, selector: 'forged' },
          origin: 'null',
          source: frame.contentWindow,
        }),
      )
    })
    expect(
      screen.getByText('button#save ‘Save’ at localhost:3000/'),
    ).toBeTruthy()
    await act(async () => {
      report('null')
    })
    await user.type(
      await screen.findByRole('textbox', { name: 'Note' }),
      'Make it blue.',
    )
    await user.click(screen.getByRole('button', { name: 'Add' }))
    // The chosen agent is shown by its mark.
    expect(screen.getByTestId('tool-mark')).toBeTruthy()
    expect(
      (screen.getByLabelText('Send notes to') as HTMLSelectElement).value,
    ).toBe('run_1')
    await user.selectOptions(screen.getByLabelText('Send notes to'), 'run_2')
    expect(
      screen.queryByRole('option', { name: /stopped|elsewhere/ }),
    ).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(agentOpsApi.input).toHaveBeenCalledTimes(1))
    const [ref, line] = vi.mocked(agentOpsApi.input).mock.calls[0]!
    expect(ref).toBe('run_2')
    const turn = JSON.parse(line).message.content as string
    expect(turn).toBe(
      'Notes on the preview of session alpha:\n\n> button#save ‘Save’ at localhost:3000/\n\nMake it blue.',
    )
    expect(turn).not.toContain('tok1')
  })

  it('says so when the session serves nothing', async () => {
    vi.mocked(agentOpsApi.previewPorts).mockResolvedValue({ ports: [] })
    show()
    expect(
      await screen.findByText(
        'This session is not serving anything on a local port.',
      ),
    ).toBeTruthy()
    // An empty tab is that one line, not a paragraph about previews before it.
    expect(screen.getByTestId('session-preview').textContent).toBe(
      'This session is not serving anything on a local port.',
    )
  })

  it('says the ports could not be read, and reads them again on Retry', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.previewPorts).mockRejectedValueOnce(
      new Error('engine down'),
    )
    show()
    expect(
      await screen.findByText(
        "The session's ports could not be read. Try again in a moment.",
      ),
    ).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('combobox', { name: 'Port' })).toBeTruthy()
    expect(agentOpsApi.previewPorts).toHaveBeenCalledTimes(2)
  })

  it('announces that it looks for the ports while it reads them', async () => {
    vi.mocked(agentOpsApi.previewPorts).mockReturnValue(
      new Promise<never>(() => {}),
    )
    show()
    expect(screen.getByRole('status')).toHaveTextContent(
      "Looking for the session's ports…",
    )
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
