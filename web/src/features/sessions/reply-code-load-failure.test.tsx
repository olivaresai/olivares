// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A reply's code block whose colours cannot load (the chunk of CodeMirror fails, offline or
// after a new release): its text stays, plain, and nothing is thrown at the page.
import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { loadGrammar } from './code-highlight'
import { ReplyCode } from './reply-code'

vi.mock('./code-grammar', () => {
  throw new Error('the chunk did not load')
})

describe('a code block whose colours cannot load', () => {
  it('keeps its text, plain, and throws nothing', async () => {
    render(<ReplyCode text={'echo "hi" # x'} lang="bash" />)
    await loadGrammar('bash')
    const pre = document.querySelector('pre')!
    expect(pre).toHaveTextContent('echo "hi" # x')
    expect(pre.querySelector('[class^="tok-"]')).toBeNull()
  })
})
