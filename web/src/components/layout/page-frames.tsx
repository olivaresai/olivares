// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// TWO CONTENT FRAMES, SO A SHORT PAGE IS SHORT AND A WORK PAGE IS FULL.
//
// ⛔ THE SHELL USED TO IMPOSE ONE FRAME ON ALL 77 ROUTES, AND THAT IS WHY THE WORK
//    SURFACE HAD A 60 dvh CONSTANT IN IT. `main` wrapped every route in
//    `mx-auto max-w-page px-4 py-5` — a DOCUMENT frame — so a route that wanted to
//    divide the viewport into panes had to fight a shape it never asked for. The
//    surface's `xl:h-[60dvh] xl:min-h-[26rem]` was that fight, written down: a pane
//    height chosen to look deliberate inside a page that scrolls, rather than a pane
//    that simply takes the height it has.
//
// ⇒ So the frame is DECLARED, and a route is one or the other:
//
//    · `ConsolePage` — the document. A scroll container, one column, `max-w-page`.
//      Content is as tall as it is; the space below it is SPACE, not an emptied
//      container. That is the honest answer to a screen recorded as "half empty": a
//      short page is short — what was wrong was the 82 px of title block and
//      36 px of purpose banner above it, and that is a separate budget.
//
//    · `WorkPane` — the workbench. `flex min-h-0 flex-1`, no padding of its own, and
//      the route's panes divide the viewport and scroll inside themselves.
//
// ⛔ AND THE CHOICE IS RESOLVED FROM THE PATH IN THE SHELL, not declared in the feature
//    registry. It is a SHELL decision — how the viewport is divided — and the registry
//    is owned by the views. A route that wants the workbench says so here, in four
//    lines that a test can read, instead of in a field 77 entries have to carry.
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * The routes that own the viewport instead of scrolling inside it.
 *
 * All three hold PANES — a list, a narrative and a context pane — that must be on
 * screen at once, which is the whole reason they exist. Everything else in the
 * console is read top to bottom, and `document` is the right default for it: it is
 * what 50 of these routes already are, and a route that is wrongly a document loses
 * nothing but a scrollbar position, while a route that is wrongly a workbench loses
 * its content below the fold with no way to reach it.
 *
 * Matched as a prefix on a path SEGMENT, so `/work` claims `/work/abc` and never
 * `/workspace-templates`.
 */
export const WORK_MODE_PATHS: readonly string[] = ['/agentops', '/sessions']

/**
 * ⛔ `/work` WAS ON THAT LIST AND CAME OFF IT, MEASURED. The design named it a work-mode
 *    route, and the route's own view is a tabbed DOCUMENT: `IntelPage` → `Tabs` →
 *    `SectionCard`, top to bottom. Inside `WorkPane` that content does not divide the
 *    viewport, it just stops — measured against the seeded engine at 1440: the lowest
 *    painted pixel was 452 of 852, so 47 % of the work region, with no padding, because
 *    the workbench frame deliberately carries none.
 *
 *    And the half that would have cost an operator real work: `WorkPane` is
 *    `overflow-hidden` ON PURPOSE (a pane that forgets `min-h-0` should fail loudly), so
 *    a decisions tab longer than the viewport would have been CLIPPED WITH NO SCROLLER.
 *    The seeded estate has 5 work items and 51 decisions; a real one has more.
 *
 *    The list is what a route IS, not what we would like it to be. `/work` returns to
 *    it the day its view becomes panes — a list beside the decision it opens — which is
 *    a redesign of that screen, not a line in this file.
 */

/** Which frame a pathname gets. Exported for the test and for the shell. */
export function frameFor(pathname: string): 'document' | 'work' {
  const path = pathname.replace(/\/+$/, '') || '/'
  return WORK_MODE_PATHS.some(
    (candidate) => path === candidate || path.startsWith(`${candidate}/`),
  )
    ? 'work'
    : 'document'
}

/**
 * The document frame: the console's 50-odd management views.
 *
 * `overflow-y-auto` here and not on `main`, because the scroll container has to be the
 * element that also carries the padding — otherwise the bottom padding scrolls away
 * with the content and the last row sits flush against the viewport edge, which is
 * exactly the "sliced last row" measured at the bottom of every table.
 */
export function ConsolePage({
  children,
  className,
  pad = 'default',
}: {
  children: ReactNode
  className?: string
  /** `flush` drops the 16 px top padding. Home uses it so the first work row can
   *  sit at header + title + composer (48 + 24 + 64 = 136). Other document
   *  routes keep `default`. */
  pad?: 'default' | 'flush'
}) {
  return (
    <div
      data-slot="console-page"
      className={cn(
        'min-h-0 flex-1 overflow-y-auto print:overflow-visible',
        className,
      )}
    >
      <div
        data-slot="console-page-body"
        className={cn(
          'mx-auto w-full max-w-page px-4 sm:px-6 print:max-w-none print:px-0 print:py-0',
          pad === 'flush' ? 'pt-0 pb-4' : 'py-4',
        )}
      >
        {children}
      </div>
    </div>
  )
}

/**
 * The work frame: the route divides the viewport and scrolls inside its own panes.
 *
 * No padding: a workbench's regions carry their own, and a frame that padded them
 * would take 32 px off the work on every side to no purpose. `overflow-hidden` so a
 * pane that forgets its own `min-h-0` fails loudly (a clipped pane) instead of quietly
 * (a page that grew a second scrollbar).
 */
export function WorkPane({
  children,
  className,
}: {
  children: ReactNode
  className?: string
}) {
  return (
    <div
      data-slot="work-pane"
      className={cn(
        'flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden print:overflow-visible',
        className,
      )}
    >
      {children}
    </div>
  )
}

/** The shell's frame for the current route. */
export function RouteFrame({
  pathname,
  children,
}: {
  pathname: string
  children: ReactNode
}) {
  if (frameFor(pathname) === 'work') {
    return <WorkPane>{children}</WorkPane>
  }
  const path = pathname.replace(/\/+$/, '') || '/'
  return (
    <ConsolePage pad={path === '/' ? 'flush' : 'default'}>
      {children}
    </ConsolePage>
  )
}
