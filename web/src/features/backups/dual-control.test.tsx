// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api } = vi.hoisted(() => ({
  api: {
    listPendingRestores: vi.fn(),
    approveRestore: vi.fn(),
    getSchedule: vi.fn(),
    updateSchedule: vi.fn(),
  },
}))

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, drApi: { ...actual.drApi, ...api } }
})

// Decouple from i18n/toast/auth — exercise the component logic + the api call shape.
vi.mock('@/lib/hooks/use-privileged-mutation', () => ({
  usePrivilegedMutation: (opts: {
    mutationFn: () => Promise<unknown>
    onDone?: (d: unknown) => void
  }) => ({
    mutate: async () => {
      const d = await opts.mutationFn()
      opts.onDone?.(d)
    },
    isPending: false,
  }),
}))

import { BackupSchedule } from './backup-schedule'
import { PendingRestores } from './pending-restores'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => vi.clearAllMocks())

describe('PendingRestores (dual-control queue)', () => {
  it('renders nothing when there are no pending restores', async () => {
    api.listPendingRestores.mockResolvedValue({ items: [] })
    const { container } = wrap(<PendingRestores />)
    await waitFor(() => expect(api.listPendingRestores).toHaveBeenCalled())
    expect(container.textContent).not.toMatch(/awaiting approval/i)
  })

  it('lists a pending restore with its initiator and an approve action', async () => {
    api.listPendingRestores.mockResolvedValue({
      items: [
        {
          request_id: 'drr_abc',
          upload_id: 'restore-upload-xyz.drbundle',
          initiator: 'alice',
          created_at: '2026-07-09T10:00:00Z',
        },
      ],
    })
    wrap(<PendingRestores />)
    expect(await screen.findByText(/awaiting approval/i)).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.getByText('drr_abc')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /approve/i })).toBeInTheDocument()
  })
})

describe('BackupSchedule dual-control toggle', () => {
  it('persists require_dual_control_restore when toggled and saved', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: false,
      cron: '',
      retain_days: 30,
      require_dual_control_restore: false,
    })
    api.updateSchedule.mockResolvedValue({})
    wrap(<BackupSchedule />)

    const toggle = await screen.findByLabelText(
      /require dual-control for restore/i,
    )
    await userEvent.click(toggle)
    await userEvent.click(
      screen.getByRole('button', { name: /save schedule/i }),
    )

    await waitFor(() =>
      expect(api.updateSchedule).toHaveBeenCalledWith(
        expect.objectContaining({ require_dual_control_restore: true }),
      ),
    )
  })

  // Disarming the gate is deliberately NOT immediate: the engine records
  // the request, keeps holding restores, and reports the gate as still on. With
  // nothing rendered, the toggle springing back to ON after a save reads as a
  // failed save — so the pending removal has to be visible and dated.
  it('shows a pending disarm instead of a toggle that silently springs back', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: false,
      cron: '',
      retain_days: 30,
      require_dual_control_restore: true,
      dual_control_disarm_effective_at: '2026-08-07T14:16:10Z',
    })
    wrap(<BackupSchedule />)

    const toggle = await screen.findByLabelText(
      /require dual-control for restore/i,
    )
    // The gate is still ARMED while the removal is pending, and the switch shows
    // what actually gates a restore, not what was last asked for.
    expect(toggle).toBeChecked()

    const note = await screen.findByRole('status')
    expect(note.textContent ?? '').toMatch(/removal requested/i)
    expect(note.textContent ?? '').toMatch(
      new RegExp(new Date('2026-08-07T14:16:10Z').getFullYear().toString()),
    )
    // It must say the gate still holds — a note that only gave a date would read
    // as "already off, effective later".
    expect(note.textContent ?? '').toMatch(/second administrator/i)
  })

  // WHO asked is the load-bearing half of a two-person control: the second
  // administrator is being asked to let a removal stand, and until now the engine sent
  // the requester (dual_control_disarm_requested_by) while no component read it.
  it('names WHO requested the removal, not only when it lands', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: false,
      cron: '',
      retain_days: 30,
      require_dual_control_restore: true,
      dual_control_disarm_effective_at: '2026-08-07T14:16:10Z',
      dual_control_disarm_requested_by: 'ada@acme.example',
    })
    wrap(<BackupSchedule />)
    // Wait for the FORM, not for role=status: the loading spinner carries that role
    // too, so asserting on the first match reads an empty node and the cell passes or
    // fails for a reason that has nothing to do with the attribution.
    await screen.findByLabelText(/require dual-control for restore/i)

    const note = await screen.findByRole('status')
    expect(note.textContent ?? '').toMatch(/ada@acme\.example/)
    // The date half must survive alongside it, so this does not pass by replacing
    // one fact with another.
    expect(note.textContent ?? '').toMatch(/second administrator/i)
  })

  // An empty value means the reference carries NO ACCOUNT ATTRIBUTION — a legacy token
  // actor, say — and never "nobody requested it". Interpolating it anyway would print a
  // blank where the operator reads an identity, so the attribution clause is omitted
  // rather than rendered empty. Without this cell the guard is a one-branch guard and an
  // always-true condition would look identical.
  it('omits the attribution clause when the requester is not attributable', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: false,
      cron: '',
      retain_days: 30,
      require_dual_control_restore: true,
      dual_control_disarm_effective_at: '2026-08-07T14:16:10Z',
      dual_control_disarm_requested_by: '',
    })
    wrap(<BackupSchedule />)
    await screen.findByLabelText(/require dual-control for restore/i)

    const note = await screen.findByRole('status')
    // The pending note still appears in full …
    expect(note.textContent ?? '').toMatch(/removal requested/i)
    expect(note.textContent ?? '').toMatch(/second administrator/i)
    // … and carries no dangling "Requested by" with nothing after it.
    expect(note.textContent ?? '').not.toMatch(/requested by/i)
  })

  it('shows no pending-disarm note when none is pending', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: false,
      cron: '',
      retain_days: 30,
      require_dual_control_restore: true,
    })
    wrap(<BackupSchedule />)
    await screen.findByLabelText(/require dual-control for restore/i)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
