// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU2-37: the ordinary New session dialog had no model. A tool on a key now shows the key's
// saved default and the models its last test found, and sends a model only when the user
// picks one; the engine resolves the default itself.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ providers: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: (p: string) => p !== 'sessions:provider:read' || auth.providers,
    isSuperadmin: true,
  }),
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: () => '',
}))
const launch = vi.hoisted(() => ({ launchSession: vi.fn() }))
vi.mock('@/features/agentops/session-launch', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  launchSession: launch.launchSession,
}))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import type { ProviderRecordDTO } from '@/features/providers/types'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'
import { readinessOf } from './readiness.fixture'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const key = (over: Partial<ProviderRecordDTO> = {}): ProviderRecordDTO => ({
  provider_ref: 'prv-1',
  kind: 'openai',
  display_name: 'Team key',
  state: 'active',
  probe_state: 'ok',
  models: ['gpt-a', 'gpt-b'],
  default_model: 'gpt-b',
  ...over,
})

// The key as the engine's readiness carries it: its saved default and tested models.
const source = (record: ProviderRecordDTO) => ({
  provider_ref: record.provider_ref,
  kind: record.kind,
  display_name: record.display_name,
  default_model: record.default_model ?? undefined,
  models: record.models,
})

// Codex runs on the key; every other tool on its own login.
function given(
  record: ProviderRecordDTO,
  preview: { model_required?: boolean } = {},
) {
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({
      claude: 'own_login',
      grok: 'own_login',
      opencode: 'own_login',
      codex: { provider: source(record), ...preview },
    }),
  )
}

async function chooseCodex(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('radio', { name: 'Codex' }))
}

beforeEach(() => {
  vi.restoreAllMocks()
  launch.launchSession.mockReset()
  launch.launchSession.mockResolvedValue({ run_ref: 'run_1' })
  auth.providers = true
  useTenantStore.setState({ activeTenant: 'tnt-a' })
})

describe('the model in the New session dialog', () => {
  it("shows the bound key's saved default and its tested models", async () => {
    const user = userEvent.setup()
    given(key())
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    const model = await screen.findByRole('combobox', { name: 'Model' })
    expect(model).toHaveTextContent('Default (gpt-b)')
    expect(
      screen.getByText("Models that passed this key's last connection test."),
    ).toBeInTheDocument()
    await user.click(model)
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual(['Default (gpt-b)', 'gpt-a', 'gpt-b'])
    await user.click(screen.getByRole('option', { name: 'gpt-a' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      driver: 'codex',
      model: 'gpt-a',
    })
  })

  it("offers the tool's own default when the key has none, and sends no model for it", async () => {
    const user = userEvent.setup()
    given(key({ default_model: null }))
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    expect(
      await screen.findByRole('combobox', { name: 'Model' }),
    ).toHaveTextContent('Codex default')
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).not.toHaveProperty(
      'model',
    )
  })

  // HU2 041: on a key never launched, the person had to fail once ("Choose a model…") before
  // a Codex profile existed. The choice now shows at once and the first Start carries it.
  it('asks for a model at once on a never-launched key that needs one, and the first Start sends it', async () => {
    const user = userEvent.setup()
    given(key({ default_model: null }), { model_required: true })
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    const model = await screen.findByRole('combobox', { name: 'Model' })
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(model)
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual(['gpt-a', 'gpt-b'])
    await user.click(screen.getByRole('option', { name: 'gpt-b' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(launch.launchSession).toHaveBeenCalledTimes(1))
    expect(launch.launchSession.mock.calls[0][0].quick).toMatchObject({
      driver: 'codex',
      model: 'gpt-b',
    })
  })

  // A Start that cannot act says beside it what is missing.
  it('says beside the disabled Start that the key needs a model', async () => {
    const user = userEvent.setup()
    given(key({ default_model: null }), { model_required: true })
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    await screen.findByRole('combobox', { name: 'Model' })
    const start = screen.getByRole('button', { name: 'Start' })
    expect(start).toBeDisabled()
    expect(start).toHaveAccessibleDescription(
      'Choose a model: this key has no default model.',
    )
    await user.click(screen.getByRole('combobox', { name: 'Model' }))
    await user.click(await screen.findByRole('option', { name: 'gpt-a' }))
    // DisabledReason renders the bare control once it can act: query it again.
    const ready = screen.getByRole('button', { name: 'Start' })
    expect(ready).toBeEnabled()
    expect(ready).not.toHaveAccessibleDescription()
  })

  it('says beside the disabled Start that no model of the key passed its test', async () => {
    const user = userEvent.setup()
    given(key({ default_model: null, models: [] }), { model_required: true })
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    await screen.findByRole('combobox', { name: 'Model' })
    expect(
      screen.getByRole('button', { name: 'Start' }),
    ).toHaveAccessibleDescription(
      'No model of this key has passed its connection test yet: test the key in Providers.',
    )
  })

  // SR4C on 5f26d195: a forgotten pick made the Select uncontrolled, so it kept showing the
  // old model with Start disabled, and picking it again changed nothing.
  it('forgets a required pick across two tools on keys, and takes it again', async () => {
    const user = userEvent.setup()
    const warn = vi.spyOn(console, 'warn')
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({
        codex: {
          provider: source(key({ default_model: null, models: ['gpt-a'] })),
          model_required: true,
        },
        claude: {
          provider: source(
            key({
              provider_ref: 'prv-2',
              kind: 'anthropic',
              models: ['claude-x'],
            }),
          ),
        },
        grok: 'own_login',
        opencode: 'own_login',
      }),
    )
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    await user.click(await screen.findByRole('combobox', { name: 'Model' }))
    await user.click(await screen.findByRole('option', { name: 'gpt-a' }))
    await user.click(screen.getByRole('radio', { name: 'Claude Code' }))
    await chooseCodex(user)
    const model = await screen.findByRole('combobox', { name: 'Model' })
    expect(model).not.toHaveTextContent('gpt-a')
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(model)
    await user.click(await screen.findByRole('option', { name: 'gpt-a' }))
    expect(model).toHaveTextContent('gpt-a')
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled()
    // The Select stayed controlled throughout: an uncontrolled one keeps its old choice.
    expect(warn.mock.calls.flat().join(' ')).not.toMatch(
      /from controlled to uncontrolled/,
    )
  })

  // SR4C on f50c84fc: with no tested model and no saved default, a key that needs a model
  // could only fail. Start waits, and the way to test the key is offered.
  it('sends to Providers to test a key that needs a model and has none tested', async () => {
    const user = userEvent.setup()
    given(key({ default_model: null, models: [] }), { model_required: true })
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    expect(
      await screen.findByRole('combobox', { name: 'Model' }),
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Open Providers' }))
    expect(navigate).toHaveBeenCalledWith({ to: '/providers' })
    expect(launch.launchSession).not.toHaveBeenCalled()
  })

  it("offers no model choice on the tool's own login", async () => {
    given(key())
    wrap(<StartSessionForm />)
    await screen.findByRole('radio', { name: 'Claude Code' })
    expect(screen.queryByRole('combobox', { name: 'Model' })).toBeNull()
  })

  // ARCH.C3: the key's models come with the engine's answer, so a person who may start
  // sessions but not open Providers picks a model too, instead of a Start that fails for
  // want of one. Only the way to Providers stays behind its permission.
  it('offers the model choice without Providers access, but not the way to Providers', async () => {
    const user = userEvent.setup()
    auth.providers = false
    given(key({ default_model: null, models: [] }), { model_required: true })
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    expect(
      await screen.findByRole('combobox', { name: 'Model' }),
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Open Providers' })).toBeNull()
  })

  it('forgets a chosen model when the tool changes', async () => {
    const user = userEvent.setup()
    given(key())
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    await user.click(await screen.findByRole('combobox', { name: 'Model' }))
    await user.click(await screen.findByRole('option', { name: 'gpt-a' }))
    await user.click(screen.getByRole('radio', { name: 'Claude Code' }))
    await chooseCodex(user)
    expect(
      await screen.findByRole('combobox', { name: 'Model' }),
    ).toHaveTextContent('Default (gpt-b)')
  })

  it("shows the engine's sentence and the way to Providers when the saved default is unavailable", async () => {
    const user = userEvent.setup()
    given(key())
    launch.launchSession.mockRejectedValue(
      new ApiError(
        409,
        'provider_default_model_unavailable',
        'The saved default model gpt-b is not available on Team key.',
      ),
    )
    wrap(<StartSessionForm />)
    await chooseCodex(user)
    await screen.findByRole('combobox', { name: 'Model' })
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(
      await screen.findByText(/The saved default model gpt-b is not available/),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Open Providers' }))
    expect(navigate).toHaveBeenCalledWith({ to: '/providers' })
  })
})

// #1083: Start is disabled while the chosen tool cannot start; the reason is the sentence
// under the tool choice, and Start must name it (first-hour run 37903242313).
describe('Start for a tool that cannot start yet', () => {
  it('is disabled and described by the not-ready sentence', async () => {
    const user = userEvent.setup()
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({
        claude: 'own_login',
        opencode: {
          code: 'tool_not_installed',
          message: 'Install OpenCode first, under AI tools.',
        },
      }),
    )
    wrap(<StartSessionForm />)
    await user.click(await screen.findByRole('radio', { name: 'OpenCode' }))
    // Read Start again on each try: it remounts once the folder settles.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Start' }),
      ).toHaveAccessibleDescription(
        /^Install OpenCode first, under AI tools\./,
      ),
    )
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  })
})
