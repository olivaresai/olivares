// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { AttributionChip } from './attribution-chip'
import './i18n'

describe('AttributionChip', () => {
  it('paints Managed by Olivares with muted surface tokens, not the accent', () => {
    render(<AttributionChip attribution="managed" />)
    const chip = screen.getByText('Managed by Olivares')
    const classes = chip.className.split(/\s+/)
    expect(classes).toContain('border-border')
    expect(classes).toContain('bg-muted')
    expect(classes).toContain('text-muted-foreground')
    expect(classes).not.toContain('border-accent-line')
    expect(classes).not.toContain('bg-accent-soft')
    expect(classes).not.toContain('text-accent-text')
  })
})
