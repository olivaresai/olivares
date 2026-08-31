// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Posture-export console. Gives the read-only ground-truth posture export
// (modules/posture-export) a one-click surface for a GRC operator: pick the optional
// filters, export, and download the engine's verbatim minimal-data JSON for a control
// tower to ingest. Honest by construction — the backend has no format switch,
// destination push or history endpoint, so this UI invents none: it downloads the
// exact bytes, shows a summary of what was exported, keeps a THIS-SESSION list of the
// exports it performed, and points at the tamper-evident audit ledger (every export
// is recorded as `posture.export`) for the durable history.
//
// The three filters are the SCOPE of the exported document, so they live in the URL
//: the link that produced an export is the link that reproduces it, and a
// saved view can pin one. `last`/`history` stay out — they are the results of an
// export already performed, not inputs to the next one.
import { currentLanguage } from '@/lib/i18n'
import './i18n'
import { useMemo, useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Download, Share2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { CaveatNotice, SelfAuditNotice } from '@/features/_intel'
import { SavedViewsMenu } from '@/features/saved-views'
import { UrlStateNotice } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import {
  useValidatedUrlState,
  type UrlState,
  type UrlStateDecoded,
} from '@/lib/hooks/use-url-state'
import {
  downloadBlob,
  fetchPostureExport,
  postureExportFilename,
  type PostureExportDoc,
  type PostureExportParams,
  type SeverityFloor,
} from './api'

const ANY = '__any__'
const SEVERITIES: SeverityFloor[] = ['low', 'medium', 'high', 'critical']
const URL_KEYS = ['severity', 'category', 'kind'] as const
type TermKey = 'category' | 'kind'
const TERM_KEYS: TermKey[] = ['category', 'kind']
/** Kinds are short identifiers (`policy_drift`, `sessions.live`, `genai.semconv`);
 * the store clamps its own refs far below this. Generous, but finite. */
const MAX_TERM_LEN = 128
// eslint-disable-next-line no-control-regex -- the point is to reject them.
const CONTROL_CHARS = /[\u0000-\u001f\u007f]/

/** The scope of the exported document: the three filters, defaulted IN CODE so a
 * pristine view keeps a clean, shareable URL. `severity: ''` means "no floor". */
interface ExportScope {
  severity: SeverityFloor
  category: string
  kind: string
}

/**
 * Decode the owned search params. Pure and total — a malformed link is ordinary
 * input, not an exception — so every refusal falls back to the default above AND
 * is NAMED, for the view to say out loud.
 *
 * `severity` is a closed vocabulary: the four floors this Select offers are the
 * four the engine ranks (modules/posture-export/project.go:81); anything else is
 * a 400 there (postureexport.go:113). Passing an unknown floor through would fail
 * the export, and dropping it in silence would be worse — the recipient would
 * read a WIDER posture than the link claims while it looked identical.
 *
 * `category` and `kind` have no vocabulary to check against: the engine compares
 * them verbatim against whatever finding/subject/entity kinds a tenant's modules
 * registered (postureexport.go:117-118, project.go:105 and :168), so enumerating
 * them here would refuse legitimate values. What IS checkable is the shape every
 * kind has — bounded, and free of control characters — which is what a crafted
 * link carries and what this view's own input could never have produced.
 */
function decodeScope(raw: UrlState): UrlStateDecoded<ExportScope> {
  const issues: string[] = []
  const value: ExportScope = { severity: '', category: '', kind: '' }

  const severity = raw.severity
  if (severity !== undefined && severity !== '') {
    if ((SEVERITIES as string[]).includes(severity)) {
      value.severity = severity as SeverityFloor
    } else {
      issues.push('severity')
    }
  }

  for (const key of TERM_KEYS) {
    const term = raw[key]
    // Whitespace-only is indistinguishable from no filter (the request trims),
    // and it is a state the operator passes through while typing — not a refusal.
    if (term === undefined || term.trim() === '') continue
    if (term.length > MAX_TERM_LEN || CONTROL_CHARS.test(term)) {
      issues.push(key)
      continue
    }
    // Kept verbatim: the field echoes it back while the operator types, and
    // fetchPostureExport is what trims it for the request.
    value[key] = term
  }

  return { value, issues }
}

/** What a saved view was rejected for, pinned to the state its apply produced. */
interface AppliedSavedView {
  scope: ExportScope
  issues: string[]
}

/** Stable empty list: a fresh array would re-arm the notice's dismissal effect. */
const EMPTY_ISSUES: string[] = []

interface HistoryEntry {
  at: string
  filters: PostureExportParams
  inventory: number
  findings: number
  drift: number
  truncated: boolean
}

function summarize(doc: PostureExportDoc) {
  // The two real least-privilege drift signals. inventory_grant_count is DELIBERATELY
  // excluded: those are permitted grants on resource kinds with no observed-side
  // collector (unassessable baseline, kept out of the headline drift by the backend),
  // so folding them in would inflate "drift" for an otherwise-clean posture.
  const drift =
    doc.posture_drift.unexpected_count + doc.posture_drift.unused_grant_count
  const truncated =
    doc.inventory_truncated ||
    doc.findings_truncated ||
    doc.posture_drift.truncated
  return {
    inventory: doc.inventory.length,
    findings: doc.findings.length,
    drift,
    unexpected: doc.posture_drift.unexpected_count,
    truncated,
  }
}

export function PostureExportView() {
  const { t } = useTranslation(['postureExport', 'common'])
  const { can } = useAuth()
  const canRead = can('posture:export:read')

  const [{ severity, category, kind }, patchScope, urlIssues] =
    useValidatedUrlState(URL_KEYS, decodeScope)
  const [applied, setApplied] = useState<AppliedSavedView | null>(null)
  const [last, setLast] = useState<PostureExportDoc | null>(null)
  const [history, setHistory] = useState<HistoryEntry[]>([])

  // Derived, never synchronized: the complaint stands only while the state is
  // still the one that apply produced. Editing a filter, or a Back that leaves
  // it, retires the notice without anyone having to remember to clear it.
  const savedViewIssues =
    applied &&
    applied.scope.severity === severity &&
    applied.scope.category === category &&
    applied.scope.kind === kind
      ? applied.issues
      : EMPTY_ISSUES

  /** A stored view is server data, and it outlives the vocabulary it was saved
   * against — so it goes through the SAME decoder the URL does before any of it
   * can reach a request. */
  function applySavedView(params: Record<string, string>) {
    const { value, issues } = decodeScope(params)
    setApplied(issues.length > 0 ? { scope: value, issues } : null)
    patchScope({
      severity: value.severity || undefined,
      category: value.category || undefined,
      kind: value.kind || undefined,
    })
  }

  const savedViewParams = useMemo(
    () => ({
      severity: severity || undefined,
      // Store what the request will actually send (fetchPostureExport trims).
      category: category.trim() || undefined,
      kind: kind.trim() || undefined,
    }),
    [severity, category, kind],
  )

  const filters = (): PostureExportParams => ({ severity, category, kind })

  const mut = useMutation({
    mutationFn: () => fetchPostureExport(filters()),
    onSuccess: ({ doc, blob }) => {
      downloadBlob(blob, postureExportFilename())
      setLast(doc)
      const s = summarize(doc)
      setHistory((h) =>
        [
          {
            at: new Date().toISOString(),
            filters: filters(),
            inventory: s.inventory,
            findings: s.findings,
            drift: s.drift,
            truncated: s.truncated,
          },
          ...h,
        ].slice(0, 20),
      )
      toast.success(t('export.done'))
    },
    onError: (err) => {
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(t('export.failed'), description ? { description } : undefined)
    },
  })

  if (!canRead) return <ForbiddenState />

  return (
    <div className="flex flex-col gap-6 p-6">
      <PageHeader
        icon={Share2}
        title={t('title')}
        description={t('description')}
        actions={
          <SavedViewsMenu
            featureId="posture-export"
            params={savedViewParams}
            onApply={applySavedView}
          />
        }
      />

      <Card>
        <CardHeader>
          <CardTitle>{t('filters.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {/* Directly above the filters it is about. The two origins are mutually
              exclusive by construction: applying a view rewrites every owned key,
              so whatever the URL carried is gone by the time a stored value is
              the thing that was rejected. */}
          {savedViewIssues.length > 0 ? (
            <UrlStateNotice issues={savedViewIssues} origin="saved-view" />
          ) : (
            <UrlStateNotice issues={urlIssues} />
          )}
          <div className="grid gap-3 sm:grid-cols-3">
            <Field
              label={t('filters.severity')}
              description={t('filters.severityHint')}
            >
              {({ id }) => (
                <Select
                  value={severity === '' ? ANY : severity}
                  onValueChange={(v) =>
                    patchScope({ severity: v === ANY ? undefined : v })
                  }
                >
                  <SelectTrigger id={id} aria-label={t('filters.severity')}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ANY}>{t('filters.severityAny')}</SelectItem>
                    {SEVERITIES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {t(`severity.${s}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <Field
              label={t('filters.category')}
              description={t('filters.categoryHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={category}
                  // Bounded HERE too, not only in the decoder: without it a
                  // 129th character is written to state and the URL, then
                  // refused on read — so the field the operator is typing in
                  // empties itself while the address bar still carries the long
                  // value, and the notice blames "this link" for something they
                  // just typed.
                  maxLength={MAX_TERM_LEN}
                  onChange={(e) => patchScope({ category: e.target.value })}
                  placeholder={t('filters.categoryPlaceholder')}
                  autoComplete="off"
                />
              )}
            </Field>
            <Field label={t('filters.kind')} description={t('filters.kindHint')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={kind}
                  maxLength={MAX_TERM_LEN}
                  onChange={(e) => patchScope({ kind: e.target.value })}
                  placeholder={t('filters.kindPlaceholder')}
                  autoComplete="off"
                />
              )}
            </Field>
          </div>
          <CaveatNotice tone="info">{t('notes.pull')}</CaveatNotice>
          <SelfAuditNotice />
          <div>
            <Button
              variant="primary"
              onClick={() => mut.mutate()}
              disabled={mut.isPending}
            >
              {mut.isPending ? (
                <Spinner size="sm" aria-hidden />
              ) : (
                <Download className="size-4" aria-hidden />
              )}
              {t('export.action')}
            </Button>
          </div>
        </CardContent>
      </Card>

      {last ? <SummaryCard doc={last} /> : null}

      <Card>
        <CardHeader>
          <CardTitle>{t('history.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <CaveatNotice tone="neutral">
            {t('history.note')}{' '}
            <Link to={'/audit' as never} className="underline">
              {t('history.auditLink')}
            </Link>
            .
          </CaveatNotice>
          {history.length === 0 ? (
            <EmptyState title={t('history.empty')} />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                    <th className="py-2 pr-4 font-medium">{t('history.colTime')}</th>
                    <th className="py-2 pr-4 font-medium">{t('history.colFilters')}</th>
                    <th className="py-2 pr-4 text-right font-medium">
                      {t('history.colCounts')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {history.map((h, i) => (
                    <tr key={`${h.at}-${i}`} className="border-b last:border-0">
                      <td className="py-2 pr-4 font-mono text-xs">
                        {new Date(h.at).toLocaleTimeString(currentLanguage())}
                      </td>
                      <td className="py-2 pr-4 text-xs text-muted-foreground">
                        {describeFilters(h.filters, t)}
                      </td>
                      <td className="py-2 pr-4 text-right text-xs">
                        {t('history.counts', {
                          inventory: h.inventory,
                          findings: h.findings,
                          drift: h.drift,
                        })}
                        {h.truncated ? (
                          <Badge variant="warning" className="ml-2">
                            {t('summary.truncated')}
                          </Badge>
                        ) : null}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function describeFilters(
  f: PostureExportParams,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  const parts: string[] = []
  if (f.severity) parts.push(t('summary.severityFloor', { severity: f.severity }))
  if (f.category?.trim()) parts.push(`category=${f.category.trim()}`)
  if (f.kind?.trim()) parts.push(`kind=${f.kind.trim()}`)
  return parts.length ? parts.join(', ') : t('filters.none')
}

function SummaryCard({ doc }: { doc: PostureExportDoc }) {
  const { t } = useTranslation(['postureExport', 'common'])
  const s = summarize(doc)
  const stats: { label: string; value: number }[] = [
    { label: t('summary.inventory'), value: s.inventory },
    { label: t('summary.findings'), value: s.findings },
    { label: t('summary.unexpected'), value: s.unexpected },
    { label: t('summary.drift'), value: s.drift },
  ]
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('summary.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {stats.map((st) => (
            <div key={st.label} className="rounded-md border bg-muted/30 p-3">
              <div className="text-2xl font-semibold tabular-nums text-foreground">
                {st.value}
              </div>
              <div className="text-xs text-muted-foreground">{st.label}</div>
            </div>
          ))}
        </div>
        {s.truncated ? (
          <CaveatNotice tone="warning">{t('summary.truncatedHint')}</CaveatNotice>
        ) : null}
        <p className="text-xs leading-relaxed text-muted-foreground">{doc.note}</p>
      </CardContent>
    </Card>
  )
}

export default PostureExportView
