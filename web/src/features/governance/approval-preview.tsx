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
import type { TFunction } from 'i18next'
import { CircleCheck, Waypoints } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { KvList, KvRow } from '@/components/ui/kv'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
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
    /** What the session reaches, as the run read reports it (#2545). */
    reaches: owner?.reaches,
  }
}

/** The rule that asked, as one sentence: the approval policy the engine matched (by name
 * when this person may read the rules), or the default when none matched. Null while the
 * name is being read, so the line never flashes a wrong answer; a failed read says so and
 * is never shown as a rule they may not read. */
export function useMatchedRule(approval: ApprovalDTO): string | null {
  const { t } = useTranslation('governance')
  const { activeTenant, can } = useAuth()
  const readable = can('governance:policy:read')
  const ref = approval.policy_ref
  const policy = useQuery({
    queryKey: governanceKeys.policy(activeTenant, ref ?? ''),
    queryFn: () => governanceApi.getPolicy(ref as string),
    enabled: !!ref && readable,
    staleTime: 60_000,
  })
  if (!ref) return t('preview.matchedDefault')
  if (!readable) return t('preview.matchedUnreadable')
  if (policy.isPending) return null
  return policy.data?.name
    ? t('preview.matched', { rule: policy.data.name })
    : t('preview.matchedUnknown')
}

/** Request subjects that are themselves a destination: a new network host, an MCP server.
 * What such a request reaches is the subject it names. */
const DESTINATION_SUBJECTS = new Set(['network_destination', 'mcp_server'])

/** What a request reaches, as a person reads it: the destination it names, else the
 * folders and hosts of the session that asks. Empty when neither is known: nothing is
 * guessed. */
export function approvalReaches(
  approval: ApprovalDTO,
  reaches: RunDTO['reaches'],
  t: TFunction<'governance'>,
): string[] {
  if (
    approval.subject_kind &&
    approval.subject_ref &&
    DESTINATION_SUBJECTS.has(approval.subject_kind)
  )
    return [approval.subject_ref]
  if (!reaches) return []
  const known = [
    ...reaches.folders.map((f) =>
      t(`preview.reachFolder.${f.access}`, { path: f.path }),
    ),
    ...reaches.hosts,
  ]
  // A part the engine could not read is said, never left out as if the list were whole.
  return reaches.reason ? [...known, t('preview.reachIncomplete')] : known
}

/** Why a request needs a person and what it would reach: the two lines every request
 * carries, in the queue's row and in the open request. */
export function ApprovalWhy({
  approval,
  reaches,
  className,
}: {
  approval: ApprovalDTO
  reaches: RunDTO['reaches']
  className?: string
}) {
  const { t } = useTranslation('governance')
  const matched = useMatchedRule(approval)
  const reach = approvalReaches(approval, reaches, t)
  return (
    <span
      className={cn('flex min-w-0 flex-col gap-0.5 text-caption', className)}
      data-slot="approval-why"
    >
      {matched ? (
        <span className="flex min-w-0 items-center gap-1.5 text-text-2">
          <CircleCheck aria-hidden className="size-3.5 shrink-0" />
          <span className="min-w-0 truncate" title={matched}>
            {matched}
          </span>
        </span>
      ) : null}
      {reach.length > 0 ? (
        <span className="flex min-w-0 items-center gap-1.5 text-text-2">
          <Waypoints aria-hidden className="size-3.5 shrink-0" />
          <span
            className="min-w-0 truncate"
            title={t('preview.reaches', { list: reach.join(', ') })}
          >
            {t('preview.reaches', { list: reach.join(', ') })}
          </span>
        </span>
      ) : null}
    </span>
  )
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

const PUBLISH_SUBJECT = 'gitpublish.target'
/** Requests, besides `*.tool` calls, whose approval makes the engine run something: an
 * MCP tool call (cmd/olivares/mcpgateway.go; its arguments are not in the reason), a
 * deploy plan the executor applies or retires (modules/deploy/lifecycle.go) and an
 * AgentCore export apply (cmd/olivares/agentcoreexportgate.go). */
const RUNS_ON_APPROVAL = new Set([
  PROVIDER_APPROVAL,
  'mcp.tool.call',
  'deploy.apply',
  'deploy.retire',
  'agentcore.export.apply',
  'agentcore.export.apply_weakening',
])

/** Where a command, a tool call or a plan is expected: the requests the engine raises to
 * run something. Only on these is a missing review worth saying. A request a person
 * raised (a deploy, an export, a launch) runs nothing by itself, so the note would only
 * repeat under it (#2269). */
export function approvalExpectsCommand(approval: ApprovalDTO): boolean {
  return (
    !!approval.subject_kind?.endsWith('.tool') ||
    approval.subject_kind === PUBLISH_SUBJECT ||
    (!!approval.action && RUNS_ON_APPROVAL.has(approval.action))
  )
}

/** Reviewed text this long is cut to a few lines until the person opens it in full. */
export function reviewIsLong(text: string): boolean {
  return text.split('\n').length > 4 || text.length > 320
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
  const long = reviewIsLong(text)
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

/** What will run, as the engine stored it for review: the reviewed text verbatim, a note
 * when the tool's request names no command, or the request's own words otherwise. */
export function ApprovalWhatRuns({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  const review = approval.review
  const toolCall = approval.subject_kind?.endsWith('.tool')
  if (approvalCommandUnknown(approval))
    return (
      <p
        className="text-body text-warning"
        data-slot="approval-command-unknown"
      >
        {t('preview.commandUnknown')}
      </p>
    )
  if (review?.text)
    return (
      <div className="flex min-w-0 flex-col gap-1">
        <span className="text-overline text-text-3">
          {t('preview.willRun')}
        </span>
        <ReviewText text={review.text} />
      </div>
    )
  return (
    <div className="flex flex-col gap-1 text-body">
      {!toolCall && approval.reason ? (
        <p className="break-words font-medium">{approval.reason}</p>
      ) : !toolCall && approval.action ? (
        <p className="font-medium">{readableAction(approval.action)}</p>
      ) : null}
      {approvalExpectsCommand(approval) ? (
        <p className="text-text-2" data-slot="approval-no-review">
          {t('preview.noReview')}
        </p>
      ) : null}
    </div>
  )
}

/** One request, as a person reviews it: session, folder, who asks, tool, then what runs. */
export function ApprovalPreview({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  const session = useApprovalSession(approval)
  const members = useMemberNames()
  const { principal, can } = useAuth()
  const requester = approval.launched_by || approval.requested_by
  const review = approval.review
  const toolCall = approval.subject_kind?.endsWith('.tool')
  return (
    <section
      className="flex min-w-0 flex-col gap-2"
      aria-label={toolCall ? t('preview.title') : t('preview.requestSummary')}
      data-slot="approval-preview"
    >
      <KvList>
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
      <ApprovalWhatRuns approval={approval} />
      <ApprovalWhy approval={approval} reaches={session.reaches} />
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
  // A request a person raised leads with their own words; the kind of request
  // ("Killswitch release") names it only when they wrote none (#2548, audit finding 8).
  const head =
    [review?.tool, session.name].filter(Boolean).join(' · ') ||
    (!toolCall && approval.action && !approval.reason
      ? readableAction(approval.action)
      : '')
  return (
    // A fixed width with every line cut to one: a long folder widened the column and
    // pushed Approve and Reject off the right edge at 1280 (Root, 09b capture review).
    // A span, because the queue's row is a button and holds phrasing content only.
    <span
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
              className={cn(
                'truncate',
                head
                  ? 'text-caption text-text-2'
                  : 'font-medium text-foreground',
              )}
              title={approval.reason}
            >
              {approval.reason}
            </span>
          ) : null}
          {approvalExpectsCommand(approval) ? (
            <span className="text-caption text-text-2">
              {t('preview.noReview')}
            </span>
          ) : null}
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
    </span>
  )
}

/** Who asks, as words only: the queue's row names the person and opens nothing inside
 * itself. The engine's handle stays in Details. */
export function AskedByName({ approval }: { approval: ApprovalDTO }) {
  const { t } = useTranslation('governance')
  const members = useMemberNames()
  const { principal, can } = useAuth()
  const requester = approval.launched_by || approval.requested_by
  return askedBy(
    approval,
    t,
    can('user:read') && requester?.startsWith('user:')
      ? members.nameOf(requester)
      : null,
    principal?.actor,
  )
}
