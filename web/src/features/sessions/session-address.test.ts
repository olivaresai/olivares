// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ADDRESS OF ONE SESSION. Pure, so every rung of the decoder can be driven
// directly: a URL is typed, pasted, edited and forged, and the whole value of this
// module is what it does with input nobody wrote on purpose.
import { describe, expect, it } from 'vitest'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import {
  DEFAULT_EVIDENCE,
  DEFAULT_PANE,
  MAX_ADDRESS_LENGTH,
  addressOf,
  decodeSessionAddress,
  isSessionAddress,
  targetFromAddress,
} from './session-address'

describe('targetFromAddress — the three shapes a row key has', () => {
  it('resolves a profile-scoped row by its own opaque id', () => {
    expect(targetFromAddress('live:01a0b0b2-e581-71b8')).toEqual({
      liveRef: '01a0b0b2-e581-71b8',
    })
  })

  it('resolves a legacy row by its bare provider id', () => {
    expect(targetFromAddress('sess:sess-coder-7a3f')).toEqual({
      sessionRef: 'sess-coder-7a3f',
    })
  })

  it('resolves a run whose managed row is not proven yet', () => {
    expect(targetFromAddress('run:run-42')).toEqual({ runRef: 'run-42' })
  })

  it('keeps every colon after the first, because a provider id may hold one', () => {
    expect(targetFromAddress('sess:urn:agent:7a3f')).toEqual({
      sessionRef: 'urn:agent:7a3f',
    })
  })

  it.each([
    ['', 'empty'],
    ['live', 'no separator'],
    ['live:', 'no reference'],
    ['live:   ', 'a blank reference'],
    [':7a3f', 'no kind'],
    ['agent:7a3f', 'a kind this plane does not mint'],
    [
      'LIVE:7a3f',
      'the kind in another case — the key is a literal, not a word',
    ],
  ])('refuses %o (%s)', (address) => {
    expect(targetFromAddress(address)).toBeNull()
    expect(isSessionAddress(address)).toBe(false)
  })

  it('refuses an address longer than the ceiling', () => {
    const long = `live:${'a'.repeat(MAX_ADDRESS_LENGTH)}`
    expect(long.length).toBeGreaterThan(MAX_ADDRESS_LENGTH)
    expect(targetFromAddress(long)).toBeNull()
    // …and the one AT the ceiling still resolves, so it is a ceiling and not a ban.
    const ok = `live:${'a'.repeat(MAX_ADDRESS_LENGTH - 'live:'.length)}`
    expect(ok.length).toBe(MAX_ADDRESS_LENGTH)
    expect(targetFromAddress(ok)).not.toBeNull()
  })
})

/** A row shaped like the ones `serve --seed-demo` returns (measured). */
function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-coder-7a3f',
    live_ref: '01a0b0b2-e581-71b8-a163-5f73c7ed6aaa',
    attribution: 'legacy',
    cc_state: 'idle',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:01:00Z',
    duration_seconds: 60,
    ...over,
  }
}

describe('the address IS the row key, and the round trip proves it', () => {
  // THE ASSERTION THAT MATTERS. A second identifier minted for the URL would drift
  // from the one the join already gives every row. This drives the REAL join, so the
  // two cannot diverge without this test saying so.
  it.each([
    ['a legacy row', live()],
    ['a profile-scoped row', live({ attribution: 'observed' })],
    ['a managed row', live({ attribution: 'managed' })],
  ])('round-trips %s through the URL and back to a target', (_name, row) => {
    const [session] = mergeSessions([row], [])
    const address = addressOf(session)
    expect(isSessionAddress(address)).toBe(true)

    const target = targetFromAddress(address)
    // A cold deep link has no list. What comes back must be enough for the card to
    // ask the engine by itself, which is exactly one non-empty reference.
    const refs = Object.values(target ?? {}).filter(Boolean)
    expect(refs).toHaveLength(1)
    expect(target).toEqual(
      row.attribution === 'legacy'
        ? { sessionRef: row.session_ref }
        : { liveRef: row.live_ref },
    )
  })

  it('survives a URL round trip, which is where the separator could have been eaten', () => {
    const [session] = mergeSessions([live({ attribution: 'observed' })], [])
    const address = addressOf(session)
    const url = new URL('https://example.invalid/sessions')
    url.searchParams.set('session', address)
    const read = new URL(url.toString()).searchParams.get('session')
    expect(read).toBe(address)
    expect(targetFromAddress(read ?? '')).toEqual({ liveRef: live().live_ref })
  })
})

describe('decodeSessionAddress — a bad value falls back AND says which', () => {
  it('reads all three owned keys', () => {
    expect(
      decodeSessionAddress({
        session: 'live:abc',
        pane: 'context',
        evidence: 'activity',
      }),
    ).toEqual({
      value: { address: 'live:abc', pane: 'context', evidence: 'activity' },
      issues: [],
    })
  })

  it('an absent key is not an issue — it is the default', () => {
    expect(decodeSessionAddress({})).toEqual({
      value: { address: null, pane: DEFAULT_PANE, evidence: DEFAULT_EVIDENCE },
      issues: [],
    })
  })

  it('reports every refused key, and falls back on each', () => {
    const { value, issues } = decodeSessionAddress({
      session: 'nonsense',
      pane: 'diff',
      evidence: 'files',
    })
    expect(value).toEqual({
      address: null,
      pane: DEFAULT_PANE,
      evidence: DEFAULT_EVIDENCE,
    })
    expect(issues).toEqual(['session', 'pane', 'evidence'])
  })

  it('keeps a valid pane when the session is gone, rather than teaching a half-wrong URL', () => {
    const { value, issues } = decodeSessionAddress({ pane: 'narrative' })
    expect(value.pane).toBe('narrative')
    expect(value.address).toBeNull()
    expect(issues).toEqual([])
  })

  it('never throws on anything a URL can hold', () => {
    const nul = String.fromCharCode(0)
    for (const bad of ['%%%', '../../etc/passwd', '<script>', nul, '1']) {
      expect(() =>
        decodeSessionAddress({ session: bad, pane: bad, evidence: bad }),
      ).not.toThrow()
    }
  })
})
