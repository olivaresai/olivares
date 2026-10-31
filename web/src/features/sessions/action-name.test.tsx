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

// the regression: "Write · /srv/work/…/verdict.md" pushed the Context pane
// sideways. #3135: a path goes through the one shared PathText, which cuts it in its middle,
// keeps the whole value as the hover and copies it whole; ActionName keeps no cutter of its own.
describe('a path is shown through PathText, never cut by a second mechanism', () => {
  const path = '/srv/work/reports/2026-10/review-0142/verdict.md'

  it('renders a path through PathText, whole as its hover, with a copy action', () => {
    const { container } = render(<ActionName value={path} />)
    const shared = container.querySelector('[data-slot="path-text"]')
    expect(shared).not.toBeNull()
    expect(shared!.querySelector('code')).toHaveAttribute('title', path)
    expect(shared!.textContent).toContain('verdict.md')
    expect(container.querySelector('button')).not.toBeNull()
  })

  it('leaves the copy action out where the caller says so, as in a clickable table row', () => {
    const { container } = render(<ActionName value={path} copy={false} />)
    expect(container.querySelector('[data-slot="path-text"]')).not.toBeNull()
    expect(container.querySelector('button')).toBeNull()
  })

  it('leaves a value that is not a path in one piece', () => {
    const { container } = render(<ActionName value="web.search" />)
    expect(container.querySelector('[data-slot="path-text"]')).toBeNull()
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
