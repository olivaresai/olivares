// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { ListTruncationBadge } from '@/features/_intel'
import { Activity, EyeOff, Radio, Send } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Spinner } from '@/components/ui/spinner'
import { RelTimeLabel } from '@/features/shared'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ApiError } from '@/lib/api/errors'
import { formatLatency } from '@/lib/format'
import { cn } from '@/lib/utils'
import { healthApi, healthKeys, type EventParams } from './api'
import { HealthStateBadge, healthToken } from './health-state-badge'
import type { EventDTO, StatusDTO } from './types'

const CAUSE_ICON: Record<string, LucideIcon> = {
  edge: Radio,
  report: Send,
  sweep: EyeOff,
}

/** A `down` whose transition was caused by the staleness sweep = silence within the
 * expected cadence → possible evasion (docs UI-CONTRACT-HEALTH §8), not a load error. */
function isPossibleEvasion(e: EventDTO): boolean {
  return e.state === 'down' && e.cause === 'sweep'
}

export interface SubjectRef {
  subject_kind: string
  subject_ref: string
}

const EVENT_LIMIT = 1000

export function ReliabilityTimeline({
  tenant,
  subjects,
  selected,
  onSelect,
}: {
  tenant: string | null
  subjects: StatusDTO[]
  selected: SubjectRef | null
  onSelect: (s: SubjectRef | null) => void
}) {
  const { t } = useTranslation('health')

  // Mismo techo y misma razón que en incidentes: sin `limit` el motor pagina a 100 y la
  // línea de tiempo se ve entera sin serlo.
  const params: EventParams | undefined = selected
    ? {
        subject_kind: selected.subject_kind,
        subject_ref: selected.subject_ref,
        limit: EVENT_LIMIT,
      }
    : undefined

  const query = useQuery({
    queryKey: healthKeys.events(tenant, params),
    queryFn: () => healthApi.events(params),
    enabled: !!selected,
  })

  return (
    <div className="flex flex-col gap-4">
      <SubjectPicker
        subjects={subjects}
        selected={selected}
        onSelect={onSelect}
        labelKey="timeline.pick"
        placeholderKey="timeline.pickPlaceholder"
      />

      {!selected ? (
        <EmptyState
          icon={<Activity />}
          title={t('timeline.pickPrompt.title')}
          description={t('timeline.pickPrompt.description')}
        />
      ) : query.isLoading ? (
        <div className="flex justify-center py-12">
          <Spinner />
        </div>
      ) : query.error ? (
        query.error instanceof ApiError && query.error.isStepUpRequired ? (
          // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status
          // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también, así
          // que leerlo primero acusaba al operador de un permiso que SÍ tiene.
          <StepUpRequiredState
            action="generic"
            onElevated={() => void query.refetch()}
          />
        ) : query.error instanceof ApiError && query.error.isForbidden ? (
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        ) : (
          <ErrorState retry={() => void query.refetch()} />
        )
      ) : (query.data?.items.length ?? 0) === 0 ? (
        <EmptyState
          icon={<Activity />}
          title={t('timeline.empty.title')}
          description={t('timeline.empty.description')}
        />
      ) : (
        <>
          {/* El recorte se dice también aquí: una línea de tiempo cortada por arriba se
              lee como «no pasó nada antes», que es lo contrario de lo que significa. */}
          <ListTruncationBadge
            query={query}
            label={t('timeline.truncated', { n: EVENT_LIMIT })}
            hint={t('timeline.truncatedHint')}
          />
          <ol className="relative ml-2 flex flex-col gap-0 border-l border-border pl-5">
            {query.data!.items.map((e) => {
              const Icon = CAUSE_ICON[e.cause] ?? Activity
              const evasion = isPossibleEvasion(e)
              return (
                <li key={e.id} className="relative py-3">
                  {/* Node dot, colored by the NEW state. */}
                  <span
                    className="absolute -left-[27px] top-4 size-3 rounded-full ring-4 ring-background"
                    style={{ backgroundColor: healthToken(e.state) }}
                    aria-hidden
                  />
                  <div className="flex flex-wrap items-center gap-2">
                    <HealthStateBadge state={e.state} />
                    <span className="text-xs text-muted-foreground">
                      {t('timeline.transition', {
                        prev: t(`state.${e.prev_state}`, {
                          defaultValue: e.prev_state,
                        }),
                        next: t(`state.${e.state}`, { defaultValue: e.state }),
                      })}
                    </span>
                    {evasion && (
                      <span
                        className="inline-flex items-center gap-1 rounded-sm border border-warning-line bg-warning-soft px-1.5 py-0.5 text-[10px] font-medium text-warning"
                        title={t('timeline.possibleEvasionHint')}
                      >
                        <EyeOff className="size-3" />
                        {t('timeline.possibleEvasion')}
                      </span>
                    )}
                  </div>
                  <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                    <span
                      className={cn('inline-flex items-center gap-1')}
                      title={t(`timeline.causeHint.${e.cause}`, {
                        defaultValue: '',
                      })}
                    >
                      <Icon className="size-3.5" />
                      {t(`timeline.cause.${e.cause}`, {
                        defaultValue: e.cause,
                      })}
                    </span>
                    <span>
                      {t('timeline.latency')}:{' '}
                      <span className="font-mono tabular-nums text-foreground">
                        {e.latency_ms < 0
                          ? t('timeline.latencyUnknown')
                          : formatLatency(e.latency_ms)}
                      </span>
                    </span>
                    <RelTimeLabel ts={e.occurred_at} className="tabular-nums" />
                  </div>
                </li>
              )
            })}
          </ol>
        </>
      )}
    </div>
  )
}

/** Shared subject picker for the SLA and Timeline tabs — a labelled Select over the
 * monitored subjects. Keys are localized via the caller's i18n key params. */
export function SubjectPicker({
  subjects,
  selected,
  onSelect,
  labelKey,
  placeholderKey,
}: {
  subjects: StatusDTO[]
  selected: SubjectRef | null
  onSelect: (s: SubjectRef | null) => void
  labelKey: string
  placeholderKey: string
}) {
  const { t } = useTranslation('health')
  const value = selected
    ? `${selected.subject_kind}::${selected.subject_ref}`
    : ''
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-sm text-muted-foreground">{t(labelKey)}</span>
      <Select
        value={value}
        onValueChange={(v) => {
          const [kind, ...rest] = v.split('::')
          onSelect({ subject_kind: kind ?? '', subject_ref: rest.join('::') })
        }}
      >
        <SelectTrigger
          className="h-8 w-full max-w-sm text-sm"
          aria-label={t(placeholderKey)}
        >
          <SelectValue placeholder={t(placeholderKey)} />
        </SelectTrigger>
        <SelectContent>
          {subjects.map((s) => (
            <SelectItem
              key={`${s.subject_kind}::${s.subject_ref}`}
              value={`${s.subject_kind}::${s.subject_ref}`}
            >
              <span className="font-mono text-xs">
                {s.name || s.subject_ref}
              </span>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
