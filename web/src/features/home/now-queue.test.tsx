// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// NEEDS YOU on Now: the rail's own classification over the page Now already holds, plus
// the offered handoffs — and no sessions read of its own.
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, DEFAULT_AUTH } from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
import type { LiveDTO } from '@/features/sessions/types'
import { sessionsApi } from '@/features/sessions/api'
import * as communications from '@/features/communications/api'
import { useWorkspaceStore } from '@/stores/workspace'
import { NowQueue } from './now-queue'
import './i18n'

const auth = vi.hoisted(() => ({ superadmin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    ...DEFAULT_AUTH,
    principal: { superadmin: auth.superadmin },
    isSuperadmin: auth.superadmin,
    can: () => true,
  }),
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} {...rest}>
        {children}
      </a>
    )
  },
}))

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-coder-7a3f',
    live_ref: '01a0b0b2-e581-71b8-a163-5f73c7ed6aaa',
    attribution: 'legacy',
    cc_state: 'idle',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 1,
    tool_call_count: 0,
    first_event_at: '2026-09-17T18:47:59Z',
    last_event_at: '2026-09-17T18:48:14Z',
    duration_seconds: 15,
    ...over,
  }
}

afterEach(() => {
  auth.superadmin = true
  useWorkspaceStore.setState({ activeWorkspace: null } as never)
  vi.restoreAllMocks()
})

describe('NowQueue — what needs a person', () => {
  it('lists the sessions that went silent or that nobody claimed, and nothing else', () => {
    const spy = vi.spyOn(sessionsApi, 'live')
    renderIntel(
      <NowQueue
        state="ready"
        sessions={[
          live({
            session_ref: 's-active',
            live_ref: 'l1',
            cc_state: 'active',
            goal: 'running work',
          }),
          live({
            session_ref: 's-silent',
            live_ref: 'l2',
            cc_state: 'silent_evasion',
            goal: 'gone quiet',
          }),
          live({
            session_ref: 's-unclaimed',
            live_ref: 'l3',
            unclaimed: true,
            goal: 'nobody owns me',
          }),
          live({
            session_ref: 's-ended',
            live_ref: 'l4',
            cc_state: 'ended',
            goal: 'done',
          }),
        ]}
      />,
    )
    const rows = screen.getAllByTestId('now-needs-row')
    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('gone quiet'),
      expect.stringContaining('nobody owns me'),
    ])
    for (const r of rows)
      expect(r.getAttribute('href')).toMatch(/^\/sessions\?/)
    expect(
      screen.getByRole('heading', { name: 'Needs you' }),
    ).toBeInTheDocument()
    // It reads no sessions of its own: the page comes from Now.
    expect(spy).not.toHaveBeenCalled()
    expectNoRawI18nKeys(document.body)
  })

  it('says so when nothing waits, instead of drawing an empty list', () => {
    renderIntel(
      <NowQueue state="ready" sessions={[live({ cc_state: 'active' })]} />,
    )
    expect(screen.queryByTestId('now-needs-row')).toBeNull()
    expect(screen.getByText('Nothing waits for you.')).toBeInTheDocument()
  })

  it('reports a failed sessions read as a failure, never as "nothing waits"', () => {
    renderIntel(<NowQueue state="unavailable" sessions={undefined} />)
    expect(
      screen.getByText('The sessions could not be read.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
  })

  it('adds a handoff offered to this operator in the selected workspace', async () => {
    auth.superadmin = false
    useWorkspaceStore.setState({ activeWorkspace: 'w1' } as never)
    const inbox = vi
      .spyOn(communications, 'listHandoffInbox')
      .mockResolvedValue({
        items: [
          {
            carrier: { delivery_id: 'd-1' },
            work_item: { id: 'wi-42' },
            handoff: {
              from: { ref: 'agent:reviewer' },
              state: 'offered',
              created_at: '2026-09-17T18:40:00Z',
            },
          },
        ],
      } as never)
    renderIntel(<NowQueue state="ready" sessions={[]} />)
    const row = await screen.findByTestId('now-needs-row')
    expect(row.getAttribute('href')).toBe(
      '/communications/handoffs?handoff=d-1',
    )
    expect(row.textContent).toContain('agent:reviewer')
    expect(inbox).toHaveBeenCalledWith(
      expect.objectContaining({ workspace_id: 'w1', state: 'offered' }),
      expect.anything(),
      expect.anything(),
    )
  })

  it('puts pending approvals first, with their progress, each opening the queue', () => {
    renderIntel(
      <NowQueue
        state="ready"
        sessions={[]}
        approvals={[
          {
            id: 'ap-1',
            action: 'mcp.tools/call',
            subject_kind: 'mcp_tool',
            subject_ref: 'github/create_issue',
            requested_by: 'user:ada',
            status: 'pending',
            required_approvals: 2,
            approve_count: 1,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    const row = screen.getByTestId('now-approval-row')
    expect(row.getAttribute('href')).toBe('/permissions?tab=approvals')
    expect(row).toHaveTextContent('mcp.tools/call')
    expect(row).toHaveTextContent('github/create_issue')
    expect(row).toHaveTextContent('1 of 2')
    // An approval alone is something waiting: never "Nothing waits for you."
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
    expectNoRawI18nKeys(document.body)
  })

  it('serves a role that reads approvals but not live sessions', () => {
    const spy = vi.spyOn(sessionsApi, 'live')
    renderIntel(
      <NowQueue
        state="ready"
        sessions={undefined}
        sessionsReadable={false}
        approvals={[
          {
            id: 'ap-2',
            action: 'deploy.promote',
            requested_by: 'user:grace',
            status: 'pending',
            required_approvals: 1,
            approve_count: 0,
            reject_count: 0,
            escalated: false,
          },
        ]}
      />,
    )
    expect(screen.getByTestId('now-approval-row')).toHaveTextContent(
      'deploy.promote',
    )
    // Nothing about sessions it may not read: no failure line, no session read.
    expect(screen.queryByText('The sessions could not be read.')).toBeNull()
    expect(spy).not.toHaveBeenCalled()
  })

  it('says a failed approvals read, and never "nothing waits" over it', () => {
    renderIntel(
      <NowQueue state="ready" sessions={[]} approvalsState="unavailable" />,
    )
    expect(
      screen.getByText('Approvals could not be loaded.'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Nothing waits for you.')).toBeNull()
  })
})
