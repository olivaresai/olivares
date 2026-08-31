// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ANT2-08/07 — WIF graph + linter. Renders the fdis_ → fdrl_ → svac_ → scope graph
// with the WebGL primitive and runs the client-side linter (CEL/subject over-broad,
// key-shadow footgun, token lifetimes, scope over-broad, jwks, and declared-vs-actual
// drift). When the backend reconciles against the live WIF Admin API (org:admin OAuth) the graph shows the ACTUAL config with per-object provenance and drift; writes
// remain Console-only, so the panel VISUALISES + LINTS + shows drift — NO create/edit, at
// most a deep link to the Anthropic Console. Viewing it is a privileged, audited action
// gated at AAL3.
import { useQuery } from '@tanstack/react-query'
import {
  CircleAlert,
  CircleCheck,
  ExternalLink,
  KeyRound,
  ShieldX,
} from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AsyncSection,
  SectionCard,
  SelfAuditNotice,
  SeverityBadge,
} from '@/features/_intel'
import { SigmaGraph } from '@/features/shared'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { useAuth } from '@/lib/auth/context'
import { identityApi, identityKeys } from '../api'
import { AAL, RequireAssurance } from '../assurance'
import { AuthorityReferences, AUTHORITY } from '../references'
import type {
  WifGraphData,
  WifLintFinding,
  WifReconciliation,
} from '../types'
import { buildWifGraph, wifNodeColor } from './wif-graph-build'
import { lintSummary, lintWifGraph, SHADOW_ENV_VARS } from './wif-lint'

const LINT_RULES = [
  'cel-over-broad',
  'key-shadow',
  'token-lifetime',
  'scope-over-broad',
  'jwks-insecure',
  'drift',
] as const

export function WifGraphTab() {
  const { t } = useTranslation('identity')
  return (
    <RequireAssurance minAal={AAL.HARDWARE} action="wif">
      <WifGraphContent />
      <p className="sr-only">{t('wif.gatedNote')}</p>
    </RequireAssurance>
  )
}

function WifGraphContent() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()

  // REAL: the key-shadow footgun is emitted as a governance Finding on the
  // anthropic.federation subject (it shows TODAY, independent of the WIF graph).
  const footgun = useQuery({
    queryKey: identityKeys.findings(activeTenant, 'wif-footgun'),
    queryFn: () =>
      identityApi.findings({
        kind: 'governance',
        subject_kind: 'anthropic.federation',
      }),
    retry: false,
  })
  // LIVE (flip): the rich WIF objects (CEL/lifetime/scope) the linter renders over,
  // served by GET /v1/m/identity/wif (modules/governance/identityconsole.go). The
  // route ALWAYS answers 200 — an empty federation is an honest empty state, not a seam.
  const graph = useQuery({
    queryKey: identityKeys.wif(activeTenant),
    queryFn: () => identityApi.wifGraph(),
    retry: false,
  })

  const footgunFinding = footgun.data?.items[0]
  // subject_ref is optional on a Finding; fall back to the canonical env var name
  // (as the linter does) so the banner never renders a literal "undefined".
  const footgunVar = footgunFinding?.subject_ref || SHADOW_ENV_VARS[0]

  const { nodes, edges, lint } = useMemo(() => {
    if (!graph.data)
      return { nodes: [], edges: [], lint: [] as WifLintFinding[] }
    const merged: WifGraphData = {
      ...graph.data,
      key_shadow: footgunFinding
        ? { present: true, var: footgunVar }
        : graph.data.key_shadow,
    }
    const lintFindings = lintWifGraph(merged)
    const built = buildWifGraph(merged, lintFindings)
    return { ...built, lint: lintFindings }
  }, [graph.data, footgunFinding, footgunVar])

  const summary = lintSummary(lint)

  return (
    <div className="flex flex-col gap-6">
      <SelfAuditNotice />

      {/* Console-only writes: VISUALISE/LINT/reconcile, never CRUD. */}
      <div
        className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/30 px-3 py-2"
        role="note"
      >
        <p className="text-xs text-muted-foreground">{t('wif.consoleOnly')}</p>
        <Button asChild variant="outline" size="sm">
          <a
            href={AUTHORITY.workloadIdentity}
            target="_blank"
            rel="noreferrer noopener"
          >
            <ExternalLink className="size-4" aria-hidden />
            {t('wif.openConsole')}
          </a>
        </Button>
      </div>

      <ReconciliationStatus reconciliation={graph.data?.reconciliation} />

      {/* REAL footgun, shown today. */}
      {footgunFinding ? (
        <div className="flex flex-col gap-2 rounded-md border border-danger/40 bg-danger-soft/40 px-3 py-2.5">
          <div className="flex items-center gap-2">
            <ShieldX className="size-4 text-danger" aria-hidden />
            <span className="text-sm font-medium text-foreground">
              {t('wif.lint.key-shadow.title')}
            </span>
            <Badge variant="danger">{footgunVar}</Badge>
          </div>
          <p className="text-xs text-muted-foreground">
            {footgunFinding.title ??
              t('wif.lint.key-shadow.detail', { var: footgunVar })}
          </p>
          <p className="flex items-center gap-1.5 text-xs text-foreground">
            <KeyRound className="size-3.5 shrink-0 text-warning" aria-hidden />
            {t('wif.lint.key-shadow.recommendation')}
          </p>
        </div>
      ) : null}

      {/* The graph + lint (declared rich objects; honest pending today). */}
      <SectionCard
        title={t('wif.graphTitle')}
        description={t('wif.graphDescription')}
        actions={<LintSummaryBadges summary={summary} />}
      >
        <AsyncSection query={graph} skeletonHeight={240}>
          {(data) =>
            data.issuers.length === 0 &&
            data.rules.length === 0 &&
            data.service_accounts.length === 0 ? (
              // Live route, 200 with no federation declared — an honest empty state, NOT
              // a fabricated graph and NOT a pending seam (the backend IS live). The
              // linter's rules stay documented so the value is clear once federation lands.
              <div className="flex flex-col gap-4">
                <EmptyState
                  title={t('wif.empty')}
                  description={t('wif.emptyHint')}
                />
                <LintRulesDoc />
              </div>
            ) : (
              <div className="flex flex-col gap-4">
                <SigmaGraph
                  nodes={nodes}
                  edges={edges}
                  nodeColor={wifNodeColor}
                  ariaLabel={t('wif.graphTitle')}
                  className="h-[420px] w-full rounded-md border border-border"
                  fitKey={`${nodes.length}`}
                >
                  <WifLegend />
                </SigmaGraph>
                <LintPanel findings={lint} />
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      <AuthorityReferences
        area="wif"
        keys={['wifReference', 'workloadIdentity']}
      />
    </div>
  )
}

/** Honest declared-vs-actual reconciliation status. Reconciled → the graph shows
 *  the live config; unavailable → it shows the declared baseline and says so (never a
 *  fabricated "all clear"); absent → declared-only (no org:admin token configured). */
function ReconciliationStatus({
  reconciliation,
}: {
  reconciliation?: WifReconciliation
}) {
  const { t } = useTranslation('identity')
  if (!reconciliation) {
    return (
      <p className="text-xs text-muted-foreground">
        {t('wif.recon.declaredOnly')}
      </p>
    )
  }
  if (reconciliation.reconciled) {
    return (
      <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <CircleCheck className="size-3.5 text-success" aria-hidden />
        {t('wif.recon.reconciled', { at: reconciliation.observed_at ?? '' })}
      </p>
    )
  }
  return (
    <div
      className="flex items-center gap-2 rounded-md border border-warning/40 bg-warning-soft/30 px-3 py-2 text-xs text-foreground"
      role="status"
    >
      <CircleAlert className="size-3.5 shrink-0 text-warning" aria-hidden />
      {t('wif.recon.unavailable')}
    </div>
  )
}

function LintSummaryBadges({
  summary,
}: {
  summary: { error: number; warning: number; info: number }
}) {
  const { t } = useTranslation('identity')
  if (summary.error + summary.warning + summary.info === 0) {
    return <Badge variant="success">{t('wif.lintClean')}</Badge>
  }
  return (
    <span className="flex items-center gap-1.5">
      {summary.error > 0 && (
        <Badge variant="danger">
          {t('wif.lintErrors', { n: summary.error })}
        </Badge>
      )}
      {summary.warning > 0 && (
        <Badge variant="warning">
          {t('wif.lintWarnings', { n: summary.warning })}
        </Badge>
      )}
      {summary.info > 0 && (
        <Badge variant="info">{t('wif.lintInfo', { n: summary.info })}</Badge>
      )}
    </span>
  )
}

function LintPanel({ findings }: { findings: WifLintFinding[] }) {
  const { t } = useTranslation('identity')
  if (findings.length === 0) {
    return <p className="text-sm text-success">{t('wif.lintNoFindings')}</p>
  }
  return (
    <ul className="flex flex-col gap-2" aria-label={t('wif.lintLabel')}>
      {findings.map((f, i) => {
        // The drift rule carries a machine reason code in meta.reason; localize it so the
        // detail never renders a raw hyphenated token (e.g. "undeclared-rule") to the user.
        const meta =
          f.rule === 'drift' && typeof f.meta?.reason === 'string'
            ? { ...f.meta, reason: t(`wif.lint.drift.reason.${f.meta.reason}`) }
            : f.meta
        return (
        <li
          key={`${f.rule}-${f.subjectRef}-${i}`}
          className="flex flex-col gap-1 rounded-md border border-border bg-surface px-3 py-2"
        >
          <div className="flex flex-wrap items-center gap-2">
            <SeverityBadge severity={f.severity} />
            <span className="text-sm font-medium text-foreground">
              {t(`wif.lint.${f.rule}.title`)}
            </span>
            <code className="font-mono text-xs text-muted-foreground break-all">
              {f.subjectRef}
            </code>
          </div>
          <p className="text-xs text-muted-foreground">
            {t(`wif.lint.${f.rule}.detail`, { ...meta })}
          </p>
          {f.rule === 'key-shadow' ? (
            <p className="text-xs text-foreground">
              {t('wif.lint.key-shadow.recommendation')}
            </p>
          ) : null}
        </li>
        )
      })}
    </ul>
  )
}

/** When the rich objects are not served yet, document the rules the linter applies
 *  — the differential value is ready the moment the backend lands. */
function LintRulesDoc() {
  const { t } = useTranslation('identity')
  return (
    <div className="mt-3 rounded-md border border-border bg-muted/20 p-3">
      <p className="mb-2 flex items-center gap-2 text-xs font-medium text-muted-foreground">
        <CircleAlert className="size-3.5" aria-hidden />
        {t('wif.lintRulesTitle')}
      </p>
      <ul className="flex flex-col gap-1.5">
        {LINT_RULES.map((rule) => (
          <li key={rule} className="text-xs text-foreground">
            <span className="font-medium">{t(`wif.lint.${rule}.title`)}</span>
            <span className="text-muted-foreground">
              {' '}
              — {t(`wif.lint.${rule}.rule`)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function WifLegend() {
  const { t } = useTranslation('identity')
  const items: { kind: string; color: string }[] = [
    { kind: 'issuer', color: 'var(--color-accent-text)' },
    { kind: 'rule', color: 'var(--color-graphite-400)' },
    { kind: 'svac', color: 'var(--color-info)' },
    { kind: 'scope', color: 'var(--color-muted-foreground)' },
  ]
  return (
    <div className="absolute left-2 top-2 flex flex-col gap-1 rounded-md border border-border bg-background/90 p-2 text-xs">
      {items.map((it) => (
        <span key={it.kind} className="flex items-center gap-1.5">
          <span
            className="inline-block size-2.5 rounded-full"
            style={{ backgroundColor: it.color }}
            aria-hidden
          />
          {t(`wif.legend.${it.kind}`)}
        </span>
      ))}
    </div>
  )
}
