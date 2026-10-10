// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The license read as the License tab states it: a failed refresh is not the last success,
// and no prior success is not "no license". A paid build's license cards compose these
// facts with their own reads (PANEL_EXTENSIONS.licenseCards).
import { useTranslation } from 'react-i18next'
import type { LicenseStatusDTO } from './api'
import { Button } from '@/components/ui/button'
import { CaveatNotice } from '@/features/_intel'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'

export const DISCLOSURE_SUMMARY_CLASS = cn(
  'cursor-pointer rounded-sm text-body text-foreground',
  'outline-none focus-visible:ring-2 focus-visible:ring-ring',
  'focus-visible:ring-offset-2 focus-visible:ring-offset-background',
)

/** The license GET used to compose entitlement — never a write. */
export const LICENSE_STATUS_PATH = '/v1/console/license'

export type CatalogueErrorDetail = {
  status?: number
  code?: string
  requestId?: string
}

/**
 * Status/code/request-id only. Never the response body, error message, env or
 * secret-bearing fields — those are not operator-facing technical detail here.
 */
export function catalogueErrorDetail(error: unknown): CatalogueErrorDetail {
  if (error instanceof ApiError) {
    return {
      status: error.status,
      code: error.code || undefined,
      requestId: error.requestId,
    }
  }
  return {}
}

/** Slice of a license query. Stale `data` after an error is last success, not current. */
export type LicenseQuerySlice = {
  isPending: boolean
  isError: boolean
  error: unknown
  data: LicenseStatusDTO | undefined
}

export type LicenseRead =
  | { kind: 'loading' }
  | {
      kind: 'failed'
      hadPriorData: boolean
      lastSuccess?: LicenseStatusDTO
      status?: number
      code?: string
      requestId?: string
    }
  | { kind: 'success'; license: LicenseStatusDTO }

/**
 * ⛔ A FAILED REFRESH IS NOT THE LAST SUCCESS. TanStack Query keeps prior
 *    `data` after an error. Feeding that payload to the matrix as current
 *    facts paints a green "entitled" cell and "license status Valid" after
 *    the read that would have to support those claims has failed.
 *
 *    Prior edition/status may be shown only as last successful read.
 *    Cached features must not supply current entitlement. No prior success
 *    means current facts are unavailable — not "no license".
 */
export function classifyLicenseRead(q: LicenseQuerySlice): LicenseRead {
  if (q.isError) {
    const detail = catalogueErrorDetail(q.error)
    return {
      kind: 'failed',
      hadPriorData: q.data !== undefined,
      lastSuccess: q.data,
      status: detail.status,
      code: detail.code,
      requestId: detail.requestId,
    }
  }
  if (q.data === undefined) return { kind: 'loading' }
  return { kind: 'success', license: q.data }
}

export function LicenseReadStatus({
  read,
  onRetry,
}: {
  read: LicenseRead
  onRetry: () => void
}) {
  const { t } = useTranslation('console')
  if (read.kind === 'loading') {
    return (
      <p
        className="text-body text-muted-foreground"
        data-slot="license-read-state"
        data-kind="loading"
      >
        {t('entitlement.licenseLoading')}
      </p>
    )
  }
  if (read.kind === 'failed') {
    const edition = read.lastSuccess?.edition
      ? t(`license.editions.${read.lastSuccess.edition}`, {
          defaultValue: read.lastSuccess.edition,
        })
      : t('entitlement.unknown')
    const status = read.lastSuccess?.status
      ? t(`license.statuses.${read.lastSuccess.status}`, {
          defaultValue: read.lastSuccess.status,
        })
      : t('entitlement.unknown')
    return (
      <div
        className="flex flex-col gap-3"
        data-slot="license-read-state"
        data-kind="failed"
        data-had-prior={read.hadPriorData ? 'true' : 'false'}
      >
        <CaveatNotice tone="warning">
          {read.hadPriorData
            ? t('entitlement.licenseRefreshFailed')
            : t('entitlement.licenseFactsUnavailable')}
        </CaveatNotice>
        {read.hadPriorData &&
        (read.lastSuccess?.edition || read.lastSuccess?.status) ? (
          <p className="text-body text-muted-foreground">
            {t('entitlement.licenseLastSuccessfulRead', { edition, status })}
          </p>
        ) : null}
        <p className="text-body text-muted-foreground">
          {t('entitlement.licenseRefreshFailedAction')}
        </p>
        <LicenseRetry onRetry={onRetry} />
        <ReadTechnicalDetail
          endpoint={LICENSE_STATUS_PATH}
          status={read.status}
          code={read.code}
          requestId={read.requestId}
        />
      </div>
    )
  }
  const license = read.license
  if (!license.edition && !license.status) {
    return (
      <p
        className="text-body text-muted-foreground"
        data-slot="license-read-state"
        data-kind="success"
      >
        {t('entitlement.knownFactsUnavailable')}
      </p>
    )
  }
  const edition = license.edition
    ? t(`license.editions.${license.edition}`, {
        defaultValue: license.edition,
      })
    : t('entitlement.unknown')
  const status = license.status
    ? t(`license.statuses.${license.status}`, {
        defaultValue: license.status,
      })
    : t('entitlement.unknown')
  return (
    <p
      className="text-body text-foreground"
      data-slot="license-read-state"
      data-kind="success"
    >
      {t('entitlement.knownFacts', { edition, status })}
    </p>
  )
}

export function ReadTechnicalDetail({
  endpoint,
  status,
  code,
  requestId,
}: CatalogueErrorDetail & { endpoint: string }) {
  const { t } = useTranslation('console')
  if (status === undefined && !code && !requestId) return null
  const codePart = code ? ` · ${code}` : ''
  const requestPart = requestId ? ` · ${requestId}` : ''
  return (
    <details className="text-caption text-muted-foreground">
      <summary className={DISCLOSURE_SUMMARY_CLASS}>
        {t('entitlement.technicalSummary')}
      </summary>
      <p className="mt-1 font-mono">
        {t('entitlement.technicalDetail', {
          method: 'GET',
          endpoint,
          status: status ?? '—',
          codePart,
          requestPart,
        })}
      </p>
    </details>
  )
}

export function LicenseRetry({ onRetry }: { onRetry: () => void }) {
  const { t } = useTranslation('console')
  return (
    <Button
      variant="secondary"
      size="sm"
      onClick={onRetry}
      data-slot="license-retry"
    >
      {t('entitlement.licenseRetry')}
    </Button>
  )
}
