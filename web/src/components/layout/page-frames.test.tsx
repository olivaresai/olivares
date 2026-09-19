// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL CONTRACT, and only the parts of it that are CONTRACT.
//
// jsdom does not lay out, so nothing here measures a pixel. What it pins is what the
// pixels depend on: which routes get the workbench, that the document frame is the
// element that both scrolls AND carries the padding, and that neither frame stretches
// its content. The pixel budgets themselves are measured on captures, because a class
// string is not a measurement and a test that asserts one turns every design change
// into a red cell.
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import {
  ConsolePage,
  frameFor,
  RouteFrame,
  WorkPane,
  WORK_MODE_PATHS,
} from './page-frames'

describe('frameFor — which routes own the viewport', () => {
  it('gives the workbench to the pane routes and to their children', () => {
    for (const path of WORK_MODE_PATHS) {
      expect(frameFor(path)).toBe('work')
      expect(frameFor(`${path}/abc`)).toBe('work')
      // A trailing slash is the same route, not an unknown one.
      expect(frameFor(`${path}/`)).toBe('work')
    }
  })

  it('matches on a path SEGMENT, so /work never claims /workspace-templates', () => {
    // The whole reason `frameFor` is a function with a test rather than a
    // `startsWith`: `/work` and `/workspace-templates` share five characters, and a
    // prefix match would have given the workbench to a management table — which is a
    // route whose content would then sit in a frame that clips instead of scrolls.
    expect(frameFor('/workspace-templates')).toBe('document')
    expect(frameFor('/workspaces')).toBe('document')
    expect(frameFor('/sessions-archive')).toBe('document')
    expect(frameFor('/agentops-export')).toBe('document')
  })

  it('gives the document frame to everything else, including the root', () => {
    // Home is a document ON PURPOSE: it reads top to bottom — composer, work
    // in progress, verbs, numbers — and has no panes to divide a viewport between.
    expect(frameFor('/')).toBe('document')
    // ⛔ AND `/work` IS ONE TOO, which is a correction this pass made to its own design
    //    rather than to the code: the route's view is `IntelPage` → `Tabs` →
    //    `SectionCard`, read top to bottom. In the workbench frame it filled 47 % of the
    //    work region at 1440 (measured, seeded engine) and — because that frame is
    //    `overflow-hidden` by design — a decisions tab longer than the viewport would
    //    have had no scroller at all.
    expect(frameFor('/work')).toBe('document')
    expect(frameFor('/work/abc')).toBe('document')
    expect(frameFor('/settings')).toBe('document')
    expect(frameFor('/models')).toBe('document')
    expect(frameFor('/audit')).toBe('document')
  })
})

describe('the frames themselves', () => {
  it('the document frame is the element that scrolls AND the element that pads', () => {
    // If the scroll container were the parent, the bottom padding would scroll away
    // with the content and the last row would sit flush against the viewport edge —
    // which is what shows as a sliced last row on every table.
    const { container } = render(<ConsolePage>content</ConsolePage>)
    const frame = container.querySelector(
      '[data-slot="console-page"]',
    ) as HTMLElement
    expect(frame.className).toContain('overflow-y-auto')
    expect(frame.className).toContain('min-h-0')
    const inner = frame.firstElementChild as HTMLElement
    expect(inner.className).toContain('py-4')
    expect(inner.className).toContain('max-w-page')
  })

  it('home keeps the document scroller and drops the top padding', () => {
    // 16 px of py-4 above the title made the first work row 16 px later than the
    // budget (header 48 + title 24 + composer 64 = 136) before anything else sat
    // on the page. Other document routes keep the padded frame.
    const home = render(<RouteFrame pathname="/">home</RouteFrame>)
    const homeBody = home.container.querySelector(
      '[data-slot="console-page-body"]',
    ) as HTMLElement
    expect(homeBody.className).toMatch(/\bpt-0\b/)
    expect(homeBody.className).not.toMatch(/\bpy-4\b/)
    home.unmount()

    const settings = render(
      <RouteFrame pathname="/settings">settings</RouteFrame>,
    )
    const settingsBody = settings.container.querySelector(
      '[data-slot="console-page-body"]',
    ) as HTMLElement
    expect(settingsBody.className).toMatch(/\bpy-4\b/)
  })

  it('the document frame does not stretch its content', () => {
    // The honest answer to a screen measured as "the content occupies the top third and
    // the rest is blank" on 40+ routes): a short page is SHORT. A frame that stretched
    // would centre or space content into an emptied container, which is how a screen
    // starts reading as half broken rather than as brief.
    const { container } = render(<ConsolePage>content</ConsolePage>)
    const frame = container.querySelector(
      '[data-slot="console-page"]',
    ) as HTMLElement
    expect(frame.className).not.toContain('justify-center')
    expect(frame.className).not.toContain('items-center')
    expect(frame.className).not.toContain('h-full')
  })

  it('the work frame carries no padding of its own and cannot grow the shell', () => {
    const { container } = render(<WorkPane>panes</WorkPane>)
    const frame = container.querySelector(
      '[data-slot="work-pane"]',
    ) as HTMLElement
    // `min-h-0` is what lets a pane scroll inside itself instead of growing the
    // column; without it a flex child refuses to shrink below its content.
    expect(frame.className).toContain('min-h-0')
    expect(frame.className).toContain('flex-1')
    expect(frame.className).toContain('overflow-hidden')
    expect(frame.className).not.toMatch(/\bp-\d|\bpx-\d|\bpy-\d/)
  })
})
