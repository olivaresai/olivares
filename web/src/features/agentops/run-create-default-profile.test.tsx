// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The advanced launch opens on the profile the New session form shows (2026-10-12:
// the form said "Default sign-in" while the dialog said "Select a profile" with Model and
// Effort empty). With several profiles and nothing handed over or picked, the dialog
// takes the profile the engine would reuse for the form's own tool; a pick still wins.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: {
      createRun: vi.fn(),
      listWorkspaces: vi.fn(),
      listProfiles: vi.fn(),
      profileLaunchReadiness: vi.fn(),
      profileModels: vi.fn(),
      previewProfile: vi.fn(),
      toolsReadiness: vi.fn(),
    },
  }
})
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@tanstack/react-router', async (orig) => ({
  ...(await orig<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { firstHourKeys } from '@/features/first-hour/api'
import { useAuthBoundary } from './auth-boundary'
import { useTenantStore } from '@/stores/tenant'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { templatesApi } from '@/features/workspace-templates/api'
import { agentOpsApi, agentOpsKeys } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { RunCreateDialog } from './run-create-dialog'
import type { ProfileModelCatalogDTO, ProviderProfileDTO } from './types'

const profile = (
  ref: string,
  name: string,
  driver = 'claude',
): ProviderProfileDTO => ({
  profile_ref: ref,
  driver,
  environment_ref: 'xenv_1',
  display_name: name,
  state: 'active',
  local_environment: true,
  operable: true,
})
const homeA = profile('ppf_a', 'Home A')
const homeB = profile('ppf_b', 'Home B')
const codexHome = profile('ppf_codex', 'Codex home', 'codex')

const catalog = (ref: string, driver: string): ProfileModelCatalogDTO => ({
  profile_ref: ref,
  driver,
  state: 'fresh',
  default_model: '',
  free_text: true,
  checked_at: '2026-10-12T10:00:00Z',
  refresh_interval_seconds: 120,
  models: [
    {
      id: 'opus-5-5',
      display_name: 'Claude Opus 5.5',
      is_default: true,
      effort_levels: ['low', 'high'],
    },
  ],
})

function wrap(
  props: Partial<ComponentProps<typeof RunCreateDialog>> = {},
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } }),
) {
  return {
    qc,
    ...render(
      <QueryClientProvider client={qc}>
        <RunCreateDialog open onOpenChange={vi.fn()} {...props} />
      </QueryClientProvider>,
    ),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  useTenantStore.setState({ activeTenant: 't1' })
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA, homeB, codexHome],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileModels).mockImplementation(async (ref: string) =>
    catalog(ref, ref === 'ppf_codex' ? 'codex' : 'claude'),
  )
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) =>
      fixtureReadiness({ profile_ref: ref, configuration_state: 'ready' }),
  )
  vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
    readinessOf({ claude: 'own_login', codex: 'own_login' }),
  )
  vi.mocked(agentOpsApi.previewProfile).mockImplementation(
    async (driver: string) => ({
      reason: 'own_login',
      profile: driver === 'codex' ? codexHome : homeB,
    }),
  )
})

describe('RunCreateDialog — the default profile follows the New session form', () => {
  it('opens on the profile the form would run under, and loads its models at once', async () => {
    wrap()
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Home B/,
      ),
    )
    expect(screen.getByLabelText('Provider profile')).not.toHaveTextContent(
      'Select a profile',
    )
    await waitFor(() =>
      expect(agentOpsApi.profileModels).toHaveBeenCalledWith(
        'ppf_b',
        expect.anything(),
      ),
    )
    expect(agentOpsApi.previewProfile).toHaveBeenCalledWith(
      'claude',
      expect.anything(),
    )
  })

  it("asks for the form's tool: the first ready one, not always Claude Code", async () => {
    vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
      readinessOf({ codex: 'own_login' }),
    )
    wrap()
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Codex home/,
      ),
    )
    expect(agentOpsApi.previewProfile).toHaveBeenCalledWith(
      'codex',
      expect.anything(),
    )
  })

  it('reads the default for the tool the form had chosen, not the first ready one', async () => {
    wrap({ initialDriver: 'codex' })
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Codex home/,
      ),
    )
    expect(agentOpsApi.previewProfile).toHaveBeenCalledWith(
      'codex',
      expect.anything(),
    )
    expect(agentOpsApi.previewProfile).not.toHaveBeenCalledWith(
      'claude',
      expect.anything(),
    )
  })

  it('offers no default for a tool on a key: it has no profile before its first launch', async () => {
    vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(
      readinessOf({
        claude: 'own_login',
        codex: {
          provider: {
            provider_ref: 'prv-1',
            kind: 'openai',
            display_name: 'Team key',
            models: ['gpt-a'],
          },
        },
      }),
    )
    wrap({ initialDriver: 'codex' })
    await waitFor(() => expect(agentOpsApi.toolsReadiness).toHaveBeenCalled())
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(agentOpsApi.previewProfile).not.toHaveBeenCalled()
    expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
      'Select a profile',
    )
  })

  it.each(['key-backed', 'not-ready'] as const)(
    'ignores a cached reuse profile for a %s tool',
    async (state) => {
      const qc = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      const boundary = renderHook(useAuthBoundary, {
        wrapper: ({ children }) => (
          <QueryClientProvider client={qc}>{children}</QueryClientProvider>
        ),
      })
      const { tenant, epoch } = boundary.result.current
      boundary.unmount()
      const reuseKey = [
        ...agentOpsKeys.boundaryScope(tenant, epoch),
        'reuse-profile',
        'codex',
      ]
      qc.setQueryData(reuseKey, { reason: 'own_login', profile: codexHome })
      const readiness = readinessOf({
        claude: 'own_login',
        ...(state === 'key-backed'
          ? {
              codex: {
                provider: {
                  provider_ref: 'prv-1',
                  kind: 'openai',
                  display_name: 'Team key',
                  models: ['gpt-a'],
                },
              },
            }
          : {}),
      })
      qc.setQueryData(firstHourKeys.readiness(tenant), readiness)
      vi.mocked(agentOpsApi.toolsReadiness).mockResolvedValue(readiness)
      wrap({ initialDriver: 'codex' }, qc)
      await waitFor(() => expect(agentOpsApi.listProfiles).toHaveBeenCalled())
      await waitFor(() =>
        expect(
          screen
            .getAllByRole('option', { hidden: true })
            .some((option) => option.textContent?.includes('Codex home')),
        ).toBe(true),
      )
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        'Select a profile',
      )
      expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
      expect(agentOpsApi.previewProfile).not.toHaveBeenCalled()
      expect(agentOpsApi.profileModels).not.toHaveBeenCalled()
      expect(agentOpsApi.profileLaunchReadiness).not.toHaveBeenCalled()
      expect(qc.getQueryData(reuseKey)).toMatchObject({
        profile: { profile_ref: 'ppf_codex' },
      })
    },
  )

  it('keeps the default it took when the engine later answers with another profile', async () => {
    const { qc } = wrap()
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Home B/,
      ),
    )
    vi.mocked(agentOpsApi.previewProfile).mockResolvedValue({
      reason: 'own_login',
      profile: homeA,
    })
    await qc.invalidateQueries()
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
      /Home B/,
    )
  })

  it('reads nothing to find a default when one profile exists or one was handed over', async () => {
    vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
      items: [homeA],
      has_more: false,
    })
    wrap()
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Home A/,
      ),
    )
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(agentOpsApi.toolsReadiness).not.toHaveBeenCalled()
    expect(agentOpsApi.previewProfile).not.toHaveBeenCalled()
  })

  it('launches on the default profile without a choice', async () => {
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Home B/,
      ),
    )
    await user.click(screen.getByRole('button', { name: 'Advanced options' }))
    await screen.findByText('Local requirements checked')
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    const body = vi.mocked(agentOpsApi.createRun).mock
      .calls[0][0] as unknown as Record<string, unknown>
    expect(body.provider_profile_ref).toBe('ppf_b')
  })

  it('keeps a profile the person picks over the default', async () => {
    const user = userEvent.setup()
    wrap()
    await waitFor(() =>
      expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
        /Home B/,
      ),
    )
    await user.click(screen.getByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home A/ }))
    expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
      /Home A/,
    )
  })

  it('offers no default when the profile the engine would reuse is not in the list', async () => {
    vi.mocked(agentOpsApi.previewProfile).mockResolvedValue({
      reason: 'own_login',
      profile: profile('ppf_retired', 'Retired home'),
    })
    wrap()
    await waitFor(() =>
      expect(agentOpsApi.previewProfile).toHaveBeenCalledOnce(),
    )
    // Let the answer land and render: a negative assertion has nothing to wait for.
    await new Promise((resolve) => setTimeout(resolve, 100))
    expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
      'Select a profile',
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    // Nothing is read for a profile the list does not hold.
    expect(agentOpsApi.profileLaunchReadiness).not.toHaveBeenCalled()
    expect(agentOpsApi.profileModels).not.toHaveBeenCalled()
  })
})
