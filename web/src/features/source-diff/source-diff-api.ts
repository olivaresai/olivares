// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import type { GitHostDiff, GitHostDiffQuery } from './source-diff-model'

export type DiffFailureKind =
  | 'badRequest'
  | 'unauthenticated'
  | 'forbidden'
  | 'hostForbidden'
  | 'unknownRef'
  | 'tooLarge'
  | 'unavailable'
  | 'upstream'
  | 'rateLimited'

const KIND_BY_CODE: Record<string, DiffFailureKind> = {
  bad_request: 'badRequest',
  unauthenticated: 'unauthenticated',
  forbidden: 'forbidden',
  git_host_forbidden: 'hostForbidden',
  unknown_ref: 'unknownRef',
  diff_too_large: 'tooLarge',
  git_host_diff_unavailable: 'unavailable',
  upstream: 'upstream',
  rate_limited: 'rateLimited',
}

export class SourceDiffError extends Error {
  readonly kind: DiffFailureKind
  readonly code: string
  readonly retryAt?: number

  constructor(kind: DiffFailureKind, code: string, retryAt?: number) {
    super(code)
    this.name = 'SourceDiffError'
    this.kind = kind
    this.code = code
    this.retryAt = retryAt
  }
}

export function diffRequestPath(query: GitHostDiffQuery): string {
  const params = new URLSearchParams()
  params.set('source', query.source)
  params.set('host', query.host)
  params.set('repository', query.repository)
  params.set('base', query.base)
  params.set('head', query.head)
  return `/v1/console/sources/diff?${params.toString()}`
}

/** Seconds, or an HTTP date. Anything else is ignored. */
export function retryDeadline(
  header: string | null,
  now: number,
): number | undefined {
  if (!header) return undefined
  const trimmed = header.trim()
  if (/^\d+$/.test(trimmed)) return now + Number(trimmed) * 1000
  const date = Date.parse(trimmed)
  if (!Number.isNaN(date)) return date
  return undefined
}

/**
 * Choose the failure from error.code only. The message is prose and is not read.
 */
export function classifyDiffError(
  error: unknown,
  retryAfter: string | null,
  now = Date.now(),
): SourceDiffError {
  if (error instanceof SourceDiffError) return error
  if (error instanceof ApiError) {
    const kind = KIND_BY_CODE[error.code] ?? 'upstream'
    const retryAt =
      kind === 'rateLimited' ? retryDeadline(retryAfter, now) : undefined
    return new SourceDiffError(kind, error.code, retryAt)
  }
  return new SourceDiffError('upstream', 'upstream')
}

export async function readContentDiff(
  query: GitHostDiffQuery,
  now = Date.now(),
): Promise<GitHostDiff> {
  try {
    return await http.get<GitHostDiff>(diffRequestPath(query))
  } catch (error) {
    const retryAfter =
      error instanceof ApiError ? (error.retryAfter ?? null) : null
    throw classifyDiffError(error, retryAfter, now)
  }
}
