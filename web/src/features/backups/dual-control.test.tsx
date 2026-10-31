// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
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

import { drKeys } from './api'
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
  it.each([false, true])(
    'refreshing backup age preserves drafts unless restore policy changed (%s)',
    async (policyChanged) => {
      const saved = {
        enabled: true,
        cron: '0 2 * * *',
        retain_days: 30,
        verify_cron: '0 3 * * 0',
        last_backup: '2026-10-28T01:00:00Z',
        backup_age_seconds: 0,
      }
      api.getSchedule.mockResolvedValue(saved)
      const qc = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      render(
        <QueryClientProvider client={qc}>
          <BackupSchedule />
        </QueryClientProvider>,
      )
      const weekly = await screen.findByLabelText(/verification schedule/i)
      await userEvent.clear(weekly)
      await userEvent.type(weekly, '0 4 * * 0')
      const bucket = screen.getByLabelText(/offsite bucket/i)
      await userEvent.type(bucket, 'recovery')
      api.getSchedule.mockResolvedValue({
        ...saved,
        backup_age_seconds: 7200,
        require_dual_control_restore: policyChanged ? true : undefined,
      })
      await act(async () => {
        await qc.refetchQueries({ queryKey: drKeys.schedule() })
      })
      await screen.findByText(/2 hours old/)
      expect(weekly).toHaveValue(policyChanged ? '0 3 * * 0' : '0 4 * * 0')
      expect(bucket).toHaveValue(policyChanged ? '' : 'recovery')
      const save = screen.getByRole('button', { name: /save schedule/i })
      if (policyChanged) {
        expect(save).toBeDisabled()
        expect(
          screen.getByLabelText(/require dual-control for restore/i),
        ).toBeChecked()
      } else {
        expect(save).toBeEnabled()
      }
    },
  )
  it('shows failed verification independently and saves weekly cadence and offsite references', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: true,
      cron: '0 2 * * *',
      retain_days: 30,
      verify_cron: '0 3 * * 0',
      last_run_status: 'completed',
      last_verify_status: 'failed',
      last_verify_error: 'bundle authentication failed',
      rpo_hours: 24,
    })
    api.updateSchedule.mockResolvedValue({})
    wrap(<BackupSchedule />)
    expect(
      await screen.findByText('bundle authentication failed'),
    ).toBeVisible()
    const weekly = screen.getByLabelText(/verification schedule/i)
    await userEvent.clear(weekly)
    await userEvent.type(weekly, '0 4 * * 0')
    await userEvent.type(screen.getByLabelText(/offsite bucket/i), 'recovery')
    await userEvent.type(
      screen.getByLabelText(/credential reference/i),
      'store:dr/offsite',
    )
    await userEvent.click(
      screen.getByRole('button', { name: /save schedule/i }),
    )
    await waitFor(() =>
      expect(api.updateSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          verify_cron: '0 4 * * 0',
          offsite: expect.objectContaining({
            bucket: 'recovery',
            credential_ref: 'store:dr/offsite',
          }),
        }),
      ),
    )
  })
  it.each([
    ['none', /does not lock new copies/i],
    ['governance', /does not lock new copies/i],
    ['no_default', /does not lock new copies/i],
    ['unknown', /could not read the object lock/i],
  ])(
    'warns when the offsite bucket lock is %s',
    async (offsite_lock, warning) => {
      api.getSchedule.mockResolvedValue({
        enabled: true,
        cron: '0 2 * * *',
        retain_days: 30,
        offsite: {
          bucket: 'recovery',
          credential_ref: 'store:dr/offsite',
          gfs: { daily: 7, weekly: 4, monthly: 12, yearly: 0, keep_last: 1 },
        },
        offsite_lock,
      })
      wrap(<BackupSchedule />)
      expect(await screen.findByText(warning)).toBeVisible()
    },
  )
  it('names the COMPLIANCE mode as code, without raw backticks', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: true,
      cron: '0 2 * * *',
      retain_days: 30,
      offsite: {
        bucket: 'recovery',
        credential_ref: 'store:dr/offsite',
        gfs: { daily: 7, weekly: 4, monthly: 12, yearly: 0, keep_last: 1 },
      },
      offsite_lock: 'none',
    })
    wrap(<BackupSchedule />)
    const warning = await screen.findByText(/does not lock new copies/i)
    expect(warning.textContent).not.toContain('`')
    const modes = within(warning).getAllByText('COMPLIANCE')
    expect(modes.map((m) => m.tagName)).toEqual(['CODE', 'CODE'])
  })
  it('does not warn about a bucket with a default COMPLIANCE lock', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: true,
      cron: '0 2 * * *',
      retain_days: 30,
      offsite: {
        bucket: 'recovery',
        credential_ref: 'store:dr/offsite',
        gfs: { daily: 7, weekly: 4, monthly: 12, yearly: 0, keep_last: 1 },
      },
      offsite_lock: 'compliance',
    })
    wrap(<BackupSchedule />)
    expect(await screen.findByText(/last backup/i)).toBeVisible()
    expect(screen.queryByText(/does not lock new copies/i)).toBeNull()
    expect(screen.queryByText(/could not read the object lock/i)).toBeNull()
  })
  it('renders the restore command in its explanation without raw backticks', async () => {
    api.getSchedule.mockResolvedValue({
      enabled: false,
      cron: '',
      retain_days: 30,
      require_dual_control_restore: false,
    })
    wrap(<BackupSchedule />)
    const command = await screen.findByText('olivares dr restore')
    expect(command.tagName).toBe('CODE')
    const hint = command.closest('p')!
    expect(hint.textContent).not.toContain('`')
    expect(
      within(hint).getByRole('button', { name: 'Copy command' }),
    ).toBeInTheDocument()
  })

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

    const note = await screen.findByText(/removal requested/i)
    expect(note).toHaveAttribute('role', 'status')
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

    const note = await screen.findByText(/removal requested/i)
    expect(note).toHaveAttribute('role', 'status')
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

    const note = await screen.findByText(/removal requested/i)
    expect(note).toHaveAttribute('role', 'status')
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
    expect(screen.queryByText(/removal requested/i)).not.toBeInTheDocument()
  })
})
