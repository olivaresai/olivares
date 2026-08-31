// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import {
  QueryClient,
  QueryClientProvider,
  type QueryClient as QueryClientType,
} from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { governanceKeys } from './api'
import {
  AGENT_RISK_TIERS,
  effectiveTierSource,
  type AgentRiskProfileDTO,
} from './types'

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 'tenant-one' as string | null,
  can: (_permission: string): boolean => true,
  principal: { actor: 'user:reviewer', kind: 'user' } as {
    actor: string
    kind: string
  } | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listAgentRiskProfiles: vi.fn(),
  getAgentRiskProfile: vi.fn(),
  classifyAgentRisk: vi.fn(),
  setAgentRiskTier: vi.fn(),
  reviewAgentRisk: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, governanceApi: api }
})

import { AgentRiskView } from './agent-risk-view'

// A ParseID-valid agent id (core/model/ids.go ParseID = uuid.Parse). A name or
// external ref would 400, so the console must send a real UUID.
const AGENT_UUID = '550e8400-e29b-41d4-a716-446655440000'

function wrap(ui: ReactNode): {
  queryClient: QueryClientType
} & ReturnType<typeof render> {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  })
  return {
    queryClient,
    ...render(
      <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>,
    ),
  }
}

// Override row: suggested 'low' ≠ operator 'critical' = effective 'critical'.
// The three tiers are DISTINCT so a cell that leaks the suggestion instead of
// the effective tier is observable (it would show 'Low', not 'Critical').
const overridden: AgentRiskProfileDTO = {
  id: 'p-crit',
  agent_id: 'agent:planner',
  operator_tier: 'critical',
  suggested_tier: 'low',
  effective_tier: 'critical',
  state: 'reviewed',
  reviewed_by: 'user:sec-lead',
  reviewed_at: '2099-01-01T00:00:00Z',
  signals: {
    rw_edges: 12,
    total_edges: 20,
    distinct_resources: 4,
    high_severity_findings: 0,
    critical_severity_findings: 1,
    autonomous: true,
    scheduled: false,
  },
}

// Suggestion-only row: no operator override → effective = suggested 'high'.
const suggestedOnly: AgentRiskProfileDTO = {
  id: 'p-sug',
  agent_id: 'agent:reader',
  suggested_tier: 'high',
  effective_tier: 'high',
  state: 'suggested',
  signals: {
    rw_edges: 6,
    total_edges: 8,
    distinct_resources: 2,
    high_severity_findings: 1,
    critical_severity_findings: 0,
    autonomous: false,
    scheduled: true,
  },
}

const profilesKey = governanceKeys.agentRiskProfiles('tenant-one')

beforeEach(() => {
  authState.can = () => true
  authState.principal = { actor: 'user:reviewer', kind: 'user' }
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  api.listAgentRiskProfiles.mockResolvedValue({ items: [], has_more: false })
})

afterEach(() => vi.clearAllMocks())

// --- pure invariant: effective tier provenance (mutation-verified) -----------

describe('effectiveTierSource', () => {
  it('is "operator" when the operator override is set (effective = operator_tier)', () => {
    expect(
      effectiveTierSource({ operator_tier: 'high', suggested_tier: 'low' }),
    ).toBe('operator')
  })

  it('is "suggested" when only the heuristic applies (effective = suggested_tier)', () => {
    expect(
      effectiveTierSource({ operator_tier: undefined, suggested_tier: 'low' }),
    ).toBe('suggested')
  })

  it('is "none" for an unclassified profile (neither tier present)', () => {
    expect(effectiveTierSource({})).toBe('none')
  })
})

describe('AgentRiskView — honest effective-tier rendering', () => {
  it('renders the EFFECTIVE tier (never the suggestion) prominently, per profile', async () => {
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [overridden, suggestedOnly],
      has_more: false,
    })
    wrap(<AgentRiskView />)
    await screen.findByText('agent:planner')

    // Override row: the effective element (scoped by test id) is the operator's
    // 'critical', NOT the heuristic 'low'. This assertion FAILS if the cell leaks
    // the suggested tier — the tiers are deliberately distinct.
    expect(screen.getByTestId('effective-tier-p-crit')).toHaveTextContent(
      /^Critical$/,
    )
    const critRow = screen.getByText('agent:planner').closest('tr')!
    expect(within(critRow).getByText('Override')).toBeInTheDocument()

    // Suggestion-only row: effective = suggested 'high', labelled as heuristic,
    // and carrying NO override cue.
    expect(screen.getByTestId('effective-tier-p-sug')).toHaveTextContent(
      /^High$/,
    )
    const sugRow = screen.getByText('agent:reader').closest('tr')!
    expect(within(sugRow).getByText('Heuristic suggestion')).toBeInTheDocument()
    expect(within(sugRow).queryByText('Override')).toBeNull()
  })

  /**
   * ⛔ UN NIVEL ALTO POR TRUNCADO NO ES UN NIVEL ALTO POR HALLAZGO, y la etiqueta era la misma.
   * `modules/compliance/risk.go:64-66`: `truncated` se pone cuando el barrido de hallazgos «could
   * not be completed within the bounded page budget», y **puede haberse perdido un hallazgo alto o
   * crítico**; por eso `suggestTier` nunca baja de `high` en ese caso (`:314-318`).
   *
   * EL MUTANTE: no pintarlo. El operador ve «riesgo alto» con **CERO hallazgos** y ninguna
   * explicación — y las dos lecturas piden acciones OPUESTAS: repetir la clasificación con más
   * presupuesto, o investigar al agente. El tipo de la consola ni siquiera declaraba el campo, así
   * que la distinción no era representable.
   */
  it('un nivel alto por barrido TRUNCADO se distingue de uno por hallazgo', async () => {
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [
        {
          ...suggestedOnly,
          signals: {
            ...suggestedOnly.signals,
            high_severity_findings: 0,
            critical_severity_findings: 0,
            truncated: true,
          },
        },
      ],
      has_more: false,
    })
    wrap(<AgentRiskView />)
    expect(await screen.findByText(/counts are a floor/i)).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: un barrido COMPLETO no lleva el aviso. Si saliera siempre,
   * las cuentas de al lado dejarían de leerse como lo que son —una medición— justo cuando lo son.
   */
  it('un barrido completo no dice que las cuentas sean un suelo', async () => {
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [suggestedOnly],
      has_more: false,
    })
    wrap(<AgentRiskView />)
    expect(await screen.findByText('agent:reader')).toBeInTheDocument()
    expect(screen.queryByText(/counts are a floor/i)).toBeNull()
  })

  it('claims "reviewed" (with reviewer) only when the state says so', async () => {
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [overridden, suggestedOnly],
      has_more: false,
    })
    wrap(<AgentRiskView />)

    // Reviewed profile: the reviewed state + the reviewer handle are surfaced.
    const critRow = (await screen.findByText('agent:planner')).closest('tr')!
    expect(within(critRow).getByText('Reviewed')).toBeInTheDocument()
    expect(within(critRow).getByText('user:sec-lead')).toBeInTheDocument()

    // Suggested profile: rendered as "Suggested", never as reviewed.
    const sugRow = screen.getByText('agent:reader').closest('tr')!
    expect(within(sugRow).getByText('Suggested')).toBeInTheDocument()
    expect(within(sugRow).queryByText('Reviewed')).toBeNull()
  })
})

describe('AgentRiskView — permission gating', () => {
  it('hides Classify without agent-risk:write', async () => {
    authState.can = (p) => p !== 'governance:agent-risk:write'
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [suggestedOnly],
      has_more: false,
    })
    wrap(<AgentRiskView />)
    await screen.findByText('agent:reader')
    expect(screen.queryByRole('button', { name: /classify agent/i })).toBeNull()
  })

  it('hides Override and Review without agent-risk:admin', async () => {
    authState.can = (p) => p !== 'governance:agent-risk:admin'
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [suggestedOnly],
      has_more: false,
    })
    wrap(<AgentRiskView />)
    await screen.findByText('agent:reader')
    expect(screen.queryByRole('button', { name: /override tier/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /mark reviewed/i })).toBeNull()
  })

  it('renders a calm not-authorized state without agent-risk:read', async () => {
    authState.can = (p) => p !== 'governance:agent-risk:read'
    wrap(<AgentRiskView />)
    expect(await screen.findByText('Not authorized')).toBeInTheDocument()
    expect(api.listAgentRiskProfiles).not.toHaveBeenCalled()
  })
})

describe('AgentRiskView — privileged actions: endpoint + invalidation', () => {
  it('classify: dialog (UUID) → api.classifyAgentRisk({agent_id}) → toast + invalidate', async () => {
    api.classifyAgentRisk.mockResolvedValue(suggestedOnly)
    const { queryClient } = wrap(<AgentRiskView />)
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')

    await userEvent.click(
      await screen.findByRole('button', { name: /classify agent/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText(/^Agent ID/), AGENT_UUID)
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^Classify$/ }),
    )

    await waitFor(() => expect(api.classifyAgentRisk).toHaveBeenCalledTimes(1))
    expect(api.classifyAgentRisk).toHaveBeenCalledWith({ agent_id: AGENT_UUID })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(invalidate).toHaveBeenCalledWith({ queryKey: profilesKey })
  })

  it.each(AGENT_RISK_TIERS)(
    'override: select "%s" → confirm → api.setAgentRiskTier(id, {tier}) → toast + invalidate',
    async (tier) => {
      api.listAgentRiskProfiles.mockResolvedValue({
        items: [suggestedOnly],
        has_more: false,
      })
      api.setAgentRiskTier.mockResolvedValue({
        ...suggestedOnly,
        operator_tier: tier,
        effective_tier: tier,
        state: 'reviewed',
      })
      const { queryClient } = wrap(<AgentRiskView />)
      const invalidate = vi.spyOn(queryClient, 'invalidateQueries')

      await userEvent.click(
        await screen.findByRole('button', { name: /override tier/i }),
      )
      const dialog = await screen.findByRole('dialog')
      await userEvent.click(within(dialog).getByRole('combobox'))
      const label = tier[0].toUpperCase() + tier.slice(1)
      await userEvent.click(await screen.findByRole('option', { name: label }))
      await userEvent.click(
        within(dialog).getByRole('button', { name: /^Set tier$/ }),
      )

      await waitFor(() => expect(api.setAgentRiskTier).toHaveBeenCalledTimes(1))
      expect(api.setAgentRiskTier).toHaveBeenCalledWith('p-sug', { tier })
      await waitFor(() => expect(toast.success).toHaveBeenCalled())
      expect(invalidate).toHaveBeenCalledWith({ queryKey: profilesKey })
    },
  )

  it('override: the clear option sends tier="" to drop the override', async () => {
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [overridden],
      has_more: false,
    })
    api.setAgentRiskTier.mockResolvedValue({
      ...overridden,
      operator_tier: undefined,
      effective_tier: 'low',
    })
    wrap(<AgentRiskView />)

    await userEvent.click(
      await screen.findByRole('button', { name: /override tier/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('combobox'))
    await userEvent.click(
      await screen.findByRole('option', { name: /clear override/i }),
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: /^Set tier$/ }),
    )

    await waitFor(() => expect(api.setAgentRiskTier).toHaveBeenCalledTimes(1))
    expect(api.setAgentRiskTier).toHaveBeenCalledWith('p-crit', { tier: '' })
  })

  it('review: confirm dialog → api.reviewAgentRisk(id) (no body) → toast + invalidate', async () => {
    api.listAgentRiskProfiles.mockResolvedValue({
      items: [suggestedOnly],
      has_more: false,
    })
    api.reviewAgentRisk.mockResolvedValue({
      ...suggestedOnly,
      state: 'reviewed',
    })
    const { queryClient } = wrap(<AgentRiskView />)
    const invalidate = vi.spyOn(queryClient, 'invalidateQueries')

    await userEvent.click(
      await screen.findByRole('button', { name: /mark reviewed/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /mark reviewed/i }),
    )

    await waitFor(() => expect(api.reviewAgentRisk).toHaveBeenCalledTimes(1))
    expect(api.reviewAgentRisk).toHaveBeenCalledWith('p-sug')
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(invalidate).toHaveBeenCalledWith({ queryKey: profilesKey })
  })
})
