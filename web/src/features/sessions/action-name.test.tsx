// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A session's current action is the engine's identifier for what the agent is doing
// (`web.search`, `create_issue`): machine text, shown as sent, in every language. It renders
// as code in the machine face, so a reader — and a check for untranslated keys — can tell an
// identifier from interface copy that failed to translate.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ActionName } from './action-name'

/** Dotted text outside code, the shape an untranslated key has (the browser check's rule). */
function keyShapedCopy(root: HTMLElement): string[] {
  const KEY = /^[a-z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*){1,5}$/
  const out: string[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = (n.textContent ?? '').trim()
    if (!KEY.test(text)) continue
    if (n.parentElement?.closest('code, pre, a[href], [data-allow-dotted]'))
      continue
    out.push(text)
  }
  return out
}

// dev#3120: "Write · /srv/work/…/verdict.md" pushed the Context pane
// sideways. A path is cut in its middle: the folders shrink, the file name stays, and
// the whole path is the hover and what a copy takes.
describe('a path is cut in the middle, never sideways', () => {
  const path = '/srv/work/reports/2026-10/review-0142/verdict.md'

  it('keeps the file name whole and lets only the folders shrink', () => {
    const { container } = render(<ActionName value={path} />)
    const code = container.querySelector('code')!
    expect(code).toHaveAttribute('title', path)
    // Selected and copied, it is the whole path, in order.
    expect(code.textContent).toBe(path)
    const [head, tail] = Array.from(code.children) as HTMLElement[]
    expect(head).toHaveClass('truncate', 'min-w-0')
    expect(tail.textContent).toBe('/verdict.md')
    expect(tail).toHaveClass('shrink-0')
    // The element can shrink to its column instead of widening it.
    expect(code).toHaveClass('max-w-full', 'min-w-0')
  })

  it('cuts the folders, not the file name, in a table cell and after a trailing slash', () => {
    const cell = render(
      <ActionName value={path} className="block truncate" />,
    ).container.querySelector('code')!
    expect(cell).toHaveClass('inline-flex')
    expect(cell.lastElementChild?.textContent).toBe('/verdict.md')
    const folder = render(
      <ActionName value="/srv/work/reports/tmp/" />,
    ).container.querySelector('code')!
    expect(folder.lastElementChild?.textContent).toBe('/tmp/')
  })

  it('leaves a value that is not a path in one piece', () => {
    const { container } = render(<ActionName value="web.search" />)
    expect(container.querySelector('code')!.children).toHaveLength(0)
  })
})

describe('the current action renders as an identifier', () => {
  it('is a code element in the machine face, with its value as the title', () => {
    const { container } = render(<ActionName value="web.search" />)
    const code = container.querySelector('code')
    expect(code).not.toBeNull()
    expect(code).toHaveTextContent('web.search')
    expect(code).toHaveClass('font-mono')
    expect(code).toHaveAttribute('title', 'web.search')
    expect(keyShapedCopy(container)).toEqual([])
  })

  it('the key check still sees dotted copy that is not code', () => {
    // The control positive: without the code element the same text reads as a key.
    const { container } = render(<span>web.search</span>)
    expect(keyShapedCopy(container)).toEqual(['web.search'])
  })

  it('both places that show the current action render it through ActionName', () => {
    for (const file of [
      'src/features/sessions/session-card.tsx',
      'src/features/sessions/sessions-workspace-view.tsx',
    ]) {
      const src = readFileSync(file, 'utf8')
      expect(src, file).toMatch(/<ActionName\b/)
      expect(src, file).not.toMatch(/\{\s*live\.current_action\s*\|\|/)
      expect(src, file).not.toMatch(/title=\{v\}>\s*\{v\}/)
    }
  })
})
