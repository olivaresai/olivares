// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The bell describes events as sentences: "Audit checkpoint created",
// "{actor} started a session" — never a raw action id, a target id or "system: system". Reads,
// sign-ins and sign-outs are routine and not shown. An action without a sentence is a record,
// not a person's action: the bell shows it, as its name made readable, only on request.
import type { AuditEventDTO } from '@/lib/api/types'
import { preferenceStorage } from '@/lib/preference-storage'

/** Routine events a person does not wait for: never in the bell. */
export function hiddenInBell(action: string): boolean {
  return (
    action.endsWith('.read') ||
    ROUTINE.has(action) ||
    action.startsWith('test.')
  )
}

/** Bookkeeping a person does not wait for: sign-out and a session's own steps
 * (created, attached, streamed, the engine's "stopped" after the person's "stopping"). */
const ROUTINE: ReadonlySet<string> = new Set([
  'auth.logout',
  'auth.refresh',
  'sessions.run.created',
  'sessions.run.attach',
  'sessions.run.stopped',
  'sessions.run.resumed',
  'sessions.run.interrupting',
  'sessions.stream.open',
  'recording.session.open',
  'recording.session.close',
])

/** Supporting engine records stay available without crowding out people's actions:
 * authorization and policy evidence (*.record), reads, and a session being viewed. Audit
 * folds this set; the bell folds every action without a sentence, which includes it. */
export function internalRecord(action: string): boolean {
  return (
    action.endsWith('.record') ||
    action.endsWith('.read') ||
    action === 'sessions.run.attach' ||
    action.endsWith('.stream.open')
  )
}

/** The sentence key (common:notifications.events.<key>) for an action, if it has one. */
const SENTENCES: Readonly<Record<string, string>> = {
  'auth.login': 'signedIn',
  'sessions.work.item.create': 'workCreated',
  'sessions.work.item.ready': 'workReady',
  'sessions.work.item.complete': 'workCompleted',
  'sessions.work.item.block': 'workBlocked',
  'sessions.work.item.cancel': 'workCanceled',
  'governance.approval.decision': 'approvalDecided',
  'governance.approval.cancel': 'approvalCanceled',
  'governance.policy.create': 'policyCreated',
  'governance.policy.update': 'policyUpdated',
  'governance.policy.delete': 'policyDeleted',
  'audit.checkpoint': 'auditCheckpoint',
  'org.create': 'orgCreated',
  'auth.login.blocked': 'loginBlocked',
  'sso.login': 'ssoLogin',
  'user.invite': 'userInvited',
  'user.invite.accept': 'userJoined',
  'sessions.run.launched': 'sessionStarted',
  'sessions.run.stopping': 'sessionStopped',
  'sessions.run.resuming': 'sessionResumed',
  'sessions.run.failed': 'sessionFailed',
  'governance.approval.create': 'approvalRequested',
  'governance.killswitch.engage': 'killswitchEngaged',
  'governance.killswitch.reenable': 'killswitchReleased',
  'security.killswitch.deny': 'killswitchDenied',
  'agenttools.install.succeeded': 'toolInstalled',
  'secret.put': 'secretStored',
  'consoleviews.view.create': 'viewSaved',
  'workspace.create': 'workspaceCreated',
  'workspace.update': 'workspaceUpdated',
}

export function sentenceKey(action: string): string | null {
  return SENTENCES[action] ?? null
}

/** "mcp_gateway.server.put" -> "Mcp gateway server put": the fallback for an action
 * that has no sentence yet. */
export function readableAction(action: string): string {
  const words = action.replace(/[._]+/g, ' ').trim()
  return words ? words[0]!.toUpperCase() + words.slice(1) : action
}

/** Where a click on the event goes, when there is one place to act on it: the page's
 * view, so the bell offers it only where it opens, and its address. */
export function eventLink(
  event: Pick<AuditEventDTO, 'action' | 'target_id'>,
): { view: string; to: string; search?: Record<string, string> } | null {
  if (event.action === 'governance.approval.create' && event.target_id)
    return {
      view: 'permissions',
      to: '/permissions',
      search: { approval: event.target_id },
    }
  if (event.action.startsWith('sessions.run.'))
    return { view: 'sessions', to: '/sessions' }
  return null
}

export type ActorClass = 'you' | 'member' | 'token' | 'system'

/** Who did it, as a class, and the user id when it is a person. */
export function actorOf(
  event: Pick<AuditEventDTO, 'actor' | 'actor_kind'>,
  selfUserId: string | undefined,
): { kind: ActorClass; userId?: string } {
  const actor = event.actor ?? ''
  if (actor.startsWith('token:')) return { kind: 'token' }
  if (actor.startsWith('user:')) {
    const userId = actor.slice('user:'.length)
    return userId && userId === selfUserId
      ? { kind: 'you', userId }
      : { kind: 'member', userId }
  }
  return { kind: 'system' }
}

/** Where the person's read marks live: their server-kept preferences, so a mark follows
 * them to another browser. One mark per organization: the newest event they read there. */
const READ_KEY = 'olivares.notifications.lastSeen'

export function readMarks(): Record<string, string> {
  try {
    const stored: unknown = JSON.parse(
      preferenceStorage.getItem(READ_KEY) ?? '{}',
    )
    return stored && typeof stored === 'object' && !Array.isArray(stored)
      ? (stored as Record<string, string>)
      : {}
  } catch {
    // An unreadable mark forgets: the worst it can do is show events as unread once more.
    return {}
  }
}

export function saveMarks(marks: Record<string, string>): void {
  preferenceStorage.setItem(READ_KEY, JSON.stringify(marks))
}

/** Unread: newer than the mark, or no mark yet. A mark (or a time) the clock cannot read
 * compares as nothing, so the event stays unread rather than silently read. */
export function isUnread(
  occurredAt: string,
  mark: string | undefined,
): boolean {
  return mark === undefined || !(Date.parse(mark) >= Date.parse(occurredAt))
}
