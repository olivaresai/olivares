// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { LiveDot } from './live-dot'
import './i18n'

describe('LiveDot', () => {
  it('an open stream is a solid teal dot beside the word, nothing pulses', () => {
    const { container } = render(<LiveDot status="open" />)
    expect(screen.getByText(/live/i)).toBeInTheDocument()
    const dot = container.querySelector('[aria-hidden]') as HTMLElement
    expect(dot).toHaveClass('bg-confidence-attributed')
    expect(container.querySelector('.animate-pulse-live')).toBeNull()
    expect(dot.className).not.toMatch(/animate|pulse/i)
  })
})
