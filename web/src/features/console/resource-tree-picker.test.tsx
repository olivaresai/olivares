// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: { listResources: vi.fn() },
  authState: {
    activeTenant: 't1' as string | null,
    can: (_p: string) => true,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ResourceTreePicker } from './resource-tree-picker'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listResources.mockImplementation((params?: { parent?: string }) => {
    if (params?.parent === 'res-root') {
      return Promise.resolve({
        items: [
          {
            id: 'res-child',
            name: 'Payroll',
            kind: 'folder',
            path: '/Finance/Payroll',
            parent_id: 'res-root',
            sensitivity: 'restricted',
          },
        ],
        has_more: false,
      })
    }
    return Promise.resolve({
      items: [
        {
          id: 'res-root',
          name: 'Finance',
          kind: 'folder',
          path: '/Finance',
        },
      ],
      has_more: false,
    })
  })
})

describe('ResourceTreePicker', () => {
  it('loads roots, lazily expands children, and selects a subtree', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    wrap(<ResourceTreePicker value="" onChange={onChange} />)

    const root = await screen.findByRole('treeitem', { name: /finance/i })
    expect(root).toHaveAttribute('aria-expanded', 'false')
    await user.click(
      within(root).getByRole('button', { name: /expand finance/i }),
    )

    const child = await screen.findByRole('treeitem', { name: /payroll/i })
    // El techo va con el filtro del nivel: el recorte de este selector es POR RAMA, y el id que
    // se elija aquí queda como ancla del enlace.
    expect(api.listResources).toHaveBeenCalledWith({
      parent: 'res-root',
      limit: 1000,
    })
    await user.click(
      within(child).getByRole('button', { name: /select subtree/i }),
    )

    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith(
        'res-child',
        expect.objectContaining({ id: 'res-child', path: '/Finance/Payroll' }),
      ),
    )
  })

  /**
   * ⛔ ESTE SELECTOR PAGINA POR NIVEL y el id que se elija aquí queda como ANCLA del enlace: el
   * motor lo usa en runtime para permitir, prohibir y elegir credencial. Un recurso invisible no
   * se puede elegir ni teclear — se elige OTRO. Por eso cada nivel dice si está incompleto.
   * Lo señaló el contraste externo como el mayor coste de silencio de la pantalla.
   */
  it('un nivel recortado lo dice', async () => {
    api.listResources.mockResolvedValue({
      items: [
        { id: 'res-root', name: 'raíz', kind: 'folder', has_children: true },
      ],
      has_more: true,
    })
    wrap(<ResourceTreePicker value="" onChange={vi.fn()} />)
    expect(await screen.findByText(/this level is incomplete/i)).toBeVisible()
  })

  it('un nivel completo NO lo dice', async () => {
    api.listResources.mockResolvedValue({
      items: [
        { id: 'res-root', name: 'raíz', kind: 'folder', has_children: true },
      ],
      has_more: false,
    })
    wrap(<ResourceTreePicker value="" onChange={vi.fn()} />)
    await screen.findByText('raíz')
    expect(screen.queryByText(/this level is incomplete/i)).toBeNull()
  })

  it('shows an empty state when no resources are returned', async () => {
    api.listResources.mockResolvedValueOnce({ items: [], has_more: false })
    wrap(<ResourceTreePicker value="" onChange={vi.fn()} />)
    expect(await screen.findByText(/no resources found/i)).toBeInTheDocument()
  })
})
