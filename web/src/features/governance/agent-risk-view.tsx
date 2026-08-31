// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useInfiniteQuery } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCheck,
  Plus,
  ScrollText,
  SlidersHorizontal,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { ForbiddenState } from '@/components/ui/error-state'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { EmptyState } from '@/components/ui/empty-state'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { RelTimeLabel } from '@/features/shared'
import { governanceApi, governanceKeys } from './api'
import './i18n'
import {
  AGENT_RISK_TIERS,
  effectiveTierSource,
  type AgentRiskProfileDTO,
  type AgentRiskSignals,
  type ClassifyAgentRiskInput,
  type SetAgentRiskTierInput,
} from './types'

/** Sentinel Select value for "clear the operator override" (maps to tier=""). */
const CLEAR_OVERRIDE = '__clear__'

// The register is a RISK surface: never silently drop rows. Page through the
// cursor like the other governance lists so search + counts cover the whole
// estate, not just the first page.
const AGENT_RISK_PAGE_SIZE = 100

const TIER_VARIANT: Record<string, BadgeVariant> = {
  low: 'success',
  medium: 'info',
  high: 'warning',
  critical: 'danger',
}

const STATE_VARIANT: Record<string, BadgeVariant> = {
  unclassified: 'neutral',
  suggested: 'info',
  reviewed: 'success',
}

/**
 * AgentRiskView is the operating console for the governance per-agent risk tier
 * lifecycle: classify from observed signals → operator override → human
 * review. Read gates on governance:agent-risk:read; classify on
 * governance:agent-risk:write; tier override + review on
 * governance:agent-risk:admin.
 *
 * HONEST rendering is the whole point: `effective_tier` is what enforcement reads
 * (operator_tier when overridden, else the heuristic suggested_tier). The console
 * shows the effective tier prominently and NEVER conflates the operator's
 * declaration with the heuristic — an override is badged as such, a suggestion is
 * labelled as heuristic, and "reviewed" is only ever claimed when the state says so.
 */
export function AgentRiskView() {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant, can } = useAuth()
  const canRead = can('governance:agent-risk:read')
  const canWrite = can('governance:agent-risk:write')
  const canAdmin = can('governance:agent-risk:admin')

  const [classifyOpen, setClassifyOpen] = useState(false)
  const [override, setOverride] = useState<AgentRiskProfileDTO | null>(null)
  const [review, setReview] = useState<AgentRiskProfileDTO | null>(null)

  const profiles = useInfiniteQuery({
    queryKey: governanceKeys.agentRiskProfiles(activeTenant),
    queryFn: ({ pageParam }) =>
      governanceApi.listAgentRiskProfiles({
        cursor: pageParam,
        limit: AGENT_RISK_PAGE_SIZE,
      }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) =>
      page.has_more && page.cursor ? page.cursor : undefined,
    enabled: canRead,
  })

  const items = profiles.data?.pages.flatMap((page) => page.items) ?? []

  const reviewMutation = usePrivilegedMutation<void, AgentRiskProfileDTO>({
    mutationFn: () => governanceApi.reviewAgentRisk(review!.id),
    invalidateKeys: () => [governanceKeys.agentRiskProfiles(activeTenant)],
    successMessage: t('agentRisk.review.done'),
    onDone: () => setReview(null),
  })

  if (!canRead) {
    return (
      <ForbiddenState
        title={t('agentRisk.forbiddenTitle')}
        description={t('agentRisk.forbiddenBody')}
      />
    )
  }

  const columns: TableColumn<AgentRiskProfileDTO, unknown>[] = [
    {
      accessorKey: 'agent_id',
      header: t('agentRisk.columns.agent'),
      cell: ({ row }) => (
        <span className="font-mono text-xs font-medium text-foreground">
          {row.original.agent_id}
        </span>
      ),
    },
    {
      accessorKey: 'suggested_tier',
      header: t('agentRisk.columns.suggested'),
      cell: ({ row }) => (
        <span className="text-xs text-muted-foreground">
          {tierLabel(t, row.original.suggested_tier)}
        </span>
      ),
    },
    {
      accessorKey: 'operator_tier',
      header: t('agentRisk.columns.operator'),
      cell: ({ row }) =>
        row.original.operator_tier ? (
          <TierBadge tier={row.original.operator_tier} />
        ) : (
          <span className="text-xs text-muted-foreground">
            {t('agentRisk.noOperator')}
          </span>
        ),
    },
    {
      accessorKey: 'effective_tier',
      header: t('agentRisk.columns.effective'),
      // The one enforcement reads — prominent, with an honest provenance cue that
      // never conflates an operator override with a heuristic suggestion.
      cell: ({ row }) => <EffectiveCell profile={row.original} />,
    },
    {
      accessorKey: 'state',
      header: t('agentRisk.columns.state'),
      cell: ({ row }) => <StateCell profile={row.original} />,
    },
    {
      id: 'signals',
      header: t('agentRisk.columns.signals'),
      cell: ({ row }) => <SignalsSummary signals={row.original.signals} />,
    },
    ...(canAdmin
      ? [
          {
            id: 'actions',
            header: '',
            cell: ({ row }: { row: { original: AgentRiskProfileDTO } }) => (
              <div className="flex items-center justify-end gap-1">
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation()
                    setOverride(row.original)
                  }}
                >
                  <SlidersHorizontal aria-hidden />
                  {t('agentRisk.override.button')}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={(e) => {
                    e.stopPropagation()
                    setReview(row.original)
                  }}
                >
                  <CheckCheck aria-hidden />
                  {t('agentRisk.review.button')}
                </Button>
              </div>
            ),
          } as TableColumn<AgentRiskProfileDTO, unknown>,
        ]
      : []),
  ]

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">{t('agentRisk.caption')}</p>

      <DataTable
        columns={columns}
        data={items}
        isLoading={profiles.isLoading}
        error={profiles.error}
        onRetry={() => profiles.refetch()}
        searchable
        searchPlaceholder={t('agentRisk.search')}
        getRowId={(r) => r.id}
        hasMore={profiles.hasNextPage}
        onLoadMore={() => void profiles.fetchNextPage()}
        isFetchingMore={profiles.isFetchingNextPage}
        empty={
          <EmptyState
            title={t('empty.agentRisk.title')}
            description={t('empty.agentRisk.description')}
          />
        }
        toolbar={
          canWrite ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setClassifyOpen(true)}
            >
              <Plus aria-hidden />
              {t('agentRisk.classifyButton')}
            </Button>
          ) : undefined
        }
      />

      {canWrite && (
        <ClassifyDialog open={classifyOpen} onOpenChange={setClassifyOpen} />
      )}

      {canAdmin && override && (
        <OverrideDialog
          profile={override}
          open={!!override}
          onOpenChange={(o) => !o && setOverride(null)}
        />
      )}

      {canAdmin && review && (
        <ConfirmDialog
          open={!!review}
          onOpenChange={(o) => !o && setReview(null)}
          title={t('agentRisk.review.title')}
          description={t('agentRisk.review.body')}
          confirmLabel={t('agentRisk.review.confirm')}
          pending={reviewMutation.isPending}
          onConfirm={() => reviewMutation.mutate()}
        />
      )}
    </div>
  )
}

function tierLabel(
  t: (key: string, opts?: Record<string, unknown>) => string,
  tier: string | undefined,
): string {
  if (!tier) return t('agentRisk.tier.none')
  return t(`agentRisk.tier.${tier}`, { defaultValue: tier })
}

function TierBadge({ tier }: { tier: string }) {
  const { t } = useTranslation('governance')
  const key = tier.toLowerCase()
  const emphatic = key === 'critical'
  return (
    <Badge
      variant={TIER_VARIANT[key] ?? 'neutral'}
      className={emphatic ? 'font-semibold uppercase tracking-wide' : undefined}
    >
      {emphatic ? <AlertTriangle className="size-3" aria-hidden /> : null}
      {t(`agentRisk.tier.${key}`, { defaultValue: tier })}
    </Badge>
  )
}

function EffectiveCell({ profile }: { profile: AgentRiskProfileDTO }) {
  const { t } = useTranslation('governance')
  const source = effectiveTierSource(profile)
  return (
    <div className="flex flex-col items-start gap-1">
      {/* Scoped to JUST the tier value (no provenance cue), so a test can assert
          the effective tier is rendered — not merely that some matching text is
          present in the row. */}
      <span data-testid={`effective-tier-${profile.id}`}>
        {profile.effective_tier ? (
          <TierBadge tier={profile.effective_tier} />
        ) : (
          <span className="text-xs text-muted-foreground">
            {t('agentRisk.effectiveNone')}
          </span>
        )}
      </span>
      {source === 'operator' ? (
        <Badge variant="warning">{t('agentRisk.overrideBadge')}</Badge>
      ) : source === 'suggested' ? (
        <span className="text-[11px] text-muted-foreground">
          {t('agentRisk.effectiveFromSuggested')}
        </span>
      ) : null}
    </div>
  )
}

function StateCell({ profile }: { profile: AgentRiskProfileDTO }) {
  const { t } = useTranslation('governance')
  const state = profile.state
  return (
    <div className="flex flex-col items-start gap-1">
      <Badge variant={STATE_VARIANT[state] ?? 'neutral'}>
        {t(`agentRisk.stateValue.${state}`, {
          defaultValue: t('agentRisk.stateValue.unknown'),
        })}
      </Badge>
      {/* Only ever surface reviewed_by/at when the state actually says reviewed. */}
      {state === 'reviewed' && profile.reviewed_by && (
        <span className="font-mono text-[11px] text-muted-foreground">
          {profile.reviewed_by}
        </span>
      )}
      {state === 'reviewed' && profile.reviewed_at && (
        <span className="text-[11px] text-muted-foreground">
          <RelTimeLabel ts={profile.reviewed_at} />
        </span>
      )}
    </div>
  )
}

function SignalsSummary({ signals }: { signals?: AgentRiskSignals }) {
  const { t } = useTranslation('governance')
  if (!signals) {
    return (
      <span className="text-xs text-muted-foreground">
        {t('agentRisk.signals.none')}
      </span>
    )
  }
  const crit = signals.critical_severity_findings ?? 0
  const high = signals.high_severity_findings ?? 0
  return (
    <div className="flex flex-wrap gap-1">
      {/* ⛔ VA PRIMERO, y no es orden estético: si el barrido se truncó, las cuentas de al lado son
          un SUELO, no una medición, y el nivel pudo quedarse corto. Leer «0 críticos» junto a un
          nivel alto sin saber esto es incomprensible; sabiéndolo, es una instrucción: repetir la
          clasificación, no investigar al agente. */}
      {signals.truncated && (
        <Badge variant="warning">{t('agentRisk.signals.truncated')}</Badge>
      )}
      {crit > 0 && (
        <Badge variant="danger">
          {t('agentRisk.signals.critFindings', { value: crit })}
        </Badge>
      )}
      {high > 0 && (
        <Badge variant="warning">
          {t('agentRisk.signals.highFindings', { value: high })}
        </Badge>
      )}
      <Badge variant="neutral">
        {t('agentRisk.signals.rwEdges', { value: signals.rw_edges ?? 0 })}
      </Badge>
      <Badge variant="neutral">
        {t('agentRisk.signals.resources', {
          value: signals.distinct_resources ?? 0,
        })}
      </Badge>
      {signals.autonomous ? (
        <Badge variant="warning">{t('agentRisk.signals.autonomous')}</Badge>
      ) : null}
      {signals.scheduled ? (
        <Badge variant="outline">{t('agentRisk.signals.scheduled')}</Badge>
      ) : null}
    </div>
  )
}

function ClassifyDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        {open && <ClassifyForm onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function ClassifyForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  const [agentId, setAgentId] = useState('')
  const valid = agentId.trim().length > 0

  const mutation = usePrivilegedMutation<
    ClassifyAgentRiskInput,
    AgentRiskProfileDTO
  >({
    mutationFn: (input) => governanceApi.classifyAgentRisk(input),
    invalidateKeys: () => [governanceKeys.agentRiskProfiles(activeTenant)],
    successMessage: t('agentRisk.classify.done'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    mutation.mutate({ agent_id: agentId.trim() })
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('agentRisk.classify.title')}</DialogTitle>
        <DialogDescription>{t('agentRisk.classify.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('agentRisk.classify.agent')}
          htmlFor="classify-agent-id"
          description={t('agentRisk.classify.agentHint')}
          required
        >
          <Input
            id="classify-agent-id"
            value={agentId}
            onChange={(e) => setAgentId(e.target.value)}
            mono
          />
        </Field>
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('agentRisk.classify.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}

function OverrideDialog({
  profile,
  open,
  onOpenChange,
}: {
  profile: AgentRiskProfileDTO
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
        {open && (
          <OverrideForm profile={profile} onClose={() => onOpenChange(false)} />
        )}
      </DialogContent>
    </Dialog>
  )
}

function OverrideForm({
  profile,
  onClose,
}: {
  profile: AgentRiskProfileDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['governance', 'common'])
  const { activeTenant } = useAuth()
  // Seed with the operator's current declaration, else the heuristic suggestion.
  const [value, setValue] = useState(
    profile.operator_tier || profile.suggested_tier || 'low',
  )

  const mutation = usePrivilegedMutation<
    SetAgentRiskTierInput,
    AgentRiskProfileDTO
  >({
    mutationFn: (input) => governanceApi.setAgentRiskTier(profile.id, input),
    invalidateKeys: () => [governanceKeys.agentRiskProfiles(activeTenant)],
    successMessage: t('agentRisk.override.done'),
    onDone: onClose,
  })

  function submit() {
    // The clear sentinel sends tier="" to drop the override → fall back to the
    // heuristic suggestion.
    mutation.mutate({ tier: value === CLEAR_OVERRIDE ? '' : value })
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('agentRisk.override.title')}</DialogTitle>
        <DialogDescription>{t('agentRisk.override.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <p className="text-xs text-muted-foreground">
          {t('agentRisk.override.currentSuggested', {
            tier: tierLabel(t, profile.suggested_tier),
          })}
        </p>
        <Field
          label={t('agentRisk.override.tierLabel')}
          description={t('agentRisk.override.tierHint')}
        >
          <Select value={value} onValueChange={setValue}>
            <SelectTrigger aria-label={t('agentRisk.override.tierLabel')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {AGENT_RISK_TIERS.map((tier) => (
                <SelectItem key={tier} value={tier}>
                  {t(`agentRisk.tier.${tier}`)}
                </SelectItem>
              ))}
              <SelectSeparator />
              <SelectItem value={CLEAR_OVERRIDE}>
                {t('agentRisk.override.clear')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>

      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <ScrollText className="size-3.5 shrink-0" aria-hidden />
        {t('common:privileged.auditedNotice')}
      </p>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('agentRisk.override.confirm')}
        </Button>
      </DialogFooter>
    </>
  )
}
