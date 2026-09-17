// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// The Sessions workspace endpoint currently enforces its role permission without an
// AAL3 gate. These cases defensively verify the client's typed error routing; they do
// not prove that the server route requires step-up authentication.
import { QueryClientProvider, QueryClient } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    principal: { aal: 1 },
  }),
}))

const api = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  createWorkspace: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { WorkspacesPanel } from './workspaces-panel'

const stepUpError = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')
const roleError = () =>
  new ApiError(403, 'forbidden', 'your role cannot create workspaces')

let queryClient: QueryClient | undefined
let view: ReturnType<typeof render> | undefined

const wrap = (ui: ReactNode) => {
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  view = render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
  return view
}

/** Open the dialog and submit its minimal valid form. */
async function createWorkspace() {
  const user = userEvent.setup()
  wrap(<WorkspacesPanel />)
  await user.click(
    await screen.findByRole('button', { name: /register workspace/i }),
  )
  await user.type(await screen.findByLabelText(/^name$/i), 'w1')
  await user.type(await screen.findByLabelText(/^root path$/i), '/srv/w1')
  await user.click(screen.getByRole('button', { name: /^register$/i }))
  return user
}

describe('Sessions workspace mutation error routing', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useStepUpStore.getState().clear()
    api.listWorkspaces.mockResolvedValue({ items: [], has_more: false })
  })

  afterEach(() => {
    view?.unmount()
    useStepUpStore.getState().clear()
    queryClient?.clear()
    view = undefined
    queryClient = undefined
  })

  it('publishes a real step-up demand without an error toast', async () => {
    api.createWorkspace.mockRejectedValue(stepUpError())
    await createWorkspace()

    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    const request = useStepUpStore.getState().request
    expect(request?.action).toBe('generic')
    expect(request?.owner.current()).toBe(true)
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('reports a role refusal without publishing a ceremony', async () => {
    api.createWorkspace.mockRejectedValue(roleError())
    await createWorkspace()

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(useStepUpStore.getState().request).toBeNull()
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('reports an ordinary server failure as an error', async () => {
    api.createWorkspace.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    await createWorkspace()

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(useStepUpStore.getState().request).toBeNull()
  })

  it('distinguishes the two 403 responses by code', () => {
    expect(stepUpError().status).toBe(403)
    expect(roleError().status).toBe(403)
    expect(stepUpError().isForbidden).toBe(true)
    expect(stepUpError().isStepUpRequired).toBe(true)
    expect(roleError().isStepUpRequired).toBe(false)
  })
})
