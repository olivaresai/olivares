// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useEffect, useId, useRef, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { APPROVALS_SECTION } from '@/components/layout/shell-sections'
import { AppLink } from '@/components/ui/app-link'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PathText } from '@/components/ui/path-text'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { RunDTO, RunGitStatusDTO } from '@/features/agentops/types'
import { gitpublishApi, gitpublishKeys } from '@/features/gitpublish/api'
import {
  actionAuthority,
  failureOf,
  hasChangeAPI,
  newOperationId,
  refusalKey,
  safePullRequestUrl,
  sessionTargets,
} from '@/features/gitpublish/model'
import { ShipHeld } from '@/features/gitpublish/ship-dialog'
import type {
  PublicationIntent,
  PullRequestInput,
  PushInput,
  ShipResult,
} from '@/features/gitpublish/types'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import { useViewAccess } from '@/features/navigation/authorization'
import { stepHref } from '@/features/navigation/journeys'
import { viewById } from '@/features/navigation/model'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { useModuleOn } from '@/stores/modules'
import '@/features/gitpublish/i18n'
import './i18n'

/** The folder's status with the commit and tree HEAD names. The engine announced `head` for
 * this route; it is not in the generated client yet, so it is optional here. */
export type GitStatus = RunGitStatusDTO & {
  head?: { commit: string; tree: string }
}

/** A commit or tree name as the engine writes it: 40 lowercase hex characters, or 64 in a
 * SHA-256 repository. */
const OBJECT_NAME = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/
// A string first: RegExp.test turns any other value into one, an array included.
const isObjectName = (name: unknown) =>
  typeof name === 'string' && OBJECT_NAME.test(name)

/** The folder's HEAD when both of its names have that shape. The engine leaves `head` out
 * of a folder with no commit; any other value counts as no HEAD too, and is never sent. */
export function pushableHead(status?: GitStatus) {
  const head = status?.head
  return head && isObjectName(head.commit) && isObjectName(head.tree)
    ? head
    : undefined
}

/** A push or pull request the tenant's approval policy holds answers 202 with the shape of
 * a held ship: the engine gives the publishing routes the policy `/ships` asks. Typed here
 * until the client has it. */
type HeldPush = Partial<ShipResult> & { state: 'awaiting_approval' }
type PublishAnswer = PublicationIntent | HeldPush

type PublishAct = { target: string } & (
  | { effect: 'push'; body: PushInput }
  | { effect: 'pull_request'; body: PullRequestInput }
)

/** What the panel says once the approval it follows is decided, in the ship's words. */
const DECIDED = {
  approved: 'git.approvalGiven',
  rejected: 'git.approvalRefused',
  ended: 'git.approvalEnded',
} as const

/** Refusals the panel says in its own words, where the module's sentence names a control
 * this panel does not have, or misses what the panel knows: the session is someone else's. */
const PANEL_WORDS: Partial<Record<string, string>> = {
  stale_lease: 'git.staleLease',
  approval_denied: 'git.approvalDenied',
  secret_detected: 'git.secretDetected',
  session_source_refused: 'git.sourceRefused',
}

const LANDED: readonly string[] = ['applied', 'adopted']
const UNSETTLED: readonly string[] = ['uncertain', 'dispatching']
const answerOf = (intent: PublicationIntent) => intent.answer ?? intent.state
const isHeld = (answer?: PublishAnswer): answer is HeldPush =>
  answer?.state === 'awaiting_approval'

/** A 403 that refuses WHAT is pushed, not who pushes it: a detected credential, a declined
 * approval. The privileged hook would add a «not authorized» toast to any 403, so these
 * reach the panel as its own error and are said once, here. */
const NOT_A_ROLE: readonly string[] = ['secret_detected', 'approval_denied']
class PanelRefusal extends Error {
  readonly refusal: ApiError
  constructor(refusal: ApiError) {
    super(refusal.message)
    this.refusal = refusal
  }
}

/** PUSH, THEN THE PULL REQUEST, beside Commit. The folder's commit goes to the approved
 * remote of the workspace's Git publishing target through that module's own routes: the
 * engine fetches it from the session's folder and pushes it with the target's credential,
 * which never enters the session. One step at a time, each said where the person looks;
 * what landed is read back from the target's intents, so a reload says it again. Without
 * the folder's HEAD from the engine it says nothing: the publish block under the diff
 * pushes, as it did before. */
export function SessionPush({
  run,
  status,
  uncommitted,
  subject,
}: {
  run: RunDTO
  status: GitStatus
  uncommitted: boolean
  subject: string
}) {
  const { t } = useTranslation(['sessions', 'gitpublish'])
  const { activeTenant, can } = useAuth()
  const { navigable } = useViewAccess()
  const moduleOn = useModuleOn('gitpublish')
  const id = useId()
  const workspace = run.authz_workspace_id
  const head = pushableHead(status)
  const readable =
    moduleOn && !!workspace && !!head && can('gitpublish:target:read')
  const targets = useQuery({
    queryKey: gitpublishKeys.targets(activeTenant),
    queryFn: () => gitpublishApi.targets(),
    enabled: readable,
  })
  // The engine reads the run in the target's workspace: only such a target takes it.
  const { bound, served: target } = sessionTargets(
    targets.data?.items,
    workspace,
  )
  const intents = useQuery({
    queryKey: gitpublishKeys.intents(activeTenant, target?.id ?? ''),
    queryFn: () => gitpublishApi.intents(target!.id),
    enabled: !!target,
  })
  const [title, setTitle] = useState<string | null>(null)
  const [body, setBody] = useState('')
  const [base, setBase] = useState<string | null>(null)
  const [draft, setDraft] = useState(false)
  // Open pull request with no title says so at the Title field, which takes focus.
  const [titleAsked, setTitleAsked] = useState(false)
  const titleField = useRef<HTMLInputElement>(null)
  // One decision, one operation id: sending it again replays what the engine recorded,
  // and after a refused or ended approval asks again, since the engine finds an approval
  // by the act's plan, never by this id, and records nothing for a held or refused answer.
  // A changed commit or changed words are a new decision.
  const operations = useRef(new Map<string, string>())
  const operationFor = (decision: string[]) => {
    const key = decision.join('\n')
    const known = operations.current.get(key)
    if (known) return known
    const fresh = newOperationId()
    operations.current.set(key, fresh)
    return fresh
  }
  const publish = usePrivilegedMutation<PublishAct, PublishAnswer>({
    mutationFn: async (act) => {
      try {
        return act.effect === 'push'
          ? await gitpublishApi.push(act.target, act.body, {
              tenant: activeTenant,
            })
          : await gitpublishApi.openPullRequest(act.target, act.body, {
              tenant: activeTenant,
            })
      } catch (err) {
        if (
          err instanceof ApiError &&
          err.isForbidden &&
          NOT_A_ROLE.includes(err.code)
        )
          throw new PanelRefusal(err)
        throw err
      }
    },
    invalidateKeys: (_answer, act) => [
      gitpublishKeys.intents(activeTenant, act.target),
    ],
    successMessage: (answer) =>
      isHeld(answer)
        ? t('gitpublish:ship.awaitingTitle')
        : t('gitpublish:outcome.toast', {
            state: t(`gitpublish:states.${answerOf(answer)}`),
          }),
    successIntent: (answer) => (isHeld(answer) ? 'warning' : 'success'),
    // Refusals are said in the panel; the hook still owns the step-up ceremony.
    onError: () => true,
  })
  // After an act, focus moves to what it answered.
  const outcome = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (publish.isSuccess || publish.isError) outcome.current?.focus()
  }, [publish.isSuccess, publish.isError, publish.submittedAt])
  // A push or pull request the approval policy holds is said as a held ship is. Where this
  // person may open the approval, the panel follows it: the act opens again once it is
  // decided, and the panel says the decision. Elsewhere the panel cannot know, so the act
  // stays pressable and sends the same operation again.
  const answer = publish.data
  const held = isHeld(answer) ? answer : undefined
  const approvalId = held?.approval_id ?? ''
  const approvals = viewById(APPROVALS_SECTION.view)
  const followed =
    !!approvalId && !!approvals && navigable(approvals, APPROVALS_SECTION)
  const approval = useQuery({
    queryKey: governanceKeys.approval(activeTenant, approvalId),
    queryFn: () => governanceApi.getApproval(approvalId),
    enabled: followed,
    // As the conversation's approval card: a decision made elsewhere shows by itself.
    refetchInterval: (query) =>
      query.state.data?.status === 'pending' ? 12_000 : false,
  })
  // The engine does not send a held act by itself once it is approved: it goes out when
  // it is sent again. A refusal, or an approval that ended undecided, sends nothing.
  const decided = followed ? approval.data?.status : undefined
  const phase =
    !decided || decided === 'pending'
      ? 'waiting'
      : decided === 'approved'
        ? 'approved'
        : decided === 'rejected'
          ? 'rejected'
          : 'ended'
  const awaiting = followed && !approval.isError && phase === 'waiting'
  const heldBy = (effect: PublishAct['effect']) =>
    awaiting && publish.variables?.effect === effect

  if (!moduleOn || !workspace || !head) return null
  if (!can('gitpublish:target:read'))
    return (
      <p className="text-caption text-muted-foreground">
        {t('git.cannotSeeRemote')}
      </p>
    )
  if (targets.isError)
    return (
      <QueryErrorState
        error={targets.error}
        subject={t('gitpublish:targets.title')}
        module="gitpublish"
        retry={() => void targets.refetch()}
      />
    )
  if (!targets.data) return null
  const view = viewById('gitPublication')
  // A link takes the size and ink of the line it ends: AppLink draws neither.
  const door =
    view && navigable(view) ? (
      <span className="text-caption text-text">
        <AppLink href={view.path} className="underline">
          {t('git.publishingDoor')}
        </AppLink>
      </span>
    ) : undefined
  if (!target)
    return (
      <div className="flex flex-wrap items-baseline gap-x-2 text-caption text-muted-foreground">
        <p>{t('git.noRemote')}</p>
        {door}
      </div>
    )

  const branch = status.branch
  const pushBranch = branch.startsWith(target.push_prefix)
    ? branch
    : target.push_prefix + branch
  const ref = `refs/heads/${pushBranch}`
  const known = [
    ...(intents.data?.items ?? []),
    ...(answer && !isHeld(answer) ? [answer] : []),
  ]
  const pushes = known.filter(
    (i) => i.effect === 'push' && i.requested.ref === ref,
  )
  const ofHead = (i: PublicationIntent) => i.requested.commit === head.commit
  const landed = pushes.filter((i) => LANDED.includes(answerOf(i)))
  const pushed = landed.find(ofHead)
  const pushWaits = pushes.some(
    (i) => ofHead(i) && UNSETTLED.includes(answerOf(i)),
  )
  // The push is leased on the commit the remote was last seen holding for the branch.
  const seen = (i: PublicationIntent) => i.observed.at ?? ''
  const tip = landed.sort((a, b) => seen(b).localeCompare(seen(a))).at(0)
  const lease = tip?.requested.commit ?? ''
  const pulls = known.filter(
    (i) => i.effect === 'pull_request' && i.requested.head_ref === pushBranch,
  )
  const pullWaits = pulls.some((i) => UNSETTLED.includes(answerOf(i)))
  // A pull request merged here, or reported merged by the host, is a past fact: it is not
  // the branch's open one, and it does not hide a new one for a newer commit.
  const mergedHere = known
    .filter((i) => i.effect === 'merge' && LANDED.includes(answerOf(i)))
    .map((i) => i.requested.number)
  const isMerged = (i: PublicationIntent) =>
    i.observed.merged ||
    (!!i.observed.number && mergedHere.includes(i.observed.number))
  const opened = pulls.filter((i) => LANDED.includes(answerOf(i)))
  const pull = opened.find((i) => i.observed.present && !isMerged(i))
  const merged = opened.find(isMerged)
  const mergedHead =
    !!merged &&
    [merged.requested.commit, merged.observed.head_sha].includes(head.commit)
  const pullTitle = title ?? (subject || run.name || '')
  const noTitle = !pullTitle.trim()
  const pullBase = base ?? target.merge_bases[0] ?? ''
  const url = safePullRequestUrl(pull?.observed.url)
  const number = pull?.observed.number
  // Said as the act the panel saw done: the engine does not read the pull
  // request again, so the panel never claims it is still open.
  const pullLabel = number
    ? t('git.pullOpened', { number })
    : t('gitpublish:ship.pullRequestOpen')
  const remote =
    target.host === 'github'
      ? t('git.remoteGithub')
      : target.host === 'gitlab'
        ? t('git.remoteGitlab')
        : t('git.remoteGit')
  // Push says where it goes: to the repository, when the engine names it; else, with
  // several targets in the workspace, by the host and prefix the publish block shows.
  const named = {
    repository: target.repository,
    remote,
    prefix: target.push_prefix,
  }
  const forge = target.host === 'github' || target.host === 'gitlab'
  const where = target.repository
    ? t(forge ? 'git.pushRepositoryOn' : 'git.pushRepository', named)
    : bound.length > 1
      ? t('git.pushTarget', named)
      : undefined
  const waiting = t('gitpublish:outcome.uncertain.body')
  const pushReason = !branch
    ? t('git.pushNoBranch')
    : pushWaits
      ? waiting
      : heldBy('push')
        ? t('git.awaitingReason', { action: t('git.push') })
        : undefined
  const pullReason = pullWaits
    ? waiting
    : heldBy('pull_request')
      ? t('git.awaitingReason', { action: t('git.openPullRequest') })
      : undefined
  // The last act, named by its control, and what the panel read of its approval.
  const lastAct = {
    id: approvalId,
    action:
      publish.variables?.effect === 'pull_request'
        ? t('git.openPullRequest')
        : t('git.push'),
  }
  const verdict = phase === 'waiting' ? null : t(DECIDED[phase], lastAct)
  const approvalDoor = followed ? (
    <span className="text-caption text-text">
      <AppLink
        href={stepHref(APPROVALS_SECTION, { approval: approvalId })}
        className="underline"
      >
        {t('git.openApproval')}
      </AppLink>
    </span>
  ) : undefined
  const working = (effect: PublishAct['effect']) =>
    publish.isPending && publish.variables?.effect === effect
  // A failed act in the module's own words: what happened, then what to do.
  const failure = failureOf(
    publish.error instanceof PanelRefusal
      ? publish.error.refusal
      : publish.error,
  )
  const refused = failure?.kind === 'refused' ? failure.code : null
  // A branch moved or created outside this panel holds a commit the lease never saw.
  const stale = refused === 'stale_lease'
  const said = refused
    ? t(PANEL_WORDS[refused] ?? `gitpublish:${refusalKey(refused)}`, lastAct)
    : null
  const refusal = refused
    ? `${said} (${refused})`
    : failure?.kind === 'rejected'
      ? `${t('gitpublish:outcome.rejected.title')}. ${t('gitpublish:outcome.rejected.body')}`
      : failure?.kind === 'unknown'
        ? `${t('gitpublish:outcome.unknown.title')}. ${t('git.sendAgain')}`
        : null

  const push = () => {
    publish.mutate({
      target: target.id,
      effect: 'push',
      body: {
        operation_id: operationFor([target.id, ref, lease, head.commit]),
        ref,
        expected_old: lease,
        commit: head.commit,
        tree: head.tree,
        acknowledge_intent: '',
        session_run: run.run_ref,
      },
    })
  }
  const openPull = () => {
    const words = [pullTitle, body, pullBase, String(draft)]
    const decision = [target.id, pushBranch, head.commit, ...words]
    publish.mutate({
      target: target.id,
      effect: 'pull_request',
      body: {
        operation_id: operationFor(decision),
        head_ref: pushBranch,
        base: pullBase,
        commit: head.commit,
        title: pullTitle,
        body,
        draft,
        acknowledge_intent: '',
      },
    })
  }

  return (
    <div ref={outcome} tabIndex={-1} className="space-y-2 outline-none">
      <div role="status" className="space-y-1 text-caption text-text-2">
        {pushed ? (
          <p>
            <Trans
              t={t}
              i18nKey="git.pushed"
              values={{ remote, commit: head.commit.slice(0, 7) }}
              components={{
                branch: <PathText value={pushBranch} copy={false} />,
              }}
            />
          </p>
        ) : null}
        {pull && url ? (
          <a
            href={url}
            target="_blank"
            rel="noopener noreferrer"
            className="font-medium text-text underline"
          >
            {pullLabel}
          </a>
        ) : pull ? (
          <p>{pullLabel}</p>
        ) : merged?.observed.number ? (
          <p>{t('git.pullMerged', { number: merged.observed.number })}</p>
        ) : null}
      </div>
      {held && verdict ? (
        <div className="flex flex-wrap items-baseline gap-x-2">
          <p
            role={phase === 'approved' ? 'status' : 'alert'}
            className={
              phase === 'approved'
                ? 'text-caption text-text-2'
                : 'text-caption text-destructive'
            }
          >
            {verdict}
          </p>
          {approvalDoor}
        </div>
      ) : held ? (
        <ShipHeld action={approvalDoor}>{t('git.awaiting', lastAct)}</ShipHeld>
      ) : null}
      {refusal ? (
        <div className="flex flex-wrap items-baseline gap-x-2">
          <p role="alert" className="text-caption text-destructive">
            {refusal}
          </p>
          {stale ? door : null}
        </div>
      ) : null}
      {!can(actionAuthority('push').permission) ? (
        <p className="text-caption text-muted-foreground">
          {t('git.cannotPush')}
        </p>
      ) : !pushed ? (
        <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          <DisabledReason
            mode="native"
            disabled={!!pushReason}
            reason={pushReason ?? t('git.push')}
            action={pushWaits ? door : undefined}
          >
            <Button
              size="sm"
              variant={uncommitted ? 'secondary' : 'primary'}
              disabled={publish.isPending}
              aria-describedby={where ? `${id}-where` : undefined}
              onClick={push}
            >
              {working('push') ? t('git.working') : t('git.push')}
            </Button>
          </DisabledReason>
          {where ? (
            <p
              id={`${id}-where`}
              className="text-caption text-muted-foreground"
            >
              {where}
            </p>
          ) : null}
        </div>
      ) : null}
      {pushed && !pull && !mergedHead && hasChangeAPI(target) ? (
        can(actionAuthority('pull_request').permission) ? (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault()
              if (noTitle) {
                setTitleAsked(true)
                titleField.current?.focus()
                return
              }
              if (!pullReason && !publish.isPending) openPull()
            }}
          >
            <Field
              label={t('gitpublish:fields.title')}
              required
              error={titleAsked && noTitle ? t('git.noTitle') : undefined}
            >
              <Input
                ref={titleField}
                value={pullTitle}
                maxLength={256}
                onChange={(e) => setTitle(e.target.value)}
              />
            </Field>
            <Field label={t('gitpublish:fields.body')}>
              <Textarea
                value={body}
                maxLength={65536}
                rows={3}
                onChange={(e) => setBody(e.target.value)}
              />
            </Field>
            <Field label={t('gitpublish:fields.base')} required>
              {(field) => (
                <Select value={pullBase} onValueChange={setBase}>
                  <SelectTrigger
                    id={field.id}
                    aria-labelledby={field['aria-labelledby']}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {target.merge_bases.map((name) => (
                      <SelectItem key={name} value={name}>
                        {name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
            <div className="flex items-center gap-2">
              <Checkbox
                id={`${id}-draft`}
                checked={draft}
                onCheckedChange={(checked) => setDraft(checked === true)}
              />
              <Label htmlFor={`${id}-draft`}>
                {t('gitpublish:fields.draft')}
              </Label>
            </div>
            <DisabledReason
              mode="native"
              disabled={!!pullReason}
              reason={pullReason ?? t('git.openPullRequest')}
              action={pullWaits ? door : undefined}
            >
              <Button
                size="sm"
                type="submit"
                variant={uncommitted ? 'secondary' : 'primary'}
                className="self-start"
                disabled={publish.isPending}
              >
                {working('pull_request')
                  ? t('git.working')
                  : t('git.openPullRequest')}
              </Button>
            </DisabledReason>
          </form>
        ) : (
          <p className="text-caption text-muted-foreground">
            {t('git.cannotOpenPullRequest')}
          </p>
        )
      ) : null}
    </div>
  )
}
