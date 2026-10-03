// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen, waitFor } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import type { LiveDTO } from './types'
import { mergeSessions } from './provenance'
import './i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 't1',
  can: (_p: string) => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
const setPeers = vi.hoisted(() => vi.fn())
vi.mock('@/features/agentops/api', async (original) => {
  const real = await original<typeof import('@/features/agentops/api')>()
  return { ...real, agentOpsApi: { ...real.agentOpsApi, setPeers } }
})

import { CanMessage, peerCandidates } from './can-message'

function launched(
  ref: string,
  osn: string,
  over: Partial<RunDTO> = {},
): { run: RunDTO; live: LiveDTO } {
  const run = {
    run_ref: ref,
    state: 'running',
    transport: 'stream-json',
    provider_profile_ref: 'ppf_1',
    live_ref: `lr-${ref}`,
    workspace_ref: 'ws-app',
    authz_workspace_id: 'aw-1',
    name: ref,
    peers: [],
    ...over,
  } as unknown as RunDTO
  const live = {
    session_ref: `claude-${ref}`,
    live_ref: `lr-${ref}`,
    attribution: 'managed',
    canonical_sid: osn,
    run_ref: ref,
    cc_state: 'active',
  } as unknown as LiveDTO
  return { run, live }
}

const a = launched('A', 'osn_a')
const b = launched('B', 'osn_b')
// Another folder, the same workspace: a peer since MC 850beb3d.
const c = launched('C', 'osn_c', { workspace_ref: 'ws-other' })
const d = launched('D', 'osn_d', { state: 'stopped' })
// The same folder, another workspace: never a peer.
const e = launched('E', 'osn_e', { authz_workspace_id: 'aw-2' })
const sessions = mergeSessions(
  [a.live, b.live, c.live, d.live, e.live],
  [a.run, b.run, c.run, d.run, e.run],
)
const self = sessions.find((s) => s.runs[0]?.run_ref === 'A')!

beforeEach(() => {
  setPeers.mockReset()
  setPeers.mockResolvedValue({ ...a.run, peers: ['osn_b'] })
})

describe('Can message (COMMS-PATH #5)', () => {
  it('offers the other live sessions of the same workspace, whatever their folder', () => {
    expect(peerCandidates(self, sessions).map((p) => p.sid)).toEqual([
      'osn_b',
      'osn_c',
    ])
  })

  it('offers nothing when the engine recorded no workspace for this run', () => {
    const own = mergeSessions(
      [a.live],
      [{ ...a.run, authz_workspace_id: undefined }],
    )[0]!
    expect(peerCandidates(own, sessions)).toEqual([])
  })

  it('sends the chosen sessions as peers', async () => {
    const user = userEvent.setup()
    renderIntel(<CanMessage session={self} sessions={sessions} />)
    await user.click(
      screen.getByRole('button', { name: 'Can message no other session' }),
    )
    await user.click(screen.getByRole('checkbox', { name: 'B' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(setPeers).toHaveBeenCalledWith('A', { peers: ['osn_b'] }),
    )
  })

  it('sends the template rule alone when chosen', async () => {
    const user = userEvent.setup()
    renderIntel(<CanMessage session={self} sessions={sessions} />)
    await user.click(
      screen.getByRole('button', { name: 'Can message no other session' }),
    )
    await user.click(
      screen.getByRole('checkbox', { name: 'Sessions from this template' }),
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(setPeers).toHaveBeenCalledWith('A', {
        peers_rule: 'same-template',
      }),
    )
  })

  it('says what it counts: no other session, N other sessions, or the template', () => {
    const labelFor = (over: Partial<RunDTO>) => {
      const one = mergeSessions([a.live], [{ ...a.run, ...over } as RunDTO])[0]!
      const view = renderIntel(<CanMessage session={one} sessions={sessions} />)
      const text = screen.getByTestId('can-message').textContent
      view.unmount()
      return text
    }
    expect(labelFor({ peers: [] })).toBe('Can message no other session')
    expect(labelFor({ peers: ['osn_b'] })).toBe('Can message 1 other session')
    expect(labelFor({ peers: ['osn_b', 'osn_c'] })).toBe(
      'Can message 2 other sessions',
    )
    expect(
      labelFor({ peers: [], peers_rule: 'same-template' } as Partial<RunDTO>),
    ).toBe('Can message sessions from this template')
  })

  it("keeps the engine's refusal beside Save, and the choice open", async () => {
    setPeers.mockRejectedValue(
      new ApiError(
        422,
        'unprocessable',
        'peer must be another readable live session in this authorization workspace',
      ),
    )
    const user = userEvent.setup()
    renderIntel(<CanMessage session={self} sessions={sessions} />)
    await user.click(
      screen.getByRole('button', { name: 'Can message no other session' }),
    )
    await user.click(screen.getByRole('checkbox', { name: 'C' }))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'peer must be another readable live session in this authorization workspace',
    )
    expect(screen.getByRole('checkbox', { name: 'C' })).toBeChecked()
  })

  it('is not offered on an engine whose runs carry no peers field', () => {
    const old = mergeSessions(
      [a.live],
      [{ ...a.run, peers: undefined } as RunDTO],
    )[0]!
    renderIntel(<CanMessage session={old} sessions={[old]} />)
    expect(screen.queryByTestId('can-message')).toBeNull()
  })
})
