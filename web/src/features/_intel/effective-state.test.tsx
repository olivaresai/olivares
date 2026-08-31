// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EffectiveStateLinks is the seam that stops a read-only reference page from being
// a dead end. It is tested HERE, at the shared kit, because the two consumers (platforms,
// rate-limits) run their suites with `can: () => false`, where this component correctly
// renders nothing — so neither suite would ever exercise the allowed path.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

const allow = { set: new Set<string>() }
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: (p: string) => allow.set.has(p) }),
}))
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))

import { EffectiveStateLinks } from './effective-state'

const TARGETS = [
  { to: '/models', permission: 'models:catalog:read', label: 'Model catalog' },
  {
    to: '/model-operations',
    permission: 'models:registry:read',
    label: 'Model operations',
  },
]

function renderWith(permissions: string[]) {
  allow.set = new Set(permissions)
  return render(
    <EffectiveStateLinks label="In this estate:" targets={TARGETS} />,
  )
}

describe('EffectiveStateLinks', () => {
  it('links to every target the principal can open', () => {
    renderWith(['models:catalog:read', 'models:registry:read'])
    expect(
      screen.getByRole('link', { name: /model catalog/i }),
    ).toHaveAttribute('href', '/models')
    expect(
      screen.getByRole('link', { name: /model operations/i }),
    ).toHaveAttribute('href', '/model-operations')
    expect(screen.getByText('In this estate:')).toBeInTheDocument()
  })

  it('hides a target whose route permission the principal lacks', () => {
    // can() is membership of the effective set — a link offered without it lands the
    // operator on a Forbidden page, which is worse than no link.
    renderWith(['models:catalog:read'])
    expect(screen.getByRole('link', { name: /model catalog/i })).toBeVisible()
    expect(screen.queryByRole('link', { name: /model operations/i })).toBeNull()
  })

  it('renders nothing at all when no target can be opened', () => {
    // Non-firing direction: a component that always rendered the row would pass the
    // first case while showing a viewer a list of doors that do not open.
    const { container } = renderWith([])
    expect(container).toBeEmptyDOMElement()
    expect(screen.queryByText('In this estate:')).toBeNull()
  })
})
