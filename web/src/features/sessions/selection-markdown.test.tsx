// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A selection of a reply goes to the clipboard as the Markdown it was: what the console
// paints from Markdown reads back as Markdown, and the code block's own controls stay out.
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MarkdownText } from './markdown-text'
import { selectionMarkdown } from './selection-markdown'

function copyOf(markdown: string): string {
  const { container } = render(<MarkdownText text={markdown} />)
  const range = document.createRange()
  range.selectNodeContents(container)
  return selectionMarkdown(range.cloneContents())
}

describe('selectionMarkdown', () => {
  it('reads inline marks, links and lists back as Markdown', () => {
    const source = [
      'Run **this** and *that* with `x`, see [docs](https://example.com/d).',
      '',
      '- one',
      '- two',
      '',
      '1. first',
      '2. second',
      '',
      '> quoted',
    ].join('\n')
    expect(copyOf(source)).toBe(source)
  })

  it('reads a fenced block back with its language and without the block controls', () => {
    const source = '```sh\nnpm test\n```'
    expect(copyOf(source)).toBe(source)
  })

  it('reads a table back', () => {
    const source = '| a | b |\n| --- | --- |\n| 1 | 2 |'
    expect(copyOf(source)).toBe(source)
  })

  it('is empty when nothing is selected', () => {
    expect(selectionMarkdown(document.createDocumentFragment())).toBe('')
  })
})
