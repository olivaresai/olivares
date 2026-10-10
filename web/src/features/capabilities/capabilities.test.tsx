// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import type { TabExtension } from '@/features/panels'
import type { ConfigDTO, ServerDetailDTO } from './types'

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
const panels = vi.hoisted(() => ({ capabilitiesTabs: [] as TabExtension[] }))
vi.mock('@/features/extensions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/extensions')>()
  return {
    ...actual,
    PANEL_EXTENSIONS: {
      ...actual.PANEL_EXTENSIONS,
      get capabilitiesTabs() {
        return panels.capabilitiesTabs
      },
    },
  }
})
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listServers: vi.fn(),
  getServer: vi.fn(),
  listTools: vi.fn(),
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

beforeEach(() => {
  authState.can = () => true
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  api.listServers.mockResolvedValue({ items: [], has_more: false })
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

describe('Saved MCP definitions are observation metadata', () => {
  const notice =
    'Saved definitions are observation metadata. Saving, enabling or deleting one does not change the MCP gateway or a running server.'

  it('explains the runtime effect on the saved-config list', async () => {
    const user = userEvent.setup()
    api.listConfigs.mockResolvedValue({
      items: [configFixture],
      has_more: false,
    })
    wrap(<CapabilitiesView />)
    await user.click(screen.getByRole('tab', { name: /managed configs/i }))
    expect(await screen.findByText(notice)).toBeInTheDocument()
  })

  it.each([null, configFixture])(
    'explains the runtime effect before saving %j',
    (config) => {
      wrap(<ConfigEditorDialog open onOpenChange={() => {}} config={config} />)
      expect(screen.getByText(notice)).toBeInTheDocument()
    },
  )

  it('explains the runtime effect beside a server definition', async () => {
    api.getServer.mockResolvedValue(detailFixture)
    wrap(<ServerDetailSheet serverId="s1" open onOpenChange={() => {}} />)
    expect(await screen.findByText(notice)).toBeInTheDocument()
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

describe('CapabilitiesView — extension tabs', () => {
  afterEach(() => {
    panels.capabilitiesTabs = []
  })

  it('offers only its own tabs in the default console', () => {
    wrap(<CapabilitiesView />)
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual([
      'Servers',
      'Tools',
      'Skills',
      'Wiring',
      'Managed configs',
    ])
  })

  it('mounts an extension tab after Tools and paints its panel on click', async () => {
    panels.capabilitiesTabs = [
      {
        id: 'fixture-tab',
        label: () => 'Fixture tab',
        Component: () => <p>fixture panel</p>,
      },
    ]
    const user = userEvent.setup()
    wrap(<CapabilitiesView />)

    const tabs = screen.getAllByRole('tab').map((tab) => tab.textContent)
    expect(tabs.indexOf('Fixture tab')).toBe(tabs.indexOf('Tools') + 1)
    await user.click(screen.getByRole('tab', { name: 'Fixture tab' }))
    expect(await screen.findByText('fixture panel')).toBeInTheDocument()
  })

  it('does not offer an extension tab without its permission', () => {
    authState.can = (permission) => permission !== 'fixture:read'
    panels.capabilitiesTabs = [
      {
        id: 'fixture-tab',
        label: () => 'Fixture tab',
        permission: 'fixture:read',
        Component: () => <p>fixture panel</p>,
      },
    ]
    wrap(<CapabilitiesView />)
    expect(
      screen.queryByRole('tab', { name: 'Fixture tab' }),
    ).not.toBeInTheDocument()
  })
})

describe('ConfigEditorDialog — secrets are references, never values', () => {
  it('offers NO secret-value input, shows the audit notice, gates on a server ref', async () => {
    const user = userEvent.setup()
    wrap(<ConfigEditorDialog open onOpenChange={() => {}} />)
    expect(screen.getByText(/tamper-evident audit ledger/i)).toBeInTheDocument()

    const create = screen.getByRole('button', { name: /create config/i })
    expect(create).toBeDisabled()
    await user.type(screen.getByLabelText(/server reference/i), 'github')
    expect(create).toBeEnabled()

    await user.click(screen.getByRole('button', { name: /add reference/i }))
    // The secret-ref editor collects name / locator / hint — never a raw value.
    expect(screen.queryByLabelText(/secret value/i)).toBeNull()
    expect(screen.getByLabelText(/^name$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/locator/i)).toBeInTheDocument()
  })

  it('warns when the endpoint looks like an embedded credential and blocks save', async () => {
    const user = userEvent.setup()
    wrap(<ConfigEditorDialog open onOpenChange={() => {}} serverRef="github" />)
    const create = screen.getByRole('button', { name: /create config/i })
    expect(create).toBeEnabled()
    await user.type(
      screen.getByLabelText(/^endpoint$/i),
      'https://user:secretpw@host/mcp',
    )
    expect(
      screen.getAllByText(/looks like a credential/i).length,
    ).toBeGreaterThan(0)
    expect(create).toBeDisabled()
  })

  it('creates a config (submit → api.createConfig → success toast → close)', async () => {
    const user = userEvent.setup()
    api.createConfig.mockResolvedValue({ ...configFixture, revision: 1 })
    const onOpenChange = vi.fn()
    wrap(
      <ConfigEditorDialog
        open
        onOpenChange={onOpenChange}
        serverRef="github"
      />,
    )
    await user.click(screen.getByRole('button', { name: /create config/i }))
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
      screen.getAllByText(/unverified statements/i).length,
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
    const user = userEvent.setup()
    api.getServer.mockResolvedValue(detailFixture)
    api.deleteConfig.mockResolvedValue(undefined)
    wrap(<ServerDetailSheet serverId="s1" open onOpenChange={() => {}} />)
    await user.click(
      await screen.findByRole('button', { name: /delete config/i }),
    )
    // The confirm dialog gates the destructive action.
    const confirm = await screen.findByRole('button', {
      name: /delete permanently/i,
    })
    await user.click(confirm)
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
