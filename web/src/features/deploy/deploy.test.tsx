// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type {
  DefinitionDTO,
  MutationResponse,
  OperationDTO,
  PlanResponse,
  WiringDTO,
} from './types'

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

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    Link: ({
      children,
      to,
      ...rest
    }: {
      children: ReactNode
      to: string
    } & Record<string, unknown>) => (
      <a href={to} {...rest}>
        {children}
      </a>
    ),
  }
})

const api = vi.hoisted(() => ({
  listDefinitions: vi.fn(),
  getDefinition: vi.fn(),
  createDefinition: vi.fn(),
  updateDefinition: vi.fn(),
  deleteDefinition: vi.fn(),
  listRevisions: vi.fn(),
  rollback: vi.fn(),
  plan: vi.fn(),
  verify: vi.fn(),
  apply: vi.fn(),
  retire: vi.fn(),
  listWirings: vi.fn(),
  listOperations: vi.fn(),
  executor: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, deployApi: api }
})

// The Subject is chosen from what exists: the agents and the MCP servers of the tenant.
const subjects = vi.hoisted(() => ({
  agents: vi.fn(),
  servers: vi.fn(),
}))
vi.mock('@/lib/api/endpoints', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/endpoints')>()
  return {
    ...actual,
    agentsApi: { ...actual.agentsApi, list: subjects.agents },
  }
})
vi.mock('@/features/capabilities/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/capabilities/api')>()
  return {
    ...actual,
    capabilitiesApi: {
      ...actual.capabilitiesApi,
      listServers: subjects.servers,
    },
  }
})

import DeployView from './deploy-view'
import { DefinitionDetailSheet } from './definition-detail'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import { useModulesStore } from '@/stores/modules'
import { DefinitionEditorDialog } from './definition-editor'
import { WiringsTable } from './wirings-table'
import { OperationsTable } from './operations-table'
import { PageActionsProvider } from '@/components/ui/page-actions'
import * as deployLocales from './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

// --- fixtures ----------------------------------------------------------------

const definitionRow: DefinitionDTO = {
  id: 'd1',
  subject_kind: 'agent',
  subject_ref: 'agent/billing',
  name: 'billing-bot',
  environment: 'prod',
  target: 'docker.host/node1',
  runtime: 'docker',
  desired_status: 'active',
  current_version: 3,
  applied_version: 2,
  spec_hash: 'a1b2c3d4e5f6a7b8',
  up_to_date: false,
}

const definitionDetail: DefinitionDTO = {
  ...definitionRow,
  applied_version: 3,
  up_to_date: true,
  source_ref: 'git:repo#abc123',
  real: { status: 'active', version: '3', deployed_at: '2026-06-01T10:00:00Z' },
  spec: {
    image: 'registry/billing:1.2.0',
    command: 'serve',
    replicas: 2,
    resources: { cpu: '500m', mem: '512Mi' },
    env_refs: [{ name: 'DB_DSN', secret_ref: 'vault:secret/data/db#dsn' }],
    wirings: [
      {
        resource_kind: 'postgres.table',
        resource_ref: 'public.customers',
        mode: 'read',
        secret_ref: 'vault:secret/data/pg#ro',
      },
    ],
    identity: { identity_ref: 'nhi/billing', mint: false },
  },
}

const wiringRow: WiringDTO = {
  definition_id: 'd1',
  agent_ref: 'agent/billing',
  identity_ref: 'nhi/billing',
  resource_kind: 'postgres.table',
  resource_ref: 'public.customers',
  mode: 'read',
  secret_ref: 'vault:secret/data/pg#ro',
  status: 'applied',
  attribution: 'degraded',
  version: 3,
}

const operationRow: OperationDTO = {
  definition_id: 'd1',
  op: 'apply',
  from_version: 2,
  to_version: 3,
  plan_hash: 'deadbeefcafef00d',
  approval_ref: 'appr-123',
  gate_status: 'pending',
  status: 'requested',
  actor: 'user/fran',
  occurred_at: '2026-06-01T09:00:00Z',
}

beforeEach(() => {
  useModulesStore.getState().setOff([])
  authState.can = () => true
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  // Sensible defaults so polling queries in the detail sheet never reject.
  api.listOperations.mockResolvedValue({ items: [], has_more: false })
  api.listRevisions.mockResolvedValue({ items: [], has_more: false })
  api.executor.mockResolvedValue({ configured: true })
  subjects.agents.mockReset()
  subjects.servers.mockReset()
  subjects.agents.mockResolvedValue({
    items: [
      { id: 'a1', name: 'billing-bot' },
      { id: 'a2', name: 'ops-bot' },
    ],
    has_more: false,
  })
  subjects.servers.mockResolvedValue({
    items: [{ id: 's1', name: 'github-mcp' }],
    has_more: false,
  })
})

// The page with its header slots, as the console mounts it.
function wrapPage() {
  return wrap(
    <PageActionsProvider>
      <DeployView />
    </PageActionsProvider>,
  )
}
const primarySlot = () =>
  document.querySelector('[data-page-primary-action]') as HTMLElement
afterEach(() => vi.clearAllMocks())

// --- (a) the main list renders rows ------------------------------------------

describe('DeployView — definitions list', () => {
  it('lists deployment definitions with desired status and reconciliation state', async () => {
    api.listDefinitions.mockResolvedValue({
      items: [definitionRow],
      has_more: false,
    })
    wrap(<DeployView />)
    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    expect(screen.getByText('prod')).toBeInTheDocument()
    // applied_version (2) < current_version (3) → "changes pending" affordance.
    expect(screen.getByText('Changes pending')).toBeInTheDocument()
    expect(screen.getByText('v2 / v3')).toBeInTheDocument()
  })

  it('shows "declared, never applied" when applied_version is 0', async () => {
    api.listDefinitions.mockResolvedValue({
      items: [{ ...definitionRow, applied_version: 0, up_to_date: false }],
      has_more: false,
    })
    wrap(<DeployView />)
    await screen.findByText('billing-bot')
    expect(screen.getByText('Declared, never applied')).toBeInTheDocument()
  })

  it('hides the declare button and wirings tab when the role cannot', async () => {
    authState.can = (p) =>
      p !== 'deploy:deployment:write' && p !== 'deploy:wiring:read'
    api.listDefinitions.mockResolvedValue({ items: [], has_more: false })
    wrap(<DeployView />)
    await waitFor(() => expect(api.listDefinitions).toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: /declare deployment/i }),
    ).toBeNull()
    expect(screen.queryByRole('tab', { name: /^wirings$/i })).toBeNull()
  })

  it('with an executor, the empty state says what the screen lists, keeps Declare as its action and links to launched sessions', async () => {
    api.listDefinitions.mockResolvedValue({ items: [], has_more: false })
    wrapPage()
    const title = await screen.findByText('No deployments declared yet')
    const empty = title.closest('[data-slot="empty-state"]')
    expect(empty).not.toBeNull()
    expect(empty).toHaveTextContent(/deployment definitions/i)
    const link = within(empty as HTMLElement).getByRole('link', {
      name: 'Operate sessions',
    })
    expect(link).toHaveAttribute('href', '/agentops')
    // The screen's own verb stays the PRIMARY action, in the empty state and in the
    // page header; the way out to the sessions is the quieter one.
    expect(
      within(empty as HTMLElement).getByRole('button', {
        name: /declare deployment/i,
      }),
    ).toBeInTheDocument()
    expect(
      within(primarySlot()).getByRole('button', {
        name: /declare deployment/i,
      }),
    ).toBeInTheDocument()
    // A status code is engine vocabulary: the console never shows one.
    expect(document.body).not.toHaveTextContent(/\b503\b|\bHTTP\b/)
  })
})

// --- (a2) no runtime executor: say what Deploy needs before offering a form -----

describe('DeployView — without a runtime executor', () => {
  beforeEach(() => {
    api.executor.mockResolvedValue({ configured: false })
  })

  it('says in one sentence what Deploy needs and how to connect it, and does not offer the form first', async () => {
    api.listDefinitions.mockResolvedValue({ items: [], has_more: false })
    wrapPage()
    const title = await screen.findByText('Deploying needs a runtime executor')
    const empty = title.closest('[data-slot="empty-state"]') as HTMLElement
    expect(empty).toHaveTextContent('OLIVARES_DEPLOY_EXECUTOR_CONFIG')
    // The way forward is the guide to connecting one, not an unrelated screen.
    expect(
      within(empty).getByRole('link', { name: 'How to connect an executor' }),
    ).toHaveAttribute(
      'href',
      'https://docs.olivares.ai/reference/modules/vii-deploy/#connect-an-executor',
    )
    expect(
      within(empty).queryByRole('link', { name: 'Operate sessions' }),
    ).toBeNull()
    expect(document.body).not.toHaveTextContent(/\b503\b|\bHTTP\b/)
    // Neither the empty state nor the header's primary slot opens the form.
    expect(
      within(empty).queryByRole('button', { name: /declare deployment/i }),
    ).toBeNull()
    expect(within(primarySlot()).queryByRole('button')).toBeNull()
    // Declaring desired state stays possible, as a quieter header action.
    await userEvent.click(
      screen.getByRole('button', { name: /declare deployment/i }),
    )
    expect(
      await screen.findByRole('dialog', { name: 'Declare deployment' }),
    ).toBeInTheDocument()
  })

  it('says in one plain sentence what Deploy does, in every locale', async () => {
    api.listDefinitions.mockResolvedValue({ items: [], has_more: false })
    wrapPage()
    expect(
      await screen.findByText(
        'Declare which agents and MCP servers run where, then plan, approve and apply each change, or roll it back.',
      ),
    ).toBeInTheDocument()
    for (const [locale, strings] of Object.entries(deployLocales)) {
      // Arrows and "vs" were the old subtitle's shorthand, not words.
      expect(strings.subtitle, locale).not.toMatch(/→|\bvs\b/)
      expect(strings.noExecutor.guide, locale).toBeTruthy()
    }
  })

  it('keeps the list and states what Plan and Apply need above it', async () => {
    api.listDefinitions.mockResolvedValue({
      items: [definitionRow],
      has_more: false,
    })
    wrapPage()
    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    expect(
      screen.getByText('Deploying needs a runtime executor'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/OLIVARES_DEPLOY_EXECUTOR_CONFIG/),
    ).toBeInTheDocument()
    expect(within(primarySlot()).queryByRole('button')).toBeNull()
    // The detail sheet a row opens knows it too.
    api.getDefinition.mockResolvedValue(definitionDetail)
    await userEvent.click(screen.getByText('billing-bot'))
    expect(
      await screen.findByRole('button', { name: /^plan$/i }),
    ).toHaveAttribute('aria-disabled', 'true')
  })

  it('shows rows without waiting for the executor answer, and places the verb only once it arrives', async () => {
    api.executor.mockReturnValue(new Promise(() => {}))
    api.listDefinitions.mockResolvedValue({
      items: [definitionRow],
      has_more: false,
    })
    wrapPage()
    expect(await screen.findByText('billing-bot')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /declare deployment/i }),
    ).toBeNull()
  })

  it('an unreadable executor state claims nothing: the page keeps its full layout', async () => {
    api.executor.mockRejectedValue(new ApiError(500, 'internal', 'boom'))
    api.listDefinitions.mockResolvedValue({ items: [], has_more: false })
    wrapPage()
    expect(
      await screen.findByText('No deployments declared yet'),
    ).toBeInTheDocument()
    expect(screen.queryByText('Deploying needs a runtime executor')).toBeNull()
    expect(
      within(primarySlot()).getByRole('button', {
        name: /declare deployment/i,
      }),
    ).toBeInTheDocument()
  })
})

// --- (b) untrusted/provenance signal: degraded attribution -------------------

describe('WiringsTable — provenance honesty signal', () => {
  it('renders degraded attribution as an honest "unavailable" note, not a failure', async () => {
    api.listWirings.mockResolvedValue({ items: [wiringRow], has_more: false })
    wrap(<WiringsTable />)
    expect(await screen.findByText('agent/billing')).toBeInTheDocument()
    expect(screen.getByText('Attribution unavailable')).toBeInTheDocument()
  })

  it('renders secret refs as references, never values', async () => {
    api.listWirings.mockResolvedValue({ items: [wiringRow], has_more: false })
    wrap(<WiringsTable />)
    await screen.findByText('agent/billing')
    // The SecretRef chip shows the reference name inside a non-editable chip.
    const chip = screen
      .getByText('vault:secret/data/pg#ro')
      .closest('[data-slot="secret-ref"]')
    expect(chip).not.toBeNull()
    // The reference is never rendered as an editable value field.
    expect(screen.queryByDisplayValue('vault:secret/data/pg#ro')).toBeNull()
  })

  it('shows a calm forbidden state (not an error) without wiring:read', async () => {
    authState.can = (p) => p !== 'deploy:wiring:read'
    wrap(<WiringsTable />)
    expect(
      await screen.findByText(/do not have access to wirings/i),
    ).toBeInTheDocument()
    expect(api.listWirings).not.toHaveBeenCalled()
  })
})

// --- (c) secrets render as references in the editor & detail ------------------

describe('DefinitionEditorDialog — secrets are references, never values', () => {
  it('offers NO secret-value input and shows the control-plane + audit notices', async () => {
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    expect(screen.getByText(/tamper-evident audit ledger/i)).toBeInTheDocument()
    // The control-plane-only notice appears (the dialog description and the inline
    // banner both carry it — at least one must be present).
    expect(
      screen.getAllByText(/does NOT change running infrastructure/i).length,
    ).toBeGreaterThan(0)

    await userEvent.click(
      screen.getByRole('button', { name: /add reference/i }),
    )
    // The env-ref editor collects a name + a secret REFERENCE — never a raw value.
    expect(screen.queryByLabelText(/secret value/i)).toBeNull()
    // The definition "Name" field plus the env-ref row's "Name" both match.
    expect(screen.getAllByLabelText(/^name$/i).length).toBeGreaterThanOrEqual(1)
    expect(screen.getByLabelText(/secret reference/i)).toBeInTheDocument()
  })

  it('warns when a secret reference looks like an embedded credential and blocks save', async () => {
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    // Fill the required base fields so only the credential guard blocks submit.
    // Accessible-name role queries exclude the aria-hidden required "*".
    await userEvent.click(
      await screen.findByRole('combobox', { name: /^subject$/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: 'billing-bot' }),
    )
    await userEvent.type(screen.getByRole('textbox', { name: /^name$/i }), 'x')
    await userEvent.type(
      screen.getByRole('textbox', { name: /^environment$/i }),
      'prod',
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: /^target$/i }),
      'node1',
    )

    const create = screen.getByRole('button', { name: /declare deployment/i })
    expect(create).toBeEnabled()

    await userEvent.click(
      screen.getByRole('button', { name: /add reference/i }),
    )
    // Type a credential-looking value into the secret REFERENCE locator field.
    await userEvent.type(
      screen.getByRole('textbox', { name: /secret reference/i }),
      'password=hunter2',
    )
    expect(
      screen.getAllByText(/looks like a credential/i).length,
    ).toBeGreaterThan(0)
    expect(create).toBeDisabled()
  })
})

describe('DefinitionEditorDialog — the Subject is chosen from what exists', () => {
  async function fillPlacement() {
    await userEvent.type(
      screen.getByRole('textbox', { name: /^name$/i }),
      'billing',
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: /^environment$/i }),
      'prod',
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: /^target$/i }),
      'docker.host/node1',
    )
  }

  it('offers the agents of this tenant and declares the one chosen', async () => {
    subjects.agents.mockResolvedValue({
      items: [
        { id: 'a1', name: 'billing-bot', external_id: 'billing-bot' },
        { id: 'a2', name: 'ops-bot', external_id: 'ops-primary' },
      ],
      has_more: false,
    })
    api.createDefinition.mockResolvedValue({ ...definitionRow, id: 'new' })
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    // The kind reads as product words, not identifiers.
    expect(
      screen.getByRole('combobox', { name: /subject kind/i }),
    ).toHaveTextContent('Agent')
    expect(screen.queryByRole('textbox', { name: /^subject$/i })).toBeNull()
    await userEvent.click(
      await screen.findByRole('combobox', { name: /^subject$/i }),
    )
    expect(
      await screen.findByRole('option', { name: 'ops-bot' }),
    ).toBeInTheDocument()
    await userEvent.click(screen.getByRole('option', { name: 'billing-bot' }))
    await fillPlacement()
    await userEvent.click(
      screen.getByRole('button', { name: /declare deployment/i }),
    )
    await waitFor(() => expect(api.createDefinition).toHaveBeenCalledTimes(1))
    expect(api.createDefinition.mock.calls[0][0]).toMatchObject({
      subject_kind: 'agent',
      subject_ref: 'billing-bot',
      name: 'billing',
    })
  })

  it('offers the MCP servers when the kind is MCP server, and forgets the agent chosen before', async () => {
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    await userEvent.click(
      await screen.findByRole('combobox', { name: /^subject$/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: 'billing-bot' }),
    )
    await userEvent.click(
      screen.getByRole('combobox', { name: /subject kind/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: 'MCP server' }),
    )
    const subject = await screen.findByRole('combobox', { name: /^subject$/i })
    expect(subject).toHaveTextContent('Choose an MCP server')
    await userEvent.click(subject)
    expect(
      await screen.findByRole('option', { name: 'github-mcp' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'billing-bot' })).toBeNull()
  })

  it('declares an MCP reference without reading Capabilities when that module is off', async () => {
    useModulesStore.getState().setOff(['capabilities'])
    api.createDefinition.mockResolvedValue({ ...definitionRow, id: 'new' })
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    // The agent roster remains available independently of Capabilities.
    await userEvent.click(
      await screen.findByRole('combobox', { name: /^subject$/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: 'billing-bot' }),
    )
    await userEvent.click(
      screen.getByRole('combobox', { name: /subject kind/i }),
    )
    await userEvent.click(
      await screen.findByRole('option', { name: 'MCP server' }),
    )
    expect(subjects.servers).not.toHaveBeenCalled()
    const subject = await screen.findByRole('textbox', { name: /^subject$/i })
    expect(subject).toHaveValue('')
    await userEvent.type(subject, 'github-mcp')
    await fillPlacement()
    await userEvent.click(
      screen.getByRole('button', { name: /declare deployment/i }),
    )
    await waitFor(() => expect(api.createDefinition).toHaveBeenCalledTimes(1))
    expect(api.createDefinition.mock.calls[0][0]).toMatchObject({
      subject_kind: 'mcp_server',
      subject_ref: 'github-mcp',
    })
    expect(subjects.servers).not.toHaveBeenCalled()
  })

  it('says so when nothing of that kind exists yet, and the form cannot be sent', async () => {
    subjects.agents.mockResolvedValue({ items: [], has_more: false })
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    expect(await screen.findByText('No agents yet.')).toBeInTheDocument()
    await fillPlacement()
    expect(
      screen.getByRole('button', { name: /declare deployment/i }),
    ).toBeDisabled()
  })

  it.each([
    ...['\u0085billing', 'billing\u0085', '\u0085billing\u0085'].map(
      (name, index) => ({
        scenario: `Go trims U+0085 from the agent name (case ${index + 1})`,
        items: [
          { id: 'a1', name: 'billing', external_id: 'billing-primary' },
          { id: 'a2', name, external_id: 'billing-secondary' },
        ],
      }),
    ),
    {
      scenario: 'JavaScript trims U+FEFF from the agent name',
      items: [
        { id: 'a1', name: 'billing', external_id: 'billing-primary' },
        {
          id: 'a2',
          name: '\ufeffbilling\ufeff',
          external_id: 'billing-secondary',
        },
      ],
    },
    {
      scenario: 'an agent name changes when the submitted reference is trimmed',
      items: [
        { id: 'a1', name: 'billing', external_id: 'billing-primary' },
        { id: 'a2', name: ' billing ', external_id: 'billing-secondary' },
      ],
    },
    {
      scenario: 'a padded agent name has no unpadded counterpart',
      items: [
        { id: 'a2', name: '\tbilling\n', external_id: 'billing-secondary' },
      ],
    },
    {
      scenario: 'agents have duplicate names',
      items: [
        { id: 'a1', name: 'billing-bot', external_id: 'billing-primary' },
        { id: 'a2', name: 'billing-bot', external_id: 'billing-secondary' },
      ],
    },
    {
      scenario: "an agent name matches another agent's external ID",
      items: [
        { id: 'a1', name: 'ops', external_id: 'billing' },
        { id: 'a2', name: 'billing', external_id: 'billing-secondary' },
      ],
    },
    {
      scenario: 'the name/external-ID collision appears in reverse order',
      items: [
        { id: 'a2', name: 'billing', external_id: 'billing-secondary' },
        { id: 'a1', name: 'ops', external_id: 'billing' },
      ],
    },
  ])('preserves external-ID entry when $scenario', async ({ items }) => {
    subjects.agents.mockResolvedValue({ items, has_more: false })
    api.createDefinition.mockResolvedValue({ ...definitionRow, id: 'new' })
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    const subject = await screen.findByRole('textbox', { name: /^subject$/i })
    expect(screen.queryByRole('combobox', { name: /^subject$/i })).toBeNull()
    await userEvent.type(subject, 'billing-secondary')
    await fillPlacement()
    await userEvent.click(
      screen.getByRole('button', { name: /declare deployment/i }),
    )
    await waitFor(() => expect(api.createDefinition).toHaveBeenCalledTimes(1))
    expect(api.createDefinition.mock.calls[0][0]).toMatchObject({
      subject_kind: 'agent',
      subject_ref: 'billing-secondary',
      name: 'billing',
    })
  })

  it('falls back to typing the reference when the list is longer than one page', async () => {
    subjects.agents.mockResolvedValue({
      items: [{ id: 'a1', name: 'billing-bot' }],
      has_more: true,
    })
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    expect(
      await screen.findByRole('textbox', { name: /^subject$/i }),
    ).toBeEnabled()
    expect(screen.queryByRole('combobox', { name: /^subject$/i })).toBeNull()
  })

  it('editing keeps the declared Subject and asks for no list', async () => {
    wrap(
      <DefinitionEditorDialog
        open
        onOpenChange={() => {}}
        definition={definitionDetail}
      />,
    )
    const subject = screen.getByRole('textbox', { name: /^subject$/i })
    expect(subject).toBeDisabled()
    expect(subject).toHaveValue('agent/billing')
    expect(subjects.agents).not.toHaveBeenCalled()
    expect(subjects.servers).not.toHaveBeenCalled()
  })

  it('falls back to typing the reference when the list cannot be read', async () => {
    subjects.agents.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    api.createDefinition.mockResolvedValue({ ...definitionRow, id: 'new' })
    wrap(
      <DefinitionEditorDialog open onOpenChange={() => {}} definition={null} />,
    )
    await userEvent.type(
      await screen.findByRole('textbox', { name: /^subject$/i }),
      'acme-bot',
    )
    await fillPlacement()
    await userEvent.click(
      screen.getByRole('button', { name: /declare deployment/i }),
    )
    await waitFor(() => expect(api.createDefinition).toHaveBeenCalledTimes(1))
    expect(api.createDefinition.mock.calls[0][0]).toMatchObject({
      subject_ref: 'acme-bot',
    })
  })
})

describe('DefinitionDetailSheet — desired spec secrets as references', () => {
  it('renders env_refs and wiring secret_refs as reference chips, not values', async () => {
    api.getDefinition.mockResolvedValue(definitionDetail)
    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await screen.findByText(/desired specification/i)
    expect(screen.getByText('DB_DSN')).toBeInTheDocument()
    // The secret reference renders inside a SecretRef chip (data-slot=secret-ref).
    const refs = document.querySelectorAll('[data-slot="secret-ref"]')
    expect(refs.length).toBeGreaterThanOrEqual(2)
    // No editable control exposes the secret value.
    expect(screen.queryByDisplayValue('vault:secret/data/db#dsn')).toBeNull()
  })
})

// --- (d) privileged action gated + confirmed ---------------------------------

describe('DefinitionDetailSheet — privileged actions are gated and confirmed', () => {
  it('disables Apply/Retire for a non-admin role', async () => {
    authState.can = (p) => p !== 'deploy:deployment:admin'
    api.getDefinition.mockResolvedValue(definitionDetail)
    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await screen.findByText(/desired specification/i)
    expect(screen.getByRole('button', { name: /^apply$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /^retire$/i })).toBeDisabled()
  })

  it('asks for confirmation before deleting (write-tier, danger)', async () => {
    api.getDefinition.mockResolvedValue(definitionDetail)
    api.deleteDefinition.mockResolvedValue(undefined)
    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^delete$/i }),
    )
    // The confirm dialog gates the destructive action with the audit notice.
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(/tamper-evident audit ledger/i),
    ).toBeInTheDocument()
    await userEvent.click(
      within(dialog).getByRole('button', { name: /delete definition/i }),
    )
    await waitFor(() => expect(api.deleteDefinition).toHaveBeenCalledWith('d1'))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })
})

//-CX-01 (Codex sol max). apply/retire are AAL3-gated in the engine
// (modules/deploy/helpers.go:74 -> 403 with the `step_up_required` CODE), and this
// view reported that refusal as "your role can't perform this action" through a
// local reportError that only looked at the STATUS. The code was routed through the
// shared policy; the contrast pointed out there was no cell proving it, which is
// how a fix goes back out again unnoticed.
describe('DefinitionDetailSheet — a step-up refusal is not a role refusal', () => {
  it('opens the ceremony for a step_up_required apply, and blames no role', async () => {
    useStepUpStore.setState({ request: null })
    api.getDefinition.mockResolvedValue(definitionDetail)
    api.apply.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'AAL3 session required'),
    )
    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^apply$/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: /confirmation phrase/i }),
      'APPLY',
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: /apply/i }),
    )

    await waitFor(() =>
      expect(useStepUpStore.getState().request).not.toBeNull(),
    )
    expect(toast.warning).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  // Non-firing direction: a plain forbidden must still read as a role refusal.
  it('still reports a plain forbidden apply as a role refusal', async () => {
    useStepUpStore.setState({ request: null })
    api.getDefinition.mockResolvedValue(definitionDetail)
    api.apply.mockRejectedValue(new ApiError(403, 'forbidden', 'nope'))
    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^apply$/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: /confirmation phrase/i }),
      'APPLY',
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: /apply/i }),
    )

    await waitFor(() => expect(toast.warning).toHaveBeenCalledOnce())
    expect(useStepUpStore.getState().request).toBeNull()
  })
})

describe('DefinitionDetailSheet — without a runtime executor', () => {
  it('disables Plan, Verify, Apply and Retire and says what they need', async () => {
    api.getDefinition.mockResolvedValue(definitionDetail)
    wrap(
      <DefinitionDetailSheet
        definitionId="d1"
        open
        onOpenChange={() => {}}
        noExecutor
      />,
    )
    await screen.findByText(/desired specification/i)
    for (const name of [/^plan$/i, /^verify$/i, /^apply$/i, /^retire$/i]) {
      const button = screen.getByRole('button', { name })
      expect(button).toHaveAttribute('aria-disabled', 'true')
      await userEvent.click(button)
    }
    for (const title of [
      'Run a dry-run plan?',
      'Verify against live infrastructure?',
      'Apply to live infrastructure?',
      'Retire from live infrastructure?',
    ])
      expect(screen.queryByRole('dialog', { name: title })).toBeNull()
    for (const call of [api.plan, api.verify, api.apply, api.retire])
      expect(call).not.toHaveBeenCalled()
    expect(
      screen.getAllByText(/OLIVARES_DEPLOY_EXECUTOR_CONFIG/).length,
    ).toBeGreaterThan(0)
    expect(document.body).not.toHaveTextContent(/\b503\b|\bHTTP\b/)
    // Editing the desired state stays available.
    expect(screen.getByRole('button', { name: /^edit$/i })).toBeEnabled()
  })
})

// --- (e) one full mutation flow: phase-1 apply -------------------------------

describe('DefinitionDetailSheet — governed apply flow (two-phase HITL)', () => {
  it('requires the exact APPLY phrase before phase 1 can be requested', async () => {
    api.getDefinition.mockResolvedValue(definitionDetail)
    api.apply.mockResolvedValue({
      op: 'apply',
      plan_hash: 'deadbeefcafef00d',
      version: 3,
      status: 'requested',
      requires_approval: true,
      approval_ref: 'appr-typed',
      gate_status: 'pending',
    } satisfies MutationResponse)

    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^apply$/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', {
      name: /request apply/i,
    })
    expect(confirm).toBeDisabled()
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: /confirmation phrase/i }),
      'apply',
    )
    expect(confirm).toBeDisabled()
    await userEvent.clear(
      within(dialog).getByRole('textbox', { name: /confirmation phrase/i }),
    )
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: /confirmation phrase/i }),
      'APPLY',
    )
    expect(confirm).toBeEnabled()
    await userEvent.click(confirm)
    await waitFor(() => expect(api.apply).toHaveBeenCalledWith('d1', {}))
  })

  it('requests apply → confirm → phase-1 returns pending approval (mutates nothing)', async () => {
    api.getDefinition.mockResolvedValue(definitionDetail)
    const phase1: MutationResponse = {
      op: 'apply',
      plan_hash: 'deadbeefcafef00d',
      version: 3,
      status: 'requested',
      requires_approval: true,
      approval_ref: 'appr-123',
      gate_status: 'pending',
      changes: [
        { kind: 'update', resource: 'container', detail: 'image bump' },
      ],
    }
    api.apply.mockResolvedValue(phase1)

    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^apply$/i }),
    )
    const dialog = await screen.findByRole('dialog')
    // The confirm carries the governed-mutation copy + audit notice.
    expect(
      within(dialog).getByText(/governed mutation requiring human approval/i),
    ).toBeInTheDocument()
    await userEvent.type(
      within(dialog).getByRole('textbox', { name: /confirmation phrase/i }),
      'APPLY',
    )
    await userEvent.click(
      within(dialog).getByRole('button', { name: /request apply/i }),
    )
    // Phase 1: api called with NO approval_ref (request, not execute).
    await waitFor(() => expect(api.apply).toHaveBeenCalledTimes(1))
    expect(api.apply.mock.calls[0]).toEqual(['d1', {}])
    // The pending-approval state surfaces with the approval reference.
    expect(await screen.findByText(/pending approval/i)).toBeInTheDocument()
    expect(screen.getByText(/appr-123/)).toBeInTheDocument()
    // Nothing was applied — no success toast.
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('runs a dry-run plan and shows the diff (privileged, low risk)', async () => {
    api.getDefinition.mockResolvedValue(definitionDetail)
    const plan: PlanResponse = {
      plan_hash: 'abc123def456',
      from_version: 3,
      to_version: 3,
      up_to_date: false,
      changes: [{ kind: 'create', resource: 'wiring', detail: 'new edge' }],
    }
    api.plan.mockResolvedValue(plan)
    wrap(
      <DefinitionDetailSheet definitionId="d1" open onOpenChange={() => {}} />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: /^plan$/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(
      within(dialog).getByRole('button', { name: /run plan/i }),
    )
    await waitFor(() => expect(api.plan).toHaveBeenCalledWith('d1'))
    expect(await screen.findByText(/plan result/i)).toBeInTheDocument()
    expect(screen.getByText('new edge')).toBeInTheDocument()
  })
})

// --- operations ledger -------------------------------------------------------

describe('OperationsTable — empty ledger', () => {
  it('without an executor, says when entries will appear, with no status code', async () => {
    wrap(<OperationsTable noExecutor />)
    expect(
      await screen.findByText('No deployment operations recorded yet'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/once a runtime executor is connected/i),
    ).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(/\b503\b|\bHTTP\b/)
  })

  it('with an executor, says what the ledger records', async () => {
    wrap(<OperationsTable />)
    expect(
      await screen.findByText(
        'Every plan, apply, verify and retire is recorded here.',
      ),
    ).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent(/\b503\b|\bHTTP\b/)
  })
})

describe('OperationsTable — change-management ledger', () => {
  it('renders ledger rows with op, status and a gate badge', async () => {
    api.listOperations.mockResolvedValue({
      items: [operationRow],
      has_more: false,
    })
    wrap(<OperationsTable />)
    expect(await screen.findByText('appr-123')).toBeInTheDocument()
    expect(screen.getByText('Requested')).toBeInTheDocument()
    expect(screen.getByText('Pending')).toBeInTheDocument()
  })
})

// ⛔ TESTIGO DE VISTA MONTADA. El de transporte (`api-transport.test.ts`) prueba que el método
// MANDA el techo; éste prueba que la pantalla lo pide y que el aviso es ALCANZABLE. Una sonda de
// fuente —un grep por `has_more`— daría verde con el aviso montado en una rama que no se
// renderiza nunca.
describe('deploy — el recorte se declara, no se calla', () => {
  it('las tres pantallas piden el techo', async () => {
    api.listDefinitions.mockResolvedValue({ items: [], has_more: false })
    api.listWirings.mockResolvedValue({ items: [], has_more: false })
    wrap(<DeployView />)
    await waitFor(() => expect(api.listDefinitions).toHaveBeenCalled())
    expect(api.listDefinitions).toHaveBeenCalledWith()
    wrap(<WiringsTable />)
    await waitFor(() => expect(api.listWirings).toHaveBeenCalled())
    expect(api.listWirings).toHaveBeenCalledWith()
  })

  it('con has_more en wirings, el aviso SALE', async () => {
    api.listWirings.mockResolvedValue({ items: [], has_more: true })
    wrap(<WiringsTable />)
    expect(
      await screen.findByText(/wirings; there are more|enlaces; hay más/i),
    ).toBeVisible()
  })

  it('con has_more en operations, el aviso SALE', async () => {
    api.listOperations.mockResolvedValue({ items: [], has_more: true })
    wrap(<OperationsTable />)
    expect(
      await screen.findByText(
        /operations; there are more|operaciones; hay más/i,
      ),
    ).toBeVisible()
  })

  it('sin has_more no sale ninguno: un aviso que sale siempre no declara nada', async () => {
    api.listWirings.mockResolvedValue({ items: [], has_more: false })
    wrap(<WiringsTable />)
    await waitFor(() => expect(api.listWirings).toHaveBeenCalled())
    expect(screen.queryByText(/there are more|hay más/i)).toBeNull()
  })
})
