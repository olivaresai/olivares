// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PROOF LINE (#2546): whether the ledger's chain verifies, when the engine last said
// so and how many entries it holds, from the reads the engine already answers. It never
// says "verified" for a chain the engine did not verify.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { ProofLine } from './proof-line'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    ...rest
  }: { children: ReactNode; to: string } & Record<string, unknown>) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))

// Today at 16:40: a verdict from today shows the time alone.
const AT = new Date().setHours(16, 40, 0, 0)

describe('ProofLine', () => {
  it('a verified chain says when and how many entries, and opens the Ledger', () => {
    renderIntel(<ProofLine verified at={AT} entries={12408} />)
    const line = screen.getByRole('link', { name: /Chain verified/ })
    expect(line).toHaveTextContent('Chain verified at 16:40 · 12,408 entries')
    expect(line).toHaveAttribute('href', '/audit')
  })

  it('without the entry count it says only what it knows', () => {
    renderIntel(<ProofLine verified at={AT} entries={null} />)
    expect(screen.getByRole('link')).toHaveTextContent(
      /^Chain verified at 16:40$/,
    )
  })

  it('a broken chain is said, in words', () => {
    renderIntel(<ProofLine verified={false} at={AT} entries={12408} />)
    const line = screen.getByRole('link', { name: /Chain broken/ })
    expect(line).not.toHaveTextContent('verified')
  })

  it('no verdict from the engine: no line at all', () => {
    const { container } = renderIntel(
      <ProofLine verified={null} at={AT} entries={12408} />,
    )
    expect(container).toBeEmptyDOMElement()
  })
})
