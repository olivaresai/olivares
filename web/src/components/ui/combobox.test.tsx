// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-02 — APG `combobox` pattern (filterable). The combobox a11y roles are
// supplied by cmdk: once open, the filter input is role=combobox over a role=listbox
// of role=option rows, with DOM focus staying on the input while arrow keys move the
// active descendant. These tests assert the public contract (placeholder, open,
// filter, select, clear, empty) against those roles. cmdk filters asynchronously, so
// open/filter assertions go through findBy*/waitFor.
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { Combobox, type ComboboxOption } from './combobox'

const OPTIONS: ComboboxOption[] = [
  { value: 'us-east', label: 'US East', keywords: ['virginia', 'iad'] },
  { value: 'us-west', label: 'US West', keywords: ['oregon', 'pdx'] },
  { value: 'eu-central', label: 'EU Central', keywords: ['frankfurt', 'fra'] },
  { value: 'ap-south', label: 'AP South', disabled: true },
]

const noop = () => {}

/** Controlled harness so selecting actually updates the trigger label. */
function Harness({
  initial = null,
  clearable = false,
  onChange,
}: {
  initial?: string | null
  clearable?: boolean
  onChange?: (v: string | null) => void
}) {
  const [value, setValue] = useState<string | null>(initial)
  return (
    <Combobox
      options={OPTIONS}
      value={value}
      onChange={(v) => {
        setValue(v)
        onChange?.(v)
      }}
      clearable={clearable}
      placeholder="Select a region"
      searchPlaceholder="Filter regions"
      emptyText="No regions match"
      aria-label="Region"
    />
  )
}

const trigger = () => screen.getByRole('button', { name: /region/i })

describe('Combobox (APG filterable combobox)', () => {
  it('renders the trigger with the placeholder when value is null', () => {
    render(
      <Combobox
        options={OPTIONS}
        value={null}
        onChange={noop}
        placeholder="Select a region"
        aria-label="Region"
      />,
    )
    const btn = trigger()
    expect(btn).toHaveTextContent('Select a region')
    expect(btn).toHaveAttribute('aria-expanded', 'false')
    expect(btn).toHaveAttribute('aria-haspopup', 'listbox')
    // WCAG 2.5.8 target size: the design-system trigger is h-8 (32px).
    expect(btn).toHaveClass('h-8')
    // The combobox role belongs to cmdk's input, which only exists once open.
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  })

  it('shows the selected option label on the trigger', () => {
    render(<Harness initial="eu-central" />)
    expect(trigger()).toHaveTextContent('EU Central')
  })

  it('forwards id and aria-label to the trigger for label association', () => {
    render(
      <Combobox
        options={OPTIONS}
        value={null}
        onChange={noop}
        id="region-field"
        aria-label="Region"
      />,
    )
    const btn = trigger()
    expect(btn).toHaveAttribute('id', 'region-field')
    expect(btn).toHaveAttribute('aria-label', 'Region')
  })

  it('opens the listbox (cmdk combobox + options) on click', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(trigger())

    // Listbox + the editable combobox input come from cmdk.
    const listbox = await screen.findByRole('listbox')
    expect(listbox).toBeInTheDocument()
    const input = screen.getByRole('combobox')
    expect(input).toHaveAttribute('aria-autocomplete', 'list')
    expect(input).toHaveAttribute('aria-controls', listbox.id)
    // APG: DOM focus is on the input, not on an option.
    expect(input).toHaveFocus()

    const options = within(listbox).getAllByRole('option')
    expect(options).toHaveLength(OPTIONS.length)
    expect(options.map((o) => o.textContent)).toEqual(
      expect.arrayContaining(['US East', 'US West', 'EU Central']),
    )
    // The disabled option is exposed but not selectable.
    expect(
      within(listbox).getByText('AP South').closest('[role="option"]'),
    ).toHaveAttribute('aria-disabled', 'true')
  })

  it('filters options as you type (matches label and keywords)', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(trigger())

    const input = await screen.findByRole('combobox')
    // Matches a keyword ("oregon") that is not in the visible label.
    await user.type(input, 'oregon')
    await waitFor(() => {
      const options = screen.getAllByRole('option')
      expect(options).toHaveLength(1)
      expect(options[0]).toHaveTextContent('US West')
    })
  })

  it('shows emptyText when nothing matches the filter', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(trigger())

    const input = await screen.findByRole('combobox')
    await user.type(input, 'zzzzz')
    expect(await screen.findByText('No regions match')).toBeInTheDocument()
    expect(screen.queryAllByRole('option')).toHaveLength(0)
  })

  it('selecting an option calls onChange with its value, closes, and updates the trigger', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Harness onChange={onChange} />)
    await user.click(trigger())

    const option = await screen.findByText('US West')
    await user.click(option)

    expect(onChange).toHaveBeenCalledWith('us-west')
    // Popover closed.
    await waitFor(() =>
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument(),
    )
    // Trigger reflects the new selection.
    expect(trigger()).toHaveTextContent('US West')
  })

  it('marks the chosen option with a visible check (opacity-100)', async () => {
    const user = userEvent.setup()
    render(<Harness initial="us-east" />)
    await user.click(trigger())

    const listbox = await screen.findByRole('listbox')
    const chosenRow = within(listbox)
      .getByText('US East')
      .closest('[role="option"]')!
    const otherRow = within(listbox)
      .getByText('US West')
      .closest('[role="option"]')!
    // The chosen row's check is shown; the others are transparent.
    expect(chosenRow.querySelector('svg')).toHaveClass('opacity-100')
    expect(otherRow.querySelector('svg')).toHaveClass('opacity-0')
  })

  it('re-choosing the selected option clears it when clearable', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Harness initial="us-east" clearable onChange={onChange} />)
    await user.click(trigger())

    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByText('US East'))
    expect(onChange).toHaveBeenCalledWith(null)
    await waitFor(() => expect(trigger()).toHaveTextContent('Select a region'))
  })

  it('does NOT clear on re-choose when not clearable', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Harness initial="us-east" onChange={onChange} />)
    await user.click(trigger())

    const listbox = await screen.findByRole('listbox')
    await user.click(within(listbox).getByText('US East'))
    expect(onChange).toHaveBeenCalledWith('us-east')
    expect(onChange).not.toHaveBeenCalledWith(null)
  })

  it('Escape closes the popover and returns focus to the trigger', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(trigger())
    expect(await screen.findByRole('listbox')).toBeInTheDocument()

    fireEvent.keyDown(await screen.findByRole('combobox'), { key: 'Escape' })
    await waitFor(() =>
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument(),
    )
    expect(trigger()).toHaveFocus()
  })

  it('does not open when disabled', async () => {
    const user = userEvent.setup()
    render(
      <Combobox
        options={OPTIONS}
        value={null}
        onChange={noop}
        disabled
        aria-label="Region"
      />,
    )
    const btn = trigger()
    expect(btn).toBeDisabled()
    await user.click(btn)
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
  })
})
