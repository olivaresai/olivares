// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A PATH OR A BRANCH NAME IS CUT IN ITS MIDDLE, NEVER SIDEWAYS (#3135).
//
// The end of a path is the part a person reads (the file, the last folder), and the end of a
// branch name is the part that tells one branch from the next. So the start gives way: it
// shrinks to an ellipsis, the end stays whole, the full value is the hover and what a copy
// takes, and the copy action sits beside it. This is the one component every raw path in the
// console renders through (path-text.census.test.ts is the proof it does).
import { createEvent, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { splitMiddle } from './path-split'
import { PathText } from './path-text'

const COMBINING_GRAPHEME = 'é' // language-data: grapheme fixture
const FILE = '/srv/work/reports/2026-10/review-0142/verdict.md'

// The copy tests spy on the clipboard; none of them may leave its spy behind.
afterEach(() => {
  vi.restoreAllMocks()
})

describe('splitMiddle: where a value is cut', () => {
  it('keeps the last segment of a path whole', () => {
    expect(splitMiddle(FILE)).toEqual([
      '/srv/work/reports/2026-10/review-0142',
      '/verdict.md',
    ])
  })

  it('keeps a trailing slash with the last folder', () => {
    expect(splitMiddle('/srv/work/reports/tmp/')).toEqual([
      '/srv/work/reports',
      '/tmp/',
    ])
  })

  it('cuts a branch name at its last slash', () => {
    expect(splitMiddle('task/3135-calm-dense-console')).toEqual([
      'task',
      '/3135-calm-dense-console',
    ])
  })

  it('cuts by characters when no slash leaves a tail short enough to keep', () => {
    const long = `${'a'.repeat(40)}-0123456789`
    expect(splitMiddle(long)).toEqual([long.slice(0, -10), '0123456789'])
    const deep = `/srv/${'x'.repeat(60)}`
    expect(splitMiddle(deep)).toEqual([deep.slice(0, -10), deep.slice(-10)])
  })

  it('leaves a short value whole', () => {
    expect(splitMiddle('main')).toEqual(['main', ''])
    expect(splitMiddle('')).toEqual(['', ''])
  })

  it('counts short values in graphemes, not UTF-16 units', () => {
    const value = '🙂'.repeat(11)
    expect(splitMiddle(value)).toEqual([value, ''])
  })

  it('cuts from 21 characters and not before', () => {
    const at20 = '/srv/work/reports/xy'
    const at21 = '/srv/work/reports/xyz'
    expect(at20).toHaveLength(20)
    expect(splitMiddle(at20)).toEqual([at20, ''])
    expect(splitMiddle(at21)).toEqual(['/srv/work/reports', '/xyz'])
    const noSlash = 'a'.repeat(21)
    expect(splitMiddle(noSlash)).toEqual(['a'.repeat(11), 'a'.repeat(10)])
  })

  it('keeps a last segment of 32 graphemes, not 32 UTF-16 units', () => {
    const value = `/a/${'🙂'.repeat(20)}`
    expect(splitMiddle(value)).toEqual(['/a', `/${'🙂'.repeat(20)}`])
  })

  it('keeps a last segment of 32 characters and falls back to characters at 33', () => {
    const folder = `/${'d'.repeat(10)}`
    const keeps = `${folder}/${'x'.repeat(31)}` // the tail is "/" + 31 = 32
    expect(splitMiddle(keeps)).toEqual([folder, `/${'x'.repeat(31)}`])
    const falls = `${folder}/${'x'.repeat(32)}` // 33
    expect(splitMiddle(falls)).toEqual([falls.slice(0, -10), falls.slice(-10)])
    const deep = `/a/${'x'.repeat(40)}`
    expect(splitMiddle(deep)).toEqual([deep.slice(0, -10), deep.slice(-10)])
  })

  it('cuts at a backslash and drops every trailing slash before looking', () => {
    expect(splitMiddle('C:\\Users\\me\\work\\reports\\verdict.md')).toEqual([
      'C:\\Users\\me\\work\\reports',
      '\\verdict.md',
    ])
    expect(splitMiddle('/srv/work/reports/tmp//')).toEqual([
      '/srv/work/reports',
      '/tmp//',
    ])
  })

  it('never splits a grapheme in two', () => {
    for (const glyph of ['🙂', '👩‍💻', COMBINING_GRAPHEME]) {
      const value = `${'x'.repeat(30)}${glyph.repeat(5)}`
      const [head, tail] = splitMiddle(value)
      expect(head + tail).toBe(value)
      expect(tail).toBe(`${'x'.repeat(5)}${glyph.repeat(5)}`)
    }
  })

  it('never splits a grapheme where Intl.Segmenter is unavailable', async () => {
    const segmenter = Object.getOwnPropertyDescriptor(Intl, 'Segmenter')
    Object.defineProperty(Intl, 'Segmenter', {
      value: undefined,
      configurable: true,
    })
    try {
      vi.resetModules()
      const { splitMiddle: splitWithoutSegmenter } =
        await import('./path-split')
      const value = `/${'x'.repeat(40)}/0123456789`
      expect(splitWithoutSegmenter(value)).toEqual([value, ''])
      const prependSlash = `${'x'.repeat(30)}؀/tail`
      expect(splitWithoutSegmenter(prependSlash)).toEqual([prependSlash, ''])
      const glyphs = [
        '👩‍💻',
        COMBINING_GRAPHEME,
        String.fromCodePoint(0x1100, 0x1161, 0x11a8),
        String.fromCodePoint(0x0633, 0x200c),
      ]
      for (const glyph of glyphs) {
        const composite = `${'x'.repeat(30)}${glyph.repeat(5)}`
        expect(splitWithoutSegmenter(composite)).toEqual([composite, ''])
      }
    } finally {
      if (segmenter) Object.defineProperty(Intl, 'Segmenter', segmenter)
      else delete (Intl as { Segmenter?: unknown }).Segmenter
      vi.resetModules()
    }
  })

  it('always joins back to the value it was given', () => {
    for (const v of [FILE, '/a/b/', 'task/x', 'x'.repeat(50), '/', 'a/b/c.md'])
      expect(splitMiddle(v).join('')).toBe(v)
  })
})

describe('PathText', () => {
  it('lets the start shrink and keeps the end whole, with the whole value as the hover', () => {
    const { container } = render(<PathText value={FILE} />)
    const code = container.querySelector('code')!
    expect(code).toHaveAttribute('title', FILE)
    // Selected and copied by hand, it is the whole value, in order.
    expect(code.textContent).toBe(FILE)
    const [head, tail] = Array.from(code.children) as HTMLElement[]
    // The start gives way but keeps two characters, so the ellipsis can always show.
    expect(head).toHaveClass('truncate', 'min-w-[2ch]')
    expect(tail.textContent).toBe('/verdict.md')
    // The end does not shrink before the start has gone, and never exceeds the code.
    expect(tail).toHaveClass('shrink-0', 'max-w-full', 'truncate')
    // The element shrinks to its column instead of widening it, and reads left to right.
    expect(code).toHaveClass('min-w-0', 'max-w-full', 'overflow-hidden')
    expect(code).toHaveAttribute('dir', 'ltr')
  })

  it('shows the whole value, wrapping, where the caller reads it in full', () => {
    const { container } = render(<PathText value={FILE} wrap />)
    const code = container.querySelector('code')!
    expect(code.children).toHaveLength(1)
    expect(code.textContent).toBe(FILE)
    expect(code).toHaveClass('break-all')
    expect(code.firstElementChild).not.toHaveClass('truncate')
    expect(code).toHaveAttribute('title', FILE)
    expect(screen.getByRole('button', { name: `Copy ${FILE}` })).toBeVisible()
  })

  // Measured in Chromium (real-use run): a selection of a cut path copied as `…/work` + a line
  // break + `/infra`. jsdom adds no line break, so the selection is stubbed with the browser's text.
  describe('a copy of a selection of the value', () => {
    const copyWith = (selected: string, props: { wrap?: boolean } = {}) => {
      const { container } = render(<PathText value={FILE} {...props} />)
      const code = container.querySelector('code')!
      vi.spyOn(window, 'getSelection').mockReturnValue({
        toString: () => selected,
      } as unknown as Selection)
      const setData = vi.fn()
      const event = createEvent.copy(code, { clipboardData: { setData } })
      fireEvent(code, event)
      return { setData, event }
    }

    it('joins the two halves the browser separated with a line break', () => {
      const { setData, event } = copyWith(
        '/srv/work/reports/2026-10/review-0142\n/verdict.md',
      )
      expect(setData).toHaveBeenCalledWith('text/plain', FILE)
      expect(event.defaultPrevented).toBe(true)
    })

    it('joins a selection that starts and ends inside the value', () => {
      const { setData } = copyWith('2026-10/review-0142\n/verdict')
      expect(setData).toHaveBeenCalledWith(
        'text/plain',
        '2026-10/review-0142/verdict',
      )
    })

    it('leaves a copy alone when the selection is not part of the value', () => {
      const { setData, event } = copyWith('some other text\nmore text')
      expect(setData).not.toHaveBeenCalled()
      expect(event.defaultPrevented).toBe(false)
    })

    it('leaves a value shown whole alone: it has no two halves to join', () => {
      const { setData } = copyWith('/srv/work/reports', { wrap: true })
      expect(setData).not.toHaveBeenCalled()
    })
  })

  it('shows a value that needs no cut as one piece of text', () => {
    const { container } = render(<PathText value="main" />)
    const code = container.querySelector('code')!
    expect(code.children).toHaveLength(1)
    expect(code.textContent).toBe('main')
    // Short paths are the common case: one text node, so a search or a test finds it whole.
    render(<PathText value="/srv/work/repo" />)
    expect(screen.getByText('/srv/work/repo')).toBeInTheDocument()
  })

  it('copies the whole value, not the cut one, and says so', async () => {
    const user = userEvent.setup()
    const write = vi
      .spyOn(navigator.clipboard, 'writeText')
      .mockResolvedValue(undefined)
    render(<PathText value={FILE} />)
    await user.click(screen.getByRole('button', { name: `Copy ${FILE}` }))
    expect(write).toHaveBeenCalledWith(FILE)
    // Announced in the polite region that is always there.
    const announced = await screen.findByText('Copied')
    expect(announced.closest('[aria-live="polite"]')).not.toBeNull()
  })

  it('shows the copy action on hover and on focus, and always on a touch screen', () => {
    render(<PathText value={FILE} />)
    const button = screen.getByRole('button', { name: `Copy ${FILE}` })
    // Hidden until wanted, but still in the tab order: focus reveals it.
    expect(button).toHaveClass(
      'opacity-0',
      'group-hover/path:opacity-100',
      'group-focus-within/path:opacity-100',
      'pointer-coarse:opacity-100',
    )
    // A target of at least 24 px, and larger under a finger.
    expect(button).toHaveClass('size-6', 'pointer-coarse:size-8')
  })

  it('says it could not copy when the browser offers no clipboard (an http origin)', async () => {
    const user = userEvent.setup()
    const clipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
    Object.defineProperty(navigator, 'clipboard', {
      value: undefined,
      configurable: true,
    })
    try {
      render(<PathText value={FILE} />)
      await user.click(screen.getByRole('button', { name: `Copy ${FILE}` }))
      expect(
        await screen.findByText('Could not copy. Select the text and copy it.'),
      ).toBeInTheDocument()
    } finally {
      if (clipboard) Object.defineProperty(navigator, 'clipboard', clipboard)
      else delete (navigator as { clipboard?: unknown }).clipboard
    }
  })

  it('says when the copy failed, so the person copies by hand', async () => {
    const user = userEvent.setup()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(
      new Error('denied'),
    )
    render(<PathText value="task/3135-calm-dense-console" />)
    await user.click(screen.getByRole('button', { name: /^Copy task\// }))
    expect(
      await screen.findByText('Could not copy. Select the text and copy it.'),
    ).toBeInTheDocument()
  })

  it('offers no copy action where the caller turns it off', () => {
    render(<PathText value={FILE} copy={false} />)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('takes its look from variants, so a call site never restyles it', () => {
    const { container } = render(
      <PathText value={FILE} tone="muted" size="small" />,
    )
    const root = container.querySelector('[data-slot="path-text"]')!
    expect(root).toHaveClass('text-mono-s', 'text-text-2')
    const base = render(<PathText value={FILE} />).container.querySelector(
      '[data-slot="path-text"]',
    )!
    expect(base).toHaveClass('text-mono', 'text-text')
  })

  // A path sits inside sentences and table cells: its root must be phrasing content.
  it('keeps valid inside a paragraph', () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { container } = render(
      <p>
        Working in <PathText value={FILE} /> now.
      </p>,
    )
    expect(container.querySelector('[data-slot="path-text"]')!.tagName).toBe(
      'SPAN',
    )
    expect(error).not.toHaveBeenCalled()
    error.mockRestore()
  })
})
