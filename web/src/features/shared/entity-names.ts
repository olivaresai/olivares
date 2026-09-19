// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAME BEHIND A REFERENCE — one lookup, used by every screen that has to paint
// somebody else's identifier.
//
// ⛔ WHY IT IS ONE MODULE AND NOT A LOOKUP PER ROUTE. A browser census of the seeded
//    estate found nine routes painting a raw identifier where a name belongs, and they
//    are the SAME three questions asked by seven different screens: who is this user,
//    which workspace is this, what was this session doing. Answering them seven times
//    is seven caches, seven permission gates and seven ways to be wrong about a 403.
//
// ⛔ EVERY ANSWER IS A FACT THE ENGINE SENT. Nothing here composes a name. When the
//    directory read is not permitted, or the reference is not in the page the engine
//    returned, the answer is `null` and the caller paints its own honest fallback with
//    the reference beside it — never an invented label, never a silent blank.
//
// ⛔ AND THE READS ARE THE ONES THE CONSOLE ALREADY MAKES. The workspace list is the
//    switcher's own read under the switcher's own key, the member roster is the
//    people tab's, and the live-sessions page is the front door's: on a screen that
//    already made them this costs no request at all, and on one that did not it costs
//    a single cached one.
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { sessionNameLadder } from '@/features/home/work-line'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import type { LiveDTO } from '@/features/sessions/types'
import { useAuth } from '@/lib/auth/context'

/** The ceiling the generic repository accepts (`maxLimit`, sqlstore/generic.go:29). */
const DIRECTORY_PAGE = 1000
/** The live page the sessions plane serves: most-recent-first, one page. */
const SESSION_PAGE = 200

/**
 * THE TITLE OF A SESSION, or `null` when the engine sent nothing to make one from.
 *
 * ⛔ THE LADDER IS `sessionNameLadder`'s, CALLED AND NOT COPIED, and this function used
 *    to walk its own — which is how one session came to have four names. The last rung
 *    is mapped to `null` because the word for an untitled session belongs to the screen
 *    that paints it: an audit row and a rail row do not say it the same way.
 *
 * ⛔ AND THE RUN RUNG IS `null` HERE BECAUSE THERE IS NO RUN IN HAND, which is a fact
 *    about the read and not a shorter ladder. `useSessionNames` reads the live page
 *    (`GET /v1/m/sessions/live`); a run's operator-given name arrives with the runs, on
 *    a read this lookup does not make and cannot make for six screens without six more
 *    permission gates. Passing `null` says so at the seam instead of quietly omitting
 *    the rung — so a session named `deploy-api` at launch resolves here by its summary,
 *    its goal or its action, and by `null` when it has none of the three.
 */
export function sessionTitle(s: LiveDTO): string | null {
  const { text, from } = sessionNameLadder(null, s, '')
  return from === 'untitled' ? null : text
}

/** `user:01a0…` and `01a0…` are the same principal; the ledger writes the first. */
export function bareId(reference: string): string {
  const colon = reference.indexOf(':')
  return colon === -1 ? reference : reference.slice(colon + 1)
}

/**
 * THE SHORT FORM OF A REFERENCE, for the muted caption beside a fallback name.
 *
 * A uuid is cut at its first group — eight hex digits is enough to recognise a row and
 * to match it against a log line, and the whole value stays on `title` and in the
 * detail surface, where an identifier is the thing being operated on. A reference that
 * is already short and readable (`sess-coder-7a3f`) is left exactly as it is: cutting
 * it would destroy the only thing a reader could have searched for.
 */
export function shortRef(reference: string): string {
  const id = bareId(reference)
  const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
  return uuid.test(id) ? id.slice(0, 8) : id
}

/**
 * THE DISTINGUISHING TAIL OF A REFERENCE — what a chip under a LABEL paints, when the
 * label already states what kind of thing the reference names.
 *
 * ⛔ IT IS THE TAIL AND NOT THE HEAD, and that is measured rather than chosen. These ids
 *    are uuid v7: the first 48 bits are the MINTING TIMESTAMP, so every row of an
 *    inspector opened on one session shares them. Cut at the first group, the seeded
 *    estate's tenant, workspace, environment, profile, live and run references all read
 *    `01a0b6bb` — six rows, one string, nothing told apart. The last group is the random
 *    one; it is what distinguishes two rows and what matches a line in a log.
 *
 * ⛔ AND THE TYPE PREFIX GOES, BECAUSE THE LABEL BESIDE IT ALREADY SAYS IT. `xenv_` under
 *    a row labelled ENVIRONMENT is the word twice. This is the rule `shortSessionId`
 *    already applies on the front door — "the distinguishing tail of a session id, never
 *    the `sess-` type prefix" — generalised to the references that carry one.
 *
 * ⛔ NOTHING IS LOST: the WHOLE value stays on `title=`, in the chip's accessible name,
 *    and on the clipboard, which is what the chip exists for. A reference that is
 *    readable once its prefix is off (`anthropic-team`, `coder-7a3f`) is painted whole —
 *    cutting that would destroy the only part a reader could have searched for.
 */
export function refTail(reference: string): string {
  const id = bareId(reference)
  const typed = id.replace(/^(ppf|osn|xenv|wsp|run|sess)[_-]/i, '')
  const uuid =
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-([0-9a-f]{12})$/i.exec(
      typed,
    )
  return uuid ? `…${uuid[1]}` : typed
}

/** What every lookup here answers: a name, or `null` when it cannot be known. */
export interface NameLookup {
  /** The display name for a reference, or `null` — never a composed one. */
  nameOf: (reference: string | undefined | null) => string | null
  /** The read answered. `false` means "not yet", or "not permitted" — the caller
   *  paints the same fallback either way, and says nothing it cannot support. */
  ready: boolean
}

/**
 * USERS. The roster the people tab reads, gated on the same `user:read` it asks for.
 * The audit ledger and the recording sessions both name a principal as `user:<uuid>`,
 * so both forms resolve.
 */
export function useMemberNames(): NameLookup {
  const { activeTenant, can } = useAuth()
  const permitted = can('user:read')
  const { data, isSuccess } = useQuery({
    queryKey: consoleKeys.members(activeTenant),
    queryFn: () => consoleApi.listMembers(),
    enabled: permitted && !!activeTenant,
    staleTime: 60_000,
  })
  // ⛔ THE LOOKUP KEEPS ITS IDENTITY WHILE THE ANSWER DOES. A caller puts this object
  //    in the dependency list of the `useMemo` that builds its table columns, so a new
  //    object on every render rebuilds the columns on every render. It cost a real
  //    regression the first time it was written without this: the inventory table's
  //    tenant-lifetime test stopped settling.
  return useMemo(() => {
    const byId = new Map<string, string>()
    for (const m of data?.items ?? []) {
      const name = m.display_name?.trim() || m.email?.trim()
      if (m.user_id && name) byId.set(m.user_id, name)
    }
    return {
      ready: isSuccess,
      nameOf: (reference: string | undefined | null) =>
        reference ? (byId.get(bareId(reference)) ?? null) : null,
    }
  }, [data, isSuccess])
}

/** WORKSPACES. The switcher's own read, under the switcher's own key, so a screen that
 *  carries the switcher pays for this twice over exactly zero times. Both the id and
 *  the slug resolve: a scope reference carries either. */
export function useWorkspaceNames(): NameLookup {
  const { activeTenant } = useAuth()
  const { data, isSuccess } = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, { limit: DIRECTORY_PAGE }),
    queryFn: () => consoleApi.listWorkspaces({ limit: DIRECTORY_PAGE }),
    enabled: !!activeTenant,
    staleTime: 60_000,
  })
  return useMemo(() => {
    const byKey = new Map<string, string>()
    for (const w of data?.items ?? []) {
      if (w.name) {
        if (w.id) byKey.set(w.id, w.name)
        if (w.slug) byKey.set(w.slug, w.name)
      }
    }
    return {
      ready: isSuccess,
      nameOf: (reference: string | undefined | null) =>
        reference ? (byKey.get(reference) ?? null) : null,
    }
  }, [data, isSuccess])
}

/**
 * SESSIONS. The live page the front door already reads, gated on `sessions:live:read`.
 *
 * ⛔ IT IS ONE PAGE, AND THAT IS SAID RATHER THAN HIDDEN. `GET /v1/m/sessions/live`
 *    answers with the most-recent page; a session older than it resolves to `null` and
 *    the caller paints its fallback. That is the same honest gap the front door's own
 *    tile carries, not a new one.
 */
export function useSessionNames(): NameLookup {
  const { activeTenant, can } = useAuth()
  const permitted = can('sessions:live:read')
  const params = { limit: SESSION_PAGE }
  const { data, isSuccess } = useQuery({
    queryKey: sessionsKeys.live(activeTenant, params),
    queryFn: () => sessionsApi.live(params),
    enabled: permitted && !!activeTenant,
    staleTime: 30_000,
  })
  return useMemo(() => {
    const byRef = new Map<string, string>()
    for (const s of data?.items ?? []) {
      const title = sessionTitle(s)
      if (title) {
        byRef.set(s.session_ref, title)
        if (s.live_ref) byRef.set(s.live_ref, title)
      }
    }
    return {
      ready: isSuccess,
      nameOf: (reference: string | undefined | null) =>
        reference ? (byRef.get(reference) ?? null) : null,
    }
  }, [data, isSuccess])
}
