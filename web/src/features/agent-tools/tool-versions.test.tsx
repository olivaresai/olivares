// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import { agentOpsApi } from '@/features/agentops/api'
import { useTenantStore } from '@/stores/tenant'
import { agentToolsApi } from './api'
import { ToolVersions } from './tool-versions'
import { InstallDetails } from './install-details'
import './i18n'

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}))

beforeEach(() => {
  vi.restoreAllMocks()
  useTenantStore.setState({ activeTenant: null })
})

it('saves a workspace pin and removes an exact release after confirmation', async () => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [{ workspace_ref: 'folder-a', name: 'Research' }],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  const pins = vi.spyOn(agentToolsApi, 'pins').mockResolvedValue({ pins: [] })
  const pin = vi.spyOn(agentToolsApi, 'pin').mockResolvedValue({ ok: true })
  const remove = vi
    .spyOn(agentToolsApi, 'remove')
    .mockResolvedValue({ ok: true })
  const user = userEvent.setup()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ToolVersions
        driver="codex"
        name="Codex"
        installs={[
          { version: '0.1', state: 'installed' },
          { version: '0.2', state: 'installed' },
        ]}
      />
    </QueryClientProvider>,
  )
  await screen.findByRole('option', { name: 'Research' })
  await user.selectOptions(screen.getByLabelText('Workspace'), 'folder-a')
  await user.selectOptions(screen.getByLabelText('Session version'), '0.1')
  await user.click(screen.getByRole('button', { name: 'Save version' }))
  await user.click(screen.getByRole('button', { name: 'Confirm version' }))
  await waitFor(() =>
    expect(pin).toHaveBeenCalledWith(
      'tenant-a',
      { driver: 'codex', workspace_ref: 'folder-a', version: '0.1' },
      expect.any(Object),
    ),
  )
  expect(pins).toHaveBeenCalledWith('tenant-a', expect.any(AbortSignal))
  await user.click(screen.getByRole('button', { name: 'Remove Codex 0.1' }))
  expect(remove).not.toHaveBeenCalled()
  await user.click(screen.getByRole('button', { name: 'Confirm removal' }))
  await waitFor(() =>
    expect(remove).toHaveBeenCalledWith('codex', '0.1', expect.any(Object)),
  )
})

it('shows the stored pin and the named in-use refusal', async () => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [{ workspace_ref: 'folder-a', name: 'Research' }],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  vi.spyOn(agentToolsApi, 'pins').mockResolvedValue({
    pins: [{ driver: 'codex', workspace_ref: 'folder-a', version: '0.1' }],
  })
  vi.spyOn(agentToolsApi, 'remove').mockRejectedValue(
    new Error('tool_in_use: session pinned-run uses this release'),
  )
  const user = userEvent.setup()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ToolVersions
        driver="codex"
        name="Codex"
        installs={[{ version: '0.1', state: 'installed' }]}
      />
    </QueryClientProvider>,
  )
  await screen.findByRole('option', { name: 'Research' })
  await user.selectOptions(screen.getByLabelText('Workspace'), 'folder-a')
  expect(screen.getByLabelText('Session version')).toHaveValue('0.1')
  await user.click(screen.getByRole('button', { name: 'Remove Codex 0.1' }))
  await user.click(screen.getByRole('button', { name: 'Confirm removal' }))
  expect(await screen.findByRole('alert')).toHaveTextContent(
    'tool_in_use: session pinned-run uses this release',
  )
})

it('retires the pending confirmation when the selected organization changes', async () => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [{ workspace_ref: 'folder-a', name: 'Research' }],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  vi.spyOn(agentToolsApi, 'pins').mockResolvedValue({ pins: [] })
  const pin = vi.spyOn(agentToolsApi, 'pin').mockResolvedValue({ ok: true })
  const user = userEvent.setup()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <ToolVersions
        driver="codex"
        name="Codex"
        installs={[{ version: '0.1', state: 'installed' }]}
      />
    </QueryClientProvider>,
  )
  await screen.findByRole('option', { name: 'Research' })
  await user.selectOptions(screen.getByLabelText('Workspace'), 'folder-a')
  await user.selectOptions(screen.getByLabelText('Session version'), '0.1')
  await user.click(screen.getByRole('button', { name: 'Save version' }))
  expect(screen.getByRole('button', { name: 'Confirm version' })).toBeVisible()
  act(() => useTenantStore.getState().setActiveTenant('tenant-b'))
  await waitFor(() =>
    expect(
      screen.queryByRole('button', { name: 'Confirm version' }),
    ).not.toBeInTheDocument(),
  )
  expect(pin).not.toHaveBeenCalled()
  expect(screen.getByLabelText('Workspace')).toHaveValue('')
})

it('keeps the stored selection clearable after the last release is removed', async () => {
  useTenantStore.setState({ activeTenant: 'tenant-a' })
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [{ workspace_ref: 'folder-a', name: 'Research' }],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  vi.spyOn(agentToolsApi, 'pins').mockResolvedValue({
    pins: [{ driver: 'codex', workspace_ref: 'folder-a', version: '0.1' }],
  })
  const pin = vi.spyOn(agentToolsApi, 'pin').mockResolvedValue({ ok: true })
  const user = userEvent.setup()
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <InstallDetails
        driver="codex"
        name="Codex"
        open
        onOpenChange={vi.fn()}
        installs={[]}
        onPreview={vi.fn()}
        installedOutside={false}
        logins={[]}
      />
    </QueryClientProvider>,
  )
  await screen.findByRole('option', { name: 'Research' })
  await user.selectOptions(screen.getByLabelText('Workspace'), 'folder-a')
  expect(screen.getByLabelText('Session version')).toHaveValue('0.1')
  await user.selectOptions(screen.getByLabelText('Session version'), '')
  await user.click(screen.getByRole('button', { name: 'Save version' }))
  await user.click(screen.getByRole('button', { name: 'Confirm version' }))
  await waitFor(() =>
    expect(pin).toHaveBeenCalledWith(
      'tenant-a',
      { driver: 'codex', workspace_ref: 'folder-a', version: '' },
      expect.any(Object),
    ),
  )
})
