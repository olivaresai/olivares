// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// FinOps model rate catalog (C07-04): the per-provider/model list prices the engine uses to
// ESTIMATE cost when a provider reports no amount (modules/finops/ratecatalog.go). Read-only.
//
// TarifasCard (finops-view.tsx) owns the query, its tenant identity, the truncation badge and
// AsyncSection's loading, forbidden, step-up and transport states. This renders a settled read:
//   · each entry's provider, model and input/output rate exactly as served (integer micro-USD
//     per 1M tokens), plus the validity dates the entry declares;
//   · no "current" badge: an entry without an end date may start in the future or be
//     superseded, and which entry priced a charge is resolved by the engine (`resolveRate`),
//     not by this list;
//   · a refused payload is an unavailable catalog with a read-only reload: never an empty list,
//     never a zero rate, never the payload.
import './i18n'
import { Fragment, useEffect, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import type {
  ModelRateEntry,
  RateCatalogIssue,
  RateCatalogRead,
  RateInstant,
} from './model-rate-validation'

export function ModelRateCatalog({
  read,
  reloading,
  onReload,
}: {
  read: RateCatalogRead
  /** A reload of the same GET is in flight. */
  reloading: boolean
  /** Repeats the GET; the catalog offers no other action. */
  onReload: () => void
}) {
  const { t } = useTranslation('finops')
  const result = useRef<HTMLDivElement>(null)
  // Set when the operator asks for the read again. The first settled read after it consumes the
  // request: a readable page takes focus, because the Retry button that held it is gone; a page
  // still unreadable leaves focus on Retry; a page arriving later on its own never moves focus.
  const reloadAsked = useRef(false)
  useEffect(() => {
    if (!reloadAsked.current || reloading) return
    reloadAsked.current = false
    if (read.status === 'valid') result.current?.focus()
  }, [read, reloading])

  if (read.status === 'invalid')
    return (
      <InvalidCatalog
        issue={read.issue}
        reloading={reloading}
        onReload={() => {
          reloadAsked.current = true
          onReload()
        }}
      />
    )
  return (
    <div
      ref={result}
      tabIndex={-1}
      className="outline-none"
      data-slot="model-rate-catalog-result"
    >
      {read.items.length === 0 ? (
        <EmptyState
          title={t('rates.empty')}
          description={t('rates.emptyHint')}
        />
      ) : (
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1 text-xs text-muted-foreground">
            <p>{t('rates.unitNote', { unit: t('rates.unit') })}</p>
            <p>{t('rates.validityNote')}</p>
          </div>
          <ul className="flex flex-col gap-2" data-slot="model-rate-list">
            {read.items.map((entry, index) => (
              // Position-qualified: the key must stay unique even if two entries share an id.
              <RateEntry key={`${index}:${entry.id}`} entry={entry} />
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

function RateEntry({ entry }: { entry: ModelRateEntry }) {
  const { t } = useTranslation('finops')
  return (
    <li
      className="rounded-md border border-border px-3 py-2.5"
      data-slot="model-rate-entry"
    >
      <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm sm:grid-cols-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.25fr)_minmax(0,1.25fr)]">
        <Fact label={t('rates.model')} className="col-span-2 xl:col-span-1">
          <span className="font-mono font-medium text-foreground [overflow-wrap:anywhere]">
            {entry.model}
          </span>
        </Fact>
        <Fact label={t('rates.provider')} className="col-span-2 xl:col-span-1">
          <span className="[overflow-wrap:anywhere]">{entry.provider}</span>
        </Fact>
        <Fact label={t('rates.inputRate')}>
          <ExactInteger value={entry.inputRateMicroUsd} />
        </Fact>
        <Fact label={t('rates.outputRate')}>
          <ExactInteger value={entry.outputRateMicroUsd} />
        </Fact>
        <Fact label={t('rates.effectiveFrom')}>
          <Instant at={entry.effectiveFrom} />
        </Fact>
        <Fact label={t('rates.effectiveUntil')}>
          {entry.effectiveUntil ? (
            <Instant at={entry.effectiveUntil} />
          ) : (
            <span className="text-muted-foreground">
              {t('rates.noEndDate')}
            </span>
          )}
        </Fact>
      </dl>
    </li>
  )
}

function Fact({
  label,
  className,
  children,
}: {
  label: string
  className?: string
  children: ReactNode
}) {
  return (
    <div className={cn('min-w-0', className)}>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 break-words text-foreground">{children}</dd>
    </div>
  )
}

const integerFormats = new Map<string, Intl.NumberFormat>()

/**
 * A localized exact integer (the options of `formatInt`) that may wrap only after a group
 * separator. Without the break opportunities a narrow cell split an amount inside a digit group
 * ("…740,99" / "1"), and some locales group with a no-break space that offers none at all.
 */
function ExactInteger({ value }: { value: number }) {
  const { i18n } = useTranslation('finops')
  let format = integerFormats.get(i18n.language)
  if (!format) {
    format = new Intl.NumberFormat(i18n.language, { maximumFractionDigits: 0 })
    integerFormats.set(i18n.language, format)
  }
  return (
    <span className="font-mono tabular-nums">
      {format.formatToParts(value).map((part, index) =>
        part.type === 'group' ? (
          <Fragment key={index}>
            {part.value}
            <wbr />
          </Fragment>
        ) : (
          part.value
        ),
      )}
    </span>
  )
}

function Instant({ at }: { at: RateInstant }) {
  const { i18n } = useTranslation('finops')
  const iso = new Date(at.epochMs).toISOString()
  return (
    <time dateTime={iso} title={at.text}>
      {formatDateTime(iso, i18n.language)}
    </time>
  )
}

function InvalidCatalog({
  issue,
  reloading,
  onReload,
}: {
  issue: RateCatalogIssue
  reloading: boolean
  onReload: () => void
}) {
  const { t } = useTranslation(['finops', 'common'])
  const n = issue.entry
  const field = issue.field
  // One literal key per reason, so the i18n usage gate can resolve each of them.
  const reasons: Record<RateCatalogIssue['reason'], () => string> = {
    envelope: () => t('finops:rates.invalid.reason.envelope'),
    entry: () => t('finops:rates.invalid.reason.entry', { n }),
    missing: () => t('finops:rates.invalid.reason.missing', { n, field }),
    empty: () => t('finops:rates.invalid.reason.empty', { n, field }),
    type: () => t('finops:rates.invalid.reason.type', { n, field }),
    negative: () => t('finops:rates.invalid.reason.negative', { n, field }),
    notInteger: () => t('finops:rates.invalid.reason.notInteger', { n, field }),
    unsafe: () => t('finops:rates.invalid.reason.unsafe', { n, field }),
    timestamp: () => t('finops:rates.invalid.reason.timestamp', { n, field }),
  }
  return (
    <div className="flex flex-col items-center gap-1">
      <ErrorState
        data-slot="model-rate-catalog-invalid"
        data-reason={issue.reason}
        data-entry={n}
        data-field={field}
        title={t('finops:rates.invalid.title')}
        description={
          <>
            <span className="block">
              {t('finops:rates.invalid.description')}
            </span>
            <span className="mt-1 block">{reasons[issue.reason]()}</span>
          </>
        }
        retry={onReload}
      />
      {reloading ? (
        <p role="status" className="text-xs text-muted-foreground">
          {t('common:states.loading')}
        </p>
      ) : null}
    </div>
  )
}
