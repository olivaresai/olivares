// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Dialog and sheet titles are panel titles: 16 px on a 22 px line, weight 600, tracking
// -0.01em — the `heading` step of the type ladder. The page title (22/28) stays on the page
// header; a panel that repeats it competes with the page it opens over.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from './dialog'
import { Sheet, SheetContent, SheetDescription, SheetTitle } from './sheet'

/** The @theme value of one ladder step, read from the generated stylesheet. */
function step(name: string): Record<string, string> {
  const css = readFileSync('src/styles/tokens.css', 'utf8').replace(
    /\/\*[\s\S]*?\*\//g,
    '',
  )
  const body = /@theme inline\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/--text-([\w-]+):\s*([^;]+);/g)) {
    const [key, sub] = m[1].split('--')
    if (key === name) out[sub ?? 'size'] = m[2].trim()
  }
  return out
}

describe('panel titles take the heading step (16/22)', () => {
  it('the heading step is 16 px on a 22 px line, weight 600', () => {
    expect(step('heading')).toEqual({
      size: '1rem',
      'line-height': '1.375rem',
      'letter-spacing': '-0.01em',
      'font-weight': '600',
    })
  })

  it('a dialog title renders at the heading step, not the page title step', () => {
    render(
      <Dialog open>
        <DialogContent>
          <DialogTitle>Confirm action</DialogTitle>
          <DialogDescription>Body.</DialogDescription>
        </DialogContent>
      </Dialog>,
    )
    const title = screen.getByText('Confirm action')
    expect(title).toHaveClass('text-heading')
    expect(title).not.toHaveClass('text-title')
  })

  it('a sheet title renders at the heading step, not the page title step', () => {
    render(
      <Sheet open>
        <SheetContent>
          <SheetTitle>Add an account</SheetTitle>
          <SheetDescription>Body.</SheetDescription>
        </SheetContent>
      </Sheet>,
    )
    const title = screen.getByText('Add an account')
    expect(title).toHaveClass('text-heading')
    expect(title).not.toHaveClass('text-title')
  })
})
