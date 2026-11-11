// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  STASH_ALL_MAX_BYTES,
  STASH_MAX_BYTES,
  clearSessionDrafts,
  keepSessionDraftsOf,
  readPromptHistory,
  readSessionDraft,
  rememberPrompt,
  sessionDraftKey,
  stashDraft,
  stashedDrafts,
  subscribeSessionDrafts,
  unstashDraft,
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
  localStorage.clear()
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

// A draft set aside lives in the storage every tab of this browser shares, under the
// person's address; it is bounded, it expires, and it is never another person's.
describe('drafts set aside', () => {
  const draft = (text: string) => ({ text, attachments: [] })
  const bob = sessionDraftKey(
    'tenant',
    { kind: 'user', actor: 'user:bob', user_id: 'bob' },
    'run',
  )!
  const otherTenant = sessionDraftKey('tenant-b', principal, 'run')!
  const otherRun = sessionDraftKey('tenant', principal, 'run-2')!
  const quota = () => new DOMException('Storage full', 'QuotaExceededError')

  it('keeps a draft set aside for a second tab of the same browser', () => {
    expect(stashDraft(key, draft('Half a thought'))).toEqual({
      ok: true,
      dropped: [],
    })
    // A second tab has its own tab storage and shares the browser's.
    sessionStorage.clear()
    expect(stashedDrafts(key)).toBe(1)
    expect(unstashDraft(key)).toEqual(draft('Half a thought'))
    expect(stashedDrafts(key)).toBe(0)
  })

  it('takes a draft back once: a second press finds it gone', () => {
    stashDraft(key, draft('Only one'))
    expect(unstashDraft(key)).toEqual(draft('Only one'))
    expect(unstashDraft(key)).toBeNull()
  })

  it('loses no draft another tab sets aside while this one brings one back', async () => {
    stashDraft(key, draft('First'))
    // Another tab: its own page memory, the browser's shared storage.
    vi.resetModules()
    const otherTab = await import('./session-drafts')
    const read = Storage.prototype.getItem
    let raced = false
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation((name) => {
      const value = read.call(localStorage, name)
      // The other tab sets a draft aside between this tab's read and its write.
      if (!raced && name.startsWith('olivares.stash.')) {
        raced = true
        otherTab.stashDraft(key, draft('Second'))
      }
      return value
    })
    expect(unstashDraft(key)).toEqual(draft('First'))
    vi.mocked(Storage.prototype.getItem).mockRestore()
    expect(unstashDraft(key)).toEqual(draft('Second'))
  })

  it('never shows a draft set aside to another person or in another tenant', () => {
    stashDraft(key, draft('Alice only'))
    expect(stashedDrafts(bob)).toBe(0)
    expect(unstashDraft(bob)).toBeNull()
    expect(stashedDrafts(otherTenant)).toBe(0)
    expect(unstashDraft(otherTenant)).toBeNull()
    expect(unstashDraft(key)).toEqual(draft('Alice only'))
  })

  it('drops a draft set aside more than 24 hours ago, on read', () => {
    vi.mocked(Date.now).mockReturnValue(now - 24 * 60 * 60 * 1000 - 1)
    stashDraft(key, draft('Yesterday'))
    vi.mocked(Date.now).mockReturnValue(now - 60_000)
    stashDraft(key, draft('A minute ago'))
    vi.mocked(Date.now).mockReturnValue(now)
    expect(stashedDrafts(key)).toBe(1)
    expect(unstashDraft(key)).toEqual(draft('A minute ago'))
    expect(unstashDraft(key)).toBeNull()
  })

  it('stops offering a draft it counted once that draft has aged out', () => {
    stashDraft(key, draft('Set aside a day ago'))
    const heard = vi.fn()
    const stop = subscribeSessionDrafts(heard)
    expect(stashedDrafts(key)).toBe(1)
    vi.mocked(Date.now).mockReturnValue(now + 24 * 60 * 60 * 1000 + 1)
    expect(unstashDraft(key)).toBeNull()
    expect(heard).toHaveBeenCalledOnce()
    expect(stashedDrafts(key)).toBe(0)
    stop()
  })

  it('keeps the newest 20 and names the one it dropped', () => {
    for (let n = 1; n <= 20; n++) stashDraft(key, draft(`Draft number ${n}`))
    expect(stashDraft(key, draft('Draft number 21'))).toEqual({
      ok: true,
      dropped: ['Draft number 1'],
    })
    expect(stashedDrafts(key)).toBe(20)
    expect(unstashDraft(key)).toEqual(draft('Draft number 21'))
  })

  it('keeps one session within its byte limit, the oldest dropped and named', () => {
    const half = 'x'.repeat(STASH_MAX_BYTES / 2)
    stashDraft(key, draft(`First words of the old one ${half}`))
    const result = stashDraft(key, draft(`The new one ${half}`))
    expect(result).toEqual({
      ok: true,
      dropped: ['First words of the old one…'],
    })
    expect(stashedDrafts(key)).toBe(1)
  })

  it('keeps all sessions together within their byte limit, the oldest dropped', () => {
    const big = 'y'.repeat(STASH_ALL_MAX_BYTES / 3)
    vi.mocked(Date.now).mockReturnValue(now - 2000)
    stashDraft(key, draft(`Oldest of all ${big}`))
    vi.mocked(Date.now).mockReturnValue(now - 1000)
    stashDraft(otherRun, draft(`Second ${big}`))
    vi.mocked(Date.now).mockReturnValue(now)
    const third = sessionDraftKey('tenant', principal, 'run-3')!
    expect(stashDraft(third, draft(`Third ${big}`))).toEqual({
      ok: true,
      dropped: ['Oldest of all…'],
    })
    expect(stashedDrafts(key)).toBe(0)
    expect(stashedDrafts(otherRun)).toBe(1)
    expect(stashedDrafts(third)).toBe(1)
  })

  it('counts, drops and names only this person’s drafts in this tenant against the total', () => {
    const big = 'y'.repeat(STASH_ALL_MAX_BYTES / 3)
    vi.mocked(Date.now).mockReturnValue(now - 4000)
    stashDraft(bob, draft(`Bob private words ${big}`))
    vi.mocked(Date.now).mockReturnValue(now - 3000)
    stashDraft(otherTenant, draft(`Other tenant words ${big}`))
    vi.mocked(Date.now).mockReturnValue(now - 2000)
    stashDraft(key, draft(`Alice oldest ${big}`))
    vi.mocked(Date.now).mockReturnValue(now - 1000)
    stashDraft(otherRun, draft(`Alice second ${big}`))
    vi.mocked(Date.now).mockReturnValue(now)
    const third = sessionDraftKey('tenant', principal, 'run-3')!
    const read = vi.spyOn(Storage.prototype, 'getItem')
    expect(stashDraft(third, draft(`Alice third ${big}`))).toEqual({
      ok: true,
      dropped: ['Alice oldest…'],
    })
    const foreign = read.mock.calls.filter(
      ([name]) => name.includes('user:bob') || name.includes('"tenant-b"'),
    )
    expect(foreign).toEqual([])
    expect(stashedDrafts(bob)).toBe(1)
    expect(stashedDrafts(otherTenant)).toBe(1)
    expect(stashedDrafts(key)).toBe(0)
  })

  it('refuses a draft too large to set aside and loses nothing', () => {
    stashDraft(key, draft('Kept'))
    expect(stashDraft(key, draft('z'.repeat(STASH_MAX_BYTES + 1))).ok).toBe(
      false,
    )
    expect(unstashDraft(key)).toEqual(draft('Kept'))
  })

  it('loses nothing when the browser refuses the write, and the other writes still work', () => {
    stashDraft(key, draft('Already aside'))
    vi.spyOn(Storage.prototype, 'setItem').mockImplementationOnce(() => {
      throw quota()
    })
    expect(stashDraft(key, draft('Refused')).ok).toBe(false)
    expect(stashedDrafts(key)).toBe(1)
    expect(writeSessionDraft(key, 'Unsent words')).toBe(true)
    expect(readSessionDraft(key)).toBe('Unsent words')
    expect(stashDraft(key, draft('Next one')).ok).toBe(true)
    expect(stashedDrafts(key)).toBe(2)
  })

  it('sets nothing aside where the browser keeps no shared storage, and says so', () => {
    vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => {
      throw new DOMException('Storage disabled', 'SecurityError')
    })
    expect(stashDraft(key, draft('Private window')).ok).toBe(false)
    expect(stashedDrafts(key)).toBe(0)
    expect(unstashDraft(key)).toBeNull()
    expect(() => clearSessionDrafts()).not.toThrow()
    expect(writeSessionDraft(key, 'Still a draft')).toBe(true)
  })

  it('tells its readers when another tab sets a draft aside or takes one back', () => {
    const heard = vi.fn()
    const stop = subscribeSessionDrafts(heard)
    window.dispatchEvent(
      new StorageEvent('storage', { key: 'olivares.stash.v1.anything' }),
    )
    window.dispatchEvent(new StorageEvent('storage', { key: 'theme' }))
    expect(heard).toHaveBeenCalledOnce()
    stop()
    window.dispatchEvent(new StorageEvent('storage', { key: null }))
    expect(heard).toHaveBeenCalledOnce()
  })

  it('clears the drafts set aside at sign-out, in the storage the tabs share too', () => {
    stashDraft(key, draft('Before sign-out'))
    stashDraft(otherRun, draft('Another session'))
    localStorage.setItem('other-setting', 'keep')
    clearSessionDrafts()
    expect(stashedDrafts(key)).toBe(0)
    expect(stashedDrafts(otherRun)).toBe(0)
    expect(localStorage.getItem('other-setting')).toBe('keep')
  })

  it('clears another person’s drafts, history and drafts set aside when the person changes', () => {
    writeSessionDraft(bob, 'Bob draft')
    rememberPrompt(bob, 'Bob prompt')
    stashDraft(bob, draft('Bob aside'))
    writeSessionDraft(key, 'Alice draft')
    stashDraft(key, draft('Alice aside'))
    keepSessionDraftsOf(principal)
    expect(readSessionDraft(bob)).toBe('')
    expect(readPromptHistory(bob)).toEqual([])
    expect(stashedDrafts(bob)).toBe(0)
    expect(readSessionDraft(key)).toBe('Alice draft')
    expect(stashedDrafts(key)).toBe(1)
  })
})
