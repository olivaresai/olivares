// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  deployments: vi.fn(),
  createDeployment: vi.fn(),
  updateDeployment: vi.fn(),
  deleteDeployment: vi.fn(),
}))

vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return { ...actual, modelsApi: { ...actual.modelsApi, ...api } }
})
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('./owned-models', () => ({ OwnedModelsTab: () => <p>Owned models</p> }))
vi.mock('./datasets', () => ({ DatasetsTab: () => <p>Datasets</p> }))
vi.mock('./finetune', () => ({ FinetuneTab: () => <p>Fine-tune</p> }))
vi.mock('./admission', () => ({ AdmissionTab: () => <p>Admission</p> }))
vi.mock('./evidence', () => ({ ModelEvidenceTab: () => <p>Evidence</p> }))

import { ModelOpsView } from './model-ops-view'
import './i18n'
import '@/features/_intel'

function show() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <ModelOpsView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  window.history.replaceState({}, '', '/')
  api.deployments.mockResolvedValue({ items: [], has_more: false })
  const created = {
    id: 'deployment-composition-1',
    name: 'Composition deployment',
    runtime: 'vllm',
    deployment_type: 'local',
    status: 'active',
    governed: false,
    owned_ref: 'owned-1',
    version_ref: 'version-1',
  }
  api.createDeployment.mockImplementation(async () => {
    api.deployments.mockResolvedValue({ items: [created], has_more: false })
    return created
  })
})

describe('ModelOpsView composition', () => {
  it('mounts Deployments through its parent tab and completes a create', async () => {
    const user = userEvent.setup()
    show()

    const tab = screen.getByRole('tab', { name: /deployments/i })
    expect(
      tab,
      'Rendered: the real ModelOpsView must expose the permitted Deployments tab',
    ).toBeVisible()
    await user.click(tab)

    const newButton = await screen.findByRole('button', {
      name: /new deployment/i,
    })
    expect(
      newButton,
      'Rendered: clicking the parent tab must mount the real deployment create control',
    ).toBeEnabled()
    await user.click(newButton)
    fireEvent.change(await screen.findByLabelText(/^name/i), {
      target: { value: 'Composition deployment' },
    })
    fireEvent.change(screen.getByLabelText(/model reference/i), {
      target: { value: 'owned-1' },
    })
    fireEvent.change(screen.getByLabelText(/version reference/i), {
      target: { value: 'version-1' },
    })
    await user.click(screen.getByRole('button', { name: /^create$/i }))

    await waitFor(() =>
      expect(
        api.createDeployment,
        'Fired: the parent composition must dispatch modelsApi.createDeployment',
      ).toHaveBeenCalledWith({
        name: 'Composition deployment',
        runtime: 'vllm',
        deployment_type: 'local',
        status: 'active',
        governed: false,
        owned_ref: 'owned-1',
        version_ref: 'version-1',
      }),
    )
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog'),
        'Effect: a successful deployment create must close the parent-mounted editor',
      ).not.toBeInTheDocument(),
    )
    expect(
      api.deployments.mock.calls.length,
      'Effect: the successful create must refetch the deployment state',
    ).toBeGreaterThan(1)
    expect(
      await screen.findByText('Composition deployment'),
      'Effect: the refetched deployment returned by the handler must be painted',
    ).toBeVisible()
  })
})
