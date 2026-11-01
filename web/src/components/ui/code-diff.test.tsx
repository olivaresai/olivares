// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { CodeDiff } from './code-diff'

describe('CodeDiff', () => {
  it('renders both revisions read-only with accessible names', () => {
    render(
      <CodeDiff
        original={'{\n  "a": 1\n}'}
        modified={'{\n  "a": 2\n}'}
        language="json"
        originalLabel="Revision 1"
        modifiedLabel="Revision 2"
      />,
    )
    const left = screen.getByRole('textbox', { name: 'Revision 1' })
    const right = screen.getByRole('textbox', { name: 'Revision 2' })
    expect(left).toHaveAttribute('contenteditable', 'false')
    expect(right).toHaveAttribute('contenteditable', 'false')
    expect(left.textContent).toContain('"a": 1')
    expect(right.textContent).toContain('"a": 2')
  })

  // jsdom has no layout, so the gutter resolves every click to the first line: this
  // proves the pane and that a real line number comes back, not which line a browser
  // would pick (the capture does).
  it('reports the pane and the line whose number is clicked', async () => {
    const onLineClick = vi.fn()
    const { container } = render(
      <CodeDiff
        original={'a\nb\n'}
        modified={'a\nc\n'}
        language="text"
        originalLabel="Revision 1"
        modifiedLabel="Revision 2"
        onLineClick={onLineClick}
      />,
    )
    const gutters = container.querySelectorAll('.cm-lineNumbers')
    expect(gutters).toHaveLength(2)

    await userEvent.click(gutters[0])
    expect(onLineClick).toHaveBeenLastCalledWith('original', 1)
    await userEvent.click(gutters[1])
    expect(onLineClick).toHaveBeenLastCalledWith('modified', 1)
  })

  it('leaves the line numbers alone without a handler', async () => {
    const { container } = render(
      <CodeDiff
        original="a"
        modified="b"
        language="text"
        originalLabel="Revision 1"
        modifiedLabel="Revision 2"
      />,
    )
    await userEvent.click(container.querySelector('.cm-lineNumbers')!)
    expect(screen.getByRole('textbox', { name: 'Revision 1' })).toBeVisible()
  })
})
