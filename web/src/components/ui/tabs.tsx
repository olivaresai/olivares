// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Content, List, Root, Trigger } from '@radix-ui/react-tabs'
import { useRouter, type AnyRouter } from '@tanstack/react-router'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ComponentProps,
  type Ref,
} from 'react'
import { cn } from '@/lib/utils'

/**
 * Tabs — ledger/underline navigation over Radix Tabs (not a pill switcher). The list
 * is a hairline-bottomed strip; the active trigger lifts to `foreground` with a copper
 * underline that overlaps the strip border (`-mb-px` + `border-b-2`). Color-only
 * transitions keep it calm; Radix supplies roving-tabindex and ARIA for free.
 *
 * OVERFLOW, measured on the rendered console (console-ui-current-baseline, 2026-09-06):
 * `/console` carries eleven tabs, `/knowledge` nine, `/health` and `/finops` eight. The
 * strip already scrolled horizontally but with its scrollbar hidden, so at 1024 px the
 * eleventh tab was cut to "Sec" with nothing on screen saying more existed, and a
 * `?tab=license` deep link selected a tab that was out of view. What the list does now,
 * without touching a single caller:
 *
 * - `data-overflow` on the shell (`none | start | end | both`) reports which edge hides
 *   tabs; the strip fades at that edge (a mask, so it works over any surface colour) and
 *   a pointer scroll button appears there. The buttons are `aria-hidden` and out of the
 *   Tab order on purpose: keyboard users already reach every tab with the arrow keys
 *   (Radix roving tabindex), so a second pair of stops would be noise for them.
 * - The focused tab and the selected tab are scrolled into view HORIZONTALLY, by
 *   moving the strip's own `scrollLeft` — never `scrollIntoView`, which would also
 *   scroll the page to a card lower down when a deep link lands. That move is
 *   INSTANT on purpose: with the strip on `scroll-behavior: smooth`, the browser's
 *   own focus scroll and the reveal both animated, and a keyboard user pressing
 *   ArrowRight every few hundred milliseconds landed on positions read mid-animation
 *   (measured in the browser evidence of console-ui-layout-density). Only the pointer
 *   scroll buttons animate, through `scrollTo({ behavior: 'smooth' })`, and they
 *   respect `prefers-reduced-motion` by falling back to an instant jump.
 * - GEOMETRY CHANGES reveal too (independent review F1, 2026-09-06): the first
 *   version only re-measured overflow when the strip or a tab changed size, so a
 *   window resized from 1440 to 390 px left the selected AND focused "Edition &
 *   license" tab at 1033–1168 px beside a 16–374 px strip. Now every size change the
 *   ResizeObserver reports (viewport, sidebar rail, fonts landing, labels changing
 *   language) and every tab mount/unmount re-reveals: the focused tab when focus is
 *   inside the list (manual activation keeps its distinct focused trigger visible
 *   without selecting it or moving focus), otherwise the selected tab. A plain
 *   `scroll` event is NOT a geometry change — pointer, wheel and touch scrolling of
 *   the strip stay under the operator's control and are never snapped back.
 * - ROUTER RESTORATION is ordered, not raced (console-tab-scroll-restoration,
 *   2026-09-06): the app router (`scrollRestoration: true`) writes cached element scroll
 *   offsets back after every navigation renders, and on `/console` that write landed on
 *   the strip AFTER the reveal of the tab just selected, with a position cached before
 *   it — 3/11 selected tabs visible at 390 px. The four tab setters now navigate with
 *   `resetScroll: false`, which keeps the router out of a tab switch; for every other
 *   navigation the strip subscribes to the router and, only when the router's restore
 *   MOVED the strip, reveals the current tab again (see the effect below). An operator
 *   scroll, or a restore that leaves the current tab visible, is never touched.
 * - Touch and trackpad scroll natively (`overflow-x-auto`); the wheel/pointer case is
 *   what the buttons are for.
 * - Vertical lists (`orientation="vertical"` on the Root) are left alone: the shell
 *   reports `none` and adds nothing.
 *
 * `className` goes on the SHELL (the box a caller positions with margins); every other
 * prop, including `ref`, reaches the Radix `tablist` element.
 */
export function Tabs({ className, ...props }: ComponentProps<typeof Root>) {
  return <Root className={cn('flex flex-col', className)} {...props} />
}

type Overflow = 'none' | 'start' | 'end' | 'both'

/** Keep a tab clear of the fade/button at the edge it is scrolled toward. */
const EDGE_CLEARANCE_PX = 36

function computeOverflow(el: HTMLElement): Overflow {
  if (el.getAttribute('data-orientation') === 'vertical') return 'none'
  const max = el.scrollWidth - el.clientWidth
  if (max <= 1) return 'none'
  const start = el.scrollLeft > 1
  const end = max - el.scrollLeft > 1
  return start && end ? 'both' : start ? 'start' : end ? 'end' : 'none'
}

/** Scroll the strip horizontally so `tab` is fully visible, never scrolling the page. */
function revealTab(list: HTMLElement, tab: HTMLElement): void {
  if (list.getAttribute('data-orientation') === 'vertical') return
  const lr = list.getBoundingClientRect()
  const tr = tab.getBoundingClientRect()
  if (lr.width === 0 || tr.width === 0) return
  const overflows = list.scrollWidth - list.clientWidth > 1
  if (!overflows) return
  const clearance = Math.min(
    EDGE_CLEARANCE_PX,
    Math.max(0, (lr.width - tr.width) / 2),
  )
  const max = list.scrollWidth - list.clientWidth
  if (tr.left < lr.left + clearance) {
    list.scrollLeft = Math.max(
      0,
      list.scrollLeft + tr.left - lr.left - clearance,
    )
  } else if (tr.right > lr.right - clearance) {
    list.scrollLeft = Math.min(
      max,
      list.scrollLeft + tr.right - lr.right + clearance,
    )
  }
}

function assignRef<T>(ref: Ref<T> | undefined, value: T) {
  if (typeof ref === 'function') ref(value)
  else if (ref) ref.current = value
}

export function TabsList({
  className,
  ref,
  ...props
}: ComponentProps<typeof List>) {
  const listRef = useRef<HTMLDivElement | null>(null)
  const [overflow, setOverflow] = useState<Overflow>('none')

  const measure = useCallback(() => {
    const el = listRef.current
    if (!el) return
    const next = computeOverflow(el)
    setOverflow((prev) => (prev === next ? prev : next))
  }, [])

  const reveal = useCallback((tab: Element | null) => {
    const el = listRef.current
    if (!el || !(tab instanceof HTMLElement)) return
    revealTab(el, tab)
  }, [])

  /**
   * The tab whose visibility matters right now: the focused one while focus is in
   * this list (it may differ from the selected one under manual activation), else
   * the selected one. Never focuses or selects anything itself.
   */
  const revealCurrent = useCallback(() => {
    const el = listRef.current
    if (!el) return
    const focused =
      typeof document !== 'undefined' ? document.activeElement : null
    const focusedTab =
      focused instanceof HTMLElement &&
      el.contains(focused) &&
      focused.getAttribute('role') === 'tab'
        ? focused
        : null
    reveal(focusedTab ?? el.querySelector('[role="tab"][data-state="active"]'))
  }, [reveal])

  useEffect(() => {
    const el = listRef.current
    if (!el) return
    measure()
    revealCurrent()

    // Only the overflow flags follow a scroll: scrolling the strip by pointer,
    // wheel or touch is the operator's choice, not a geometry change.
    const onScroll = () => measure()
    const onFocusIn = (event: FocusEvent) => {
      const target = event.target as Element | null
      reveal(target?.closest('[role="tab"]') ?? null)
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    el.addEventListener('focusin', onFocusIn)

    // The list's own box (viewport resizes, sidebar rail) AND each tab's box (fonts
    // landing, labels changing language) decide whether there is overflow — and
    // where the current tab now sits, so it is revealed again after each change.
    // Revealing only writes `scrollLeft`, which changes no box, so the observer does
    // not re-fire on its own work.
    const ro =
      typeof ResizeObserver === 'function'
        ? new ResizeObserver(() => {
            measure()
            revealCurrent()
          })
        : null
    const observeChildren = () => {
      if (!ro) return
      ro.disconnect()
      ro.observe(el)
      for (const child of Array.from(el.children)) ro.observe(child)
    }
    observeChildren()

    // Tabs mount/unmount conditionally (e.g. a "Live" tab only while a run exists) and
    // Radix flips `data-state` on selection — including a `?tab=` deep link that
    // selects a tab out of view, and a controlled value changed by the owner.
    const mo =
      typeof MutationObserver === 'function'
        ? new MutationObserver((records) => {
            let childList = false
            for (const record of records) {
              if (record.type === 'childList') childList = true
              if (
                record.type === 'attributes' &&
                record.target instanceof HTMLElement &&
                record.target.getAttribute('role') === 'tab' &&
                record.target.getAttribute('data-state') === 'active'
              ) {
                reveal(record.target)
              }
            }
            if (childList) {
              // A tab appeared or left: the strip's geometry changed under the
              // current tab as much as a resize would.
              observeChildren()
              revealCurrent()
            }
            measure()
          })
        : null
    mo?.observe(el, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['data-state'],
    })

    return () => {
      el.removeEventListener('scroll', onScroll)
      el.removeEventListener('focusin', onFocusIn)
      ro?.disconnect()
      mo?.disconnect()
    }
  }, [measure, reveal, revealCurrent])

  // ROUTER SCROLL RESTORATION (console-tab-scroll-restoration, 2026-09-06). With
  // `scrollRestoration: true` (app/router.tsx) TanStack Router snapshots, at
  // `onBeforeLoad`, the offsets of every element that fired a `scroll` event since the
  // previous render, copies the previous location's entries onto the new one, and writes
  // them back to the DOM in its own `onRendered` subscriber. A reveal runs BEFORE the
  // navigation it leads to (focusin → reveal → Radix selects → the owner navigates) and
  // its `scroll` event is dispatched asynchronously, so the snapshot never holds the
  // reveal: what comes back is the strip's position from an earlier location. Measured
  // 2026-09-06 on /console at 390 px, End → Home → ArrowRight walk: 3/11 selected and
  // focused tabs visible, `scrollLeft` written back to 0 after every render.
  //
  // The tab setters navigate with `resetScroll: false`, which keeps the router out of a
  // tab switch entirely. This effect is for every OTHER navigation that renders while
  // the strip is mounted — Back/Forward to a cached location, a Link or command that
  // changes `?tab=` from outside. The router's restoration subscriber was registered
  // when the router was created, so this later subscriber runs after it, and a router
  // write shows as a change of `scrollLeft` between `onResolved` and `onRendered`, the
  // two events the router emits back to back once the new location has rendered. Only
  // then is the current tab revealed again; a restore that left it visible, and any
  // operator scroll, are never touched. No timers and no scroll listeners: this is
  // ordering, not a race. Outside a RouterProvider (fixtures, component tests) the hook
  // answers undefined and nothing is subscribed — that is the installed hook's own
  // no-provider contract, not a fallback of ours: any other failure of the hook propagates
  // to the error boundary like any hook error (independent review, 2026-09-06). A test
  // that partially mocks '@tanstack/react-router' therefore has to declare `useRouter`
  // in its factory, as the affected tests now do.
  const router = useRouter({ warn: false }) as AnyRouter | undefined
  useEffect(() => {
    if (!router) return
    let before: number | undefined
    const unsubscribeResolved = router.subscribe('onResolved', () => {
      before = listRef.current?.scrollLeft
    })
    const unsubscribeRendered = router.subscribe('onRendered', () => {
      const el = listRef.current
      const moved =
        before !== undefined && el !== null && el.scrollLeft !== before
      before = undefined
      if (moved) revealCurrent()
    })
    return () => {
      unsubscribeResolved()
      unsubscribeRendered()
    }
  }, [router, revealCurrent])

  const scrollByPage = (direction: -1 | 1) => {
    const el = listRef.current
    if (!el) return
    const left =
      el.scrollLeft + direction * Math.max(80, Math.round(el.clientWidth * 0.6))
    const reduce =
      typeof window !== 'undefined' &&
      typeof window.matchMedia === 'function' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches
    if (typeof el.scrollTo === 'function' && !reduce) {
      el.scrollTo({ left, behavior: 'smooth' })
    } else {
      el.scrollLeft = left
    }
    measure()
  }

  const showStart = overflow === 'start' || overflow === 'both'
  const showEnd = overflow === 'end' || overflow === 'both'

  return (
    <div
      data-slot="tabs-list-shell"
      data-overflow={overflow}
      className={cn('relative flex min-w-0', className)}
    >
      {showStart && (
        <TabsScrollButton direction={-1} onClick={() => scrollByPage(-1)} />
      )}
      <List
        ref={(node) => {
          listRef.current = node
          assignRef(ref, node)
        }}
        className={cn(
          'relative flex h-9 min-w-0 flex-1 items-center gap-1 border-b border-border',
          'overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
          // Fade the strip at the edge that hides tabs. A mask (not a painted gradient)
          // reads correctly on `background` and `surface` alike.
          overflow === 'start' &&
            '[mask-image:linear-gradient(to_right,transparent_0,#000_2.5rem,#000_100%)]',
          overflow === 'end' &&
            '[mask-image:linear-gradient(to_right,#000_0,#000_calc(100%-2.5rem),transparent_100%)]',
          overflow === 'both' &&
            '[mask-image:linear-gradient(to_right,transparent_0,#000_2.5rem,#000_calc(100%-2.5rem),transparent_100%)]',
        )}
        {...props}
      />
      {showEnd && (
        <TabsScrollButton direction={1} onClick={() => scrollByPage(1)} />
      )}
    </div>
  )
}

/**
 * Pointer-only overflow affordance. Hidden from assistive technology and out of the
 * Tab sequence because the arrow keys already reach every tab; a scroll button that
 * TOOK a Tab stop would put two extra stops before the panel on every overflowing
 * strip. `data-slot` names it for the browser evidence and the component tests.
 */
function TabsScrollButton({
  direction,
  onClick,
}: {
  direction: -1 | 1
  onClick: () => void
}) {
  const Icon = direction < 0 ? ChevronLeft : ChevronRight
  return (
    <div
      className={cn(
        'pointer-events-none absolute inset-y-0 z-10 flex items-center',
        direction < 0 ? 'left-0' : 'right-0',
      )}
    >
      <button
        type="button"
        tabIndex={-1}
        aria-hidden="true"
        data-slot="tabs-scroll-button"
        data-direction={direction < 0 ? 'start' : 'end'}
        onClick={onClick}
        className="pointer-events-auto inline-flex size-7 items-center justify-center rounded-md border border-border bg-surface text-muted-foreground shadow-xs transition-colors duration-100 ease-out hover:bg-muted hover:text-foreground"
      >
        <Icon className="size-4" aria-hidden="true" />
      </button>
    </div>
  )
}

export function TabsTrigger({
  className,
  ...props
}: ComponentProps<typeof Trigger>) {
  return (
    <Trigger
      className={cn(
        'inline-flex h-9 shrink-0 items-center -mb-px px-3 text-sm font-medium whitespace-nowrap',
        'border-b-2 border-transparent text-muted-foreground transition-colors duration-100 ease-out',
        'hover:text-foreground',
        'data-[state=active]:text-foreground data-[state=active]:border-accent-text',
        'outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'disabled:pointer-events-none disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}

export function TabsContent({
  className,
  ...props
}: ComponentProps<typeof Content>) {
  return (
    <Content
      className={cn(
        'pt-4 outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
      {...props}
    />
  )
}
