// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A backticked part of a translated or engine-written sentence reads as a command (#1844).
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { withInlineCommands } from './inline-commands'

describe('withInlineCommands', () => {
  it('renders names as plain code when the caller chooses code instead of commands', () => {
    const { container } = render(
      <p>
        {withInlineCommands(
          'Ordered by `executed_at`; needs `voice:session:read`.',
          'code',
        )}
      </p>,
    )
    expect(
      [...container.querySelectorAll('code')].map((c) => c.textContent),
    ).toEqual(['executed_at', 'voice:session:read'])
    expect(container.querySelector('button')).toBeNull()
    expect(container.textContent).not.toContain('`')
  })

  it('turns each backticked part into a command chip and keeps the rest as text', () => {
    const { container } = render(
      <p>
        {withInlineCommands(
          'Use `docker logs` or `journalctl -u olivares` for the service.',
        )}
      </p>,
    )
    expect(
      [...container.querySelectorAll('code')].map((c) => c.textContent),
    ).toEqual(['docker logs', 'journalctl -u olivares'])
    expect(container.querySelectorAll('[data-slot="code-line"]')).toHaveLength(
      2,
    )
    expect(container.textContent).toBe(
      'Use docker logs or journalctl -u olivares for the service.',
    )
    expect(container.textContent).not.toContain('`')
  })

  // CodeLine throws on a blank command, and there is no error boundary above a page: one odd
  // engine string must stay text.
  it('keeps a blank backtick pair as text instead of throwing', () => {
    const { container } = render(
      <p>{withInlineCommands('separate with ` ` here, then run `ls`')}</p>,
    )
    expect(container.textContent).toBe('separate with   here, then run ls')
    expect(
      [...container.querySelectorAll('code')].map((c) => c.textContent),
    ).toEqual(['ls'])
  })

  it('keeps a pair that spans lines as text, so the chip never shows a different command than it copies', () => {
    const { container } = render(
      <p>{withInlineCommands('a `first\nsecond` b')}</p>,
    )
    expect(container.querySelector('code')).toBeNull()
  })

  it('renders nothing for a missing sentence', () => {
    expect(withInlineCommands(undefined)).toEqual([])
    expect(withInlineCommands(null)).toEqual([])
  })

  it('returns a sentence without a backtick as its own text', () => {
    expect(withInlineCommands('Nothing to run here.')).toEqual([
      'Nothing to run here.',
    ])
  })
})
