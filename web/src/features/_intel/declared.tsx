// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DeclaredSection — AsyncSection's seam-aware sibling for the admin dashboards.
// When a DECLARED endpoint (a UI data contract this session exposes for the backend
// to implement) is not yet live — 404 / 501 / 405 via isContractPending — it renders
// an honest "backend pending" seam (SeamBadge + a one-line caveat) instead of a red
// error. Every other state (loading / forbidden / network / data) matches AsyncSection
// so the four states stay consistent. Honest-seam precedent: Claude-policy
// identity (never fake live data, never dress a missing API as working).
import type { ReactNode } from 'react'
import type { UseQueryResult } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { isContractPending } from '@/lib/api/contract'
import { cn } from '@/lib/utils'
import { CaveatNotice, SeamBadge } from './notices'
// The `intel` namespace travels with the modules that translate: these are deep-
// imported across features (`@/features/_intel/notices`), where the barrel — and so
// the registration — is never in the chunk.
import './i18n'

/** The honest "backend pending" seam: a DECLARED endpoint is not wired to the engine
 *  yet, so the view shows the declared shape, never fabricated live data. */
export function ContractPendingNotice({
  what,
  className,
}: {
  /** Human label of the declared capability (e.g. "the rate-limit inventory"). */
  what: string
  className?: string
}) {
  const { t } = useTranslation('intel')
  return (
    <CaveatNotice tone="info" className={cn('items-center', className)}>
      <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
        <SeamBadge />
        <span>{t('notices.seamBody', { what })}</span>
      </span>
    </CaveatNotice>
  )
}

export function DeclaredSection<T>({
  query,
  what,
  skeletonHeight = 120,
  className,
  children,
}: {
  query: Pick<
    UseQueryResult<T>,
    'data' | 'isLoading' | 'isError' | 'error' | 'refetch'
  >
  /** Human label of the declared capability, for the pending-seam copy. */
  what: string
  skeletonHeight?: number
  className?: string
  children: (data: T) => ReactNode
}) {
  const { t } = useTranslation('errors')
  if (query.isLoading) {
    return (
      <Skeleton
        className={cn('w-full', className)}
        style={{ height: skeletonHeight }}
      />
    )
  }
  if (query.isError) {
    const error = query.error
    if (isContractPending(error)) return <ContractPendingNotice what={what} />
    // ASSURANCE before ROLE — see AsyncSection. A step-up demand is not a missing
    // permission, and ForbiddenState said it was.
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
        <div className="flex min-h-40 items-center justify-center">
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
