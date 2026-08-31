// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { CodeEditor } from './code-editor'

describe('CodeEditor', () => {
  it('renders the document with an accessible textbox name', () => {
    render(
      <CodeEditor
        value={'{\n  "a": 1\n}'}
        language="json"
        ariaLabel="Managed settings"
      />,
    )
    const box = screen.getByRole('textbox', { name: 'Managed settings' })
    expect(box).toBeInTheDocument()
    // CM splits the doc across line elements; the host text still contains it.
    expect(box.textContent).toContain('"a": 1')
  })

  it('is contenteditable when writable and locked when read-only', () => {
    const { rerender } = render(
      <CodeEditor value="permit;" language="cedar" ariaLabel="Policy" />,
    )
    expect(screen.getByRole('textbox', { name: 'Policy' })).toHaveAttribute(
      'contenteditable',
      'true',
    )
    rerender(
      <CodeEditor
        value="permit;"
        language="cedar"
        ariaLabel="Policy"
        readOnly
      />,
    )
    expect(screen.getByRole('textbox', { name: 'Policy' })).toHaveAttribute(
      'contenteditable',
      'false',
    )
  })

  it('emits onChange when the user types', async () => {
    const onChange = vi.fn()
    render(
      <CodeEditor
        value=""
        language="json"
        ariaLabel="Doc"
        onChange={onChange}
      />,
    )
    const box = screen.getByRole('textbox', { name: 'Doc' })
    box.focus()
    await userEvent.type(box, 'x')
    expect(onChange).toHaveBeenCalled()
    expect(onChange.mock.calls.at(-1)?.[0]).toContain('x')
  })

  it('wires aria-describedby when provided', () => {
    render(
      <CodeEditor
        value=""
        language="rego"
        ariaLabel="Rego"
        describedById="rego-help"
      />,
    )
    expect(screen.getByRole('textbox', { name: 'Rego' })).toHaveAttribute(
      'aria-describedby',
      'rego-help',
    )
  })
})
