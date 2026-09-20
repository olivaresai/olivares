// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { StatusDTO } from './types'
import './i18n'

const { api, permission } = vi.hoisted(() => ({
  api: {
    checks: vi.fn(),
    createCheck: vi.fn(),
    updateCheck: vi.fn(),
    deleteCheck: vi.fn(),
  },
  permission: {
    read: true,
    write: true,
    admin: true,
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (value: string) => {
      if (value === 'health:check:read') return permission.read
      if (value === 'health:check:write') return permission.write
      if (value === 'health:check:admin') return permission.admin
      return false
    },
  }),
}))

vi.mock('./api', () => ({
  healthApi: api,
  healthKeys: {
    checks: (tenant: string | null, params?: unknown) => [
      'health',
      tenant,
      'checks',
      params ?? null,
    ],
    status: (tenant: string | null) => ['health', tenant, 'status'],
  },
}))

import { ChecksTab } from './checks-tab'

const check: StatusDTO = {
  id: 'check-1',
  name: 'Agent one',
  subject_kind: 'agent',
  subject_ref: 'agent-one',
  state: 'healthy',
  desired_status: 'active',
  expected_interval_seconds: 60,
  grace_factor: 2,
  sla_target_ppm: 0,
  sla_breach_open: false,
  last_latency_ms: 12,
  last_checked_at: '2026-07-24T10:00:00Z',
}

function wrap(ui: ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  permission.read = true
  permission.write = true
  permission.admin = true
  api.checks.mockResolvedValue({ items: [check], has_more: false })
  api.createCheck.mockResolvedValue(check)
  api.updateCheck.mockResolvedValue(check)
  api.deleteCheck.mockResolvedValue(undefined)
})

describe('ChecksTab CRUD', () => {
  it('creates a check with the exact displayed payload', async () => {
    api.checks.mockResolvedValue({ items: [], has_more: false })
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    await user.click(
      await screen.findByRole('button', { name: 'Create check' }),
    )
    const dialog = screen.getByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Subject reference/),
      'agent-prod',
    )
    await user.type(within(dialog).getByLabelText('Name'), 'Production agent')
    await user.clear(
      within(dialog).getByLabelText(/Expected interval \(seconds\)/),
    )
    await user.type(
      within(dialog).getByLabelText(/Expected interval \(seconds\)/),
      '120',
    )
    await user.clear(within(dialog).getByLabelText(/Grace factor/))
    await user.type(within(dialog).getByLabelText(/Grace factor/), '3')
    await user.clear(within(dialog).getByLabelText(/SLA target \(PPM\)/))
    await user.type(
      within(dialog).getByLabelText(/SLA target \(PPM\)/),
      '999500',
    )
    await user.click(
      within(dialog).getByRole('button', { name: 'Create check' }),
    )

    await waitFor(() =>
      expect(api.createCheck).toHaveBeenCalledWith({
        name: 'Production agent',
        subject_kind: 'agent',
        subject_ref: 'agent-prod',
        expected_interval_seconds: 120,
        grace_factor: 3,
        sla_target_ppm: 999500,
        desired_status: 'active',
      }),
    )
  })

  it('keeps the subject immutable and sends sla_target_ppm on edit', async () => {
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    await user.click(
      await screen.findByRole('button', {
        name: 'Edit health check for Agent one',
      }),
    )
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('agent-one')).toBeInTheDocument()
    expect(
      within(dialog).queryByDisplayValue('agent-one'),
    ).not.toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('Name'))
    await user.type(within(dialog).getByLabelText('Name'), 'Renamed check')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(api.updateCheck).toHaveBeenCalledWith('check-1', {
        name: 'Renamed check',
        expected_interval_seconds: 60,
        grace_factor: 2,
        sla_target_ppm: 0,
        desired_status: 'active',
      }),
    )
  })

  it('sends an explicit SLA value on a partial lifecycle update', async () => {
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    await user.click(
      await screen.findByRole('button', {
        name: 'Pause health check for Agent one',
      }),
    )

    await waitFor(() =>
      expect(api.updateCheck).toHaveBeenCalledWith('check-1', {
        desired_status: 'paused',
        sla_target_ppm: 0,
      }),
    )
  })

  it('requires a danger confirmation before deleting', async () => {
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    await user.click(
      await screen.findByRole('button', {
        name: 'Delete health check for Agent one',
      }),
    )
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('recorded in the audit ledger')
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(api.deleteCheck).toHaveBeenCalledWith('check-1'))
  })

  // THE LADDER'S LAST RUNG IS A WORD, NOT AN IDENTITY. Two unnamed checks both read
  // "Unnamed", so a confirmation carrying only the ladder asks the operator to approve
  // an irreversible delete of a target it cannot distinguish. This opens the dialog for
  // the SECOND of two unnamed rows and holds the dialog itself to three things: the
  // ladder is still primary, the selected subject reference is present, the other row's
  // reference is absent. A list-row assertion cannot cover any of them. The final
  // expectation pins the deletion id, so a mock that would accept the wrong check fails.
  it('distinguishes the selected target in the confirmation when two checks are unnamed', async () => {
    const alpha: StatusDTO = {
      ...check,
      id: 'check-alpha',
      name: undefined,
      subject_ref: 'agent-alpha',
    }
    const beta: StatusDTO = {
      ...check,
      id: 'check-beta',
      name: undefined,
      subject_ref: 'agent-beta',
    }
    api.checks.mockResolvedValue({ items: [alpha, beta], has_more: false })
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    const betaRow = (await screen.findByText('agent-beta')).closest('tr')
    expect(betaRow).not.toBeNull()
    await user.click(
      within(betaRow as HTMLElement).getByRole('button', {
        name: /^Delete health check/,
      }),
    )

    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Unnamed')
    expect(dialog).toHaveTextContent('agent-beta')
    expect(dialog).not.toHaveTextContent('agent-alpha')

    await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() =>
      expect(api.deleteCheck).toHaveBeenCalledWith('check-beta'),
    )
  })

  // A LABELLED FIELD WITH NOTHING IN IT IS A FACT THE DIALOG DOES NOT HAVE. The
  // confirmation carries the subject reference beside the ladder's word so two unnamed
  // checks can be told apart; a subject that carries no reference either has nothing to
  // put there, and printing the label over an empty value tells the operator a field
  // exists and is blank rather than that there is nothing to show.
  it('omits the reference block from the confirmation when there is no reference', async () => {
    const bare: StatusDTO = {
      ...check,
      id: 'check-bare',
      name: undefined,
      subject_ref: '',
    }
    api.checks.mockResolvedValue({ items: [bare], has_more: false })
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    await user.click(
      await screen.findByRole('button', {
        name: 'Delete health check for Unnamed',
      }),
    )
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Unnamed')
    expect(within(dialog).queryByText('Subject reference')).toBeNull()

    await user.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() =>
      expect(api.deleteCheck).toHaveBeenCalledWith('check-bare'),
    )
  })

  it('renders a duplicate conflict inline in the create dialog', async () => {
    api.checks.mockResolvedValue({ items: [], has_more: false })
    api.createCheck.mockRejectedValue(new ApiError(409, 'conflict', 'conflict'))
    const user = userEvent.setup()
    wrap(<ChecksTab tenant="tenant-1" />)

    await user.click(
      await screen.findByRole('button', { name: 'Create check' }),
    )
    const dialog = screen.getByRole('dialog')
    await user.type(
      within(dialog).getByLabelText(/Subject reference/),
      'agent-prod',
    )
    await user.click(
      within(dialog).getByRole('button', { name: 'Create check' }),
    )

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      'already exists',
    )
  })
})

describe('ChecksTab RBAC actions', () => {
  it('shows write actions without exposing the admin delete action', async () => {
    permission.admin = false
    wrap(<ChecksTab tenant="tenant-1" />)
    expect(
      await screen.findByRole('button', { name: 'Create check' }),
    ).toBeInTheDocument()
    await screen.findByText('Agent one')
    expect(
      screen.getByRole('button', {
        name: 'Edit health check for Agent one',
      }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', {
        name: 'Delete health check for Agent one',
      }),
    ).not.toBeInTheDocument()
  })

  it('shows only the admin delete action when write is denied', async () => {
    permission.write = false
    wrap(<ChecksTab tenant="tenant-1" />)
    expect(await screen.findByText('Agent one')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Create check' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', {
        name: 'Edit health check for Agent one',
      }),
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', {
        name: 'Delete health check for Agent one',
      }),
    ).toBeInTheDocument()
  })

  // A ROW'S OWN CONTROLS MUST SAY WHICH SUBJECT THEY ACT ON. The ladder's last rung is
  // the word "Unnamed", so on a list of unnamed checks every row offered the same five
  // accessible names and a screen reader heard one "Delete health check for Unnamed"
  // per row. The two rows here differ only in their reference and their intent, which
  // is what makes all five names — edit, pause, resume, retire, delete — observable at
  // once: the first row is active, the second is paused.
  it('names every per-row control by the subject reference when two checks are unnamed', async () => {
    const alpha: StatusDTO = {
      ...check,
      id: 'check-alpha',
      name: undefined,
      subject_ref: 'agent-alpha',
      desired_status: 'active',
    }
    const beta: StatusDTO = {
      ...check,
      id: 'check-beta',
      name: undefined,
      subject_ref: 'agent-beta',
      desired_status: 'paused',
    }
    api.checks.mockResolvedValue({ items: [alpha, beta], has_more: false })
    wrap(<ChecksTab tenant="tenant-1" />)

    await screen.findByText('agent-alpha')
    for (const name of [
      'Edit health check for agent-alpha',
      'Pause health check for agent-alpha',
      'Retire health check for agent-alpha',
      'Delete health check for agent-alpha',
      'Edit health check for agent-beta',
      'Resume health check for agent-beta',
      'Retire health check for agent-beta',
      'Delete health check for agent-beta',
    ]) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument()
    }
    expect(screen.queryAllByRole('button', { name: /Unnamed/ })).toHaveLength(0)
  })

  // THE NAME IS A NAME, AND THE REFERENCE APPEARS ONCE. The ladder never answers with
  // the reference, so an unnamed subject must read as the word and carry the reference
  // under it; and a subject whose name IS its reference must not carry it twice —
  // which is the condition the status table of the same feature already used.
  it('paints the reference under the name only when it is not the name', async () => {
    const named: StatusDTO = {
      ...check,
      id: 'check-same',
      name: 'agent-same',
      subject_ref: 'agent-same',
    }
    const unnamed: StatusDTO = {
      ...check,
      id: 'check-none',
      name: undefined,
      subject_ref: 'agent-none',
    }
    api.checks.mockResolvedValue({ items: [named, unnamed], has_more: false })
    wrap(<ChecksTab tenant="tenant-1" />)

    const unnamedRow = (await screen.findByText('agent-none')).closest('tr')
    expect(unnamedRow).not.toBeNull()
    const face = (unnamedRow as HTMLElement).querySelector('.font-medium')
    expect(face?.textContent).toBe('Unnamed')
    expect(
      within(unnamedRow as HTMLElement).getByText('agent-none'),
    ).toBeInTheDocument()

    expect(screen.getAllByText('agent-same')).toHaveLength(1)
  })

  it('renders the read-only table with no actions when both verbs are denied', async () => {
    permission.write = false
    permission.admin = false
    wrap(<ChecksTab tenant="tenant-1" />)
    expect(await screen.findByText('Agent one')).toBeInTheDocument()
    expect(screen.queryByText('Actions')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', {
        name: 'Edit health check for Agent one',
      }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', {
        name: 'Delete health check for Agent one',
      }),
    ).not.toBeInTheDocument()
  })
})
