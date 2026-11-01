// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDE PANE'S TABS AT THE PANE'S OWN WIDTH.
//
// The pane is 280 to 640 px wide (surface-resize.ts) and a launched session has five tabs or
// more, wider together than the pane at its default width. The strip shows the tabs that fit,
// in order, and the rest under More; the tab in front always shows. A tab is never squeezed
// under the next one. The widths come from a hidden copy of the whole strip, so a longer
// label in another language is measured, not guessed.
import { MoreHorizontal } from 'lucide-react'
import { useLayoutEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Segmented, type SegmentedOption } from '@/components/ui/segmented'
import { IconTip } from './icon-tip'

/** What a strip takes besides its tabs: the group's padding and gap, and More beside it. */
interface StripChrome {
  padding: number
  gap: number
  /** More and the space before it. */
  more: number
}

/**
 * The tabs that show in `room`, by index: every one when they all fit; else the chosen one
 * and the leading others that fit beside More.
 */
function tabsThatFit(
  widths: readonly number[],
  chosen: number,
  room: number,
  chrome: StripChrome,
): number[] {
  const strip = (shown: readonly number[]) =>
    chrome.padding +
    shown.reduce((sum, i) => sum + widths[i]!, 0) +
    chrome.gap * Math.max(0, shown.length - 1)
  const all = widths.map((_, i) => i)
  if (strip(all) <= room) return all
  const shown = chosen >= 0 ? [chosen] : []
  for (const i of all) {
    if (i === chosen) continue
    if (strip([...shown, i]) + chrome.more > room) break
    shown.push(i)
  }
  return shown.sort((a, b) => a - b)
}

const px = (value: string) => parseFloat(value) || 0

export function PaneTabs({
  options,
  value,
  onValueChange,
  label,
  moreLabel,
}: {
  options: ReadonlyArray<SegmentedOption>
  value: string
  onValueChange: (value: string) => void
  label: string
  moreLabel: string
}) {
  const shell = useRef<HTMLDivElement>(null)
  const ruler = useRef<HTMLDivElement>(null)
  // The values on show, never indices: a tab that leaves the list (a right withdrawn)
  // must not shift the others before the next measure. null while every tab fits.
  const [shown, setShown] = useState<string[] | null>(null)
  const chosen = options.findIndex((o) => o.value === value)
  const ids = options.map((o) => o.value).join('\n')

  useLayoutEffect(() => {
    const box = shell.current
    const copy = ruler.current
    if (!box || !copy) return
    const measure = () => {
      const group = copy.querySelector<HTMLElement>('[role="radiogroup"]')
      const more = copy.querySelector<HTMLElement>(
        '[data-slot="pane-tabs-more"]',
      )
      if (!group || !more) return
      const groupStyle = getComputedStyle(group)
      const fit = tabsThatFit(
        Array.from(group.children, (tab) => (tab as HTMLElement).offsetWidth),
        chosen,
        box.clientWidth,
        {
          padding: px(groupStyle.paddingLeft) + px(groupStyle.paddingRight),
          gap: px(groupStyle.columnGap),
          more: more.offsetWidth + px(getComputedStyle(box).columnGap),
        },
      )
      const values = ids.split('\n')
      const next =
        fit.length === group.children.length ? null : fit.map((i) => values[i]!)
      setShown((prev) => (prev?.join() === next?.join() ? prev : next))
    }
    measure()
    // The pane dragged or the window resized, and labels in another language or a font
    // that landed late: each changes what fits.
    const observer = new ResizeObserver(measure)
    observer.observe(box)
    observer.observe(copy)
    return () => observer.disconnect()
  }, [chosen, ids])

  const visible = shown
    ? options.filter((o) => shown.includes(o.value))
    : options
  const hidden = options.filter((o) => !visible.includes(o))
  const more = (
    <Button
      variant="ghost"
      size="sm"
      aria-label={moreLabel}
      data-slot="pane-tabs-more"
    >
      <MoreHorizontal aria-hidden="true" />
    </Button>
  )

  return (
    <div
      ref={shell}
      data-slot="pane-tabs"
      className="relative flex min-w-0 items-center gap-1"
    >
      <Segmented
        size="sm"
        aria-label={label}
        options={visible}
        value={value}
        onValueChange={onValueChange}
      />
      {hidden.length ? (
        <DropdownMenu>
          <IconTip label={moreLabel}>
            <DropdownMenuTrigger asChild>{more}</DropdownMenuTrigger>
          </IconTip>
          <DropdownMenuContent align="end">
            <DropdownMenuRadioGroup value={value} onValueChange={onValueChange}>
              {hidden.map((o) => (
                <DropdownMenuRadioItem key={o.value} value={o.value}>
                  {o.icon}
                  {o.label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      ) : null}
      {/* The measure: every tab and More at their own widths, out of sight, out of the
          accessibility tree and out of the pane's scroll width. */}
      <div className="pointer-events-none absolute top-0 left-0 size-0 overflow-hidden">
        <div
          ref={ruler}
          aria-hidden="true"
          inert
          className="invisible flex w-max gap-1"
        >
          <Segmented
            size="sm"
            options={options}
            value={value}
            onValueChange={() => {}}
          />
          {more}
        </div>
      </div>
    </div>
  )
}
