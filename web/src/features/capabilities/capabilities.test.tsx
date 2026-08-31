// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import type { ConfigDTO, ServerDetailDTO, ToolPinDTO } from './types'

// --- mocks -------------------------------------------------------------------

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
  warning: vi.fn(),
}))
vi.mock('@/components/ui/toaster', () => ({ toast, Toaster: () => null }))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listServers: vi.fn(),
  getServer: vi.fn(),
  listTools: vi.fn(),
  listToolPins: vi.fn(),
  sendToolPinIntent: vi.fn(),
  listSkills: vi.fn(),
  wiring: vi.fn(),
  listConfigs: vi.fn(),
  getConfig: vi.fn(),
  createConfig: vi.fn(),
  updateConfig: vi.fn(),
  deleteConfig: vi.fn(),
  listRevisions: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, capabilitiesApi: api }
})

import CapabilitiesView from './capabilities-view'
import { ConfigEditorDialog } from './config-editor'
import { ServerDetailSheet } from './server-detail'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const rendered = render(
    <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
  )
  return { ...rendered, qc }
}

const configFixture: ConfigDTO = {
  id: 'c1',
  server_ref: 'github',
  transport: 'stdio',
  endpoint: 'npx -y @modelcontextprotocol/server-github',
  scope: 'team-a',
  secret_refs: [
    { name: 'GITHUB_TOKEN', ref_kind: 'env', ref: '$GITHUB_TOKEN' },
  ],
  enabled: true,
  revision: 2,
}

const detailFixture: ServerDetailDTO = {
  id: 's1',
  name: 'github',
  transport: 'stdio',
  status: 'active',
  connection: 'connected',
  tool_count: 2,
  has_config: true,
  config_revision: 2,
  config: configFixture,
  health: null,
  tools: [
    {
      id: 't1',
      name: 'delete_repo',
      read_only_hint: false,
      destructive_hint: true,
      annotation_trust: 'untrusted',
    },
  ],
  skills: [],
  resources: [],
  consumers: [],
}

const toolPinFixture: ToolPinDTO = {
  tool: 'github.search',
  fingerprint: 'sha256:pinned-fingerprint-1234567890',
  pinned_at: '2026-07-20T09:00:00Z',
  updated_at: '2026-07-20T10:00:00Z',
  pin_count: 3,
  version: 7,
  drift_fingerprint: 'sha256:drifted-fingerprint-0987654321',
  drift_at: '2026-07-20T10:00:00Z',
}

/** A v4 UUID, so an assertion about "there is a key" cannot pass for `undefined`. */
const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** The 202 the engine returns (toolpins.go:224-228). */
const acceptedResult = {
  tool: toolPinFixture.tool,
  operation_id: 'op-1',
  apply_state: 'applied',
  version: 8,
  evidence_ref: 'ref-1',
}

beforeEach(() => {
  authState.can = () => true
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  api.listServers.mockResolvedValue({ items: [], has_more: false })
  api.listToolPins.mockResolvedValue({ items: [] })
})
afterEach(() => vi.clearAllMocks())

describe('CapabilitiesView — servers catalog', () => {
  it('lists MCP servers with their managed marker and connection state', async () => {
    api.listServers.mockResolvedValue({
      items: [
        {
          id: 's1',
          name: 'github',
          transport: 'stdio',
          status: 'active',
          connection: 'connected',
          tool_count: 3,
          has_config: true,
          config_revision: 2,
        },
      ],
      has_more: false,
    })
    wrap(<CapabilitiesView />)
    expect(await screen.findByText('github')).toBeInTheDocument()
    expect(screen.getByText(/Managed · rev 2/)).toBeInTheDocument()
    expect(screen.getByText('Connected')).toBeInTheDocument()
  })
})

describe('CapabilitiesView — el 403 de ceremonia en la pestaña de wiring', () => {
  it('ofrece la ceremonia, no la acusación, cuando el grafo se niega por ASEGURAMIENTO', async () => {
    // ⛔ Los dos 403 satisfacen `isForbidden` (lib/api/errors.ts:59 es sólo el status).
    // Leyéndolo primero, 560 px de grafo se sustituían por un escudo tachado SIN
    // reintento ni mención de la ceremonia, sobre un permiso que el operador SÍ tiene.
    useStepUpStore.setState({ request: null })
    api.wiring.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)
    await user.click(await screen.findByRole('tab', { name: /wiring/i }))

    // Ancla POSITIVA: la ceremonia aparece. Sin ella, la ausencia de abajo se
    // cumpliría en el primer tick y la celda pasaría con el defecto puesto.
    expect(
      await screen.findByText(/step-up|verification|verificación/i),
    ).toBeInTheDocument()
  })

  it('conserva la negativa de ROL cuando el 403 no trae código de ceremonia', async () => {
    // Control negativo del anterior: sin código, sigue siendo una negativa de rol
    // legítima y se pinta como tal, sin ofrecer elevación.
    useStepUpStore.setState({ request: null })
    api.wiring.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)
    await user.click(await screen.findByRole('tab', { name: /wiring/i }))

    // ⛔ ANCLA POSITIVA, Y NO ES ADORNO: esta celda era SÓLO ausencias, y el contraste
    // `sol max` mutó la rama de rol de `<ForbiddenState />` a `<ErrorState />` y la celda
    // siguió VERDE — dos ausencias se cumplen igual pinte lo que pinte. La distinción es
    // el rol ARIA: una frontera de permiso es `role="status"` (calma), una avería es
    // `role="alert"` (error-state.tsx:41 vs :99). Afirmo la que DEBE estar y niego la otra.
    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await waitFor(() =>
      expect(
        screen.queryByText(/step-up|verificación/i),
      ).not.toBeInTheDocument(),
    )
    expect(useStepUpStore.getState().request).toBeNull()
  })
})

describe('CapabilitiesView — tool pins', () => {
  it('renders current pins and highlights a pending fingerprint drift', async () => {
    api.listToolPins.mockResolvedValue({ items: [toolPinFixture] })
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))

    expect(await screen.findByText('Pending drifts')).toBeInTheDocument()
    expect(screen.getAllByText('github.search').length).toBeGreaterThan(0)
    expect(
      screen.getAllByTitle(toolPinFixture.fingerprint).length,
    ).toBeGreaterThan(0)
    expect(
      screen.getByTitle(toolPinFixture.drift_fingerprint!),
    ).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('approves the current drift through confirmation and invalidates the tenant query', async () => {
    api.listToolPins.mockResolvedValue({ items: [toolPinFixture] })
    api.sendToolPinIntent.mockResolvedValue(acceptedResult)
    const user = userEvent.setup()
    const { qc } = wrap(<CapabilitiesView />)
    const invalidate = vi.spyOn(qc, 'invalidateQueries')

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /approve drift for github\.search/i,
      }),
    )

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/changes the tool authorization baseline/i),
    ).toBeInTheDocument()
    await user.click(
      within(dialog).getByRole('button', { name: /^approve drift$/i }),
    )

    // The FULL intent, not just `{tool, from_drift}`. That shorter assertion is what
    // this file used to make, and it passed for a payload the engine answers 400 to
    // (toolpins_evidence_test.go:166-193): a mocked mutationFn cannot refuse anything,
    // so the only protection is naming every field the contract requires.
    await waitFor(() => expect(api.sendToolPinIntent).toHaveBeenCalledTimes(1))
    // `.mock.calls[0][0]`, not toHaveBeenCalledWith: react-query hands mutationFn a
    // second (context) argument, so a whole-arguments matcher fails for the wrong reason.
    expect(api.sendToolPinIntent.mock.calls[0][0]).toEqual({
      key: expect.stringMatching(UUID_RE),
      kind: 'approve',
      body: {
        tool: 'github.search',
        from_drift: true,
        expected_version: toolPinFixture.version,
        expected_drift_fingerprint: toolPinFixture.drift_fingerprint,
      },
    })
    await waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith({
        queryKey: ['capabilities', 't1', 'toolpins'],
      }),
    )
  })

  it('shows the divergence instead of a red error when the state moved (409)', async () => {
    // The tool really moved between the read and the write: the refetch must return the
    // NEW state, or "show the divergence" would be asserting against two identical
    // halves and would pass for a panel that just echoes what the operator submitted.
    api.listToolPins.mockResolvedValueOnce({ items: [toolPinFixture] })
    api.listToolPins.mockResolvedValue({
      items: [
        {
          ...toolPinFixture,
          version: 9,
          drift_fingerprint: 'sha256:drifted-again-5555555555',
        },
      ],
    })
    api.sendToolPinIntent.mockRejectedValue(
      new ApiError(
        409,
        'pin_version_conflict',
        'pin state changed since your read',
      ),
    )
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /approve drift for github\.search/i,
      }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /^approve drift$/i,
      }),
    )

    // Re-read and SHOW the divergence — never a silent resend with the fresh value.
    const alert = await screen.findByRole('alert')
    // BOTH sides, and they must differ: what the operator reviewed, and what is there now.
    await waitFor(() =>
      expect(within(alert).getByText(/base version 9/i)).toBeInTheDocument(),
    )
    expect(within(alert).getByText(/base version 7/i)).toBeInTheDocument()
    // The pre-conflict cache also said version 7. If "current" were rendered from it, the
    // panel would have shown 7 on BOTH sides — reviewed and current — which is the stale
    // half of the same lie the reviewed side was fixed for. Only 9 may be labelled current.
    expect(
      within(alert).queryByText(/current \(base version 7\)/i),
    ).not.toBeInTheDocument()
    expect(
      within(alert).getByTitle('sha256:drifted-again-5555555555'),
    ).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
    expect(api.sendToolPinIntent).toHaveBeenCalledTimes(1)
    // The stale review must be gone: re-confirming it would apply a decision taken
    // against a fingerprint that is no longer what the tool serves.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('requires a danger confirmation before unpinning', async () => {
    api.listToolPins.mockResolvedValue({ items: [toolPinFixture] })
    api.sendToolPinIntent.mockResolvedValue(acceptedResult)
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /unpin github\.search/i,
      }),
    )
    expect(api.sendToolPinIntent).not.toHaveBeenCalled()

    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: /^unpin$/i })
    expect(confirm).toHaveClass('bg-danger-solid')
    await user.click(confirm)

    await waitFor(() => expect(api.sendToolPinIntent).toHaveBeenCalledTimes(1))
    expect(api.sendToolPinIntent.mock.calls[0][0]).toEqual({
      key: expect.stringMatching(UUID_RE),
      kind: 'unpin',
      body: { tool: 'github.search', expected_version: toolPinFixture.version },
    })
  })

  it('keeps a rebound idempotency key LOUD instead of calling it a divergence', async () => {
    // Same status as the test above, different code. A key reused for a DIFFERENT effect
    // is a replay or a client bug — presenting it as "somebody else moved the state",
    // refetching and hiding the message would reassure the operator about the one 409
    // that deserves attention.
    api.listToolPins.mockResolvedValue({ items: [toolPinFixture] })
    api.sendToolPinIntent.mockRejectedValue(
      new ApiError(
        409,
        'idempotency_key_reused',
        'idempotency key reused for a different change',
      ),
    )
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /approve drift for github\.search/i,
      }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /^approve drift$/i,
      }),
    )

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a calm enterprise state when the verifier is not wired (501)', async () => {
    api.listToolPins.mockRejectedValue(
      new ApiError(501, 'not_implemented', 'no verifier wired'),
    )
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))

    expect(
      await screen.findByText(/enterprise capability/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/server error/i)).not.toBeInTheDocument()
  })

  it('hides approve and unpin actions without config write permission', async () => {
    authState.can = (permission) => permission !== 'capabilities:config:write'
    api.listToolPins.mockResolvedValue({ items: [toolPinFixture] })
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))
    expect(await screen.findByText('Pending drifts')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /approve drift for/i }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /unpin github\.search/i }),
    ).not.toBeInTheDocument()
  })

  it('hides the tool pins tab entirely without config read permission', () => {
    // The route only needs catalog:read; the pin set is config-read tier
    // (mirrors the Managed configs tab) — the tab must not render at all.
    authState.can = (permission) =>
      permission !== 'capabilities:config:read' &&
      permission !== 'capabilities:config:write'
    wrap(<CapabilitiesView />)
    expect(
      screen.queryByRole('tab', { name: /tool pins/i }),
    ).not.toBeInTheDocument()
  })

  it('surfaces a forbidden action as a calm warning toast', async () => {
    api.listToolPins.mockResolvedValue({ items: [toolPinFixture] })
    api.sendToolPinIntent.mockRejectedValue(
      new ApiError(403, 'forbidden', 'permission changed'),
    )
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    await user.click(screen.getByRole('tab', { name: /tool pins/i }))
    await user.click(
      await screen.findByRole('button', {
        name: /approve drift for github\.search/i,
      }),
    )
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', {
        name: /^approve drift$/i,
      }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalled())
    expect(toast.error).not.toHaveBeenCalled()
  })
})

describe('ConfigEditorDialog — secrets are references, never values', () => {
  it('offers NO secret-value input, shows the audit notice, gates on a server ref', async () => {
    wrap(<ConfigEditorDialog open onOpenChange={() => {}} />)
    expect(screen.getByText(/tamper-evident audit ledger/i)).toBeInTheDocument()

    const create = screen.getByRole('button', { name: /create config/i })
    expect(create).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/server reference/i), 'github')
    expect(create).toBeEnabled()

    await userEvent.click(
      screen.getByRole('button', { name: /add reference/i }),
    )
    // The secret-ref editor collects name / locator / hint — never a raw value.
    expect(screen.queryByLabelText(/secret value/i)).toBeNull()
    expect(screen.getByLabelText(/^name$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/locator/i)).toBeInTheDocument()
  })

  it('warns when the endpoint looks like an embedded credential and blocks save', async () => {
    wrap(<ConfigEditorDialog open onOpenChange={() => {}} serverRef="github" />)
    const create = screen.getByRole('button', { name: /create config/i })
    expect(create).toBeEnabled()
    await userEvent.type(
      screen.getByLabelText(/^endpoint$/i),
      'https://user:secretpw@host/mcp',
    )
    expect(
      screen.getAllByText(/looks like a credential/i).length,
    ).toBeGreaterThan(0)
    expect(create).toBeDisabled()
  })

  it('creates a config (submit → api.createConfig → success toast → close)', async () => {
    api.createConfig.mockResolvedValue({ ...configFixture, revision: 1 })
    const onOpenChange = vi.fn()
    wrap(
      <ConfigEditorDialog
        open
        onOpenChange={onOpenChange}
        serverRef="github"
      />,
    )
    await userEvent.click(
      screen.getByRole('button', { name: /create config/i }),
    )
    await waitFor(() => expect(api.createConfig).toHaveBeenCalledTimes(1))
    expect(api.createConfig.mock.calls[0][0]).toMatchObject({
      server_ref: 'github',
      transport: 'stdio',
    })
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})

describe('ServerDetailSheet — untrusted annotations, RBAC, delete flow', () => {
  it('renders the destructive hint as an UNVERIFIED signal', async () => {
    api.getServer.mockResolvedValue(detailFixture)
    wrap(<ServerDetailSheet serverId="s1" open onOpenChange={() => {}} />)
    expect(await screen.findByText('delete_repo')).toBeInTheDocument()
    expect(screen.getByText('Destructive')).toBeInTheDocument()
    // The untrusted disclaimer must be present (annotation is a claim, not truth).
    expect(
      screen.getAllByText(/not verified|self-reported/i).length,
    ).toBeGreaterThan(0)
  })

  it('shows secrets only as references, never values', async () => {
    api.getServer.mockResolvedValue(detailFixture)
    wrap(<ServerDetailSheet serverId="s1" open onOpenChange={() => {}} />)
    await screen.findByText(/managed configuration/i)
    expect(screen.getByText('GITHUB_TOKEN')).toBeInTheDocument()
    // The ref locator value must not be rendered as a usable plaintext field.
    expect(screen.queryByText('$GITHUB_TOKEN')).toBeNull()
  })

  it('hides config write actions when the role cannot write', async () => {
    authState.can = (p) => p !== 'capabilities:config:write'
    api.getServer.mockResolvedValue(detailFixture)
    wrap(<ServerDetailSheet serverId="s1" open onOpenChange={() => {}} />)
    await screen.findByText(/managed configuration/i)
    expect(screen.queryByRole('button', { name: /edit config/i })).toBeNull()
    expect(screen.queryByRole('button', { name: /delete config/i })).toBeNull()
  })

  it('deletes a config through a confirm dialog (privileged-action flow)', async () => {
    api.getServer.mockResolvedValue(detailFixture)
    api.deleteConfig.mockResolvedValue(undefined)
    wrap(<ServerDetailSheet serverId="s1" open onOpenChange={() => {}} />)
    await userEvent.click(
      await screen.findByRole('button', { name: /delete config/i }),
    )
    // The confirm dialog gates the destructive action.
    const confirm = await screen.findByRole('button', {
      name: /delete permanently/i,
    })
    await userEvent.click(confirm)
    await waitFor(() => expect(api.deleteConfig).toHaveBeenCalledWith('c1'))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

// ⛔ TESTIGO DE VISTA MONTADA. El de transporte prueba que el método MANDA el techo; éste prueba
// que el aviso es ALCANZABLE desde la pantalla. Una sonda de fuente daría verde con el aviso
// montado en una rama que no se renderiza nunca.
describe('capabilities — el recorte se declara, no se calla', () => {
  it('con has_more en servers, el aviso SALE', async () => {
    api.listServers.mockResolvedValue({ items: [], has_more: true })
    wrap(<CapabilitiesView />)
    expect(
      await screen.findByText(/servers; there are more|servidores; hay más/i),
    ).toBeVisible()
  })

  it('sin has_more NO sale: un aviso que sale siempre no declara nada', async () => {
    api.listServers.mockResolvedValue({ items: [], has_more: false })
    wrap(<CapabilitiesView />)
    await waitFor(() => expect(api.listServers).toHaveBeenCalled())
    expect(screen.queryByText(/there are more|hay más/i)).toBeNull()
  })
})
