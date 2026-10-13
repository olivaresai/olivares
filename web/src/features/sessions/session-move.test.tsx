// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Move to…" on a stopped session. The same session continues on another account of its
// tool (a move), or a new, linked session continues in another tool from a summary the
// person reviews (a handoff). The console renders the engine's two routes and adds no
// rule of its own: it offers only profiles of the session's tool for a move, and none of
// its own profile.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api, toast, auth } = vi.hoisted(() => ({
  api: {
    listProfiles: vi.fn(),
    move: vi.fn(),
    handoffPreview: vi.fn(),
    handoff: vi.fn(),
    stop: vi.fn(),
    resume: vi.fn(),
    cleanup: vi.fn(),
    deleteRun: vi.fn(),
  },
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  auth: { can: (_: string): boolean => true },
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => auth.can(p) }),
}))
vi.mock('@/features/agentops/auth-boundary', () => ({
  useAuthBoundary: () => ({ epoch: 1 }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
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
vi.mock('@/components/ui/toaster', () => ({ toast }))

import {
  nameProfileRefs,
  namesProfiles,
  profileLabel,
  type RunDTO,
} from '@/features/agentops/types'
import type { Capability } from './provenance'
import { RunActions } from './run-actions'
import { MoveSessionDialog } from './session-move'

const profile = (ref: string, driver: string, name?: string) => ({
  profile_ref: ref,
  driver,
  ...(name ? { display_name: name } : {}),
  state: 'active',
  local_environment: true,
  operable: true,
  environment_ref: 'env1',
})
const run = (over: Partial<RunDTO> = {}) =>
  ({
    run_ref: 'run_1',
    state: 'stopped',
    transport: 'stream-json',
    provider_driver: 'claude',
    provider_profile_ref: 'ppf_a',
    claude_session_id: 'sid-1',
    ...over,
  }) as unknown as RunDTO

function show(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const handoffMode = async (user: ReturnType<typeof userEvent.setup>) =>
  user.click(
    await screen.findByRole('radio', {
      name: 'Another tool, as a new session',
    }),
  )

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  api.listProfiles.mockResolvedValue({
    items: [
      profile('ppf_a', 'claude', 'Account A'),
      profile('ppf_b', 'claude', 'Account B'),
      profile('ppf_codex', 'codex', 'Codex main'),
      { ...profile('ppf_off', 'claude', 'Disabled'), state: 'disabled' },
      {
        ...profile('ppf_far', 'claude', 'Elsewhere'),
        local_environment: false,
      },
    ],
    has_more: false,
  })
  api.move.mockResolvedValue(run({ provider_profile_ref: 'ppf_b' }))
  api.handoffPreview.mockResolvedValue({
    summary: 'The task: migrate the schema.',
    source_driver: 'claude',
    turns: 4,
    transcript_bytes: 2048,
  })
  api.handoff.mockResolvedValue({
    ...run({ provider_driver: 'codex', provider_profile_ref: 'ppf_codex' }),
    summary_state: 'delivered',
  })
})

describe('MoveSessionDialog — a move', () => {
  it('moves the session to another account of its own tool, and lists no other', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    show(<MoveSessionDialog run={run()} onClose={onClose} />)

    await user.click(await screen.findByRole('combobox', { name: 'Account' }))
    // Another tool, its own profile, a disabled one and a foreign one are not choices.
    expect(
      await screen.findByRole('option', { name: /Account B/ }),
    ).toBeInTheDocument()
    for (const absent of [/Account A/, /Codex main/, /Disabled/, /Elsewhere/])
      expect(screen.queryByRole('option', { name: absent })).toBeNull()

    await user.click(screen.getByRole('option', { name: /Account B/ }))
    await user.click(screen.getByRole('button', { name: 'Move' }))
    await waitFor(() => expect(api.move).toHaveBeenCalledWith('run_1', 'ppf_b'))
    expect(api.handoff).not.toHaveBeenCalled()
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'Moved to Account B. Resume it to continue.',
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('does not offer Move until an account is chosen', async () => {
    show(<MoveSessionDialog run={run()} onClose={vi.fn()} />)
    expect(await screen.findByRole('button', { name: 'Move' })).toBeDisabled()
  })

  it('tells two unnamed accounts of one tool apart', async () => {
    const user = userEvent.setup()
    api.listProfiles.mockResolvedValue({
      items: [
        profile('ppf_00000000-aaaa-0000-0000-00000000a111', 'claude'),
        profile('ppf_00000000-bbbb-0000-0000-00000000b222', 'claude'),
      ],
      has_more: false,
    })
    show(<MoveSessionDialog run={run()} onClose={vi.fn()} />)
    await user.click(await screen.findByRole('combobox', { name: 'Account' }))
    expect(
      await screen.findByRole('option', { name: /claude 00a111/ }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('option', { name: /claude 00b222/ }),
    ).toBeInTheDocument()
  })

  it('says why no account can be chosen when the list is refused or fails', async () => {
    auth.can = (p) => p !== 'sessions:profile:read'
    const { unmount } = show(
      <MoveSessionDialog run={run()} onClose={vi.fn()} />,
    )
    expect(
      await screen.findByText(
        'You are not allowed to list accounts, so none can be chosen.',
      ),
    ).toBeInTheDocument()
    expect(api.listProfiles).not.toHaveBeenCalled()
    unmount()

    auth.can = () => true
    api.listProfiles.mockRejectedValue(new Error('boom'))
    show(<MoveSessionDialog run={run()} onClose={vi.fn()} />)
    expect(
      await screen.findByText('The accounts could not be listed.'),
    ).toBeInTheDocument()
  })
})

describe('MoveSessionDialog — a handoff', () => {
  it('shows the generated summary, any tool is a choice, and sends the text on screen', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    show(<MoveSessionDialog run={run()} onClose={onClose} />)

    await handoffMode(user)
    const summary = await screen.findByRole('textbox', {
      name: 'Summary the new session starts from',
    })
    await waitFor(() =>
      expect(summary).toHaveValue('The task: migrate the schema.'),
    )
    expect(
      screen.getByText('Transcript attached: 4 requests, 2048 bytes.'),
    ).toBeInTheDocument()

    await user.click(screen.getByRole('combobox', { name: 'Account' }))
    expect(
      screen.getByRole('option', { name: /Account B/ }),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('option', { name: /Codex main/ }))
    await user.click(screen.getByRole('button', { name: 'Start new session' }))

    // What the person read is what is sent, edited or not.
    await waitFor(() =>
      expect(api.handoff).toHaveBeenCalledWith('run_1', {
        provider_profile_ref: 'ppf_codex',
        summary: 'The task: migrate the schema.',
      }),
    )
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        'New session started from the summary.',
      ),
    )
    expect(onClose).toHaveBeenCalled()
  })

  it('sends the reviewed text, and will not send an empty one', async () => {
    const user = userEvent.setup()
    show(<MoveSessionDialog run={run()} onClose={vi.fn()} />)
    await handoffMode(user)
    const summary = await screen.findByRole('textbox', {
      name: 'Summary the new session starts from',
    })
    await waitFor(() =>
      expect((summary as HTMLTextAreaElement).value).toContain('migrate'),
    )
    await user.click(screen.getByRole('combobox', { name: 'Account' }))
    await user.click(await screen.findByRole('option', { name: /Codex main/ }))

    await user.clear(summary)
    expect(
      screen.getByRole('button', { name: 'Start new session' }),
    ).toBeDisabled()

    await user.type(summary, 'Reviewed: finish the rollout.')
    await user.click(screen.getByRole('button', { name: 'Start new session' }))
    await waitFor(() =>
      expect(api.handoff).toHaveBeenCalledWith('run_1', {
        provider_profile_ref: 'ppf_codex',
        summary: 'Reviewed: finish the rollout.',
      }),
    )
  })

  it.each([
    [
      'not_delivered',
      'The new session started, but its first message did not arrive. Send the text below from the session.',
    ],
    [
      'unknown',
      'The new session started and its first message may have arrived. Check the session before sending the text below again.',
    ],
    [
      'waits_for_approval',
      'The new session waits for an approval, and its first message is not sent automatically. Once it runs, send the text below.',
    ],
  ])(
    'says so and keeps the text when the first message is %s',
    async (state, message) => {
      const user = userEvent.setup()
      const onClose = vi.fn()
      api.handoff.mockResolvedValue({
        ...run({ provider_driver: 'codex' }),
        summary_state: state,
        first_turn:
          'Reviewed text.\n\nTranscript of the earlier session: /h/x.md',
      })
      show(<MoveSessionDialog run={run()} onClose={onClose} />)
      await handoffMode(user)
      await screen.findByDisplayValue('The task: migrate the schema.')
      await user.click(screen.getByRole('combobox', { name: 'Account' }))
      await user.click(
        await screen.findByRole('option', { name: /Codex main/ }),
      )
      await user.click(
        screen.getByRole('button', { name: 'Start new session' }),
      )

      expect(await screen.findByText(message)).toBeInTheDocument()
      expect(
        screen.getByRole('textbox', { name: 'First message' }),
      ).toHaveValue(
        'Reviewed text.\n\nTranscript of the earlier session: /h/x.md',
      )
      // Not a success toast, and the dialog is still there to copy the text from.
      expect(toast.success).not.toHaveBeenCalled()
      expect(onClose).not.toHaveBeenCalled()
    },
  )
})

describe('the ledger of a move', () => {
  const from = 'ppf_01a130ad-ea6b-7e53-aa21-ced22db8269f'
  const to = 'ppf_01a130ad-ea73-78aa-897b-6504f64fb8b5'
  const detail = `moved from ${from} to ${to}: 4 files, 1203 bytes`

  it('shows the account names the person gave, and keeps a reference it cannot resolve', () => {
    const names = new Map([[from, 'Account A']])
    expect(nameProfileRefs(detail, names)).toBe(
      `moved from Account A to ${to}: 4 files, 1203 bytes`,
    )
  })

  it('applies to a move and a handoff, and to no other event', () => {
    expect(namesProfiles('moved')).toBe(true)
    expect(namesProfiles('handed_off')).toBe(true)
    expect(namesProfiles('resumed')).toBe(false)
  })

  it('names an unnamed account by its tool and the end of its reference', () => {
    expect(profileLabel(profile(from, 'claude'))).toBe(
      `claude ${from.slice(-6)}`,
    )
  })
})

describe('RunActions — Move to…', () => {
  const caps: Capability[] = [{ id: 'resume', available: true }]

  it('is offered beside Resume for a stopped Claude Code session with a conversation', async () => {
    const user = userEvent.setup()
    show(<RunActions run={run()} caps={caps} onClose={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: 'Move to…' }))
    expect(
      await screen.findByRole('dialog', { name: 'Move this session' }),
    ).toBeInTheDocument()
  })

  it('reads nothing until it is opened', () => {
    show(<RunActions run={run()} caps={caps} onClose={vi.fn()} />)
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(api.handoffPreview).not.toHaveBeenCalled()
  })

  it('is not offered where Resume is not, nor for a session of another tool', () => {
    show(<RunActions run={run()} caps={[]} onClose={vi.fn()} />)
    expect(screen.queryByRole('button', { name: 'Move to…' })).toBeNull()
    show(
      <RunActions
        run={run({ provider_driver: 'codex' })}
        caps={caps}
        onClose={vi.fn()}
      />,
    )
    expect(screen.queryByRole('button', { name: 'Move to…' })).toBeNull()
  })
})
