// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT WILL RUN, BEFORE APPROVE (SC sweep on 09b, Root URGENT): the queue showed
// claude.tool.use, a #plan/singleuse reference and session:osn_…, and the Approve dialog
// only audit wording. A reviewer approved without seeing the command. The request now
// leads with the session, its folder, who asks and the tool, then the reviewed text
// exactly as the engine stored it (`review`, PEP/N1). Without that field it says so in
// one line; the stored `reason` is never cut up to fake it. The references go behind
// Details.
import { useQuery } from '@tanstack/react-query'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { KvList, KvRow } from '@/components/ui/kv'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { sessionNameLadder } from '@/features/home/work-line'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { runAwaitedApproval } from '@/features/sessions/provenance'
import { useMemberNames } from '@/features/shared'
import { readableAction } from '@/components/layout/bell-events'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import './i18n'
import { governanceApi, governanceKeys } from './api'
import type { ApprovalDTO } from './types'

const SESSION_ACTOR = 'session:'

/** The session a request belongs to: its `session_ref`, or the session that asked (the
 * audit actor `session:<id>`, a structured kind:id, not free text). */
export function approvalSessionId(a: ApprovalDTO): string | undefined {
  if (a.session_ref) return a.session_ref
  return a.requested_by?.startsWith(SESSION_ACTOR)
    ? a.requested_by.slice(SESSION_ACTOR.length) || undefined
    : undefined
}

/** The session's name and folder, from the reads the Sessions page makes. A waiting run
 * names its request (`runAwaitedApproval` is this approval's id, the join the rail's
 * "Needs you" already makes); otherwise the session's live row. Unread or not permitted:
 * undefined, and the preview says less rather than guessing. */
export function useApprovalSession(approval: ApprovalDTO) {
  const { activeTenant, can } = useAuth()
  const { t } = useTranslation('governance')
  const id = approvalSessionId(approval)
  const runParams = { limit: 200 }
  const runs = useQuery({
    queryKey: agentOpsKeys.runs(activeTenant, runParams),
    queryFn: () => agentOpsApi.listRuns(runParams),
    enabled: !!id && can('sessions:run:read'),
    staleTime: 10_000,
  })
  const waiting = runs.data?.items.find(
    (r) => runAwaitedApproval(r) === approval.id,
  )
  const params = { session_ref: id ?? '', limit: 5 }
  const live = useQuery({
    queryKey: sessionsKeys.live(activeTenant, params),
    queryFn: () => sessionsApi.live(params),
    enabled: !waiting && !!id && can('sessions:live:read'),
    staleTime: 30_000,
  })
  const row = live.data?.items.find(
    (r) => r.session_ref === id || r.canonical_sid === id,
  )
  const runRef = waiting ? undefined : row?.run_ref
  const run = useQuery({
    queryKey: agentOpsKeys.run(activeTenant, runRef ?? ''),
    queryFn: () => agentOpsApi.getRun(runRef as string),
    enabled: !!runRef && can('sessions:run:read'),
    staleTime: 30_000,
  })
  const owner = waiting ?? run.data
  const workspaceRef = owner?.workspace_ref
  const workspace = useQuery({
    queryKey: ['approval-preview', activeTenant, 'workspace', workspaceRef],
    queryFn: () => agentOpsApi.getWorkspace(workspaceRef as string),
    enabled: !!workspaceRef,
    staleTime: 60_000,
  })
  return {
    name:
      owner || row
        ? sessionNameLadder(owner?.name, row, t('preview.untitled')).text
        : undefined,
    folder: workspace.data?.root_path,
  }
}

/** Who asks, in words: the session itself, or the actor the engine recorded. */
function askedBy(
  a: ApprovalDTO,
  t: (k: string, options?: { name: string }) => string,
  name?: string | null,
  actor?: string | null,
): string {
  const requester = a.launched_by || a.requested_by
  if (!requester) return '—'
  const person = requester.startsWith(SESSION_ACTOR)
    ? t('preview.askedBySession')
    : (name ??
      (requester === actor
        ? t('preview.you')
        : requester.startsWith('user:')
          ? t('preview.member')
          : requester.startsWith('token:')
            ? t('preview.apiToken')
            : t('detail.requestedBy')))
  return a.launched_by
    ? t('preview.askedByForSession', { name: person })
    : person
}

/** The action of a tool's own permission request (cmd/olivares/providerapproval.go). */
const PROVIDER_APPROVAL = 'sessions.provider.approval'
/** Its reason, as the engine writes it: "Provider permission: driver=… method=… kind=…
 * run=… turn=… permissions=<JSON scope>". The fields before the scope are identifiers,
 * so the scope is what follows the first "permissions=". */
const PROVIDER_REASON =
  /^Provider permission: driver=\S+\s+method=\S+\s+kind=\S+\s+run=\S*\s+turn=\S*\s+permissions=/

/** The permission scope of a provider permission request, read from its structured reason;
 * null for any other request, or when the scope does not parse (a masked reason). */
function providerScope(
  approval: ApprovalDTO,
): { CommandLine?: unknown; FilePaths?: unknown } | null {
  if (approval.action !== PROVIDER_APPROVAL) return null
  const head = PROVIDER_REASON.exec(approval.reason ?? '')
  if (!head) return null
  try {
    const scope: unknown = JSON.parse(approval.reason!.slice(head[0].length))
    return scope && typeof scope === 'object' ? scope : null
  } catch {
    return null
  }
}

/** A provider permission request that names no command and no path (HU-R32: OpenCode
 * sends "CommandLine":"","FilePaths":null today, and its review lists the permission
 * options, not an operation). It cannot show what will run, and must not look as if it
 * did. Only the structured scope decides: a reviewed command is always shown as it is,
 * whatever it prints (SR4C on 912c4d15). */
export function approvalCommandUnknown(approval: ApprovalDTO): boolean {
  const scope = providerScope(approval)
  if (!scope) return false
  const tool = approval.review?.tool
  if (tool === 'Command' || tool === 'File change') return false
  const noCommand =
    typeof scope.CommandLine !== 'string' || scope.CommandLine === ''
  const noPaths =
    scope.FilePaths == null ||
    (Array.isArray(scope.FilePaths) && scope.FilePaths.length === 0)
  return noCommand && noPaths
}

/** The reviewed text, verbatim and monospace; long text opens in full on demand. */
export function ReviewText({
  text,
  compact = false,
}: {
  text: string
  compact?: boolean
}) {
  const { t } = useTranslation('governance')
  const [all, setAll] = useState(false)
  const long = text.split('\n').length > 4 || text.length > 320
  if (compact)
    return (
      <span
        data-slot="approval-review"
        className="block min-w-0 truncate font-mono text-caption text-foreground"
        title={text}
      >
        {text.replace(/\s*\n\s*/g, ' ')}
      </span>
    )
  return (
    <div className="flex min-w-0 flex-col items-start gap-1">
      <pre
        data-slot="approval-review"
        className={cn(
          'm-0 w-full overflow-x-auto whitespace-pre-wrap break-all rounded-md bg-muted px-2.5 py-2 font-mono text-caption text-foreground',
          long && !all && 'max-h-24 overflow-hidden',
        )}
      >
        {text}
      </pre>
      {long ? (
        <button
          type="button"
          className="text-caption text-accent-text underline underline-offset-2"
          aria-expanded={all}
          onClick={() => setAll((v) => !v)}
        >
          {all ? t('preview.showLess') : t('preview.showAll')}
        </button>
      ) : null}
    </div>
  )
}

/** One request, as a person reviews it: session, folder, who asks, tool, then what runs. */
export function ApprovalPreview({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  const session = useApprovalSession(approval)
  const members = useMemberNames()
  const { principal, activeTenant, can } = useAuth()
  const requester = approval.launched_by || approval.requested_by
  const policies = useQuery({
    queryKey: governanceKeys.policies(activeTenant),
    queryFn: () => governanceApi.listPolicies(),
    enabled: !!approval.policy_ref && can('governance:policy:read'),
    staleTime: 60_000,
  })
  const policyName = can('governance:policy:read')
    ? policies.data?.items.find((p) => p.id === approval.policy_ref)?.name
    : undefined
  const review = approval.review
  const unknown = approvalCommandUnknown(approval)
  const toolCall = approval.subject_kind?.endsWith('.tool')
  return (
    <section
      className="flex min-w-0 flex-col gap-2"
      aria-label={toolCall ? t('preview.title') : t('preview.requestSummary')}
      data-slot="approval-preview"
    >
      <KvList>
        {policyName ? (
          <KvRow label={t('detail.policyRef')}>{policyName}</KvRow>
        ) : null}
        {session.name ? (
          <KvRow label={t('preview.session')}>{session.name}</KvRow>
        ) : null}
        {session.folder ? (
          <KvRow label={t('preview.folder')} mono>
            {session.folder}
          </KvRow>
        ) : null}
        <KvRow label={t('preview.askedBy')}>
          {askedBy(
            approval,
            t,
            can('user:read') && requester?.startsWith('user:')
              ? members.nameOf(requester)
              : null,
            principal?.actor,
          )}
        </KvRow>
        {review?.tool ? (
          <KvRow label={t('preview.tool')}>{review.tool}</KvRow>
        ) : null}
      </KvList>
      {unknown ? (
        <p
          className="text-body text-warning"
          data-slot="approval-command-unknown"
        >
          {t('preview.commandUnknown')}
        </p>
      ) : review?.text ? (
        <div className="flex min-w-0 flex-col gap-1">
          <span className="text-overline text-text-3">
            {t('preview.willRun')}
          </span>
          <ReviewText text={review.text} />
        </div>
      ) : !toolCall ? (
        <div className="flex flex-col gap-1 text-body">
          {approval.action ? (
            <p className="font-medium">{readableAction(approval.action)}</p>
          ) : null}
          {approval.reason ? (
            <p className="break-words text-text-2">{approval.reason}</p>
          ) : null}
          <p className="text-text-2" data-slot="approval-no-review">
            {t('preview.noReview')}
          </p>
        </div>
      ) : (
        <p className="text-body text-text-2" data-slot="approval-no-review">
          {t('preview.noReview')}
        </p>
      )}
    </section>
  )
}

/** The engine's references for a request, behind one disclosure. */
export function ApprovalDetails({
  approval,
  children,
}: {
  approval: ApprovalDTO
  children?: ReactNode
}) {
  const { t } = useTranslation('governance')
  return (
    <details className="group min-w-0" data-slot="approval-details">
      <summary className="cursor-pointer text-caption text-text-2 select-none">
        {t('preview.details')}
      </summary>
      <div className="mt-2 flex min-w-0 flex-col gap-2">
        {children ?? (
          <KvList>
            {approval.action ? (
              <KvRow label={t('detail.action')} mono>
                {approval.action}
              </KvRow>
            ) : null}
            {approval.subject_kind ? (
              <KvRow label={t('detail.subjectKind')}>
                {approval.subject_kind}
              </KvRow>
            ) : null}
            {approval.subject_ref ? (
              <KvRow label={t('detail.subjectRef')} mono align="start">
                <span className="break-all">{approval.subject_ref}</span>
              </KvRow>
            ) : null}
            <KvRow label={t('preview.id')} mono>
              {approval.id}
            </KvRow>
            {approval.reason ? (
              <KvRow label={t('detail.reason')} align="start">
                <span className="break-words">{approval.reason}</span>
              </KvRow>
            ) : null}
          </KvList>
        )}
      </div>
    </details>
  )
}

/** The list's Request cell: session and tool on the first line, what runs on the second
 * (one line; whole in the review), the folder on the third. */
export function ApprovalRequestCell({
  approval,
  className,
}: {
  approval: ApprovalDTO
  /** Another surface's width (Now's card fills its row); the list keeps its fixed column. */
  className?: string
}) {
  const { t } = useTranslation('governance')
  const session = useApprovalSession(approval)
  const review = approval.review
  const toolCall = approval.subject_kind?.endsWith('.tool')
  const head =
    [review?.tool, session.name].filter(Boolean).join(' · ') ||
    (!toolCall && approval.action ? readableAction(approval.action) : '')
  return (
    // A fixed width with every line cut to one: a long folder widened the column and
    // pushed Approve and Reject off the right edge at 1280 (Root, 09b capture review).
    <div
      className={cn(
        'flex w-[20rem] min-w-0 max-w-[20rem] flex-col gap-0.5',
        className,
      )}
      data-slot="approval-request"
    >
      {head ? (
        <span className="truncate font-medium text-foreground">{head}</span>
      ) : null}
      {approvalCommandUnknown(approval) ? (
        <span
          className="text-caption text-warning"
          data-slot="approval-command-unknown"
        >
          {t('preview.commandUnknown')}
        </span>
      ) : review?.text ? (
        <ReviewText text={review.text} compact />
      ) : (
        <>
          {!toolCall && approval.reason ? (
            <span
              className="truncate text-caption text-text-2"
              title={approval.reason}
            >
              {approval.reason}
            </span>
          ) : null}
          <span className="text-caption text-text-2">
            {t('preview.noReview')}
          </span>
        </>
      )}
      {session.folder ? (
        <span
          className="truncate font-mono text-caption text-text-2"
          title={session.folder}
          data-slot="approval-folder"
        >
          {session.folder}
        </span>
      ) : null}
      {/* On a phone only this column is in view: say that the row opens the decision. */}
      {approval.status === 'pending' ? (
        <span
          className="text-caption text-accent-text md:hidden"
          data-slot="approval-open-hint"
        >
          {t('preview.openToDecide')}
        </span>
      ) : null}
    </div>
  )
}

/** Who asks, for the list's column. The engine's handle remains in Details. */
export function AskedBy({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  const members = useMemberNames()
  const { principal, can } = useAuth()
  const requester = approval.launched_by || approval.requested_by
  return (
    <div className="min-w-0">
      <span
        className={cn(
          'block max-w-[12rem] truncate text-caption',
          'text-foreground',
        )}
        title={requester}
      >
        {askedBy(
          approval,
          t,
          can('user:read') && requester?.startsWith('user:')
            ? members.nameOf(requester)
            : null,
          principal?.actor,
        )}
      </span>
      <details
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ')
            event.stopPropagation()
        }}
      >
        <summary className="cursor-pointer text-caption text-text-2">
          {t('preview.details')}
        </summary>
        {approval.launched_by ? (
          <span className="block break-all font-mono text-caption">
            {approval.launched_by}
          </span>
        ) : null}
        <span className="block break-all font-mono text-caption">
          {approval.requested_by}
        </span>
      </details>
    </div>
  )
}
