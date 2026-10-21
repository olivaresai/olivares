// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Model and effort, from the live catalog of the session's profile: grouped, searchable,
// favorites kept, an effort that follows its model, and free text when the tool cannot
// list its models. Launch speed choices belong to the full launch dialog.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  can: (_: string): boolean => true,
  principal: { kind: 'user', user_id: 'u1' } as Record<string, unknown> | null,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: auth.can,
    principal: auth.principal,
  }),
}))
vi.mock('./api', async (orig) => ({
  ...(await orig<typeof import('./api')>()),
  agentOpsApi: { profileModels: vi.fn() },
}))

import { agentOpsApi } from './api'
import { ModelEffortFields } from './model-effort-picker'
import type { ProfileModelCatalogDTO, ProfileModelDTO } from './types'

const model = (
  id: string,
  over: Partial<ProfileModelDTO> = {},
): ProfileModelDTO => ({ id, ...over })

const catalog = (
  models: ProfileModelDTO[],
  over: Partial<ProfileModelCatalogDTO> = {},
): ProfileModelCatalogDTO => ({
  profile_ref: 'ppf_a',
  driver: 'claude',
  state: 'fresh',
  models,
  default_model: '',
  free_text: true,
  checked_at: '2026-10-12T10:00:00Z',
  refresh_interval_seconds: 120,
  ...over,
})

const MODELS = [
  model('opus-5-5', {
    display_name: 'Claude Opus 5.5',
    is_default: true,
    effort_levels: ['low', 'medium', 'high', 'xhigh', 'max'],
    default_effort: 'medium',
    supports_fast_mode: true,
  }),
  model('haiku-5-5', {
    display_name: 'Claude Haiku 5.5',
    is_new: true,
    effort_levels: ['low', 'high'],
  }),
  model('sonnet-4', {
    display_name: 'Claude Sonnet 4',
    is_legacy: true,
    effort_levels: ['low', 'medium', 'high', 'ultra'],
  }),
]

let last: { model: string; effort: string } = { model: '', effort: '' }

function Harness({
  driver = 'claude',
  profileRef = 'ppf_a',
  initial = { model: '', effort: '' },
}: {
  driver?: string
  profileRef?: string
  initial?: { model: string; effort: string }
}) {
  const [value, setValue] = useState(initial)
  return (
    <ModelEffortFields
      profileRef={profileRef}
      driver={driver}
      model={value.model}
      effort={value.effort}
      onChange={(next) => {
        last = next
        setValue(next)
      }}
      enabled
    />
  )
}

function wrap(props: Parameters<typeof Harness>[0] = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <Harness {...props} />
    </QueryClientProvider>,
  )
}

async function openModels(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: 'Model' }))
  return screen.findByRole('listbox')
}

beforeEach(() => {
  vi.clearAllMocks()
  last = { model: '', effort: '' }
  window.localStorage.clear()
  auth.can = () => true
  auth.principal = { kind: 'user', user_id: 'u1' }
  vi.mocked(agentOpsApi.profileModels).mockResolvedValue(catalog(MODELS))
})

describe('the model list', () => {
  it("lists the tool's models with its own default, new and legacy markers, legacy last", async () => {
    const user = userEvent.setup()
    wrap()
    const list = await openModels(user)
    await waitFor(() =>
      expect(within(list).getByText('Claude Opus 5.5')).toBeInTheDocument(),
    )
    const rows = within(list)
      .getAllByRole('option')
      .map((o) => o.textContent)
    expect(rows).toEqual([
      'Default (Claude Opus 5.5)',
      'Claude Opus 5.5default',
      'Claude Haiku 5.5New',
      'Claude Sonnet 4',
    ])
    expect(screen.getByText('Legacy')).toBeInTheDocument()
    expect(agentOpsApi.profileModels).toHaveBeenCalledWith(
      'ppf_a',
      expect.objectContaining({ tenant: 't1' }),
    )
  })

  it('searches the list', async () => {
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    await screen.findByText('Claude Haiku 5.5')
    await user.type(screen.getByPlaceholderText('Search models'), 'haiku')
    expect(screen.queryByText('Claude Opus 5.5')).toBeNull()
    expect(screen.getByText('Claude Haiku 5.5')).toBeInTheDocument()
    // A row that leaves the list takes its star with it.
    expect(
      screen
        .getAllByRole('button', { name: /to favorites$/ })
        .map((b) => b.getAttribute('aria-label')),
    ).toEqual(['Add Claude Haiku 5.5 to favorites'])
  })

  it('says so when nothing matches and offers the typed text as a model', async () => {
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    await screen.findByText('Claude Haiku 5.5')
    await user.type(screen.getByPlaceholderText('Search models'), 'zzz')
    expect(screen.getByText('Use “zzz”')).toBeInTheDocument()
    expect(screen.queryByText('Claude Opus 5.5')).toBeNull()
  })

  it('offers no typed model when the tool takes none', async () => {
    vi.mocked(agentOpsApi.profileModels).mockResolvedValue(
      catalog(MODELS, { free_text: false }),
    )
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    await screen.findByText('Claude Haiku 5.5')
    await user.type(screen.getByPlaceholderText('Search models'), 'zzz')
    expect(screen.queryByText('Use “zzz”')).toBeNull()
    expect(screen.getByText('No model matches.')).toBeInTheDocument()
  })

  it("picks the tool's default as no model at all", async () => {
    const user = userEvent.setup()
    wrap({ initial: { model: 'haiku-5-5', effort: 'low' } })
    const list = await openModels(user)
    await user.click(await within(list).findByText('Default (Claude Opus 5.5)'))
    await waitFor(() => expect(last).toEqual({ model: '', effort: 'low' }))
  })

  it('shows a favorite first, and keeps it after a reload of the page state', async () => {
    const user = userEvent.setup()
    const first = wrap()
    await openModels(user)
    await user.click(
      await screen.findByRole('button', {
        name: 'Add Claude Haiku 5.5 to favorites',
      }),
    )
    first.unmount()
    wrap()
    const list = await openModels(user)
    await screen.findByText('Favorites')
    const options = within(list).getAllByRole('option')
    expect(options[1]).toHaveTextContent('Claude Haiku 5.5')
    expect(
      screen.getByRole('button', {
        name: 'Remove Claude Haiku 5.5 from favorites',
      }),
    ).toHaveAttribute('aria-pressed', 'true')
  })

  it('offers no star when there is no signed-in user to keep it for', async () => {
    auth.principal = null
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    await screen.findByText('Claude Haiku 5.5')
    expect(screen.queryByRole('button', { name: /to favorites/ })).toBeNull()
  })

  it('forgets a favorite when its star is pressed again', async () => {
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    await user.click(
      await screen.findByRole('button', {
        name: 'Add Claude Haiku 5.5 to favorites',
      }),
    )
    await user.click(
      await screen.findByRole('button', {
        name: 'Remove Claude Haiku 5.5 from favorites',
      }),
    )
    expect(screen.queryByText('Favorites')).toBeNull()
    expect(
      JSON.parse(
        window.localStorage.getItem(
          Object.keys(window.localStorage).find((k) =>
            k.startsWith('olivares.sessions.model-favorites'),
          )!,
        )!,
      ).favorites,
    ).toEqual([])
  })
})

describe('the effort', () => {
  it('is refused while no model is known, and says why', async () => {
    vi.mocked(agentOpsApi.profileModels).mockResolvedValue(
      catalog([model('a'), model('b')]),
    )
    wrap()
    await waitFor(() => expect(screen.getByLabelText('Effort')).toBeDisabled())
    expect(
      screen.getByText(
        'Choose a model first: an effort is a level of a model.',
      ),
    ).toBeInTheDocument()
  })

  it("offers the levels of the model the session starts on, and names the model's own default", async () => {
    const user = userEvent.setup()
    wrap()
    await waitFor(() => expect(screen.getByLabelText('Effort')).toBeEnabled())
    await user.click(screen.getByLabelText('Effort'))
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual([
      'Model default (medium)',
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ])
  })

  it('follows the chosen model, and forgets a level the new model does not take', async () => {
    const user = userEvent.setup()
    wrap({ initial: { model: 'opus-5-5', effort: 'max' } })
    const list = await openModels(user)
    await user.click(await within(list).findByText('Claude Haiku 5.5'))
    await waitFor(() =>
      expect(last).toEqual({ model: 'haiku-5-5', effort: '' }),
    )
    await user.click(screen.getByLabelText('Effort'))
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual(['Model default', 'low', 'high'])
  })

  it('offers a Claude launch only the levels it accepts, so no choice ends in a 400', async () => {
    const user = userEvent.setup()
    wrap({ initial: { model: 'sonnet-4', effort: '' } })
    await waitFor(() => expect(screen.getByLabelText('Effort')).toBeEnabled())
    await user.click(screen.getByLabelText('Effort'))
    const levels = (await screen.findAllByRole('option')).map(
      (o) => o.textContent,
    )
    expect(levels).toEqual(['Model default', 'low', 'medium', 'high'])
    expect(levels).not.toContain('ultra')
  })

  it('offers a Codex launch the levels the tool reports, vendor values included', async () => {
    vi.mocked(agentOpsApi.profileModels).mockResolvedValue(
      catalog(
        [model('gpt-x', { effort_levels: ['minimal', 'low', 'ultra'] })],
        {
          driver: 'codex',
          default_model: 'gpt-x',
        },
      ),
    )
    const user = userEvent.setup()
    wrap({ driver: 'codex' })
    await waitFor(() => expect(screen.getByLabelText('Effort')).toBeEnabled())
    await user.click(screen.getByLabelText('Effort'))
    expect(
      (await screen.findAllByRole('option')).map((o) => o.textContent),
    ).toEqual(['Model default', 'minimal', 'low', 'ultra'])
  })
})

describe('fast mode', () => {
  it('is never offered, even for a model that reports it', async () => {
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    await screen.findByText('Claude Opus 5.5')
    expect(document.body.innerHTML).not.toMatch(/fast/i)
    expect(screen.queryByRole('switch')).toBeNull()
  })
})

describe('a tool that cannot list its models', () => {
  it('says so once and takes a typed model', async () => {
    vi.mocked(agentOpsApi.profileModels).mockResolvedValue(
      catalog([], {
        state: 'unsupported',
        driver: 'opencode',
        message: 'not listed',
      }),
    )
    const user = userEvent.setup()
    wrap({ driver: 'opencode' })
    await openModels(user)
    expect(
      await screen.findByText(
        'This tool does not list its models. Type one, or keep the default.',
      ),
    ).toBeInTheDocument()
    await user.type(screen.getByPlaceholderText('Search models'), 'my-model')
    await user.click(await screen.findByText('Use “my-model”'))
    expect(last.model).toBe('my-model')
  })

  it('says the tool did not answer when the read fails, and still takes a typed model', async () => {
    vi.mocked(agentOpsApi.profileModels).mockRejectedValue(new Error('down'))
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    expect(
      await screen.findByText(
        'The tool did not answer, so there is no list. Type a model, or keep the default.',
      ),
    ).toBeInTheDocument()
    await user.type(screen.getByPlaceholderText('Search models'), 'typed')
    await user.click(await screen.findByText('Use “typed”'))
    expect(last.model).toBe('typed')
  })

  it('reads nothing without a profile', async () => {
    const user = userEvent.setup()
    wrap({ profileRef: '' })
    const list = await openModels(user)
    expect(agentOpsApi.profileModels).not.toHaveBeenCalled()
    expect(within(list).getByText('Claude Code default')).toBeInTheDocument()
    expect(
      screen.getByText(
        'The model list is not available here. Type a model, or keep the default.',
      ),
    ).toBeInTheDocument()
  })

  it('reads nothing without the profile read permission', async () => {
    auth.can = (p) => p !== 'sessions:profile:read'
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    expect(agentOpsApi.profileModels).not.toHaveBeenCalled()
    expect(
      screen.getByText(
        'The model list is not available here. Type a model, or keep the default.',
      ),
    ).toBeInTheDocument()
  })

  it('treats a 200 answer that says it failed as a failure, not as an empty list', async () => {
    vi.mocked(agentOpsApi.profileModels).mockResolvedValue(
      catalog([], { state: 'failed' }),
    )
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    expect(
      await screen.findByText(
        'The tool did not answer, so there is no list. Type a model, or keep the default.',
      ),
    ).toBeInTheDocument()
  })

  it('does not take an empty fresh list for a catalog', async () => {
    vi.mocked(agentOpsApi.profileModels).mockResolvedValue(catalog([]))
    const user = userEvent.setup()
    wrap()
    await openModels(user)
    expect(
      await screen.findByText(
        'This tool does not list its models. Type one, or keep the default.',
      ),
    ).toBeInTheDocument()
  })

  it('keeps a model already chosen when a later read fails', async () => {
    vi.mocked(agentOpsApi.profileModels).mockRejectedValue(new Error('down'))
    wrap({ initial: { model: 'opus-5-5', effort: 'high' } })
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Model' })).toHaveTextContent(
        'opus-5-5',
      ),
    )
    // The failure is said, and the choice made is still the one shown and kept.
    expect(screen.getByLabelText('Effort')).toHaveTextContent('high')
  })
})
