// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The console's tool names and session tools are the engine's answer (GET
// /v1/m/sessions/tools); the fixture is that answer, held to it by
// modules/sessions/tools_api_test.go. A row the engine adds reaches the New session form
// with no console change.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: () => '',
}))
vi.mock('@/features/first-hour/api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { StartSessionForm } from '@/features/first-hour/first-hour'
import { providersApi } from '@/features/providers/api'
import { http } from '@/lib/api'
import { useTenantStore } from '@/stores/tenant'
import engineTools from './engine-tools.fixture.json'
import { engineToolsQuery } from './api'
import {
  sessionTools,
  setEngineTools,
  toolName,
  type EngineTool,
} from './engine-tools'

/** A tool row the engine could add to core/driverfacts; the console has never heard of it. */
const NEW_TOOL: EngineTool = {
  key: 'hermes',
  name: 'Hermes Agent',
  name_stem: 'hermes',
  session: true,
  bindings: [{ kind: 'openai', egress: 'configured-endpoint' }],
}

describe("the engine's tool table", () => {
  it('names each tool by the name the engine gives it', () => {
    expect(toolName('claude')).toBe('Claude Code')
    expect(toolName('gemini-cli')).toBe('Gemini CLI')
    expect(toolName('ollama')).toBe('Ollama')
  })

  it('names a driver the engine does not list by its key', () => {
    expect(toolName('hermes')).toBe('hermes')
  })

  it('offers the session tools in the engine order, and no service that runs none', () => {
    expect(sessionTools()).toEqual([
      'claude',
      'codex',
      'grok',
      'opencode',
      'gemini-cli',
      'cursor',
    ])
  })

  it('is read from the engine, and an answer that is not a tool list is refused', async () => {
    const get = vi.spyOn(http, 'get')
    get.mockResolvedValueOnce({ tools: [NEW_TOOL] })
    await new QueryClient().fetchQuery(engineToolsQuery('tnt-a'))
    expect(get).toHaveBeenCalledWith('/v1/m/sessions/tools')
    expect(toolName('hermes')).toBe('Hermes Agent')
    for (const answer of [
      {},
      { tools: [{ key: 'x', name: 'X' }] },
      { tools: [{ ...NEW_TOOL, bindings: [{ kind: 'openai' }] }] },
      { tools: [null] },
    ]) {
      get.mockResolvedValueOnce(answer)
      await expect(
        new QueryClient().fetchQuery(engineToolsQuery('tnt-a')),
      ).rejects.toThrow('The engine did not list its tools.')
    }
    // A refused answer leaves the last good table in place.
    expect(toolName('hermes')).toBe('Hermes Agent')
    get.mockRestore()
  })
})

describe('a tool the engine adds', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    useTenantStore.setState({ activeTenant: 'tnt-a' })
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [],
      has_more: false,
    } as Awaited<ReturnType<typeof agentOpsApi.listRuns>>)
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
      items: [],
      has_more: false,
    } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
    vi.spyOn(providersApi, 'list').mockResolvedValue({
      items: [],
      has_more: false,
    } as Awaited<ReturnType<typeof providersApi.list>>)
  })

  it('is a choice in the New session form, by its own name', async () => {
    setEngineTools([...engineTools.tools, NEW_TOOL])
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ hermes: 'own_login' }),
    )
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <StartSessionForm />
      </QueryClientProvider>,
    )
    expect(
      await screen.findByRole('radio', { name: 'Hermes Agent' }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('radio', { name: 'Claude Code' }),
    ).toBeInTheDocument()
  })
})
