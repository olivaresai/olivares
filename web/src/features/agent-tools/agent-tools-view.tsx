// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  FIRST_HOUR_TOOLS,
  firstHourKeys,
  type ToolKey,
} from '@/features/first-hour/api'
import { useTenantStore } from '@/stores/tenant'
import { ToolCard, useToolStatus } from '@/features/first-hour/first-hour'
import { OllamaService } from './ollama-service'
import '@/features/first-hour/i18n'
import { ForbiddenState } from '@/components/ui/error-state'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useProviderBoundary } from '@/features/providers/auth-boundary'
import {
  agentToolsApi,
  type InstallRequest,
  type ToolJob,
  type ToolPlan,
  type ToolDetection,
} from './api'
import './i18n'
const NAMES: Record<string, string> = {
  claude: 'Claude Code',
  codex: 'Codex',
  grok: 'Grok Build',
  opencode: 'OpenCode',
  ollama: 'Ollama',
}
const message = (err: unknown) =>
  err instanceof Error ? err.message : undefined
export function AgentToolsView() {
  const boundary = useProviderBoundary()
  return <Tools key={boundary.key} epoch={boundary.epoch} />
}
function Tools({ epoch }: { epoch: number }) {
  const { t } = useTranslation('agentTools')
  const { isSuperadmin } = useAuth()
  const qc = useQueryClient()
  const tenant = useTenantStore((s) => s.activeTenant)
  const scope = ['agent-tools', epoch]
  const inventoryKey = [...scope, 'inventory']
  const inventory = useQuery({
    queryKey: inventoryKey,
    queryFn: ({ signal }) => agentToolsApi.inventory(signal),
    enabled: isSuperadmin,
  })
  const [approval, setApproval] = useState<{
    plan: ToolPlan
    request: InstallRequest
  } | null>(null)
  const [startedJobID, setJobID] = useState<string | null>(null)
  const preview = useMutation({
    mutationFn: ({ driver, version }: { driver: string; version: string }) =>
      agentToolsApi.plan(driver, version),
    onSuccess: (plan) =>
      setApproval({
        plan,
        request: { plan_digest: plan.digest, request_id: crypto.randomUUID() },
      }),
  })
  const install = usePrivilegedMutation<InstallRequest, ToolJob>({
    mutationFn: (body, authority) => agentToolsApi.install(body, authority),
    stepUpAction: 'console',
    successMessage: t('started'),
    onDone: (job) => {
      setJobID(job.id)
      setApproval(null)
    },
  })
  // The job this page started, else the latest one the engine reports: derived, not
  // copied into state by an effect (react-hooks/set-state-in-effect).
  const jobID = startedJobID ?? inventory.data?.jobs?.[0]?.id ?? null
  const job = useQuery({
    queryKey: ['agent-tools', epoch, 'job', jobID],
    queryFn: ({ signal }) => agentToolsApi.job(jobID!, signal),
    enabled: isSuperadmin && !!jobID,
    refetchInterval: (query) =>
      query.state.data?.state === 'running' ? 1000 : false,
  })
  useEffect(() => {
    if (job.data && job.data.state !== 'running') {
      void qc.invalidateQueries({ queryKey: inventoryKey })
      // The tool cards above read the tool's own status and what it runs on: a finished
      // install changes both (HU2-30: "Not installed" stayed next to "Installed" until a
      // reload).
      void qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) })
    }
  }, [job.data?.state, qc, epoch, tenant])
  useEffect(
    () => () => {
      void qc.cancelQueries({ queryKey: scope })
      qc.removeQueries({ queryKey: scope })
    },
    [epoch, qc],
  )
  if (!isSuperadmin) return <ForbiddenState description={t('adminOnly')} />
  const busy = install.isPending || job.data?.state === 'running'
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <Button
            variant="outline"
            onClick={() => void inventory.refetch()}
            disabled={inventory.isFetching}
          >
            {t('refresh')}
          </Button>
        }
      />
      {/* ONE LIST, ONE ROW PER TOOL. Claude Code and Codex install in one action and
          sign in with their own login (the first-hour card) and show the version
          installed; the other tools keep their install row. API keys are the other tab;
          each tool card links to it ("Use an API key instead"), so the page does not. */}
      {inventory.isPending ? (
        <Spinner />
      ) : inventory.isError ? (
        <QueryErrorState
          error={inventory.error}
          subject={t('title')}
          description={message(inventory.error)}
          retry={() => void inventory.refetch()}
        />
      ) : (
        <>
          {inventory.data.read_only && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('readOnly')}
            </p>
          )}
          <ul className="divide-y divide-border rounded-md border border-border">
            {inventory.data.drivers.map((driver) => (
              // Every tool keeps its install row; Claude Code and Codex add their sign-in
              // in it (ToolRow). A merge brought back a card-only branch for those two.
              <ToolRow
                key={driver}
                driver={driver}
                verification={inventory.data.verification_levels?.[driver]}
                installs={inventory.data.inventory.installed.filter(
                  (row) => row.driver === driver,
                )}
                disabled={
                  !!busy || inventory.data.read_only || preview.isPending
                }
                onPreview={(version) => {
                  setApproval(null)
                  install.reset()
                  preview.mutate({ driver, version })
                }}
              />
            ))}
          </ul>
          {inventory.data.inventory.leftovers.length > 0 && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('leftovers', {
                count: inventory.data.inventory.leftovers.length,
              })}
            </p>
          )}
        </>
      )}
      {preview.isPending && <p role="status">{t('planning')}</p>}
      {preview.isError && (
        <QueryErrorState
          error={preview.error}
          description={message(preview.error)}
        />
      )}
      {approval && (
        <section
          aria-labelledby="tool-approval"
          className="flex min-w-0 flex-col gap-3 rounded-md border border-border p-4"
        >
          <h2 id="tool-approval" className="text-heading">
            {t('approval')}
          </h2>
          <p>
            {NAMES[approval.plan.driver] ?? approval.plan.driver} ·{' '}
            <strong>{approval.plan.version}</strong>
          </p>
          <p className="text-sm text-muted-foreground">
            {t(`verification.${approval.plan.verification}`, {
              defaultValue: approval.plan.verification,
            })}
          </p>
          <p className="break-all font-mono text-xs text-muted-foreground">
            {approval.plan.executable}
          </p>
          <p className="text-sm text-muted-foreground">{t('installHint')}</p>
          {install.isError && (
            <QueryErrorState
              error={install.error}
              description={message(install.error)}
            />
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              onClick={() => install.mutate(approval.request)}
              disabled={!!busy}
            >
              {install.isPending ? t('starting') : t('install')}
            </Button>
            <Button
              variant="ghost"
              disabled={install.isPending}
              onClick={() => setApproval(null)}
            >
              {t('cancel')}
            </Button>
          </div>
        </section>
      )}
      {job.isError && (
        <QueryErrorState
          error={job.error}
          description={message(job.error)}
          retry={() => void job.refetch()}
        />
      )}
      {job.data && (
        <section
          aria-labelledby="tool-job"
          className="flex min-w-0 flex-col gap-2 rounded-md border border-border p-4"
        >
          <h2 id="tool-job" className="text-heading">
            {t('job')}
          </h2>
          <p aria-live="polite" role="status">
            {t(`state.${job.data.state}`)} ·{' '}
            {NAMES[job.data.driver] ?? job.data.driver} {job.data.version}
          </p>
          {job.data.error && (
            <p role="alert" className="break-words text-sm text-destructive">
              {job.data.error}
            </p>
          )}
          {job.data.audit_error && (
            <p role="alert" className="text-sm text-destructive">
              {job.data.audit_error}
            </p>
          )}
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-all rounded bg-muted p-3 font-mono text-xs">
            {job.data.progress}
          </pre>
        </section>
      )}
    </div>
  )
}

/** The managed-install line of Claude Code or Codex when Olivares installed no release:
 * the tool's own status says whether it is on this server anyway. EU on RC10: the card
 * said "Installed" and, below it, "No managed installation". */
function UnmanagedLabel({ driver }: { driver: ToolKey }) {
  const { t } = useTranslation('agentTools')
  const status = useToolStatus(driver)
  return (
    <span className="text-sm text-muted-foreground">
      {status.data?.installed ? t('installedOutside') : t('notInstalled')}
    </span>
  )
}

function ToolRow({
  driver,
  verification,
  installs,
  disabled,
  onPreview,
}: {
  driver: string
  verification?: string
  installs: { version: string; state: string; reason?: string }[]
  disabled: boolean
  onPreview: (version: string) => void
}) {
  const { t } = useTranslation('agentTools')
  const [version, setVersion] = useState(
    driver === 'grok' ? 'stable' : 'latest',
  )
  const detect = useMutation({ mutationFn: () => agentToolsApi.detect(driver) })
  const probe = usePrivilegedMutation<string, ToolDetection>({
    mutationFn: async (path, authority) => {
      const result = await agentToolsApi.detect(
        driver,
        undefined,
        path,
        authority,
      )
      if (result.probe_error) {
        const failed = result.candidates.find((row) => row.probe_error)
        const selected = result.candidates.find((row) => row.path === path)
        throw new Error(
          failed?.probe_error ?? selected?.probe_skipped ?? result.probe_error,
        )
      }
      return result
    },
    stepUpAction: 'console',
    successMessage: t('probeComplete'),
  })
  const detection = probe.data ?? detect.data

  const name = NAMES[driver] ?? driver
  // Claude Code and Codex: one-click install of the latest verified release and sign-in
  // with the tool's own login, in the same row as the versions and the advanced install.
  const firstHour = (FIRST_HOUR_TOOLS as readonly string[]).includes(driver)
  return (
    <li className="flex min-w-0 flex-col gap-3 p-4">
      {firstHour ? <ToolCard driver={driver as ToolKey} /> : null}
      {driver === 'grok' &&
      installs.some((row) => row.state === 'installed') ? (
        <ToolCard driver="grok" part="signIn" />
      ) : null}
      {driver === 'ollama' &&
      installs.some((row) => row.state === 'installed') ? (
        <OllamaService />
      ) : null}
      <div className="flex flex-wrap items-baseline gap-2">
        <h2 className="text-heading">{name}</h2>
        {installs.length === 0 ? (
          firstHour ? (
            <UnmanagedLabel driver={driver as ToolKey} />
          ) : (
            <span className="text-sm text-muted-foreground">
              {t('notInstalled')}
            </span>
          )
        ) : (
          installs.map((row) => (
            <Badge
              key={row.version}
              variant={row.state === 'installed' ? 'neutral' : 'outline'}
            >
              {row.version} · {t(`installedState.${row.state}`)}
            </Badge>
          ))
        )}
      </div>
      {installs.map(
        (row) =>
          row.reason && (
            <p key={row.version} className="text-sm text-muted-foreground">
              {row.reason}
            </p>
          ),
      )}
      {verification && (
        <p className="text-sm text-muted-foreground">
          {t(`verification.${verification}`)}
        </p>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault()
          if (version.trim()) onPreview(version.trim())
        }}
        className="flex flex-col gap-2 sm:flex-row sm:items-end"
      >
        <div className="flex min-w-0 flex-col gap-1 sm:w-52">
          <label htmlFor={`version-${driver}`} className="text-sm">
            {t('version', { name })}
          </label>
          <Input
            id={`version-${driver}`}
            value={version}
            onChange={(e) => setVersion(e.target.value)}
            disabled={disabled}
            maxLength={128}
          />
        </div>
        <Button
          type="submit"
          disabled={disabled || !version.trim()}
          aria-label={t('reviewName', { name })}
        >
          {t('review')}
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => {
            probe.reset()
            detect.mutate()
          }}
          disabled={detect.isPending || probe.isPending}
        >
          {t('detect', { name })}
        </Button>
      </form>
      {detect.isPending && (
        <p role="status" className="text-sm">
          {t('detecting')}
        </p>
      )}
      {detect.isError && (
        <p role="alert" className="text-sm text-destructive">
          {message(detect.error)}
        </p>
      )}
      {probe.isError && (
        <p role="alert" className="text-sm text-destructive">
          {message(probe.error)}
        </p>
      )}
      {detection && (
        <div className="text-sm">
          {detection.probe_error && (
            <p role="alert" className="text-destructive">
              {detection.probe_error}
            </p>
          )}
          {detection.candidates.length === 0
            ? !detection.probe_error && <p>{t('notDetected')}</p>
            : detection.candidates.map((row) => (
                <div
                  key={row.path}
                  className="flex flex-col gap-2 border-t border-border py-2"
                >
                  <p className="break-all font-mono text-xs">
                    {row.path} · {row.version ?? t('versionUnknown')}
                  </p>
                  {row.probe_error && <p role="alert">{row.probe_error}</p>}
                  {row.probe_skipped && (
                    <p className="text-xs text-muted-foreground">
                      {row.probe_skipped}
                    </p>
                  )}
                  {!row.version &&
                    row.executable &&
                    row.match === 'unregistered-observed' && (
                      <>
                        <p className="text-xs text-muted-foreground">
                          {t('probeHint')}
                        </p>
                        <Button
                          variant="outline"
                          aria-label={t('probeName', { path: row.path })}
                          disabled={disabled || probe.isPending}
                          onClick={() => probe.mutate(row.path)}
                        >
                          {t('probe')}
                        </Button>
                      </>
                    )}
                </div>
              ))}
        </div>
      )}
    </li>
  )
}
