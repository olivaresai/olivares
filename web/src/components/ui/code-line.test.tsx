// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// CLI PARITY: every guided action shows the same thing as a terminal command, in a `code`
// element, and Copy writes exactly that text (never the prompt sign).
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { CodeLine } from './code-line'

describe('CodeLine', () => {
  it('shows the command in a code element, with the prompt sign outside it', () => {
    const { container } = render(
      <CodeLine command="olivares tools install opencode" />,
    )
    const code = container.querySelector('code')!
    expect(code.textContent).toBe('olivares tools install opencode')
    const prompt = container.querySelector('[data-slot="code-line-prompt"]')!
    expect(prompt).toHaveAttribute('aria-hidden', 'true')
    expect(code.contains(prompt)).toBe(false)
  })

  it('names the block for what it is', () => {
    render(<CodeLine command="olivares setup" />)
    expect(screen.getByText('Same thing in a terminal')).toBeVisible()
  })

  it('takes no heading when it sits under one of its own', () => {
    render(<CodeLine command="olivares setup" label={null} />)
    expect(screen.queryByText('Same thing in a terminal')).toBeNull()
    expect(screen.getByText('olivares setup')).toBeInTheDocument()
  })

  it('copies exactly the command and says so', async () => {
    const user = userEvent.setup()
    const write = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    render(<CodeLine command="olivares session start" />)
    await user.click(screen.getByRole('button', { name: 'Copy command' }))
    expect(write).toHaveBeenCalledWith('olivares session start')
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('Copied'),
    )
  })

  it('says when the copy failed, so the operator copies by hand', async () => {
    const user = userEvent.setup()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(
      new Error('denied'),
    )
    render(<CodeLine command="olivares session start" />)
    await user.click(screen.getByRole('button', { name: 'Copy command' }))
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        'Could not copy. Select the command and copy it.',
      ),
    )
  })

  it('has an inline form for a sentence, with the same copy button', () => {
    const { container } = render(
      <CodeLine inline command="olivares session start" />,
    )
    expect(container.firstElementChild).toHaveAttribute('data-inline', 'true')
    expect(container.querySelector('code')!.textContent).toBe(
      'olivares session start',
    )
    expect(
      screen.getByRole('button', { name: 'Copy command' }),
    ).toBeInTheDocument()
    expect(screen.queryByText('Same thing in a terminal')).toBeNull()
  })

  it('refuses an empty command: a missing command is a parity gap, not a blank line', () => {
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    expect(() => render(<CodeLine command=" " />)).toThrow(/command/)
    quiet.mockRestore()
  })
})
