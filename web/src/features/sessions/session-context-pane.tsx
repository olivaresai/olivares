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
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { Compass } from 'lucide-react'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useAuth } from '@/lib/auth/context'
import { useTenantLabel } from '@/components/layout/tenant-label'
import type { ConversationItem } from './conversation-frames'
import { primaryRun } from './provenance'
import { RefChip } from './ref-chip'
import type { SessionResolution } from './use-session-resolution'
import './i18n'

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
}: {
  resolution: SessionResolution
  inspected?: ConversationItem | null
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const org = useTenantLabel()
  const { target, session, live } = resolution
  const run = primaryRun(session.runs)
  const canReadWorkspaces = can('sessions:workspace:read')
  const canReadProfiles = can('sessions:profile:read')
  const workspacesQuery = useQuery({
    queryKey: agentOpsKeys.workspaces(activeTenant),
    queryFn: () => agentOpsApi.listWorkspaces({ limit: 200 }),
    enabled: canReadWorkspaces && !!activeTenant && !!run?.workspace_ref,
  })
  const workspaceName =
    workspacesQuery.data?.items.find(
      (w) => w.workspace_ref === run?.workspace_ref,
    )?.name ?? null
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

  return (
    <div className="flex flex-col gap-4" data-testid="session-context">
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
            <span className="text-caption text-foreground">
              {workspaceName || t('context.noWorkspace')}
            </span>
          </Row>
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
              <span className="text-caption text-muted-foreground">{none}</span>
            )}
          </Row>
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
            <RefChip value={live?.live_ref || session.liveRef} absent={none} />
          </Row>
          <Row label={t('context.runRef')}>
            <RefChip value={run?.run_ref} absent={none} />
          </Row>
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
    </div>
  )
}
