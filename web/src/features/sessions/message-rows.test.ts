// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { ConversationItem } from './conversation-frames'
import {
  indexLine,
  instantOf,
  itemForMessage,
  sampleMarks,
} from './message-rows'

const row = (
  id: string,
  kind: ConversationItem['kind'],
  text: string,
): ConversationItem => ({ kind, id, summary: text.slice(0, 20), text, raw: [] })

describe('indexLine', () => {
  it('is the first line with words, trimmed', () => {
    expect(indexLine('\n  \n  Plan the migration \nthen list the risks')).toBe(
      'Plan the migration',
    )
    expect(indexLine('   ')).toBe('')
  })

  it('cuts a long line at 160 characters, counting characters and not bytes', () => {
    const long = '漢'.repeat(170)
    expect(indexLine(long)).toBe('漢'.repeat(160) + '…')
    expect(indexLine('x'.repeat(160))).toBe('x'.repeat(160))
  })
})

describe('itemForMessage', () => {
  const rows = [
    row('a', 'operator', 'continue'),
    row('b', 'assistant', 'continue'),
    row('c', 'operator', 'continue'),
    row('d', 'tool', 'continue'),
    row('e', 'operator', 'continue\nwith more'),
  ]

  it('finds the row of the same sender with the same first line', () => {
    expect(
      itemForMessage(rows, { role: 'assistant', first_line: 'continue' })?.id,
    ).toBe('b')
    expect(
      itemForMessage(rows, { role: 'user', first_line: 'absent' }),
    ).toBeNull()
  })

  it('takes the equal row that many from the end, as the engine counts the later ones', () => {
    const at = (later_same: number) =>
      itemForMessage(rows, { role: 'user', first_line: 'continue', later_same })
        ?.id ?? null
    expect(at(0)).toBe('e')
    expect(at(1)).toBe('c')
    expect(at(2)).toBe('a')
    // A conversation that holds only the newest part has no row for the oldest.
    expect(at(3)).toBeNull()
  })
})

describe('sampleMarks', () => {
  const list = Array.from({ length: 100 }, (_, i) => i)

  it('keeps everything a rail can hold', () => {
    expect(sampleMarks([1, 2, 3], 24)).toEqual([1, 2, 3])
  })

  it('spreads a long session evenly with its first and last message always in', () => {
    const marks = sampleMarks(list, 5)
    expect(marks).toEqual([0, 25, 50, 74, 99])
    expect(sampleMarks(list, 24)).toHaveLength(24)
  })
})

describe('instantOf', () => {
  it('reads a datetime-local value as the person’s own time', () => {
    expect(instantOf('2026-10-14T10:30')).toBe(
      new Date('2026-10-14T10:30').toISOString(),
    )
    expect(instantOf('')).toBeUndefined()
    expect(instantOf('not a time')).toBeUndefined()
  })
})
