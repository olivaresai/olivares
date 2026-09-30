// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { screen, within } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
const auth = vi.hoisted(() => ({ read: true }))
beforeEach(() => {
  auth.read = true
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tenant-fixture',
    principal: { user_id: 'human-fixture' },
    can: (p: string) => auth.read && p === 'sessions:run:read',
  }),
}))
vi.mock('@/components/layout/tenant-label', () => ({
  useTenantLabel: () => ({
    tenant: 'tenant-fixture',
    name: 'Fixture organization',
  }),
}))
import { SessionContextPane } from './session-context-pane'
import { mergeSessions } from './provenance'
import type { RunDTO } from '@/features/agentops/types'
import type { SessionResolution } from './use-session-resolution'
const run: RunDTO = {
  run_ref: 'run-fixture',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'idle',
  last_event_seq: 0,
  pep_provisioned: false,
  record_io: false,
  critical: false,
  process_state: 'running',
  work_scope: {
    role: 'orchestrator',
    workspace_id: '01a0ef7c-f25c-72f1-9cf8-3b5b2c8d6ab0',
    capabilities: ['work.read'],
    grant_id: '01a0ef7c-f25a-7f2b-a336-aed3df18206f',
  },
}
function mount(value: RunDTO | null) {
  const session = mergeSessions(
    [],
    [value ?? { ...run, work_scope: undefined }],
  )[0]
  const resolution: SessionResolution = {
    target: { runRef: 'run-fixture' },
    session: { ...session, runs: value ? [value] : [] },
    live: undefined,
    runs: value ? [value] : [],
    related: [],
    streamStatus: 'open',
    operateUnknown: !value,
    observeUnknown: true,
    loading: false,
    grants: {
      liveRead: false,
      runRead: !!value,
      runWrite: false,
      runAdmin: false,
    },
  }
  return renderIntel(<SessionContextPane resolution={resolution} />)
}
it('shows the authorized launch role/workspace separately from process and activity', () => {
  mount(run)
  const section = screen.getByTestId('context-work-scope')
  expect(within(section).getByText('Orchestrator')).toBeInTheDocument()
  expect(
    within(section).getByTitle(run.work_scope!.workspace_id),
  ).toBeInTheDocument()
  expect(screen.getByTestId('context-process-state')).toHaveTextContent(
    'Running',
  )
  expect(screen.getByTestId('context-activity-state')).toHaveTextContent('Idle')
  expect(within(section).getByText('Read work')).toBeInTheDocument()
})
it('does not infer a legacy run role from its current profile or selected workspace', () => {
  mount({
    ...run,
    process_state: undefined,
    work_scope: undefined,
    provider_profile_ref: 'ppf_current',
  })
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Not recorded',
  )
  expect(screen.queryByText('Orchestrator')).toBeNull()
  expect(screen.getByTestId('context-process-state')).toHaveTextContent(
    'Not recorded',
  )
})
it('retains the recorded grant after stop without claiming that it is active authority', () => {
  mount({ ...run, state: 'stopped', process_state: 'stopped' })
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Orchestrator',
  )
  expect(screen.getByTestId('context-process-state')).toHaveTextContent(
    'Stopped',
  )
  expect(
    screen.getByText(
      'Recorded for the last launch. Every action checks current authorization.',
    ),
  ).toBeInTheDocument()
})
it('withholds run scope when the operated half is not authorized', () => {
  mount(null)
  expect(screen.queryByText('Orchestrator')).toBeNull()
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Not recorded',
  )
})

it('hides a previously read run grant immediately when run-read permission is withdrawn', () => {
  auth.read = false
  mount(run)
  expect(screen.queryByText('Orchestrator')).toBeNull()
  expect(screen.getByTestId('context-work-scope')).toHaveTextContent(
    'Not recorded',
  )
})
