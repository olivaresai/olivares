// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NINE ROUTES PAINTED A RAW IDENTIFIER WHERE A NAME BELONGS, and they were asking the
// same three questions: who is this user, which workspace is this, what was this
// session doing. These tests drive the one lookup that answers them — including the
// two answers it must refuse to give: a name it was not permitted to read, and a name
// for a reference the engine's page did not carry.
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  activeTenant: 'demo' as string | null,
  denied: new Set<string>(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: auth.activeTenant,
    can: (p: string) => !auth.denied.has(p),
  }),
}))

const api = vi.hoisted(() => ({
  listMembers: vi.fn(),
  listWorkspaces: vi.fn(),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...api } }
})

const sessions = vi.hoisted(() => ({ live: vi.fn() }))
vi.mock('@/features/sessions/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/sessions/api')>()
  return { ...actual, sessionsApi: { ...actual.sessionsApi, ...sessions } }
})

import type { LiveDTO } from '@/features/sessions/types'
import { NamedRef } from './named-ref'
import {
  bareId,
  refTail,
  sessionTitle,
  shortRef,
  useMemberNames,
  useSessionNames,
  useWorkspaceNames,
  type NameLookup,
} from './entity-names'
import './i18n'

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

/** Renders whatever the lookup answers for one reference, so the assertion is on the
 *  ANSWER and not on a mocked cache. */
function Probe({
  use,
  reference,
}: {
  use: () => NameLookup
  reference: string
}) {
  const { nameOf, ready } = use()
  return (
    <output>{`${ready ? 'ready' : 'waiting'}:${nameOf(reference) ?? 'null'}`}</output>
  )
}

const live = (over: Partial<LiveDTO>): LiveDTO => ({
  session_ref: 'sess-coder-7a3f',
  cc_state: 'idle',
  input_tokens: 0,
  output_tokens: 0,
  cost_micro_usd: 0,
  event_count: 0,
  tool_call_count: 0,
  first_event_at: '2026-09-18T10:00:00Z',
  last_event_at: '2026-09-18T10:00:10Z',
  duration_seconds: 10,
  live_ref: '01a0b580-4d95-7f27-b125-bf5e1d1c6473',
  attribution: 'legacy',
  ...over,
})

beforeEach(() => {
  vi.clearAllMocks()
  auth.activeTenant = 'demo'
  auth.denied = new Set()
})

describe('shortRef and bareId', () => {
  it('cuts a uuid at its first group and leaves a readable reference whole', () => {
    expect(shortRef('01a0b580-4f7e-79b5-a98c-32aa681a4502')).toBe('01a0b580')
    expect(shortRef('user:01a0b580-4f7e-79b5-a98c-32aa681a4502')).toBe(
      '01a0b580',
    )
    // Cutting this would destroy the only string the reader could have searched for.
    expect(shortRef('sess-coder-7a3f')).toBe('sess-coder-7a3f')
  })

  it('reads the principal out of the form the ledger writes', () => {
    expect(bareId('user:01a0')).toBe('01a0')
    expect(bareId('01a0')).toBe('01a0')
  })
})

describe('refTail — what a chip under a LABEL paints', () => {
  it('keeps the tail of a uuid, because uuid v7 shares its head', () => {
    // Minted in the same millisecond, these differ only after the fourth group: cut at
    // the first, every row of one session's inspector reads `01a0b6bb`.
    expect(refTail('01a0b6bb-3b69-71a6-9c03-4ee43fcc157e')).toBe(
      '…4ee43fcc157e',
    )
    expect(refTail('01a0b6bb-569b-7d77-a064-041945d8cedb')).toBe(
      '…041945d8cedb',
    )
    expect(refTail('01a0b6bb-3b69-71a6-9c03-4ee43fcc157e')).not.toBe(
      refTail('01a0b6bb-569b-7d77-a064-041945d8cedb'),
    )
  })

  it('drops the type prefix the row’s own label already states', () => {
    expect(refTail('xenv_01a0b6bb-3b56-73e6-8d07-badc21ebe919')).toBe(
      '…badc21ebe919',
    )
    expect(refTail('ppf_anthropic-team')).toBe('anthropic-team')
    expect(refTail('sess-coder-7a3f')).toBe('coder-7a3f')
    expect(refTail('user:01a0b6bb-3b69-71a6-9c03-4ee43fcc157e')).toBe(
      '…4ee43fcc157e',
    )
  })

  it('leaves a readable reference exactly as it is', () => {
    // Cutting one of these would destroy the only part a reader could have searched
    // for — the same rule `shortRef` follows for the caption beside a fallback name.
    expect(refTail('ws-production')).toBe('ws-production')
    expect(refTail('env-prod')).toBe('env-prod')
    expect(refTail('tnt-demo')).toBe('tnt-demo')
  })

  it('never leaves behind a string the id walk would still count', () => {
    const ID_SHAPE =
      /\b([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}|(ppf|osn|xenv|sess|run|wsp)_[0-9a-z]{6,}|sess-[a-z0-9-]{6,})/i
    for (const reference of [
      '01a0b6bb-3b69-71a6-9c03-4ee43fcc157e',
      'xenv_01a0b6bb-3b56-73e6-8d07-badc21ebe919',
      'ppf_01a0b6bb-56c1-793d-95f4-3fa76388cd64',
      'osn_01a0b6bb-56c6-7daa-9f40-7d937683795d',
      'sess-coder-7a3f',
      'ppf_anthropic-team',
    ]) {
      expect(ID_SHAPE.test(reference), reference).toBe(true)
      expect(ID_SHAPE.test(refTail(reference)), reference).toBe(false)
    }
  })
})

describe('sessionTitle', () => {
  it('degrades down the front door’s own ladder', () => {
    expect(sessionTitle(live({ summary: 'Fixing the billing runbook' }))).toBe(
      'Fixing the billing runbook',
    )
    expect(sessionTitle(live({ goal: 'Ship the invoice fix' }))).toBe(
      'Ship the invoice fix',
    )
    expect(
      sessionTitle(
        live({ current_action: 'create_issue', current_resource: 'github' }),
      ),
    ).toBe('create_issue · github')
  })

  it('answers null rather than handing back the reference as a title', () => {
    // The last rung of `workLine` is the session's own reference. A caller here paints
    // a fallback sentence instead — that rung is the defect this module exists for.
    expect(sessionTitle(live({}))).toBeNull()
  })
})

describe('useMemberNames', () => {
  it('resolves both forms of a principal reference', async () => {
    api.listMembers.mockResolvedValue({
      items: [
        {
          user_id: '01a0b580-4f7e-79b5-a98c-32aa681a4502',
          display_name: 'Administrator',
          email: 'demo@olivares.local',
        },
      ],
      has_more: false,
    })
    render(
      <Probe
        use={useMemberNames}
        reference="user:01a0b580-4f7e-79b5-a98c-32aa681a4502"
      />,
      { wrapper },
    )
    await screen.findByText('ready:Administrator')
  })

  it('falls back to the address when the account has no display name', async () => {
    api.listMembers.mockResolvedValue({
      items: [
        { user_id: 'u1', display_name: '', email: 'demo@olivares.local' },
      ],
      has_more: false,
    })
    render(<Probe use={useMemberNames} reference="user:u1" />, { wrapper })
    await screen.findByText('ready:demo@olivares.local')
  })

  it('makes no read at all without the right the people tab asks for', async () => {
    auth.denied = new Set(['user:read'])
    render(<Probe use={useMemberNames} reference="user:u1" />, { wrapper })
    await screen.findByText('waiting:null')
    expect(api.listMembers).not.toHaveBeenCalled()
  })
})

describe('useWorkspaceNames', () => {
  it('resolves a scope reference by id and by slug', async () => {
    api.listWorkspaces.mockResolvedValue({
      items: [{ id: 'ws-1', name: 'Billing', slug: 'billing' }],
      has_more: false,
    })
    render(<Probe use={useWorkspaceNames} reference="ws-1" />, { wrapper })
    await screen.findByText('ready:Billing')
    render(<Probe use={useWorkspaceNames} reference="billing" />, { wrapper })
    await waitFor(() =>
      expect(screen.getAllByText('ready:Billing').length).toBeGreaterThan(1),
    )
  })
})

describe('useSessionNames', () => {
  it('titles a session by what the engine said it was doing', async () => {
    sessions.live.mockResolvedValue({
      items: [
        live({
          current_action: 'create_issue',
          current_resource: 'github/create_issue',
        }),
      ],
      has_more: false,
    })
    render(<Probe use={useSessionNames} reference="sess-coder-7a3f" />, {
      wrapper,
    })
    await screen.findByText('ready:create_issue · github/create_issue')
  })

  it('answers null for a session the page carried nothing about', async () => {
    // `sess-coder-9c21` is seeded with no action, no goal and no summary: the honest
    // answer is that there is no title, and the caller paints "Untitled session".
    sessions.live.mockResolvedValue({
      items: [live({ session_ref: 'sess-coder-9c21' })],
      has_more: false,
    })
    render(<Probe use={useSessionNames} reference="sess-coder-9c21" />, {
      wrapper,
    })
    await screen.findByText('ready:null')
  })

  it('makes no read without `sessions:live:read`', async () => {
    auth.denied = new Set(['sessions:live:read'])
    render(<Probe use={useSessionNames} reference="sess-coder-7a3f" />, {
      wrapper,
    })
    await screen.findByText('waiting:null')
    expect(sessions.live).not.toHaveBeenCalled()
  })
})

describe('NamedRef', () => {
  it('paints the name and keeps the reference reachable', () => {
    render(
      <NamedRef
        name="create_issue · github"
        reference="sess-coder-7a3f"
        fallback="Untitled session"
      />,
    )
    const label = screen.getByText('create_issue · github')
    expect(label.closest('[title]')?.getAttribute('title')).toBe(
      'sess-coder-7a3f',
    )
    // With a name in hand the identifier is not repeated on the line.
    expect(screen.queryByText('sess-coder-7a3f')).toBeNull()
  })

  it('paints the fallback with the short reference when there is no name', () => {
    render(
      <NamedRef
        name={null}
        reference="01a0b580-4f7e-79b5-a98c-32aa681a4502"
        fallback="Unknown user"
      />,
    )
    expect(screen.getByText('Unknown user')).toBeTruthy()
    expect(screen.getByText('01a0b580')).toBeTruthy()
  })

  it('does not repeat the reference when the fallback already is it', () => {
    // `system`, `token:ci`, an audit target named by its kind: a short form beside a
    // label that already contains it reads like a second fact and is not one.
    render(
      <NamedRef name={null} reference="token:ci" fallback="token:ci" mono />,
    )
    expect(screen.getByTitle('token:ci').textContent).toBe('token:ci')
  })

  it('carries the whole truth on the tooltip when it is wider than the reference', () => {
    render(
      <NamedRef
        name={null}
        reference="01a0b580-4d33-7e39-af6e-e35c160f03f3"
        title="evals.suite: 01a0b580-4d33-7e39-af6e-e35c160f03f3"
        fallback="evals.suite"
        mono
      />,
    )
    const line = screen.getByTitle(
      'evals.suite: 01a0b580-4d33-7e39-af6e-e35c160f03f3',
    )
    expect(line.textContent?.startsWith('evals.suite')).toBe(true)
    expect(screen.getByText('01a0b580')).toBeTruthy()
  })

  it('never opens the line with the identifier', () => {
    // The census rule: no cell may START with a raw reference.
    render(
      <NamedRef
        name={null}
        reference="sess-coder-9c21"
        fallback="Untitled session"
      />,
    )
    const line = screen.getByTitle('sess-coder-9c21')
    expect(line.textContent?.startsWith('Untitled session')).toBe(true)
  })
})
