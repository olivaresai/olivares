// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StrictMode } from 'react'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, toastMock } = vi.hoisted(() => ({
  api: {
    listConnectors: vi.fn(),
    listSources: vi.fn(),
    putConnector: vi.fn(),
    testConnector: vi.fn(),
    deleteConnector: vi.fn(),
    reloadRuntime: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { user_id: 'u1', aal: 3 } as {
      user_id?: string
      aal?: number
    } | null,
    can: (_p: string) => true,
  },
  // Spy the toast surface so we can assert the reload success message is QUALIFIED
  // (restart caveat / partial), never a plain "reloaded".
  toastMock: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
// `RequireAssurance` stays the passthrough the accepted write tests need. Only the
// LAZY ceremony PANEL is doubled, as a marker (the pattern of
// data-table-step-up.test.tsx): the real `StepUpRequiredState` wrapper — including
// which state the tab paints for a `step_up_required` READ — stays under test.
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
  StepUpPanel: ({ action }: { action: string }) => (
    <span>{`step-up ceremony:${action}`}</span>
  ),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: toastMock,
  Toaster: () => null,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { ApiError } from '@/lib/api/errors'
import { createQueryClient } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { useStepUpStore } from '@/stores/step-up'
import { ConnectorsTab } from './connectors-tab'
// The literal GET /v1/console/sources body, generated from the real endpoint by
// core/api/handlers_sources_instances_test.go (regenerate with
// OLIVARES_UPDATE_GOLDEN=1). Two rows of ONE connector kind, in both states.
import sourcesTwoOfOneKind from './testdata/sources-two-of-one-kind.json'

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const catalog = {
  connectors: [
    {
      kind: 'vault',
      title: 'Vault',
      description: 'HashiCorp Vault audit',
      transport: 'in_process',
      fields_known: true,
      fields: [
        { key: 'base_url', type: 'string', required: true, secret: false },
        { key: 'token', type: 'string', required: true, secret: true },
      ],
    },
  ],
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.isSuperadmin = true
  authState.principal = { user_id: 'u1', aal: 3 }
  authState.activeTenant = 't1'
  useSessionStore.setState({ credentialGeneration: 0 })
  api.listConnectors.mockResolvedValue(catalog)
  api.listSources.mockResolvedValue({ sources: [] })
})

describe('ConnectorsTab', () => {
  it('is superadmin-only', async () => {
    authState.isSuperadmin = false
    wrap(<ConnectorsTab />)
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(api.listConnectors).not.toHaveBeenCalled()
  })

  it('lists configured connectors with source-mode badges and filters them', async () => {
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          config: {},
        },
        {
          name: 'wiki-live',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          source_mode: 'live',
          config: { mode: 'live' },
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(screen.getByText('wiki-live')).toBeInTheDocument()
    expect(screen.getByText('Export')).toBeInTheDocument()
    expect(screen.getByText('Live')).toBeInTheDocument()
    expect(screen.getAllByText('Running')).toHaveLength(2)
    await user.click(screen.getByRole('combobox', { name: /mode filter/i }))
    await user.click(screen.getByRole('option', { name: 'Live' }))
    expect(screen.queryByText('vault-prod')).toBeNull()
    expect(screen.getByText('wiki-live')).toBeInTheDocument()
    // A filter that MATCHES is rows: no zero-match copy, no clear action.
    expect(
      screen.queryByText('No connectors match this mode.'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all modes' }),
    ).not.toBeInTheDocument()
  })

  // Several sources of ONE connector kind. Until this lot, a second row of a kind
  // was persisted and never wired, so the console listed a source the engine had
  // refused. The fixture is not written here: it is the literal body of
  // GET /v1/console/sources, generated and held against the endpoint by
  // core/api/handlers_sources_instances_test.go, so this case cannot drift into
  // asserting a shape the engine does not send.
  it('lists two sources of one kind with independent statuses', async () => {
    api.listSources.mockResolvedValue(sourcesTwoOfOneKind.both_running)
    wrap(<ConnectorsTab />)

    expect(await screen.findByText('grok-home-a')).toBeInTheDocument()
    expect(screen.getByText('grok-home-b')).toBeInTheDocument()
    // One connector kind, two rows: the kind column repeats, the name column does not.
    expect(screen.getAllByText('grok')).toHaveLength(2)
    expect(screen.getAllByText('Running')).toHaveLength(2)
  })

  it('keeps the sibling running when one source of the kind is disabled', async () => {
    // Both rows first, then the roster as the endpoint reports it after A is
    // disabled — a refetch, i.e. what the console shows after a reload/restart.
    api.listSources.mockResolvedValueOnce(sourcesTwoOfOneKind.both_running)
    api.listSources.mockResolvedValue(sourcesTwoOfOneKind.a_disabled)
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = render(
      <QueryClientProvider client={qc}>
        <ConnectorsTab />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('grok-home-a')).toBeInTheDocument()
    expect(screen.getAllByText('Running')).toHaveLength(2)

    await qc.invalidateQueries({ queryKey: ['console', 'sources'] })
    await waitFor(() => {
      expect(screen.getByText('Disabled')).toBeInTheDocument()
    })
    // The sibling is untouched: still listed, still running, still its own row.
    expect(screen.getByText('grok-home-b')).toBeInTheDocument()
    expect(screen.getAllByText('Running')).toHaveLength(1)
    expect(screen.getByText('grok-home-a')).toBeInTheDocument()
    view.unmount()
  })

  it('shows an empty state when there are no connectors', async () => {
    wrap(<ConnectorsTab />)
    expect(await screen.findByText(/no connectors yet/i)).toBeInTheDocument()
  })

  // --- Filtered-empty recovery (successful, NON-EMPTY roster only) -----------------

  /** The mode facet's trigger — a Radix Select trigger is a `button role="combobox"`. */
  const modeTrigger = () =>
    screen.getByRole('combobox', { name: /mode filter/i })
  /** The empty-state region that holds `text`, typed for `within()`. */
  function emptyStateOf(text: string | RegExp): HTMLElement {
    const region = screen.getByText(text).closest('[data-slot="empty-state"]')
    expect(region).not.toBeNull()
    return region as HTMLElement
  }
  /** Every write or reload the tab can issue. None may fire from a filter change. */
  function expectNoWriteOrReload() {
    expect(api.putConnector).not.toHaveBeenCalled()
    expect(api.testConnector).not.toHaveBeenCalled()
    expect(api.deleteConnector).not.toHaveBeenCalled()
    expect(api.reloadRuntime).not.toHaveBeenCalled()
  }

  // ⛔ Until this lot the zero-match branch reused the ESTATE hint — "Add a connector to
  // start ingesting" — over a roster that was not empty: it told the operator to create
  // what they had just filtered out of view. The recovery is the facet, so the copy
  // names it and the one action drops it. Two independent instances of ONE kind, from
  // the endpoint's own golden body, so the recovery is also seen not to collapse them.
  it('an export-only roster filtered to Live names the mode, offers no Add, and recovers through the facet without a new read or any write', async () => {
    api.listSources.mockResolvedValue(sourcesTwoOfOneKind.both_running)
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    expect(await screen.findByText('grok-home-a')).toBeInTheDocument()
    expect(screen.getByText('grok-home-b')).toBeInTheDocument()
    expect(api.listSources).toHaveBeenCalledTimes(1)
    expect(api.listConnectors).toHaveBeenCalledTimes(1)

    await user.click(modeTrigger())
    await user.click(screen.getByRole('option', { name: 'Live' }))
    expect(screen.queryByText('grok-home-a')).not.toBeInTheDocument()
    expect(screen.queryByText('grok-home-b')).not.toBeInTheDocument()
    const empty = emptyStateOf('No connectors match this mode.')
    expect(
      within(empty).getByText(
        'Only connectors in the “Live” mode are listed. Show all modes to remove this filter.',
      ),
    ).toBeInTheDocument()
    // Not an estate: no add-oriented instruction, no "none yet", and the ONE control
    // inside is the facet's — never a create.
    expect(empty.textContent).not.toMatch(/add a connector|none yet|ingest/i)
    expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
    expect(within(empty).getAllByRole('button')).toHaveLength(1)
    expect(
      within(empty).queryByRole('button', { name: /add connector/i }),
    ).not.toBeInTheDocument()
    // The facet stays on screen, showing the value that hides the rows.
    expect(modeTrigger()).toHaveTextContent('Live')

    await user.click(
      within(empty).getByRole('button', { name: 'Show all modes' }),
    )
    // Both instances of the one kind are back as TWO rows with their own statuses.
    expect(await screen.findByText('grok-home-a')).toBeInTheDocument()
    expect(screen.getByText('grok-home-b')).toBeInTheDocument()
    expect(screen.getAllByText('grok')).toHaveLength(2)
    expect(screen.getAllByText('Running')).toHaveLength(2)
    expect(modeTrigger()).toHaveTextContent('All modes')
    // The button left with the empty state it lived in; focus moved to the trigger.
    expect(
      screen.queryByRole('button', { name: 'Show all modes' }),
    ).not.toBeInTheDocument()
    expect(modeTrigger()).toHaveFocus()
    // Recovery was a filter change over the SAME loaded roster: no new read, no
    // catalog re-read, and nothing written or reloaded.
    expect(api.listSources).toHaveBeenCalledTimes(1)
    expect(api.listConnectors).toHaveBeenCalledTimes(1)
    expectNoWriteOrReload()
  })

  it('the clear action is a real button: keyboard activation recovers the same rows', async () => {
    api.listSources.mockResolvedValue(sourcesTwoOfOneKind.both_running)
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await screen.findByText('grok-home-a')
    await user.click(modeTrigger())
    await user.click(screen.getByRole('option', { name: 'Live' }))
    const clear = await screen.findByRole('button', { name: 'Show all modes' })
    clear.focus()
    expect(clear).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(await screen.findByText('grok-home-a')).toBeInTheDocument()
    expect(screen.getByText('grok-home-b')).toBeInTheDocument()
    expect(modeTrigger()).toHaveFocus()
    expect(api.listSources).toHaveBeenCalledTimes(1)
    expectNoWriteOrReload()
  })

  // Regression BOUNDARIES, not acceptance of the deferred catalog-error design: the
  // estate-empty, catalog-501 and source-error screens are what they were before this
  // lot — none of them grows a clear action, and none grows a new write control.
  it('keeps the estate-empty screen: the add hint, no clear action, no second Add', async () => {
    wrap(<ConnectorsTab />)
    await screen.findByText(/no connectors yet/i)
    const empty = emptyStateOf(/no connectors yet/i)
    expect(
      within(empty).getByText(/add a connector to start ingesting/i),
    ).toBeInTheDocument()
    expect(within(empty).queryByRole('button')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all modes' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('No connectors match this mode.'),
    ).not.toBeInTheDocument()
    // Exactly the header Add, enabled by the loaded catalog — as before.
    expect(
      screen.getAllByRole('button', { name: /add connector/i }),
    ).toHaveLength(1)
    expect(screen.getByRole('button', { name: /add connector/i })).toBeEnabled()
  })

  it('keeps the known catalog 501 as the unwired sentence: not empty, not filtered, not red, Add disabled', async () => {
    api.listConnectors.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'connector onboarding is not wired'),
    )
    api.listSources.mockResolvedValue(sourcesTwoOfOneKind.both_running)
    wrap(<ConnectorsTab />)
    expect(
      await screen.findByText(/did not wire connector onboarding/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText('No connectors match this mode.'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all modes' }),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /add connector/i }),
    ).toBeDisabled()
  })

  it('keeps a source-roster failure as the error state: not empty, not filtered, no clear action', async () => {
    api.listSources.mockRejectedValue(
      new ApiError(500, 'internal', 'boom', 'req-42'),
    )
    wrap(<ConnectorsTab />)
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(screen.getByText('Something went wrong')).toBeInTheDocument()
    expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
    expect(
      screen.queryByText('No connectors match this mode.'),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Show all modes' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('combobox', { name: /mode filter/i }),
    ).not.toBeInTheDocument()
  })

  it('adds a connector with an inline credential (PUT seals it into secrets)', async () => {
    api.putConnector.mockResolvedValue({
      name: 'vault-prod',
      action: 'added',
      persisted: true,
      applied: true,
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /add connector/i }),
    )
    const dialog = await screen.findByRole('dialog')

    await user.type(within(dialog).getByLabelText(/^name/i), 'vault-prod')
    await user.type(within(dialog).getByLabelText(/^tenant/i), 'acme')
    // Descriptor-driven fields: a non-secret text field and a masked secret field.
    await user.type(
      within(dialog).getByLabelText(/^base_url/),
      'https://v:8200',
    )
    const token = within(dialog).getByLabelText(/^token/) as HTMLInputElement
    expect(token).toHaveAttribute('type', 'password')
    await user.type(token, 'hvs.SECRET')

    await user.click(
      within(dialog).getByRole('button', { name: /save connector/i }),
    )
    await waitFor(() =>
      expect(api.putConnector).toHaveBeenCalledWith(
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          poll_seconds: 0,
          config: { base_url: 'https://v:8200' },
          secrets: { token: 'hvs.SECRET' },
        },
        {
          signal: expect.any(AbortSignal),
          dispatchGuard: expect.any(Function),
        },
      ),
    )
  })

  it('tests the connection before saving (POST /test)', async () => {
    api.testConnector.mockResolvedValue({ ok: true })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /add connector/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'vault-prod')
    await user.type(within(dialog).getByLabelText(/^tenant/i), 'acme')
    await user.type(
      within(dialog).getByLabelText(/^base_url/),
      'https://v:8200',
    )
    await user.type(within(dialog).getByLabelText(/^token/), 'hvs.SECRET')
    await user.click(
      within(dialog).getByRole('button', { name: /test connection/i }),
    )
    await waitFor(() => expect(api.testConnector).toHaveBeenCalledTimes(1))
  })

  it('a step_up_required on POST /test opens the ceremony, not a generic toast, and does not retry by itself', async () => {
    useStepUpStore.setState({ request: null })
    api.testConnector.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance', 'req-stepup'),
    )
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /add connector/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^name/i), 'vault-prod')
    await user.type(within(dialog).getByLabelText(/^tenant/i), 'acme')
    await user.type(
      within(dialog).getByLabelText(/^base_url/),
      'https://v:8200',
    )
    await user.type(within(dialog).getByLabelText(/^token/), 'hvs.SECRET')
    await user.click(
      within(dialog).getByRole('button', { name: /test connection/i }),
    )
    await waitFor(() => expect(api.testConnector).toHaveBeenCalledTimes(1))
    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(useStepUpStore.getState().request?.action).toBe('console')
    expect(toastMock.error).not.toHaveBeenCalled()
    expect(api.testConnector).toHaveBeenCalledTimes(1)
    useStepUpStore.setState({ request: null })
  })

  it('keeps a stored credential when the secret field is left blank on edit', async () => {
    api.putConnector.mockResolvedValue({
      name: 'vault-prod',
      action: 'rotated',
      persisted: true,
      applied: true,
    })
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          config: {
            base_url: 'https://v:8200',
            token: 'store:source/vault-prod/token',
          },
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(await screen.findByRole('button', { name: /edit/i }))
    const dialog = await screen.findByRole('dialog')
    // The token field is blank on edit (never prefilled with the stored value).
    const token = within(dialog).getByLabelText(/^token/) as HTMLInputElement
    expect(token.value).toBe('')
    await user.click(
      within(dialog).getByRole('button', { name: /save connector/i }),
    )
    await waitFor(() =>
      // A blank secret field is sent as "" — the engine keeps the stored sealed value.
      expect(api.putConnector).toHaveBeenCalledWith(
        expect.objectContaining({ name: 'vault-prod', secrets: { token: '' } }),
        {
          signal: expect.any(AbortSignal),
          dispatchGuard: expect.any(Function),
        },
      ),
    )
  })

  it('deletes a connector after confirmation (DELETE)', async () => {
    api.deleteConnector.mockResolvedValue({
      name: 'vault-prod',
      action: 'removed',
      persisted: true,
      applied: true,
    })
    api.listSources.mockResolvedValue({
      sources: [
        {
          name: 'vault-prod',
          kind: 'vault',
          tenant: 'acme',
          enabled: true,
          status: 'running',
          config: {},
        },
      ],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(await screen.findByRole('button', { name: /delete/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /delete/i }))
    await waitFor(() =>
      expect(api.deleteConnector).toHaveBeenCalledWith('vault-prod'),
    )
  })

  // --- Reload runtime / reconcile source roster --------------------------------

  // Open the reload confirm dialog and confirm it, firing the reconcile.
  async function reload(
    user: ReturnType<typeof userEvent.setup>,
    report: Record<string, unknown>,
  ) {
    api.reloadRuntime.mockResolvedValue(report)
    await user.click(
      await screen.findByRole('button', { name: /reload runtime/i }),
    )
    const confirm = await screen.findByRole('dialog')
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
  }

  it('reloads the runtime and invalidates the sources AND connectors caches', async () => {
    // PINS: reload POSTs /v1/console/runtime/reload and invalidates BOTH
    // consoleKeys.sources() and consoleKeys.connectors() so both views refresh.
    const invalidateSpy = vi.spyOn(QueryClient.prototype, 'invalidateQueries')
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await reload(user, {
      unchanged: 2,
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    await waitFor(() => expect(api.reloadRuntime).toHaveBeenCalledTimes(1))
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ['console', 'sources'],
    })
    expect(invalidateSpy).toHaveBeenCalledWith({
      queryKey: ['console', 'connectors'],
    })
    invalidateSpy.mockRestore()
  })

  it('always surfaces requires_restart as a warning and never a plain success', async () => {
    // PINS honesty (c)+(e): requires_restart is rendered EVERY time (even on an
    // otherwise-clean reconcile), and the only success signal is QUALIFIED with the
    // restart caveat — never an unconditional "reloaded".
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await reload(user, {
      unchanged: 3,
      requires_restart: [
        'HTTP/gRPC listeners and TLS',
        'database DSN, the event bus, and the sealer key',
      ],
    })
    expect(
      await screen.findByText(/a restart is still required/i),
    ).toBeInTheDocument()
    expect(screen.getByText('HTTP/gRPC listeners and TLS')).toBeInTheDocument()
    // No plain success: the success toast carries the restart qualifier.
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledTimes(1))
    expect(toastMock.success).toHaveBeenCalledWith(
      expect.stringMatching(/restart/i),
      undefined,
    )
  })

  it('renders rejected sources per name + reason and suppresses a plain success', async () => {
    // PINS honesty (d): rejected[] is shown per name + reason and the outcome is a
    // qualified PARTIAL — the success toast says "in part", never a clean success.
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await reload(user, {
      removed: ['old-wiki'],
      unchanged: 1,
      rejected: [
        { name: 'vault-prod', reason: 'seal failed: connection refused' },
        { name: 'gh-live', reason: 'unknown kind' },
      ],
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(
      screen.getByText(/seal failed: connection refused/),
    ).toBeInTheDocument()
    expect(screen.getByText('gh-live')).toBeInTheDocument()
    expect(screen.getByText(/unknown kind/)).toBeInTheDocument()
    // Suppress plain success: exactly one, qualified as partial.
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledTimes(1))
    expect(toastMock.success).toHaveBeenCalledWith(
      expect.stringMatching(/in part/i),
      undefined,
    )
  })

  it('never overclaims: the reload copy never says PDP, policy or governance', async () => {
    // PINS honesty (b): this reload covers the source roster + license only; it does
    // NOT reload the access engine. Regression-guard the button, the confirm dialog
    // and the report against policy/PDP/governance wording.
    const forbidden = /\b(pdp|policy|policies|governance)\b/i
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    const reloadBtn = await screen.findByRole('button', {
      name: /reload runtime/i,
    })
    expect(reloadBtn.textContent ?? '').not.toMatch(forbidden)
    api.reloadRuntime.mockResolvedValue({
      unchanged: 1,
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    await user.click(reloadBtn)
    const confirm = await screen.findByRole('dialog')
    expect(confirm.textContent ?? '').not.toMatch(forbidden)
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
    expect(
      await screen.findByText(/runtime reload report/i),
    ).toBeInTheDocument()
    expect(screen.getByRole('dialog').textContent ?? '').not.toMatch(forbidden)
  })

  it('confirms live sources are torn down BEFORE the reload fires', async () => {
    // PINS honesty (a): the disruptive tear-down warning is shown and the POST does
    // NOT fire until the operator confirms.
    api.reloadRuntime.mockResolvedValue({
      unchanged: 1,
      requires_restart: ['HTTP/gRPC listeners and TLS'],
    })
    const user = userEvent.setup()
    wrap(<ConnectorsTab />)
    await user.click(
      await screen.findByRole('button', { name: /reload runtime/i }),
    )
    const confirm = await screen.findByRole('dialog')
    expect(within(confirm).getByText(/torn down live/i)).toBeInTheDocument()
    // Nothing POSTed yet — the confirmation gates the mutation.
    expect(api.reloadRuntime).not.toHaveBeenCalled()
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
    await waitFor(() => expect(api.reloadRuntime).toHaveBeenCalledTimes(1))
  })
})

/**
 * CATALOG AND ROSTER ARE TWO AUTHORITIES, and this suite is the acceptance of that.
 *
 * `GET /v1/console/connectors` (the descriptor catalog) and `GET /v1/console/sources`
 * (the configured roster) are independently superadmin-gated engine-side. Until this
 * lot the tab read them as one: a catalog error that was not 501 was never classified,
 * so `kinds` kept coming out of React Query's retained copy of a catalog the operator
 * had just been refused — minting an enabled Add and descriptor fields from it — while
 * a catalog 501 replaced the ROSTER branch and hid an admitted roster completely.
 *
 * Every case here is one of the failures reproduced by the sources-catalog-authority
 * review on this branch's parent, plus the healthy controls that keep the correction
 * from being "hide more". They assert the DATA, the ACTIONS and the DIALOGS — which
 * rows are listed, whether Add is usable, whether a form still holds a refused
 * payload, and that nothing was written — never merely which notice appeared.
 */
describe('ConnectorsTab — catalog and roster are independent authorities', () => {
  const FORBIDDEN = () => new ApiError(403, 'forbidden', 'refused', 'req-403')
  const STEP_UP = () =>
    new ApiError(403, 'step_up_required', 'assurance', 'req-stepup')
  const SERVER = () => new ApiError(500, 'internal', 'boom', 'req-500')
  const UNWIRED = () => new ApiError(501, 'not_implemented', 'not wired')

  const ROWS = {
    sources: [
      {
        name: 'vault-prod',
        kind: 'vault',
        tenant: 'acme',
        enabled: true,
        status: 'running',
        config: { base_url: 'https://vault.example' },
      },
      {
        name: 'vault-live',
        kind: 'vault',
        tenant: 'acme',
        enabled: true,
        status: 'running',
        source_mode: 'live',
        config: { mode: 'live' },
      },
    ],
  }

  // The real client, not a bare QueryClient: `createQueryClient`'s 30s staleTime and
  // 5min gcTime are exactly what made a restored authority repaint the previous
  // admission without asking anybody, so the lifetime cases have to run against them
  // or they prove nothing. Only `retry` is forced off, so a 5xx case is one failure
  // rather than three.
  function wrapReal(ui: () => ReactElement) {
    const qc = createQueryClient()
    qc.setDefaultOptions({
      queries: {
        ...qc.getDefaultOptions().queries,
        retry: false,
        refetchOnWindowFocus: false,
      },
    })
    // A FRESH element every time, never the same one re-passed: React bails out of
    // re-rendering a subtree whose element is referentially identical, so re-passing
    // one would have silently skipped the authority change these cases turn on.
    const tree = () => (
      <QueryClientProvider client={qc}>{ui()}</QueryClientProvider>
    )
    const view = render(tree())
    return { qc, ...view, again: () => view.rerender(tree()) }
  }

  const addButton = () => screen.getByRole('button', { name: /add connector/i })
  const catalogHeading = () =>
    screen.queryByRole('heading', { name: 'Supported connectors' })
  const rowNames = () =>
    screen
      .queryAllByRole('row')
      .slice(1)
      .map((r) => within(r).getAllByRole('cell')[0]?.textContent)

  function expectNoWrites() {
    expect(api.putConnector).not.toHaveBeenCalled()
    expect(api.testConnector).not.toHaveBeenCalled()
    expect(api.deleteConnector).not.toHaveBeenCalled()
    expect(api.reloadRuntime).not.toHaveBeenCalled()
  }

  function deferred<T>() {
    let resolve!: (v: T) => void
    const promise = new Promise<T>((res) => {
      resolve = res
    })
    return { promise, resolve }
  }

  // Both halves healthy, then ONE of them starts answering `err`.
  async function refuse(
    qc: QueryClient,
    half: 'connectors' | 'sources',
    err: unknown,
  ) {
    const spy = half === 'connectors' ? api.listConnectors : api.listSources
    const before = spy.mock.calls.length
    spy.mockRejectedValue(err)
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ['console', half] })
    })
    await waitFor(() => expect(spy.mock.calls.length).toBeGreaterThan(before))
  }

  beforeEach(() => {
    api.listSources.mockResolvedValue(ROWS)
  })

  // --- The two healthy controls ------------------------------------------------

  it('CONTROL: both halves admitted list their own subject — the roster its rows, the catalog its kinds — with Add usable and nothing refused', async () => {
    wrapReal(() => <ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expect(catalogHeading()).toBeInTheDocument()
    expect(screen.getByText('Vault')).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expect(screen.getAllByRole('button', { name: /^edit$/i })).toHaveLength(2)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText(/not authorized/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/step-up ceremony/)).not.toBeInTheDocument()
    expectNoWrites()
  })

  // --- A refused CATALOG retires the catalog, and only the catalog ---------------

  it.each([
    ['forbidden', FORBIDDEN, 'Not authorized to view the connector catalog'],
    ['a 5xx', SERVER, 'The connector catalog could not be loaded'],
  ])(
    'a catalog refused with %s retires its kinds, its Add and its descriptor form, and leaves the admitted roster listed',
    async (_label, makeError, notice) => {
      const user = userEvent.setup()
      const { qc } = wrapReal(() => <ConnectorsTab />)
      expect(await screen.findByText('vault-prod')).toBeInTheDocument()
      // The catalog was admitted first: these are the kinds a retained copy would
      // have gone on offering.
      expect(addButton()).toBeEnabled()
      await user.click(addButton())
      const form = await screen.findByRole('dialog')
      expect(within(form).getByLabelText(/^base_url/)).toBeInTheDocument()

      await refuse(qc, 'connectors', makeError())

      // The catalog half, and everything rendered FROM it, is gone.
      expect(await screen.findByText(notice)).toBeInTheDocument()
      expect(catalogHeading()).toBeNull()
      expect(screen.queryByText('Vault')).not.toBeInTheDocument()
      expect(addButton()).toBeDisabled()
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(screen.queryByLabelText(/^base_url/)).not.toBeInTheDocument()
      // The roster is a different authority and was not refused: its rows, and the
      // actions those rows carry, stay exactly as they were.
      expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
      expect(screen.getAllByRole('button', { name: /^edit$/i })).toHaveLength(2)
      expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
      expectNoWrites()
    },
  )

  it('a forbidden catalog is calm, never the red failure state, and a catalog 5xx retries only its own half', async () => {
    const user = userEvent.setup()
    const { qc } = wrapReal(() => <ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()

    await refuse(qc, 'connectors', FORBIDDEN())
    // A permission boundary is not a breakage: no alert, and no retry offered for a
    // refusal that retrying cannot fix.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /retry/i }),
    ).not.toBeInTheDocument()

    await refuse(qc, 'connectors', SERVER())
    const alert = await screen.findByRole('alert')
    expect(
      within(alert).getByText('The connector catalog could not be loaded'),
    ).toBeInTheDocument()
    // The retry belongs to the catalog half: it re-issues THAT read, and when it
    // succeeds the catalog comes back on its own without the roster being re-read.
    const rosterReads = api.listSources.mock.calls.length
    api.listConnectors.mockResolvedValue(catalog)
    await user.click(within(alert).getByRole('button', { name: /retry/i }))
    expect(await screen.findByText('Vault')).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expect(api.listSources.mock.calls.length).toBe(rosterReads)
    expectNoWrites()
  })

  it('a catalog awaiting step-up asks for the ceremony, keeps the roster listed and replays no write', async () => {
    const { qc } = wrapReal(() => <ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    await refuse(qc, 'connectors', STEP_UP())

    expect(
      await screen.findByText('step-up ceremony:generic'),
    ).toBeInTheDocument()
    // Assurance is read BEFORE role: a step-up must not be reported as a permission
    // the operator has to go and ask for.
    expect(
      screen.queryByText('Not authorized to view the connector catalog'),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expect(addButton()).toBeDisabled()
    expectNoWrites()
  })

  // --- A refused ROSTER retires the roster, and only the roster -----------------

  it.each([
    [
      'forbidden',
      FORBIDDEN,
      'Not authorized to view the configured connectors',
    ],
    ['a step-up', STEP_UP, 'step-up ceremony:generic'],
  ])(
    'a roster refused with %s retires its rows, their actions and their open dialog, and leaves the catalog and Add',
    async (_label, makeError, notice) => {
      const user = userEvent.setup()
      const { qc } = wrapReal(() => <ConnectorsTab />)
      await user.click(
        within(
          (await screen.findByText('vault-prod')).closest('tr') as HTMLElement,
        ).getByRole('button', { name: /^edit$/i }),
      )
      expect(
        within(await screen.findByRole('dialog')).getByDisplayValue(
          'vault-prod',
        ),
      ).toBeInTheDocument()

      await refuse(qc, 'sources', makeError())

      // The rows and everything they backed are gone, including the payload the
      // open dialog had already been filled with.
      expect(await screen.findByText(notice)).toBeInTheDocument()
      expect(rowNames()).toEqual([])
      expect(screen.queryByText('vault-prod')).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: /^edit$/i }),
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('button', { name: /^delete$/i }),
      ).not.toBeInTheDocument()
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(screen.queryByDisplayValue('vault-prod')).not.toBeInTheDocument()
      // Neither of those is a red failure, and neither says anything about the
      // catalog: its kinds and its Add are a different authority's answer.
      expect(screen.queryByRole('alert')).not.toBeInTheDocument()
      expect(catalogHeading()).toBeInTheDocument()
      expect(screen.getByText('Vault')).toBeInTheDocument()
      expect(addButton()).toBeEnabled()
      expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
      expectNoWrites()
    },
  )

  it('a roster 5xx replaces the roster with a retry that restores it, and never revokes the catalog', async () => {
    const user = userEvent.setup()
    const { qc } = wrapReal(() => <ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    await refuse(qc, 'sources', SERVER())

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('Something went wrong')).toBeInTheDocument()
    expect(rowNames()).toEqual([])
    expect(catalogHeading()).toBeInTheDocument()
    expect(addButton()).toBeEnabled()

    const catalogReads = api.listConnectors.mock.calls.length
    api.listSources.mockResolvedValue(ROWS)
    await user.click(within(alert).getByRole('button', { name: /retry/i }))
    await waitFor(() =>
      expect(rowNames()).toEqual(['vault-prod', 'vault-live']),
    )
    expect(api.listConnectors.mock.calls.length).toBe(catalogReads)
    expectNoWrites()
  })

  // --- 501 is per reader, and neither 501 nor a pending read hides a healthy half -

  it('a catalog 501 keeps its own unwired sentence and does NOT hide an admitted roster', async () => {
    api.listConnectors.mockRejectedValue(UNWIRED())
    wrapReal(() => <ConnectorsTab />)
    expect(
      await screen.findByText(/did not wire connector onboarding/i),
    ).toBeInTheDocument()
    // The failure this lot corrects: `unavailable` replaced the roster branch, so
    // two admitted rows were painted nowhere.
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expect(screen.getAllByRole('button', { name: /^edit$/i })).toHaveLength(2)
    expect(addButton()).toBeDisabled()
    expect(
      screen.getByRole('button', { name: /reload runtime/i }),
    ).toBeDisabled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(catalogHeading()).toBeNull()
    expectNoWrites()
  })

  it('a roster 501 is its OWN sentence, not the catalog’s, and leaves the catalog and Add', async () => {
    api.listSources.mockRejectedValue(UNWIRED())
    wrapReal(() => <ConnectorsTab />)
    // A build with no source roster service is a different fact from a build that
    // did not wire connector ONBOARDING; saying the second when the first happened
    // sends the operator to the boot file for a catalog that is working.
    expect(
      await screen.findByText(/does not serve the source roster/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/did not wire connector onboarding/i),
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
    expect(catalogHeading()).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expectNoWrites()
  })

  it('a half still loading does not hold the half that already answered', async () => {
    const slowCatalog = deferred<typeof catalog>()
    api.listConnectors.mockReturnValue(slowCatalog.promise)
    wrapReal(() => <ConnectorsTab />)

    // The roster answered; the catalog has not. The rows are painted anyway.
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expect(screen.getAllByRole('status', { name: /loading/i })).toHaveLength(1)
    expect(screen.queryByText(/no connectors yet/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    // Add stays disabled while the catalog is unanswered: an Add with no kinds is
    // an empty form, and a pending read is not an admission.
    expect(addButton()).toBeDisabled()

    await act(async () => {
      slowCatalog.resolve(catalog)
    })
    expect(await screen.findByText('Vault')).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expectNoWrites()
  })

  it('a pending ROSTER does not hide an admitted catalog', async () => {
    const slowRoster = deferred<typeof ROWS>()
    api.listSources.mockReturnValue(slowRoster.promise)
    wrapReal(() => <ConnectorsTab />)

    expect(await screen.findByText('Vault')).toBeInTheDocument()
    expect(catalogHeading()).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expect(screen.getAllByRole('status', { name: /loading/i })).toHaveLength(1)

    await act(async () => {
      slowRoster.resolve(ROWS)
    })
    await waitFor(() =>
      expect(rowNames()).toEqual(['vault-prod', 'vault-live']),
    )
    expectNoWrites()
  })

  // --- No resurrection ----------------------------------------------------------

  it('a refusal followed by a DIFFERENT failure never brings the last success back — only a new successful read does', async () => {
    const { qc } = wrapReal(() => <ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()

    await refuse(qc, 'connectors', FORBIDDEN())
    await refuse(qc, 'sources', FORBIDDEN())
    expect(screen.queryByText('Vault')).not.toBeInTheDocument()
    expect(rowNames()).toEqual([])

    // 403 → 500 on both halves. The cached positives are still sitting in the query
    // cache; a second failure must not be read as "the last thing that worked".
    await refuse(qc, 'connectors', SERVER())
    await refuse(qc, 'sources', SERVER())
    expect(screen.queryByText('Vault')).not.toBeInTheDocument()
    expect(screen.queryByText('vault-prod')).not.toBeInTheDocument()
    expect(addButton()).toBeDisabled()

    // A NEW successful read is the only thing that re-admits either half.
    api.listConnectors.mockResolvedValue(catalog)
    api.listSources.mockResolvedValue(ROWS)
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ['console'] })
    })
    await waitFor(() =>
      expect(rowNames()).toEqual(['vault-prod', 'vault-live']),
    )
    expect(screen.getByText('Vault')).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expectNoWrites()
  })

  it('a dialog retired by a refusal does not re-open when the read succeeds again', async () => {
    const user = userEvent.setup()
    const { qc } = wrapReal(() => <ConnectorsTab />)
    await user.click(
      within(
        (await screen.findByText('vault-prod')).closest('tr') as HTMLElement,
      ).getByRole('button', { name: /^delete$/i }),
    )
    expect(await screen.findByRole('dialog')).toBeInTheDocument()

    await refuse(qc, 'sources', FORBIDDEN())
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    // The roster comes back. The confirmation the operator opened under the PREVIOUS
    // admission is over: it does not reappear, and nothing was deleted.
    api.listSources.mockResolvedValue(ROWS)
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ['console', 'sources'] })
    })
    await waitFor(() =>
      expect(rowNames()).toEqual(['vault-prod', 'vault-live']),
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expectNoWrites()
  })

  // --- The lifetime -------------------------------------------------------------

  it.each([
    ['the principal', () => (authState.principal = { user_id: 'u2', aal: 3 })],
    ['the tenant', () => (authState.activeTenant = 't2')],
    [
      'the credential generation',
      () => useSessionStore.setState({ credentialGeneration: 1 }),
    ],
  ])(
    'a change of %s ends the lifetime: the tab re-reads under the new authority instead of repainting the previous admission',
    async (_label, move) => {
      const user = userEvent.setup()
      const view = wrapReal(() => <ConnectorsTab />)
      await user.click(
        within(
          (await screen.findByText('vault-prod')).closest('tr') as HTMLElement,
        ).getByRole('button', { name: /^edit$/i }),
      )
      expect(await screen.findByRole('dialog')).toBeInTheDocument()
      const catalogReads = api.listConnectors.mock.calls.length
      const rosterReads = api.listSources.mock.calls.length

      act(() => {
        move()
      })
      view.again()

      // Both reads are re-issued under the new authority. Nothing here is inferred
      // from the data being tenant-scoped — it is not; what is not carried over is
      // the ADMISSION, and the selection made under it.
      await waitFor(() =>
        expect(api.listConnectors.mock.calls.length).toBeGreaterThan(
          catalogReads,
        ),
      )
      await waitFor(() =>
        expect(api.listSources.mock.calls.length).toBeGreaterThan(rosterReads),
      )
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(screen.queryByDisplayValue('vault-prod')).not.toBeInTheDocument()
      await waitFor(() =>
        expect(rowNames()).toEqual(['vault-prod', 'vault-live']),
      )
      expectNoWrites()
    },
  )

  it('losing superadmin ends the reads and the intent; regaining it obtains a NEW admission rather than repainting the old one', async () => {
    const user = userEvent.setup()
    const view = wrapReal(() => <ConnectorsTab />)
    await user.click(
      within(
        (await screen.findByText('vault-prod')).closest('tr') as HTMLElement,
      ).getByRole('button', { name: /^edit$/i }),
    )
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    const catalogReads = api.listConnectors.mock.calls.length
    const rosterReads = api.listSources.mock.calls.length

    authState.isSuperadmin = false
    view.again()
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    expect(screen.queryByText('vault-prod')).not.toBeInTheDocument()
    expect(catalogHeading()).toBeNull()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    authState.isSuperadmin = true
    view.again()
    // Both entries were cancelled and removed when the role left, so this is a real
    // request and not the 30s-fresh copy the previous authority obtained.
    await waitFor(() =>
      expect(api.listConnectors.mock.calls.length).toBeGreaterThan(
        catalogReads,
      ),
    )
    await waitFor(() =>
      expect(api.listSources.mock.calls.length).toBeGreaterThan(rosterReads),
    )
    await waitFor(() =>
      expect(rowNames()).toEqual(['vault-prod', 'vault-live']),
    )
    // The row the operator had opened is listed again; the DIALOG is not.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expectNoWrites()
  })

  it('a read still in flight when the lifetime ends is ABORTED, and its late answer repaints nothing', async () => {
    const slowRoster = deferred<typeof ROWS>()
    let signal: AbortSignal | undefined
    api.listSources.mockImplementation((opts?: { signal?: AbortSignal }) => {
      signal = opts?.signal
      return slowRoster.promise
    })
    const view = wrapReal(() => <ConnectorsTab />)
    await waitFor(() => expect(api.listSources).toHaveBeenCalled())
    // The tab hands the query's signal to the client, which is why ending a lifetime
    // can stop the request instead of only ignoring what it returns.
    expect(signal).toBeInstanceOf(AbortSignal)
    expect(signal?.aborted).toBe(false)

    authState.isSuperadmin = false
    view.again()
    expect(await screen.findByText(/only a superadmin/i)).toBeInTheDocument()
    await waitFor(() => expect(signal?.aborted).toBe(true))

    // The abandoned read answers late, into a context that is no longer its own.
    api.listSources.mockResolvedValue({
      sources: [{ ...ROWS.sources[0], name: 'stale-row' }],
    })
    await act(async () => {
      slowRoster.resolve(ROWS)
    })
    expect(screen.queryByText('vault-prod')).not.toBeInTheDocument()
    expect(screen.queryByText('stale-row')).not.toBeInTheDocument()
    expectNoWrites()
  })

  it('a reload dispatched under one authority does not open its report in the next one', async () => {
    const landed = deferred<Record<string, unknown>>()
    api.reloadRuntime.mockReturnValue(landed.promise)
    const user = userEvent.setup()
    const view = wrapReal(() => <ConnectorsTab />)
    await screen.findByText('vault-prod')
    await user.click(screen.getByRole('button', { name: /reload runtime/i }))
    const confirm = await screen.findByRole('dialog')
    await user.click(
      within(confirm).getByRole('button', { name: /reload runtime/i }),
    )
    await waitFor(() => expect(api.reloadRuntime).toHaveBeenCalledTimes(1))

    // The operator's authority moves while the reconcile is still running. The AAL3
    // ceremony that authorized the write is untouched and the engine still answers
    // it — what must not happen is the REPORT of that answer opening over a context
    // it was never dispatched in.
    act(() => {
      authState.activeTenant = 't2'
    })
    view.again()
    await act(async () => {
      landed.resolve({
        added: ['vault-prod'],
        requires_restart: ['HTTP/gRPC listeners and TLS'],
      })
    })
    expect(screen.queryByText(/reconcile|report/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(api.reloadRuntime).toHaveBeenCalledTimes(1)
  })

  it('the SHARED flat key still invalidates this tab’s scoped reads', async () => {
    // The two reads carry a tab-local lifetime suffix, so this is the assertion that
    // the suffix did not break the prefix every mutation here — and both other
    // consumers of `consoleKeys.sources()` — invalidate by.
    const { qc } = wrapReal(() => <ConnectorsTab />)
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    const reads = api.listSources.mock.calls.length

    api.listSources.mockResolvedValue({
      sources: [{ ...ROWS.sources[0], name: 'vault-reconciled' }],
    })
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ['console', 'sources'] })
    })
    await waitFor(() =>
      expect(api.listSources.mock.calls.length).toBeGreaterThan(reads),
    )
    await waitFor(() => expect(rowNames()).toEqual(['vault-reconciled']))
    expectNoWrites()
  })

  // --- SCA-L1: the owner's exit retires the owner's reads ------------------------

  /**
   * ONE CLIENT ACROSS THE OWNER'S EXIT, which is the whole point of these cases and
   * the shape the independent review reproduced: Sources leaves the screen while
   * everything around it — the provider, the stores, the CACHE — stays. A helper that
   * built a fresh client per render could never see an owner inherit the previous
   * owner's entry, because there would be nothing to inherit.
   */
  function ownerHost() {
    const qc = createQueryClient()
    qc.setDefaultOptions({
      queries: {
        ...qc.getDefaultOptions().queries,
        retry: false,
        refetchOnWindowFocus: false,
      },
    })
    const shell = (mounted: boolean) => (
      <QueryClientProvider client={qc}>
        {mounted ? <ConnectorsTab /> : <div data-testid="tab-absent" />}
      </QueryClientProvider>
    )
    const view = render(shell(true))
    return {
      qc,
      /** The tab leaves the screen; the client it read into does not. */
      exit: () => view.rerender(shell(false)),
      /** A NEW owner arrives against that same client. */
      enter: () => view.rerender(shell(true)),
      unmount: view.unmount,
    }
  }

  /**
   * The independent review's residual, and the only thing this correction changes.
   *
   * Retirement used to hang off an effect that watched `scope` for a REPLACEMENT it
   * could observe. Unmounting the tab is not a replacement, so nothing was retired:
   * the entries stayed for `gcTime`, and the catalog's request — which consumes no
   * signal and whose HTTP cannot be aborted — went on to finish into the cache. A
   * role withdrawal that happened while the tab was absent was never observed either,
   * and because `useAuthBoundary().epoch` is memoized per distinct tuple, true →
   * false → true handed the next mount back the SAME scope string. So the next mount
   * attached to the previous owner's pending request and painted its answer as a
   * current admission, with the catalog invocation count still at one.
   */
  it('an owner that exits with a pending catalog leaves nothing for the next one: a true\u2192false\u2192true role round trip cannot paint the OLD answer or enable Add', async () => {
    const stalled = deferred<typeof catalog>()
    api.listConnectors.mockReturnValueOnce(stalled.promise)
    api.listConnectors.mockResolvedValue({
      connectors: [{ ...catalog.connectors[0], title: 'CURRENT-CATALOG' }],
    })

    const host = ownerHost()
    // The owner is alive, its roster answered, its catalog is still in flight.
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(addButton()).toBeDisabled()
    expect(api.listConnectors).toHaveBeenCalledTimes(1)

    // The tab leaves the screen. Not a scope change: the whole owner exits, and the
    // client it was reading into stays exactly where it was.
    host.exit()
    expect(screen.getByTestId('tab-absent')).toBeInTheDocument()

    // The role is withdrawn and restored while nothing of this tab is mounted, so no
    // effect of its can observe either transition. `useAuthBoundary` gives the
    // restored role the same epoch it gave the original one — the reusable tuple that
    // made the scope suffix alone insufficient.
    authState.isSuperadmin = false
    authState.isSuperadmin = true

    // The abandoned request finishes. Its transport could not be aborted; what was
    // cancelled and removed is its QUERY, so it has no entry to land in.
    await act(async () => {
      stalled.resolve({
        connectors: [
          { ...catalog.connectors[0], title: 'OLD-PENDING-CATALOG' },
        ],
      })
    })

    // A new owner arrives against that same client, and reads for itself.
    host.enter()
    expect(await screen.findByText('CURRENT-CATALOG')).toBeInTheDocument()
    expect(screen.queryByText('OLD-PENDING-CATALOG')).not.toBeInTheDocument()
    expect(api.listConnectors.mock.calls.length).toBeGreaterThan(1)
    expect(addButton()).toBeEnabled()
    // The healthy half is a new read too, and it is intact.
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expectNoWrites()
    host.unmount()
  })

  it('the exiting owner cancels and removes its OWN two entries, and nobody else\u2019s', async () => {
    const host = ownerHost()
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    // A neighbour on the shared flat key — what workspace-connectors-tab and
    // onboarding-view use — and an unrelated console entry.
    host.qc.setQueryData(['console', 'sources'], ROWS)
    host.qc.setQueryData(['console', 'secrets'], { secrets: [] })
    const owned = () =>
      host.qc
        .getQueryCache()
        .findAll({ queryKey: ['console'] })
        .filter((q) => typeof q.queryKey[2] === 'object')
    expect(owned()).toHaveLength(2)

    host.unmount()

    // The owner's own two suffixed entries are gone; the flat and unrelated ones the
    // tab does not own are untouched.
    expect(owned()).toHaveLength(0)
    expect(host.qc.getQueryData(['console', 'sources'])).toEqual(ROWS)
    expect(host.qc.getQueryData(['console', 'secrets'])).toEqual({
      secrets: [],
    })
    expectNoWrites()
  })

  it('an ordinary remount reads again rather than inheriting the previous owner\u2019s success', async () => {
    const host = ownerHost()
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(screen.getByText('Vault')).toBeInTheDocument()
    const catalogReads = api.listConnectors.mock.calls.length
    const rosterReads = api.listSources.mock.calls.length

    host.exit()
    // Well inside `createQueryClient`'s 30s freshness and 5min retention, which is
    // exactly the window in which the previous owner's answer used to be re-shown to
    // the next one with no read of its own.
    host.enter()

    await waitFor(() =>
      expect(api.listConnectors.mock.calls.length).toBeGreaterThan(
        catalogReads,
      ),
    )
    await waitFor(() =>
      expect(api.listSources.mock.calls.length).toBeGreaterThan(rosterReads),
    )
    // And the new owner ends up whole: both halves, the actions, no stale notice.
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expect(catalogHeading()).toBeInTheDocument()
    expect(addButton()).toBeEnabled()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expectNoWrites()
    host.unmount()
  })

  it('under StrictMode the double-invoked cleanup does not strand the owner: both halves answer and Add is usable', async () => {
    // React's development double-invoke runs this owner's cleanup while it is still
    // mounted. Retirement must not leave it attached to entries it has just retired —
    // the extra read that costs is a development-only cost, and the end state is a
    // whole tab, not a stranded one.
    const view = wrapReal(() => (
      <StrictMode>
        <ConnectorsTab />
      </StrictMode>
    ))
    expect(await screen.findByText('vault-prod')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('Vault')).toBeInTheDocument())
    expect(rowNames()).toEqual(['vault-prod', 'vault-live'])
    expect(addButton()).toBeEnabled()
    expect(screen.getAllByRole('button', { name: /^edit$/i })).toHaveLength(2)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.queryByRole('status', { name: /loading/i })).toBeNull()
    expectNoWrites()
    view.unmount()
  })
})
