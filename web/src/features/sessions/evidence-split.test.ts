// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE THREE READINGS of one timeline page.
import { describe, expect, it } from 'vitest'
import type { TimelineDTO } from './types'
import { splitEvidence } from './evidence-split'

function entry(over: Partial<TimelineDTO> = {}): TimelineDTO {
  return { at: '2026-09-18T09:00:00Z', kind: 'tool', ...over }
}

describe('splitEvidence', () => {
  it('separates findings from turns', () => {
    const split = splitEvidence(
      [
        entry({ kind: 'finding', title: 'secret in a prompt' }),
        entry({ kind: 'tool', tool_ref: 'Read' }),
        entry({ kind: 'mcp', tool_ref: 'github/create_issue' }),
        // A cost event is neither a check nor a turn: it belongs to the facts line,
        // which is where the session's cost is already told.
        entry({ kind: 'cost' }),
      ],
      false,
    )
    expect(split.checks).toHaveLength(1)
    expect(split.activity.map((e) => e.tool_ref)).toEqual([
      'Read',
      'github/create_issue',
    ])
  })

  it('counts each resource once, most-touched first', () => {
    const split = splitEvidence(
      [
        entry({ resource_ref: 'repo/a' }),
        entry({ resource_ref: 'repo/b' }),
        entry({ resource_ref: 'repo/a' }),
      ],
      false,
    )
    expect(split.resources).toEqual([
      { ref: 'repo/a', count: 2 },
      { ref: 'repo/b', count: 1 },
    ])
  })

  it('ignores a blank resource rather than counting an empty one', () => {
    expect(
      splitEvidence([entry({ resource_ref: '   ' }), entry({})], false)
        .resources,
    ).toEqual([])
  })

  it('breaks a tie by name, so the order is stable between reads', () => {
    const split = splitEvidence(
      [entry({ resource_ref: 'b' }), entry({ resource_ref: 'a' })],
      false,
    )
    expect(split.resources.map((r) => r.ref)).toEqual(['a', 'b'])
  })

  it('reports that the page was bounded, so a count can be told as a floor', () => {
    // A count taken from a truncated page is a FLOOR. The count that lies is the one
    // that looks exact.
    expect(splitEvidence([entry()], true).bounded).toBe(true)
    expect(splitEvidence([entry()], false).bounded).toBe(false)
  })

  it('answers empty for an empty page, without throwing', () => {
    expect(splitEvidence([], false)).toEqual({
      checks: [],
      activity: [],
      resources: [],
      bounded: false,
    })
  })
})
