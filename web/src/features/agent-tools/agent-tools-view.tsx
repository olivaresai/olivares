// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useIsFetching,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import { ChevronDown } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { firstHourKeys } from '@/features/first-hour/api'
import { useTenantStore } from '@/stores/tenant'
import { useReadyTools } from '@/features/first-hour/first-hour'
import { OllamaService } from './ollama-service'
import '@/features/first-hour/i18n'
import { ForbiddenState } from '@/components/ui/error-state'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useProviderBoundary } from '@/features/providers/auth-boundary'
import { agentOpsKeys } from '@/features/agentops/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import {
  agentToolsApi,
  agentToolsKeys,
  type InstallRequest,
  type ProviderSnapshot,
  type ToolJob,
  type ToolPlan,
} from './api'
import { AddProfileDialog } from './add-profile-dialog'
import { defaultVersion, InstallDetails } from './install-details'
import { ProfileRowView, useAccountRow, useDefaultRow } from './profile-row'
import { ProfileSheet } from './profile-sheet'
import { ProfileSignInDialog } from './profile-sign-in'
import { ToolBox } from './tool-box'
import { needsDefaultRow, useProfileList } from './use-profiles'
import { toolFacts, toolLabel as nameOf } from './inventory'
import './i18n'

/** A service the engine runs for sessions, not a tool with logins: it has no profiles. */
const SERVICE_DRIVERS: readonly string[] = ['ollama']
const message = (err: unknown) =>
  err instanceof Error ? err.message : undefined

export function AgentToolsView() {
  const boundary = useProviderBoundary()
  return <Tools key={boundary.key} epoch={boundary.epoch} />
}

function Tools({ epoch }: { epoch: number }) {
  const { t } = useTranslation('agentTools')
  const { isSuperadmin, activeTenant } = useAuth()
  const qc = useQueryClient()
  const tenant = useTenantStore((s) => s.activeTenant)
  const scope = [...agentToolsKeys.all, epoch]
  const inventoryKey = [...scope, 'inventory']
  const inventory = useQuery({
    queryKey: inventoryKey,
    queryFn: ({ signal }) => agentToolsApi.inventory(signal),
    enabled: isSuperadmin,
  })
  // Native provider probes share the host CPU with status and readiness. Let
  // the essential reads finish before starting these optional details.
  const readyTools = useReadyTools(isSuperadmin && inventory.isSuccess)
  const profiles = useProfileList(isSuperadmin)
  const criticalQueries = { queryKey: firstHourKeys.all(tenant) }
  const profileQueries = {
    queryKey: agentOpsKeys.boundaryScope(activeTenant, epoch),
    predicate: (query: { queryKey: readonly unknown[] }) =>
      query.queryKey.includes('account-sign-in'),
  }
  const criticalReads = useIsFetching(criticalQueries)
  const profileReads = useIsFetching(profileQueries)
  // A completed list can mount a new native query on the next render. Keep
  // those profiles pending until their shared status query has answered.
  const nativeProfilesReady =
    !activeTenant ||
    (profiles.data?.accounts ?? [])
      .filter(
        (account) =>
          account.auth_source !== 'managed_injection' &&
          !SERVICE_DRIVERS.includes(account.driver) &&
          inventory.data?.inventory.installed.some(
            (row) => row.driver === account.driver && row.state === 'installed',
          ),
      )
      .every((account) => {
        const status = qc.getQueryState([
          ...agentOpsKeys.boundaryScope(activeTenant, epoch),
          'account-sign-in',
          account.account_ref,
        ])?.status
        return status === 'success' || status === 'error'
      })
  const providers = useQuery({
    queryKey: [...scope, 'providers', tenant],
    queryFn: ({ signal }) => agentToolsApi.providers(tenant, signal, true),
    enabled: (query) =>
      isSuperadmin &&
      // Once answered, stay enabled: the snapshots can add tool rows with new
      // status reads, and those must not trigger another provider fetch.
      (query.state.status !== 'pending' ||
        inventory.isError ||
        (inventory.isSuccess &&
          !readyTools.isLoading &&
          !profiles.isLoading &&
          criticalReads === 0 &&
          profileReads === 0 &&
          nativeProfilesReady)),
    refetchInterval: (query) => (query.state.data?.refreshing ? 1000 : false),
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
      // The profile rows read the tool's own status and what it runs on: a finished
      // install changes both (HU2-30: "Not installed" stayed next to "Installed" until a
      // reload).
      void qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) })
      void qc.invalidateQueries({ queryKey: [...scope, 'providers', tenant] })
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
  const installReason = install.isPending ? t('starting') : t('state.running')
  // Without the inventory nothing can be reviewed or installed: said, not hidden.
  const inventoryAvailable = !!inventory.data && !inventory.isError
  const reviewDisabledReason = !inventoryAvailable
    ? t('providers.state.error')
    : inventory.data?.read_only
      ? t('readOnly')
      : busy
        ? installReason
        : preview.isPending
          ? t('planning')
          : undefined
  const review = (driver: string, version: string) => {
    setApproval(null)
    install.reset()
    preview.mutate({ driver, version })
  }
  const snaps = providers.data?.providers ?? []
  const facts = toolFacts(inventory.data, snaps)
  // A cold background read omits unchecked tools: absence is not an answer yet.
  const missing = facts.filter(
    (f) =>
      !f.installed &&
      (f.snapshots.length > 0 ||
        (providers.isSuccess && !providers.data.refreshing)),
  )
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          <Button
            variant="outline"
            onClick={() => {
              void inventory.refetch()
              void providers.refetch()
              // The profile rows read each login from the tool itself.
              void qc.invalidateQueries({ queryKey: firstHourKeys.all(tenant) })
            }}
            disabled={inventory.isFetching}
          >
            {t('refresh')}
          </Button>
        }
      />
      {/* ONE LIST PER INSTALLED TOOL, ONE ROW PER PROFILE. A profile is a way a tool signs
          in (its own account, or an API key); the install, its version form, Detect and
          its log are behind the tool's menu. A tool that is not installed is one line. */}
      {inventory.isPending ? (
        <Spinner />
      ) : (
        <>
          {inventory.isError && (
            <QueryErrorState
              error={inventory.error}
              subject={t('title')}
              description={message(inventory.error)}
              retry={() => void inventory.refetch()}
            />
          )}
          {inventory.data?.read_only && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('readOnly')}
            </p>
          )}
          {providers.isError && (
            <QueryErrorState
              error={providers.error}
              subject={t('title')}
              description={message(providers.error)}
              retry={() => void providers.refetch()}
            />
          )}
          <ProfilesNotice profiles={profiles} />
          {facts
            .filter((f) => f.installed)
            .map((f) => (
              <InstalledTool
                key={f.driver}
                driver={f.driver}
                accounts={(profiles.data?.accounts ?? []).filter(
                  (a) => a.driver === f.driver,
                )}
                taken={profiles.data?.taken}
                named={profiles.data?.named ?? false}
                version={f.version}
                installs={f.installs}
                snapshots={f.snapshots}
                verification={inventory.data?.verification_levels?.[f.driver]}
                disabledReason={reviewDisabledReason}
                onReview={review}
                job={job.data?.driver === f.driver ? job.data : undefined}
              />
            ))}
          {missing.length > 0 && (
            <div
              data-testid="not-installed"
              className="flex flex-wrap items-center justify-between gap-3 px-1"
            >
              <p className="text-caption text-text-2">
                {t('missing', {
                  tools: missing.map((f) => nameOf(f.driver)).join(' · '),
                })}
              </p>
              {inventoryAvailable ? (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="secondary" size="sm">
                      {t('installMenu')}
                      <ChevronDown aria-hidden />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    {missing.map((f) => (
                      <DropdownMenuItem
                        key={f.driver}
                        disabled={!!reviewDisabledReason}
                        onSelect={() =>
                          review(f.driver, defaultVersion(f.driver))
                        }
                      >
                        {t('installTool', { name: nameOf(f.driver) })}
                      </DropdownMenuItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
              ) : null}
            </div>
          )}
          {!!inventory.data?.inventory.leftovers.length && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('leftovers', {
                count: inventory.data?.inventory.leftovers.length ?? 0,
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
          // HU2-28: a release lookup can fail for a while (GitHub's limit for this network);
          // Retry plans the same tool and version again.
          retry={() => {
            if (preview.variables) preview.mutate(preview.variables)
          }}
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
            {nameOf(approval.plan.driver)} ·{' '}
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
          className="flex min-w-0 flex-col gap-1"
        >
          <h2 id="tool-job" className="sr-only">
            {t('job')}
          </h2>
          {/* One line: the log of the install is in the tool's details sheet. */}
          <p
            aria-live="polite"
            role="status"
            className="text-caption text-text-2"
          >
            {t(`state.${job.data.state}`)} · {nameOf(job.data.driver)}{' '}
            {job.data.version}
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
        </section>
      )}
    </div>
  )
}

/** The profiles are read once for every tool: when that read failed, or stopped at its
 * bound, one quiet line says so (the rows below are then not the whole story). */
function ProfilesNotice({
  profiles,
}: {
  profiles: ReturnType<typeof useProfileList>
}) {
  const { t } = useTranslation('agentTools')
  if (!profiles.isError && !profiles.data?.truncated) return null
  return (
    <p
      role="status"
      className="flex flex-wrap items-center gap-x-2 text-caption text-text-2"
    >
      {profiles.isError ? t('profiles.readFailed') : t('profiles.truncated')}
      {profiles.isError ? (
        <Button variant="link" onClick={() => void profiles.refetch()}>
          {t('profiles.retry')}
        </Button>
      ) : null}
    </p>
  )
}

/** One installed tool: its boxed list. A service (Ollama) has no profiles, only its panel. */
function InstalledTool({
  driver,
  accounts,
  taken,
  named,
  version,
  installs,
  snapshots,
  verification,
  disabledReason,
  onReview,
  job,
}: {
  driver: string
  accounts: ProviderAccountDTO[]
  taken?: ReadonlySet<string>
  named: boolean
  version?: string
  installs: { version: string; state: string; reason?: string }[]
  snapshots: ProviderSnapshot[]
  verification?: string
  disabledReason?: string
  onReview: (driver: string, version: string) => void
  job?: ToolJob
}) {
  const { t } = useTranslation('agentTools')
  const name = nameOf(driver)
  const [details, setDetails] = useState(false)
  const [adding, setAdding] = useState(false)
  const { can } = useAuth()
  const service = SERVICE_DRIVERS.includes(driver)
  return (
    <>
      <ToolBox
        driver={driver}
        name={name}
        version={version}
        onUpdate={() => onReview(driver, defaultVersion(driver))}
        updateDisabled={!!disabledReason}
        onDetails={() => setDetails(true)}
        profilesHref={service ? undefined : '/provider-accounts'}
        onAdd={
          service || !can('sessions:account:write')
            ? undefined
            : () => setAdding(true)
        }
      >
        {snapshots.some((snap) => snap.stale || snap.error) ? (
          <li role="status" className="px-4 py-2 text-caption text-warning">
            {t(
              snapshots.some((snap) => snap.stale)
                ? 'providers.staleBadge'
                : 'providers.state.error',
            )}
          </li>
        ) : null}
        {service ? (
          <li className="p-4">
            <OllamaService />
          </li>
        ) : (
          <ProfileRows
            driver={driver}
            name={name}
            accounts={accounts}
            named={named}
          />
        )}
      </ToolBox>
      {adding && (
        <AddProfileDialog
          driver={driver}
          toolName={name}
          taken={taken ?? new Set()}
          onOpenChange={setAdding}
        />
      )}
      <InstallDetails
        driver={driver}
        name={name}
        open={details}
        onOpenChange={setDetails}
        installs={installs}
        verification={verification}
        disabledReason={disabledReason}
        onPreview={(v) => onReview(driver, v)}
        installedOutside={installs.length === 0}
        logins={snapshots}
        job={job}
      />
    </>
  )
}

/** The default login (the profile without an account name), then every named profile. */
function ProfileRows({
  driver,
  name,
  accounts,
  named,
}: {
  driver: string
  name: string
  accounts: ProviderAccountDTO[]
  named: boolean
}) {
  // An engine without names adopted the default login as a named profile: it is then
  // listed once, as that profile.
  return (
    <>
      {needsDefaultRow(accounts, named) ? (
        <DefaultRow driver={driver} toolName={name} />
      ) : null}
      {accounts.map((account) => (
        <AccountRow
          key={account.account_ref}
          account={account}
          toolName={name}
        />
      ))}
    </>
  )
}

function DefaultRow({
  driver,
  toolName,
}: {
  driver: string
  toolName: string
}) {
  const { model, noSignIn } = useDefaultRow(driver)
  const activeTenant = useTenantStore((s) => s.activeTenant)
  const [sheet, setSheet] = useState(false)
  const [signIn, setSignIn] = useState(false)
  if (noSignIn) return null
  return (
    <>
      <ProfileRowView
        model={model}
        onOpen={() => setSheet(true)}
        onSignIn={() => setSignIn(true)}
      />
      {sheet && (
        <ProfileSheet
          toolName={toolName}
          model={model}
          onOpenChange={setSheet}
          onSignIn={() => setSignIn(true)}
        />
      )}
      {signIn && (
        <ProfileSignInDialog
          driver={driver}
          name={toolName}
          tenant={activeTenant}
          onOpenChange={setSignIn}
        />
      )}
    </>
  )
}

function AccountRow({
  account,
  toolName,
}: {
  account: ProviderAccountDTO
  toolName: string
}) {
  const model = useAccountRow(account)
  const { activeTenant } = useAuth()
  const [sheet, setSheet] = useState(false)
  const [signIn, setSignIn] = useState(false)
  return (
    <>
      <ProfileRowView
        model={model}
        onOpen={() => setSheet(true)}
        onSignIn={() => setSignIn(true)}
      />
      {sheet && (
        <ProfileSheet
          toolName={toolName}
          model={model}
          account={account}
          onOpenChange={setSheet}
          onSignIn={() => setSignIn(true)}
        />
      )}
      {signIn && (
        <ProfileSignInDialog
          driver={account.driver}
          name={account.name}
          account={account}
          tenant={activeTenant}
          onOpenChange={setSignIn}
        />
      )}
    </>
  )
}
