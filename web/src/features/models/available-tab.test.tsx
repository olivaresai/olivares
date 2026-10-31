// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models > Available: the first tab. It answers "what can my tools run now, and which of it
// do my sessions use", from the reads the engine already serves: each active profile's live
// model list, installed Ollama models, configured model availability and the live runs.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { createQueryClient } from '@/lib/api/query'
import {
  act,
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
  within,
} from '@/test/intel'
import '@/features/_intel'
import { OLLAMA_AVAILABLE_KEY } from '@/features/agent-tools/api'
import { agentOpsKeys } from '@/features/agentops/api'
import { PROVIDER_PAGE, providerKeys } from '@/features/providers/api'
import { modelsKeys } from './api'
import './i18n'

const auth = vi.hoisted(() => ({
  denied: new Set<string>(),
  superadmin: true,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => !auth.denied.has(p),
    isSuperadmin: auth.superadmin,
  }),
}))
vi.mock('@/features/agentops/auth-boundary', () => ({
  useAuthBoundary: () => ({ key: 'k', epoch: 1 }),
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useRouter: () => undefined,
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => (
    <a
      href={search ? `${to}?${new URLSearchParams(search).toString()}` : to}
      {...rest}
    >
      {children}
    </a>
  ),
}))
vi.mock('@/components/layout/step-up-state', () => ({
  StepUpRequiredState: ({ onElevated }: { onElevated?: () => void }) => (
    <button type="button" onClick={onElevated}>
      Step-up authentication required
    </button>
  ),
}))

const reads = vi.hoisted(() => ({
  profiles: vi.fn(),
  catalog: vi.fn(),
  runs: vi.fn(),
  ollama: vi.fn(),
  availability: vi.fn(),
  providers: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...actual,
    agentOpsApi: {
      ...actual.agentOpsApi,
      listProfiles: (...a: unknown[]) => reads.profiles(...a),
      profileModels: (...a: unknown[]) => reads.catalog(...a),
      listRuns: (...a: unknown[]) => reads.runs(...a),
    },
  }
})
vi.mock('@/features/agent-tools/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agent-tools/api')>()
  return {
    ...actual,
    agentToolsApi: {
      ...actual.agentToolsApi,
      ollama: (...a: unknown[]) => reads.ollama(...a),
    },
  }
})
vi.mock('@/features/providers/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/providers/api')>()
  return {
    ...actual,
    providersApi: {
      ...actual.providersApi,
      list: (...a: unknown[]) => reads.providers(...a),
    },
  }
})
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      availability: (...a: unknown[]) => reads.availability(...a),
    },
  }
})

const { AvailableTab } = await import('./available-tab')

function run(ref: string, over: Record<string, unknown> = {}) {
  return {
    run_ref: ref,
    name: `Session ${ref}`,
    state: 'running',
    provider_driver: 'opencode',
    model_ref: 'gpt-5',
    ...over,
  }
}

const API_PROVIDER = {
  source_ref: 'provider:1',
  provider_ref: 'provider:1',
  provider_kind: 'openai_compatible',
  state: 'fresh',
  models: [{ id: 'api-stub-model', seen_at: '2026-10-25T10:00:00Z' }],
}

function serve({
  runs = [] as unknown[],
  ollama = { installed: false, state: 'stopped', models: [] } as unknown,
  availability = [] as unknown[],
  providers = [] as unknown[],
  providerPages = null as unknown[][] | null,
  profiles = [
    { profile_ref: 'p-oc', driver: 'opencode', state: 'active' },
  ] as unknown[],
  catalogs = {} as Record<string, unknown>,
  runsHasMore = false,
  profilesHasMore = false,
} = {}) {
  reads.profiles.mockImplementation(() =>
    Promise.resolve({ items: profiles, has_more: profilesHasMore }),
  )
  reads.catalog.mockImplementation((ref: string) => {
    const own = catalogs[ref]
    if (own instanceof Error) return Promise.reject(own)
    return Promise.resolve(
      own ?? {
        profile_ref: ref,
        driver: 'opencode',
        state: 'fresh',
        models: [{ id: 'gpt-5' }, { id: 'o3', display_name: 'O3' }],
        default_model: '',
        free_text: false,
        checked_at: '2026-10-23T10:00:00Z',
        refresh_interval_seconds: 120,
      },
    )
  })
  reads.runs.mockImplementation((p: { state: string }) =>
    Promise.resolve({
      items: (runs as { state: string }[]).filter((r) => r.state === p.state),
      has_more: runsHasMore && p.state === 'idle',
    }),
  )
  reads.ollama.mockImplementation(() => Promise.resolve(ollama))
  reads.availability.mockImplementation(() =>
    Promise.resolve({ items: availability, refresh_interval_seconds: 3600 }),
  )
  reads.providers.mockImplementation((params: { cursor?: string }) => {
    const pages = providerPages ?? [providers]
    const page = params.cursor ? Number(params.cursor) : 0
    return Promise.resolve({
      items: pages[page] ?? [],
      cursor: String(page + 1),
      has_more: page + 1 < pages.length,
    })
  })
}

beforeEach(() => {
  auth.denied.clear()
  auth.superadmin = true
  Object.values(reads).forEach((m) => m.mockReset())
})

describe('Models > Available', () => {
  it('lists the models each tool, Ollama and provider can run, with source and state', async () => {
    serve({
      ollama: { installed: true, state: 'running', models: ['qwen3:8b'] },
      availability: [API_PROVIDER],
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'openai_compatible',
          display_name: 'Local API stub',
          state: 'active',
        },
      ],
    })
    renderIntel(<AvailableTab />)

    const tool = await screen.findByRole('row', { name: /gpt-5/ })
    expect(within(tool).getAllByText('OpenCode')).toHaveLength(2)
    expect(within(tool).getByText(/live list/i)).toBeInTheDocument()
    expect(within(tool).getByText('Ready')).toBeInTheDocument()

    expect(screen.getByRole('row', { name: /O3/ })).toBeInTheDocument()

    const provider = screen.getByRole('row', { name: /api-stub-model/ })
    expect(within(provider).getAllByText('Local API stub')).toHaveLength(2)
    expect(within(provider).getByText('API provider')).toBeInTheDocument()
    expect(within(provider).getByText('Ready')).toBeInTheDocument()

    const ollama = screen.getByRole('row', { name: /qwen3:8b/ })
    expect(within(ollama).getAllByText('Ollama')).toHaveLength(2)
    expect(within(ollama).getByText(/installed/i)).toBeInTheDocument()
    expect(within(ollama).getByText('Ready')).toBeInTheDocument()
  })

  it('reads every provider metadata page before naming or deduping', async () => {
    const first = Array.from({ length: 100 }, (_, i) => ({
      provider_ref: `provider:${i + 2}`,
      kind: 'openai_compatible',
      display_name: `Provider ${i + 2}`,
      state: 'active',
    }))
    serve({
      availability: [API_PROVIDER],
      providerPages: [
        first,
        [
          {
            provider_ref: 'provider:1',
            kind: 'openai_compatible',
            display_name: 'Local API stub',
            state: 'active',
          },
        ],
      ],
    })
    renderIntel(<AvailableTab />)

    const provider = await screen.findByRole('row', { name: /api-stub-model/ })
    expect(within(provider).getAllByText('Local API stub')).toHaveLength(2)
    expect(reads.providers).toHaveBeenCalledTimes(2)
  })

  it('keeps drained metadata out of the provider pickers infinite-query cache entry', async () => {
    const key = providerKeys.list('t1', 1, {
      state: 'active',
      limit: PROVIDER_PAGE,
    })
    const picker = {
      pages: [{ items: [], cursor: '', has_more: false }],
      pageParams: [undefined],
    }
    const queryClient = createQueryClient()
    queryClient.setDefaultOptions({ queries: { retry: false } })
    queryClient.setQueryData(key, picker)
    serve({ availability: [API_PROVIDER] })
    renderIntel(<AvailableTab />, { queryClient })

    await screen.findByRole('row', { name: /api-stub-model/ })
    expect(queryClient.getQueryData(key)).toBe(picker)
    const drained = queryClient.getQueryCache().find({
      queryKey: [...key, 'models-available'],
    })
    expect(drained?.state.data).toMatchObject({ items: [] })
  })

  it('keeps a failed provider source and its last good models visible', async () => {
    serve({
      availability: [{ ...API_PROVIDER, state: 'failed' }],
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'openai_compatible',
          display_name: 'Local API stub',
          state: 'active',
        },
      ],
    })
    renderIntel(<AvailableTab />)

    expect(await screen.findByRole('alert')).toHaveTextContent('Local API stub')
    expect(screen.queryByRole('alert')).not.toHaveTextContent('provider:1')
    const provider = screen.getByRole('row', { name: /api-stub-model/ })
    expect(within(provider).getByText('Failed')).toBeInTheDocument()
    expect(screen.getByRole('row', { name: /gpt-5/ })).toBeInTheDocument()
    expect(screen.queryByText('No model available yet')).toBeNull()
  })

  it('keeps a new provider visible while its first model read is pending', async () => {
    serve({
      profiles: [],
      availability: [
        {
          ...API_PROVIDER,
          source_ref: 'prv_new',
          provider_ref: 'prv_new',
          state: 'stale',
          models: [],
          message: 'Models have not been refreshed yet.',
        },
      ],
      providers: [
        {
          provider_ref: 'prv_new',
          kind: 'openai_compatible',
          display_name: 'New provider',
          state: 'active',
        },
      ],
    })
    renderIntel(<AvailableTab />)

    const row = await screen.findByRole('row', { name: /New provider/ })
    expect(within(row).getByText('Model list')).toBeInTheDocument()
    expect(within(row).getByText('Checking models')).toBeInTheDocument()
    expect(within(row).queryByText('Its own default model')).toBeNull()
    expect(screen.queryByText('No model available yet')).toBeNull()
    expect(screen.queryByRole('link', { name: /add a provider/i })).toBeNull()
  })

  it('keeps a failed provider with no last good models visible and names its failure', async () => {
    serve({
      profiles: [],
      availability: [{ ...API_PROVIDER, state: 'failed', models: [] }],
      providers: [
        {
          provider_ref: 'provider:1',
          kind: 'openai_compatible',
          display_name: 'Local API stub',
          state: 'active',
        },
      ],
    })
    renderIntel(<AvailableTab />)

    const row = await screen.findByRole('row', { name: /Local API stub/ })
    expect(within(row).getByText('Model list')).toBeInTheDocument()
    expect(within(row).getByText('Failed')).toBeInTheDocument()
    expect(within(row).queryByText('Its own default model')).toBeNull()
    expect(await screen.findByRole('alert')).toHaveTextContent('Local API stub')
    expect(screen.queryByText('No model available yet')).toBeNull()
  })

  it('does not call a provider with an empty successful catalog ready', async () => {
    serve({
      profiles: [],
      availability: [{ ...API_PROVIDER, state: 'fresh', models: [] }],
    })
    renderIntel(<AvailableTab />)

    const row = await screen.findByRole('row', { name: /provider:1/ })
    expect(within(row).getByText('Lists no models')).toBeInTheDocument()
    expect(within(row).queryByText('Does not list its models')).toBeNull()
    expect(within(row).queryByText('Ready')).toBeNull()
    expect(screen.queryByText('No model available yet')).toBeNull()
  })

  describe('a provider record at the local Ollama endpoint with no models', () => {
    const LOCAL_OLLAMA_RECORD = {
      provider_ref: 'provider:1',
      kind: 'ollama',
      display_name: 'Local Ollama record',
      base_url: 'http://127.0.0.1:11434',
      state: 'active',
    }
    const source = (state: string) => ({
      ...API_PROVIDER,
      provider_kind: 'ollama',
      state,
      models: [],
    })

    it.each(['fresh', 'stale'])(
      'leaves the empty state with "Install a model" when Ollama runs with none installed (%s)',
      async (state) => {
        serve({
          profiles: [],
          ollama: {
            installed: true,
            state: 'running',
            endpoint: 'http://127.0.0.1:11434',
            models: [],
          },
          providers: [LOCAL_OLLAMA_RECORD],
          availability: [source(state)],
        })
        renderIntel(<AvailableTab />)

        expect(
          await screen.findByText('No model available yet'),
        ).toBeInTheDocument()
        expect(
          screen.getByRole('link', { name: /install a model/i }),
        ).toBeInTheDocument()
        expect(
          screen.queryByRole('row', { name: /Local Ollama record/ }),
        ).toBeNull()
        expect(screen.queryByText('Checking models')).toBeNull()
        expect(screen.queryByText('Lists no models')).toBeNull()
      },
    )

    it('shows only the stopped Ollama row when the local provider failed', async () => {
      serve({
        profiles: [],
        ollama: {
          installed: true,
          state: 'stopped',
          endpoint: 'http://127.0.0.1:11434',
          models: [],
        },
        providers: [LOCAL_OLLAMA_RECORD],
        availability: [source('failed')],
      })
      renderIntel(<AvailableTab />)

      const row = await screen.findByRole('row', { name: /installed models/i })
      expect(within(row).getByText('Ollama stopped')).toBeInTheDocument()
      expect(
        screen.queryByRole('row', { name: /Local Ollama record/ }),
      ).toBeNull()
      expect(screen.getAllByRole('row')).toHaveLength(2)
    })
  })

  it('shows an installed Ollama that is not running as a row that points to AI tools, not as an empty tab', async () => {
    // The engine sends no models while Ollama is not running; the empty state would say
    // "install a model" about models that are on disk.
    serve({
      profiles: [],
      ollama: { installed: true, state: 'stopped', models: [] },
    })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /installed models/i })
    expect(within(row).getByText('Ollama stopped')).toBeInTheDocument()
    expect(
      within(row).getByRole('link', { name: /open ai tools/i }),
    ).toHaveAttribute('href', '/agent-tools')
    expect(screen.queryByText(/no model available yet/i)).toBeNull()
  })

  it('counts the live sessions on a model and opens each of them from its row', async () => {
    serve({
      runs: [
        run('r-a'),
        run('r-b', { state: 'idle' }),
        run('r-c', { model_ref: 'o3' }),
      ],
    })
    const user = userEvent.setup()
    renderIntel(<AvailableTab />)

    const row = await screen.findByRole('row', { name: /gpt-5/ })
    await user.click(within(row).getByRole('button', { name: /2 sessions/i }))
    const first = await screen.findByRole('link', { name: 'Session r-a' })
    expect(first).toHaveAttribute('href', '/sessions?session=run%3Ar-a')
    expect(screen.getByRole('link', { name: 'Session r-b' })).toHaveAttribute(
      'href',
      '/sessions?session=run%3Ar-b',
    )
    expect(screen.queryByRole('link', { name: 'Session r-c' })).toBeNull()
  })

  it('counts a run once when two live-state reads return it during a transition', async () => {
    serve({
      runs: [
        run('r-a', { state: 'pending' }),
        run('r-a', { state: 'running' }),
      ],
    })
    const user = userEvent.setup()
    renderIntel(<AvailableTab />)

    const row = await screen.findByRole('row', { name: /gpt-5/ })
    await user.click(within(row).getByRole('button', { name: /1 session/i }))
    expect(screen.getAllByRole('link', { name: 'Session r-a' })).toHaveLength(1)
  })

  it('offers no sessions control on a model no live session runs', async () => {
    serve({ runs: [run('r-a')] })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /O3/ })
    expect(within(row).queryByRole('button')).toBeNull()
    expect(within(row).getByText('0')).toBeInTheDocument()
  })

  it('does not count a session that has stopped', async () => {
    serve({ runs: [run('r-a', { state: 'stopped' })] })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    expect(within(row).queryByRole('button')).toBeNull()
  })

  it('tells what to do when nothing is available: install a model, add a provider', async () => {
    serve({ profiles: [] })
    renderIntel(<AvailableTab />)
    expect(
      await screen.findByRole('link', { name: /install a model/i }),
    ).toHaveAttribute('href', '/agent-tools')
    expect(
      screen.getByRole('link', { name: /add a provider/i }),
    ).toHaveAttribute('href', '/providers')
  })

  it('keeps the actionable empty state when only the session count read fails', async () => {
    serve({ profiles: [] })
    reads.runs.mockImplementation(() => Promise.reject(new Error('503')))
    renderIntel(<AvailableTab />)

    expect(await screen.findByRole('alert')).toHaveTextContent(
      /session counts are missing/i,
    )
    expect(
      await screen.findByRole('link', { name: /install a model/i }),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('link', { name: /add a provider/i }),
    ).toBeInTheDocument()
  })

  it('offers no way to add a provider to someone who may not', async () => {
    auth.denied.add('sessions:provider:write')
    serve({ profiles: [] })
    renderIntel(<AvailableTab />)
    await screen.findByRole('link', { name: /install a model/i })
    expect(screen.queryByRole('link', { name: /add a provider/i })).toBeNull()
  })

  it('says a source that failed and still shows the others', async () => {
    serve({ availability: [API_PROVIDER] })
    reads.ollama.mockImplementation(() => Promise.reject(new Error('down')))
    renderIntel(<AvailableTab />)
    expect(await screen.findByRole('alert')).toHaveTextContent(/ollama/i)
    expect(screen.getByRole('row', { name: /gpt-5/ })).toBeInTheDocument()
  })

  it('does not call a source failed when the engine says it is not theirs to read', async () => {
    // The Ollama routes admit a system administrator only (agenttoolsapi guard): anyone else
    // gets 403, which is an answer about them and not a failure of Ollama.
    serve({ availability: [API_PROVIDER] })
    reads.ollama.mockImplementation(() =>
      Promise.reject(
        new ApiError(
          403,
          'forbidden',
          'A system administrator session is required.',
        ),
      ),
    )
    renderIntel(<AvailableTab />)
    await screen.findByRole('row', { name: /gpt-5/ })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('offers no install link, and never asks Ollama, to anyone but a system administrator', async () => {
    auth.superadmin = false
    serve({ profiles: [] })
    renderIntel(<AvailableTab />)
    expect(
      await screen.findByRole('link', { name: /add a provider/i }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /install a model/i })).toBeNull()
    expect(reads.ollama).not.toHaveBeenCalled()
  })

  it("keeps each tool's models under its own name", async () => {
    serve({
      profiles: [
        { profile_ref: 'p-cl', driver: 'claude', state: 'active' },
        { profile_ref: 'p-oc', driver: 'opencode', state: 'active' },
      ],
      catalogs: {
        'p-cl': {
          profile_ref: 'p-cl',
          driver: 'claude',
          state: 'fresh',
          models: [{ id: 'opus' }],
          default_model: '',
          free_text: false,
          checked_at: '2026-10-23T10:00:00Z',
          refresh_interval_seconds: 120,
        },
      },
    })
    renderIntel(<AvailableTab />)
    const opus = await screen.findByRole('row', { name: /opus/ })
    expect(within(opus).getAllByText('Claude Code')).toHaveLength(2)
    const gpt = screen.getByRole('row', { name: /gpt-5/ })
    expect(within(gpt).getAllByText('OpenCode')).toHaveLength(2)
  })

  it('shows a tool whose model list could not be read as failed, and the others', async () => {
    serve({
      profiles: [
        { profile_ref: 'p-cl', driver: 'claude', state: 'active' },
        { profile_ref: 'p-oc', driver: 'opencode', state: 'active' },
      ],
      catalogs: { 'p-cl': new Error('boom') },
    })
    renderIntel(<AvailableTab />)
    const claude = await screen.findByRole('row', { name: /Claude Code/ })
    expect(within(claude).getByText('List unavailable')).toBeInTheDocument()
    expect(screen.getByRole('row', { name: /gpt-5/ })).toBeInTheDocument()
    expect(await screen.findByRole('alert')).toHaveTextContent(/model lists/i)
  })

  it('says a tool that does not list its models, in words', async () => {
    serve({
      catalogs: {
        'p-oc': {
          profile_ref: 'p-oc',
          driver: 'opencode',
          state: 'unsupported',
          models: [],
          default_model: '',
          free_text: true,
          checked_at: '2026-10-23T10:00:00Z',
          refresh_interval_seconds: 120,
        },
      },
    })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /OpenCode/ })
    expect(within(row).getByText('Its own default model')).toBeInTheDocument()
    expect(
      within(row).getByText('Does not list its models'),
    ).toBeInTheDocument()
  })

  it('does not read a profile that belongs to another machine', async () => {
    serve({
      profiles: [
        {
          profile_ref: 'p-far',
          driver: 'claude',
          state: 'active',
          local_environment: false,
        },
        { profile_ref: 'p-oc', driver: 'opencode', state: 'active' },
      ],
    })
    renderIntel(<AvailableTab />)
    await screen.findByRole('row', { name: /gpt-5/ })
    expect(reads.catalog).toHaveBeenCalledTimes(1)
    expect(reads.catalog.mock.calls[0][0]).toBe('p-oc')
  })

  it('closes the sessions of a row again, and names the list it controls', async () => {
    serve({ runs: [run('r-a')] })
    const user = userEvent.setup()
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    const button = within(row).getByRole('button', { name: /1 session/i })
    expect(button).toHaveAttribute('aria-expanded', 'false')
    await user.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    const sessions = screen.getByRole('list', {
      name: /sessions running gpt-5/i,
    })
    expect(button.getAttribute('aria-controls')).toBe(sessions.id)
    await user.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()
  })

  it('keeps a provider-bound row and its open sessions when the live list arrives', async () => {
    let releaseCatalog!: (value: unknown) => void
    const pendingCatalog = new Promise((resolve) => {
      releaseCatalog = resolve
    })
    serve({
      runs: [run('r-a', { provider_profile_ref: 'p-bound' })],
      profiles: [
        {
          profile_ref: 'p-bound',
          provider_record_ref: 'provider-1',
          driver: 'opencode',
          state: 'active',
        },
      ],
    })
    reads.catalog.mockImplementation(() => pendingCatalog)
    const user = userEvent.setup()
    renderIntel(<AvailableTab />)

    const row = await screen.findByRole('row', { name: /gpt-5/ })
    const button = within(row).getByRole('button', { name: /1 session/i })
    await user.click(button)
    expect(
      await screen.findByRole('link', { name: 'Session r-a' }),
    ).toBeVisible()

    releaseCatalog({
      profile_ref: 'p-bound',
      driver: 'opencode',
      state: 'fresh',
      models: [{ id: 'gpt-5' }],
      default_model: '',
      free_text: false,
      checked_at: '2026-10-23T10:00:00Z',
      refresh_interval_seconds: 120,
    })
    expect(await screen.findByText('Model list of the provider')).toBeVisible()
    expect(button).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('link', { name: 'Session r-a' })).toBeVisible()
  })

  it('does not say 0 when the sessions cannot be counted', async () => {
    auth.denied.add('sessions:run:read')
    serve({ runs: [run('r-a')] })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    expect(within(row).queryByText('0')).toBeNull()
    expect(
      within(row).getByTitle('Session counts are not available.'),
    ).toBeInTheDocument()
    expect(
      within(row).getByText('Session counts are not available.'),
    ).toBeInTheDocument()
  })

  it('says the counts are missing when the sessions read fails', async () => {
    serve({})
    reads.runs.mockImplementation(() => Promise.reject(new Error('503')))
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    expect(await screen.findByRole('alert')).toHaveTextContent(
      /session counts are missing/i,
    )
    expect(within(row).queryByText('0')).toBeNull()
  })

  it('shows a live unlisted model and counts only sessions whose model is unknown', async () => {
    serve({
      runs: [
        run('r-a'),
        run('r-x', { model_ref: 'gpt-9-unlisted' }),
        run('r-y', { model_ref: '', provider_driver: 'unlisted-tool' }),
      ],
    })
    renderIntel(<AvailableTab />)
    const unlisted = await screen.findByRole('row', {
      name: /gpt-9-unlisted/,
    })
    expect(
      within(unlisted).getByRole('button', { name: /1 session/i }),
    ).toBeInTheDocument()
    expect(
      await screen.findByText(/1 live session runs a model not listed here/i),
    ).toBeInTheDocument()
  })

  it('says when there are more sessions than were counted, as every list of this page does', async () => {
    serve({ runs: [run('r-a')], runsHasMore: true })
    renderIntel(<AvailableTab />)
    const badge = await screen.findByText(
      /cover the 200 most recent sessions of each state/i,
    )
    expect(badge.tagName).not.toBe('P')
  })

  it('does not call the first remote-only profile page an empty estate', async () => {
    serve({
      profiles: Array.from({ length: 200 }, (_, i) => ({
        profile_ref: `p-remote-${i}`,
        driver: 'claude',
        state: 'active',
        local_environment: false,
      })),
      profilesHasMore: true,
    })
    renderIntel(<AvailableTab />)

    expect(
      await screen.findByText(
        /first 200 tool profiles are shown; there are more/i,
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText(/no model available yet/i)).toBeNull()
    expect(reads.catalog).not.toHaveBeenCalled()
  })

  it('does not say there are more sessions beside the empty state when none were loaded', async () => {
    // {items: [], has_more: true} would paint "there are more" over "nothing available".
    serve({ profiles: [], runsHasMore: true })
    renderIntel(<AvailableTab />)
    expect(
      await screen.findByText(/no model available yet/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/cover the 200 most recent sessions of each state/i),
    ).toBeNull()
  })

  it('says Ollama failed, in its row', async () => {
    serve({
      profiles: [],
      ollama: { installed: true, state: 'failed', models: [] },
    })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /installed models/i })
    expect(within(row).getByText('Ollama failed')).toBeInTheDocument()
  })

  it('counts a session launched on the tool default on the default row of the tool', async () => {
    serve({
      profiles: [{ profile_ref: 'p-cl', driver: 'claude', state: 'active' }],
      catalogs: {
        'p-cl': {
          profile_ref: 'p-cl',
          driver: 'claude',
          state: 'fresh',
          models: [{ id: 'default', is_default: true }, { id: 'opus' }],
          default_model: '',
          free_text: false,
          checked_at: '2026-10-23T10:00:00Z',
          refresh_interval_seconds: 120,
        },
      },
      runs: [
        run('r-a', {
          provider_driver: 'claude',
          model_ref: undefined,
          usage_model_ref: 'claude-opus-4-8',
        }),
      ],
    })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /default/ })
    expect(
      within(row).getByRole('button', { name: /1 session/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText(/not listed here/i)).toBeNull()
  })

  it("names the provider's list as the source of a profile bound to a provider record", async () => {
    serve({
      profiles: [
        {
          profile_ref: 'p-oc',
          driver: 'opencode',
          state: 'active',
          provider_record_ref: 'prov-1',
        },
      ],
    })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    expect(
      within(row).getByText('Model list of the provider'),
    ).toBeInTheDocument()
    expect(within(row).queryByText(/live list of the tool/i)).toBeNull()
  })

  it.each(['ollama', 'availability', 'catalog'] as const)(
    'offers step-up instead of a generic failure or empty state, and stops polling %s',
    async (source) => {
      vi.useFakeTimers()
      try {
        serve({ availability: [API_PROVIDER] })
        reads[source].mockImplementation(() =>
          Promise.reject(
            new ApiError(403, 'step_up_required', 'Step-up required.'),
          ),
        )
        renderIntel(<AvailableTab />)

        await vi.waitFor(() => {
          expect(
            screen.getByText('Step-up authentication required'),
          ).toBeInTheDocument()
        })
        expect(screen.queryByRole('alert')).toBeNull()
        expect(reads[source]).toHaveBeenCalledTimes(1)
        await vi.advanceTimersByTimeAsync(
          source === 'catalog' ? 120_000 : 15_000,
        )
        expect(reads[source]).toHaveBeenCalledTimes(1)
      } finally {
        vi.useRealTimers()
      }
    },
  )

  it('does not call availability empty while it requires step-up', async () => {
    serve({ availability: [API_PROVIDER], profiles: [] })
    reads.availability.mockImplementation(() =>
      Promise.reject(
        new ApiError(403, 'step_up_required', 'Step-up required.'),
      ),
    )
    renderIntel(<AvailableTab />)

    expect(
      await screen.findByText('Step-up authentication required'),
    ).toBeInTheDocument()
    expect(screen.queryByText('No model available yet')).toBeNull()
    expect(screen.queryByRole('link', { name: /add a provider/i })).toBeNull()
  })

  it('stops polling a catalog after an ordinary refusal', async () => {
    vi.useFakeTimers()
    try {
      serve()
      reads.catalog.mockImplementation(() =>
        Promise.reject(new ApiError(403, 'forbidden', 'No.')),
      )
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(screen.getByRole('alert')).toBeInTheDocument()
      })
      expect(
        screen.queryByRole('button', {
          name: 'Step-up authentication required',
        }),
      ).toBeNull()
      expect(reads.catalog).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(120_000)
      expect(reads.catalog).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('polls a stale provider with no models at the session interval', async () => {
    vi.useFakeTimers()
    try {
      serve({
        profiles: [],
        availability: [{ ...API_PROVIDER, state: 'stale', models: [] }],
      })
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(screen.getByText('Checking models')).toBeInTheDocument()
      })
      expect(reads.availability).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(15_000)
      expect(reads.availability).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('stops the fast poll of a stale provider with no models after three minutes', async () => {
    // An open tab must not ask (and the engine audit) every 15 seconds for ever.
    vi.useFakeTimers()
    try {
      serve({
        profiles: [],
        availability: [{ ...API_PROVIDER, state: 'stale', models: [] }],
      })
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(screen.getByText('Checking models')).toBeInTheDocument()
      })
      await vi.advanceTimersByTimeAsync(195_000)
      const asked = reads.availability.mock.calls.length
      expect(asked).toBeGreaterThan(2)
      await vi.advanceTimersByTimeAsync(600_000)
      expect(reads.availability).toHaveBeenCalledTimes(asked)
    } finally {
      vi.useRealTimers()
    }
  })

  it('replaces checking models with the provider models when the next poll answers', async () => {
    vi.useFakeTimers()
    try {
      serve({ profiles: [] })
      reads.availability
        .mockImplementationOnce(() =>
          Promise.resolve({
            items: [{ ...API_PROVIDER, state: 'stale', models: [] }],
            refresh_interval_seconds: 3600,
          }),
        )
        .mockImplementation(() =>
          Promise.resolve({
            items: [API_PROVIDER],
            refresh_interval_seconds: 3600,
          }),
        )
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(screen.getByText('Checking models')).toBeInTheDocument()
      })
      await vi.advanceTimersByTimeAsync(15_000)
      await vi.waitFor(() => {
        const row = screen.getByRole('row', { name: /api-stub-model/ })
        expect(within(row).getByText('Ready')).toBeInTheDocument()
      })
      expect(screen.queryByText('Checking models')).toBeNull()
      expect(reads.availability).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it.each([
    ['a populated stale source', { ...API_PROVIDER, state: 'stale' }],
    [
      'an empty failed source',
      { ...API_PROVIDER, state: 'failed', models: [] },
    ],
  ])('keeps the advertised refresh interval for %s', async (_name, source) => {
    vi.useFakeTimers()
    try {
      serve({ profiles: [], availability: [source] })
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(
          screen.getByRole('row', { name: /provider:1/ }),
        ).toBeInTheDocument()
      })
      expect(reads.availability).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(15_000)
      expect(reads.availability).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(3_585_000)
      expect(reads.availability).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('follows the refresh interval the availability endpoint advertises', async () => {
    vi.useFakeTimers()
    try {
      serve({ availability: [API_PROVIDER] })
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(
          screen.getByRole('row', { name: /api-stub-model/ }),
        ).toBeInTheDocument()
      })
      expect(reads.availability).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(15_000)
      expect(reads.availability).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(3_585_000)
      expect(reads.availability).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('recovers a sole catalog after step-up without showing an empty state', async () => {
    serve()
    reads.catalog.mockImplementation(() =>
      Promise.reject(
        new ApiError(403, 'step_up_required', 'Step-up required.'),
      ),
    )
    const user = userEvent.setup()
    renderIntel(<AvailableTab />)

    const stepUp = await screen.findByRole('button', {
      name: 'Step-up authentication required',
    })
    expect(screen.queryByText('No model available yet')).toBeNull()
    expect(screen.queryByRole('link', { name: /add a provider/i })).toBeNull()
    reads.catalog.mockImplementation((ref: string) =>
      Promise.resolve({
        profile_ref: ref,
        driver: 'opencode',
        state: 'fresh',
        models: [{ id: 'gpt-5' }],
        default_model: '',
        free_text: false,
        checked_at: '2026-10-25T10:00:00Z',
        refresh_interval_seconds: 120,
      }),
    )
    await user.click(stepUp)
    expect(
      await screen.findByRole('row', { name: /gpt-5/ }),
    ).toBeInTheDocument()
  })

  it('removes a source step-up when its permission is lost', async () => {
    serve()
    reads.availability.mockImplementation(() =>
      Promise.reject(
        new ApiError(403, 'step_up_required', 'Step-up required.'),
      ),
    )
    const view = renderIntel(<AvailableTab />)
    await screen.findByRole('button', {
      name: 'Step-up authentication required',
    })

    auth.denied.add('models:catalog:read')
    view.rerender(<AvailableTab />)
    expect(
      screen.queryByRole('button', {
        name: 'Step-up authentication required',
      }),
    ).toBeNull()
  })

  it('removes a source failure when its permission is lost', async () => {
    serve()
    reads.availability.mockImplementation(() =>
      Promise.reject(new Error('500')),
    )
    const view = renderIntel(<AvailableTab />)
    await screen.findByRole('alert')

    auth.denied.add('models:catalog:read')
    view.rerender(<AvailableTab />)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('does not retry a failed availability read', async () => {
    vi.useFakeTimers()
    try {
      serve()
      reads.availability.mockImplementation(() =>
        Promise.reject(new Error('500')),
      )
      const queryClient = createQueryClient()
      queryClient.setDefaultOptions({ queries: { retryDelay: 0 } })
      renderIntel(<AvailableTab />, { queryClient })
      await vi.waitFor(() => {
        expect(screen.getByRole('alert')).toBeInTheDocument()
      })
      expect(reads.availability).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps its Ollama read under the auth boundary and the key Start and Stop refresh', async () => {
    serve({ availability: [API_PROVIDER] })
    const queryClient = createTestQueryClient()
    renderIntel(<AvailableTab />, { queryClient })
    await screen.findByRole('table')
    const keys = queryClient
      .getQueryCache()
      .findAll({ predicate: (q) => q.queryKey.includes(OLLAMA_AVAILABLE_KEY) })
      .map((q) => q.queryKey)
    expect(keys).toEqual([
      [...agentOpsKeys.boundaryScope('t1', 1), OLLAMA_AVAILABLE_KEY],
    ])
  })

  it('drops cached sources on permission loss and repaints them only after fresh reads', async () => {
    serve({
      runs: [run('r-a')],
      ollama: { installed: true, state: 'running', models: ['qwen3:8b'] },
      availability: [API_PROVIDER],
    })
    const queryClient = createTestQueryClient()
    const view = renderIntel(<AvailableTab />, { queryClient })
    const user = userEvent.setup()
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    await user.click(within(row).getByRole('button', { name: /1 session/i }))
    expect(
      screen.getByRole('link', { name: 'Session r-a' }),
    ).toBeInTheDocument()

    auth.denied.add('sessions:profile:read')
    auth.denied.add('sessions:run:read')
    auth.superadmin = false
    view.rerender(<AvailableTab />)
    expect(screen.queryByRole('row', { name: /gpt-5/ })).toBeNull()
    expect(screen.queryByRole('row', { name: /qwen3:8b/ })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()
    const provider = screen.getByRole('row', { name: /api-stub-model/ })
    expect(within(provider).getByText('—')).toBeInTheDocument()

    let release!: () => void
    const fresh = new Promise<void>((resolve) => {
      release = resolve
    })
    reads.profiles.mockImplementation(() =>
      fresh.then(() => ({
        items: [{ profile_ref: 'p-oc', driver: 'opencode', state: 'active' }],
        has_more: false,
      })),
    )
    reads.catalog.mockImplementation((ref: string) =>
      fresh.then(() => ({
        profile_ref: ref,
        driver: 'opencode',
        state: 'fresh',
        models: [{ id: 'gpt-6' }],
        default_model: '',
        free_text: false,
        checked_at: '2026-10-23T10:00:00Z',
        refresh_interval_seconds: 120,
      })),
    )
    reads.runs.mockImplementation((p: { state: string }) =>
      fresh.then(() => ({
        items:
          p.state === 'running' ? [run('r-b', { model_ref: 'gpt-6' })] : [],
        has_more: false,
      })),
    )
    reads.ollama.mockImplementation(() =>
      fresh.then(() => ({
        installed: true,
        state: 'running',
        models: ['qwen3:8b'],
      })),
    )
    auth.denied.clear()
    auth.superadmin = true
    view.rerender(<AvailableTab />)
    expect(screen.queryByRole('row', { name: /gpt-5/ })).toBeNull()
    expect(screen.queryByRole('row', { name: /qwen3:8b/ })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()

    release()
    const restored = await screen.findByRole('row', { name: /gpt-6/ })
    expect(screen.queryByRole('row', { name: /gpt-5/ })).toBeNull()
    expect(reads.catalog.mock.calls.length).toBeGreaterThan(1)
    await user.click(
      within(restored).getByRole('button', { name: /1 session/i }),
    )
    expect(
      screen.getByRole('link', { name: 'Session r-b' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()
    expect(screen.getByRole('row', { name: /qwen3:8b/ })).toBeInTheDocument()
  })

  it('does not repaint pre-refusal sessions after a later outage, until a fresh success', async () => {
    serve({ runs: [run('r-a')] })
    const queryClient = createTestQueryClient()
    renderIntel(<AvailableTab />, { queryClient })
    const user = userEvent.setup()
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    await user.click(within(row).getByRole('button', { name: /1 session/i }))
    expect(
      screen.getByRole('link', { name: 'Session r-a' }),
    ).toBeInTheDocument()
    const refetch = async () => {
      await act(() =>
        queryClient.refetchQueries({
          predicate: (query) => query.queryKey.includes('models-available'),
        }),
      )
    }

    reads.runs.mockImplementation(() =>
      Promise.reject(new ApiError(403, 'forbidden', 'No.')),
    )
    await refetch()
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()

    reads.runs.mockImplementation(() => Promise.reject(new Error('500')))
    await refetch()
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()

    reads.runs.mockImplementation((p: { state: string }) =>
      Promise.resolve({
        items: p.state === 'running' ? [run('r-b')] : [],
        has_more: false,
      }),
    )
    await refetch()
    expect(
      await screen.findByRole('link', { name: 'Session r-b' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Session r-a' })).toBeNull()
  })

  it('refreshes Ollama and tool model lists while the tab stays open', async () => {
    vi.useFakeTimers()
    try {
      serve({
        ollama: { installed: true, state: 'running', models: ['qwen3:8b'] },
      })
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(screen.getByRole('row', { name: /gpt-5/ })).toBeInTheDocument()
        expect(
          screen.getByRole('row', { name: /qwen3:8b/ }),
        ).toBeInTheDocument()
      })
      reads.ollama.mockImplementation(() =>
        Promise.resolve({
          installed: true,
          state: 'running',
          models: ['qwen4:8b'],
        }),
      )
      reads.catalog.mockImplementation((ref: string) =>
        Promise.resolve({
          profile_ref: ref,
          driver: 'opencode',
          state: 'fresh',
          models: [{ id: 'gpt-6' }],
          default_model: '',
          free_text: false,
          checked_at: '2026-10-23T10:00:00Z',
          refresh_interval_seconds: 120,
        }),
      )

      await vi.advanceTimersByTimeAsync(15_000)
      await vi.waitFor(() => {
        expect(
          screen.getByRole('row', { name: /qwen4:8b/ }),
        ).toBeInTheDocument()
      })
      await vi.advanceTimersByTimeAsync(120_000)
      await vi.waitFor(() => {
        expect(screen.getByRole('row', { name: /gpt-6/ })).toBeInTheDocument()
      })
      expect(reads.ollama.mock.calls.length).toBeGreaterThan(1)
      expect(reads.catalog.mock.calls.length).toBeGreaterThan(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('shows a provider model with its availability state', async () => {
    serve({
      availability: [{ ...API_PROVIDER, state: 'stale' }],
    })
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /api-stub-model/ })
    expect(within(row).getByText(/stale/i)).toBeInTheDocument()
  })

  it('reads nothing a person may not read', async () => {
    auth.denied.add('sessions:profile:read')
    auth.denied.add('sessions:run:read')
    serve({ availability: [API_PROVIDER] })
    renderIntel(<AvailableTab />)
    await screen.findByRole('row', { name: /api-stub-model/ })
    expect(reads.profiles).not.toHaveBeenCalled()
    expect(reads.runs).not.toHaveBeenCalled()
  })

  it('does not read availability of someone who may not read the catalog, and says nothing failed', async () => {
    auth.denied.add('models:catalog:read')
    serve({ runs: [run('r-a')], availability: [API_PROVIDER] })
    renderIntel(<AvailableTab />)
    await screen.findByRole('row', { name: /gpt-5/ })
    expect(reads.availability).not.toHaveBeenCalled()
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('row', { name: /api-stub-model/ })).toBeNull()
  })

  it('tells someone who may read no model source so, instead of asking them to install or add', async () => {
    auth.denied.add('sessions:profile:read')
    auth.denied.add('sessions:run:read')
    auth.denied.add('models:catalog:read')
    auth.superadmin = false
    serve()
    renderIntel(<AvailableTab />)
    expect(await screen.findByText('No access to model lists')).toBeVisible()
    expect(screen.queryByText('No model available yet')).toBeNull()
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('names the model on the sessions button, and on the list of a tool that lists no models', async () => {
    serve({
      catalogs: {
        'p-oc': {
          profile_ref: 'p-oc',
          driver: 'opencode',
          state: 'unsupported',
          models: [],
          default_model: '',
          free_text: false,
          checked_at: '2026-10-23T10:00:00Z',
          refresh_interval_seconds: 120,
        },
      },
      runs: [run('r-a', { model_ref: undefined })],
    })
    const user = userEvent.setup()
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', {
      name: /its own default model/i,
    })
    const button = within(row).getByRole('button', {
      name: /1 session.*its own default model/i,
    })
    expect(button).not.toHaveAttribute('aria-controls')
    await user.click(button)
    const list = screen.getByRole('list', {
      name: /sessions running its own default model/i,
    })
    expect(button.getAttribute('aria-controls')).toBe(list.id)
  })

  it('hides availability whose cache entry was refused, even when another screen made the read', async () => {
    serve({ availability: [API_PROVIDER] })
    const queryClient = createTestQueryClient()
    renderIntel(<AvailableTab />, { queryClient })
    await screen.findByRole('row', { name: /api-stub-model/ })
    await act(() =>
      queryClient
        .fetchQuery({
          queryKey: modelsKeys.availability('t1', 1),
          queryFn: () => Promise.reject(new ApiError(403, 'forbidden', 'No.')),
          staleTime: 0,
          retry: false,
        })
        .catch(() => undefined),
    )
    await vi.waitFor(() =>
      expect(screen.queryByRole('row', { name: /api-stub-model/ })).toBeNull(),
    )
  })

  it('follows the live sessions while the tab stays open', async () => {
    vi.useFakeTimers()
    try {
      serve({ runs: [run('r-a')] })
      renderIntel(<AvailableTab />)
      await vi.waitFor(() => {
        expect(
          screen.getByRole('button', { name: /1 session/i }),
        ).toBeInTheDocument()
      })
      reads.runs.mockImplementation((p: { state: string }) =>
        Promise.resolve({
          items: p.state === 'running' ? [run('r-a'), run('r-b')] : [],
          has_more: false,
        }),
      )
      await vi.advanceTimersByTimeAsync(15_000)
      await vi.waitFor(() => {
        expect(
          screen.getByRole('button', { name: /2 sessions/i }),
        ).toBeInTheDocument()
      })
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not repaint the sessions of a refused runs read when the tab is opened again before a fresh answer', async () => {
    serve({ runs: [run('r-a')] })
    const queryClient = createTestQueryClient()
    queryClient.setDefaultOptions({
      queries: { ...queryClient.getDefaultOptions().queries, gcTime: 300_000 },
    })
    const view = renderIntel(<AvailableTab />, { queryClient })
    const row = await screen.findByRole('row', { name: /gpt-5/ })
    expect(
      within(row).getByRole('button', { name: /1 session/i }),
    ).toBeVisible()
    reads.runs.mockImplementation(() =>
      Promise.reject(new ApiError(403, 'forbidden', 'No.')),
    )
    await act(() =>
      queryClient.refetchQueries({
        predicate: (query) => query.queryKey.includes('models-available'),
      }),
    )
    reads.runs.mockImplementation(() => new Promise(() => undefined))
    view.unmount()
    renderIntel(<AvailableTab />, { queryClient })
    const again = await screen.findByRole('row', { name: /gpt-5/ })
    expect(
      within(again).queryByRole('button', { name: /1 session/i }),
    ).toBeNull()
  })

  it('does not repaint the tools of a refused profiles read when the tab is opened again before a fresh answer', async () => {
    serve()
    const queryClient = createTestQueryClient()
    queryClient.setDefaultOptions({
      queries: { ...queryClient.getDefaultOptions().queries, gcTime: 300_000 },
    })
    const view = renderIntel(<AvailableTab />, { queryClient })
    await screen.findByRole('row', { name: /gpt-5/ })
    reads.profiles.mockImplementation(() =>
      Promise.reject(new ApiError(403, 'forbidden', 'No.')),
    )
    await act(() =>
      queryClient.refetchQueries({
        queryKey: agentOpsKeys.profiles('t1', 1, { state: 'active' }),
        exact: true,
      }),
    )
    reads.profiles.mockImplementation(() => new Promise(() => undefined))
    view.unmount()
    renderIntel(<AvailableTab />, { queryClient })
    await screen.findByText('Available now')
    expect(screen.queryByRole('row', { name: /gpt-5/ })).toBeNull()
  })

  it('does not say a live session runs an unlisted model while its tool list is still loading', async () => {
    serve({
      runs: [
        run('r-a', {
          provider_profile_ref: 'p-oc',
          model_ref: 'olivares_ollama/qwen3:8b',
        }),
      ],
      ollama: { installed: true, state: 'running', models: ['qwen3:8b'] },
    })
    reads.catalog.mockImplementation(() => new Promise(() => undefined))
    renderIntel(<AvailableTab />)
    const row = await screen.findByRole('row', { name: /qwen3:8b/ })
    await vi.waitFor(() => expect(reads.runs).toHaveBeenCalledTimes(3))
    await vi.waitFor(() => expect(reads.catalog).toHaveBeenCalled())
    // Held for the tool's list: not on the Ollama row, and not called unlisted either.
    expect(within(row).queryByRole('button', { name: /1 session/i })).toBeNull()
    expect(screen.queryByText(/not listed here/i)).toBeNull()
  })
})
