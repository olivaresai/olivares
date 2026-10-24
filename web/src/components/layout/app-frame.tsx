// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE APP FRAME (console Estate, issue #2544): no sidebar.
//
//   frame ─┬─ sheet (the canvas, full width; it does not float, so it has no shadow)
//          │    ├─ top bar (56 px: scope, places, search, notifications, account)
//          │    ├─ body: main [+ side panel 380 px; 340 px ≤ 1360 px; overlay < 1100 px]
//          │    └─ status bar (24 px: engine, running, usage, stops … Stop all, version)
//          └─ phone bar (< 761 px), the frame's own last row
//
// DOM order is reading and Tab order: skip link, top bar, work, status bar. The phone bar
// is a row of the frame, never an element fixed to the viewport and never inside the work,
// so it takes its height from the frame and occludes nothing.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { SidePanelHost } from './side-panel'

export function AppFrame({
  topbar,
  statusBar,
  phoneBar,
  overlays,
  children,
}: {
  topbar: ReactNode
  /** The thin row under the work that says how things stand (status-bar.tsx). */
  statusBar?: ReactNode
  phoneBar: ReactNode
  /** The palette, the area directory and other portalled layers. */
  overlays?: ReactNode
  children: ReactNode
}) {
  const { t } = useTranslation('common')
  return (
    <div
      data-slot="app-frame"
      className="grid h-svh grid-cols-1 grid-rows-[minmax(0,1fr)_auto] overflow-hidden bg-canvas text-text print:block print:h-auto print:overflow-visible"
    >
      {/* WCAG 2.4.1 Bypass Blocks: the first stop on every page skips the top bar. */}
      <a
        href="#main-content"
        onClick={() => {
          document.getElementById('main-content')?.focus()
        }}
        className="sr-only z-50 rounded-ctl outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:top-2 focus-visible:left-2 focus-visible:bg-accent focus-visible:px-3 focus-visible:py-2 focus-visible:text-body focus-visible:font-medium focus-visible:text-on-accent focus-visible:ring-2 focus-visible:ring-focus"
      >
        {t('a11y.skipToContent')}
      </a>
      <div
        data-slot="sheet"
        className="flex min-h-0 min-w-0 flex-col overflow-hidden bg-canvas print:overflow-visible"
      >
        {topbar}
        {/* The body is a ROW: the work, then the panel beside it. Nothing sits below
            the work inside it: `main` is the last element of its column. The status bar
            is the sheet's own last row, under the work and the panel alike. */}
        <div
          data-slot="sheet-body"
          className="relative flex min-h-0 flex-1 print:block"
        >
          {/* THE WORK REGION. `min-h-0` is load-bearing: without it a flex child refuses
              to shrink below its content, so a route that scrolls inside itself would
              grow the shell instead. No padding and no width: the frame of the work is
              the route's choice (page-frames.tsx). */}
          <main
            id="main-content"
            tabIndex={-1}
            className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden outline-none print:overflow-visible"
          >
            {children}
          </main>
          <SidePanelHost />
        </div>
        {statusBar}
      </div>
      <div data-slot="phone-bar" className="min-[761px]:hidden print:hidden">
        {phoneBar}
      </div>
      {overlays}
    </div>
  )
}
