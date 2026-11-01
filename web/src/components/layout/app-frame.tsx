// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE APP FRAME (0.5): the left navigation, then the sheet.
//
//   frame ─┬─ sidebar (228 px, folds to 64 px; from 761 px only, a drawer below)
//          └─ sheet (the canvas; it does not float, so it has no shadow)
//               ├─ top bar (56 px: title, search, notifications, account)
//               ├─ body: main [+ side panel 380 px; 340 px ≤ 1360 px; overlay < 1100 px]
//               └─ status bar (24 px: engine, running, usage, stops … Stop all, version)
//
// DOM order is reading and Tab order: skip link, navigation, top bar, work, status bar.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { SidePanelHost } from './side-panel'

export function AppFrame({
  sidebar,
  topbar,
  statusBar,
  overlays,
  children,
}: {
  /** The one left navigation (sidebar.tsx). */
  sidebar: ReactNode
  topbar: ReactNode
  /** The thin row under the work that says how things stand (status-bar.tsx). */
  statusBar?: ReactNode
  /** The palette, the navigation drawer and other portalled layers. */
  overlays?: ReactNode
  children: ReactNode
}) {
  const { t } = useTranslation('common')
  return (
    <div
      data-slot="app-frame"
      className="flex h-svh overflow-hidden bg-canvas text-text print:block print:h-auto print:overflow-visible"
    >
      {/* WCAG 2.4.1 Bypass Blocks: the first stop on every page skips the navigation and
          the top bar. */}
      <a
        href="#main-content"
        onClick={() => {
          document.getElementById('main-content')?.focus()
        }}
        className="sr-only z-50 rounded-ctl outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:top-2 focus-visible:left-2 focus-visible:bg-accent focus-visible:px-3 focus-visible:py-2 focus-visible:text-body focus-visible:font-medium focus-visible:text-on-accent focus-visible:ring-2 focus-visible:ring-focus"
      >
        {t('a11y.skipToContent')}
      </a>
      {sidebar}
      <div
        data-slot="sheet"
        className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-canvas print:overflow-visible"
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
      {overlays}
    </div>
  )
}
