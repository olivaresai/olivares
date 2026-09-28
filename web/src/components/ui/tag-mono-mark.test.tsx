// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Tag (a short state or label word on a soft fill) and MonoMark (the letters of a tool,
// workspace or account in a rounded square).
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MonoMark } from './mono-mark'
import { Tag } from './tag'

describe('Tag', () => {
  it('shows its word on the soft fill of its tone', () => {
    const { container } = render(<Tag tone="ok">Ready</Tag>)
    const tag = container.firstElementChild!
    expect(tag).toHaveAttribute('data-slot', 'tag')
    expect(tag).toHaveAttribute('data-tone', 'ok')
    expect(tag).toHaveTextContent('Ready')
    expect(tag.className).toMatch(/\bbg-ok-soft\b/)
    expect(tag.className).toMatch(/\btext-ok\b/)
  })

  it('is neutral by default, and mono for identifiers', () => {
    const { container } = render(<Tag mono>v2.1.4</Tag>)
    const tag = container.firstElementChild!
    expect(tag).toHaveAttribute('data-tone', 'neutral')
    expect(tag.className).toMatch(/\bfont-mono\b/)
  })
})

describe('MonoMark', () => {
  it('takes the initials of the name', () => {
    const { container } = render(<MonoMark name="Claude Code" />)
    expect(container.firstElementChild).toHaveTextContent('CC')
    const { container: one } = render(<MonoMark name="telescopes" />)
    expect(one.firstElementChild).toHaveTextContent('T')
  })

  it('is decoration beside the name by default, and an image named by it alone', () => {
    const { container } = render(<MonoMark name="Codex" />)
    expect(container.firstElementChild).toHaveAttribute('aria-hidden', 'true')
    render(<MonoMark name="Grok Build" labelled />)
    expect(screen.getByRole('img', { name: 'Grok Build' })).toHaveTextContent(
      'GB',
    )
  })

  it('carries the account hue as a dot that never stands alone', () => {
    const { container } = render(
      <MonoMark name="claude-b" letters="C" hue={2} />,
    )
    const dot = container.querySelector('[data-slot="mono-mark-hue"]')!
    expect(dot).toHaveAttribute('data-hue', '2')
    expect(dot).toHaveAttribute('aria-hidden', 'true')
  })
})
