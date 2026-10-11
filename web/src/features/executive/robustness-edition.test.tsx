// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { renderIntel, screen } from '@/test/intel'
import { RiskSection } from './components'
import { deriveRisk } from './derive'
import { securityFindingsFixture } from './fixtures'
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
