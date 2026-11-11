// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A reply's code block: coloured by its language, Run in terminal for a shell block,
// Wrap lines, and Copy of the exact text.
import { render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { MarkdownText } from './markdown-text'
import { ReplyCode } from './reply-code'
import { RunInTerminalContext } from './run-in-terminal'
import { tags } from '@lezer/highlight'
import { tokenHighlightStyle } from '@/components/ui/code-languages'
import { highlightPieces } from './code-highlight'

const BASH = '$ npm install\n\n# then\nnpm test --silent\n'

describe('ReplyCode', () => {
  it('colours a shell block and keeps its text exactly', async () => {
    render(<ReplyCode text={'echo "hi" # x'} lang="bash" />)
    const pre = document.querySelector('pre')!
    await waitFor(() =>
      expect(pre.querySelector('[class^="tok-"]')).not.toBeNull(),
    )
    expect(pre).toHaveTextContent('echo "hi" # x')
  })

  it('leaves a block of an unknown language plain, with its words', () => {
    render(<ReplyCode text="just words" lang="klingon" />)
    expect(screen.getByText('just words')).toBeInTheDocument()
    expect(document.querySelector('[class^="tok-"]')).toBeNull()
  })

  it('offers Run in terminal for a shell block of a session that has a terminal, and hands the text over as written', async () => {
    const run = vi.fn()
    const user = userEvent.setup()
    const withTerminal = (ui: ReactNode) => (
      <RunInTerminalContext.Provider value={{ run }}>
        {ui}
      </RunInTerminalContext.Provider>
    )
    const { rerender } = render(
      withTerminal(<ReplyCode text="npm test --silent" lang="sh" />),
    )
    await user.click(screen.getByRole('button', { name: 'Run in terminal' }))
    expect(run).toHaveBeenCalledWith('npm test --silent')
    rerender(withTerminal(<ReplyCode text="print(1)" lang="python" />))
    expect(screen.queryByRole('button', { name: 'Run in terminal' })).toBeNull()
    rerender(<ReplyCode text="npm test --silent" lang="sh" />)
    expect(screen.queryByRole('button', { name: 'Run in terminal' })).toBeNull()
  })

  it('wraps long lines on request', async () => {
    const user = userEvent.setup()
    render(<ReplyCode text="a very long line" lang="text" />)
    const wrap = screen.getByRole('button', { name: 'Wrap lines' })
    const pre = document.querySelector('pre')!
    expect(wrap).toHaveAttribute('aria-pressed', 'false')
    expect(pre.className).toContain('overflow-x-auto')
    await user.click(wrap)
    expect(wrap).toHaveAttribute('aria-pressed', 'true')
    expect(pre.className).toContain('whitespace-pre-wrap')
  })

  it('copies the text exactly', async () => {
    const user = userEvent.setup()
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    render(<ReplyCode text={BASH} lang="sh" />)
    await user.click(screen.getByRole('button', { name: 'Copy code' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(BASH))
  })

  it('sets the code in a well, in the mono face, under a quiet header of its language and Copy', () => {
    render(<ReplyCode text="ls -la" lang="sh" />)
    // The block may be coloured from its first render: read it whole from its pre.
    const pre = document.querySelector('pre')!
    expect(pre).toHaveTextContent('ls -la')
    const box = pre.parentElement!
    expect(box.className.split(/\s+/)).toEqual(
      expect.arrayContaining([
        'bg-[var(--well)]',
        'shadow-[var(--shadow-well)]',
      ]),
    )
    expect(box.className).not.toContain('bg-muted')
    expect(pre.className.split(/\s+/)).toEqual(
      expect.arrayContaining(['font-mono', 'text-mono']),
    )
    const header = box.firstElementChild as HTMLElement
    expect(header).toHaveTextContent('sh')
    expect(
      within(header).getByRole('button', { name: 'Copy code' }),
    ).toBeInTheDocument()
  })

  it('gives a fenced block of a reply its language', () => {
    render(<MarkdownText text={'```sh\nls\n```'} />)
    expect(screen.getByText('sh')).toBeInTheDocument()
  })
})

describe('the language a fence names', () => {
  it.each(['constructor', '__proto__'])(
    'reads a fence named %s as no language, so its block stays plain',
    async (name) => {
      expect(await highlightPieces('x = 1', name)).toBeNull()
    },
  )
})

describe('a code block that streams', () => {
  it('stays coloured from one streamed piece to the next, never plain in between', async () => {
    const { rerender } = render(<ReplyCode text={'echo "one"'} lang="bash" />)
    const pre = document.querySelector('pre')!
    await waitFor(() =>
      expect(pre.querySelector('[class^="tok-"]')).not.toBeNull(),
    )
    const streamed = ['echo "one"\necho "two"', 'echo "one"\necho "two"\n#']
    for (const text of streamed) {
      rerender(<ReplyCode text={text} lang="bash" />)
      // In the same render as the new text: no plain pass, no second insertion.
      expect(pre.querySelector('[class^="tok-"]')).not.toBeNull()
      expect(pre.textContent).toBe(text)
    }
  })
})

describe('the ink of a token kind', () => {
  it("is the editors' own: one map says what colour a comment is", async () => {
    render(<ReplyCode text={'# note\necho "hi"'} lang="bash" />)
    const pre = document.querySelector('pre')!
    const comment = await waitFor(() => {
      const spans = [...pre.querySelectorAll('span')]
      const found = spans.find((span) => span.textContent === '# note')
      expect(found).toBeDefined()
      return found!
    })
    const specs = tokenHighlightStyle.specs
    const editors = specs.find((s) => [s.tag].flat().includes(tags.comment))!
    expect(comment.style.color).toBe(editors.color)
    expect(comment.style.fontStyle).toBe(editors.fontStyle)
  })

  it("paints a number in the editors' number ink, never one of its own", async () => {
    render(<ReplyCode text="exit 42" lang="bash" />)
    const pre = document.querySelector('pre')!
    const number = await waitFor(() => {
      const spans = [...pre.querySelectorAll('span')]
      const found = spans.find((span) => span.textContent === '42')
      expect(found).toBeDefined()
      return found!
    })
    const specs = tokenHighlightStyle.specs
    const editors = specs.find((s) => [s.tag].flat().includes(tags.number))!
    expect(number.style.color).toBe(editors.color)
  })
})
