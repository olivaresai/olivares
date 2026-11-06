// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The three clauses of the default, each as a case that goes red on its own mutation.
import { describe, expect, it } from 'vitest'
import { chooseDefaultAddress } from './default-selection'

const ME = 'user-me'
// A session row as the rail orders it; its newest run says whom it acts for.
const row = (
  key: string,
  actsFor: { kind: 'user' | 'automation'; user_id?: string } = {
    kind: 'user',
    user_id: ME,
  },
) => ({ key, runs: [{ acts_for: actsFor }] })

describe('chooseDefaultAddress — the surface opens on work, once', () => {
  it('defaults to nothing while either half of the list is still loading', () => {
    // The walk's finding in one line: with only the run half answered, the old code
    // defaulted to a run-keyed address and subscribed to its stream, then changed its
    // mind when the observed half arrived and aborted that subscription.
    expect(
      chooseDefaultAddress({
        settling: true,
        order: [row('sess:a'), row('live:b')],
        retiredAddress: null,
        latched: null,
        userId: ME,
      }),
    ).toBeNull()
  })

  it('takes the rail order when there is nothing latched yet', () => {
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('sess:a'), row('live:b')],
        retiredAddress: null,
        latched: null,
        userId: ME,
      }),
    ).toBe('sess:a')
  })

  it('HOLDS what it opened on when the list reorders underneath', () => {
    // The expensive half of the defect: a default that follows the list lets a session
    // that has just become "most recent" steal the pane from the one being read.
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('live:new'), row('sess:a')],
        retiredAddress: null,
        latched: 'sess:a',
        userId: ME,
      }),
    ).toBe('sess:a')
  })

  it('moves when the row it held is no longer in the list', () => {
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('live:new')],
        retiredAddress: null,
        latched: 'sess:a',
        userId: ME,
      }),
    ).toBe('live:new')
  })

  it('never holds — or chooses — an address this episode retired (R2)', () => {
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('sess:a'), row('live:b')],
        retiredAddress: 'sess:a',
        latched: 'sess:a',
        userId: ME,
      }),
    ).toBe('live:b')
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('sess:a')],
        retiredAddress: 'sess:a',
        latched: null,
        userId: ME,
      }),
    ).toBeNull()
  })

  it('answers null for an empty list, settled or not', () => {
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [],
        retiredAddress: null,
        latched: 'sess:gone',
        userId: ME,
      }),
    ).toBeNull()
  })

  // An admin with no session of their own opened /sessions and landed on a
  // dispatcher worker's live session, its Stop, Esc and composer armed. The default
  // only ever opens a session the signed-in person launched.
  it("never opens on another person's or an automation's session", () => {
    const theirs = row('run:theirs', { kind: 'user', user_id: 'user-admin-2' })
    const automation = row('run:worker', { kind: 'automation' })
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [theirs, automation, row('sess:mine')],
        retiredAddress: null,
        latched: null,
        userId: ME,
      }),
    ).toBe('sess:mine')
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [theirs, automation],
        retiredAddress: null,
        latched: null,
        userId: ME,
      }),
    ).toBeNull()
    // A latch on someone else's session (an older console, a list that changed hands)
    // is dropped, not held.
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [theirs, row('sess:mine')],
        retiredAddress: null,
        latched: 'run:theirs',
        userId: ME,
      }),
    ).toBe('sess:mine')
    // Who is signed in is not known yet: nothing is anyone's, so nothing opens.
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('sess:mine')],
        retiredAddress: null,
        latched: null,
        userId: null,
      }),
    ).toBeNull()
  })
})
