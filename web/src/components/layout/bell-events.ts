// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE BELL TELLS EVENTS AS SENTENCES (Root copy, 2026-10-01): "Audit checkpoint created",
// "{actor} started a session" — never a raw action id, a target id or "system: system". Reads,
// sign-ins and sign-outs are routine and not shown. An action without a sentence is shown as
// its name made readable.
import type { AuditEventDTO } from '@/lib/api/types'

/** Routine events a person does not wait for: never in the bell. */
export function hiddenInBell(action: string): boolean {
  return (
    action.endsWith('.read') ||
    ROUTINE.has(action) ||
    action.startsWith('test.')
  )
}

/** Bookkeeping a person does not wait for: sign-in and out, and a session's own steps
 * (created, attached, streamed, the engine's "stopped" after the person's "stopping"). */
const ROUTINE: ReadonlySet<string> = new Set([
  'auth.login',
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

/** The sentence key (common:notifications.events.<key>) for an action, if it has one. */
const SENTENCES: Readonly<Record<string, string>> = {
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

/** Where a click on the event goes, when there is one place to act on it. */
export function eventLink(
  event: Pick<AuditEventDTO, 'action' | 'target_id'>,
): { to: string; search?: Record<string, string> } | null {
  if (event.action === 'governance.approval.create' && event.target_id)
    return { to: '/permissions', search: { approval: event.target_id } }
  if (event.action.startsWith('sessions.run.')) return { to: '/sessions' }
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
