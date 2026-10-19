// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Whoami } from '@/lib/api/types'

const PREFIX = 'olivares.draft.v1.'
const MAX_AGE_MS = 24 * 60 * 60 * 1000
const changes = new EventTarget()
/** Drafts this tab could not persist, newest per address, for this tab's own lifetime. */
const refused = new Map<string, { text: string; updatedAt: number }>()

/** Same-tab writes, including completion of a send from an unmounted composer. */
export function subscribeSessionDrafts(listener: () => void): () => void {
  changes.addEventListener('change', listener)
  return () => changes.removeEventListener('change', listener)
}

/** Non-secret identity facts only; a renewal must not change the draft's address. */
export function sessionDraftKey(
  tenant: string | null,
  principal: Pick<Whoami, 'kind' | 'actor' | 'user_id'> | null,
  session: string,
): string | null {
  if (
    !tenant ||
    !principal?.kind ||
    !principal.actor ||
    !principal.user_id ||
    !session
  )
    return null
  return (
    PREFIX +
    JSON.stringify([
      tenant,
      principal.kind,
      principal.actor,
      principal.user_id,
      session,
    ])
  )
}

/** The saved revision, kept in this browser only, for a send's completion check. */
export function sessionDraftSnapshot(key: string | null): string | null {
  if (!key) return null
  try {
    return sessionStorage.getItem(key)
  } catch {
    return null
  }
}

export function readSessionDraft(key: string | null): string {
  if (!key) return ''
  // A write the tab could not persist is newer than anything in storage.
  const unsaved = refused.get(key)
  if (unsaved) {
    if (Date.now() - unsaved.updatedAt <= MAX_AGE_MS) return unsaved.text
    refused.delete(key)
  }
  try {
    const raw = sessionDraftSnapshot(key)
    if (raw === null) return ''
    const draft: unknown = JSON.parse(raw)
    if (
      draft !== null &&
      typeof draft === 'object' &&
      'text' in draft &&
      typeof draft.text === 'string' &&
      'updatedAt' in draft &&
      typeof draft.updatedAt === 'number' &&
      Number.isFinite(draft.updatedAt) &&
      draft.updatedAt <= Date.now() &&
      Date.now() - draft.updatedAt <= MAX_AGE_MS
    )
      return draft.text
    sessionStorage.removeItem(key)
  } catch {
    // Invalid or unavailable tab storage is not a draft. Never log its contents.
    try {
      sessionStorage.removeItem(key)
    } catch {
      // The browser may also deny removal of an unreadable entry.
    }
  }
  return ''
}

/** Whether the newest draft of this address never reached the tab's storage. */
export function sessionDraftRefused(key: string | null): boolean {
  return key !== null && refused.has(key)
}

export function writeSessionDraft(key: string | null, text: string): boolean {
  if (!key) return true
  const entry = { text, updatedAt: Date.now() }
  try {
    if (text) sessionStorage.setItem(key, JSON.stringify(entry))
    else sessionStorage.removeItem(key)
    refused.delete(key)
    changes.dispatchEvent(new Event('change'))
    return true
  } catch {
    // The tab cannot persist it: keep the newest text for this tab's own life,
    // so a session switch does not drop it, and say so at the composer.
    refused.set(key, entry)
    changes.dispatchEvent(new Event('change'))
    return false
  }
}

export function clearSessionDrafts(): void {
  refused.clear()
  let keys: string[] = []
  try {
    // A snapshot first: removing an item shifts the remaining storage keys.
    keys = Object.keys(sessionStorage).filter((key) => key.startsWith(PREFIX))
  } catch {
    // Storage access must not prevent revoking the signed-in session.
  }
  for (const key of keys) {
    try {
      sessionStorage.removeItem(key)
    } catch {
      // One locked entry does not keep the other drafts signed in.
    }
  }
  changes.dispatchEvent(new Event('change'))
}
