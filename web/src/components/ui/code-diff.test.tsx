// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
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
})
