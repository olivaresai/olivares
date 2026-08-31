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
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, deployApi: api }
})

import DeployView from './deploy-view'
import { DefinitionDetailSheet } from './definition-detail'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import { DefinitionEditorDialog } from './definition-editor'
import { WiringsTable } from './wirings-table'
import { OperationsTable } from './operations-table'

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
  authState.can = () => true
  for (const fn of Object.values(api)) fn.mockReset()
  toast.success.mockReset()
  toast.error.mockReset()
  toast.warning.mockReset()
  // Sensible defaults so polling queries in the detail sheet never reject.
  api.listOperations.mockResolvedValue({ items: [], has_more: false })
  api.listRevisions.mockResolvedValue({ items: [], has_more: false })
})
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
    await userEvent.type(
      screen.getByRole('textbox', { name: /^subject$/i }),
      'agent/x',
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
