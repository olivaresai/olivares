// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ONE MAPPING from a failed read to what a panel says (EU18, EU20). On 08b a dormant
// module's panel and a real group forbid both read "Something went wrong / unexpected error /
// Retry": neither is a failure, and Retry cannot change either answer. Every query-backed
// panel renders its error through QueryErrorState (the census in
// features/query-error-census.test.ts keeps it so):
//   - a step-up demand: the ceremony that lifts it; a tenant not admitted: that;
//   - 403: who may grant it, or the policy that denies it when the engine names one. No Retry;
//   - 501 (open-core seam): this edition does not include it; no Retry;
//   - 404 module_not_enabled: the module is off here, with the administrator's enable action.
//     No Retry, and no New: ModuleGate does not mount the panel's reads or its actions;
//   - a network failure or a 5xx: the error, with Retry and the request id.
import { lazy, Suspense, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { useModuleName } from '@/features/settings/module-names'
import {
  ApiError,
  NetworkError,
  isEvidenceUnavailable,
  isOpenCoreSeam,
  isModuleNotEnabled,
} from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { useModuleOn } from '@/stores/modules'

// The enable action lives with the Modules screen; it loads only when the line offers it.
const TurnOnModule = lazy(() =>
  import('@/features/settings/modules-settings').then((m) => ({
    default: m.TurnOnModule,
  })),
)

/** A module that is off on this installation, in one line, with the administrator's enable
 * action (the same selection and restart as Settings > Edition & modules). */
export function ModuleOffNotice({
  module,
  className,
}: {
  /** As in /v1/m/<id>. Unknown only when neither the answer nor the panel names it: the
   * line then names no module and offers no action. */
  module?: string
  className?: string
}) {
  const { t } = useTranslation('errors')
  const name = useModuleName(module)
  return (
    <div
      role="status"
      data-slot="module-not-enabled"
      className={cn(
        'flex flex-wrap items-center gap-x-3 gap-y-2 py-3 text-body text-text-2',
        className,
      )}
    >
      <span>
        {name
          ? t('moduleNotEnabled.named', { module: name })
          : t('moduleNotEnabled.unnamed')}
      </span>
      <ModuleEnableAction module={module} />
    </div>
  )
}

/** The administrator's way to turn a module on, where it is off (EU): the same selection and
 * restart as Settings > Edition & modules. Nothing for anyone else, or for no module. */
export function ModuleEnableAction({ module }: { module?: string }) {
  const { isSuperadmin } = useAuth()
  // Communication follows its channels probe, not the selection: no action.
  if (!isSuperadmin || !module || module === 'communication') return null
  return (
    <Suspense fallback={null}>
      <TurnOnModule module={module} />
    </Suspense>
  )
}

/** Mounts `children` only while `module` runs here: a panel that reads another module's
 * routes neither reads them nor offers New while that module is off (EU18). */
export function ModuleGate({
  module,
  children,
  className,
}: {
  module: string
  children: ReactNode
  className?: string
}) {
  const on = useModuleOn(module)
  return on ? (
    <>{children}</>
  ) : (
    <ModuleOffNotice module={module} className={className} />
  )
}

export interface QueryErrorStateProps {
  /** The read's error, as the query holds it. */
  error: unknown
  /** Offered for a failure only (network, 5xx, the engine could not look). */
  retry?: () => void
  /** What the panel shows, already translated ("agents", "model groups"): the 403 line
   * says "You do not have access to <subject>." */
  subject?: string
  /** The module whose routes the panel reads, when it is not the page's own. */
  module?: string
  /** The failure's own words; the default is the generic one. */
  title?: ReactNode
  description?: ReactNode
  className?: string
}

export function QueryErrorState({
  error,
  retry,
  subject,
  module,
  title,
  description,
  className,
}: QueryErrorStateProps) {
  const { t } = useTranslation('errors')
  if (error instanceof ApiError && error.isStepUpRequired)
    return (
      <StepUpRequiredState
        action="generic"
        onElevated={retry ? () => retry() : undefined}
      />
    )
  if (
    error instanceof ApiError &&
    error.isForbidden &&
    error.code === 'tenant_admission_required'
  )
    return (
      <ForbiddenState
        className={className}
        title={t('tenantAdmission.title')}
        description={t('tenantAdmission.description')}
      />
    )
  if (error instanceof ApiError && error.isForbidden) {
    const policy =
      error.detailString('policy_name') ?? error.detailString('policy')
    return (
      <ForbiddenState
        className={className}
        title={
          subject
            ? t('forbidden.accessTo', { subject })
            : t('forbidden.accessToThis')
        }
        description={
          policy
            ? t('forbidden.deniedByPolicy', { policy })
            : t('forbidden.nextStep')
        }
      />
    )
  }
  // 501: the engine says this build does not include the capability (the open-core seam).
  // That is an answer about the edition, not a failure: no red, no Retry (09 signing).
  if (isOpenCoreSeam(error))
    return (
      <p
        role="status"
        data-slot="not-in-edition"
        className={cn('py-3 text-body text-text-2', className)}
      >
        {subject
          ? t('notInEdition.subject', { subject })
          : t('notInEdition.generic')}
      </p>
    )
  if (isModuleNotEnabled(error))
    return (
      <ModuleOffNotice
        // The answer names its module (ARCH 8a9a9524); the panel's own word is the fallback.
        module={error.detailString('module') ?? module}
        className={className}
      />
    )
  const network = error instanceof NetworkError
  const unknown = isEvidenceUnavailable(error)
  return (
    <ErrorState
      className={className}
      title={
        title ??
        (network
          ? t('network.title')
          : unknown
            ? t('evidenceUnavailable.title')
            : t('serverError.title'))
      }
      description={
        description ??
        (network
          ? t('network.description')
          : unknown
            ? t('evidenceUnavailable.description')
            : t('serverError.description'))
      }
      retry={retry}
      requestId={error instanceof ApiError ? error.requestId : undefined}
    />
  )
}
