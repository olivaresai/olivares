// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The composed console SEND PATH, proved by the request the browser would actually
// make. This exists because of a real integration defect: the console could launch an
// operable Codex profile and then sent every turn as `{"line": …}`, which the engine
// correctly refuses with 400 before any child effect, so a supported session was
// launchable and unusable.
//
// ⛔ IT ASSERTS THE BODY, NOT A HELPER'S RETURN VALUE. Only the transport (`http`) is
// stubbed: the component, the mutation, the driver discriminant and the real
// `agentOpsApi` all run, so what is inspected is the exact `(path, body)` pair that
// would go on the wire. A test that called `agentOpsApi.inputText` directly would pass
// against the very bug this file is here to keep out — the bug was in WHICH operation
// the console chose, not in either operation.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const authority = vi.hoisted(() => ({ write: true, tenant: 'tenant-a' }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (permission: string) =>
      permission === 'sessions:run:write' && authority.write,
    principal: { user_id: 'user-a' },
    activeTenant: authority.tenant,
  }),
}))

vi.mock('@/lib/api/client', () => ({
  http: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}))
// The SSE attach is a separate concern with its own test (attach.test.tsx); here it is
// held open so the console renders its live, sendable state.
vi.mock('./attach', () => ({
  useRunAttach: () => ({
    status: 'open',
    ended: false,
    ioUnavailable: null,
    retry: () => {},
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

import { http } from '@/lib/api/client'
import { ApiError, parseErrorEnvelope } from '@/lib/api/errors'
import { toast } from '@/components/ui/toaster'
import { useSessionStore } from '@/stores/session'
import { LiveConsole } from './live-console'
import type { RunDTO } from './types'

const baseRun: RunDTO = {
  run_ref: 'run_1',
  name: 'session',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
}

/** A run launched under the registered Codex driver: an owned JSON-RPC child. */
const codexRun: RunDTO = {
  ...baseRun,
  provider_profile_ref: 'ppf_codex',
  provider_driver: 'codex',
}

/** The historical Claude stream-json child: raw NDJSON on stdin. */
const claudeRun: RunDTO = {
  ...baseRun,
  provider_profile_ref: 'ppf_claude',
  provider_driver: 'claude',
}

/** A legacy run that names no profile at all — also the raw path. */
const legacyRun: RunDTO = { ...baseRun }

function wrap(run: RunDTO) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <LiveConsole run={run} />
    </QueryClientProvider>,
  )
}

const posted = () => {
  const calls = vi.mocked(http.post).mock.calls
  expect(calls).toHaveLength(1)
  return {
    path: calls[0][0] as string,
    body: calls[0][1] as Record<string, unknown>,
  }
}

/** userEvent.type reads `{` and `[` as key descriptors; `{{`/`[[` type them literally. */
const asKeystrokes = (text: string) =>
  text.replace(/\{/g, '{{').replace(/\[/g, '[[')

async function send(text: string) {
  const user = userEvent.setup()
  const box = screen.getByRole('textbox', { name: 'Session turn' })
  await user.type(box, asKeystrokes(text))
  // The typed value must be what we meant before anything is asserted about the body:
  // an escaping mistake here would silently test a different string than the one named.
  expect(box).toHaveValue(text)
  await user.click(screen.getAllByRole('button', { name: /^send$/i })[0])
  return user
}

beforeEach(() => {
  authority.write = true
  authority.tenant = 'tenant-a'
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
  vi.mocked(toast.warning).mockClear()
  vi.mocked(http.post).mockReset()
  vi.mocked(http.post).mockResolvedValue({ accepted: true })
})

describe('LiveConsole interrupt contract', () => {
  it('interrupts the turn without stopping the run or losing an unsent draft', async () => {
    wrap(codexRun)
    const user = userEvent.setup()
    await user.type(
      screen.getByRole('textbox', { name: 'Session turn' }),
      'next turn draft',
    )
    await user.click(screen.getByRole('button', { name: 'Interrupt turn' }))
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Turn interrupted. The session remains open.',
      ),
    )
    expect(posted()).toEqual({
      path: '/v1/m/sessions/runs/run_1/interrupt',
      body: undefined,
    })
    expect(screen.getByRole('textbox', { name: 'Session turn' })).toHaveValue(
      'next turn draft',
    )
    expect(screen.getByRole('textbox', { name: 'Session turn' })).toBeEnabled()
    await user.click(screen.getAllByRole('button', { name: 'Send' })[0])
    await waitFor(() =>
      expect(http.post).toHaveBeenLastCalledWith(
        '/v1/m/sessions/runs/run_1/input',
        { text: 'next turn draft' },
      ),
    )
    expect(
      vi
        .mocked(http.post)
        .mock.calls.every(([path]) => !String(path).endsWith('/stop')),
    ).toBe(true)
  })

  it('presents the observed work fence and never falls back to the unfenced plane', async () => {
    wrap({
      ...codexRun,
      work_item_id: 'work-a',
      work_lease_fence: 7,
      work_owner_epoch: 2,
      work_dispatch_key: 'dispatch-a',
    })
    const body = {
      verdict: 'ROTO',
      code: 'stale_fence',
      error: { code: 'stale_fence', message: 'stale_fence' },
    }
    const error = parseErrorEnvelope(body, 'Conflict')
    vi.mocked(http.post).mockRejectedValue(
      new ApiError(
        409,
        error.code,
        error.message,
        undefined,
        error.details,
        body,
      ),
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Interrupt turn' }),
    )
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'The session cannot accept this interruption in its current work state. Refresh the run to check its status.',
      ),
    )
    expect(posted()).toEqual({
      path: '/v1/m/sessions/runs/run_1/interrupt',
      body: { work_lease_fence: 7 },
    })
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('explains the actual unknown work verdict without retrying or claiming a refusal', async () => {
    const body = {
      verdict: 'NO_HE_PODIDO_MIRAR',
      code: 'work_interrupt_ambiguous',
      error: {
        code: 'work_interrupt_ambiguous',
        message: 'work_interrupt_ambiguous',
      },
    }
    const error = parseErrorEnvelope(body, 'Service Unavailable')
    vi.mocked(http.post).mockRejectedValue(
      new ApiError(
        503,
        error.code,
        error.message,
        undefined,
        error.details,
        body,
      ),
    )
    wrap({ ...codexRun, work_item_id: 'work-a', work_lease_fence: 7 })
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Session turn' }),
      'unsent draft',
    )
    await userEvent.click(
      screen.getByRole('button', { name: 'Interrupt turn' }),
    )
    await waitFor(() =>
      expect(toast.warning).toHaveBeenCalledWith(
        'The interruption outcome is unknown. Check the session before trying again.',
      ),
    )
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
    expect(screen.getByRole('textbox', { name: 'Session turn' })).toHaveValue(
      'unsent draft',
    )
  })

  it.each([undefined, 0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    'refuses an incomplete work binding with fence %s',
    async (fence) => {
      wrap({ ...codexRun, work_item_id: 'work-a', work_lease_fence: fence })
      const button = screen.getByRole('button', { name: 'Interrupt turn' })
      expect(button).toBeDisabled()
      await userEvent.click(button)
      expect(http.post).not.toHaveBeenCalled()
    },
  )

  it.each([
    claudeRun,
    legacyRun,
    { ...codexRun, transport: 'remote-control' as const },
  ])('does not offer unsupported interruption for %o', (run) => {
    wrap(run)
    expect(screen.queryByRole('button', { name: 'Interrupt turn' })).toBeNull()
  })

  it.each(['stopped', 'idle', 'failed'] as const)(
    'disables interruption when the run is %s',
    (state) => {
      wrap({ ...codexRun, state })
      expect(
        screen.getByRole('button', { name: 'Interrupt turn' }),
      ).toBeDisabled()
    },
  )

  it('checks the exact current write permission even for a previously rendered button', async () => {
    wrap(codexRun)
    const button = screen.getByRole('button', { name: 'Interrupt turn' })
    authority.write = false
    fireEvent.click(button)
    await act(async () => {})
    expect(http.post).not.toHaveBeenCalled()
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('hides the action from a read-only operator', () => {
    authority.write = false
    wrap(codexRun)
    expect(screen.queryByRole('button', { name: 'Interrupt turn' })).toBeNull()
  })

  it('does not report a late response under a renewed credential with the same session id', async () => {
    useSessionStore.getState().setSession({
      token: 'test-before',
      sessionId: 'same-session',
      expiresAt: '2099-01-01',
    })
    let resolve!: (run: RunDTO) => void
    vi.mocked(http.post).mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done
        }),
    )
    wrap(codexRun)
    await userEvent.click(
      screen.getByRole('button', { name: 'Interrupt turn' }),
    )
    expect(
      screen.getByRole('button', { name: 'Interrupt turn' }),
    ).toBeDisabled()
    act(() =>
      useSessionStore.getState().setSession({
        token: 'test-after',
        sessionId: 'same-session',
        expiresAt: '2099-01-01',
      }),
    )
    await act(async () => resolve(codexRun))
    expect(http.post).toHaveBeenCalledTimes(1)
    expect(toast.success).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('LiveConsole send contract', () => {
  it('sends a Codex run exactly {text}, and never a line', async () => {
    wrap(codexRun)
    await send('hello')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    const { path, body } = posted()
    expect(path).toBe('/v1/m/sessions/runs/run_1/input')
    expect(body).toEqual({ text: 'hello' })
    expect(body).not.toHaveProperty('line')
    expect(body).not.toHaveProperty('message')
  })

  it('sends a Claude stream-json run as a wrapped user frame, never asking the operator for NDJSON', async () => {
    wrap(claudeRun)
    await send('hello')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    const { path, body } = posted()
    expect(path).toBe('/v1/m/sessions/runs/run_1/input')
    expect(body).toEqual({
      line: JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'hello' },
      }),
    })
    expect(body).not.toHaveProperty('text')
  })

  it('wraps a legacy run with no profile as a sentence too', async () => {
    wrap(legacyRun)
    await send('raw')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(posted().body).toEqual({
      line: JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'raw' },
      }),
    })
  })

  it('does not decide from the text: NDJSON-looking input to a Codex run is still {text}', async () => {
    wrap(codexRun)
    await send('{"type":"user"}')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    // The operator can type anything; a driver run is spoken to as a TURN, and the
    // console never promotes a typed line into a protocol frame.
    expect(posted().body).toEqual({ text: '{"type":"user"}' })
  })

  it('labels every driver as a sentence and never asks for NDJSON on the default field', async () => {
    wrap(codexRun)
    const box = screen.getByRole('textbox', { name: 'Session turn' })
    expect(box).toHaveAttribute('placeholder', 'Send a sentence…')
    expect(box.getAttribute('placeholder')).not.toMatch(/NDJSON/i)
  })

  it('labels a Claude run as a sentence too', async () => {
    wrap(claudeRun)
    const box = screen.getByRole('textbox', { name: 'Session turn' })
    expect(box).toHaveAttribute('placeholder', 'Send a sentence…')
    expect(box.getAttribute('placeholder')).not.toMatch(/NDJSON/i)
  })

  it('keeps the NDJSON wording only under the advanced disclosure', async () => {
    wrap(claudeRun)
    expect(screen.getByText(/Advanced/i)).toBeInTheDocument()
    const wire = screen.getByRole('textbox', { name: 'Session input line' })
    expect(wire).toHaveAttribute('placeholder', 'Send an NDJSON line to stdin…')
  })

  it('clears the box only after the accepted response, and retains it on a refusal', async () => {
    // Accepted: the box is emptied.
    const { unmount } = wrap(codexRun)
    await send('accepted turn')
    await waitFor(() =>
      expect(screen.getByRole('textbox', { name: 'Session turn' })).toHaveValue(
        '',
      ),
    )
    unmount()

    // Refused: the engine's 400 must not cost the operator their text.
    vi.mocked(http.post).mockReset()
    vi.mocked(http.post).mockRejectedValue(
      new ApiError(
        400,
        'bad_request',
        'this session is driven by an owned provider protocol',
      ),
    )
    wrap(codexRun)
    await send('refused turn')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(screen.getByRole('textbox', { name: 'Session turn' })).toHaveValue(
      'refused turn',
    )
  })
})

// THE TURN IS A CONTROL, AND A WORK-BOUND RUN HAS ONE CONTROL PLANE.
//
// The console read the lease fence for INTERRUPT and for nothing else, so a work-bound
// session could be attached, read and typed into, and every turn came back 409
// "work-bound session requires fenced runtime control" (`runtime_work.go`
// refuseLegacyControlUnderWork, reached from the unfenced /input) — while
// `agent session input --text 'x' --work-lease-fence N` succeeded on that same run.
// These cases assert the BODY, the same way the send-contract ones above do: the
// defect was a missing sibling key on the wire, not a helper anyone could have
// inspected in isolation.
describe('LiveConsole send contract under a work lease', () => {
  const FENCE = 7
  const bound = (run: RunDTO): RunDTO => ({
    ...run,
    work_item_id: 'work-a',
    work_lease_fence: FENCE,
    work_owner_epoch: 2,
    work_dispatch_key: 'dispatch-a',
  })

  it('carries the observed fence on a {text} turn, exactly as the CLI does', async () => {
    wrap(bound(codexRun))
    await send('review the remaining tests')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    const { path, body } = posted()
    expect(path).toBe('/v1/m/sessions/runs/run_1/input')
    expect(body).toEqual({
      text: 'review the remaining tests',
      work_lease_fence: FENCE,
    })
  })

  it('carries the observed fence on a {line} turn too', async () => {
    wrap(bound(claudeRun))
    await send('continue')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(posted().body).toEqual({
      line: JSON.stringify({
        type: 'user',
        message: { role: 'user', content: 'continue' },
      }),
      work_lease_fence: FENCE,
    })
  })

  it('carries the fence on the advanced wire line, which is a control as well', async () => {
    const user = userEvent.setup()
    wrap(bound(claudeRun))
    const wire = screen.getByRole('textbox', { name: 'Session input line' })
    await user.type(wire, asKeystrokes('{"type":"user"}'))
    // Both forms label their button "Send": the wire one is the second, under the
    // advanced disclosure, exactly as the placeholder case above addresses it.
    await user.click(screen.getAllByRole('button', { name: /^send$/i })[1])
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(posted().body).toEqual({
      line: '{"type":"user"}',
      work_lease_fence: FENCE,
    })
  })

  // ⛔ AND THE KEY IS OMITTED, NOT SENT EMPTY, ON A RUN THAT HAS NO LEASE. The engine
  //    answers 400 to a non-positive `work_lease_fence`, so a fence field that always
  //    travelled would break every ordinary run — the mirror of the missing-fence defect.
  it('omits the key entirely on a run with no work stamp', async () => {
    wrap(codexRun)
    await send('hello')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(posted().body).not.toHaveProperty('work_lease_fence')
  })

  // A stamped run whose fence did not arrive as a positive integer has nothing to
  // present. It is left to the 409 of the unfenced plane — which says the session
  // cannot be controlled from here, and is true — rather than to a 400 that would
  // blame the request.
  it('presents no fence it does not have, and never invents one', async () => {
    wrap({ ...codexRun, work_item_id: 'work-a', work_owner_epoch: 2 })
    await send('hello')
    await waitFor(() => expect(http.post).toHaveBeenCalled())
    expect(posted().body).toEqual({ text: 'hello' })
  })
})
