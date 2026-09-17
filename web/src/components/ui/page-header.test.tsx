// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// D21 — the header contract, and only the parts of it that are CONTRACT.
//
// The T3 Code side-by-side recorded our front door as "six read-only cards, no action
// anywhere on the page". The repair is not a button; it is a header that has a place
// for the verb, so that a screen offering nothing shows it by leaving that place empty
// rather than by burying the verb among filters. What is asserted below is therefore
// the ORDER and the SLOT, never the spacing: a test that pins padding turns every
// design change into a red cell and teaches the next author to delete the test.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Button } from './button'
import { PageHeader } from './page-header'

describe('PageHeader', () => {
  it('renders the title as the page h1', () => {
    render(<PageHeader title="Inventory" />)
    expect(
      screen.getByRole('heading', { level: 1, name: 'Inventory' }),
    ).toBeInTheDocument()
  })

  it('puts the primary action LAST, after the secondary controls', () => {
    render(
      <PageHeader
        title="Policies"
        actions={<Button>Filter</Button>}
        primaryAction={<Button variant="primary">New policy</Button>}
      />,
    )
    const buttons = screen.getAllByRole('button').map((b) => b.textContent)
    expect(buttons).toEqual(['Filter', 'New policy'])
  })

  it('renders no control row at all when the screen offers nothing', () => {
    // The honest empty header: a screen with no verb must not grow a placeholder.
    render(<PageHeader title="Read-only view" />)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('renders notices under the header, not inside the title row', () => {
    render(
      <PageHeader
        title="Compliance"
        description="What the platform can evidence."
        notices={<p>Aggregates are a floor.</p>}
      />,
    )
    const h1 = screen.getByRole('heading', { level: 1 })
    const notice = screen.getByText('Aggregates are a floor.')
    // DOCUMENT_POSITION_FOLLOWING: the notice comes after the heading in the document,
    // and is not a descendant of the title row.
    expect(
      h1.compareDocumentPosition(notice) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
    expect(h1.parentElement?.contains(notice)).toBe(false)
  })

  it('emits the element the caller asks for, so consolidating the two headers moved no DOM', () => {
    // `IntelPage` has always emitted `<header>`; this primitive has always emitted a
    // `<div>`. Both now come from here, and the prop is what keeps that true.
    const { container, rerender } = render(<PageHeader title="A" />)
    expect(container.querySelector('header')).toBeNull()
    rerender(<PageHeader as="header" title="A" />)
    expect(container.querySelector('header')).not.toBeNull()
  })

  it('takes its size from the type scale, not from a hand-written stack', () => {
    // The one class assertion worth keeping: `text-display` is the token
    // (web/tokens/primitives.tokens.json, the `type` group) that carries size, leading,
    // tracking AND weight together. Six places used to spell that stack by hand with
    // three different sizes; this is the check that says the heading reads the ladder.
    render(<PageHeader title="Estate" />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveClass(
      'text-display',
    )
  })
})
