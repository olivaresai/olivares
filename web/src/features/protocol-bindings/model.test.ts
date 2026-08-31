// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  activationBlocker,
  buildProtocolComposerExport,
  buildProtocolBindingSpecInput,
  defaultProtocolMapping,
  defaultProtocolComposerDraft,
  isPinnedVersionRef,
  protocolComposerMappingIssues,
  protocolMappingCoverage,
  protocolMappingOptions,
  protocolMappingTargetsForSource,
  protocolMappingTransforms,
  protocolPlanMatchesApplied,
  successorProtocolComposerDraft,
} from './model'
import type { ProtocolBindingSpec, ProtocolBindingSpecPlan } from './types'

const cleanSpec = (
  overrides: Partial<ProtocolBindingSpec> = {},
): ProtocolBindingSpec => ({
  id: '018f0000-0000-7000-8000-000000000001',
  tenant_id: '018f0000-0000-7000-8000-000000000002',
  workspace_id: '018f0000-0000-7000-8000-000000000003',
  version: 1,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
  binding_key: 'peer-work',
  generation: 1,
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
  mapping_hash: 'mapping',
  known_losses: [],
  losses_hash: 'losses',
  rule_refs: [],
  permission_profile_ref: 'profile:standard',
  currency_policy: 'pinned',
  validation: {
    verdict: 'CLEAN',
    code: 'server_witness',
    observed_at: '2026-08-20T00:00:00Z',
  },
  state: 'draft',
  spec_hash: 'spec',
  plan_hash: 'plan',
  ...overrides,
})

describe('composer input authority', () => {
  it('uses only fields from the executable backend mapping catalog', () => {
    expect(defaultProtocolComposerDraft().mapping).toEqual([
      {
        source: 'work.title',
        target: 'message.text',
        cardinality: 'one_to_one',
        transform: 'text',
      },
    ])
    expect(defaultProtocolMapping('a2a', 'inbound', 'work_item')).toEqual([
      {
        source: 'message.text',
        target: 'work.brief',
        cardinality: 'one_to_one',
        transform: 'text',
      },
    ])
    expect(defaultProtocolMapping('mcp', 'outbound', 'work_item')).toEqual([
      {
        source: 'task.summary',
        target: 'work.brief',
        cardinality: 'one_to_one',
        transform: 'text',
      },
    ])
    expect(protocolMappingOptions('mcp', 'inbound', 'work_item')).toEqual({
      sources: [],
      targets: [],
    })
    expect(
      protocolMappingTargetsForSource(
        'a2a',
        'bidirectional',
        'work_item',
        'work.title',
      ),
    ).toContain('message.text')
    expect(
      protocolMappingTargetsForSource(
        'a2a',
        'bidirectional',
        'work_item',
        'work.title',
      ),
    ).not.toContain('work.brief')
    expect(
      protocolMappingTransforms(
        'a2a',
        'outbound',
        'work_item',
        'work.title',
        'message.text',
        'one_to_one',
      ),
    ).toContain('text')
  })

  it('reports required mapping coverage separately from accepted loss evidence', () => {
    const draft = defaultProtocolComposerDraft()
    expect(
      protocolMappingCoverage(draft)
        .filter((row) => row.required)
        .map((row) => [row.field, row.status]),
    ).toEqual([
      ['work.title', 'mapped'],
      ['message.text', 'mapped'],
    ])

    draft.mapping = []
    draft.knownLosses = [
      {
        field: 'work.title',
        reason_code: 'not_transmitted',
        accepted: true,
        acceptance_ref: 'decision:42',
      },
      {
        field: 'message.text',
        reason_code: 'not_transmitted',
        accepted: false,
      },
    ]
    expect(
      protocolMappingCoverage(draft)
        .filter((row) => row.required)
        .map((row) => [row.field, row.status]),
    ).toEqual([
      ['work.title', 'accepted_loss'],
      ['message.text', 'declared_loss'],
    ])
    expect(protocolComposerMappingIssues(draft)).toContain(
      'required_mapping_or_loss_missing',
    )
  })

  it('omits the read-only witness instead of manufacturing CLEAN or UNKNOWN', () => {
    const draft = defaultProtocolComposerDraft()
    draft.bindingKey = 'peer-work'
    draft.localRef = 'work-1'
    draft.peerAuthority = 'peer.example'
    draft.remoteResourceRef = 'queue-1'
    draft.permissionProfileRef = 'profile:standard'

    const input = buildProtocolBindingSpecInput('workspace-1', draft)
    expect(input).not.toHaveProperty('validation')
    expect(input.currency_policy).toBe('pinned')
  })

  it('rejects floating protocol aliases while accepting exact versions', () => {
    expect(isPinnedVersionRef('latest')).toBe(false)
    expect(isPinnedVersionRef('current')).toBe(false)
    expect(isPinnedVersionRef('*')).toBe(false)
    expect(isPinnedVersionRef('2026-07-28')).toBe(true)
  })

  it('normalizes rule refs and emits loss acceptance evidence only when accepted', () => {
    const draft = defaultProtocolComposerDraft()
    draft.ruleRefsText = 'policy:a\npolicy:a\n policy:b '
    draft.knownLosses = [
      {
        field: 'history',
        reason_code: 'unsupported',
        accepted: false,
        acceptance_ref: 'stale-evidence',
      },
      {
        field: 'metadata',
        reason_code: 'filtered',
        accepted: true,
        acceptance_ref: ' decision-42 ',
      },
    ]

    const input = buildProtocolBindingSpecInput('workspace-1', draft)
    expect(input.rule_refs).toEqual(['policy:a', 'policy:b'])
    expect(input.known_losses).toEqual([
      {
        field: 'history',
        reason_code: 'unsupported',
        accepted: false,
        acceptance_ref: undefined,
      },
      {
        field: 'metadata',
        reason_code: 'filtered',
        accepted: true,
        acceptance_ref: 'decision-42',
      },
    ])
  })

  it('derives a successor without copying the predecessor witness', () => {
    const draft = successorProtocolComposerDraft(
      cleanSpec({ id: 'spec-active', generation: 4, state: 'active' }),
    )
    expect(draft).toMatchObject({
      bindingKey: 'peer-work',
      generation: 5,
      supersedesId: 'spec-active',
      localRef: 'work-1',
    })
    expect(draft).not.toHaveProperty('validation')
    expect(
      buildProtocolBindingSpecInput(
        '018f0000-0000-7000-8000-000000000003',
        draft,
      ),
    ).not.toHaveProperty('validation')
  })

  it('exports only desired input plus verbatim server validation and plan', () => {
    const draft = defaultProtocolComposerDraft()
    const serverPlan: ProtocolBindingSpecPlan = {
      verdict: 'CLEAN',
      code: 'draft_planned',
      validation: {
        verdict: 'CLEAN',
        code: 'capability_validated',
        observed_at: '2026-08-20T00:00:00Z',
      },
      plan_hash: 'plan',
      operation: 'draft',
      workspace_id: 'workspace-1',
      generation: 1,
      spec_hash: 'spec',
      mapping_hash: 'mapping',
      losses_hash: 'losses',
    }
    const exported = buildProtocolComposerExport(
      'workspace-1',
      draft,
      { ...serverPlan, plan_hash: '' },
      serverPlan,
    )
    expect(exported.desired_spec).not.toHaveProperty('validation')
    expect(exported.server_validation?.validation).toEqual(
      serverPlan.validation,
    )
    expect(exported.server_plan).toEqual(serverPlan)
    expect(exported.apply_outcome).toBeNull()
  })

  it('refuses to treat an apply response with a changed pinned hash as the plan', () => {
    const serverPlan: ProtocolBindingSpecPlan = {
      verdict: 'CLEAN',
      code: 'draft_planned',
      validation: { verdict: 'UNKNOWN', code: 'not_observed' },
      plan_hash: 'plan',
      operation: 'draft',
      workspace_id: 'workspace-1',
      generation: 1,
      spec_hash: 'spec',
      mapping_hash: 'mapping',
      losses_hash: 'losses',
    }
    const result = {
      ...serverPlan,
      mapping_hash: 'changed',
      spec: cleanSpec(),
    }
    expect(protocolPlanMatchesApplied(serverPlan, result)).toBe(false)

    const successorPlan = { ...serverPlan, prior_active_id: 'spec-active' }
    expect(
      protocolPlanMatchesApplied(successorPlan, {
        ...successorPlan,
        prior_active_id: undefined,
        spec: cleanSpec({ supersedes_id: 'spec-active' }),
      }),
    ).toBe(true)
  })
})

describe('activation preconditions', () => {
  it('allows only a timestamped server CLEAN witness', () => {
    expect(activationBlocker(cleanSpec())).toBeNull()
    expect(
      activationBlocker(
        cleanSpec({ validation: { verdict: 'CLEAN', code: 'missing_time' } }),
      ),
    ).toBe('witness')
    expect(
      activationBlocker(
        cleanSpec({
          validation: {
            verdict: 'UNKNOWN',
            code: 'observation_unavailable',
          },
        }),
      ),
    ).toBe('witness')
  })

  it('blocks any loss lacking explicit accepted evidence', () => {
    expect(
      activationBlocker(
        cleanSpec({
          known_losses: [
            {
              field: 'history',
              reason_code: 'unsupported',
              accepted: true,
            },
          ],
        }),
      ),
    ).toBe('losses')
  })
})
