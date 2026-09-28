// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// COLOR NEVER CARRIES A STATUS ALONE. A `StatusGlyph` renders its word, beside the shape or
// as the shape's accessible name, and it fails when it has no word to render.
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { StatusGlyph, type Status } from './status-glyph'

const WORDS: Array<[Status, string]> = [
  ['working', 'Working'],
  ['needs-you', 'Needs you'],
  ['done', 'Done'],
  ['failed', 'Failed'],
  ['paused', 'Paused'],
  ['idle', 'Not started'],
]

describe('StatusGlyph without its word fails', () => {
  const quiet = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  afterEach(() => quiet.mockClear())

  it('throws on a blank word', () => {
    expect(() => render(<StatusGlyph status="done" label="  " />)).toThrow(
      /word/,
    )
    expect(() => render(<StatusGlyph status="done" label="" />)).toThrow(/word/)
  })

  it('throws on a status it has no word for', () => {
    expect(() =>
      render(<StatusGlyph status={'finished' as unknown as Status} />),
    ).toThrow(/word/)
  })
})

describe('StatusGlyph renders its word', () => {
  it.each(WORDS)('%s shows the word "%s" beside its shape', (status, word) => {
    const { container } = render(<StatusGlyph status={status} />)
    const glyph = container.querySelector('[data-slot="status-glyph"]')!
    expect(glyph).toHaveAttribute('data-status', status)
    expect(glyph).toHaveTextContent(word)
    // The shape is decoration once the word is there.
    expect(glyph.querySelector('svg, [data-shape]')).toHaveAttribute(
      'aria-hidden',
      'true',
    )
  })

  it.each(WORDS)(
    '%s without a visible word is an image named "%s"',
    (status, word) => {
      render(<StatusGlyph status={status} showLabel={false} />)
      const img = screen.getByRole('img', { name: word })
      expect(img).toHaveAttribute('data-status', status)
      expect(img.textContent).toBe('')
    },
  )

  it('carries a detail after the word, in the visible text and in the name', () => {
    const { container, rerender } = render(
      <StatusGlyph status="working" detail="6m 12s" />,
    )
    expect(container).toHaveTextContent('Working · 6m 12s')
    rerender(<StatusGlyph status="working" detail="6m 12s" showLabel={false} />)
    expect(
      screen.getByRole('img', { name: 'Working · 6m 12s' }),
    ).toBeInTheDocument()
  })

  it('takes a caller word in place of the default one', () => {
    render(<StatusGlyph status="needs-you" label="Handoff to you" />)
    expect(screen.getByText('Handoff to you')).toBeInTheDocument()
  })

  it('stops the running ring under reduced motion', () => {
    const { container } = render(<StatusGlyph status="working" />)
    const ring = container.querySelector('[data-shape="ring"]')!
    expect(ring.getAttribute('class')).toMatch(/motion-reduce:animate-none/)
  })
})
