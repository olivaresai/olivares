// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The installation at a glance: each component the engine reports on `GET /status`, with
// its state word, and one sentence that says everything works or names what does not. The
// words are the public status page's; an optional component nobody set up is not a fault.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { BoxedList, BoxedListRow } from '@/components/ui/boxed-list'
import {
  StatusChip,
  StatusDot,
  type StatusTone,
} from '@/components/ui/status-chip'
import { AsyncSection } from '@/features/_intel'
import { healthApi, healthKeys } from '@/features/health/api'
import type { ComponentStatusDTO } from '@/features/health/types'
import '@/features/health/i18n'
import './i18n'

const STATUS_REFETCH_MS = 30_000

/** A state this build cannot name is no evidence that all is well: it reads as a warning. */
const TONE: Partial<Record<string, StatusTone>> = {
  operational: 'ok',
  not_configured: 'neutral',
  degraded: 'warn',
  outage: 'bad',
}

const isFault = (c: ComponentStatusDTO) =>
  c.status !== 'operational' && c.status !== 'not_configured'

export function StatusSection() {
  const { t } = useTranslation('operations')
  const statusQ = useQuery({
    queryKey: healthKeys.publicStatus(),
    queryFn: () => healthApi.publicStatus(),
    refetchInterval: STATUS_REFETCH_MS,
  })
  return (
    <section
      aria-labelledby="operations-status"
      className="flex flex-col gap-3"
    >
      <h2 id="operations-status" className="text-body font-semibold">
        {t('status.heading')}
      </h2>
      <AsyncSection query={statusQ} skeletonHeight={208}>
        {(status) => <StatusBody components={status.components} />}
      </AsyncSection>
    </section>
  )
}

function StatusBody({ components }: { components: ComponentStatusDTO[] }) {
  const { t } = useTranslation(['operations', 'health'])
  const name = (c: ComponentStatusDTO) =>
    t(`health:statusPage.component.${c.name}`, { defaultValue: c.name })
  const faults = components.filter(isFault)
  const unset = components.filter((c) => c.status === 'not_configured')
  const list = (some: ComponentStatusDTO[]) => some.map(name).join(', ')
  const sentence =
    faults.length > 0
      ? t('status.attention', { list: list(faults) })
      : unset.length > 0
        ? t('status.healthyConfigured', { list: list(unset) })
        : t('status.healthy')
  const tone: StatusTone =
    faults.length === 0
      ? 'ok'
      : faults.some((c) => c.status === 'outage')
        ? 'bad'
        : 'warn'
  return (
    <>
      {/* Polite: the sentence is read again only when it changes. */}
      <p
        role="status"
        className="flex items-center gap-2 text-body font-medium"
      >
        <StatusDot tone={tone} />
        {sentence}
      </p>
      <BoxedList role="list" aria-labelledby="operations-status">
        {components.map((c) => (
          <BoxedListRow key={c.name} role="listitem">
            <span className="min-w-0 flex-1 text-body font-medium">
              {name(c)}
            </span>
            <StatusChip tone={TONE[c.status] ?? 'warn'}>
              {t(`health:statusPage.status.${c.status}`, {
                defaultValue: c.status,
              })}
            </StatusChip>
          </BoxedListRow>
        ))}
      </BoxedList>
    </>
  )
}
