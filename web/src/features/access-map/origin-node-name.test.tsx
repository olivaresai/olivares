// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SESSION NODE ON THE ACCESS MAP IS NAMED BY WHAT IT WAS DOING.
//
// The census measured two raw references on this map (`sess-coder-7a3f`,
// `sess-coder-9c21`): an origin that is an agent or an identity carries a name a
// person chose, and a session carries only the reference the ingest stream gave it.
//
// The graph chrome is doubled the way `graph-canvas.test.tsx` doubles it — this file
// is about the LABEL, and React Flow's handles need a store this test has no business
// standing up.
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@xyflow/react', () => ({
  Handle: () => null,
  Position: { Left: 'left', Right: 'right', Top: 'top', Bottom: 'bottom' },
}))

const names = vi.hoisted(() => ({ nameOf: vi.fn() }))
vi.mock('@/features/shared', () => ({
  useSessionNames: () => ({ nameOf: names.nameOf, ready: true }),
}))

import { OriginNode } from './nodes'
import './i18n'

type NodeArgs = Parameters<typeof OriginNode>[0]

const node = (kind: string, label: string) =>
  ({
    data: { kind, label, dimmed: false },
    selected: false,
  }) as unknown as NodeArgs

function paint(ui: ReactNode) {
  return render(<>{ui}</>)
}

beforeEach(() => {
  names.nameOf.mockReset()
})

describe('OriginNode', () => {
  it('labels a session with what the live page says it was doing', () => {
    names.nameOf.mockReturnValue('create_issue · github/create_issue')
    paint(<OriginNode {...node('session', 'sess-coder-7a3f')} />)
    const label = screen.getByText('create_issue · github/create_issue')
    // The reference stays on the tooltip: it is what the filters and the detail pane
    // take.
    expect(label.getAttribute('title')).toBe('sess-coder-7a3f')
    expect(label.className).not.toContain('font-mono')
  })

  it('keeps the reference when the live page carries nothing about it', () => {
    // A node with no label is worse than a node labelled by its reference: on a graph
    // there is no row, no neighbour and no column to recover the identity from.
    names.nameOf.mockReturnValue(null)
    paint(<OriginNode {...node('session', 'sess-coder-9c21')} />)
    const label = screen.getByText('sess-coder-9c21')
    expect(label.className).toContain('font-mono')
  })

  it('asks nothing about an origin that is not a session', () => {
    paint(<OriginNode {...node('agent', 'agent-claude-coder-7')} />)
    expect(screen.getByText('agent-claude-coder-7')).toBeTruthy()
    expect(names.nameOf).not.toHaveBeenCalled()
  })
})
