// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The reference chip is named by its aria-label ("Copy <value>"), which replaces the text inside
// the button in the accessible name. Visually hidden text inside it is therefore read by nobody,
// and a one-pixel box that holds a word overflows that box in every language.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RefChip } from './ref-chip'

describe('the reference chip name', () => {
  it('is its aria-label, with the whole value', () => {
    render(<RefChip value="t-demo" absent="none" />)
    const chip = screen.getByTestId('ref-chip')
    expect(chip).toHaveAccessibleName('Copy t-demo')
    expect(chip).toHaveAttribute('title', 't-demo')
  })

  it('holds no visually hidden text that the name replaces', () => {
    render(<RefChip value="t-demo" absent="none" />)
    const chip = screen.getByTestId('ref-chip')
    expect(chip.querySelectorAll('.sr-only')).toHaveLength(0)
  })
})
