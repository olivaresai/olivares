// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Segmented: one choice of a few, shown side by side. It is a radio group: one tab stop,
// the arrows move the choice, and every option says whether it is checked.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Segmented } from './segmented'

const OPTIONS = [
  { value: 'list', label: 'List' },
  { value: 'table', label: 'Table' },
  {
    value: 'board',
    label: 'Board',
    disabled: true as const,
    reason: 'The board view needs a work item stream.',
  },
  { value: 'map', label: 'Map' },
]

function Harness({ onChange }: { onChange?: (v: string) => void }) {
  const [value, setValue] = useState('list')
  return (
    <Segmented
      aria-label="View"
      options={OPTIONS}
      value={value}
      onValueChange={(v) => {
        setValue(v)
        onChange?.(v)
      }}
    />
  )
}

describe('Segmented', () => {
  it('is a named radio group whose options say which one is checked', () => {
    render(<Harness />)
    expect(screen.getByRole('radiogroup', { name: 'View' })).toBeVisible()
    const radios = screen.getAllByRole('radio')
    expect(radios.map((r) => r.getAttribute('aria-checked'))).toEqual([
      'true',
      'false',
      'false',
      'false',
    ])
  })

  it('is one tab stop: only the checked option is in the tab order', () => {
    render(<Harness />)
    const tabbable = screen
      .getAllByRole('radio')
      .filter((r) => r.getAttribute('tabindex') === '0')
    expect(tabbable.map((r) => r.textContent)).toEqual(['List'])
  })

  it('moves the choice with the arrows, skipping a disabled option, and wraps', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<Harness onChange={onChange} />)
    screen.getByRole('radio', { name: 'List' }).focus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByRole('radio', { name: 'Table' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    expect(screen.getByRole('radio', { name: 'Table' })).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByRole('radio', { name: 'Map' })).toHaveFocus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByRole('radio', { name: 'List' })).toHaveFocus()
    await user.keyboard('{ArrowLeft}')
    expect(screen.getByRole('radio', { name: 'Map' })).toHaveFocus()
    expect(onChange.mock.calls.map((c) => c[0])).toEqual([
      'table',
      'map',
      'list',
      'map',
    ])
  })

  it('selects by click, and a disabled option does not act', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<Harness onChange={onChange} />)
    await user.click(screen.getByRole('radio', { name: 'Board' }))
    await user.click(screen.getByRole('radio', { name: 'Table' }))
    expect(onChange.mock.calls.map((c) => c[0])).toEqual(['table'])
    expect(screen.getByRole('radio', { name: 'Board' })).toHaveAttribute(
      'aria-disabled',
      'true',
    )
  })

  it('shows the reason of a disabled option, and the option points at it', () => {
    render(<Harness />)
    const board = screen.getByRole('radio', { name: 'Board' })
    expect(board).toHaveAccessibleDescription(
      'The board view needs a work item stream.',
    )
    expect(
      screen.getByText('The board view needs a work item stream.'),
    ).toBeVisible()
  })

  it('refuses a disabled option without reason text', () => {
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    expect(() =>
      render(
        <Segmented
          aria-label="View"
          options={[
            { value: 'a', label: 'A' },
            { value: 'b', label: 'B', disabled: true, reason: ' ' },
          ]}
          value="a"
          onValueChange={() => undefined}
        />,
      ),
    ).toThrow(/reason text/)
    quiet.mockRestore()
  })
})
