// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Whoami } from '@/lib/api/types'

const PREFIX = 'olivares.draft.v1.'
/** The prompts sent to a session, for ArrowUp: another prefix, so no draft count sees them. */
const HISTORY_PREFIX = 'olivares.history.v1.'
/** Drafts set aside, under the same address (see `stashDraft`). */
const STASH_PREFIX = 'olivares.stash.v1.'
const PREFIXES = [PREFIX, HISTORY_PREFIX, STASH_PREFIX]
/** How many prompts a session remembers, and how many drafts it keeps set aside. */
const HISTORY_MAX = 20
const MAX_AGE_MS = 24 * 60 * 60 * 1000
const changes = new EventTarget()
/** How many drafts each address has set aside, counted when they change and not at
 * every render: one of them may hold half a megabyte. */
const counted = new Map<string, number>()
function stashesChanged(): void {
  counted.clear()
  changes.dispatchEvent(new Event('change'))
}
/** Drafts this tab could not persist, newest per address, for this tab's own lifetime. */
const refused = new Map<string, { text: string; updatedAt: number }>()

/** The one age rule of everything kept here: written at a real time, at most a day ago. */
function fresh(at: unknown): boolean {
  return (
    typeof at === 'number' &&
    Number.isFinite(at) &&
    at <= Date.now() &&
    Date.now() - at <= MAX_AGE_MS
  )
}

/** The tab's own storage and the one every tab of this browser shares; null where the
 * browser keeps none (a private window may refuse either). */
function tabStorage(): Storage | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}
function sharedStorage(): Storage | null {
  try {
    return window.localStorage
  } catch {
    return null
  }
}

/** Same-tab writes, including completion of a send from an unmounted composer, and a
 * draft another tab of this browser set aside or took back. */
export function subscribeSessionDrafts(listener: () => void): () => void {
  const fromAnotherTab = (event: StorageEvent) => {
    if (event.key !== null && !event.key.startsWith(STASH_PREFIX)) return
    counted.clear()
    listener()
  }
  // A count kept while nobody listened may be old: the first read counts again.
  counted.clear()
  changes.addEventListener('change', listener)
  window.addEventListener('storage', fromAnotherTab)
  return () => {
    changes.removeEventListener('change', listener)
    window.removeEventListener('storage', fromAnotherTab)
  }
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
      fresh(draft.updatedAt)
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

const historyKey = (draftKey: string) =>
  HISTORY_PREFIX + draftKey.slice(PREFIX.length)

/** The prompts sent to this session, newest first; kept in this tab's storage under the
 * draft's address and cleared with the drafts at sign-out. Recalling is a convenience:
 * storage that is unavailable or unreadable is no history. */
export function readPromptHistory(draftKey: string | null): string[] {
  if (!draftKey) return []
  try {
    const raw = sessionStorage.getItem(historyKey(draftKey))
    const list: unknown = raw ? JSON.parse(raw) : []
    return Array.isArray(list)
      ? list.filter((p): p is string => typeof p === 'string')
      : []
  } catch {
    return []
  }
}

/** Remembers a prompt that reached the session; the same words again move to the front. */
export function rememberPrompt(draftKey: string | null, text: string): void {
  const prompt = text.trim()
  if (!draftKey || !prompt) return
  const list = [
    prompt,
    ...readPromptHistory(draftKey).filter((p) => p !== prompt),
  ].slice(0, HISTORY_MAX)
  try {
    sessionStorage.setItem(historyKey(draftKey), JSON.stringify(list))
  } catch {
    // The tab cannot keep it; the prompt was sent all the same.
  }
}

/** A draft set aside with Ctrl+S: its words and the files attached to it, each by the
 * id the session stores it under and the name a person knows it by. */
export interface StashedDraft {
  text: string
  attachments: { id: string; name: string }[]
}
/** What setting a draft aside did: kept, with the first words of each older draft the
 * limits dropped to make room; or not kept, and why: the draft alone is over the limit,
 * the browser keeps no storage its tabs share, or it has no room left. Not kept, the
 * draft stays where it was and nothing set aside before is dropped. */
export type StashResult =
  | { ok: true; dropped: string[] }
  | { ok: false; why: 'too-large' | 'no-storage' | 'full' }

/** The bytes one session's drafts set aside, and all of one person's in one tenant
 * together, may take: room for a draft that carries a whole session pulled in as
 * context (256 KiB), and most of the 5 MiB a browser gives a site left to everything
 * else. */
export const STASH_MAX_BYTES = 512 * 1024
export const STASH_ALL_MAX_BYTES = 1024 * 1024

/*
 * Drafts set aside, each under its own name in the storage every tab of this browser
 * shares: the draft's address and the entry's id. Setting one aside only adds a name and
 * bringing one back only removes one, so no tab writes over what another set aside. A
 * reload and a second tab see them, a sign-out clears them, another person's sign-in
 * clears the last person's, and the console forgets those a day old as it loads. One
 * browser only: they never travel to another device. The files stay stored with the
 * session; only their ids are here.
 */
type Stored = StashedDraft & { setAt: number; seq: number }
type Entry = Stored & { name: string; size: number }
/** Where the names of one session's drafts set aside start: its address, open. */
const stashAddress = (draftKey: string) =>
  STASH_PREFIX + draftKey.slice(PREFIX.length, -1) + ','
/** Where the names of everything one person set aside in one tenant start: the
 * address without its session. */
function personAddress(draftKey: string): string {
  const person = JSON.parse(draftKey.slice(PREFIX.length)).slice(0, -1)
  return STASH_PREFIX + JSON.stringify(person).slice(0, -1) + ','
}
const bytes = (text: string) => new TextEncoder().encode(text).length
/** Orders two drafts this tab set aside within the same millisecond. */
let setAsideHere = 0
const older = (a: Stored, b: Stored) => a.setAt - b.setAt || a.seq - b.seq

function isStored(draft: unknown): draft is Stored {
  if (typeof draft !== 'object' || draft === null) return false
  const { text, attachments, setAt, seq } = draft as Record<string, unknown>
  return (
    typeof text === 'string' &&
    fresh(setAt) &&
    typeof seq === 'number' &&
    Array.isArray(attachments) &&
    attachments.every(
      (file) => typeof file?.id === 'string' && typeof file?.name === 'string',
    )
  )
}

/** The drafts set aside whose names start with `start`, oldest first, without what is
 * malformed or a day old; storage that cannot be listed holds nothing set aside. */
function entries(store: Storage, start: string): Entry[] {
  let names: string[]
  try {
    names = Object.keys(store).filter((name) => name.startsWith(start))
  } catch {
    return []
  }
  const found: Entry[] = []
  for (const name of names) {
    try {
      const raw = store.getItem(name)
      if (raw === null) continue
      const draft: unknown = JSON.parse(raw)
      if (isStored(draft))
        found.push({ ...draft, name, size: bytes(name + raw) })
    } catch {
      // An entry that cannot be read is not a draft set aside.
    }
  }
  return found.sort(older)
}

/** The first few words of a draft, to name it in a sentence; its files when it has none. */
function firstWords({ text, attachments }: StashedDraft): string {
  const all = text.trim()
  let said = ''
  for (const word of all.split(/\s+/).slice(0, 6)) {
    if (said && said.length + word.length >= 40) break
    said = said ? `${said} ${word}` : word.slice(0, 40)
  }
  if (!said) return attachments.map((file) => file.name).join(', ')
  return said.length < all.length ? `${said}…` : said
}

export function stashedDrafts(key: string | null): number {
  if (!key) return 0
  const known = counted.get(key)
  if (known !== undefined) return known
  const store = sharedStorage()
  const count = store ? entries(store, stashAddress(key)).length : 0
  counted.set(key, count)
  return count
}

/** Sets a draft aside, within the limits: the newest 20 of a session, its bytes and the
 * bytes of all this person's sessions in this tenant, the oldest dropped first and never
 * the one being set aside. Another person's or tenant's drafts are never read, counted
 * or named here: another person's go at the next sign-in, and any a day old as the
 * console loads. Nothing is removed unless the draft itself is written: a refusal
 * leaves everything as it was. */
export function stashDraft(
  key: string | null,
  draft: StashedDraft,
): StashResult {
  const store = sharedStorage()
  if (!key || !store) return { ok: false, why: 'no-storage' }
  const address = stashAddress(key)
  // An id no other tab picks at the same moment; the console may be served over
  // plain HTTP, where the browser offers no UUIDs.
  const id = Math.random().toString(36).slice(2, 12)
  const name = `${address}${JSON.stringify(id)}]`
  const entry: Stored = { ...draft, setAt: Date.now(), seq: setAsideHere++ }
  const value = JSON.stringify(entry)
  const size = bytes(name + value)
  if (size > STASH_MAX_BYTES) return { ok: false, why: 'too-large' }
  const all = entries(store, personAddress(key))
  const mine = all.filter((old) => old.name.startsWith(address))
  const dropped: Entry[] = []
  let mineSize = mine.reduce((sum, old) => sum + old.size, size)
  while (mine.length >= HISTORY_MAX || mineSize > STASH_MAX_BYTES) {
    const oldest = mine.shift()!
    mineSize -= oldest.size
    dropped.push(oldest)
  }
  const rest = all.filter((old) => !dropped.includes(old))
  let total = rest.reduce((sum, old) => sum + old.size, size)
  while (total > STASH_ALL_MAX_BYTES) {
    const oldest = rest.shift()!
    total -= oldest.size
    dropped.push(oldest)
  }
  try {
    store.setItem(name, value)
  } catch {
    return { ok: false, why: 'full' }
  }
  for (const old of dropped) {
    try {
      store.removeItem(old.name)
    } catch {
      // Only ever smaller than before: a refusal keeps an older draft a while longer.
    }
  }
  stashesChanged()
  return { ok: true, dropped: dropped.sort(older).map(firstWords) }
}

/** Takes back the newest draft set aside and removes its one entry: null when there is
 * none (another tab took it, or it aged). Two tabs that take one back at the same moment
 * can both find the same entry: at worst both take it, a duplicate, never a loss. */
export function unstashDraft(key: string | null): StashedDraft | null {
  const store = sharedStorage()
  if (!key || !store) return null
  const back = entries(store, stashAddress(key)).pop()
  if (!back) {
    // What was counted may have aged out since: count again.
    stashesChanged()
    return null
  }
  try {
    store.removeItem(back.name)
  } catch {
    // It stays set aside as well: twice is better than lost.
  }
  stashesChanged()
  return { text: back.text, attachments: back.attachments }
}

/** Removes the entries `doomed` names from both storages. Storage that cannot be listed,
 * or an entry that cannot be removed, never stops the rest. */
function sweep(doomed: (name: string, store: Storage) => boolean): void {
  for (const store of [tabStorage(), sharedStorage()]) {
    if (!store) continue
    let names: string[] = []
    try {
      // A snapshot first: removing an item shifts the remaining storage keys.
      names = Object.keys(store).filter((name) => doomed(name, store))
    } catch {
      // Storage access must not prevent revoking the signed-in session.
    }
    for (const name of names) {
      try {
        store.removeItem(name)
      } catch {
        // One locked entry does not keep the other drafts signed in.
      }
    }
  }
  stashesChanged()
}

const prefixOf = (name: string) => PREFIXES.find((p) => name.startsWith(p))

export function clearSessionDrafts(): void {
  refused.clear()
  sweep((name) => prefixOf(name) !== undefined)
}

/** Forgets every person's drafts set aside more than a day ago, which nobody can read
 * any more: the console does it as it loads, so none waits for its owner to come back. */
export function clearExpiredStashes(): void {
  sweep((name, store) => {
    if (!name.startsWith(STASH_PREFIX)) return false
    try {
      const draft: unknown = JSON.parse(store.getItem(name) ?? 'null')
      const setAt = (draft as { setAt?: unknown } | null)?.setAt
      return typeof setAt === 'number' && Date.now() - setAt > MAX_AGE_MS
    } catch {
      // Not a draft set aside, so not one this forgets.
      return false
    }
  })
}

/** Once a person is signed in on this tab, what anyone else left here goes: drafts,
 * histories and drafts set aside, in both storages. This person's own stay. */
export function keepSessionDraftsOf(
  principal: Pick<Whoami, 'kind' | 'actor' | 'user_id'>,
): void {
  const theirs = (name: string) => {
    const prefix = prefixOf(name)
    if (prefix === undefined) return false
    try {
      const [, kind, actor, user] = JSON.parse(name.slice(prefix.length))
      return (
        kind !== principal.kind ||
        actor !== principal.actor ||
        user !== principal.user_id
      )
    } catch {
      // An entry under these names that is nobody's address is nobody's draft.
      return true
    }
  }
  for (const key of refused.keys()) if (theirs(key)) refused.delete(key)
  sweep(theirs)
}
