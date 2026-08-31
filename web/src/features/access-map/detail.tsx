// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useNavigate } from '@tanstack/react-router'
import {
  ArrowUpRight,
  GitBranch,
  Hash,
  Lock,
  Locate,
  Network,
  RefreshCw,
  ShieldQuestion,
} from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AccessModeBadge, ConfidenceBadge } from '@/components/data/badges'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ErrorState } from '@/components/ui/error-state'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import {
  type Authority,
  classifyAuthority,
  DRIFT_PERMISSION,
  type DriftLookup,
} from './authority'
import type { AccessEdge } from './types'
import { AttackPathsPanel } from './attack-paths'

/** What the detail sheet is showing: a single edge, or a node (for expansion). */
export type Selection =
  | { type: 'edge'; edge: AccessEdge }
  | {
      type: 'node'
      id: string
      kind: string
      ref: string
      role: 'origin' | 'resource'
      /** Aggregate graph nodes have no engine resource id and cannot root exfil. */
      cluster?: boolean
    }
  | null

/** Outcome of step 4 (verify), owned by the view that can refetch /drift. */
export interface RecheckState {
  status: 'idle' | 'checking' | 'clear' | 'present' | 'unknown'
  /** The edge the last verdict belongs to — a verdict never outlives its edge. */
  edgeId?: string
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[7rem_1fr] items-start gap-2 py-1.5 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words text-foreground">{children}</span>
    </div>
  )
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-xs break-all">{children}</span>
}

export function AccessDetailSheet({
  selection,
  onClose,
  onExpand,
  expandError,
  drift,
  recheck,
  onRecheck,
  onCheckDrift,
  canDrift,
}: {
  selection: Selection
  onClose: () => void
  /** Expand a node's neighbors into the graph (privileged, audited like /graph). */
  onExpand?: (id: string, kind: string) => void
  /** A failed neighbors read is distinct from a successfully empty expansion. */
  expandError?: unknown | null
  /** What is known about this edge's drift state — including that nothing is. */
  drift: DriftLookup
  recheck?: RecheckState
  /** Re-ask the engine whether this edge is still drifting (step 4). */
  onRecheck?: (edgeId: string) => void
  /** Perform the drift read this sheet has not had (turns the overlay on). */
  onCheckDrift?: () => void
  /** Whether the principal may read the diff at all — decides which honest answer applies. */
  canDrift?: boolean
}) {
  const { t } = useTranslation('accessMap')
  const open = selection !== null
  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full sm:max-w-md">
        {selection?.type === 'edge' && (
          <EdgeDetail
            edge={selection.edge}
            drift={drift}
            recheck={recheck}
            onRecheck={onRecheck}
            onCheckDrift={onCheckDrift}
            canDrift={canDrift}
          />
        )}
        {selection?.type === 'node' && (
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2">
              <Network className="size-4 text-accent-text" />
              {t(`kinds.${selection.kind}`, { defaultValue: selection.kind })}
            </SheetTitle>
            <SheetDescription>{t('detail.nodeSubtitle')}</SheetDescription>
            <Separator className="my-2" />
            <div className="text-left">
              <Row label={t('detail.ref')}>
                <Mono>{selection.ref || selection.id}</Mono>
              </Row>
              <Row label={t('detail.id')}>
                <Mono>{selection.id}</Mono>
              </Row>
            </div>
            {onExpand && !selection.cluster && (
              <>
                <Button
                  variant="secondary"
                  size="sm"
                  className="mt-3 w-fit"
                  onClick={() => onExpand(selection.id, selection.kind)}
                >
                  <Locate className="size-3.5" /> {t('detail.expand')}
                </Button>
                {expandError ? (
                  <ErrorState
                    className="py-4"
                    retry={() => onExpand(selection.id, selection.kind)}
                  />
                ) : null}
              </>
            )}
            {/* Reachability/escalation are rooted at an agent; exfiltration is rooted at a
                resource. The handler contracts use different query parameters, so the node
                role must survive selection instead of guessing from the resource kind. */}
            {!selection.cluster &&
              ((selection.role === 'origin' && selection.kind === 'agent') ||
                selection.role === 'resource') && (
                <>
                  <Separator className="my-2" />
                  <AttackPathsPanel
                    key={`${selection.role}:${selection.id}`}
                    subject={{
                      id: selection.id,
                      kind:
                        selection.role === 'resource' ? 'resource' : 'agent',
                    }}
                  />
                </>
              )}
          </SheetHeader>
        )}
      </SheetContent>
    </Sheet>
  )
}

function EdgeDetail({
  edge,
  drift,
  recheck,
  onRecheck,
  onCheckDrift,
  canDrift,
}: {
  edge: AccessEdge
  drift: DriftLookup
  recheck?: RecheckState
  onRecheck?: (edgeId: string) => void
  onCheckDrift?: () => void
  canDrift?: boolean
}) {
  const { t } = useTranslation('accessMap')
  const sources = (edge.signal_sources || edge.signal_source || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
  // ONE CLASSIFICATION FOR THE WHOLE SHEET (second contrast). The red headline used to
  // be computed here as a raw `observed && !permitted`, independently of the section below —
  // so a sheet could shout "Observed without a matching grant" in danger red while its own
  // explanation said the drift set had not been read, or that the engine cannot decide this
  // edge yet. The mirror contract is explicit that a pending finding is amber and NEVER red
  // (types.ts). A headline and an explanation drawn from two different derivations will
  // eventually disagree, and the louder one wins with the operator.
  const authority = classifyAuthority(edge, drift)
  const unexpected = authority.cls === 'unpermitted'
  return (
    <>
      <SheetHeader>
        <SheetTitle className="flex items-center gap-2">
          {t('detail.edgeTitle')}
          <AccessModeBadge mode={edge.mode} />
        </SheetTitle>
        <SheetDescription className="font-mono text-xs break-all">
          {edge.origin_ref || edge.origin_kind} →{' '}
          {edge.resource_ref || edge.resource_kind}
        </SheetDescription>
      </SheetHeader>

      <div className="overflow-y-auto text-left">
        {unexpected && (
          <div className="mb-2 flex items-center gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-1.5 text-xs text-danger">
            <span className="font-medium">{t('detail.unexpectedFlag')}</span>
          </div>
        )}

        <Section title={t('detail.origin')}>
          <Row label={t('detail.kind')}>
            <Badge variant="outline">{edge.origin_kind}</Badge>
          </Row>
          <Row label={t('detail.ref')}>
            <Mono>{edge.origin_ref || '—'}</Mono>
          </Row>
          {edge.attribution_tier && (
            <Row label={t('detail.attribution')}>
              <AttributionTierBadge
                tier={edge.attribution_tier}
                reason={edge.attribution_tier_reason}
              />
            </Row>
          )}
          {!edge.bridged && (
            <Row label={t('detail.bridge')}>
              <span className="inline-flex items-center gap-1 text-warning">
                <GitBranch className="size-3.5" /> {t('detail.unbridged')}
              </span>
            </Row>
          )}
        </Section>

        <Section title={t('detail.resource')}>
          <Row label={t('detail.kind')}>
            <Badge variant="outline">{edge.resource_kind || 'resource'}</Badge>
          </Row>
          <Row label={t('detail.ref')}>
            <Mono>{edge.resource_ref || '—'}</Mono>
          </Row>
          {edge.coverage_tier && (
            <Row label={t('detail.coverage')}>
              <Badge variant="outline">
                {t(`coverage.${edge.coverage_tier}`, {
                  defaultValue: edge.coverage_tier,
                })}
              </Badge>
            </Row>
          )}
          {edge.tool_ref && (
            <Row label={t('detail.tool')}>
              <Mono>{edge.tool_ref}</Mono>
            </Row>
          )}
        </Section>

        <Section title={t('detail.signal')}>
          <Row label={t('detail.confidence')}>
            <ConfidenceBadge confidence={edge.confidence} />
          </Row>
          <Row label={t('detail.sources')}>
            <span className="flex flex-wrap gap-1">
              {sources.length
                ? sources.map((s) => (
                    <Badge key={s} variant="neutral" className="font-mono">
                      {s}
                    </Badge>
                  ))
                : '—'}
            </span>
          </Row>
          {edge.attribution_reason && (
            <Row label={t('detail.reason')}>
              <span className="text-xs text-muted-foreground">
                {edge.attribution_reason}
              </span>
            </Row>
          )}
        </Section>

        <Section title={t('detail.diff')}>
          <Row label={t('detail.observed')}>
            <Flag on={edge.observed} />
          </Row>
          <Row label={t('detail.permitted')}>
            <Flag on={edge.permitted} />
          </Row>
        </Section>

        <Section title={t('detail.activity')}>
          <Row label={t('detail.occurrences')}>
            <span className="font-mono tabular-nums">
              {edge.occurrence_count}
            </span>
          </Row>
          <Row label={t('detail.firstSeen')}>
            <RelTimeLabel ts={edge.first_seen} />
          </Row>
          <Row label={t('detail.lastSeen')}>
            <RelTimeLabel ts={edge.last_seen} />
          </Row>
        </Section>

        <AuthoritySection
          edge={edge}
          authority={authority}
          recheck={recheck}
          onRecheck={onRecheck}
          onCheckDrift={onCheckDrift}
          canDrift={canDrift}
        />

        <Separator className="my-2" />
        <p className="flex items-start gap-1.5 text-[11px] leading-snug text-muted-foreground">
          <Lock className="mt-0.5 size-3 shrink-0" />
          {t('detail.minimalData')}
        </p>
      </div>
    </>
  )
}

/**
 * WHY THIS EDGE EXISTS, AND WHERE TO CHANGE IT — the four-step loop the map was
 * missing: select (already there) → explain the authority → go where it is changed →
 * re-ask whether the drift is gone.
 *
 * Every sentence here is the ENGINE's: the class comes from `signal_sources`/`permitted`
 * against the closed permitted-side set (authority.ts), and the verification comes from
 * re-fetching /drift. Nothing about who may do what is computed or cached in the client,
 * so the map stays a reflection and never becomes a second permission store — the bound
 * the brief set on this work.
 */
function AuthoritySection({
  edge,
  authority,
  recheck,
  onRecheck,
  onCheckDrift,
  canDrift,
}: {
  edge: AccessEdge
  /** Classified ONCE by EdgeDetail, so the headline and this explanation cannot disagree. */
  authority: Authority
  recheck?: RecheckState
  onRecheck?: (edgeId: string) => void
  onCheckDrift?: () => void
  canDrift?: boolean
}) {
  const { t } = useTranslation('accessMap')
  const { can } = useAuth()
  const navigate = useNavigate()

  // A verdict belongs to the edge it was measured on. Without this guard, opening a
  // second edge would inherit the previous one's "clear" — a fix reported for an edge
  // that was never checked.
  const verdict =
    recheck && recheck.edgeId === edge.id ? recheck.status : 'idle'

  return (
    <Section title={t('authority.title')}>
      <div className="py-2">
        <p className="text-xs leading-snug text-foreground">
          {t(`authority.explain.${authority.cls}`)}
        </p>
        {authority.signals.length > 0 && (
          <p className="mt-1.5 flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
            {t('authority.declaredBy')}
            {authority.signals.map((s) => (
              <Badge key={s} variant="neutral" className="font-mono">
                {s}
              </Badge>
            ))}
          </p>
        )}
        {/* `signal_sources` is omitempty (modules/access-map/dto.go:30) and the UI contract
            separates the two fields by name: the scalar is the LAST signal, the plural is
            every signal that corroborates. Absent is
            therefore "you were not told", never "nothing corroborates it" — so when the set
            did not arrive, every sentence on this panel about which signals are or are not
            present is qualified here rather than quietly drawn from the scalar. */}
        {!authority.signalsFused && (
          <p className="mt-1.5 text-[11px] leading-snug text-muted-foreground">
            {t('authority.signalsNotFused')}
          </p>
        )}
        {/* Two facts the operator cannot act correctly without, and both are properties of
            the ENGINE, not of this screen. The map never retracts (edges OR-merge forever),
            so editing the source does not remove the edge; and a fused edge can carry more
            than one independent permit, so addressing one leaves the other standing. */}
        {authority.multiple && (
          <p className="mt-1.5 text-[11px] leading-snug text-warning">
            {t('authority.multiple')}
          </p>
        )}
        {/* WHAT THE RE-CHECK CAN AND CANNOT EVER SAY, before the button is pressed. The two
            halves of the drift set behave oppositely under a monotonic store, so one generic
            "it can persist" line covered a case where the edge ALWAYS persists — and an
            operator told "still in the drift set" after a deletion that worked goes looking
            for a failure that did not happen. `closure` is only non-`unknown` when the drift
            read named which half this is, so neither sentence is ever a guess. */}
        {authority.closure === 'cannotClose' && (
          <p className="mt-1.5 text-[11px] leading-snug text-warning">
            {t('authority.cannotClose')}
          </p>
        )}
        {authority.closure === 'closes' && (
          <p className="mt-1.5 text-[11px] leading-snug text-muted-foreground">
            {t('authority.closes')}
          </p>
        )}
        {/* Non-retraction is a property of the edge carrying a PERMIT, not of which signals
            happen to name it: an `unattributed` permit is just as unretractable, and gating
            on the signal list hid the warning exactly where the map knows least. */}
        {edge.permitted && authority.closure === 'unknown' && (
          <p className="mt-1.5 text-[11px] leading-snug text-muted-foreground">
            {t('authority.noRetraction')}
          </p>
        )}

        {/* This edge's drift state was not ESTABLISHED, so there is no finding to remedy —
            only the read that would settle it. Offer THAT, or say which permission is
            missing; never a remediation link for a violation nobody confirmed. The copy
            names no cause on purpose: the state covers every way of not having a successful
            read, and saying which one happened is a claim this sheet cannot make. */}
        {authority.cls === 'unchecked' &&
          (onCheckDrift ? (
            <Button
              variant="secondary"
              size="sm"
              className="mt-2.5 w-fit"
              onClick={onCheckDrift}
            >
              <ShieldQuestion className="size-3.5" aria-hidden />
              {t('authority.checkDrift')}
            </Button>
          ) : canDrift === false ? (
            <p className="mt-2.5 text-[11px] leading-snug text-muted-foreground">
              {t('authority.needsPermission', {
                action: t('authority.checkDrift'),
                permission: DRIFT_PERMISSION,
              })}
            </p>
          ) : null)}

        {/* Step 3 — go where the authority is actually changed. */}
        <div className="mt-2.5 flex flex-col gap-1.5">
          {authority.targets.map((target) => {
            const allowed = can(target.permission)
            if (!allowed) {
              // Do NOT offer a link that lands on a Forbidden page, and do not pretend
              // the action does not exist: name the permission it needs.
              return (
                <p
                  key={`${target.key}-denied`}
                  className="text-[11px] leading-snug text-muted-foreground"
                >
                  {t('authority.needsPermission', {
                    action: t(`authority.target.${target.key}`),
                    permission: target.permission,
                  })}
                </p>
              )
            }
            return (
              <Button
                key={target.key}
                variant="secondary"
                size="sm"
                className="w-fit"
                onClick={() =>
                  // Feature routes are generated from the registry, so their paths are
                  // not in the statically-typed route tree — the same loose cast the
                  // NHI roster uses to come the other way (nhi-roster.tsx:249).
                  navigate({
                    to: target.to,
                    search: target.search,
                  } as never)
                }
              >
                <ArrowUpRight className="size-3.5" aria-hidden />
                {t(`authority.target.${target.key}`)}
              </Button>
            )
          })}
        </div>

        {/* Step 4 — verify by re-asking, never by assuming. */}
        {onRecheck && (
          <div className="mt-3 border-t border-border pt-2.5">
            <Button
              variant="ghost"
              size="sm"
              className="w-fit"
              disabled={verdict === 'checking'}
              onClick={() => onRecheck(edge.id)}
            >
              <RefreshCw
                className={cn(
                  'size-3.5',
                  verdict === 'checking' && 'animate-spin',
                )}
                aria-hidden
              />
              {t('authority.recheck')}
            </Button>
            {verdict !== 'idle' && verdict !== 'checking' && (
              <p
                className={cn(
                  'mt-1.5 flex items-start gap-1.5 text-[11px] leading-snug',
                  verdict === 'clear'
                    ? 'text-confidence-attributed'
                    : verdict === 'present'
                      ? 'text-warning'
                      : 'text-muted-foreground',
                )}
                role="status"
              >
                {verdict === 'unknown' && (
                  <ShieldQuestion
                    className="mt-0.5 size-3 shrink-0"
                    aria-hidden
                  />
                )}
                {t(`authority.verdict.${verdict}`)}
              </p>
            )}
          </div>
        )}
      </div>
    </Section>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="py-1.5">
      <h3 className="mb-0.5 flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        <Hash className="size-3 opacity-60" />
        {title}
      </h3>
      <div className={cn('rounded-md border border-border bg-muted/40 px-2.5')}>
        {children}
      </div>
    </div>
  )
}

function Flag({ on }: { on: boolean }) {
  const { t } = useTranslation('accessMap')
  return on ? (
    <Badge variant="success">{t('detail.yes')}</Badge>
  ) : (
    <Badge variant="neutral">{t('detail.no')}</Badge>
  )
}

/** Honest per-edge attribution firmness (G8). firm = solid/success,
 * approximate = amber/warning, unknown = low-emphasis outline — never rendered as if
 * a non-firm tier were firm. The reason (or the per-tier hint) rides the tooltip. */
function AttributionTierBadge({
  tier,
  reason,
}: {
  tier: string
  reason?: string
}) {
  const { t } = useTranslation('accessMap')
  const variant =
    tier === 'firm' ? 'success' : tier === 'approximate' ? 'warning' : 'outline'
  const hint =
    reason ||
    t(`attributionTierHint.${tier}`, { defaultValue: '' }) ||
    undefined
  return (
    <Badge variant={variant} title={hint}>
      {t(`attributionTier.${tier}`, { defaultValue: tier })}
    </Badge>
  )
}
