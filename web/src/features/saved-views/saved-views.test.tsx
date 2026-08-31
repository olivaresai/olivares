// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import type { SavedView } from './types'

const auth = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  activeRole: 'editor' as string | null,
  // The workspace this membership is confined to, or null when it is tenant-wide. It
  // reaches the console in Whoami.grants[].confined_workspace.
  confinedWorkspace: null as string | null,
  isSuperadmin: false,
  can: vi.fn((permission: string) => permission !== 'never'),
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  delete: vi.fn(),
}))
vi.mock('./api', () => ({
  savedViewsApi: api,
  savedViewsKeys: {
    all: (tenant: string | null) => ['consoleviews', tenant],
    list: (tenant: string | null, featureId: string) => [
      'consoleviews',
      tenant,
      'views',
      featureId,
    ],
  },
}))

import { SavedViewsMenu } from './saved-views-menu'

function wrap(ui: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
  )
}

const mine: SavedView = {
  id: 'view-mine',
  feature_id: 'audit',
  name: 'My investigation',
  description: 'Only mine',
  params: { actor: 'user:alice', q: '', ignored: 42 },
  owner: 'user:alice',
  shared: false,
  mine: true,
  created_at: '2026-07-24T10:00:00Z',
  updated_at: '2026-07-24T10:00:00Z',
}

const shared: SavedView = {
  id: 'view-shared',
  feature_id: 'audit',
  name: 'Team investigation',
  params: { action: 'agent.' },
  owner: 'user:bob',
  shared: true,
  mine: false,
  created_at: '2026-07-24T09:00:00Z',
  updated_at: '2026-07-24T09:00:00Z',
}

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  auth.can.mockImplementation((permission: string) => permission !== 'never')
  auth.activeTenant = 't1'
  auth.activeRole = 'editor'
  auth.confinedWorkspace = null
  auth.isSuperadmin = false
  api.list.mockResolvedValue({ items: [] })
  api.create.mockResolvedValue(mine)
  api.delete.mockResolvedValue(undefined)
})

describe('SavedViewsMenu', () => {
  it('lists mine and shared views and applies validated stored params', async () => {
    api.list.mockResolvedValue({ items: [mine, shared] })
    const onApply = vi.fn()
    wrap(<SavedViewsMenu featureId="audit" params={{}} onApply={onApply} />)

    await userEvent.click(
      await screen.findByRole('button', { name: 'Saved views' }),
    )
    expect(await screen.findByText('My views')).toBeInTheDocument()
    expect(screen.getByText('Shared')).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('menuitem', { name: 'My investigation' }),
    )

    expect(onApply).toHaveBeenCalledWith({ actor: 'user:alice' })
  })

  it('saves only non-empty current params', async () => {
    wrap(
      <SavedViewsMenu
        featureId="audit"
        params={{
          actor: 'user:alice',
          q: '',
          target_id: undefined,
        }}
        onApply={() => {}}
      />,
    )

    await userEvent.click(
      await screen.findByRole('button', { name: 'Saved views' }),
    )
    await userEvent.click(
      await screen.findByRole('menuitem', { name: 'Save current view…' }),
    )
    await userEvent.type(screen.getByRole('textbox', { name: 'Name' }), 'Hunt')
    await userEvent.type(
      screen.getByRole('textbox', { name: 'Description' }),
      'Denied agents',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Save view' }))

    await waitFor(() =>
      expect(api.create).toHaveBeenCalledWith({
        feature_id: 'audit',
        name: 'Hunt',
        description: 'Denied agents',
        params: { actor: 'user:alice' },
        shared: false,
      }),
    )
  })

  it.each([
    [409, 'a saved view with this name already exists for this feature'],
    [422, 'per-user saved-view cap reached (200)'],
  ])(
    'renders a %i save refusal inline with the server message',
    async (status, message) => {
      api.create.mockRejectedValue(new ApiError(status, 'internal', message))
      wrap(<SavedViewsMenu featureId="audit" params={{}} onApply={() => {}} />)

      await userEvent.click(
        await screen.findByRole('button', { name: 'Saved views' }),
      )
      await userEvent.click(
        await screen.findByRole('menuitem', { name: 'Save current view…' }),
      )
      await userEvent.type(
        screen.getByRole('textbox', { name: 'Name' }),
        'Hunt',
      )
      await userEvent.click(screen.getByRole('button', { name: 'Save view' }))

      expect(await screen.findByRole('alert')).toHaveTextContent(message)
    },
  )

  it('confirms before deleting a view', async () => {
    api.list.mockResolvedValue({ items: [mine] })
    wrap(<SavedViewsMenu featureId="audit" params={{}} onApply={() => {}} />)

    await userEvent.click(
      await screen.findByRole('button', { name: 'Saved views' }),
    )
    await userEvent.click(
      await screen.findByRole('menuitem', {
        name: 'Delete saved view My investigation',
      }),
    )
    expect(
      await screen.findByRole('heading', { name: 'Delete saved view?' }),
    ).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Delete view' }))

    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('view-mine'))
  })

  // Q4-F2: delete-any is the server's `Superadmin || ((admin|owner) && !confined)`
  // (modules/consoleviews/consoleviews.go, handleDelete). It is a ROLE gate the handler
  // applies itself, so no permission set can answer it — the console must read the role
  // AND the confinement, which only started travelling in whoami with.
  describe('delete-any on a view that is not mine', () => {
    const cases: Array<[string, Partial<typeof auth>, boolean]> = [
      ['an editor', { activeRole: 'editor' }, false],
      ['a tenant-wide admin', { activeRole: 'admin' }, true],
      ['a tenant-wide owner', { activeRole: 'owner' }, true],
      ['a workspace-CONFINED admin', { activeRole: 'admin', confinedWorkspace: 'ws-payments' }, false],
      ['a workspace-CONFINED owner', { activeRole: 'owner', confinedWorkspace: 'ws-payments' }, false],
      // A superadmin is never confined server-side, so the flag wins whatever else says.
      ['a superadmin, even confined', { activeRole: 'viewer', confinedWorkspace: 'ws-payments', isSuperadmin: true }, true],
    ]

    it.each(cases)('%s is offered Delete: %j -> %s', async (_name, patch, offered) => {
      Object.assign(auth, patch)
      api.list.mockResolvedValue({ items: [shared] })
      wrap(<SavedViewsMenu featureId="audit" params={{}} onApply={() => {}} />)

      await userEvent.click(
        await screen.findByRole('button', { name: 'Saved views' }),
      )
      // The view itself must render, or the absence of Delete below proves nothing.
      expect(
        await screen.findByRole('menuitem', { name: 'Team investigation' }),
      ).toBeInTheDocument()
      const del = screen.queryByRole('menuitem', {
        name: 'Delete saved view Team investigation',
      })
      expect(!!del).toBe(offered)
    })

    it('still offers Delete on MY OWN view to a confined admin', async () => {
      // Confinement removes the tenant-wide override, never ownership. Without this the
      // case above could pass by hiding Delete from everyone.
      Object.assign(auth, { activeRole: 'admin', confinedWorkspace: 'ws-payments' })
      api.list.mockResolvedValue({ items: [mine] })
      wrap(<SavedViewsMenu featureId="audit" params={{}} onApply={() => {}} />)

      await userEvent.click(
        await screen.findByRole('button', { name: 'Saved views' }),
      )
      expect(
        await screen.findByRole('menuitem', {
          name: 'Delete saved view My investigation',
        }),
      ).toBeInTheDocument()
    })
  })
})
