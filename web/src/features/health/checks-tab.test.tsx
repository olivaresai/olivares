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
