// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useEffect,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type PointerEvent,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/** Arrow keys move a pane this far; with Shift, four times as far. */
const KEY_STEP = 16
const KEY_STEP_SHIFT = 64

export interface ResizeBounds {
  min: number
  max: number
}

export interface ResizeHandleProps {
  /** What the handle resizes, spoken by a screen reader ("Resize the session list"). */
  label: string
  /** The pane whose width the handle sets. Its rendered width is where every gesture starts. */
  target: RefObject<HTMLElement | null>
  /** +1 when the handle is the pane's right edge (dragging right widens it), -1 for its left edge. */
  grow: 1 | -1
  /** The widths the pane may take, read at the start of each gesture so they follow the layout. */
  bounds: () => ResizeBounds
  /** Follows a drag live without touching state: a width in px, or null to put back the settled one. */
  onPreview?: (px: number | null) => void
  /** The width the person settled on: the end of a drag, a key press. */
  onCommit: (px: number) => void
  /** Back to the pane's default (double-click, Enter). */
  onReset: () => void
  className?: string
  style?: CSSProperties
}

function clamp(px: number, { min, max }: ResizeBounds): number {
  return Math.round(Math.min(Math.max(px, min), Math.max(min, max)))
}

/**
 * A vertical splitter that resizes the pane beside it (the WAI-ARIA window splitter): drag
 * it, or focus it and press the arrow keys (Shift for a larger step, Home and End for the
 * limits); double-click or Enter puts the pane back to its default. The caller places it
 * on the pane's edge (it is `absolute`, so the nearest positioned ancestor frames it).
 */
export function ResizeHandle({
  label,
  target,
  grow,
  bounds,
  onPreview,
  onCommit,
  onReset,
  className,
  style,
}: ResizeHandleProps) {
  const { t } = useTranslation('common')
  const [now, setNow] = useState(0)
  const [limits, setLimits] = useState<ResizeBounds>({ min: 0, max: 0 })
  const [dragging, setDragging] = useState(false)
  const drag = useRef<{
    x: number
    start: number
    limits: ResizeBounds
    last: number
  } | null>(null)
  const boundsRef = useRef(bounds)
  useEffect(() => {
    boundsRef.current = bounds
  })

  // The value a screen reader announces is the width on screen, whatever set it: a stored
  // width, the pane's default at this window size, the cap a narrow window imposes.
  useEffect(() => {
    const el = target.current
    if (!el || typeof ResizeObserver !== 'function') return
    const read = () => {
      setNow(Math.round(el.getBoundingClientRect().width))
      setLimits(boundsRef.current())
    }
    const observer = new ResizeObserver(read)
    observer.observe(el)
    return () => observer.disconnect()
  }, [target])

  const widthNow = () =>
    Math.round(target.current?.getBoundingClientRect().width ?? 0)

  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    e.currentTarget.focus()
    e.currentTarget.setPointerCapture(e.pointerId)
    const start = widthNow()
    drag.current = { x: e.clientX, start, limits: bounds(), last: start }
    setDragging(true)
  }
  const onPointerMove = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current
    if (!d) return
    const px = clamp(d.start + grow * (e.clientX - d.x), d.limits)
    if (px === d.last) return
    d.last = px
    setNow(px)
    onPreview?.(px)
  }
  const endDrag = (settle: boolean) => {
    const d = drag.current
    if (!d) return
    drag.current = null
    setDragging(false)
    if (settle && d.last !== d.start) onCommit(d.last)
    // The settled width (or the one before a cancelled drag) is the layout's again.
    onPreview?.(null)
  }
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    // Alt+Arrow is the browser's Back/Forward, Ctrl+Home the page's: not ours.
    if (e.altKey || e.ctrlKey || e.metaKey) return
    const range = bounds()
    const start = widthNow()
    let next: number
    switch (e.key) {
      case 'ArrowLeft':
      case 'ArrowRight':
        next =
          start +
          (e.key === 'ArrowRight' ? 1 : -1) *
            grow *
            (e.shiftKey ? KEY_STEP_SHIFT : KEY_STEP)
        break
      case 'Home':
        next = range.min
        break
      case 'End':
        next = range.max
        break
      case 'Enter':
        e.preventDefault()
        onReset()
        return
      default:
        return
    }
    e.preventDefault()
    const px = clamp(next, range)
    if (px !== start) onCommit(px)
  }

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuemin={limits.min || undefined}
      aria-valuemax={limits.max || undefined}
      aria-valuenow={now || undefined}
      tabIndex={0}
      title={t('resize.hint')}
      data-slot="resize-handle"
      data-dragging={dragging || undefined}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={() => endDrag(true)}
      onPointerCancel={() => endDrag(false)}
      onLostPointerCapture={() => endDrag(false)}
      onKeyDown={onKeyDown}
      onDoubleClick={onReset}
      className={cn(
        'group absolute inset-y-0 z-10 w-2 cursor-col-resize touch-none outline-none select-none',
        className,
      )}
      style={style}
    >
      <span
        aria-hidden
        className="pointer-events-none absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-transparent transition-colors group-hover:w-0.5 group-hover:bg-accent group-focus-visible:w-0.5 group-focus-visible:bg-accent group-data-[dragging]:w-0.5 group-data-[dragging]:bg-accent"
      />
    </div>
  )
}
