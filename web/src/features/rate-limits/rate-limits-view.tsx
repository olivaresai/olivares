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
import { ModuleOffNotice } from '@/components/layout/query-error-state'
import { useModuleOn } from '@/stores/modules'
import { Gauge } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import {
  useProfileList,
  type ProfileList,
} from '@/features/agent-tools/use-profiles'
import { accountLabel } from '@/features/agent-tools/account-marks'
import {
  hasFigures,
  UsageMeter,
  UsageWindows,
} from '@/features/agentops/account-usage'
import { toolName } from '@/features/agentops/engine-tools'
import { COST_VIEW } from '@/features/registry'
import { useViewAccess } from '@/features/navigation/authorization'
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
  const access = useViewAccess()
  const { t } = useTranslation(['rateLimits', 'common'])
  const { activeTenant, can } = useAuth()
  // Each profile's own limits: what its tool lets it use and when that resets, read from
  // the accounts the person may read (their own and the installation's shared ones).
  const accountsRead = can('sessions:account:read')
  const profiles = useProfileList(accountsRead)

  // REAL: the governance Info finding carrying the count (subject anthropic.rate_limit).
  // It is the security module's: while that is off the summary says so (EU18).
  const securityOn = useModuleOn('security')
  const findingsQ = useQuery({
    queryKey: rateLimitsKeys.findings(activeTenant),
    queryFn: () => rateLimitsApi.findings(),
    enabled: securityOn,
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
            links={
              access.navigable(COST_VIEW)
                ? [
                    {
                      to: COST_VIEW.path,
                      label: t(`nav:items.${COST_VIEW.id}`),
                    },
                  ]
                : []
            }
            targets={[
              {
                to: '/inference-proxy',
                permission: 'inferenceproxy:config:read',
                label: t('effectiveState.proxy'),
              },
            ]}
          />
        </>
      }
    >
      {accountsRead ? (
        <SectionCard
          title={t('profiles.title')}
          description={t('profiles.description')}
        >
          <AsyncSection query={profiles} skeletonHeight={104}>
            {(list) => <ProfileLimits list={list} />}
          </AsyncSection>
        </SectionCard>
      ) : null}

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
        {securityOn ? null : <ModuleOffNotice module="security" />}
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

/** One row per profile whose tool reported a limit: who it is, what the tool limits and
 * when that resets (the account read's `usage`, GET /v1/m/sessions/provider-accounts).
 * Olivares sets none of these: the tool reports them. */
function ProfileLimits({ list }: { list: ProfileList }) {
  const { t } = useTranslation('rateLimits')
  // A profile is listed when its tool gave a figure for a window still in force.
  const limited = [...list.accounts, ...list.disabled].filter((account) =>
    hasFigures(account.usage),
  )
  return (
    <div className="flex flex-col gap-2">
      {limited.length > 0 ? (
        <BoxedList>
          {limited.map((account) => (
            <BoxedListRow key={account.account_ref}>
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <span className="text-body font-medium text-text">
                  {accountLabel(account)}
                </span>
                <span className="text-caption text-text-2">
                  {toolName(account.driver)}
                </span>
                <UsageWindows usage={account.usage} />
              </div>
              <UsageMeter usage={account.usage} />
            </BoxedListRow>
          ))}
        </BoxedList>
      ) : list.truncated ? null : (
        <p className="text-body text-text-2">{t('profiles.none')}</p>
      )}
      {list.truncated ? (
        <p role="status" className="text-caption text-text-2">
          {t('profiles.truncated')}
        </p>
      ) : null}
    </div>
  )
}
