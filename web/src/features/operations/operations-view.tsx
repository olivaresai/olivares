// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// OPERATIONS: the one page for a system administrator, an engineer and whoever answers for
// the spend. The host and its disks, the live sessions by tool and person, and the usage of
// the runs each person may read, with one range for them all. Each section is a read the
// console already makes for its own page (host metrics, Now's sessions, Models > Usage),
// asked only when the principal may read it, so this page says nothing those pages do not.
import { useState } from 'react'
import { Server } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Segmented } from '@/components/ui/segmented'
import { IntelPage } from '@/features/_intel'
import { UsageTab } from '@/features/models/usage-tab'
import type { UsagePeriod } from '@/features/models/usage'
import { useAuth } from '@/lib/auth/context'
import { HostSection } from './host-section'
import { LiveSection } from './live-section'
import { StatusSection } from './status-section'
import './i18n'

/** The range the person picks, and the usage period it asks for. The host keeps 24 hours. */
const RANGES = { '24h': 1, '7d': 7, '30d': 30 } as const satisfies Record<
  string,
  UsagePeriod
>
type Range = keyof typeof RANGES

export function OperationsView() {
  const { t } = useTranslation('operations')
  const { can } = useAuth()
  const [range, setRange] = useState<Range>('24h')
  return (
    <IntelPage
      icon={Server}
      title={t('title')}
      description={t('subtitle')}
      actions={
        <Segmented
          size="sm"
          aria-label={t('range.label')}
          value={range}
          onValueChange={setRange}
          options={(Object.keys(RANGES) as Range[]).map((value) => ({
            value,
            label: t(`range.${value}`),
          }))}
        />
      }
    >
      <div className="flex flex-col gap-6">
        {can('health:status:read') ? <StatusSection /> : null}
        {can('system:admin') ? (
          <HostSection longRange={range !== '24h'} />
        ) : null}
        {can('sessions:live:read') ? <LiveSection /> : null}
        {can('sessions:run:read') ? (
          <section
            aria-labelledby="operations-usage"
            className="flex flex-col gap-3"
          >
            <h2 id="operations-usage" className="text-body font-semibold">
              {t('usage.heading')}
            </h2>
            <UsageTab period={RANGES[range]} />
          </section>
        ) : null}
      </div>
    </IntelPage>
  )
}
