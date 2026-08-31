// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// The capability catalog — "what the platform can evidence right now".
//
// The engine has served `GET /v1/m/compliance/capabilities` since
// `modules/compliance/compliance.go:454` under `permFrameworkRead`, and until this
// file existed the console never called it: measured with the console's own client,
// `grep -n "/capabilities" web/src/features/compliance/api.ts` returned nothing. The
// catalog was reachable only by curl or the API playground, and a raw REST client is
// not an operating surface.
//
// THREE ENGINE FACTS SHAPE THIS VIEW, AND EACH IS RENDERED RATHER THAN DISCOVERED.
// They are not stylistic: each one is a way this screen could look correct and lie.
//
//   COUNT IS `omitempty`, SO ABSENT AND ZERO ARE THE SAME BYTES. `Count int64
//   `json:"count,omitempty"`` (`modules/compliance/types.go:127`) means a zero count
//   never reaches the wire. The field's own doc says it is "0 for architectural or
//   absent" — two different things collapsed into one absence. So the count is keyed
//   off `class`, NEVER off the field being present: an architectural capability is
//   drawn as "not counted", because printing `0` there would be a measurement claim
//   about something that is cited to a design document and has nothing to count.
//
//   `more` MEANS THE COUNT IS A FLOOR. It reports that the count was truncated at
//   the page cap (`types.go:128`). Rendered as a bare number, a truncated count reads
//   as a total — so when `more` is set the value is drawn as "at least N" and says it
//   was truncated. A count that stops being exact without saying so is worse than no
//   count, because it is still believed.
//
//   `unknown` IS NOT `absent`. `EvidenceUnknown` means the capability could not be
//   evaluated; `EvidenceAbsent` means it was evaluated and nothing backs it
//   (`types.go:108-115`). Collapsing them — the natural two-state badge — turns "I
//   did not measure" into "there is no evidence", which is the one direction a
//   compliance screen must never round.
//
// And the disclaimer rides the view, because the engine ships one with every report
// body and this module reports control STATUS + EVIDENCE, never certification.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { useAuth } from '@/lib/auth/context'
import { complianceApi, complianceKeys } from './api'
import type { CapabilityEvidence, CapabilityState } from './types'

/** present → default, absent → destructive, unknown → outline. Three, never two:
 *  the two-state version is exactly the rounding this view exists to avoid. */
const STATE_VARIANT: Record<CapabilityState, 'success' | 'danger' | 'warning'> =
  {
    present: 'success',
    absent: 'danger',
    unknown: 'warning',
  }

/** A class the catalog added and this build has no label for is shown RAW. i18next
 *  answers a missing key with the key itself, so the alternative on screen would be
 *  the literal string "capabilities.class.<whatever>". */
function classLabel(cls: string, t: (k: string) => string): string {
  return cls === 'operational' || cls === 'architectural'
    ? t(`capabilities.class.${cls}`)
    : cls
}

function CountCell({ cap }: { cap: CapabilityEvidence }) {
  const { t } = useTranslation('compliance')
  // `!== 'operational'` and not `=== 'architectural'`: the class union is widened
  // with `| string` because the engine catalog can add one, and a class we have no
  // rule for must not be counted. Counting by default would make an unknown class
  // claim "0 rows" the moment the engine grows.
  if (cap.class !== 'operational') {
    return (
      <span
        className="text-muted-foreground"
        title={t('capabilities.count.notCountedHint')}
      >
        {t('capabilities.count.notCounted')}
      </span>
    )
  }
  const n = cap.count ?? 0
  if (cap.more) {
    return (
      <span>
        {t('capabilities.count.atLeast', { count: n })}{' '}
        <Badge variant="outline">{t('capabilities.count.truncated')}</Badge>
      </span>
    )
  }
  return <span>{n}</span>
}

export function CapabilityCatalog() {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: complianceKeys.capabilities(activeTenant),
    queryFn: () => complianceApi.capabilities(),
  })

  if (query.isPending)
    return <Skeleton data-testid="capabilities-loading" className="h-40" />
  if (query.isError) return <ErrorState title={t('capabilities.error')} />

  const items = query.data?.capabilities ?? []
  if (items.length === 0) return <EmptyState title={t('capabilities.empty')} />

  return (
    <section aria-labelledby="capability-catalog-title">
      <h2 id="capability-catalog-title">{t('capabilities.title')}</h2>
      <p className="text-muted-foreground">{t('capabilities.description')}</p>
      <table>
        <thead>
          <tr>
            <th scope="col">{t('capabilities.columns.capability')}</th>
            <th scope="col">{t('capabilities.columns.class')}</th>
            <th scope="col">{t('capabilities.columns.state')}</th>
            <th scope="col">{t('capabilities.count.label')}</th>
            <th scope="col">{t('capabilities.refs.label')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((cap) => (
            <tr key={cap.key}>
              <th scope="row">
                <span>{cap.key}</span>
                <span className="text-muted-foreground block">
                  {cap.detail}
                </span>
              </th>
              <td>
                <Badge variant="outline">{classLabel(cap.class, t)}</Badge>
              </td>
              <td>
                <Badge variant={STATE_VARIANT[cap.state]}>
                  {t(`capabilities.state.${cap.state}`)}
                </Badge>
              </td>
              <td>
                <CountCell cap={cap} />
              </td>
              <td>
                {(cap.refs ?? []).length === 0 ? (
                  <span className="text-muted-foreground">
                    {t('capabilities.refs.none')}
                  </span>
                ) : (
                  <ul>
                    {(cap.refs ?? []).map((r, i) => (
                      <li key={`${r.kind}-${i}`}>
                        <Badge variant="outline">{r.kind}</Badge> {r.detail}
                      </li>
                    ))}
                  </ul>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {query.data?.disclaimer ? (
        <p role="note" className="text-muted-foreground">
          {query.data.disclaimer}
        </p>
      ) : null}
    </section>
  )
}
