// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Shared building blocks for the identity & NHI console. Mirrors the
// honest-seam pattern: a DECLARED endpoint that is not live yet renders a plain
// "backend pending" notice — never a fake success, never a red error.
import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  CaveatNotice,
  HashChip,
  SeamBadge,
  SeverityBadge,
} from '@/features/_intel'
import { StatusBadge } from '@/components/data/badges'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { isContractPending } from './api'
// Same reason as assurance.tsx: `ContractPendingNotice` is rendered from the step-up
// panel, i.e. from chunks that never import the identity view. It registers the
// namespace it translates with rather than trusting its caller.
import './i18n'
import type { IdentityFinding } from './types'

/** The honest "backend pending" seam — a DECLARED endpoint is not
 *  live yet. We say so plainly; local logic (e.g. the WIF linter) still works. */
export function ContractPendingNotice({ what }: { what: string }) {
  const { t } = useTranslation('identity')
  return (
    <CaveatNotice tone="info" className="items-center">
      <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <SeamBadge label={t('seam.badge')} />
        <span>{t('seam.body', { what })}</span>
      </span>
    </CaveatNotice>
  )
}

/** The honest "we could not look" notice. Distinct from the pending seam above:
 *  the route IS live and answered — it just could not read its upstream, and it says
 *  why. Rendering this as an empty table would tell an operator their organization has
 *  no customer-managed keys when the truth is that no Admin credential is provisioned. */
export function PostureUnavailableNotice({ reason }: { reason: string }) {
  const { t } = useTranslation('identity')
  return (
    <CaveatNotice tone="warning" className="items-center">
      <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <SeamBadge label={t('posture.unavailable.badge')} />
        <span>{reason}</span>
      </span>
    </CaveatNotice>
  )
}

/**
 * Render a query whose endpoint is DECLARED: 404/501/405 → honest pending seam,
 * 403 → calm forbidden, network/5xx → retryable error, else the data.
 */
export function DeclaredSection<T>({
  query,
  what,
  skeletonHeight = 120,
  children,
}: {
  query: Pick<
    UseQueryResult<T>,
    'data' | 'isLoading' | 'isError' | 'error' | 'refetch'
  >
  what: string
  skeletonHeight?: number
  children: (data: T) => ReactNode
}) {
  const { t } = useTranslation('errors')
  if (query.isLoading) {
    return <Skeleton className="w-full" style={{ height: skeletonHeight }} />
  }
  if (query.isError) {
    const error = query.error
    if (isContractPending(error)) return <ContractPendingNotice what={what} />
    // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
    // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también.
    if (error instanceof ApiError && error.isStepUpRequired) {
      return (
        <StepUpRequiredState
          action="generic"
          onElevated={() => void query.refetch()}
        />
      )
    }
    if (error instanceof ApiError && error.isForbidden) {
      return (
        <div className="flex min-h-32 items-center justify-center">
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        </div>
      )
    }
    const isNetwork = error instanceof NetworkError
    return (
      <ErrorState
        title={isNetwork ? t('network.title') : t('serverError.title')}
        description={
          isNetwork ? t('network.description') : t('serverError.description')
        }
        retry={() => void query.refetch()}
      />
    )
  }
  if (query.data === undefined) return null
  return <>{children(query.data)}</>
}

/** A single posture/governance Finding row — severity, title, subject, redacted
 *  hash. Never expands the detail (docs/SECURITY-HARDENING.md).*/
export function FindingRow({ finding }: { finding: IdentityFinding }) {
  return (
    <li className="flex flex-col gap-1.5 rounded-md border border-border bg-surface px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <SeverityBadge severity={finding.severity} />
        {finding.status ? <StatusBadge status={finding.status} /> : null}
        {finding.subject_ref ? (
          <code className="font-mono text-xs text-muted-foreground break-all">
            {finding.subject_ref}
          </code>
        ) : null}
      </div>
      {finding.title ? (
        <p className="text-sm text-foreground">{finding.title}</p>
      ) : null}
      {finding.detail_hash ? <HashChip hash={finding.detail_hash} /> : null}
    </li>
  )
}

/** A list of Findings, with an empty calm state. */
export function FindingList({
  findings,
  emptyTitle,
  emptyDescription,
  label,
}: {
  findings: IdentityFinding[]
  emptyTitle: string
  emptyDescription?: string
  label: string
}) {
  if (findings.length === 0) {
    return <EmptyState title={emptyTitle} description={emptyDescription} />
  }
  return (
    <ul className="flex flex-col gap-2" aria-label={label}>
      {findings.map((f) => (
        <FindingRow key={f.id} finding={f} />
      ))}
    </ul>
  )
}

/** An external authority citation link (RFC / NIST / FIPS / vendor docs). */
export function AuthorityLink({
  href,
  children,
  className,
}: {
  href: string
  children: ReactNode
  className?: string
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer noopener"
      className={cn(
        'font-mono text-xs text-accent-text underline-offset-2 hover:underline break-all',
        className,
      )}
    >
      {children}
    </a>
  )
}
