// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { tags } from '@lezer/highlight'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { usePreferencesStore } from '@/stores/preferences'
import { preferenceStorage } from '@/lib/preference-storage'
import { CodeDiff } from './code-diff'
import { tokenHighlightStyle } from './code-languages'

describe('CodeDiff', () => {
  it('renders both revisions read-only with accessible names', () => {
    render(
      <CodeDiff
        original={'{\n  "a": 1\n}'}
        modified={'{\n  "a": 2\n}'}
        language="json"
        originalLabel="Revision 1"
        modifiedLabel="Revision 2"
      />,
    )
    const left = screen.getByRole('textbox', { name: 'Revision 1' })
    const right = screen.getByRole('textbox', { name: 'Revision 2' })
    expect(left).toHaveAttribute('contenteditable', 'false')
    expect(right).toHaveAttribute('contenteditable', 'false')
    expect(left.textContent).toContain('"a": 1')
    expect(right.textContent).toContain('"a": 2')
  })

  it('sits in a well: recessed, with an inner edge and no outline', () => {
    const { container } = render(
      <CodeDiff
        original="a"
        modified="b"
        language="json"
        originalLabel="Before"
        modifiedLabel="After"
      />,
    )
    const well = container.querySelector('[data-slot="code-diff"]')!
    expect(well).toHaveClass(
      'rounded-ctl',
      'bg-[var(--well)]',
      'shadow-[var(--shadow-well)]',
    )
    expect(well.className).not.toMatch(/\bborder\b|bg-surface/)
  })

  it('marks added and removed lines with the status tokens, not the editor library colors', () => {
    render(
      <CodeDiff
        original={'a\nb\n'}
        modified={'a\nc\n'}
        language="text"
        originalLabel="Before"
        modifiedLabel="After"
      />,
    )
    // The editor mounts its themes as <style> rules; read the ones that paint each mark.
    const css = [...document.querySelectorAll('style')]
      .map((s) => s.textContent ?? '')
      .join('\n')
    const paints = (selector: string, value: string) =>
      [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].some(
        ([, sel, body]) => sel.includes(selector) && body.includes(value),
      )
    // The gutter marks: the status inks.
    expect(paints('.cm-merge-b .cm-changedLineGutter', 'var(--ok)')).toBe(true)
    expect(paints('.cm-merge-a .cm-changedLineGutter', 'var(--bad)')).toBe(true)
    expect(paints('.cm-deletedLineGutter', 'var(--bad)')).toBe(true)
    // The lines: the diff tints.
    expect(paints('.cm-merge-b .cm-changedLine', 'var(--diff-add)')).toBe(true)
    expect(paints('.cm-merge-a .cm-changedLine', 'var(--diff-del)')).toBe(true)
    expect(paints('.cm-deletedChunk', 'var(--diff-del)')).toBe(true)
    // The changed words: underlined in the status inks.
    expect(paints('.cm-merge-b .cm-changedText', 'var(--ok)')).toBe(true)
    expect(paints('.cm-merge-a .cm-changedText', 'var(--bad)')).toBe(true)
  })

  it('paints a number in the info ink, which holds 4.5:1 on the well and on both tints', () => {
    // The token contract measures that ink on the well and on each diff tint over it.
    const number = tokenHighlightStyle.specs.find((s) => s.tag === tags.number)
    expect(number?.color).toBe('var(--info)')
  })

  // jsdom has no layout, so the gutter resolves every click to the first line: this
  // proves the pane and that a real line number comes back, not which line a browser
  // would pick (the capture does).
  it('reports the pane and the line whose number is clicked', async () => {
    const onLineClick = vi.fn()
    const { container } = render(
      <CodeDiff
        original={'a\nb\n'}
        modified={'a\nc\n'}
        language="text"
        originalLabel="Revision 1"
        modifiedLabel="Revision 2"
        onLineClick={onLineClick}
      />,
    )
    const gutters = container.querySelectorAll('.cm-lineNumbers')
    expect(gutters).toHaveLength(2)

    await userEvent.click(gutters[0])
    expect(onLineClick).toHaveBeenLastCalledWith('original', 1)
    await userEvent.click(gutters[1])
    expect(onLineClick).toHaveBeenLastCalledWith('modified', 1)
  })

  it('leaves the line numbers alone without a handler', async () => {
    const { container } = render(
      <CodeDiff
        original="a"
        modified="b"
        language="text"
        originalLabel="Revision 1"
        modifiedLabel="Revision 2"
      />,
    )
    await userEvent.click(container.querySelector('.cm-lineNumbers')!)
    expect(screen.getByRole('textbox', { name: 'Revision 1' })).toBeVisible()
  })
})

it('switches layout, wrapping and whitespace and keeps the choice when reopened', async () => {
  const props = {
    original: 'old text\n',
    modified: 'new text\n',
    language: 'text' as const,
    originalLabel: 'Before',
    modifiedLabel: 'After',
  }
  const user = userEvent.setup()
  const first = render(<CodeDiff {...props} />)
  await user.click(screen.getByRole('radio', { name: 'Unified' }))
  expect(screen.getByRole('textbox', { name: 'After' })).toHaveTextContent(
    'new text',
  )
  expect(screen.queryByRole('textbox', { name: 'Before' })).toBeNull()
  await user.click(screen.getByRole('button', { name: 'Wrap lines' }))
  await user.click(screen.getByRole('button', { name: 'Ignore whitespace' }))
  first.unmount()
  const saved = preferenceStorage.getItem('olivares.prefs')!
  usePreferencesStore.setState({
    diffDisplay: { layout: 'split', wrap: true, whitespace: false },
  })
  preferenceStorage.setItem('olivares.prefs', saved)
  await usePreferencesStore.persist.rehydrate()
  render(<CodeDiff {...props} />)
  expect(screen.getByRole('radio', { name: 'Unified' })).toHaveAttribute(
    'aria-checked',
    'true',
  )
  expect(screen.getByRole('button', { name: 'Wrap lines' })).toHaveAttribute(
    'aria-pressed',
    'false',
  )
  expect(
    screen.getByRole('button', { name: 'Ignore whitespace' }),
  ).toHaveAttribute('aria-pressed', 'true')
  await user.click(screen.getByRole('radio', { name: 'Split' }))
  expect(screen.getByRole('textbox', { name: 'Before' })).toHaveTextContent(
    'old text',
  )
})

it('can ignore spacing-only changes while preserving the revision text', async () => {
  usePreferencesStore.setState({
    diffDisplay: { layout: 'split', wrap: true, whitespace: false },
  })
  const { container } = render(
    <CodeDiff
      original="const a = 1\n"
      modified="const  a = 1\n"
      language="text"
      originalLabel="Before"
      modifiedLabel="After"
    />,
  )
  expect(container.querySelector('.cm-changedLine')).not.toBeNull()
  await userEvent.click(
    screen.getByRole('button', { name: 'Ignore whitespace' }),
  )
  expect(container.querySelector('.cm-changedLine')).toBeNull()
  expect(screen.getByRole('textbox', { name: 'Before' })).toHaveTextContent(
    'const a = 1',
  )
  expect(screen.getByRole('textbox', { name: 'After' }).textContent).toContain(
    'const  a = 1',
  )
})
