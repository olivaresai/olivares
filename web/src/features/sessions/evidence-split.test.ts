// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE THREE READINGS of one timeline page.
import { describe, expect, it } from 'vitest'
import type { TimelineDTO } from './types'
import { groupEntries, rowWords, splitEvidence } from './evidence-split'

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

describe('groupEntries', () => {
  const gap = (at: string, over: Partial<TimelineDTO> = {}) =>
    entry({ kind: 'finding', title: 'cooperative trace gap', at, ...over })

  it('folds identical checks into one row with a count and the time range', () => {
    const groups = groupEntries([
      gap('2026-10-15T21:20:00Z'),
      gap('2026-10-15T21:10:00Z'),
      gap('2026-10-15T21:15:00Z'),
    ])
    expect(groups).toHaveLength(1)
    expect(groups[0]).toMatchObject({
      label: 'cooperative trace gap',
      count: 3,
      newest: '2026-10-15T21:20:00Z',
      oldest: '2026-10-15T21:10:00Z',
    })
    expect(groups[0].entries).toHaveLength(3)
  })

  it('tells a range only when the checks are a minute or more apart', () => {
    const [close] = groupEntries([
      gap('2026-10-15T21:20:00Z'),
      gap('2026-10-15T21:20:40Z'),
    ])
    expect(close.ranged).toBe(false)
    const [far] = groupEntries([
      gap('2026-10-15T21:20:00Z'),
      gap('2026-10-15T21:21:00Z'),
    ])
    expect(far.ranged).toBe(true)
  })

  it('does not fold a title with a colon into another title and resource', () => {
    const groups = groupEntries([
      gap('2026-10-15T21:20:00Z', { title: 'a:b', resource_ref: 'c' }),
      gap('2026-10-15T21:20:00Z', { title: 'a', resource_ref: 'b:c' }),
    ])
    expect(groups).toHaveLength(2)
    expect(new Set(groups.map((g) => g.key)).size).toBe(2)
    expect(groups.map((g) => g.resource).sort()).toEqual(['b:c', 'c'])
  })

  it('never tells an unparseable time as a range or as the newest', () => {
    const [group] = groupEntries([
      gap('not-a-time'),
      gap('2026-10-15T21:20:00Z'),
      gap('2026-10-14T09:00:00Z'),
    ])
    expect(group.newest).toBe('2026-10-15T21:20:00Z')
    expect(group.oldest).toBe('2026-10-14T09:00:00Z')
    expect(group.ranged).toBe(true)
    const [lone] = groupEntries([gap('not-a-time'), gap('also-not')])
    expect(lone.ranged).toBe(false)
  })

  it('keeps different checks apart, newest group first', () => {
    const groups = groupEntries([
      gap('2026-10-15T21:20:00Z'),
      entry({
        kind: 'finding',
        title: 'secret in a prompt',
        at: '2026-10-15T21:25:00Z',
      }),
      gap('2026-10-15T21:10:00Z'),
    ])
    expect(groups.map((g) => [g.label, g.count])).toEqual([
      ['secret in a prompt', 1],
      ['cooperative trace gap', 2],
    ])
  })

  it('does not fold the same title on different resources', () => {
    const groups = groupEntries([
      gap('2026-10-15T21:20:00Z', { resource_ref: 'repo/a' }),
      gap('2026-10-15T21:20:00Z', { resource_ref: 'repo/b' }),
    ])
    expect(groups.map((g) => g.count)).toEqual([1, 1])
  })
})

describe('groupEntries on turns (#2267 review)', () => {
  const call = (at: string, over: Partial<TimelineDTO> = {}) =>
    entry({ kind: 'mcp', tool_ref: 'github/create_issue', at, ...over })

  it('folds a call that wrote two entries a second apart into one turn with its count', () => {
    const groups = groupEntries([
      call('2026-10-15T21:20:35Z'),
      call('2026-10-15T21:20:34Z'),
      entry({ kind: 'tool', tool_ref: 'Read', at: '2026-10-15T21:21:00Z' }),
    ])
    expect(groups.map((g) => [g.label, g.count, g.ranged])).toEqual([
      ['Read', 1, false],
      ['github/create_issue', 2, false],
    ])
  })

  it('keeps a tool call and an MCP call with the same words apart', () => {
    const groups = groupEntries([
      call('2026-10-15T21:20:35Z', { kind: 'tool' }),
      call('2026-10-15T21:20:35Z', { kind: 'mcp' }),
    ])
    expect(groups.map((g) => [g.kind, g.count])).toEqual([
      ['tool', 1],
      ['mcp', 1],
    ])
  })

  it('uses a key that cannot collide when a title holds the separator of another scheme', () => {
    const groups = groupEntries([
      call('2026-10-15T21:20:35Z', { tool_ref: 'a\u0000b', resource_ref: 'c' }),
      call('2026-10-15T21:20:35Z', { tool_ref: 'a', resource_ref: 'b\u0000c' }),
    ])
    expect(groups).toHaveLength(2)
  })
})

describe('rowWords', () => {
  it('says nothing under the words when the resource is the title or ends it', () => {
    expect(rowWords('tool', 'github', 'github')).toEqual({
      title: 'github',
      subtitle: '',
    })
    expect(rowWords('tool', 'Task worker-indexer', 'worker-indexer')).toEqual({
      title: 'Task worker-indexer',
      subtitle: '',
    })
  })

  it('uses the qualified name as the words when an MCP call only adds its server', () => {
    expect(rowWords('mcp', 'create_issue', 'github/create_issue')).toEqual({
      title: 'github/create_issue',
      subtitle: '',
    })
  })

  it('keeps the tool name when the resource is only a path that ends in it', () => {
    expect(rowWords('tool', 'Read', 'src/Read')).toEqual({
      title: 'Read',
      subtitle: 'src/Read',
    })
  })

  it('keeps a resource that merely shares the tail of the words', () => {
    expect(rowWords('tool', 'Spread', 'read').subtitle).toBe('read')
    expect(rowWords('tool', 'xgithub', 'github').subtitle).toBe('github')
    expect(rowWords('tool', 'github', 'hub').subtitle).toBe('hub')
  })

  it('keeps a resource that tells something the title does not', () => {
    expect(rowWords('tool', 'Read', 'src/Read.go')).toEqual({
      title: 'Read',
      subtitle: 'src/Read.go',
    })
    expect(rowWords('tool', 'worker-indexer', 'Task worker-indexer')).toEqual({
      title: 'worker-indexer',
      subtitle: 'Task worker-indexer',
    })
    expect(rowWords('tool', 'Read', undefined).subtitle).toBe('')
    expect(rowWords('tool', 'Read', '  ').subtitle).toBe('')
  })
})
