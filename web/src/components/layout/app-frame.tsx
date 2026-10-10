// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE APP FRAME (redesign §3.2):
//
//   frame ─┬─ sidebar (240 px full or 56 px rail, the sidebar's own choice; hidden < 761 px)
//          └─ sheet (canvas, radius 14 px, 8 px from the window edges)
//               ├─ top bar
//               └─ body: main [+ side panel 380 px; 340 px ≤ 1360 px; overlay < 1100 px]
//   phone (< 761 px): the sheet full screen + the phone bar as the frame's own last row
//
// DOM order is reading and Tab order: skip link, sidebar, top bar, work. The phone bar is
// a row of the frame, never an element fixed to the viewport and never inside the work,
// so it takes its height from the frame and occludes nothing.
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { SidePanelHost } from './side-panel'

export function AppFrame({
  sidebar,
  topbar,
  phoneBar,
  overlays,
  children,
}: {
  sidebar: ReactNode
  topbar: ReactNode
  phoneBar: ReactNode
  /** The palette, the area directory and other portalled layers. */
  overlays?: ReactNode
  children: ReactNode
}) {
  const { t } = useTranslation('common')
  return (
    <div
      data-slot="app-frame"
      className={cn(
        'grid h-svh grid-cols-1 grid-rows-[minmax(0,1fr)_auto] overflow-hidden bg-frame text-text',
        'min-[761px]:grid-cols-[auto_minmax(0,1fr)] min-[761px]:grid-rows-1',
        'print:block print:h-auto print:overflow-visible',
      )}
    >
      {/* WCAG 2.4.1 Bypass Blocks: the first stop on every page skips the sidebar. */}
      <a
        href="#main-content"
        onClick={() => {
          document.getElementById('main-content')?.focus()
        }}
        className="sr-only z-50 rounded-ctl bg-accent px-3 py-2 text-body font-medium text-on-accent outline-none focus-visible:not-sr-only focus-visible:absolute focus-visible:top-2 focus-visible:left-2 focus-visible:ring-2 focus-visible:ring-focus"
      >
        {t('a11y.skipToContent')}
      </a>
      <div
        data-slot="sidebar"
        className="hidden min-h-0 min-[761px]:flex print:hidden"
      >
        {sidebar}
      </div>
      <div
        data-slot="sheet"
        className={cn(
          'flex min-h-0 min-w-0 flex-col overflow-hidden bg-canvas',
          'min-[761px]:m-2 min-[761px]:ml-0 min-[761px]:rounded-panel min-[761px]:border min-[761px]:border-line min-[761px]:shadow-card',
          'print:m-0 print:overflow-visible print:border-0',
        )}
      >
        {topbar}
        {/* The body is a ROW: the work, then the panel beside it. Nothing sits below
            the work: `main` is the last element of its column. */}
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
      </div>
      <div data-slot="phone-bar" className="min-[761px]:hidden print:hidden">
        {phoneBar}
      </div>
      {overlays}
    </div>
  )
}
