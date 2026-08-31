// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type {
  BindingDirection,
  BindingLocalKind,
  BindingProtocol,
  ProtocolMappingRule,
  ProtocolBindingSpec,
  ProtocolBindingSpecInput,
  ProtocolBindingSpecPlan,
  ProtocolBindingSpecResult,
  ProtocolComposerDraft,
  ProtocolMappingCardinality,
  ProtocolMappingTransform,
  ProtocolSpecApplyOutcome,
} from './types'

export const DEFAULT_A2A_VERSION = '1.0.1'
export const DEFAULT_MCP_VERSION = '2026-07-28'
export const PROTOCOL_MAPPING_SCHEMA_V1 = 'olivares.protocol-binding/v1'

export type ProtocolMappingValueKind =
  'text' | 'reference' | 'metadata' | 'status'

export interface ProtocolMappingCatalogField {
  name: string
  kind: ProtocolMappingValueKind
  required: boolean
}

export interface ProtocolMappingCatalogRoute {
  direction: Exclude<BindingDirection, 'bidirectional'>
  sources: ProtocolMappingCatalogField[]
  targets: ProtocolMappingCatalogField[]
}

type CatalogField = Omit<ProtocolMappingCatalogField, 'required'>

const field = (name: string, kind: ProtocolMappingValueKind): CatalogField => ({
  name,
  kind,
})

const LOCAL_FIELDS: Record<
  BindingLocalKind,
  { primary: string; sink: string; fields: CatalogField[] }
> = {
  work_item: {
    primary: 'work.title',
    sink: 'work.brief',
    fields: [
      field('work.id', 'reference'),
      field('work.workspace_id', 'reference'),
      field('work.title', 'text'),
      field('work.brief', 'text'),
      field('work.status', 'status'),
      field('work.kind', 'text'),
      field('work.owner_ref', 'reference'),
      field('work.priority', 'text'),
      field('work.context_refs', 'metadata'),
    ],
  },
  agent: {
    primary: 'agent.name',
    sink: 'agent.metadata',
    fields: [
      field('agent.id', 'reference'),
      field('agent.workspace_id', 'reference'),
      field('agent.name', 'text'),
      field('agent.kind', 'text'),
      field('agent.status', 'status'),
      field('agent.external_id', 'reference'),
      field('agent.identity_id', 'reference'),
      field('agent.labels', 'metadata'),
      field('agent.metadata', 'metadata'),
    ],
  },
  model: {
    primary: 'model.name',
    sink: 'model.metadata',
    fields: [
      field('model.id', 'reference'),
      field('model.name', 'text'),
      field('model.family', 'text'),
      field('model.status', 'status'),
      field('model.provider_id', 'reference'),
      field('model.modality', 'text'),
      field('model.context_window', 'text'),
      field('model.metadata', 'metadata'),
    ],
  },
  channel: {
    primary: 'channel.name',
    sink: 'channel.metadata',
    fields: [
      field('channel.id', 'reference'),
      field('channel.workspace_id', 'reference'),
      field('channel.name', 'text'),
      field('channel.description', 'text'),
      field('channel.kind', 'text'),
      field('channel.status', 'status'),
      field('channel.sensitivity', 'text'),
      field('channel.retention_policy_ref', 'reference'),
      field('channel.metadata', 'metadata'),
    ],
  },
}

const A2A_FIELDS = [
  field('message.text', 'text'),
  field('message.parts', 'text'),
  field('message.id', 'reference'),
  field('message.context_id', 'reference'),
  field('message.reference', 'reference'),
  field('message.metadata', 'metadata'),
  field('message.status', 'status'),
  field('peer.subject', 'reference'),
]
const MCP_FIELDS = [
  field('task.id', 'reference'),
  field('task.tool', 'text'),
  field('task.summary', 'text'),
  field('task.status', 'status'),
  field('task.owner', 'metadata'),
  field('task.required_scope', 'reference'),
  field('task.origin_ref', 'reference'),
  field('task.metadata', 'metadata'),
]

function requiredFields(
  values: CatalogField[],
  required: string,
): ProtocolMappingCatalogField[] {
  return values.map((value) => ({
    ...value,
    required: value.name === required,
  }))
}

/** Exact browser mirror of protocolMappingRoutes in communication_binding_runtime.go. */
export function protocolMappingCatalogRoutes(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
): ProtocolMappingCatalogRoute[] {
  const local = LOCAL_FIELDS[localKind]
  const routes: ProtocolMappingCatalogRoute[] = []
  if (protocol === 'a2a' && direction !== 'inbound') {
    routes.push({
      direction: 'outbound',
      sources: requiredFields(local.fields, local.primary),
      targets: requiredFields(A2A_FIELDS, 'message.text'),
    })
  }
  if (protocol === 'a2a' && direction !== 'outbound') {
    routes.push({
      direction: 'inbound',
      sources: requiredFields(A2A_FIELDS, 'message.text'),
      targets: requiredFields(local.fields, local.sink),
    })
  }
  if (protocol === 'mcp' && direction === 'outbound') {
    routes.push({
      direction: 'outbound',
      sources: requiredFields(MCP_FIELDS, 'task.summary'),
      targets: requiredFields(local.fields, local.sink),
    })
  }
  return routes
}

export function protocolMappingOptions(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
): { sources: string[]; targets: string[] } {
  const routes = protocolMappingCatalogRoutes(protocol, direction, localKind)
  return {
    sources: Array.from(
      new Set(routes.flatMap((route) => route.sources.map(({ name }) => name))),
    ),
    targets: Array.from(
      new Set(routes.flatMap((route) => route.targets.map(({ name }) => name))),
    ),
  }
}

export function protocolMappingTargetsForSource(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
  source: string,
): string[] {
  return Array.from(
    new Set(
      protocolMappingCatalogRoutes(protocol, direction, localKind)
        .filter((route) => route.sources.some((item) => item.name === source))
        .flatMap((route) => route.targets.map((item) => item.name)),
    ),
  )
}

export function protocolMappingSourcesForTarget(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
  target: string,
): string[] {
  return Array.from(
    new Set(
      protocolMappingCatalogRoutes(protocol, direction, localKind)
        .filter((route) => route.targets.some((item) => item.name === target))
        .flatMap((route) => route.sources.map((item) => item.name)),
    ),
  )
}

function mappingFieldKind(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
  role: 'source' | 'target',
  name: string,
): ProtocolMappingValueKind | null {
  for (const route of protocolMappingCatalogRoutes(
    protocol,
    direction,
    localKind,
  )) {
    const value = route[role === 'source' ? 'sources' : 'targets'].find(
      (item) => item.name === name,
    )
    if (value) return value.kind
  }
  return null
}

export function protocolMappingTransforms(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
  source: string,
  target: string,
  cardinality: ProtocolMappingCardinality,
): ProtocolMappingTransform[] {
  if (
    !protocolMappingTargetsForSource(
      protocol,
      direction,
      localKind,
      source,
    ).includes(target)
  )
    return []
  const sourceKind = mappingFieldKind(
    protocol,
    direction,
    localKind,
    'source',
    source,
  )
  const targetKind = mappingFieldKind(
    protocol,
    direction,
    localKind,
    'target',
    target,
  )
  if (!sourceKind || !targetKind) return []
  const values: ProtocolMappingTransform[] = []
  if (sourceKind === targetKind && cardinality === 'one_to_one')
    values.push('identity')
  if (targetKind === 'text') values.push('text')
  if (
    targetKind === 'reference' &&
    (sourceKind === 'text' || sourceKind === 'reference') &&
    cardinality !== 'many_to_one'
  )
    values.push('reference')
  if (
    sourceKind === 'metadata' &&
    targetKind === 'metadata' &&
    cardinality !== 'one_to_many'
  )
    values.push('metadata')
  if (
    sourceKind === 'status' &&
    targetKind === 'status' &&
    cardinality === 'one_to_one'
  )
    values.push('status')
  return values
}

export function protocolMappingCardinalities(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
  source: string,
  target: string,
  transform: ProtocolMappingTransform,
): ProtocolMappingCardinality[] {
  return (['one_to_one', 'one_to_many', 'many_to_one'] as const).filter(
    (cardinality) =>
      protocolMappingTransforms(
        protocol,
        direction,
        localKind,
        source,
        target,
        cardinality,
      ).includes(transform),
  )
}

export function defaultProtocolMapping(
  protocol: BindingProtocol,
  direction: BindingDirection,
  localKind: BindingLocalKind,
): ProtocolMappingRule[] {
  const local = LOCAL_FIELDS[localKind]
  if (protocol === 'mcp') {
    if (direction !== 'outbound') return []
    if (!local.sink.endsWith('.metadata')) {
      return [
        {
          source: 'task.summary',
          target: local.sink,
          cardinality: 'one_to_one',
          transform: 'text',
        },
      ]
    }
    return [
      {
        source: 'task.summary',
        target: local.primary,
        cardinality: 'one_to_one',
        transform: 'text',
      },
      {
        source: 'task.metadata',
        target: local.sink,
        cardinality: 'one_to_one',
        transform: 'metadata',
      },
    ]
  }
  const outbound: ProtocolMappingRule = {
    source: local.primary,
    target: 'message.text',
    cardinality: 'one_to_one',
    transform: 'text',
  }
  const inbound: ProtocolMappingRule[] = local.sink.endsWith('.metadata')
    ? [
        {
          source: 'message.text',
          target: local.primary,
          cardinality: 'one_to_one',
          transform: 'text',
        },
        {
          source: 'message.metadata',
          target: local.sink,
          cardinality: 'one_to_one',
          transform: 'metadata',
        },
      ]
    : [
        {
          source: 'message.text',
          target: local.sink,
          cardinality: 'one_to_one',
          transform: 'text',
        },
      ]
  if (direction === 'outbound') return [outbound]
  if (direction === 'inbound') return inbound
  return [outbound, ...inbound]
}

export function defaultProtocolComposerDraft(): ProtocolComposerDraft {
  return {
    bindingKey: '',
    generation: 1,
    supersedesId: '',
    protocol: 'a2a',
    protocolVersion: DEFAULT_A2A_VERSION,
    direction: 'outbound',
    peerAuthority: '',
    localKind: 'work_item',
    localRef: '',
    remoteResourceKind: 'agent',
    remoteResourceRef: '',
    mappingSchema: PROTOCOL_MAPPING_SCHEMA_V1,
    mapping: defaultProtocolMapping('a2a', 'outbound', 'work_item'),
    knownLosses: [],
    ruleRefsText: '',
    permissionProfileRef: '',
  }
}

export function successorProtocolComposerDraft(
  predecessor: ProtocolBindingSpec,
): ProtocolComposerDraft {
  const localRef =
    typeof predecessor.local_selector.id === 'string'
      ? predecessor.local_selector.id
      : ''
  return {
    bindingKey: predecessor.binding_key,
    generation: predecessor.generation + 1,
    supersedesId: predecessor.id,
    protocol: predecessor.protocol,
    protocolVersion: predecessor.protocol_version,
    direction: predecessor.direction,
    peerAuthority: predecessor.peer_authority,
    localKind: predecessor.local_kind,
    localRef,
    remoteResourceKind: predecessor.remote_resource_kind,
    remoteResourceRef: predecessor.remote_resource_ref,
    mappingSchema: predecessor.mapping_schema,
    mapping: predecessor.mapping.map((rule) => ({ ...rule })),
    knownLosses: predecessor.known_losses.map((loss) => ({ ...loss })),
    ruleRefsText: predecessor.rule_refs.join('\n'),
    permissionProfileRef: predecessor.permission_profile_ref,
  }
}

export type ProtocolMappingCoverageStatus =
  'mapped' | 'accepted_loss' | 'declared_loss' | 'missing' | 'optional'

export interface ProtocolMappingCoverage {
  direction: Exclude<BindingDirection, 'bidirectional'>
  role: 'source' | 'target'
  field: string
  kind: ProtocolMappingValueKind
  required: boolean
  status: ProtocolMappingCoverageStatus
}

function ruleMatchesRoute(
  rule: ProtocolMappingRule,
  route: ProtocolMappingCatalogRoute,
): boolean {
  return (
    route.sources.some((item) => item.name === rule.source) &&
    route.targets.some((item) => item.name === rule.target)
  )
}

export function protocolMappingCoverage(
  draft: ProtocolComposerDraft,
): ProtocolMappingCoverage[] {
  const acceptedLosses = new Set(
    draft.knownLosses
      .filter((loss) => loss.accepted && loss.acceptance_ref?.trim())
      .map((loss) => loss.field),
  )
  const declaredLosses = new Set(draft.knownLosses.map((loss) => loss.field))
  return protocolMappingCatalogRoutes(
    draft.protocol,
    draft.direction,
    draft.localKind,
  ).flatMap((route) =>
    (['source', 'target'] as const).flatMap((role) =>
      route[role === 'source' ? 'sources' : 'targets'].map((item) => {
        const mapped = draft.mapping.some(
          (rule) => ruleMatchesRoute(rule, route) && rule[role] === item.name,
        )
        let status: ProtocolMappingCoverageStatus = 'optional'
        if (mapped) status = 'mapped'
        else if (acceptedLosses.has(item.name)) status = 'accepted_loss'
        else if (declaredLosses.has(item.name)) status = 'declared_loss'
        else if (item.required) status = 'missing'
        return {
          direction: route.direction,
          role,
          field: item.name,
          kind: item.kind,
          required: item.required,
          status,
        }
      }),
    ),
  )
}

export type ProtocolComposerMappingIssue =
  | 'mapping_field_not_in_schema'
  | 'mapping_target_duplicated'
  | 'mapping_transform_not_allowed'
  | 'loss_field_not_in_schema'
  | 'required_mapping_or_loss_missing'

export function protocolComposerMappingIssues(
  draft: ProtocolComposerDraft,
): ProtocolComposerMappingIssue[] {
  const issues = new Set<ProtocolComposerMappingIssue>()
  const targets = new Set<string>()
  for (const rule of draft.mapping) {
    const transforms = protocolMappingTransforms(
      draft.protocol,
      draft.direction,
      draft.localKind,
      rule.source,
      rule.target,
      rule.cardinality,
    )
    if (transforms.length === 0) issues.add('mapping_field_not_in_schema')
    else if (!transforms.includes(rule.transform))
      issues.add('mapping_transform_not_allowed')
    if (targets.has(rule.target)) issues.add('mapping_target_duplicated')
    targets.add(rule.target)
  }
  const catalogFields = new Set(
    protocolMappingCatalogRoutes(
      draft.protocol,
      draft.direction,
      draft.localKind,
    ).flatMap((route) => [
      ...route.sources.map((item) => item.name),
      ...route.targets.map((item) => item.name),
    ]),
  )
  if (draft.knownLosses.some((loss) => !catalogFields.has(loss.field)))
    issues.add('loss_field_not_in_schema')
  if (
    protocolMappingCoverage(draft).some(
      (item) =>
        item.required &&
        item.status !== 'mapped' &&
        item.status !== 'accepted_loss',
    )
  )
    issues.add('required_mapping_or_loss_missing')
  return [...issues]
}

export interface ProtocolPlanDiffRow {
  field:
    | 'operation'
    | 'workspace_id'
    | 'generation'
    | 'prior_active_id'
    | 'plan_hash'
    | 'spec_hash'
    | 'mapping_hash'
    | 'losses_hash'
  planned: string
  applied?: string
  matches?: boolean
}

export function protocolPlanDiff(
  plan: ProtocolBindingSpecPlan,
  applied?: ProtocolBindingSpecResult,
): ProtocolPlanDiffRow[] {
  const rows: Array<{
    field: ProtocolPlanDiffRow['field']
    planned: string | number | undefined
    applied: string | number | undefined
  }> = [
    {
      field: 'operation',
      planned: plan.operation,
      applied: applied?.operation,
    },
    {
      field: 'workspace_id',
      planned: plan.workspace_id,
      applied: applied?.workspace_id,
    },
    {
      field: 'generation',
      planned: plan.generation,
      applied: applied?.generation,
    },
    {
      field: 'prior_active_id',
      planned: plan.prior_active_id,
      applied: applied
        ? (applied.prior_active_id ?? applied.spec.supersedes_id)
        : undefined,
    },
    {
      field: 'plan_hash',
      planned: plan.plan_hash,
      applied: applied?.plan_hash,
    },
    {
      field: 'spec_hash',
      planned: plan.spec_hash,
      applied: applied?.spec_hash,
    },
    {
      field: 'mapping_hash',
      planned: plan.mapping_hash,
      applied: applied?.mapping_hash,
    },
    {
      field: 'losses_hash',
      planned: plan.losses_hash,
      applied: applied?.losses_hash,
    },
  ]
  return rows.map((row) => {
    const planned = String(row.planned ?? '')
    if (!applied) return { field: row.field, planned }
    const appliedValue = String(row.applied ?? '')
    return {
      field: row.field,
      planned,
      applied: appliedValue,
      matches: planned === appliedValue,
    }
  })
}

export function protocolPlanMatchesApplied(
  plan: ProtocolBindingSpecPlan,
  applied: ProtocolBindingSpecResult,
): boolean {
  return protocolPlanDiff(plan, applied).every((row) => row.matches)
}

export function buildProtocolComposerExport(
  workspaceId: string,
  draft: ProtocolComposerDraft,
  validation: ProtocolBindingSpecPlan | null,
  plan: ProtocolBindingSpecPlan | null,
  outcome: ProtocolSpecApplyOutcome | null = null,
) {
  return {
    schema: 'olivares.protocol-binding-composer/export-v1',
    desired_spec: buildProtocolBindingSpecInput(workspaceId, draft),
    // These values are included only after the server issued them. The export never
    // derives an observation verdict or timestamp in the browser.
    server_validation: validation,
    server_plan: plan,
    apply_outcome: outcome,
  }
}

export function versionForProtocol(protocol: 'a2a' | 'mcp'): string {
  return protocol === 'a2a' ? DEFAULT_A2A_VERSION : DEFAULT_MCP_VERSION
}

export function isPinnedVersionRef(value: string): boolean {
  const normalized = value.trim().toLowerCase()
  return (
    !!normalized &&
    normalized !== 'latest' &&
    normalized !== 'current' &&
    normalized !== '*'
  )
}

export function buildProtocolBindingSpecInput(
  workspaceId: string,
  draft: ProtocolComposerDraft,
): ProtocolBindingSpecInput {
  return {
    workspace_id: workspaceId,
    binding_key: draft.bindingKey.trim(),
    generation: draft.generation,
    protocol: draft.protocol,
    protocol_version: draft.protocolVersion.trim(),
    direction: draft.direction,
    local_kind: draft.localKind,
    local_selector: { id: draft.localRef.trim() },
    peer_authority: draft.peerAuthority.trim(),
    remote_resource_kind: draft.remoteResourceKind.trim(),
    remote_resource_ref: draft.remoteResourceRef.trim(),
    mapping_schema: draft.mappingSchema.trim(),
    mapping: draft.mapping.map((rule) => ({
      ...rule,
      source: rule.source.trim(),
      target: rule.target.trim(),
    })),
    known_losses: draft.knownLosses.map((loss) => ({
      ...loss,
      field: loss.field.trim(),
      reason_code: loss.reason_code.trim(),
      acceptance_ref: loss.accepted
        ? loss.acceptance_ref?.trim() || undefined
        : undefined,
    })),
    rule_refs: Array.from(
      new Set(
        draft.ruleRefsText
          .split(/\r?\n/)
          .map((value) => value.trim())
          .filter(Boolean),
      ),
    ),
    permission_profile_ref: draft.permissionProfileRef.trim(),
    currency_policy: 'pinned',
    // There is deliberately no validation field. It is read-only authority issued by
    // the engine and returned separately on validate/plan and the persisted spec.
    supersedes_id: draft.supersedesId.trim() || undefined,
  }
}

export type ActivationBlocker = 'state' | 'witness' | 'currency' | 'losses'

export function activationBlocker(
  spec: ProtocolBindingSpec,
): ActivationBlocker | null {
  if (spec.state !== 'draft') return 'state'
  if (spec.validation.verdict !== 'CLEAN' || !spec.validation.observed_at)
    return 'witness'
  if (spec.currency_policy !== 'pinned') return 'currency'
  if (
    spec.known_losses.some(
      (loss) => !loss.accepted || !loss.acceptance_ref?.trim(),
    )
  )
    return 'losses'
  return null
}

export function canDisableSpec(spec: ProtocolBindingSpec): boolean {
  return spec.state === 'draft' || spec.state === 'active'
}
