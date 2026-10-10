// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THIS SESSION APPLIES TO — the right pane of the work surface.
//
// The console keeps one rule about scope: *"the scope of the next action is always on
// screen"*. The shell's status line answers that for the NEXT
// action; this answers it for the session the operator is READING, which is a
// different question and usually a different answer — a session launched last week did
// not run under the workspace selected today.
//
// ⛔ EVERY ROW IS A REFERENCE THE ENGINE SENT, AND ABSENT IS A VALUE. `provider`,
//    `environment_ref` and the profile are documented as ABSENT on a legacy row, and a
//    pane that filled them from the topbar would be reporting today's selection as
//    yesterday's fact. So each row says `not declared` or `none` — the two are
//    different sentences: nobody declared it, versus there is none.
//
// ⛔ AND THE GOVERNANCE MARKERS ARE THE SESSION'S OWN, NOT A POSTURE THIS PANE INFERS.
//    `posture` is the WEAKEST value the engine saw across the producing connectors,
//    `pep_provisioned` and `record_io` are stored on the run. A blank badge tells the
//    truth where "enforced" by default would not (features/sessions/types.ts).
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Segmented } from '@/components/ui/segmented'
import { Compass } from 'lucide-react'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { RunDTO, WorkspaceDTO } from '@/features/agentops/types'
import { WorkspaceBrowser } from '@/features/agentops/workspace-browser'
import { consoleApi } from '@/features/console/api'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import {
  RESERVED_SESSION_PANEL_IDS,
  useOfferedPanels,
  type SessionPanelSession,
} from '@/features/panels'
import { SessionPublish } from '@/features/gitpublish/session-publish'
import { useAuth } from '@/lib/auth/context'
import { useTenantLabel } from '@/components/layout/tenant-label'
import type { ConversationItem } from './conversation-frames'
import { primaryRun, type UnifiedSession } from './provenance'
import type { EvidenceBlock } from './session-address'
import { hasThread } from './has-thread'
import { SessionOverview } from './session-overview'
import { RefChip } from './ref-chip'
import { SessionBranchChanges } from './session-branch-changes'
import { SessionChanges } from './session-changes'
import { SessionGit } from './session-git'
import { SessionPreview } from './session-preview'
import type { SessionResolution } from './use-session-resolution'
import './i18n'
import '@/features/agentops/i18n'

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-0.5 py-1.5">
      <dt className="text-overline text-muted-foreground uppercase">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  )
}

export function SessionContextPane({
  resolution,
  inspected,
  evidence,
  onExpandEvidence,
  peerSessions,
  frameCwd,
  panel,
  onPanel,
}: {
  resolution: SessionResolution
  inspected?: ConversationItem | null
  /** The overview's evidence block that is open (`?evidence=`). */
  evidence?: EvidenceBlock
  onExpandEvidence?: (block: EvidenceBlock) => void
  /** The sessions on the surface, from which "Can message" offers this one's peers. */
  peerSessions?: readonly UnifiedSession[]
  frameCwd?: string | null
  /** The side pane's tab in front (`?panel=`); Context when absent or not one of the tabs. */
  panel?: string
  onPanel?: (panel: string) => void
}) {
  const { t } = useTranslation(['sessions', 'agentops'])
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const org = useTenantLabel()
  const { target, session, live } = resolution
  const run = can('sessions:run:read') ? primaryRun(session.runs) : undefined
  const canReadWorkspaces = can('sessions:workspace:read')
  const canReadProfiles = can('sessions:profile:read')
  const workspacesQuery = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'workspace',
      run?.workspace_ref,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.getWorkspace(run!.workspace_ref!, {
        tenant: activeTenant!,
        signal,
      }),
    enabled: canReadWorkspaces && !!activeTenant && !!run?.workspace_ref,
  })
  const workspace = canReadWorkspaces ? workspacesQuery.data : undefined
  const folderName = workspace?.name ?? null
  const workspaceId = run?.authz_workspace_id
  const canReadWorkspace = can('tenant:read')
  const workspaceQuery = useQuery({
    // Reuse the boundary's cancellation and retirement, including while this pane is closed.
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'authorization-workspace',
      workspaceId,
    ],
    queryFn: ({ signal }) =>
      consoleApi.getWorkspaceByID(workspaceId!, { signal }),
    enabled: canReadWorkspace && !!activeTenant && !!workspaceId,
  })
  const profileRef = live?.provider_profile_ref || run?.provider_profile_ref
  const profilesQuery = useQuery({
    queryKey: agentOpsKeys.profiles(activeTenant, boundary.epoch, {
      limit: 200,
    }),
    queryFn: ({ signal }) =>
      agentOpsApi.listProfiles({ limit: 200 }, { signal }),
    enabled: canReadProfiles && !!activeTenant && !!profileRef,
  })
  const profileName =
    profilesQuery.data?.items
      .find((p) => p.profile_ref === profileRef)
      ?.display_name?.trim() ||
    profilesQuery.data?.items.find((p) => p.profile_ref === profileRef)
      ?.driver ||
    live?.provider ||
    run?.provider_driver ||
    null

  // THE TABS: the console's own four, then what a build registers.
  // A registrant that took one of the pane's own ids is ignored, so a build can add tabs
  // and never replace Context, Changes, Files or Preview.
  const registered = useOfferedPanels(
    (PANEL_EXTENSIONS.sessionPanels ?? []).filter(
      (p) => !(RESERVED_SESSION_PANEL_IDS as readonly string[]).includes(p.id),
    ),
  )
  const [localTab, setLocalTab] = useState('context')
  const options = [
    { value: 'context', label: t('surface.pane.context') },
    { value: 'changes', label: t('context.tabChanges') },
    { value: 'files', label: t('context.tabFiles') },
    // The app the session serves on a local port. Only a launched session has one.
    ...(run ? [{ value: 'preview', label: t('preview.title') }] : []),
    ...registered.map((p) => ({
      value: p.id,
      label: t(p.labelKey),
      icon: <p.icon />,
    })),
  ]
  const asked = onPanel ? (panel ?? 'context') : (panel ?? localTab)
  const tab = options.some((o) => o.value === asked) ? asked : 'context'
  const extension = registered.find((p) => p.id === tab)
  const panelSession: SessionPanelSession = {
    ref: run?.run_ref ?? live?.live_ref ?? session.sessionRef ?? '',
    workspaceRef: run?.workspace_ref,
    tool: run?.provider_driver || live?.provider,
    state: run?.state ?? live?.cc_state,
  }

  if (!target)
    return (
      <EmptyState
        icon={<Compass />}
        title={t('context.noneTitle')}
        description={t('context.noneDescription')}
      />
    )

  const none = t('context.none')
  const notDeclared = t('context.notDeclared')

  const contextBody = (
    <>
      {/* What the thread's header no longer says: who manages the session, its mode, the
          folder, what it did and its evidence. A session with no conversation of its own
          paints this block as the thread's body instead (`session-narrative.tsx`). */}
      {hasThread(resolution) && evidence && onExpandEvidence ? (
        <SessionOverview
          resolution={resolution}
          evidence={evidence}
          onExpandEvidence={onExpandEvidence}
          peerSessions={peerSessions}
          frameCwd={frameCwd}
        />
      ) : null}
      <details data-testid="context-details">
        <summary className="cursor-pointer text-caption font-medium text-foreground">
          {t('context.details')}
        </summary>
        <section data-testid="context-scope">
          <h3 className="text-caption font-medium text-foreground">
            {t('context.scopeTitle')}
          </h3>
          <p className="text-caption text-muted-foreground">
            {t('context.scopeHint')}
          </p>
          <dl className="mt-1 divide-y divide-border">
            <Row label={t('context.tenant')}>
              <span className="text-caption text-foreground">
                {org.name || none}
              </span>
            </Row>
            <Row label={t('context.workspace')}>
              {canReadWorkspace && workspaceQuery.isSuccess ? (
                <span className="text-caption text-foreground">
                  {workspaceQuery.data.name}
                </span>
              ) : (
                <RefChip value={workspaceId} absent={notDeclared} />
              )}
            </Row>
            <Row label={t('context.folder')}>
              {run?.workspace_ref ? (
                folderName ? (
                  <span className="text-caption text-foreground">
                    {folderName}
                  </span>
                ) : (
                  <RefChip value={run.workspace_ref} absent={notDeclared} />
                )
              ) : (
                <span className="text-caption text-muted-foreground">
                  {run?.workspace_path
                    ? t('context.temporaryFolder')
                    : notDeclared}
                </span>
              )}
            </Row>
            {run?.worktree_branch && (
              <Row label={t('agentops:info.worktreeBranch')}>
                <span className="text-caption text-foreground">
                  {run.worktree_branch}
                </span>
              </Row>
            )}
            <Row label={t('context.environment')}>
              <span className="text-caption text-foreground">
                {live?.environment_ref || run?.provider_environment_ref
                  ? t('context.thisNode')
                  : notDeclared}
              </span>
            </Row>
            <Row label={t('context.provider')}>
              {live?.provider || run?.provider_driver ? (
                <span className="text-caption text-foreground">
                  {live?.provider || run?.provider_driver}
                </span>
              ) : (
                <span className="text-caption text-muted-foreground">
                  {notDeclared}
                </span>
              )}
            </Row>
            <Row label={t('context.profile')}>
              {profileName ? (
                <span
                  className="text-caption text-foreground"
                  title={profileRef || undefined}
                >
                  {profileName}
                </span>
              ) : (
                <span className="text-caption text-muted-foreground">
                  {none}
                </span>
              )}
            </Row>
          </dl>
        </section>

        <section data-testid="context-work-scope">
          <h3 className="text-caption font-medium text-foreground">
            {t('context.workTitle')}
          </h3>
          <p className="text-caption text-muted-foreground">
            {t('context.workHint')}
          </p>
          <dl className="mt-1 divide-y divide-border">
            <Row label={t('context.processState')}>
              <span
                data-testid="context-process-state"
                className="text-caption"
              >
                {run?.process_state
                  ? t(`agentops:state.${run.process_state}`, {
                      defaultValue: run.process_state,
                    })
                  : t('context.notRecorded')}
              </span>
            </Row>
            <Row label={t('context.activityState')}>
              <span
                data-testid="context-activity-state"
                className="text-caption"
              >
                {run?.state
                  ? t(`agentops:state.${run.state}`, {
                      defaultValue: run.state,
                    })
                  : t('context.notRecorded')}
              </span>
            </Row>
            <Row label={t('context.workRole')}>
              <span className="text-caption">
                {run?.work_scope
                  ? t(`context.workRoles.${run.work_scope.role}`)
                  : t('context.notRecorded')}
              </span>
            </Row>
            <Row label={t('context.workWorkspace')}>
              <RefChip
                value={run?.work_scope?.workspace_id}
                absent={t('context.notRecorded')}
              />
            </Row>
            {run?.work_scope?.capabilities?.length ? (
              <Row label={t('context.workActions')}>
                <ul className="text-caption">
                  {run.work_scope.capabilities.map((action) => (
                    <li key={action}>
                      {t(
                        `agentops:profiles.workGrant.capabilities.${action.replace('.', '_')}`,
                      )}
                    </li>
                  ))}
                </ul>
              </Row>
            ) : null}
          </dl>
        </section>

        <section data-testid="context-identifiers">
          <h3 className="text-caption font-medium text-foreground">
            {t('context.identifiersTitle')}
          </h3>
          <p className="text-caption text-muted-foreground">
            {t('context.identifiersHint')}
          </p>
          <dl className="mt-1 divide-y divide-border">
            <Row label={t('context.tenant')}>
              <RefChip value={org.tenant || activeTenant} absent={none} />
            </Row>
            <Row label={t('context.workspace')}>
              <RefChip value={workspaceId} absent={notDeclared} />
            </Row>
            <Row label={t('context.folder')}>
              <RefChip value={run?.workspace_ref} absent={none} />
            </Row>
            <Row label={t('context.environment')}>
              <RefChip
                value={live?.environment_ref || run?.provider_environment_ref}
                absent={none}
              />
            </Row>
            <Row label={t('context.profile')}>
              <RefChip value={profileRef} absent={none} />
            </Row>
            <Row label={t('context.sessionRef')}>
              <RefChip value={session.sessionRef} absent={none} />
            </Row>
            <Row label={t('context.liveRef')}>
              <RefChip
                value={live?.live_ref || session.liveRef}
                absent={none}
              />
            </Row>
            <Row label={t('context.runRef')}>
              <RefChip value={run?.run_ref} absent={none} />
            </Row>
            {/* Rows the plane wrote for this run on other channels (J5): shown here,
                not as sessions of their own. */}
            {session.echoes?.length ? (
              <Row label={t('context.echoes')}>
                <span className="flex flex-wrap justify-end gap-1">
                  {session.echoes.map((e) => (
                    <RefChip
                      key={e.live_ref}
                      value={e.live_ref}
                      absent={none}
                    />
                  ))}
                </span>
              </Row>
            ) : null}
            {live?.canonical_sid ? (
              <Row label={t('context.canonicalSid')}>
                <RefChip value={live.canonical_sid} absent={none} />
              </Row>
            ) : null}
            {live?.source_binding_ref ? (
              <Row label={t('context.binding')}>
                <RefChip value={live.source_binding_ref} absent={none} />
              </Row>
            ) : null}
            <Row label={t('context.engine')}>
              <span className="text-caption text-foreground">
                {live?.engine || notDeclared}
              </span>
            </Row>
            <Row label={t('context.posture')}>
              <span className="text-caption text-foreground">
                {live?.posture
                  ? t(`card.postureValue.${live.posture}`, {
                      defaultValue: live.posture,
                    })
                  : notDeclared}
              </span>
            </Row>
            {run ? (
              <Row label={t('context.governance')}>
                <ul className="flex flex-col gap-0.5 text-caption text-muted-foreground">
                  <li>
                    {run.pep_provisioned
                      ? t('context.pepOn')
                      : t('context.pepOff')}
                  </li>
                  <li>
                    {run.record_io
                      ? t('context.recordOn')
                      : t('context.recordOff')}
                  </li>
                  {run.approval_ref ? (
                    <li className="flex items-center gap-1.5">
                      {t('context.approval')}
                      <RefChip value={run.approval_ref} absent={none} />
                    </li>
                  ) : null}
                </ul>
              </Row>
            ) : null}
          </dl>
        </section>
      </details>

      <section>
        <h3 className="text-caption font-medium text-foreground">
          {t('context.wireTitle')}
        </h3>
        <p className="text-caption text-muted-foreground">
          {t('context.wireHint')}
        </p>
        {inspected ? (
          <pre
            data-testid="context-wire"
            className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border bg-muted p-2 font-mono text-caption text-foreground"
          >
            {inspected.raw.join('\n')}
          </pre>
        ) : (
          <p className="mt-2 text-caption text-muted-foreground">
            {t('context.wireEmpty')}
          </p>
        )}
      </section>
    </>
  )
  const changesBody = (
    <>
      {run ? null : (
        <p className="text-caption text-muted-foreground">
          {t('context.noChanges')}
        </p>
      )}
      {/* What the session changed comes first: it is what a person reviews beside the
          conversation. The scope it ran under stays one click away. */}
      {run ? <SessionGit run={run} /> : null}
      {run ? <SessionChanges run={run} /> : null}
      {/* Then what a person does with them: publish the session's commit, when its
          workspace has a publication target. */}
      {/* Keyed by run: another session is another decision, never this one's open dialog. */}
      {run ? <SessionPublish key={run.run_ref} run={run} /> : null}
      {/* A session in its own worktree also shows what its branch holds against where
          it began: the work a handoff named, and what the session added to it. */}
      {run?.worktree_branch ? <SessionBranchChanges run={run} /> : null}
    </>
  )
  const filesBody = (
    <SessionFiles
      key={`${boundary.key}:${run?.run_ref ?? ''}`}
      run={run}
      workspace={workspace}
      canRead={canReadWorkspaces}
      loading={
        workspacesQuery.isPending && workspacesQuery.fetchStatus !== 'idle'
      }
      failed={workspacesQuery.isError}
      onRetry={() => void workspacesQuery.refetch()}
    />
  )

  return (
    <div className="flex flex-col gap-4" data-testid="session-context">
      {/* THE SIDE PANE HAS TABS: Context (what the header no longer says, the scope and
          the references), Changes (what the session changed, and publishing it) and Files
          (the folder it works in), Preview (the app it serves on a local port), then whatever
          a build registers (a terminal). A build with none shows exactly these. */}
      <div className="-mx-1 overflow-x-auto px-1 pb-0.5">
        <Segmented
          size="sm"
          aria-label={t('context.panels')}
          options={options}
          value={tab}
          onValueChange={(value) => {
            setLocalTab(value)
            onPanel?.(value)
          }}
        />
      </div>
      {extension ? (
        <extension.Component key={extension.id} session={panelSession} />
      ) : null}
      {!extension && tab === 'changes' ? changesBody : null}
      {!extension && tab === 'files' ? filesBody : null}
      {/* Keyed by run: another session's preview is another URL. */}
      {!extension && tab === 'preview' && run ? (
        <SessionPreview key={run.run_ref} run={run} />
      ) : null}
      {!extension && tab === 'context' ? contextBody : null}
    </div>
  )
}

/**
 * The folder the session works in, in the workspace browser the Workspaces view opens.
 * Four answers, never merged: nothing to browse, no right to browse it, the read is on its
 * way, and the read failed (with a way to ask again).
 */
function SessionFiles({
  run,
  workspace,
  canRead,
  loading,
  failed,
  onRetry,
}: {
  run: RunDTO | undefined
  workspace: WorkspaceDTO | undefined
  canRead: boolean
  loading: boolean
  failed: boolean
  onRetry: () => void
}) {
  const { t } = useTranslation(['sessions', 'common'])
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const path = run?.workspace_path
  const differentRoot = !!path && !!workspace && path !== workspace.root_path
  // A file endpoint is jailed by its registered reference. A worktree's original
  // reference still names the checkout, so resolve the worktree's own registration.
  // Never change the DTO's root_path and silently send writes to the old reference.
  const actualQuery = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'workspace-files-root',
      path,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.listWorkspaces(
        { root_path: path!, state: 'active', limit: 1 },
        { tenant: activeTenant!, signal },
      ),
    enabled: canRead && !!activeTenant && differentRoot,
  })
  const actual = differentRoot
    ? actualQuery.data?.items.find(
        (w) => w.root_path === path && w.state === 'active',
      )
    : run?.worktree_branch && !path
      ? undefined
      : workspace
  loading ||= differentRoot && actualQuery.isPending
  failed ||= differentRoot && actualQuery.isError
  const retry = () => {
    onRetry()
    if (differentRoot) void actualQuery.refetch()
  }
  if (!run?.workspace_ref)
    return (
      <p
        className="text-caption text-muted-foreground"
        data-testid="context-no-files"
      >
        {t('sessions:context.noFiles')}
      </p>
    )
  if (!canRead)
    return (
      <p
        className="text-caption text-muted-foreground"
        data-testid="context-files-forbidden"
      >
        {t('sessions:context.filesNoAccess')}
      </p>
    )
  if (loading)
    return (
      <div
        aria-busy="true"
        data-testid="context-files-loading"
        className="flex flex-col gap-2"
      >
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-5 w-full" />
        <Skeleton className="h-5 w-1/2" />
      </div>
    )
  if (failed)
    return (
      <div
        role="alert"
        data-testid="context-files-failed"
        className="flex items-center gap-2 text-caption text-warning"
      >
        <span className="min-w-0 flex-1">
          {t('sessions:context.filesFailed')}
        </span>
        <Button variant="secondary" size="sm" onClick={retry}>
          {t('common:actions.retry')}
        </Button>
      </div>
    )
  if (!actual)
    return (
      <p
        className="text-caption text-muted-foreground"
        data-testid="context-files-forbidden"
      >
        {t('sessions:context.filesUnavailable')}
      </p>
    )
  return <WorkspaceBrowser workspace={actual} />
}
