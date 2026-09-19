// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ «NADA COINCIDE CON ESTOS FILTROS» ES FALSO CUANDO NO HAY FILTROS.
//
//    Medido a 1440x900 sobre el motor sembrado: `/work` abre sin filtro alguno, y su
//    lista vacía afirmaba ser el resultado de uno. Son DOS estados y sólo uno tiene
//    siguiente acción — con filtro puesto, quitarlo; sin filtro, no hay nada que
//    pulsar y la frase dice de dónde vendrán las unidades.
//
//    Las dos direcciones a propósito: un testigo que sólo mirara el caso filtrado
//    pasaría igual si la vista enseñara SIEMPRE el texto de filtro.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, within } from '@/test/intel'
import '@/features/_intel'
import './i18n'

const auth = vi.hoisted(() => ({ denied: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (permission: string) => !auth.denied.has(permission),
    confinedWorkspace: null,
  }),
}))

// The unfiltered state now offers the door that STARTS work, and a door is a route
// link. There is no router in this bench, so the link is doubled as the anchor it
// renders to — what is under test is which door is offered, not how it navigates.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouterState: () => '',
  useRouter: () => undefined,
  useNavigate: () => vi.fn(),
}))

const consola = vi.hoisted(() => ({ listMembers: vi.fn() }))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi: { ...actual.consoleApi, ...consola } }
})

const api = vi.hoisted(() => ({ listWorkItems: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})

const { WorkView } = await import('./work-view')

const vacio = (texto: string) =>
  screen.getByText(texto).closest('[data-slot="empty-state"]') as HTMLElement

beforeEach(() => {
  vi.clearAllMocks()
  auth.denied = new Set()
  consola.listMembers.mockResolvedValue({ items: [], has_more: false })
  api.listWorkItems.mockResolvedValue({ items: [], has_more: false })
})

describe('La lista vacía de Work dice cuál de los dos vacíos es', () => {
  it('sin filtro: no culpa a un filtro y no ofrece quitarlo', async () => {
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    expect(screen.queryByText('No work items match')).toBeNull()
    expect(
      within(vacio('No work items yet')).queryByRole('button', {
        name: 'Clear filters',
      }),
    ).toBeNull()
  })

  it('sin filtro: ofrece EMPEZAR trabajo, que es de donde vienen las unidades', async () => {
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    const puerta = within(vacio('No work items yet')).getByRole('link', {
      name: 'Start a session',
    })
    expect(puerta.getAttribute('href')).toBe('/sessions')
  })

  it('sin el permiso de lanzar, no ofrece ninguna puerta', async () => {
    // Un estado vacío no ofrece una puerta que responda 403 al otro lado.
    auth.denied = new Set(['sessions:run:write'])
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')
    expect(within(vacio('No work items yet')).queryByRole('link')).toBeNull()
  })

  it('con filtro: lo dice y ofrece quitarlo, y quitarlo devuelve el otro texto', async () => {
    const user = userEvent.setup()
    renderIntel(<WorkView />)
    await screen.findByText('No work items yet')

    // El filtro se pone por el mismo control que usa quien lee la pantalla.
    await user.click(screen.getByRole('combobox', { name: /status/i }))
    await user.click(await screen.findByRole('option', { name: 'Ready' }))

    await screen.findByText('No work items match')
    await user.click(
      within(vacio('No work items match')).getByRole('button', {
        name: 'Clear filters',
      }),
    )
    expect(await screen.findByText('No work items yet')).toBeInTheDocument()
  })
})
