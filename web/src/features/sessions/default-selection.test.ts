// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The three clauses of the default, each as a case that goes red on its own mutation.
import { describe, expect, it } from 'vitest'
import { chooseDefaultAddress } from './default-selection'

const row = (key: string) => ({ key })

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
      }),
    ).toBe('live:b')
    expect(
      chooseDefaultAddress({
        settling: false,
        order: [row('sess:a')],
        retiredAddress: 'sess:a',
        latched: null,
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
      }),
    ).toBeNull()
  })
})
