// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { renderIntel, screen } from '@/test/intel'
import { RiskSection } from './components'
import { deriveDrift, deriveRisk } from './derive'
import { accessDriftFixture, securityFindingsFixture } from './fixtures'
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/features/extensions', async (importOriginal) => {
  const original =
    await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...original,
    PANEL_EXTENSIONS: {
      ...original.PANEL_EXTENSIONS,
      executiveRiskCards: [],
    },
  }
})
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
it('does not offer red-team robustness in the shared security section', () => {
  renderIntel(<RiskSection risk={deriveRisk(securityFindingsFixture)!} />)
  expect(screen.queryAllByText(/robustness/i)).toHaveLength(0)
  expect(screen.getByText('Open findings')).toBeInTheDocument()
})

//: with no grant declared every observed access is "unexpected" by
// construction; the card must say there is nothing to compare, not headline it.
it('shows no red unexpected count when the engine reports no grants declared', () => {
  const drift = deriveDrift({
    ...accessDriftFixture,
    unexpected_count: 12913,
    grants_declared: false,
  })
  renderIntel(
    <RiskSection risk={deriveRisk(securityFindingsFixture)!} drift={drift} />,
  )
  expect(screen.queryByText('12,913')).not.toBeInTheDocument()
  expect(screen.queryByText('Unexpected accesses')).not.toBeInTheDocument()
  expect(screen.getByText(/No access rules declared/i)).toBeInTheDocument()
})
