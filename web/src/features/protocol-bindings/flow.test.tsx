// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import * as api from './api'
import { ProtocolBindingReconcileDialog } from './binding-detail'
import { ProtocolComposerDialog } from './composer-dialog'
import { ProtocolSpecTransitionDialog } from './spec-detail'
import type {
  ProtocolBinding,
  ProtocolBindingAssessment,
  ProtocolBindingReconcilePlan,
  ProtocolBindingSpec,
  ProtocolBindingSpecPlan,
  ProtocolSpecApplyOutcome,
} from './types'
import './i18n'

const TENANT_REQUEST = { tenant: 'tenant-1' } as const

const spec: ProtocolBindingSpec = {
  id: 'spec-1',
  tenant_id: 'tenant-1',
  workspace_id: 'workspace-1',
  version: 4,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
  binding_key: 'peer-work',
  generation: 2,
  protocol: 'a2a',
  protocol_version: '1.0.1',
  direction: 'outbound',
  local_kind: 'work_item',
  local_selector: { id: 'work-1' },
  peer_authority: 'peer.example',
  remote_resource_kind: 'task',
  remote_resource_ref: 'queue-1',
  mapping_schema: 'olivares.protocol-binding/v1',
  mapping: [
    {
      source: 'work.title',
      target: 'message.text',
      cardinality: 'one_to_one',
      transform: 'text',
    },
  ],
  mapping_hash: 'mapping-1',
  known_losses: [],
  losses_hash: 'losses-1',
  rule_refs: [],
  permission_profile_ref: 'profile:standard',
  currency_policy: 'pinned',
  validation: {
    verdict: 'CLEAN',
    code: 'server_witness',
    observed_at: '2026-08-20T00:00:00Z',
  },
  state: 'draft',
  spec_hash: 'spec-hash',
  plan_hash: 'prior-plan',
}

const plan: ProtocolBindingSpecPlan = {
  verdict: 'CLEAN',
  code: 'activate_planned',
  validation: {
    verdict: 'CLEAN',
    code: 'server_witness',
    observed_at: '2026-08-20T00:00:00Z',
  },
  plan_hash: 'plan-1',
  operation: 'activate',
  workspace_id: 'workspace-1',
  spec_id: 'spec-1',
  generation: 2,
  spec_hash: 'spec-hash',
  mapping_hash: 'mapping-1',
  losses_hash: 'losses-1',
}

beforeEach(() => vi.restoreAllMocks())

function arrangeTransition(applyOutcome: ProtocolSpecApplyOutcome) {
  vi.spyOn(api, 'getProtocolBindingSpec').mockResolvedValue({
    spec,
    etag: '"v4"',
  })
  vi.spyOn(api, 'runProtocolBindingSpecTransition')
    .mockResolvedValueOnce({ ...plan, plan_hash: '' } as never)
    .mockResolvedValueOnce(plan as never)
    .mockResolvedValueOnce(applyOutcome as never)
}

describe('activation flow', () => {
  it('exposes an accessible validate/plan/apply control and claims active only from the returned resource', async () => {
    const onApplied = vi.fn()
    arrangeTransition({
      result: {
        ...plan,
        spec: { ...spec, state: 'active', version: 5 },
      },
      etag: '"v5"',
      replayed: false,
    })
    render(
      <ProtocolSpecTransitionDialog
        open
        specId="spec-1"
        operation="activate"
        request={TENANT_REQUEST}
        onOpenChange={() => {}}
        onApplied={onApplied}
      />,
    )

    const apply = await screen.findByRole('button', { name: 'Activate' })
    expect(
      screen.getByRole('dialog', { name: 'Activate specification' }),
    ).toBeInTheDocument()
    await userEvent.click(apply)
    await waitFor(() => expect(onApplied).toHaveBeenCalledOnce())
    expect(
      vi
        .mocked(api.runProtocolBindingSpecTransition)
        .mock.calls.every((call) => call[4] === TENANT_REQUEST),
    ).toBe(true)
    expect(
      screen.getByText('The returned specification is active.'),
    ).toBeInTheDocument()
  })

  it('does not claim active when apply returns UNKNOWN in a 2xx body', async () => {
    const onApplied = vi.fn()
    arrangeTransition({
      result: {
        ...plan,
        verdict: 'UNKNOWN',
        code: 'observation_unavailable',
        spec,
      },
      etag: '"v4"',
      replayed: false,
    })
    render(
      <ProtocolSpecTransitionDialog
        open
        specId="spec-1"
        operation="activate"
        request={TENANT_REQUEST}
        onOpenChange={() => {}}
        onApplied={onApplied}
      />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Activate' }),
    )
    expect(
      await screen.findByText('No state claim was made'),
    ).toBeInTheDocument()
    expect(onApplied).not.toHaveBeenCalled()
    expect(
      screen.queryByText('The returned specification is active.'),
    ).not.toBeInTheDocument()
  })
})

describe('draft composer flow', () => {
  it('wires labelled fields through validate → plan → apply and omits the read-only witness', async () => {
    const onCreated = vi.fn()
    const createPlan: ProtocolBindingSpecPlan = {
      ...plan,
      operation: 'draft',
      spec_id: undefined,
      generation: 1,
    }
    const create = vi
      .spyOn(api, 'runProtocolBindingSpecCreate')
      .mockResolvedValueOnce({ ...createPlan, plan_hash: '' } as never)
      .mockResolvedValueOnce(createPlan as never)
      .mockResolvedValueOnce({
        result: {
          ...createPlan,
          spec: { ...spec, generation: 1, state: 'draft' },
        },
        etag: '"v4"',
        replayed: false,
      } as never)

    render(
      <ProtocolComposerDialog
        open
        workspaceId="workspace-1"
        request={TENANT_REQUEST}
        onOpenChange={() => {}}
        onCreated={onCreated}
      />,
    )
    expect(
      screen.getByRole('dialog', {
        name: 'Compose protocol binding draft',
      }),
    ).toBeInTheDocument()
    await userEvent.type(
      screen.getByRole('textbox', { name: /Binding key/ }),
      'peer-work',
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: /Peer authority/ }),
      'peer.example',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Next' }))
    await userEvent.type(
      screen.getByRole('textbox', { name: /Local resource ID/ }),
      'work-1',
    )
    await userEvent.type(
      screen.getByRole('textbox', { name: /Remote resource reference/ }),
      'queue-1',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Next' }))
    await userEvent.click(screen.getByRole('button', { name: 'Next' }))
    await userEvent.type(
      screen.getByRole('textbox', { name: /Permission profile reference/ }),
      'profile:standard',
    )
    await userEvent.click(screen.getByRole('button', { name: 'Next' }))
    expect(
      screen.getByRole('button', {
        name: 'Export desired spec and server evidence as JSON',
      }),
    ).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'Validate and plan' }),
    )
    expect(
      await screen.findByRole('region', { name: 'Plan → apply comparison' }),
    ).toBeInTheDocument()
    await userEvent.click(
      await screen.findByRole('button', { name: 'Create planned draft' }),
    )

    await waitFor(() => expect(onCreated).toHaveBeenCalledOnce())
    expect(create.mock.calls.map((call) => call[1])).toEqual([
      'validate',
      'plan',
      'apply',
    ])
    expect(create.mock.calls.every((call) => call[2] === TENANT_REQUEST)).toBe(
      true,
    )
    expect(create.mock.calls[2]?.[0]).not.toHaveProperty('validation')
  })

  it('derives a successor generation from the selected active specification', async () => {
    const predecessor = { ...spec, state: 'active' as const }
    const createPlan: ProtocolBindingSpecPlan = {
      ...plan,
      operation: 'draft',
      generation: 3,
      prior_active_id: 'spec-1',
      spec_id: undefined,
    }
    const create = vi
      .spyOn(api, 'runProtocolBindingSpecCreate')
      .mockResolvedValueOnce({ ...createPlan, plan_hash: '' } as never)
      .mockResolvedValueOnce(createPlan as never)
      .mockResolvedValueOnce({
        result: {
          ...createPlan,
          spec: {
            ...predecessor,
            id: 'spec-2',
            generation: 3,
            supersedes_id: 'spec-1',
            state: 'draft',
          },
        },
        etag: '"v1"',
        replayed: false,
      } as never)

    render(
      <ProtocolComposerDialog
        open
        workspaceId="workspace-1"
        request={TENANT_REQUEST}
        catalogSpecs={[predecessor]}
        catalogSpecsComplete
        onOpenChange={() => {}}
        onCreated={() => {}}
      />,
    )
    await userEvent.click(
      screen.getByRole('combobox', { name: 'Generation workflow' }),
    )
    await userEvent.click(
      screen.getByRole('option', {
        name: 'Successor of an active generation',
      }),
    )
    await userEvent.click(
      screen.getByRole('combobox', { name: 'Active predecessor' }),
    )
    await userEvent.click(
      screen.getByRole('option', {
        name: /peer-work · Generation 2 · A2A/,
      }),
    )

    expect(screen.getByRole('spinbutton', { name: 'Generation' })).toHaveValue(
      3,
    )
    expect(screen.getByRole('textbox', { name: 'Binding key' })).toHaveValue(
      'peer-work',
    )
    for (let index = 0; index < 4; index += 1)
      await userEvent.click(screen.getByRole('button', { name: 'Next' }))
    await userEvent.click(
      screen.getByRole('button', { name: 'Validate and plan' }),
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Create planned draft' }),
    )

    expect(create.mock.calls[0]?.[0]).toMatchObject({
      generation: 3,
      supersedes_id: 'spec-1',
    })
    expect(create.mock.calls[0]?.[0]).not.toHaveProperty('validation')
  })
})

const binding: ProtocolBinding = {
  id: 'binding-1',
  tenant_id: 'tenant-1',
  workspace_id: 'workspace-1',
  version: 7,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
  binding_spec_id: 'spec-1',
  binding_spec_generation: 2,
  pinned_spec_hash: 'spec-hash',
  pinned_mapping_hash: 'mapping-hash',
  pinned_losses_hash: 'losses-hash',
  protocol: 'a2a',
  protocol_version: '1.0.1',
  direction: 'outbound',
  peer_authority: 'peer.example',
  remote_resource_ref: 'queue-1',
  attempt_id: 'attempt-1',
  generation: 1,
  synthetic_sid: 'sid-1',
  external_kind: 'task',
  external_id: 'remote-1',
  local_state: 'active',
  remote_state: 'working',
  observation_verdict: 'UNKNOWN',
  observation_code: 'not_observed',
  terminal: false,
  cancel_requested: false,
  last_command_id: 'command-1',
  last_event_id: 'event-1',
  last_event_seq: 1,
}

const reconcilePlan: ProtocolBindingReconcilePlan = {
  verdict: 'LIMPIO',
  code: 'binding_reconcile_planned',
  observed_at: '2026-08-20T00:00:00Z',
  checks: [],
  plan_hash: 'reconcile-plan',
  resource: binding,
  command: 'binding.reconcile',
  expected_etag: '"v7"',
  row_effects: ['sessions.protocol_binding:update'],
  event_type: 'work.binding.observed',
  audit_action: 'sessions.work.binding.reconcile',
  permission: 'sessions:protocol-binding:write',
  external_calls: ['protocol_binding.get'],
}

const assessment = (
  verdict: ProtocolBindingAssessment['verdict'],
): ProtocolBindingAssessment => ({
  verdict,
  code: verdict === 'ROTO' ? 'remote_drift' : 'observation_unavailable',
  observed_at: '2026-08-20T00:01:00Z',
  checks: [{ name: 'remote_state', verdict }],
  plan_hash: 'reconcile-plan',
  resource: binding,
})

describe('reconcile flow', () => {
  it('allows a BROKEN remote test to be recorded as evidence without relabeling it clean', async () => {
    const onApplied = vi.fn()
    vi.spyOn(api, 'getProtocolBinding').mockResolvedValue({
      binding,
      etag: '"v7"',
    })
    vi.spyOn(api, 'reconcileProtocolBinding')
      .mockResolvedValueOnce({
        ...assessment('LIMPIO'),
        plan_hash: '',
      } as never)
      .mockResolvedValueOnce(reconcilePlan as never)
      .mockResolvedValueOnce(assessment('ROTO') as never)
      .mockResolvedValueOnce({
        assessment: assessment('ROTO'),
        etag: '"v8"',
        replayed: false,
      } as never)
    render(
      <ProtocolBindingReconcileDialog
        open
        bindingId="binding-1"
        request={TENANT_REQUEST}
        onOpenChange={() => {}}
        onApplied={onApplied}
      />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Test remote state' }),
    )
    expect(
      await screen.findByRole('button', { name: 'Record observation' }),
    ).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'Record observation' }),
    )
    await waitFor(() => expect(onApplied).toHaveBeenCalledOnce())
    expect(
      vi
        .mocked(api.reconcileProtocolBinding)
        .mock.calls.every((call) => call[3] === TENANT_REQUEST),
    ).toBe(true)
    expect(screen.getAllByText('Broken').length).toBeGreaterThan(0)
  })

  it('blocks apply when the remote test could not observe', async () => {
    vi.spyOn(api, 'getProtocolBinding').mockResolvedValue({
      binding,
      etag: '"v7"',
    })
    vi.spyOn(api, 'reconcileProtocolBinding')
      .mockResolvedValueOnce({
        ...assessment('LIMPIO'),
        plan_hash: '',
      } as never)
      .mockResolvedValueOnce(reconcilePlan as never)
      .mockResolvedValueOnce(assessment('NO_HE_PODIDO_MIRAR') as never)
    render(
      <ProtocolBindingReconcileDialog
        open
        bindingId="binding-1"
        request={TENANT_REQUEST}
        onOpenChange={() => {}}
        onApplied={() => {}}
      />,
    )
    await userEvent.click(
      await screen.findByRole('button', { name: 'Test remote state' }),
    )
    expect(
      await screen.findByText('No state claim was made'),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Record observation' }),
    ).not.toBeInTheDocument()
  })
})
