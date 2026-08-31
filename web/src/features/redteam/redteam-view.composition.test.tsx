// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { renderIntel, screen, waitFor, within } from '@/test/intel'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  targets: vi.fn(),
  runs: vi.fn(),
  launchRun: vi.fn(),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, redteamApi: { ...actual.redteamApi, ...api } }
})

import { RedTeamView } from './redteam-view'
import './i18n'
import '@/features/_intel'

const target = {
  id: 'target-1',
  agent_ref: 'agent-1',
  name: 'Composition target',
  endpoint: 'https://agent.internal',
  scope: 'input,output',
  authorized: true,
  authorized_by: 'security@example.com',
  authorized_at: '2026-08-26T00:00:00Z',
  status: 'authorized',
  created_by: 'security@example.com',
}

const createdRun = {
  id: 'run-composition-1',
  target_ref: target.id,
  suite: 'all',
  status: 'degraded',
  total: 5,
  passed: 0,
  failed: 0,
  errors: 0,
  skipped: 5,
  score: 0,
  started_at: '2026-08-26T00:00:00Z',
  finished_at: '2026-08-26T00:00:01Z',
  by_family: {},
  owasp_failures: {},
}

beforeEach(() => {
  vi.clearAllMocks()
  api.targets.mockResolvedValue({ items: [target], has_more: false })
  api.runs.mockResolvedValue({ items: [], has_more: false })
  api.launchRun.mockImplementation(async () => {
    api.runs.mockResolvedValue({ items: [createdRun], has_more: false })
    return createdRun
  })
})

describe('RedTeamView composition', () => {
  it('wires an authorized target through the launch dialog and closes on success', async () => {
    const user = userEvent.setup()
    renderIntel(<RedTeamView />)

    const launch = await screen.findByRole('button', { name: /launch run/i })
    expect(
      launch,
      'Rendered: the real RedTeamView must enable Launch run for an authorized target',
    ).toBeEnabled()
    await user.click(launch)

    const dialog = await screen.findByRole('dialog')
    const submit = within(dialog).getByRole('button', { name: /launch run/i })
    expect(
      submit,
      'Rendered: the parent-selected target must reach the real launch dialog',
    ).toBeEnabled()
    await user.click(submit)

    await waitFor(() =>
      expect(
        api.launchRun,
        'Fired: the parent composition must dispatch redteamApi.launchRun',
      ).toHaveBeenCalledWith({ target_ref: 'target-1', suite: 'all' }),
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog'),
        'Effect: a successful run launch must close the parent-mounted dialog',
      ).not.toBeInTheDocument(),
    )
    await user.click(screen.getByRole('tab', { name: /^runs$/i }))
    expect(
      await screen.findByText(target.id),
      'Effect: the run persisted by the handler must be painted by the parent Runs tab',
    ).toBeVisible()
    expect(screen.getByText(/pending sandbox/i)).toBeVisible()
  })
})
