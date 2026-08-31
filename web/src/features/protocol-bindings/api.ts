// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError } from '@/lib/api/errors'
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type {
  AssessmentVerdict,
  ListProtocolBindingsParams,
  ListProtocolBindingSpecsParams,
  ProtocolBinding,
  ProtocolBindingAssessment,
  ProtocolBindingReconcilePlan,
  ProtocolBindingSpec,
  ProtocolBindingSpecInput,
  ProtocolBindingSpecOperation,
  ProtocolBindingSpecPlan,
  ProtocolBindingSpecResult,
  ProtocolObservationVerdict,
  ProtocolPage,
  ProtocolReconcileMode,
  ProtocolReconcileOutcome,
  ProtocolSpecApplyOutcome,
  ProtocolSpecMode,
} from './types'

// Keep these paths as direct string literals. The console-route census resolves literal
// module paths but deliberately does not execute nested constant interpolation.
const SPECS = '/v1/m/sessions/protocol-binding-specs'
const BINDINGS = '/v1/m/sessions/protocol-bindings'
const encodeId = (value: string) => encodeURIComponent(value)

export const protocolBindingKeys = {
  // ⛔ TAMBIÉN EL PREFIJO. Era `['protocol-bindings']` a secas, y sus cuatro hermanas sí llevan
  // el inquilino: la invalidación alcanzaba las consultas de TODOS los inquilinos en caché, no
  // sólo las del activo. No filtra dato —es un prefijo, no una lectura—, pero el «todo» de una
  // fábrica por inquilino no puede ser el de todos.
  all: (tenant: string | null) => ['protocol-bindings', tenant] as const,
  specs: (tenant: string | null, params: ListProtocolBindingSpecsParams) =>
    ['protocol-bindings', tenant, 'specs', params] as const,
  spec: (tenant: string | null, id: string) =>
    ['protocol-bindings', tenant, 'spec', id] as const,
  bindings: (tenant: string | null, params: ListProtocolBindingsParams) =>
    ['protocol-bindings', tenant, 'instances', params] as const,
  binding: (tenant: string | null, id: string) =>
    ['protocol-bindings', tenant, 'instance', id] as const,
}

function queryOf(
  params: ListProtocolBindingSpecsParams | ListProtocolBindingsParams,
): Record<string, string | number | boolean | undefined> {
  return { ...params }
}

export function listProtocolBindingSpecs(
  params: ListProtocolBindingSpecsParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ProtocolPage<ProtocolBindingSpec>> {
  return http.get<ProtocolPage<ProtocolBindingSpec>>(SPECS, {
    ...options,
    query: queryOf(params),
    signal,
  })
}

export async function getProtocolBindingSpec(
  specId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<{ spec: ProtocolBindingSpec; etag: string | null }> {
  const { data, headers } = await http.getWithMeta<ProtocolBindingSpec>(
    `${SPECS}/${encodeId(specId)}`,
    { ...options, signal },
  )
  return { spec: data, etag: headers.get('ETag') }
}

export async function runProtocolBindingSpecCreate(
  input: ProtocolBindingSpecInput,
  mode: 'validate' | 'plan',
  options: TenantRequestOptions,
): Promise<ProtocolBindingSpecPlan>
export async function runProtocolBindingSpecCreate(
  input: ProtocolBindingSpecInput,
  mode: 'apply',
  options: TenantRequestOptions,
  intent: { idempotencyKey: string; planHash: string },
): Promise<ProtocolSpecApplyOutcome>
export async function runProtocolBindingSpecCreate(
  input: ProtocolBindingSpecInput,
  mode: ProtocolSpecMode,
  options: TenantRequestOptions,
  intent?: { idempotencyKey: string; planHash: string },
): Promise<ProtocolBindingSpecPlan | ProtocolSpecApplyOutcome> {
  if (mode !== 'apply') {
    return http.post<ProtocolBindingSpecPlan>(SPECS, input, {
      ...options,
      query: { mode },
    })
  }
  if (!intent) throw new Error('apply requires a pinned intention and plan')
  const { data, headers } = await http.postWithMeta<ProtocolBindingSpecResult>(
    SPECS,
    input,
    {
      ...options,
      query: { mode },
      headers: {
        'Idempotency-Key': intent.idempotencyKey,
        'If-Plan-Hash': intent.planHash,
      },
    },
  )
  return {
    result: data,
    etag: headers.get('ETag'),
    replayed: headers.get('Idempotency-Replayed') === 'true',
  }
}

export async function runProtocolBindingSpecTransition(
  specId: string,
  operation: Exclude<ProtocolBindingSpecOperation, 'draft'>,
  mode: 'validate' | 'plan',
  etag: string | null,
  options: TenantRequestOptions,
): Promise<ProtocolBindingSpecPlan>
export async function runProtocolBindingSpecTransition(
  specId: string,
  operation: Exclude<ProtocolBindingSpecOperation, 'draft'>,
  mode: 'apply',
  etag: string | null,
  options: TenantRequestOptions,
  intent: { idempotencyKey: string; planHash: string },
): Promise<ProtocolSpecApplyOutcome>
export async function runProtocolBindingSpecTransition(
  specId: string,
  operation: Exclude<ProtocolBindingSpecOperation, 'draft'>,
  mode: ProtocolSpecMode,
  etag: string | null,
  options: TenantRequestOptions,
  intent?: { idempotencyKey: string; planHash: string },
): Promise<ProtocolBindingSpecPlan | ProtocolSpecApplyOutcome> {
  const encodedSpecId = encodeId(specId)
  const headers: Record<string, string> = {}
  if (etag) headers['If-Match'] = etag
  if (mode === 'apply') {
    if (!intent) throw new Error('apply requires a pinned intention and plan')
    headers['Idempotency-Key'] = intent.idempotencyKey
    headers['If-Plan-Hash'] = intent.planHash
    const response = await (operation === 'activate'
      ? http.postWithMeta<ProtocolBindingSpecResult>(
          `${SPECS}/${encodedSpecId}/activate`,
          undefined,
          { ...options, query: { mode }, headers },
        )
      : http.postWithMeta<ProtocolBindingSpecResult>(
          `${SPECS}/${encodedSpecId}/disable`,
          undefined,
          { ...options, query: { mode }, headers },
        ))
    return {
      result: response.data,
      etag: response.headers.get('ETag'),
      replayed: response.headers.get('Idempotency-Replayed') === 'true',
    }
  }
  return operation === 'activate'
    ? http.post<ProtocolBindingSpecPlan>(
        `${SPECS}/${encodedSpecId}/activate`,
        undefined,
        { ...options, query: { mode }, headers },
      )
    : http.post<ProtocolBindingSpecPlan>(
        `${SPECS}/${encodedSpecId}/disable`,
        undefined,
        { ...options, query: { mode }, headers },
      )
}

export function listProtocolBindings(
  params: ListProtocolBindingsParams,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ProtocolPage<ProtocolBinding>> {
  return http.get<ProtocolPage<ProtocolBinding>>(BINDINGS, {
    ...options,
    query: queryOf(params),
    signal,
  })
}

export async function getProtocolBinding(
  bindingId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<{ binding: ProtocolBinding; etag: string | null }> {
  const { data, headers } = await http.getWithMeta<ProtocolBinding>(
    `${BINDINGS}/${encodeId(bindingId)}`,
    { ...options, signal },
  )
  return { binding: data, etag: headers.get('ETag') }
}

export async function reconcileProtocolBinding(
  bindingId: string,
  mode: 'validate',
  etag: string | null,
  options: TenantRequestOptions,
): Promise<ProtocolBindingAssessment>
export async function reconcileProtocolBinding(
  bindingId: string,
  mode: 'plan',
  etag: string | null,
  options: TenantRequestOptions,
): Promise<ProtocolBindingReconcilePlan>
export async function reconcileProtocolBinding(
  bindingId: string,
  mode: 'test',
  etag: string | null,
  options: TenantRequestOptions,
  intent: { planHash: string },
): Promise<ProtocolBindingAssessment>
export async function reconcileProtocolBinding(
  bindingId: string,
  mode: 'apply',
  etag: string | null,
  options: TenantRequestOptions,
  intent: { planHash: string; idempotencyKey: string },
): Promise<ProtocolReconcileOutcome>
export async function reconcileProtocolBinding(
  bindingId: string,
  mode: ProtocolReconcileMode,
  etag: string | null,
  options: TenantRequestOptions,
  intent?: { planHash: string; idempotencyKey?: string },
): Promise<
  | ProtocolBindingAssessment
  | ProtocolBindingReconcilePlan
  | ProtocolReconcileOutcome
> {
  const headers: Record<string, string> = {}
  if (etag) headers['If-Match'] = etag
  if (mode === 'test' || mode === 'apply') {
    if (!intent?.planHash) throw new Error(`${mode} requires a pinned plan`)
    headers['If-Plan-Hash'] = intent.planHash
  }
  if (mode === 'apply') {
    if (!intent?.idempotencyKey)
      throw new Error('apply requires a pinned intention')
    headers['Idempotency-Key'] = intent.idempotencyKey
    const response = await http.postWithMeta<ProtocolBindingAssessment>(
      `${BINDINGS}/${encodeId(bindingId)}/reconcile`,
      undefined,
      { ...options, query: { mode }, headers },
    )
    return {
      assessment: response.data,
      etag: response.headers.get('ETag'),
      replayed: response.headers.get('Idempotency-Replayed') === 'true',
    }
  }
  return http.post<ProtocolBindingAssessment | ProtocolBindingReconcilePlan>(
    `${BINDINGS}/${encodeId(bindingId)}/reconcile`,
    undefined,
    { ...options, query: { mode }, headers },
  )
}

type ProtocolErrorBody = {
  verdict?: ProtocolObservationVerdict | AssessmentVerdict
  code?: string
}

export function protocolVerdictOfError(
  error: unknown,
): ProtocolObservationVerdict | AssessmentVerdict | null {
  if (!(error instanceof ApiError)) return null
  const verdict = (error.body as ProtocolErrorBody | undefined)?.verdict
  return verdict === 'CLEAN' ||
    verdict === 'BROKEN' ||
    verdict === 'UNKNOWN' ||
    verdict === 'LIMPIO' ||
    verdict === 'ROTO' ||
    verdict === 'NO_HE_PODIDO_MIRAR'
    ? verdict
    : null
}

export function protocolErrorCode(error: unknown): string | null {
  if (!(error instanceof ApiError)) return null
  return (
    (error.body as ProtocolErrorBody | undefined)?.code ?? error.code ?? null
  )
}

export function isProtocolUnknown(value: unknown): boolean {
  const direct = (value as { verdict?: unknown } | null | undefined)?.verdict
  return (
    direct === 'UNKNOWN' ||
    direct === 'NO_HE_PODIDO_MIRAR' ||
    protocolVerdictOfError(value) === 'UNKNOWN' ||
    protocolVerdictOfError(value) === 'NO_HE_PODIDO_MIRAR'
  )
}

export function isProtocolConflict(error: unknown): boolean {
  return (
    error instanceof ApiError &&
    (error.status === 409 || error.status === 412 || error.status === 428)
  )
}
