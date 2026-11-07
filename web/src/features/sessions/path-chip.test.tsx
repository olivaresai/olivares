// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// File and folder references in a reply become chips: the path and line of an inline
// code span, opened in the person's editor, its whole path on the hover, and copied relative
// or whole from its menu.
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { MarkdownText } from './markdown-text'
import { pathRef } from './markdown'
import { PathRoot } from './path-chip'

describe('pathRef', () => {
  it('reads a file, a file and line, and a folder; leaves a word or a URL alone', () => {
    expect(pathRef('tools/dispatch.py:3446')).toEqual({
      path: 'tools/dispatch.py',
      line: 3446,
    })
    expect(pathRef('README.md')).toEqual({ path: 'README.md' })
    expect(pathRef('0.6-server-site/')).toEqual({ path: '0.6-server-site/' })
    for (const word of [
      'make',
      'v1.2.3',
      'npm run build',
      'https://x.dev/a.md',
      'a = b',
      '~/notes.md',
    ])
      expect(pathRef(word)).toBeNull()
  })
})

describe('a path in a reply', () => {
  const reply = 'See `tools/dispatch.py:3446` and `docs/` before `make`.'

  it('is a chip that opens the file in the editor at its line, with its whole path on the hover', () => {
    render(
      <PathRoot root="/home/dev/work/app">
        <MarkdownText text={reply} />
      </PathRoot>,
    )
    const file = screen.getByRole('link', { name: /dispatch\.py · L3446/ })
    expect(file).toHaveAttribute(
      'href',
      'vscode://file/home/dev/work/app/tools/dispatch.py:3446',
    )
    expect(file).toHaveAttribute(
      'title',
      '/home/dev/work/app/tools/dispatch.py',
    )
    expect(screen.getByRole('link', { name: /docs\// })).toBeInTheDocument()
    // A word is still code.
    expect(screen.getByText('make').tagName).toBe('CODE')
  })

  it('uses the shared file-type icons in reply chips and keeps folder icons', () => {
    render(
      <PathRoot root="/home/dev/work/app">
        <MarkdownText
          text={
            '`main.go:12` `app.py` `README.md` `package.json` `run.sh` `app.ts` `docs/`'
          }
        />
      </PathRoot>,
    )
    for (const [name, icon] of [
      ['main.go · L12', 'file-code'],
      ['app.py', 'file-code'],
      ['README.md', 'file-text'],
      ['package.json', 'file-braces'],
      ['run.sh', 'file-terminal'],
      ['app.ts', 'file-code'],
      ['docs/', 'folder'],
    ]) {
      const chip = screen.getByRole('link', { name })
      expect(chip.querySelector(`svg.lucide-${icon}`)).toHaveAttribute(
        'aria-hidden',
        'true',
      )
    }
  })

  it('copies the relative or the whole path from its menu', async () => {
    const user = userEvent.setup()
    const writeText = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    render(
      <PathRoot root="/home/dev/work/app">
        <MarkdownText text={reply} />
      </PathRoot>,
    )
    fireEvent.contextMenu(screen.getByRole('link', { name: /dispatch\.py/ }))
    await user.click(
      await screen.findByRole('menuitem', { name: 'Copy relative path' }),
    )
    await waitFor(() =>
      expect(writeText).toHaveBeenLastCalledWith('tools/dispatch.py'),
    )
    fireEvent.contextMenu(screen.getByRole('link', { name: /dispatch\.py/ }))
    await user.click(
      await screen.findByRole('menuitem', { name: 'Copy full path' }),
    )
    await waitFor(() =>
      expect(writeText).toHaveBeenLastCalledWith(
        '/home/dev/work/app/tools/dispatch.py',
      ),
    )
  })

  it('stays code where no session folder is known', () => {
    render(<MarkdownText text={reply} />)
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('tools/dispatch.py:3446').tagName).toBe('CODE')
  })
})
