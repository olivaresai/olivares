// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { StaticTable } from '@/components/data/static-table'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { APPROVALS_SECTION } from '@/components/layout/shell-sections'
import { AppLink } from '@/components/ui/app-link'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { PathText } from '@/components/ui/path-text'
import { Segmented } from '@/components/ui/segmented'
import { StateBlock } from '@/components/ui/state-block'
import { agentOpsKeys } from '@/features/agentops/api'
import type { RunDTO } from '@/features/agentops/types'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import { stepHref } from '@/features/navigation/journeys'
import { parseTs } from '@/features/shared/format'
import '@/features/sessions/i18n'
import type { ListResponse } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { useModuleOn } from '@/stores/modules'
import { gitpublishApi, gitpublishKeys } from './api'
import {
  approvalTarget,
  safePullRequestUrl,
  sourceSession,
  targetName,
} from './model'
import type {
  HeldApproval,
  PublicationIntent,
  PublicationTarget,
} from './types'

const LANDED: readonly string[] = ['applied', 'adopted']

/** The actions a publication waits on another person for, what each publishes in the
 * Push panel's words (a ship's or a session's push, and a session's pull request), and the
 * name of its approval link. The engine records no intent for one until it goes out. */
const HELD_ACTS: Record<string, { word: string; link: string }> = {
  'gitpublish.push': {
    word: 'sessions:git.push',
    link: 'publications.heldPush',
  },
  'gitpublish.pull_request': {
    word: 'sessions:git.openPullRequest',
    link: 'publications.heldPullRequest',
  },
}
/** Only what the list shows: what waits for a person, and what was approved and is not
 * used yet. */
const HELD_STATES = ['pending', 'approved']
const HELD = { action: Object.keys(HELD_ACTS), status: HELD_STATES }
/** The engine's largest page, and the pages read per action and state. The route lists
 * oldest first and cannot leave out what was spent, so past them the list says it read
 * only the oldest. */
const HELD_PAGE = 1000
const HELD_PAGES = 5

/** One held action's approvals in one state, page after page, up to the cap. */
async function heldApprovals(
  action: string,
  status: string,
): Promise<ListResponse<HeldApproval>> {
  const items: HeldApproval[] = []
  let cursor: string | undefined
  for (let page = 0; page < HELD_PAGES; page++) {
    const read = await governanceApi.listApprovals({
      action,
      status,
      limit: HELD_PAGE,
      cursor,
    })
    items.push(...read.items)
    if (!read.has_more) return { items, has_more: false }
    if (!read.cursor || read.cursor === cursor)
      throw new Error('The approvals list did not advance')
    cursor = read.cursor
  }
  return { items, has_more: true }
}

/** One pull request with the pushes of its branch, or the pushes of a branch with none, as
 * their newest intents saw them; or one publication that waits for an approval. */
type Publication = {
  key: string
  target: string
  at: string
  branch?: string
  pull?: PublicationIntent
  merged: boolean
  session?: string
  approved: boolean
  held?: HeldApproval
}

const when = (i: PublicationIntent) => i.observed.at ?? i.acknowledged.at ?? ''

function branchOf(i: PublicationIntent): string | undefined {
  if (i.effect === 'push') return i.requested.ref?.replace(/^refs\/heads\//, '')
  if (i.effect === 'pull_request' && i.observed.number)
    return i.requested.head_ref
  return undefined
}

function publicationsOf(
  intents: PublicationIntent[],
  approvals: HeldApproval[],
): Publication[] {
  const rows = new Map<string, Publication>()
  // The key of the newest row of each branch on a target.
  const lanes = new Map<string, string>()
  // Oldest first, so each row ends as its newest intent saw it.
  const landed = intents
    .filter((i) => LANDED.includes(i.state))
    .sort((a, b) => when(a).localeCompare(when(b)))
  for (const i of landed) {
    if (i.effect === 'merge') {
      const row = rows.get(`${i.target_id}#${i.requested.number}`)
      if (row) rows.set(row.key, { ...row, merged: true })
      continue
    }
    const branch = branchOf(i)
    if (!branch) continue
    const lane = `${i.target_id}\n${branch}`
    const number = i.effect === 'pull_request' ? i.observed.number : undefined
    const pull = number ? `${i.target_id}#${number}` : undefined
    const last = rows.get(lanes.get(lane) ?? '')
    // A pull request's row holds the pushes of its branch before it, and after it until
    // it is merged; a push after that starts the branch's next row.
    const open = last && !(pull ? last.pull : last.merged) ? last : undefined
    const row = (pull ? rows.get(pull) : undefined) ?? open
    const key = pull ?? row?.key ?? lane
    if (row && row.key !== key) rows.delete(row.key)
    lanes.set(lane, key)
    rows.set(key, {
      key,
      target: i.target_id,
      at: when(i),
      branch,
      pull: pull ? i : row?.pull,
      merged: (!!pull && i.observed.merged) || !!row?.merged,
      session: sourceSession(i) ?? row?.session,
      approved: !!i.proposal?.approval_id || !!row?.approved,
    })
  }
  // A pull request is merged when a read saw it merged, or a merge made here landed.
  const merged = (target: string, number?: number) =>
    landed.some(
      (i) =>
        i.target_id === target &&
        (i.effect === 'merge'
          ? i.requested.number === number
          : i.observed.number === number && i.observed.merged),
    )
  // An approval spent is not waiting, whether what it allowed landed or was refused;
  // nor is one past its expiry, which the engine no longer spends.
  const spent = new Set(intents.map((i) => i.proposal?.approval_id))
  const now = Date.now()
  const held = approvals
    .filter(
      (a) =>
        a.subject_ref &&
        !a.consumed &&
        !spent.has(a.id) &&
        (parseTs(a.expires_at) ?? Infinity) > now,
    )
    .map((a) => ({
      key: a.id,
      target: approvalTarget(a.subject_ref!),
      at: a.created_at ?? '',
      merged: false,
      approved: false,
      held: a,
    }))
  return [...rows.values()]
    .map((row) =>
      row.pull
        ? { ...row, merged: merged(row.target, row.pull.observed.number) }
        : row,
    )
    .concat(held)
    .sort((a, b) => b.at.localeCompare(a.at))
}

/** What Olivares AI published on these targets, and what waits for another person's
 * approval, newest first: one row per pull request, with the pushes of its branch, and one
 * per branch pushed with none, each with its approval and the session it came from. A pull
 * request's state is what was seen when it was opened or merged here: the engine does not
 * read it again, and its link shows today's. */
export function Publications({ targets }: { targets: PublicationTarget[] }) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const [show, setShow] = useState<'all' | 'pull'>('all')
  const intents = useQueries({
    queries: targets.map((tg) => ({
      queryKey: gitpublishKeys.intents(activeTenant, tg.id),
      queryFn: () => gitpublishApi.intents(tg.id),
    })),
  })
  const approvalsOn = useModuleOn('governance')
  const mayReadApprovals = can(APPROVALS_SECTION.requires)
  const readsApprovals = approvalsOn && mayReadApprovals
  const approvals = useQuery({
    queryKey: governanceKeys.approvals(activeTenant, HELD),
    queryFn: async (): Promise<ListResponse<HeldApproval>> => {
      const reads = await Promise.all(
        HELD.action.flatMap((action) =>
          HELD.status.map((status) => heldApprovals(action, status)),
        ),
      )
      // An approval decided between the reads comes back from both: it is listed
      // once, as decided, which is the newer.
      const byId = new Map<string, HeldApproval>()
      for (const a of reads.flatMap((read) => read.items))
        if (a.status !== 'pending' || !byId.has(a.id)) byId.set(a.id, a)
      return {
        items: [...byId.values()],
        has_more: reads.some((read) => read.has_more),
      }
    },
    enabled: readsApprovals,
  })
  const reads = [...intents, ...(readsApprovals ? [approvals] : [])]
  // What was read is listed; each read that failed says so, with its own Retry.
  const shown = publicationsOf(
    intents.flatMap((r) => r.data?.items ?? []),
    readsApprovals ? (approvals.data?.items ?? []) : [],
  ).filter(
    (row) =>
      targets.some((tg) => tg.id === row.target) &&
      (show === 'all' ||
        row.pull ||
        row.held?.action === 'gitpublish.pull_request'),
  )
  const none = show === 'all' ? 'publications' : 'pullRequests'

  return (
    <SectionCard
      title={t('publications.title')}
      description={t('publications.description')}
      actions={
        <Segmented
          size="sm"
          aria-label={t('publications.show')}
          value={show}
          onValueChange={setShow}
          options={[
            { value: 'all', label: t('publications.all') },
            { value: 'pull', label: t('pullRequests.title') },
          ]}
        />
      }
      noPadding
    >
      <ListTruncationBadge
        query={approvals}
        filas={approvals.data?.items.length}
        label={t('publications.cut', { n: approvals.data?.items.length })}
      />
      {reads.some((r) => r.isPending) ? (
        <StateBlock state="loading" />
      ) : (
        <>
          {targets.map((tg, n) => {
            const read = intents[n]
            if (!read.isError) return null
            const name = targetName(tg, t)
            return (
              <QueryErrorState
                key={tg.id}
                error={read.error}
                subject={name}
                title={t('publications.readFailed', { target: name })}
                retry={() => void read.refetch()}
              />
            )
          })}
          {readsApprovals && approvals.isError ? (
            <QueryErrorState
              error={approvals.error}
              title={t('publications.heldReadFailed')}
              retry={() => void approvals.refetch()}
            />
          ) : null}
          {shown.length > 0 ? (
            <div className="overflow-x-auto">
              <StaticTable>
                <caption className="sr-only">{t('publications.title')}</caption>
                <thead>
                  <tr>
                    <th scope="col">{t('columns.target')}</th>
                    <th scope="col">{t('pullRequests.branch')}</th>
                    <th scope="col">{t('pullRequests.pullRequest')}</th>
                    <th scope="col">{t('publications.approval')}</th>
                    <th scope="col">{t('publications.session')}</th>
                  </tr>
                </thead>
                <tbody>
                  {shown.map((row) => (
                    <PublicationRow
                      key={row.key}
                      row={row}
                      target={targets.find((tg) => tg.id === row.target)!}
                    />
                  ))}
                </tbody>
              </StaticTable>
            </div>
          ) : reads.some((r) => r.isError) ? null : (
            // A read that failed never says that nothing is here.
            <EmptyState
              size="panel"
              title={t(`${none}.empty`)}
              description={t(`${none}.emptyNext`)}
            />
          )}
        </>
      )}
      {approvalsOn && !mayReadApprovals ? (
        <p className="px-4 pb-3 text-caption text-muted-foreground">
          {t('publications.heldHidden')}
        </p>
      ) : null}
    </SectionCard>
  )
}

function PublicationRow({
  row,
  target,
}: {
  row: Publication
  target: PublicationTarget
}) {
  const { t } = useTranslation('gitpublish')
  const base = row.pull?.requested.base
  const name = targetName(target, t)
  const held = HELD_ACTS[row.held?.action ?? '']
  const state = t(
    row.held?.status === 'pending'
      ? 'ship.awaitingTitle'
      : 'publications.approvedUnused',
  )
  return (
    <tr>
      <td>{name}</td>
      <td>
        {row.branch ? (
          <span className="inline-flex min-w-0 max-w-full items-center gap-1">
            <PathText value={row.branch} copy={false} size="small" />
            {base ? (
              <>
                {' → '}
                <PathText value={base} copy={false} size="small" />
              </>
            ) : null}
          </span>
        ) : null}
      </td>
      <td>
        {row.pull ? <PullCell pull={row.pull} merged={row.merged} /> : null}
      </td>
      <td>
        {row.held && held ? (
          <span className="flex flex-wrap items-center gap-2">
            <Badge variant="outline">{t(held.word)}</Badge>
            <AppLink
              href={stepHref(APPROVALS_SECTION, { approval: row.held.id })}
              aria-label={t(held.link, { state, target: name })}
              className="underline"
            >
              {state}
            </AppLink>
          </span>
        ) : row.approved ? (
          t('publications.approved')
        ) : (
          t('publications.notAsked')
        )}
      </td>
      <td>
        {row.session ? (
          <SessionCell run={row.session} branch={row.branch} target={name} />
        ) : null}
      </td>
    </tr>
  )
}

function PullCell({
  pull,
  merged,
}: {
  pull: PublicationIntent
  merged: boolean
}) {
  const { t } = useTranslation('gitpublish')
  const { number } = pull.observed
  const { title } = pull.requested
  const name = title
    ? t('pullRequests.link', { number, title })
    : t('ship.pullRequest', { number })
  const url = safePullRequestUrl(pull.observed.url)
  return (
    <span className="flex flex-wrap items-center gap-2">
      {url ? (
        <a
          href={url}
          target="_blank"
          rel="noopener noreferrer"
          className="font-medium text-primary underline"
        >
          {name}
        </a>
      ) : (
        <span>{name}</span>
      )}
      <Badge variant="outline">
        {merged ? t('pullRequests.merged') : t('pullRequests.opened')}
      </Badge>
    </span>
  )
}

/** The session a publication came from: a link where the person may open sessions, named
 * by what it published and titled with its name where the console already holds it (no
 * read per row), else plain words. Never its run id. */
function SessionCell({
  run,
  branch,
  target,
}: {
  run: string
  branch?: string
  target: string
}) {
  const { t } = useTranslation('gitpublish')
  const { activeTenant, can } = useAuth()
  const known = useQueryClient().getQueryData<RunDTO>(
    agentOpsKeys.run(activeTenant, run),
  )
  if (!can('sessions:run:read')) return <>{t('publications.aSession')}</>
  const session = new URLSearchParams({
    session: `run:${run}`,
    pane: 'narrative',
  })
  return (
    <AppLink
      href={`/sessions?${session}`}
      title={known?.name || known?.title || undefined}
      aria-label={t('publications.openSessionOf', { branch, target })}
      className="underline"
    >
      {t('publications.openSession')}
    </AppLink>
  )
}
