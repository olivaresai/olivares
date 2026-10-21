// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  clearSessionDrafts,
  readSessionDraft,
  sessionDraftKey,
  writeSessionDraft,
} from './session-drafts'

const principal = {
  kind: 'user' as const,
  actor: 'user:alice',
  user_id: 'alice',
}
const key = sessionDraftKey('tenant', principal, 'run')!
const now = Date.UTC(2026, 9, 14, 9)

beforeEach(() => {
  clearSessionDrafts()
  sessionStorage.clear()
  vi.spyOn(Date, 'now').mockReturnValue(now)
})
afterEach(() => vi.restoreAllMocks())

describe('session draft storage', () => {
  it('restores a draft at 24 hours without renewing its age', () => {
    const raw = JSON.stringify({
      text: 'Full draft\nwith a second line',
      updatedAt: now - 24 * 60 * 60 * 1000,
    })
    sessionStorage.setItem(key, raw)
    expect(readSessionDraft(key)).toBe('Full draft\nwith a second line')
    expect(sessionStorage.getItem(key)).toBe(raw)
  })

  it.each([
    'not json',
    'null',
    JSON.stringify({ text: 17, updatedAt: now }),
    JSON.stringify({ text: 'Missing time' }),
    JSON.stringify({ text: 'Invalid time', updatedAt: 'today' }),
    JSON.stringify({ text: 'Future time', updatedAt: now + 1 }),
  ])('drops malformed draft data without restoring it: %s', (raw) => {
    sessionStorage.setItem(key, raw)
    expect(readSessionDraft(key)).toBe('')
    expect(sessionStorage.getItem(key)).toBeNull()
  })

  it('distinguishes token principals from the user that created them', () => {
    writeSessionDraft(key, 'User draft')
    const tokenKey = sessionDraftKey(
      'tenant',
      { ...principal, kind: 'token', actor: 'token:one' },
      'run',
    )
    expect(readSessionDraft(tokenKey)).toBe('')
    writeSessionDraft(tokenKey, 'Token draft')
    expect(readSessionDraft(key)).toBe('User draft')
  })

  it('does not alias addresses containing key delimiters', () => {
    expect(sessionDraftKey('tenant|user', principal, 'run')).not.toBe(
      sessionDraftKey('tenant', { ...principal, user_id: 'user|alice' }, 'run'),
    )
    expect(sessionDraftKey(null, principal, 'run')).toBeNull()
    expect(sessionDraftKey('tenant', null, 'run')).toBeNull()
  })

  it('allows a browser with disabled storage to edit, send and sign out', () => {
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new DOMException('Storage disabled', 'SecurityError')
    })
    expect(readSessionDraft(key)).toBe('')
    expect(writeSessionDraft(key, 'Draft')).toBe(false)
    expect(() => clearSessionDrafts()).not.toThrow()
  })

  it('keeps the newest refused write for this tab when storage cannot persist', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    setItem.mockImplementation(() => {
      throw new DOMException('Storage full', 'QuotaExceededError')
    })
    expect(writeSessionDraft(key, 'Refused draft')).toBe(false)
    expect(readSessionDraft(key)).toBe('Refused draft')
    setItem.mockRestore()
    expect(writeSessionDraft(key, 'Recovered draft')).toBe(true)
    expect(readSessionDraft(key)).toBe('Recovered draft')
    expect(writeSessionDraft(key, '')).toBe(true)
    expect(readSessionDraft(key)).toBe('')
  })

  it('keeps sweeping drafts at sign-out when one entry is locked', () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem')
    setItem.mockImplementation(() => {
      throw new DOMException('Storage full', 'QuotaExceededError')
    })
    writeSessionDraft(key, 'Refused draft')
    setItem.mockRestore()
    const locked = sessionDraftKey('tenant', principal, 'run-locked')!
    const open = sessionDraftKey('tenant', principal, 'run-open')!
    writeSessionDraft(locked, 'Locked draft')
    writeSessionDraft(open, 'Open draft')
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementationOnce(() => {
      throw new DOMException('Entry locked', 'InvalidStateError')
    })
    clearSessionDrafts()
    // The locked entry itself stays; the sweep still reaches the rest.
    expect(sessionStorage.getItem(open)).toBeNull()
    expect(readSessionDraft(key)).toBe('')
  })
})
