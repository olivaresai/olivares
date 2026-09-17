// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The CREDENTIAL GENERATION of the session lifecycle. `POST /v1/auth/refresh` rotates the
// bearer credential in place and answers with the SAME session id, so "did the credential
// change?" cannot be read from the session id, and must not be read from the token. The
// store compares the transition where it owns the credential and exposes only a counter.
import { beforeEach, describe, expect, it } from 'vitest'
import { useSessionStore } from './session'

const EXP = '2030-01-01T00:00:00Z'
const LATER = '2030-06-01T00:00:00Z'
const SID = 'sid-fixed'

beforeEach(() => {
  localStorage.clear()
  useSessionStore.setState({
    token: 'olvs_first',
    sessionId: SID,
    expiresAt: EXP,
    credentialGeneration: 0,
  })
})

const generation = () => useSessionStore.getState().credentialGeneration

describe('session store — credential generation', () => {
  it('a renewal that rotates the bearer under the SAME session id is a new credential', () => {
    useSessionStore
      .getState()
      .setSession({ token: 'olvs_rotated', sessionId: SID, expiresAt: LATER })
    const s = useSessionStore.getState()
    expect(s.sessionId).toBe(SID)
    expect(s.token).toBe('olvs_rotated')
    expect(s.credentialGeneration).toBe(1)
  })

  it('a login or a new session (new session id) is a new credential', () => {
    useSessionStore
      .getState()
      .setSession({ token: 'olvs_other', sessionId: 'sid-2', expiresAt: EXP })
    expect(generation()).toBe(1)
  })

  it('the same bearer and session id with another expiry is NOT a new credential', () => {
    useSessionStore
      .getState()
      .setSession({ token: 'olvs_first', sessionId: SID, expiresAt: LATER })
    expect(useSessionStore.getState().expiresAt).toBe(LATER)
    expect(generation()).toBe(0)
  })

  it('ending the credential advances once; clearing nothing does not', () => {
    useSessionStore.getState().clear()
    expect(generation()).toBe(1)
    useSessionStore.getState().clear()
    expect(generation()).toBe(1)
  })

  it('logout followed by login is two moves, and the counter only ever grows', () => {
    const seen = [generation()]
    useSessionStore.getState().clear()
    seen.push(generation())
    useSessionStore
      .getState()
      .setSession({ token: 'olvs_first', sessionId: SID, expiresAt: EXP })
    seen.push(generation())
    // Even the SAME credential as before the logout is a new one after it.
    expect(seen).toEqual([0, 1, 2])
  })

  it('persists the credential exactly as before and never the generation', () => {
    useSessionStore
      .getState()
      .setSession({ token: 'olvs_rotated', sessionId: SID, expiresAt: LATER })
    const stored = JSON.parse(localStorage.getItem('olivares.session') ?? '{}')
    expect(stored.state).toEqual({
      token: 'olvs_rotated',
      sessionId: SID,
      expiresAt: LATER,
    })
    expect('credentialGeneration' in stored.state).toBe(false)
  })

  it('rehydrating a stored credential does not count as a transition', async () => {
    localStorage.setItem(
      'olivares.session',
      JSON.stringify({
        state: {
          token: 'olvs_stored',
          sessionId: 'sid-stored',
          expiresAt: EXP,
        },
        version: 0,
      }),
    )
    await useSessionStore.persist.rehydrate()
    const s = useSessionStore.getState()
    expect(s.token).toBe('olvs_stored')
    expect(s.sessionId).toBe('sid-stored')
    expect(s.credentialGeneration).toBe(0)
  })
})
