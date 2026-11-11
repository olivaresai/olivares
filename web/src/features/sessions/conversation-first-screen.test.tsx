// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CONVERSATION'S FIRST SCREEN LOADS NO EDITOR. A reply's code block paints its text in
// the machine face at once, and CodeMirror (a language's grammar, the stream parser and the
// editors' ink) loads only when the block comes near the screen. This file imports the
// conversation's Markdown and nothing of CodeMirror itself.
import { act, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { MarkdownText } from './markdown-text'

const loaded = vi.hoisted(() => ({ view: false, language: false }))
vi.mock('@codemirror/view', async (orig) => {
  loaded.view = true
  return orig()
})
vi.mock('@codemirror/language', async (orig) => {
  loaded.language = true
  return orig()
})

// The page's IntersectionObserver, driven by the case: `near` brings every block it
// observes near the screen, and says from which root each observer watched and how many
// stopped.
const near = vi.hoisted(() => ({
  blocks: [] as Array<() => void>,
  roots: [] as Array<Element | null>,
  stopped: 0,
}))
class NearObserver {
  callback: (entries: { isIntersecting: boolean }[]) => void
  constructor(
    callback: (entries: { isIntersecting: boolean }[]) => void,
    options?: { root?: Element | null },
  ) {
    this.callback = callback
    near.roots.push(options?.root ?? null)
  }
  observe() {
    near.blocks.push(() => this.callback([{ isIntersecting: true }]))
  }
  disconnect() {
    near.stopped++
  }
}
vi.stubGlobal('IntersectionObserver', NearObserver)

describe("a reply's code on the conversation's first screen", () => {
  it('paints the code as plain text and loads no CodeMirror until the block is near the screen', async () => {
    render(<MarkdownText text={'Run it:\n\n```bash\necho "hi" # x\n```\n'} />)
    const pre = document.querySelector('pre')!
    expect(pre).toHaveTextContent('echo "hi" # x')
    expect(pre.querySelector('[class^="tok-"]')).toBeNull()
    expect(loaded.view).toBe(false)
    expect(loaded.language).toBe(false)
    act(() => near.blocks.forEach((show) => show()))
    await waitFor(() =>
      expect(pre.querySelector('[class^="tok-"]')).not.toBeNull(),
    )
    expect(pre).toHaveTextContent('echo "hi" # x')
    expect(loaded.language).toBe(true)
  })

  // The thread scrolls in its own log: «near» is reckoned there, ahead of what it shows.
  it("watches a block from the thread's own scroller, and the page where there is none", () => {
    const fence = '```sh\nls\n```\n'
    render(
      <div role="log">
        <MarkdownText text={fence} />
      </div>,
    )
    expect(near.roots.at(-1)).toBe(screen.getByRole('log'))
    render(<MarkdownText text={fence} />)
    expect(near.roots.at(-1)).toBeNull()
  })

  it('stops watching a block that leaves the page', () => {
    const view = render(<MarkdownText text={'```sh\nls\n```\n'} />)
    const stopped = near.stopped
    view.unmount()
    expect(near.stopped).toBe(stopped + 1)
  })
})
