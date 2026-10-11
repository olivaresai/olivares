// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The CREDENTIAL GENERATION of the session lifecycle. `POST /v1/auth/refresh` rotates the
// CSRF token credential in place and answers with the SAME session id, so "did the credential
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
    csrfToken: 'csrf_first',
    sessionId: SID,
    expiresAt: EXP,
    credentialGeneration: 0,
  })
})

const generation = () => useSessionStore.getState().credentialGeneration

describe('session store — credential generation', () => {
  it('a renewal that rotates the CSRF token under the SAME session id is a new credential', () => {
    useSessionStore.getState().setSession({
      csrfToken: 'csrf_rotated',
      sessionId: SID,
      expiresAt: LATER,
    })
    const s = useSessionStore.getState()
    expect(s.sessionId).toBe(SID)
    expect(s.csrfToken).toBe('csrf_rotated')
    expect(s.credentialGeneration).toBe(1)
  })

  it('a login or a new session (new session id) is a new credential', () => {
    useSessionStore.getState().setSession({
      csrfToken: 'csrf_other',
      sessionId: 'sid-2',
      expiresAt: EXP,
    })
    expect(generation()).toBe(1)
  })

  it('the same CSRF token and session id with another expiry is NOT a new credential', () => {
    useSessionStore
      .getState()
      .setSession({ csrfToken: 'csrf_first', sessionId: SID, expiresAt: LATER })
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
      .setSession({ csrfToken: 'csrf_first', sessionId: SID, expiresAt: EXP })
    seen.push(generation())
    // Even the SAME credential as before the logout is a new one after it.
    expect(seen).toEqual([0, 1, 2])
  })

  it('keeps CSRF metadata in memory and writes no browser credential', () => {
    useSessionStore.getState().setSession({
      csrfToken: 'csrf_rotated',
      sessionId: SID,
      expiresAt: LATER,
    })
    expect(localStorage.getItem('olivares.session')).toBeNull()
  })
})

// HU-R37: a session the engine ended dropped to Sign in with no reason.
describe('session store — why a session ended', () => {
  it('says expired when its own expiry passed, and ended when it was refused earlier', () => {
    useSessionStore.setState({ expiresAt: '2000-01-01T00:00:00Z' })
    useSessionStore.getState().ended()
    expect(useSessionStore.getState().endReason).toBe('expired')
    useSessionStore.setState({
      csrfToken: 'c',
      sessionId: SID,
      expiresAt: '2999-01-01T00:00:00Z',
    })
    useSessionStore.getState().ended()
    expect(useSessionStore.getState().endReason).toBe('ended')
  })

  it('records no reason for a sign-out, and forgets it at the next sign-in', () => {
    useSessionStore.getState().clear()
    expect(useSessionStore.getState().endReason).toBeNull()
    useSessionStore.setState({ sessionId: SID })
    useSessionStore.getState().ended()
    useSessionStore
      .getState()
      .setSession({ csrfToken: 'n', sessionId: 'new', expiresAt: EXP })
    expect(useSessionStore.getState().endReason).toBeNull()
  })
})

// SR4C on 793f2e30: the client answers one request's 401 twice (the failed refresh, then the
// request itself), and the second notice erased the reason.
describe('session store — a repeated end notice', () => {
  it('keeps the reason a second notice finds, and a sign-out still clears it', () => {
    useSessionStore.setState({
      sessionId: SID,
      expiresAt: '2999-01-01T00:00:00Z',
    })
    useSessionStore.getState().ended()
    useSessionStore.getState().ended()
    expect(useSessionStore.getState().endReason).toBe('ended')
    useSessionStore.getState().clear()
    expect(useSessionStore.getState().endReason).toBeNull()
  })
})
