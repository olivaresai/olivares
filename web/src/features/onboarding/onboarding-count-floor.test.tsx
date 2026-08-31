// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EL AVISO SE PRUEBA MONTANDO LA PANTALLA, no leyendo la fuente. Un `grep` de `has_more`
// dice que la vista DECLARA el techo; solo montarla dice que el operador lo VE. La guarda
// `registry.truncation-notice` lo advierte de si misma con estas palabras: «lo que ninguna
// de las dos prueba: que el aviso sea ALCANZABLE».
//
// El defecto que cierra: el asistente pintaba «N members in this tenant.» con `members.length`,
// que es lo CARGADO. Si `/v1/members` viene recortado, ese numero es un SUELO y la frase lo
// presentaba como un total. Las dos direcciones van a proposito: con `has_more: true` tiene
// que salir el «≥», y con `has_more: false` tiene que NO salir — poner el suelo sobre un
// recuento que si es total engaña igual, solo que hacia el otro lado.
//
// "Get started" is the FIRST screen a customer is asked to use, and three of its five
// steps are privileged: they render `RequireAssurance`, which translates from the
// `identity` namespace. The wizard's chunk never imported it, so steps 2, 3 and 4
// showed `assurance.stepUpTitle`, `assurance.stepUpBody` and a button reading
// `assurance.authenticate` — the operator was blocked on his first task by a screen
// that would not say why.
//
// Why onboarding.test.tsx never saw it: it mocks `@/features/identity/assurance` with
// a pass-through, so the panel it should have been reading never rendered. This file
// runs the REAL gate at AAL1 (a password session — the state of every first boot) and
// asserts on the DOM the operator actually gets.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
// No `import './i18n'` and no hand-registered `identity` bundle: every namespace this
// file needs must come from the modules the view itself pulls in. Compensating for a
// missing registration in the test is exactly how the defect stayed invisible.

const { consoleApi, policyApi, authState } = vi.hoisted(() => ({
  consoleApi: {
    setupStatus: vi.fn(),
    listWorkspaces: vi.fn(),
    listMembers: vi.fn(),
    listSources: vi.fn(),
    listConnectors: vi.fn(),
    createWorkspace: vi.fn(),
    onboard: vi.fn(),
    testConnector: vi.fn(),
    putConnector: vi.fn(),
  },
  policyApi: { getDistribution: vi.fn(), getVersion: vi.fn() },
  // A freshly installed operator: superadmin, but a PASSWORD session (AAL1). That is
  // what makes the step-up panel — and therefore the identity namespace — render.
  authState: {
    can: (_p: string): boolean => true,
    principal: { aal: 1, amr: ['pwd'] },
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
// NOTE: `@/features/identity/assurance` is deliberately NOT mocked.
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock('@/features/console/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/console/api')>()
  return { ...actual, consoleApi }
})
vi.mock('@/features/claude-policy/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/claude-policy/api')>()
  return { ...actual, claudePolicyApi: policyApi }
})

import { OnboardingView } from './onboarding-view'

const defaultWs = {
  id: 'w0',
  tenant_id: 't1',
  name: 'Default',
  slug: 'default',
  status: 'active',
  is_default: true,
  created_at: '',
  updated_at: '',
  version: 1,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  try {
    localStorage.removeItem('olivares.onboarding.dismissed')
  } catch {
    /* ignore */
  }
  consoleApi.setupStatus.mockResolvedValue({
    completed: false,
    steps: [
      { id: 'database', completed: true },
      { id: 'connectors', completed: false },
      { id: 'identity', completed: false },
      { id: 'users', completed: false },
    ],
  })
  consoleApi.listWorkspaces.mockResolvedValue({ items: [defaultWs] })
  consoleApi.listMembers.mockResolvedValue({ items: [{ user_id: 'u0' }] })
  consoleApi.listSources.mockResolvedValue({ sources: [] })
  consoleApi.listConnectors.mockResolvedValue({ connectors: [] })
  policyApi.getDistribution.mockResolvedValue({
    surface: 'managed-settings',
    scopes: [],
  })
})

describe('OnboardingView · el recuento de miembros dice cuando es un SUELO', () => {
  it('con la lista RECORTADA (has_more: true) el recuento sale con «≥»', async () => {
    consoleApi.listMembers.mockResolvedValue({
      items: [{ user_id: 'u0' }, { user_id: 'u1' }],
      has_more: true,
    })
    wrap(<OnboardingView />)
    expect(await screen.findByText('≥ 2 members in this tenant.')).toBeTruthy()
  })

  it('con la lista COMPLETA (has_more: false) el recuento sale EXACTO, sin «≥»', async () => {
    consoleApi.listMembers.mockResolvedValue({
      items: [{ user_id: 'u0' }, { user_id: 'u1' }],
      has_more: false,
    })
    wrap(<OnboardingView />)
    expect(await screen.findByText('2 members in this tenant.')).toBeTruthy()
    expect(screen.queryByText('≥ 2 members in this tenant.')).toBeNull()
  })

  it('sin el campo (motor anterior) tambien sale EXACTO: ausente no es «recortado»', async () => {
    consoleApi.listMembers.mockResolvedValue({ items: [{ user_id: 'u0' }] })
    wrap(<OnboardingView />)
    expect(await screen.findByText('1 member in this tenant.')).toBeTruthy()
    expect(screen.queryByText(/≥/)).toBeNull()
  })
})
