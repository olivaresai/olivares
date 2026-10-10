// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A VIEW CAN DECLARE ITS OWN FRAME. An edition that adds a page made of panes (a list beside the
// work) says `frame: 'work'` on its registry entry; the shell then gives it the workbench without
// the page's path being written into this file. The two public pane routes keep the list.
import { render } from '@testing-library/react'
import { Cloud } from 'lucide-react'
import { describe, expect, it, vi } from 'vitest'

vi.mock('@/features/registry', async (original) => {
  const real = await original<typeof import('@/features/registry')>()
  const declared = (id: string, path: string, frame?: 'work' | 'document') =>
    ({
      id,
      path,
      icon: Cloud,
      helpHref: '/reference/fixture',
      navigation: { kind: 'feature', areaId: 'ai', sectionId: 'sessions' },
      element: () => null,
      ...(frame ? { frame } : {}),
    }) as (typeof real.FEATURE_VIEWS)[number]
  return {
    ...real,
    FEATURE_VIEWS: [
      ...real.FEATURE_VIEWS,
      declared('fixtureWork', '/app/fixture-work', 'work'),
      declared('fixtureDocument', '/app/fixture-document', 'document'),
      declared('fixtureUndeclared', '/app/fixture-undeclared'),
      declared(
        'fixtureSessionDocument',
        '/sessions/fixture-document',
        'document',
      ),
      declared(
        'fixtureChildDocument',
        '/app/fixture-work/document',
        'document',
      ),
      declared('fixtureChildUndeclared', '/app/fixture-work/undeclared'),
    ],
  }
})

import { frameFor, RouteFrame } from './page-frames'

describe('frameFor — a view declares its frame', () => {
  it('gives the workbench to a view that declares frame work, and to its children', () => {
    expect(frameFor('/app/fixture-work')).toBe('work')
    expect(frameFor('/app/fixture-work/')).toBe('work')
    expect(frameFor('/app/fixture-work/abc')).toBe('work')
  })

  it('keeps the document for a view that declares document or declares nothing', () => {
    expect(frameFor('/app/fixture-document')).toBe('document')
    expect(frameFor('/app/fixture-undeclared')).toBe('document')
  })

  it('lets a document declaration override a public work route', () => {
    expect(frameFor('/sessions/fixture-document')).toBe('document')
    expect(frameFor('/sessions/fixture-document/')).toBe('document')
    expect(frameFor('/sessions/fixture-document/abc')).toBe('document')
    expect(frameFor('/sessions/fixture-documentary')).toBe('work')
  })

  it('uses the most specific view without inheriting its parent declaration', () => {
    expect(frameFor('/app/fixture-work/document')).toBe('document')
    expect(frameFor('/app/fixture-work/document/abc')).toBe('document')
    expect(frameFor('/app/fixture-work/documentary')).toBe('work')
    expect(frameFor('/app/fixture-work/undeclared')).toBe('document')
    expect(frameFor('/app/fixture-work/undeclared/abc')).toBe('document')
    expect(frameFor('/app/fixture-work/undeclared-extra')).toBe('work')
  })

  it('leaves the two public pane routes as they were', () => {
    expect(frameFor('/sessions')).toBe('work')
    expect(frameFor('/agentops')).toBe('work')
    expect(frameFor('/settings')).toBe('document')
  })

  it('matches the declaring view on a path segment, never on a prefix of its name', () => {
    expect(frameFor('/app/fixture-workshop')).toBe('document')
  })
})

describe('RouteFrame — the declared frame is the one rendered', () => {
  it('renders the work pane for a declared work view', () => {
    const { container } = render(
      <RouteFrame pathname="/app/fixture-work">content</RouteFrame>,
    )
    expect(container.querySelector('[data-slot="work-pane"]')).not.toBeNull()
    expect(container.querySelector('[data-slot="console-page"]')).toBeNull()
  })

  it('renders the document frame for a view that declares nothing', () => {
    const { container } = render(
      <RouteFrame pathname="/app/fixture-undeclared">content</RouteFrame>,
    )
    expect(container.querySelector('[data-slot="console-page"]')).not.toBeNull()
  })

  it.each(['/sessions/fixture-document', '/app/fixture-work/document'])(
    'renders the document frame for the overriding declaration at %s',
    (pathname) => {
      const { container } = render(
        <RouteFrame pathname={pathname}>content</RouteFrame>,
      )
      expect(
        container.querySelector('[data-slot="console-page"]'),
      ).not.toBeNull()
      expect(container.querySelector('[data-slot="work-pane"]')).toBeNull()
    },
  )
})
