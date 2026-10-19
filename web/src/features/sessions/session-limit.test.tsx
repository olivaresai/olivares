// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A turn the tool ended on a limit: the header says "Out of quota until HH:MM" and offers
// Move to. A session that is still running is stopped first, because the engine moves
// only a stopped one; a rate limit is told but not moved.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { api, toast } = vi.hoisted(() => ({
  api: {
    listProfiles: vi.fn(),
    move: vi.fn(),
    stop: vi.fn(),
    handoffPreview: vi.fn(),
    handoff: vi.fn(),
  },
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/agentops/auth-boundary', () => ({
  useAuthBoundary: () => ({ epoch: 1 }),
}))
vi.mock('@/features/agentops/api', async (orig) => ({
  ...(await orig<typeof import('@/features/agentops/api')>()),
  agentOpsApi: api,
}))
vi.mock('@/components/ui/toaster', () => ({ toast }))

import type { RunDTO } from '@/features/agentops/types'
import type { Capability } from './provenance'
import { LimitNotice } from './session-limit'
import { limitSentence } from './session-limit-text'
import i18n from 'i18next'
import './i18n'

const caps = (...available: Capability['id'][]): Capability[] =>
  available.map((id) => ({ id, available: true }))

const run = (over: Partial<RunDTO> = {}) =>
  ({
    run_ref: 'run_1',
    state: 'running',
    transport: 'stream-json',
    provider_driver: 'claude',
    provider_profile_ref: 'ppf_a',
    claude_session_id: 'sid-1',
    limit: {
      kind: 'usage',
      resets_at: new Date(Date.now() + 90 * 60_000).toISOString(),
      account_ref: 'ppf_a',
    },
    ...over,
  }) as unknown as RunDTO

function show(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listProfiles.mockResolvedValue({
    items: [
      {
        profile_ref: 'ppf_a',
        driver: 'claude',
        display_name: 'Account A',
        state: 'active',
        local_environment: true,
        operable: true,
        environment_ref: 'env1',
      },
      {
        profile_ref: 'ppf_b',
        driver: 'claude',
        display_name: 'Account B',
        state: 'active',
        local_environment: true,
        operable: true,
        environment_ref: 'env1',
      },
    ],
    has_more: false,
  })
  api.stop.mockResolvedValue({})
  api.move.mockResolvedValue({})
})

describe('limitSentence', () => {
  const t = i18n.getFixedT('en', 'sessions')
  const now = new Date(2026, 9, 14, 12, 0)
  it('names the reset time, with the day when it is not today', () => {
    const today = new Date(2026, 9, 14, 15, 45)
    const later = new Date(2026, 9, 15, 9, 30)
    expect(
      limitSentence({ kind: 'usage', resets_at: today.toISOString() }, t, now),
    ).toBe(
      `Out of quota until ${today.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' })}`,
    )
    expect(
      limitSentence({ kind: 'usage', resets_at: later.toISOString() }, t, now),
    ).toBe(
      `Out of quota until ${later.toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short' })}`,
    )
  })
  it('says what it knows when the tool named no time', () => {
    expect(limitSentence({ kind: 'usage' }, t, now)).toBe('Out of quota')
    expect(limitSentence({ kind: 'usage', resets_at: 'soon' }, t, now)).toBe(
      'Out of quota',
    )
    expect(limitSentence({ kind: 'rate' }, t, now)).toBe(
      'Rate limited by the provider. Try again in a moment.',
    )
  })
  it('resolves its keys from the sessions bundle whatever namespace t is bound to', () => {
    const common = i18n.getFixedT('en', 'common')
    expect(limitSentence({ kind: 'usage' }, common, now)).toBe('Out of quota')
    expect(limitSentence({ kind: 'rate' }, common, now)).toBe(
      'Rate limited by the provider. Try again in a moment.',
    )
    const today = new Date(2026, 9, 14, 15, 45)
    expect(
      limitSentence(
        { kind: 'usage', resets_at: today.toISOString() },
        common,
        now,
      ),
    ).toBe(
      `Out of quota until ${today.toLocaleString(undefined, { hour: '2-digit', minute: '2-digit' })}`,
    )
  })
})

describe('LimitNotice', () => {
  it('shows nothing once the limit has lifted', () => {
    show(
      <LimitNotice
        run={run({
          limit: {
            kind: 'usage',
            resets_at: new Date(Date.now() - 60_000).toISOString(),
          },
        })}
        caps={caps('stop')}
      />,
    )
    expect(screen.queryByTestId('thread-limit')).toBeNull()
  })

  it('shows nothing for a session with no limit', () => {
    show(<LimitNotice run={run({ limit: undefined })} caps={caps('stop')} />)
    expect(screen.queryByTestId('thread-limit')).toBeNull()
  })

  it('tells when the session stopped but the move failed', async () => {
    const user = userEvent.setup()
    api.move.mockRejectedValueOnce(new Error('no'))
    show(<LimitNotice run={run()} caps={caps('stop')} />)
    await user.click(screen.getByRole('button', { name: 'Move to…' }))
    await user.click(await screen.findByRole('combobox', { name: 'Account' }))
    await user.click(await screen.findByRole('option', { name: /Account B/ }))
    await user.click(screen.getByRole('button', { name: 'Move' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'The session was stopped, but the move failed. Try the move again.',
      ),
    )
    expect(api.stop).toHaveBeenCalledTimes(1)
  })

  it('stops a running session, then moves it to the chosen account', async () => {
    const user = userEvent.setup()
    show(<LimitNotice run={run()} caps={caps('stop')} />)
    expect(screen.getByTestId('thread-limit')).toHaveTextContent(
      /Out of quota until/,
    )
    await user.click(screen.getByRole('button', { name: 'Move to…' }))
    expect(
      await screen.findByText(/it is stopped first, then moved/),
    ).toBeInTheDocument()
    await user.click(await screen.findByRole('combobox', { name: 'Account' }))
    await user.click(await screen.findByRole('option', { name: /Account B/ }))
    await user.click(screen.getByRole('button', { name: 'Move' }))
    await waitFor(() => expect(api.move).toHaveBeenCalledWith('run_1', 'ppf_b'))
    expect(api.stop).toHaveBeenCalledTimes(1)
    expect(api.stop.mock.calls[0][0]).toBe('run_1')
    expect(api.stop.mock.invocationCallOrder[0]).toBeLessThan(
      api.move.mock.invocationCallOrder[0],
    )
  })

  it('moves a stopped session without stopping it', async () => {
    const user = userEvent.setup()
    show(<LimitNotice run={run({ state: 'stopped' })} caps={caps('resume')} />)
    await user.click(screen.getByRole('button', { name: 'Move to…' }))
    expect(screen.queryByText(/it is stopped first/)).toBeNull()
    await user.click(await screen.findByRole('combobox', { name: 'Account' }))
    await user.click(await screen.findByRole('option', { name: /Account B/ }))
    await user.click(screen.getByRole('button', { name: 'Move' }))
    await waitFor(() => expect(api.move).toHaveBeenCalledWith('run_1', 'ppf_b'))
    expect(api.stop).not.toHaveBeenCalled()
  })

  it('offers no move for a rate limit, a tool that cannot move, or a person who cannot control it', () => {
    const { unmount } = show(
      <LimitNotice
        run={run({ limit: { kind: 'rate', account_ref: 'ppf_a' } })}
        caps={caps('stop')}
      />,
    )
    expect(screen.getByTestId('thread-limit')).toHaveTextContent(
      'Rate limited by the provider',
    )
    expect(screen.queryByRole('button', { name: 'Move to…' })).toBeNull()
    unmount()

    const other = show(
      <LimitNotice
        run={run({ provider_driver: 'codex' })}
        caps={caps('stop')}
      />,
    )
    expect(screen.getByTestId('thread-limit')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Move to…' })).toBeNull()
    other.unmount()

    show(<LimitNotice run={run()} caps={[]} />)
    expect(screen.queryByRole('button', { name: 'Move to…' })).toBeNull()
  })
})
