// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Models/providers presentational pieces — PURE (data in, UI out). The key-ref table
// is contract-critical: it renders ONLY the masked `hint`, never a secret, with a
// lock affordance that says so. The capability matrix and pricing are read from the
// DECLARED catalog and labelled as reference, not immutable truth.
import { useMemo } from 'react'
import { ArrowRight, Check, KeyRound, Lock, Minus } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { StatusBadge } from '@/components/data/badges'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { CaveatNotice, SectionCard } from '@/features/_intel'
import { cn } from '@/lib/utils'
import { formatDate, formatInt, humanize } from '@/lib/format'
import type {
  CatalogModel,
  CatalogResponse,
  Decision,
  DecisionTarget,
  GovernedModel,
  KeyRef,
} from './types'

// --- capability matrix -------------------------------------------------------

export function CapabilityMatrix({ catalog }: { catalog: CatalogResponse }) {
  const { t } = useTranslation('models')
  return (
    <SectionCard
      title={t('catalog.title')}
      description={t('catalog.description')}
      noPadding
    >
      <div className="overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <thead>
            <tr className="border-b border-border-strong">
              <th className="sticky left-0 z-10 bg-muted px-3 py-2 text-left text-xs font-medium tracking-wide text-muted-foreground uppercase">
                {t('catalog.family')}
              </th>
              {catalog.capabilities.map((cap) => (
                <th
                  key={cap}
                  className="bg-muted px-2 py-2 text-center text-[11px] font-medium whitespace-nowrap text-muted-foreground"
                  title={humanize(cap)}
                >
                  {humanize(cap)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {catalog.models.map((m) => {
              const present = new Set(m.capabilities)
              return (
                <tr
                  key={m.family}
                  className="border-b border-border last:border-0"
                >
                  <td className="sticky left-0 z-10 bg-surface px-3 py-2 whitespace-nowrap">
                    <span className="font-mono text-xs text-foreground">
                      {m.family}
                    </span>
                    <span className="ml-2 text-xs text-muted-foreground">
                      {m.provider_ref}
                    </span>
                    {m.caps_to_confirm ? (
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Badge variant="warning" className="ml-2">
                            {t('catalog.capsToConfirm')}
                          </Badge>
                        </TooltipTrigger>
                        <TooltipContent>
                          {t('catalog.capsToConfirmHint')}
                        </TooltipContent>
                      </Tooltip>
                    ) : null}
                  </td>
                  {catalog.capabilities.map((cap) => (
                    <td key={cap} className="px-2 py-2 text-center">
                      {present.has(cap) ? (
                        <Check
                          className="mx-auto size-3.5 text-confidence-attributed"
                          aria-label={t('catalog.present')}
                        />
                      ) : (
                        <Minus
                          className="mx-auto size-3.5 text-border-strong"
                          aria-label={t('catalog.absent')}
                        />
                      )}
                    </td>
                  ))}
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </SectionCard>
  )
}

// --- declared pricing --------------------------------------------------------

export function PricingTable({ catalog }: { catalog: CatalogResponse }) {
  const { t, i18n } = useTranslation('models')
  return (
    <SectionCard title={t('catalog.pricingTitle')} noPadding>
      <div className="p-4">
        <CaveatNotice className="mb-3">
          {t('catalog.pricingDescription')}{' '}
          {t('catalog.asOf', {
            date: formatDate(catalog.pricing_as_of, i18n.language),
          })}
        </CaveatNotice>
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-border-strong text-xs text-muted-foreground uppercase">
                <th className="px-3 py-2 text-left font-medium">
                  {t('catalog.family')}
                </th>
                <th className="px-3 py-2 text-left font-medium">
                  {t('catalog.provider')}
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  {t('catalog.context')}
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  {t('catalog.inputPrice')}
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  {t('catalog.outputPrice')}
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  {t('catalog.cacheRead')}
                </th>
                <th className="px-3 py-2 text-right font-medium">
                  {t('catalog.cacheWrite1h')}
                </th>
                <th className="px-3 py-2 text-left font-medium">
                  {t('catalog.residency')}
                </th>
                <th className="px-3 py-2 text-left font-medium">
                  {t('catalog.tiers')}
                </th>
              </tr>
            </thead>
            <tbody>
              {catalog.models.map((m) => (
                <tr
                  key={m.family}
                  className="border-b border-border last:border-0"
                >
                  <td className="px-3 py-2 font-mono text-xs">{m.family}</td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {m.provider_ref}
                  </td>
                  <td className="px-3 py-2 text-right font-mono tabular-nums text-muted-foreground">
                    {formatInt(m.context_window, i18n.language)}
                  </td>
                  {m.pricing ? (
                    <>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">
                        ${m.pricing.input_per_mtok_usd}
                      </td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums">
                        ${m.pricing.output_per_mtok_usd}
                      </td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums text-muted-foreground">
                        {m.pricing.cache_read_per_mtok_usd !== undefined
                          ? `$${m.pricing.cache_read_per_mtok_usd}`
                          : '—'}
                      </td>
                      <td className="px-3 py-2 text-right font-mono tabular-nums text-muted-foreground">
                        {/* The 1-hour TTL cache-write tier is a DISTINCT rate (~2× base
                            input) — never collapsed into the 5m rate (reference.go). */}
                        {m.pricing.cache_write_1h_per_mtok_usd !== undefined
                          ? `$${m.pricing.cache_write_1h_per_mtok_usd}`
                          : '—'}
                      </td>
                    </>
                  ) : (
                    <td colSpan={4} className="px-3 py-2 text-center">
                      <Badge variant="outline">{t('catalog.noPricing')}</Badge>
                    </td>
                  )}
                  <td className="px-3 py-2">
                    <ResidencyBadges model={m} />
                  </td>
                  <td className="px-3 py-2">
                    <TierEligibility tiers={m.service_tier_eligibility} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </SectionCard>
  )
}

// --- data residency + service-tier eligibility (CLA-16 catalog dimensions) ----

/** inference_geo residency regions for a family, with the US-residency burndown
 *  multiplier shown verbatim when declared (reference.go: 1.1× on Opus/Sonnet 4.6+).
 *  An empty residency set is the honest "not applicable" case (models before Feb 2026
 *  report inference_geo=not_available), NOT a fabricated "global". */
export function ResidencyBadges({ model }: { model: CatalogModel }) {
  const { t } = useTranslation('models')
  const regions = model.data_residency ?? []
  if (regions.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {t('catalog.residencyNotApplicable')}
      </span>
    )
  }
  const mult = model.us_inference_burndown_mult
  return (
    <div className="flex flex-wrap gap-1">
      {regions.map((r) => (
        <Badge key={r} variant="neutral">
          <span className="font-mono uppercase">{r}</span>
          {r === 'us' && mult && mult > 0 ? (
            <span className="font-mono tabular-nums">
              {/* The US burndown is a CONFIRMED multiplier; presented, not computed. */}{' '}
              {t('catalog.usBurndown', { mult })}
            </span>
          ) : null}
        </Badge>
      ))}
    </div>
  )
}

/** Service-tier eligibility chips (standard, batch, priority, …). Absent/empty =
 *  not declared (honest gap), never an inferred default set. */
export function TierEligibility({ tiers }: { tiers?: string[] }) {
  const { t } = useTranslation('models')
  if (!tiers || tiers.length === 0) {
    return (
      <span className="text-xs text-muted-foreground">
        {t('catalog.tiersNotDeclared')}
      </span>
    )
  }
  return (
    <div className="flex flex-wrap gap-1">
      {tiers.map((tier) => (
        <Badge key={tier} variant="outline">
          {t(`catalog.tier.${tier}`, { defaultValue: humanize(tier) })}
        </Badge>
      ))}
    </div>
  )
}

// --- governed estate ---------------------------------------------------------

export function ModelsTable({ models }: { models: GovernedModel[] }) {
  const { t, i18n } = useTranslation('models')
  const columns = useMemo<TableColumn<GovernedModel>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('estate.columns.name'),
        cell: ({ row }) => (
          <div className="flex items-center gap-2">
            <span className="font-mono text-xs text-foreground">
              {row.original.name}
            </span>
            {!row.original.enriched ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Badge variant="outline">{t('estate.notEnriched')}</Badge>
                </TooltipTrigger>
                <TooltipContent>{t('estate.costNote')}</TooltipContent>
              </Tooltip>
            ) : null}
          </div>
        ),
      },
      {
        accessorKey: 'provider',
        header: t('estate.columns.provider'),
        cell: ({ row }) => (
          <span className="text-muted-foreground">{row.original.provider}</span>
        ),
      },
      {
        accessorKey: 'family',
        header: t('estate.columns.family'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.family || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'context_window',
        header: t('estate.columns.context'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {formatInt(row.original.context_window, i18n.language)}
          </span>
        ),
      },
      {
        accessorKey: 'input_cost_micro_usd',
        header: t('estate.columns.inputCost'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {row.original.input_cost_micro_usd}
          </span>
        ),
      },
      {
        accessorKey: 'output_cost_micro_usd',
        header: t('estate.columns.outputCost'),
        cell: ({ row }) => (
          <span className="font-mono tabular-nums text-muted-foreground">
            {row.original.output_cost_micro_usd}
          </span>
        ),
      },
      {
        id: 'capabilities',
        header: t('estate.columns.capabilities'),
        cell: ({ row }) => (
          <Badge variant="neutral">{row.original.capabilities.length}</Badge>
        ),
      },
      {
        accessorKey: 'status',
        header: t('estate.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<GovernedModel>
      columns={columns}
      data={models}
      getRowId={(r) => r.id}
      searchable
      empty={
        <EmptyState
          title={t('empty.models.title')}
          description={t('empty.models.description')}
        />
      }
    />
  )
}

// --- routing decision --------------------------------------------------------

function TargetChip({ target }: { target: DecisionTarget }) {
  const { t } = useTranslation('models')
  return (
    <span className="inline-flex items-center gap-1.5 rounded-md border border-border bg-muted px-2 py-1 font-mono text-xs">
      <span className="text-muted-foreground">{target.provider_ref}</span>
      {/* `--border-strong` is a BORDER token: as a text colour the separator
          measured 1.50:1 (dark) / 1.29:1 (light) on `bg-muted` — a glyph you
          cannot see. `muted-foreground` is the text token for de-emphasis and
          clears AA on that surface while staying lighter than the model ref. */}
      <span className="text-muted-foreground">/</span>
      <span className="text-foreground">{target.model_ref}</span>
      {target.via_gateway ? (
        <Badge variant="accent" className="ml-1">
          {t('routing.viaGateway')}
        </Badge>
      ) : null}
    </span>
  )
}

export function DecisionPanel({ decision }: { decision: Decision }) {
  const { t } = useTranslation('models')
  if (!decision.resolved) {
    return (
      <div className="rounded-md border border-warning-line bg-warning-soft p-3">
        <p className="text-sm font-medium text-warning">
          {t('routing.decision.unresolved')}
        </p>
        <p className="mt-1 text-xs text-muted-foreground">{decision.reason}</p>
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-3 rounded-md border border-border bg-muted/40 p-3">
      <div className="flex flex-col gap-1.5">
        <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
          {t('routing.decision.primary')}
        </span>
        {decision.primary ? <TargetChip target={decision.primary} /> : null}
      </div>
      {decision.fallbacks.length > 0 ? (
        <div className="flex flex-col gap-1.5">
          <span className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {t('routing.decision.fallbacks')}
          </span>
          <div className="flex flex-wrap items-center gap-1.5">
            {decision.fallbacks.map((target, i) => (
              <span
                key={`${target.provider_ref}/${target.model_ref}`}
                className="flex items-center gap-1.5"
              >
                {i > 0 ? (
                  <ArrowRight className="size-3 text-muted-foreground" />
                ) : null}
                <TargetChip target={target} />
              </span>
            ))}
          </div>
        </div>
      ) : null}
      <p className="text-xs text-muted-foreground">
        {t('routing.decision.reason')}:{' '}
        <span className="font-mono">{decision.reason}</span>
      </p>
    </div>
  )
}

// --- key references (masked) -------------------------------------------------

/** A masked key hint — NEVER the secret. The lock + tooltip make the minimal-data
 *  guarantee explicit (docs/SECURITY-HARDENING.md).*/
export function MaskedHint({ hint }: { hint: string }) {
  const { t } = useTranslation('models')
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex items-center gap-1.5 font-mono text-xs text-muted-foreground">
          <Lock className="size-3 text-border-strong" />
          {hint || '—'}
        </span>
      </TooltipTrigger>
      <TooltipContent>{t('keys.maskedOnly')}</TooltipContent>
    </Tooltip>
  )
}

export function KeyRefsTable({
  keys,
  onDelete,
}: {
  keys: KeyRef[]
  onDelete?: (k: KeyRef) => void
}) {
  const { t, i18n } = useTranslation('models')
  const columns = useMemo<TableColumn<KeyRef>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('keys.columns.name'),
        cell: ({ row }) => (
          <span className="flex items-center gap-2 text-foreground">
            <KeyRound className="size-3.5 text-muted-foreground" />
            {row.original.name}
          </span>
        ),
      },
      {
        accessorKey: 'provider_ref',
        header: t('keys.columns.provider'),
        cell: ({ row }) => (
          <span className="text-muted-foreground">
            {row.original.provider_ref}
          </span>
        ),
      },
      {
        accessorKey: 'ref_kind',
        header: t('keys.columns.kind'),
        cell: ({ row }) => (
          <Badge variant="neutral">
            {t(`keys.kind.${row.original.ref_kind}`, {
              defaultValue: humanize(row.original.ref_kind),
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'ext_id',
        header: t('keys.columns.extId'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.ext_id}
          </span>
        ),
      },
      {
        accessorKey: 'hint',
        header: t('keys.columns.hint'),
        cell: ({ row }) => <MaskedHint hint={row.original.hint} />,
      },
      {
        accessorKey: 'owner_ref',
        header: t('keys.columns.owner'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.owner_ref || '—'}
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('keys.columns.status'),
        cell: ({ row }) => <StatusBadge status={row.original.status} />,
      },
      {
        accessorKey: 'created_at',
        header: t('keys.columns.created'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {formatDate(row.original.created_at, i18n.language)}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<KeyRef>
      columns={columns}
      data={keys}
      getRowId={(r) => r.id}
      onRowClick={onDelete}
      className={cn(onDelete && 'cursor-pointer')}
      empty={
        <EmptyState
          title={t('empty.keyRefs.title')}
          description={t('empty.keyRefs.description')}
        />
      }
    />
  )
}
