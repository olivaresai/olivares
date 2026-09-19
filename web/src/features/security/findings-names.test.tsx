// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE DIRECTORY READ THAT DID NOT ANSWER, SAID OUT LOUD.
//
// `NameLookup.ready` is documented as how a caller learns the directory read did not
// answer, and it had no reader anywhere in the console — so no screen could distinguish
// "these sessions are older than the page the engine returns" from "I am not permitted
// to see any session name on this screen". Both paint the same `sess-…` fallback in the
// same cell; only a line beside the table can tell them apart.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'

const lookup = vi.hoisted(() => ({
  ready: true,
  nameOf: (_r: string | undefined | null): string | null => null,
}))

// Partial, through `importOriginal`: this module exports the time labels, the duration
// helper and the reference helpers this table's neighbours use, and a mock that listed
// only the hook would delete them.
vi.mock('@/features/shared', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/shared')>()
  return { ...actual, useSessionNames: () => lookup }
})

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, activeTenant: 't1' }),
}))

import { FindingsTable } from './components'
import './i18n'

function finding(over: Record<string, unknown> = {}) {
  return {
    id: 'f-1',
    tenant: 'acme',
    kind: 'anomaly',
    severity: 'low' as const,
    status: 'open' as const,
    source: 'olivares.sessions',
    subject_kind: 'session',
    subject_ref: 'sess-coder-7a3f',
    title: 'An anomaly on a session',
    detail_hash: 'a'.repeat(64),
    occurred_at: '2026-09-18T10:00:00Z',
    ...over,
  }
}

const render = (findings: ReturnType<typeof finding>[]): ReactNode =>
  renderIntel(<FindingsTable findings={findings} />) as unknown as ReactNode

beforeEach(() => {
  lookup.ready = true
  lookup.nameOf = () => null
})

describe('FindingsTable — when the names could not be read, it says so', () => {
  it('the read did not answer and a session row is on screen: the table says the names are missing', () => {
    lookup.ready = false
    render([finding()])
    expect(screen.getByTestId('findings-names-unread')).toHaveTextContent(
      /could not be read/i,
    )
    // The row still shows what it CAN: the reference, which is what it always showed.
    expect(screen.getByRole('grid').textContent).toContain('sess-coder-7a3f')
  })

  it('CONTROL — the read answered and simply had no row for this session: no notice', () => {
    // Identical cell contents, different cause. This is the pair the notice exists to
    // separate, and the reason the cell alone cannot: both paint `sess-coder-7a3f`.
    lookup.ready = true
    render([finding()])
    expect(screen.queryByTestId('findings-names-unread')).toBeNull()
    expect(screen.getByRole('grid').textContent).toContain('sess-coder-7a3f')
  })

  it('CONTROL — the read did not answer but no row wanted a session name: no notice', () => {
    // A line regretting names nobody asked for is the console apologising for a read it
    // did not need.
    lookup.ready = false
    render([
      finding({ subject_kind: 'local.residency', subject_ref: 'llama3:8b' }),
    ])
    expect(screen.queryByTestId('findings-names-unread')).toBeNull()
  })

  it('a name the read DID answer is painted, so the notice is about the gap and not the column', () => {
    lookup.ready = true
    lookup.nameOf = () => 'Filed PR #7723'
    render([finding()])
    expect(screen.getByRole('grid').textContent).toContain('Filed PR #7723')
    expect(screen.queryByTestId('findings-names-unread')).toBeNull()
  })
})
