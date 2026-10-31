// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LEDGER PLACE'S MODEL: how a token reads, which entry needs a person, how entries
// group by session and fall into the histogram's buckets, and the two reads the place
// shares (the names a row paints, the chain verdict). No markup here: the components
// beside it draw what this answers.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { readableAction } from '@/components/layout/bell-events'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import {
  shortRef,
  useMemberNames,
  useSessionNames,
  useWorkspaceNames,
} from '@/features/shared'
import { auditApi } from '@/lib/api/endpoints'
import { queryKeys } from '@/lib/api/query'
import type { AuditEventDTO } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { eventLabel } from './event-label'
import type { Token } from './query'

/** A token as a person reads it: `person:Marc Vidal`, `since:7d`, or the bare words. */
export function tokenText(
  token: Pick<Token, 'key' | 'filter' | 'value'>,
  personName: (actor: string) => string | null,
): string {
  const value =
    token.filter === 'actor'
      ? (personName(token.value) ?? token.value)
      : token.value
  return token.key ? `${token.key}:${value}` : value
}

export const SESSION_KIND = 'sessions.run'
const APPROVAL_KIND = 'governance.approval'

/** The request entry that still waits for a person: the approval it opened is pending. */
export function needsPerson(
  e: AuditEventDTO,
  pending: ReadonlySet<string>,
): boolean {
  return (
    e.action === 'governance.approval.create' &&
    e.target_kind === APPROVAL_KIND &&
    !!e.target_id &&
    pending.has(e.target_id)
  )
}

/** The names a row paints: who acted and what it acted on, each from a directory the
 *  console already reads, or the ledger's own honest word for it. */
export function useEntryNames(entries: readonly AuditEventDTO[]) {
  const { t } = useTranslation('audit')
  const { activeTenant, can } = useAuth()
  const mayReadMembers = can('user:read')
  const mayReadPolicies = can('governance:policy:read')
  const members = useMemberNames()
  const workspaces = useWorkspaceNames()
  const sessions = useSessionNames()
  const policies = useQuery({
    queryKey: governanceKeys.policies(activeTenant),
    queryFn: () => governanceApi.listPolicies(),
    enabled:
      mayReadPolicies && entries.some((e) => e.target_kind === 'core.policy'),
    staleTime: 60_000,
  })
  const person = (actor: string) =>
    mayReadMembers ? members.nameOf(actor) : null
  const who = (e: AuditEventDTO) =>
    (e.actor_kind === 'user' ? person(e.actor) : null) ??
    (e.actor_kind === 'user'
      ? t('detail.actorMember')
      : t(`actorKind.${e.actor_kind}`, {
          defaultValue: readableAction(e.actor_kind || 'system'),
        }))
  const target = (e: AuditEventDTO): string | null => {
    if (!e.target_id && !e.target_kind) return null
    const named =
      e.target_kind === 'core.workspace'
        ? workspaces.nameOf(e.target_id)
        : e.target_kind === 'core.user'
          ? person(e.target_id ?? '')
          : e.target_kind === SESSION_KIND
            ? sessions.nameOf(e.target_id)
            : e.target_kind === 'core.policy' && mayReadPolicies
              ? (policies.data?.items.find((p) => p.id === e.target_id)?.name ??
                null)
              : null
    return (
      named ??
      (e.target_kind === SESSION_KIND && e.target_id
        ? t('ledger.session', { ref: shortRef(e.target_id) })
        : t(`targets.${e.target_kind?.replaceAll('.', '_')}`, {
            defaultValue: readableAction(e.target_kind || t('detail.target')),
          }))
    )
  }
  return {
    person,
    resolvePerson: mayReadMembers ? members.idOf : () => null,
    who,
    target,
    sentence: (e: AuditEventDTO) =>
      t('ledger.sentence', { what: eventLabel(e.action, t), who: who(e) }),
  }
}
export type EntryNames = ReturnType<typeof useEntryNames>

interface Group {
  key: string
  heading: string
  /** The session the group is, for the heading's own filter. */
  session?: string
  entries: AuditEventDTO[]
}

/** The groups in the order the list paints them: what needs a person, then each session
 *  (and the entries of no session) by its newest entry. Entries keep their order. */
export function groupEntries(
  entries: readonly AuditEventDTO[],
  pending: ReadonlySet<string>,
  bySession: boolean,
  heading: (key: string, e: AuditEventDTO) => string,
): Group[] {
  const groups = new Map<string, Group>()
  const add = (key: string, e: AuditEventDTO, session?: string) => {
    const g = groups.get(key) ?? {
      key,
      heading: heading(key, e),
      session,
      entries: [],
    }
    g.entries.push(e)
    groups.set(key, g)
  }
  for (const e of entries) if (needsPerson(e, pending)) add('needs', e)
  for (const e of entries) {
    if (needsPerson(e, pending)) continue
    if (!bySession) add('all', e)
    else if (e.target_kind === SESSION_KIND && e.target_id)
      add(`run:${e.target_id}`, e, e.target_id)
    else add('other', e)
  }
  return [...groups.values()]
}

const HOUR = 3_600_000
export const DAY = 24 * HOUR
const MAX_WEEKS = 200
type Unit = 'hour' | 'day' | 'week'

/** The start of the local bucket that holds `at`. */
function bucketStart(at: Date, unit: Unit): Date {
  const d = new Date(at)
  d.setMinutes(0, 0, 0)
  if (unit !== 'hour') d.setHours(0)
  if (unit === 'week') d.setDate(d.getDate() - ((d.getDay() + 6) % 7))
  return d
}

function nextBucket(start: Date, unit: Unit): Date {
  const d = new Date(start)
  if (unit === 'hour') d.setHours(d.getHours() + 1)
  else d.setDate(d.getDate() + (unit === 'day' ? 1 : 7))
  return d
}

export interface Bucket {
  start: Date
  end: Date
  count: number
}

/** The buckets of [from, to], oldest first, each with the loaded entries it holds. */
export function bucketsOf(
  entries: readonly AuditEventDTO[],
  from: Date,
  to: Date,
): { unit: Unit; buckets: Bucket[] } {
  // Bars before the oldest entry loaded say nothing; over years they would be thousands.
  const oldest = Math.min(
    ...entries.map((e) => new Date(e.occurred_at).getTime()),
  )
  if (
    to.getTime() - from.getTime() > 120 * DAY &&
    Number.isFinite(oldest) &&
    oldest > from.getTime()
  )
    from = new Date(oldest)
  const span = to.getTime() - from.getTime()
  const unit: Unit =
    span <= 7 * DAY ? 'hour' : span <= 120 * DAY ? 'day' : 'week'
  // Still centuries wide (since:0001-01-01 before anything loads): the newest weeks only,
  // so a typed bound or a shared link never draws a hundred thousand buttons.
  if (unit === 'week' && span > MAX_WEEKS * 7 * DAY)
    from = new Date(to.getTime() - (MAX_WEEKS - 1) * 7 * DAY)
  const buckets: Bucket[] = []
  for (let s = bucketStart(from, unit); s < to; s = nextBucket(s, unit))
    buckets.push({ start: s, end: nextBucket(s, unit), count: 0 })
  for (const e of entries) {
    const at = new Date(e.occurred_at).getTime()
    const b = buckets.find(
      (x) => at >= x.start.getTime() && at < x.end.getTime(),
    )
    if (b) b.count += 1
  }
  return { unit, buckets }
}

/** The engine's chain verdict, read when a person asks for it and shared by every
 *  Verify chain button on the place. */
export function useVerifyChain() {
  const { activeTenant } = useAuth()
  return useQuery({
    queryKey: queryKeys.audit.verify(activeTenant),
    queryFn: () => auditApi.verify(),
    enabled: false,
  })
}
