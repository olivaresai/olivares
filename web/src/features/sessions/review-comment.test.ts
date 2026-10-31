// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { reviewCommentText } from './review-comment'

describe('reviewCommentText', () => {
  it('names the file, the line and the version, quotes the line, then the comment', () => {
    expect(
      reviewCommentText(
        { path: 'core/a.go', line: 12, side: 'modified' },
        'for {',
        'This loop never ends.',
      ),
    ).toBe(
      'Comment on core/a.go, line 12 (at the branch tip):\n> for {\n\nThis loop never ends.',
    )
  })

  it('says "at the base" for a line of the version the branch started from', () => {
    expect(
      reviewCommentText(
        { path: 'README', line: 1, side: 'original' },
        'old',
        'Why was this removed?',
      ),
    ).toBe(
      'Comment on README, line 1 (at the base):\n> old\n\nWhy was this removed?',
    )
  })

  it('keeps a long line to one short quoted line', () => {
    const text = reviewCommentText(
      { path: 'a.min.js', line: 1, side: 'modified' },
      `${'x'.repeat(300)}\r`,
      'Too long to read.',
    )
    const quoted = text.split('\n')[1]
    expect(quoted.startsWith('> xxx')).toBe(true)
    expect(quoted.length).toBeLessThan(210)
    expect(quoted.endsWith('…')).toBe(true)
    expect(text).not.toContain('\r')
  })

  it('sends the comment as typed, trimmed at the ends only', () => {
    const text = reviewCommentText(
      { path: 'a.go', line: 3, side: 'modified' },
      'x := 1',
      '  First line.\n\n  Second line.  \n',
    )
    expect(text.endsWith('\n\nFirst line.\n\n  Second line.')).toBe(true)
  })
})
