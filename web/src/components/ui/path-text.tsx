// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cva, type VariantProps } from 'class-variance-authority'
import type { ClipboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { CopyButton } from './copy-button'
import { splitMiddle } from './path-split'

const pathTextVariants = cva(
  'group/path inline-flex min-w-0 max-w-full items-center gap-1 align-bottom',
  {
    variants: {
      tone: { default: 'text-text', muted: 'text-text-2' },
      size: { base: 'text-mono', small: 'text-mono-s' },
    },
    defaultVariants: { tone: 'default', size: 'base' },
  },
)

export interface PathTextProps extends VariantProps<typeof pathTextVariants> {
  /** The path, or the branch or ref name, exactly as the engine sent it. */
  value: string
  /** The copy action beside the value; a caller turns it off where the row has its own, in a
   * long list, or inside a description a screen reader reads out. */
  copy?: boolean
  /** Value for the copy action when it differs from the visible label, such as a rename. */
  copyValue?: string
  /** Show the whole value and let it wrap, for a detail row where the person reads it in
   * full (the folder an approval would run in): a cut is for lists and headers. */
  wrap?: boolean
  className?: string
}

/**
 * The browser puts a line break between the two halves of a cut path when a selection of it is
 * copied (measured in Chromium: `…/work` and `/infra` came out on two lines). A copy that lies
 * inside the value is put back together, so what lands on the clipboard is the path.
 */
function copyAsOneLine(event: ClipboardEvent<HTMLElement>, value: string) {
  const joined = (window.getSelection()?.toString() ?? '').replace(/\n/g, '')
  if (joined === '' || !value.includes(joined)) return
  event.clipboardData.setData('text/plain', joined)
  event.preventDefault()
}

/**
 * PathText — the one way the console shows a filesystem path or a branch name (#3135).
 *
 * It is machine text, so it is code in the machine face. It is cut in its MIDDLE: the start
 * shrinks to an ellipsis and the end stays whole, so a long path never pushes its pane
 * sideways and the part a person reads (the file, the end of the branch) is always there. It
 * cuts only where its container has a width of its own (a flex row, a block, a table cell with
 * `w-full max-w-0`); an auto-width table cell grows to fit it. The text in the page is the whole value,
 * and a copy of a selection of it is joined back into the path; the whole value is also the
 * hover, and the copy action writes it. The copy action shows on hover and on focus, and
 * always on a touch screen, where there is no hover.
 *
 * The look comes from `tone` and `size`; `className` is for layout only.
 */
export function PathText({
  value,
  copy = true,
  copyValue,
  wrap = false,
  tone,
  size,
  className,
}: PathTextProps) {
  const { t } = useTranslation('common')
  const [head, tail] = wrap ? [value, ''] : splitMiddle(value)
  const copyText = copyValue ?? value
  return (
    <span
      data-slot="path-text"
      className={cn(
        pathTextVariants({ tone, size }),
        wrap && 'items-start',
        className,
      )}
    >
      {/* A path reads left to right whatever the page's direction. The end is capped at the
          width of the code, so a long file name is cut at its end rather than pushing sideways. */}
      <code
        dir="ltr"
        title={value}
        onCopy={wrap ? undefined : (event) => copyAsOneLine(event, value)}
        className={cn(
          'inline-flex min-w-0 max-w-full overflow-hidden font-mono',
          wrap && 'break-all',
        )}
      >
        {/* The start keeps two characters of width: at exact fit it would be 0 px wide and the
            ellipsis, which is what says "cut here", could not show (and at one character the
            box shows the path's own first slash instead of an ellipsis: `//storefront`). */}
        <span className={wrap ? 'min-w-0' : 'min-w-[2ch] truncate'}>
          {head}
        </span>
        {tail ? (
          <span className="max-w-full shrink-0 truncate">{tail}</span>
        ) : null}
      </code>
      {copy ? (
        <CopyButton
          text={() => copyText}
          label={t('ui.copy.value', { value: copyText })}
          copiedLabel={t('ui.copy.copied')}
          failedLabel={t('ui.copy.failed')}
          className="size-6 opacity-0 group-focus-within/path:opacity-100 group-hover/path:opacity-100 pointer-coarse:size-8 pointer-coarse:opacity-100"
        />
      ) : null}
    </span>
  )
}
