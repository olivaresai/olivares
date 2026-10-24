// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CcStateBadge } from './cc-state-badge'
import './i18n'

describe('CcStateBadge', () => {
  it('active shows its word ("Working") beside a solid dot, nothing pulses', () => {
    const { container } = render(<CcStateBadge state="active" />)
    expect(screen.getByText(/working/i)).toBeInTheDocument()
    const dot = container.querySelector('[aria-hidden]') as HTMLElement
    expect(dot).toHaveClass('bg-success')
    expect(container.querySelector('.animate-pulse-live')).toBeNull()
    expect(dot.className).not.toMatch(/animate|pulse/i)
  })
})
