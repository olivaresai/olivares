// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PolicyAuthoringPanel — the reference authoring surface for a managed-* policy
// (ANT2-10/11/12/13). Guided typed form + raw-JSON CodeEditor (two-way synced) +
// LIVE LOCAL schema validation (real, no backend) + dry-run / publish / version
// against the DECLARED contract (honest pending seam, never a fake success).
//
// Posture (docs/SECURITY-HARDENING.md): publishing is a privileged, confirmed, audited action;
// the UI triggers distribution and shows the resulting drift verification — it
// NEVER writes host files, and never implies the system enforces on its own.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { FlaskConical, Send } from 'lucide-react'
import { useId, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { CodeDiff } from '@/components/ui/code-diff'
import { CodeEditor } from '@/components/ui/code-editor'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Spinner } from '@/components/ui/spinner'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from '@/components/ui/toaster'
import { SectionCard } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import type { ReactNode } from 'react'
import { claudePolicyApi, claudePolicyKeys, isContractPending } from './api'
import {
  ContractPendingNotice,
  DeclaredSection,
  ValidationPanel,
} from './components'
import {
  type KeyDescriptor,
  type PolicySurface,
  type SchemaIssue,
  makeJsonLintSource,
  parseJson,
  validateSurface,
} from './schema'
import { SchemaForm } from './schema-form'
import { DriftFindingList } from './drift'
import type {
  DryRunResult,
  PolicyDistributionView,
  PublishResult,
} from './types'

export interface PolicyAuthoringPanelProps {
  surface: PolicySurface
  /** Typed keys for the guided form (managed-settings/sandbox). Omit for surfaces
   *  whose document is not a flat key map (hooks/mcp use JSON + the reference). */
  formKeys?: readonly KeyDescriptor[]
  /** Strip this prefix from descriptor keys for the form's path math (e.g. sandbox.). */
  formBasePrefix?: string
  defaultDoc: string
  /** The verified-facts reference panel for this surface. */
  reference: ReactNode
  /** Extra notices (e.g. domain-fronting caveat, enforcement-off-by-default). */
  notices?: ReactNode
}

type SubTab = 'guided' | 'json' | 'versions'

export function PolicyAuthoringPanel({
  surface,
  formKeys,
  formBasePrefix,
  defaultDoc,
  reference,
  notices,
}: PolicyAuthoringPanelProps) {
  const report = useFailedActionReporter()
  const { t } = useTranslation(['claudePolicy', 'common'])
  const { can, activeTenant } = useAuth()
  const canPublish = can('governance:claude-policy:admin')
  const canDryRun = can('governance:claude-policy:read')
  const queryClient = useQueryClient()

  const hasGuided = !!formKeys && formKeys.length > 0
  const [doc, setDoc] = useState(defaultDoc)
  const [subTab, setSubTab] = useState<SubTab>(hasGuided ? 'guided' : 'json')
  const [dryRun, setDryRun] = useState<DryRunResult | null>(null)
  const [dryRunPending, setDryRunPending] = useState(false)
  const [published, setPublished] = useState<PublishResult | null>(null)
  const [publishPending, setPublishPending] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const lintHelpId = useId()

  const lintSource = useMemo(() => makeJsonLintSource(surface), [surface])
  const parsed = useMemo(() => parseJson(doc), [doc])
  const issues: SchemaIssue[] = useMemo(() => {
    if (parsed.error) return [parsed.error]
    if (parsed.value === undefined) return []
    return validateSurface(surface, parsed.value)
  }, [parsed, surface])
  const errorCount = issues.filter((i) => i.severity === 'error').length
  const formValue =
    parsed.value &&
    typeof parsed.value === 'object' &&
    !Array.isArray(parsed.value)
      ? (parsed.value as Record<string, unknown>)
      : undefined

  const dryRunMutation = useMutation({
    mutationFn: () => claudePolicyApi.dryRun(surface, doc),
    onSuccess: (res) => {
      setDryRun(res)
      setDryRunPending(false)
    },
    onError: (e) => {
      if (isContractPending(e)) {
        setDryRunPending(true)
        setDryRun(null)
        return
      }
      toast.error(
        t('common:errors.generic', { defaultValue: 'Something went wrong' }),
        {
          description: e instanceof Error ? e.message : undefined,
        },
      )
    },
  })

  const publishMutation = useMutation({
    mutationFn: () => claudePolicyApi.publish(surface, doc),
    onSuccess: async (res) => {
      setPublished(res)
      setPublishPending(false)
      setConfirmOpen(false)
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: claudePolicyKeys.drift(activeTenant),
        }),
        queryClient.invalidateQueries({
          queryKey: claudePolicyKeys.versions(activeTenant, surface),
        }),
        queryClient.invalidateQueries({
          queryKey: claudePolicyKeys.distribution(activeTenant, surface),
        }),
      ])
      toast.success(t('publish.done'))
    },
    onError: (e) => {
      if (isContractPending(e)) {
        setPublishPending(true)
        setConfirmOpen(false)
        return
      }
      // `isForbidden` es cierto para los DOS 403, así que cerrar el diálogo sigue cubriendo
      // ambas negativas —como antes— y el REPORTE se delega en la política que sí los
      // distingue. La rama genérica de abajo NO cierra el diálogo, y se queda igual.
      if (e instanceof ApiError && e.isForbidden) {
        setConfirmOpen(false)
        report(e)
        return
      }
      toast.error(
        t('common:errors.generic', { defaultValue: 'Something went wrong' }),
        {
          description: e instanceof Error ? e.message : undefined,
        },
      )
    },
  })

  return (
    <div className="grid gap-4 lg:grid-cols-[1fr_22rem]">
      <div className="flex min-w-0 flex-col gap-3">
        {notices}
        <Tabs value={subTab} onValueChange={(v) => setSubTab(v as SubTab)}>
          <TabsList>
            {hasGuided && (
              <TabsTrigger value="guided">{t('panel.guided')}</TabsTrigger>
            )}
            <TabsTrigger value="json">{t('panel.json')}</TabsTrigger>
            <TabsTrigger value="versions">{t('panel.versions')}</TabsTrigger>
            <TabsTrigger value="distribution">
              {t('panel.distribution')}
            </TabsTrigger>
          </TabsList>

          {hasGuided && (
            <TabsContent value="guided">
              {formValue || parsed.value === undefined ? (
                <div className="rounded-md border border-border bg-surface px-3">
                  <SchemaForm
                    keys={formKeys!}
                    value={formValue}
                    basePrefix={formBasePrefix}
                    disabled={!canDryRun}
                    onChange={(next) => setDoc(JSON.stringify(next, null, 2))}
                  />
                </div>
              ) : (
                <p className="rounded-md border border-dashed border-border px-3 py-4 text-xs text-muted-foreground">
                  {t('panel.fixJsonToUseForm')}
                </p>
              )}
            </TabsContent>
          )}

          <TabsContent value="json">
            <p id={lintHelpId} className="sr-only">
              {t('panel.editorHelp')}
            </p>
            <CodeEditor
              value={doc}
              onChange={setDoc}
              language="json"
              jsonLint
              lintSource={lintSource}
              ariaLabel={t('panel.editorLabel', { surface })}
              describedById={lintHelpId}
              invalid={errorCount > 0}
              readOnly={!canDryRun}
              height="24rem"
            />
          </TabsContent>

          <TabsContent value="versions">
            <VersionsTab surface={surface} />
          </TabsContent>

          <TabsContent value="distribution">
            <DistributionTab surface={surface} />
          </TabsContent>
        </Tabs>

        <div className="rounded-md border border-border bg-surface p-3">
          <ValidationPanel issues={issues} />
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {canDryRun && (
            <Button
              variant="secondary"
              size="sm"
              disabled={errorCount > 0 || dryRunMutation.isPending}
              onClick={() => {
                setDryRunPending(false)
                dryRunMutation.mutate()
              }}
            >
              {dryRunMutation.isPending ? (
                <Spinner size="sm" aria-hidden />
              ) : (
                <FlaskConical />
              )}
              {t('panel.dryRun')}
            </Button>
          )}
          {canPublish && (
            <Button
              variant="primary"
              size="sm"
              disabled={errorCount > 0 || publishMutation.isPending}
              onClick={() => setConfirmOpen(true)}
            >
              <Send />
              {t('panel.publish')}
            </Button>
          )}
          {errorCount > 0 && (
            <span className="text-xs text-danger">
              {t('panel.fixErrorsFirst')}
            </span>
          )}
        </div>

        {dryRunPending && (
          <ContractPendingNotice what={t('panel.dryRunWhat')} />
        )}
        {dryRun && <DryRunResultPanel result={dryRun} />}
        {publishPending && (
          <ContractPendingNotice what={t('panel.publishWhat')} />
        )}
        {published && <PublishResultPanel result={published} />}
      </div>

      <aside className="flex min-w-0 flex-col gap-3">{reference}</aside>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={(o) => {
          if (!publishMutation.isPending) setConfirmOpen(o)
        }}
        tone="danger"
        title={t('publish.confirmTitle')}
        description={t('publish.confirmBody')}
        confirmLabel={t('panel.publish')}
        pending={publishMutation.isPending}
        onConfirm={() => publishMutation.mutate()}
      >
        <p className="text-xs text-muted-foreground">
          {t('publish.confirmNote')}
        </p>
      </ConfirmDialog>
    </div>
  )
}

function DryRunResultPanel({ result }: { result: DryRunResult }) {
  const { t } = useTranslation('claudePolicy')
  return (
    <SectionCard title={t('dryRun.title')} description={t('dryRun.subtitle')}>
      {result.resolved && result.resolved.length > 0 && (
        <ol className="mb-3 flex flex-col gap-1">
          {result.resolved.map((r, i) => (
            <li key={i} className="flex items-center gap-2 text-xs">
              <Badge variant="outline">{r.scope}</Badge>
              <span className="text-muted-foreground">{r.note}</span>
            </li>
          ))}
        </ol>
      )}
      {result.changes && result.changes.length > 0 ? (
        <ul className="flex flex-col gap-1">
          {result.changes.map((c, i) => (
            <li key={i} className="font-mono text-xs">
              <span className="text-muted-foreground">{c.path}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-xs text-muted-foreground">{t('dryRun.noChanges')}</p>
      )}
      {result.notes?.map((n, i) => (
        <p key={i} className="mt-2 text-xs text-muted-foreground">
          {n}
        </p>
      ))}
    </SectionCard>
  )
}

/** Tone for the distribution outcome: "distributed" is claimed by the backend
 *  ONLY after the signed artifact record committed (deny-closed).*/
function distributionTone(
  d: string | undefined,
): 'success' | 'warning' | 'danger' {
  if (d === 'distributed') return 'success'
  if (d === 'enqueue-failed') return 'danger'
  return 'warning'
}

function PublishResultPanel({ result }: { result: PublishResult }) {
  const { t } = useTranslation('claudePolicy')
  const drift = result.drift ?? []
  return (
    <SectionCard
      title={t('publish.driftTitle')}
      description={t('publish.driftSubtitle')}
    >
      <div className="mb-2 flex flex-wrap items-center gap-2 text-xs">
        <Badge variant={distributionTone(result.distribution)}>
          {t(`publish.dist.${result.distribution ?? 'seam-pending'}`, {
            defaultValue: result.distribution ?? 'seam-pending',
          })}
        </Badge>
        {result.artifact && (
          <span className="font-mono text-muted-foreground">
            {t('publish.artifactKey')} {result.artifact.key_fingerprint} ·
            sha256 {result.artifact.artifact_sha256.slice(0, 16)}…
          </span>
        )}
      </div>
      {drift.length > 0 ? (
        <DriftFindingList findings={drift} />
      ) : result.drift_computed ? (
        <p className="text-xs text-success">{t('publish.noDrift')}</p>
      ) : (
        <p className="text-xs text-muted-foreground">
          {t('publish.driftUnknown')}
        </p>
      )}
      {result.notes?.map((n, i) => (
        <p key={i} className="mt-1.5 text-xs text-muted-foreground">
          {n}
        </p>
      ))}
    </SectionCard>
  )
}

/** truth view: published vs signed-for-distribution vs what every scope's
 *  attested check-in reports. Real state only — absences are named, never
 *  rendered as compliant. */
function DistributionTab({ surface }: { surface: PolicySurface }) {
  const { t } = useTranslation('claudePolicy')
  const { activeTenant } = useAuth()
  const query = useQuery({
    queryKey: claudePolicyKeys.distribution(activeTenant, surface),
    queryFn: () => claudePolicyApi.getDistribution(surface),
  })

  return (
    <DeclaredSection query={query} what={t('dist.what')}>
      {(view: PolicyDistributionView) => (
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Badge variant="outline">
              {t('dist.latestRevision', { n: view.latest_revision ?? 0 })}
            </Badge>
            {view.artifact ? (
              <span className="font-mono text-muted-foreground">
                {t('dist.signedArtifact', { n: view.artifact.revision })} ·{' '}
                {t('publish.artifactKey')} {view.artifact.key_fingerprint}
              </span>
            ) : (
              <Badge variant="warning">{t('dist.noArtifact')}</Badge>
            )}
          </div>
          {view.scopes.length > 0 && (
            <ol className="flex flex-col gap-1.5">
              {view.scopes.map((s) => (
                <li
                  key={s.scope}
                  className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
                >
                  <span className="font-mono">{s.scope}</span>
                  <Badge variant="outline">
                    r{s.reported_revision ?? '—'}
                  </Badge>
                  <Badge variant={s.current ? 'success' : 'warning'}>
                    {s.current ? t('dist.current') : t('dist.stale')}
                  </Badge>
                  <Badge variant={s.verified ? 'success' : 'danger'}>
                    {s.verified ? t('dist.verified') : t('dist.unverified')}
                  </Badge>
                  {!s.content_reported && (
                    <Badge variant="warning">{t('dist.contentUnknown')}</Badge>
                  )}
                  {s.open_findings > 0 && (
                    <Badge variant="danger">
                      {t('dist.openFindings', { count: s.open_findings })}
                    </Badge>
                  )}
                  <span className="ml-auto text-muted-foreground">
                    {s.checked_in_at ?? ''}
                  </span>
                </li>
              ))}
            </ol>
          )}
          {view.notes?.map((n, i) => (
            <p key={i} className="text-xs text-muted-foreground">
              {n}
            </p>
          ))}
        </div>
      )}
    </DeclaredSection>
  )
}

function VersionsTab({ surface }: { surface: PolicySurface }) {
  const { t } = useTranslation('claudePolicy')
  const { activeTenant } = useAuth()
  const [compare, setCompare] = useState<[number, number] | null>(null)
  const query = useQuery({
    queryKey: claudePolicyKeys.versions(activeTenant, surface),
    queryFn: () => claudePolicyApi.listVersions(surface),
  })

  return (
    <DeclaredSection query={query} what={t('panel.versionsWhat')}>
      {(data) => {
        const items = data.items ?? []
        if (items.length === 0) {
          return (
            <p className="text-xs text-muted-foreground">
              {t('versions.empty')}
            </p>
          )
        }
        const left = compare
          ? items.find((v) => v.revision === compare[0])
          : undefined
        const right = compare
          ? items.find((v) => v.revision === compare[1])
          : undefined
        return (
          <div className="flex flex-col gap-3">
            <ol className="flex flex-col gap-1.5">
              {items.map((v, i) => (
                <li
                  key={v.revision}
                  className="flex items-center gap-2 rounded-md border border-border bg-surface px-2.5 py-1.5 text-xs"
                >
                  <Badge variant="outline">r{v.revision}</Badge>
                  <span className="text-muted-foreground">
                    {v.author ?? '—'}
                  </span>
                  <span className="text-muted-foreground">
                    {v.created_at ?? ''}
                  </span>
                  {v.validated === false && (
                    <Badge variant="warning" className="ml-auto">
                      {t('versions.unvalidated')}
                    </Badge>
                  )}
                  {i + 1 < items.length && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-auto"
                      onClick={() =>
                        setCompare([items[i + 1]!.revision, v.revision])
                      }
                    >
                      {t('versions.compare')}
                    </Button>
                  )}
                </li>
              ))}
            </ol>
            {left?.content != null && right?.content != null && (
              <CodeDiff
                original={left.content}
                modified={right.content}
                language="json"
                originalLabel={t('versions.revisionLabel', {
                  n: left.revision,
                })}
                modifiedLabel={t('versions.revisionLabel', {
                  n: right.revision,
                })}
              />
            )}
          </div>
        )
      }}
    </DeclaredSection>
  )
}
