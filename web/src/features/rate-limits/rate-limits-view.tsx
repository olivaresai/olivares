// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Rate Limits view (ANT2-05) — the container. READ-ONLY: the Anthropic Rate Limits
// API is read-only, so there is NO edit/create affordance anywhere here. It wires the
// two queries and composes the pure pieces; it computes nothing about the limits
// (ARCHITECTURE.md — present, never recompute). Two provenance classes:
//  • LIVE (AsyncSection) — the governance count finding from the real security-findings
//    endpoint. Shown truthfully today, with the documented caveats. An EMPTY findings
//    list means the connector never emitted the summary — i.e. the Admin-API ingest is
//    unavailable on this surface — so we say that, never a fabricated empty inventory.
//  • LIVE (AsyncSection) — the per-group inventory, now served by the backend
//    (`GET /v1/m/models/rate-limits`, flipped from a declared seam in). The route
//    always answers 200; when `available=false` the view shows an honest "unavailable"
//    notice with the backend reason, never a fabricated empty inventory.
import { Gauge } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  EffectiveStateLinks,
  IntelPage,
  ListTruncationBadge,
  SectionCard,
} from '@/features/_intel'
import { RATE_LIMIT_SUBJECT, rateLimitsApi, rateLimitsKeys } from './api'
import {
  IngestUnavailableNotice,
  InventoryUnavailableNotice,
  RateLimitCaveats,
  RateLimitCountStat,
  RateLimitInventoryTable,
} from './components'
import './i18n'

export function RateLimitsView() {
  const { t } = useTranslation(['rateLimits', 'common'])
  const { activeTenant } = useAuth()

  // REAL: the governance Info finding carrying the count (subject anthropic.rate_limit).
  const findingsQ = useQuery({
    queryKey: rateLimitsKeys.findings(activeTenant),
    queryFn: () => rateLimitsApi.findings(),
  })

  // LIVE: the per-group inventory (GET /v1/m/models/rate-limits — always 200).
  const inventoryQ = useQuery({
    queryKey: rateLimitsKeys.inventory(activeTenant),
    queryFn: () => rateLimitsApi.inventory(),
  })

  return (
    <IntelPage
      icon={Gauge}
      title={t('title')}
      description={t('description')}
      notices={
        <>
          <RateLimitCaveats />
          {/*the limits themselves are the PROVIDER's and read-only here. What
              this estate can change is what it sends and what it spends: the proxy
              that fronts the calls, and the spend those limits shape. */}
          <EffectiveStateLinks
            label={t('effectiveState.label')}
            targets={[
              {
                to: '/inference-proxy',
                permission: 'inferenceproxy:config:read',
                label: t('effectiveState.proxy'),
              },
              {
                to: '/finops',
                permission: 'finops:spend:read',
                label: t('effectiveState.finops'),
              },
            ]}
          />
        </>
      }
    >
      <SectionCard
        title={t('summary.title')}
        description={t('summary.description')}
      >
        <ListTruncationBadge
          query={findingsQ}
          label={t('rateLimits:truncation.label', {
            n: findingsQ.data?.items?.length,
          })}
          hint={t('rateLimits:truncation.hint')}
          className="px-0 pt-0 pb-3"
        />
        <AsyncSection query={findingsQ} skeletonHeight={108}>
          {(list) => {
            // The connector emits exactly one summary for this subject; an empty list
            // means the Admin-API governance ingest is unavailable on this surface —
            // say so, never fabricate a count or an empty inventory.
            const finding = list.items.find(
              (f) => f.subject_kind === RATE_LIMIT_SUBJECT,
            )
            if (!finding) {
              return <IngestUnavailableNotice />
            }
            return <RateLimitCountStat finding={finding} />
          }}
        </AsyncSection>
      </SectionCard>

      <SectionCard
        title={t('inventory.title')}
        description={t('inventory.description')}
        noPadding
      >
        <div className="p-4">
          <AsyncSection query={inventoryQ} skeletonHeight={220}>
            {(inv) =>
              inv.available ? (
                <RateLimitInventoryTable limits={inv.rate_limits} />
              ) : (
                // 200 with available=false — honest unavailability, never an empty table.
                <InventoryUnavailableNotice reason={inv.reason} />
              )
            }
          </AsyncSection>
        </div>
      </SectionCard>
    </IntelPage>
  )
}
