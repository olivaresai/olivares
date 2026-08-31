// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// The regulatory calendar in the console — E6.
//
// THE MEASURED GAP. `GET /calendar` is registered (compliance.go:460) and served
// (calendar.go:600) with 30 dated milestones and 8 watchlist entries, each with a
// primary source and a verification date. Its only caller in the whole repository
// was a test (s168_test.go:396,410): zero consumers in web/src and zero in cmd/.
// Research the product had already done, dated and cited, that no operator could
// reach.
//
// (The brief originally placed the 26 versioned frameworks here; they are in
// frameworks.go:38 and the console already consumes them. The calendar is the
// part nothing consumed — that is what this file fixes.)
//
// The one rule this view must not break: the calendar's own disclaimer says
// `provisional_agreement` and `adopted_pending_oj` entries are NOT in-force law.
// Rendering everything as one flat list of "deadlines" would quietly turn a
// political agreement into a legal obligation, so status is carried on every row
// and the not-yet-law entries are visually distinct.
import { useQuery } from '@tanstack/react-query'
import { CalendarClock, ExternalLink, Eye } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  DisclaimerNote,
  SectionCard,
} from '@/features/_intel'
import { complianceApi, complianceKeys } from './api'
import type { RegulatoryMilestone, SourceRef, WatchlistItem } from './types'

/** Statuses that are NOT in-force law.
 *
 *  These are the ENGINE'S OWN values, read out of calendar.go rather than
 *  guessed: the milestone list uses in_force / applies_from / passed /
 *  adopted_pending_oj / provisional_agreement / withdrawn_pending_revision /
 *  in_development, and the watchlist adds beta and fdis. A first pass of this
 *  file assumed `proposed` and `draft` (neither exists) and omitted
 *  `in_development` — which would have painted an in-development mapping with
 *  the same colour as law in force. The calendar's disclaimer draws exactly this
 *  line, so the set is the line. */
const NOT_IN_FORCE: ReadonlySet<string> = new Set([
  'provisional_agreement',
  'adopted_pending_oj',
  'withdrawn_pending_revision',
  'in_development',
  'beta',
  'fdis',
])

/** in_force and applies_from are binding law (applies_from carries a future
 *  application date of a regulation already in force); `passed` is a milestone
 *  that has already happened, so it is neutral rather than an alarm. */
function statusTone(status: string): 'success' | 'warning' | 'neutral' {
  if (status === 'in_force' || status === 'applies_from') return 'success'
  if (NOT_IN_FORCE.has(status)) return 'warning'
  return 'neutral'
}

export function CalendarTab({ framework }: { framework: string | null }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const calendarQ = useQuery({
    queryKey: complianceKeys.calendar(activeTenant),
    queryFn: () => complianceApi.calendar(),
  })

  return (
    <>
      <SectionCard
        title={t('calendar.title')}
        description={t('calendar.description')}
      >
        <CaveatNotice tone="warning" className="mb-3">
          {t('calendar.notInForceHint')}
        </CaveatNotice>
        <AsyncSection query={calendarQ} skeletonHeight={280}>
          {(cal) => {
            // Client-side filtering only — the engine already supports
            // ?framework=, but filtering here keeps one fetch for both panels and
            // recomputes NOTHING: it selects rows, it never derives a status.
            const milestones = framework
              ? cal.milestones.filter(
                  (m) => !m.framework_id || m.framework_id === framework,
                )
              : cal.milestones
            return milestones.length === 0 ? (
              <EmptyState
                icon={<CalendarClock />}
                title={t('calendar.empty')}
              />
            ) : (
              <div className="flex flex-col gap-2">
                {[...milestones]
                  .sort((a, b) => a.date.localeCompare(b.date))
                  .map((m) => (
                    <MilestoneRow key={m.id} milestone={m} />
                  ))}
                <DisclaimerNote text={cal.disclaimer} />
              </div>
            )
          }}
        </AsyncSection>
      </SectionCard>

      <SectionCard
        title={t('calendar.watchlistTitle')}
        description={t('calendar.watchlistDescription')}
      >
        <AsyncSection query={calendarQ} skeletonHeight={180}>
          {(cal) =>
            cal.watchlist.length === 0 ? (
              <EmptyState icon={<Eye />} title={t('calendar.watchlistEmpty')} />
            ) : (
              <div className="flex flex-col gap-2">
                {cal.watchlist.map((w) => (
                  <WatchlistRow key={w.id} item={w} />
                ))}
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>
    </>
  )
}

function MilestoneRow({ milestone }: { milestone: RegulatoryMilestone }) {
  const { t } = useTranslation('compliance')
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-sm">{milestone.date}</span>
        <Badge variant={statusTone(milestone.status)}>
          {t(`calendar.status.${milestone.status}`, {
            defaultValue: milestone.status,
          })}
        </Badge>
        <span className="font-medium">{milestone.title}</span>
        {milestone.regime ? (
          <Badge variant="neutral">{milestone.regime}</Badge>
        ) : null}
      </div>
      <p className="text-xs">{milestone.effect}</p>
      {milestone.note ? (
        <p className="text-xs text-muted-foreground">{milestone.note}</p>
      ) : null}
      <SourceLine
        source={milestone.source}
        verifiedOn={milestone.verified_on}
      />
    </div>
  )
}

function WatchlistRow({ item }: { item: WatchlistItem }) {
  const { t } = useTranslation('compliance')
  return (
    <div className="flex flex-col gap-1 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={statusTone(item.status)}>
          {t(`calendar.status.${item.status}`, { defaultValue: item.status })}
        </Badge>
        <span className="font-medium">{item.name}</span>
      </div>
      {item.expected ? <p className="text-xs">{item.expected}</p> : null}
      {item.note ? (
        <p className="text-xs text-muted-foreground">{item.note}</p>
      ) : null}
      <SourceLine source={item.source} verifiedOn={item.verified_on} />
    </div>
  )
}

/** Every entry cites a primary source and the date it was verified. That pairing
 *  is the reason this calendar can be shipped at all, so it is never dropped for
 *  space. */
function SourceLine({
  source,
  verifiedOn,
}: {
  source: SourceRef
  verifiedOn: string
}) {
  const { t } = useTranslation('compliance')
  return (
    <p className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
      <a
        href={source.url}
        target="_blank"
        rel="noreferrer noopener"
        className="inline-flex items-center gap-1 underline hover:text-foreground"
      >
        {source.title}
        <ExternalLink className="size-3" />
      </a>
      <span>· {source.publisher}</span>
      <span>· {t('calendar.verifiedOn', { date: verifiedOn })}</span>
    </p>
  )
}
