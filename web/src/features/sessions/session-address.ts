// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ADDRESS OF ONE SESSION.
//
// A review named the gap this module closes: *"one click to the evidence is one click
// to the ROOM, not to the card"*, the largest gap the front door had left. The
// cause was one sentence of state — `sessions-workspace-view.tsx` held its selection
// in `useState`, so nothing outside the component could name a session, and the front
// door's work rows could only open `/sessions` as a list.
//
// WHAT AN ADDRESS IS, and why it is not a new string. `mergeSessions` already gives
// every row a stable identity (`provenance.ts`): `live:<live_ref>` for a profile-scoped
// row, `sess:<session_ref>` for a legacy one, `run:<run_ref>` for a run whose managed
// row has not been proven yet. That key is ALREADY the unambiguous name of a session on
// this plane, it already survives two homes announcing one provider id (B2), and the
// card already knows how to open each of its three shapes. Minting a second identifier
// for the URL would have been a second answer to a question that has one.
//
// ⛔ WHY A SEARCH PARAM AND NOT `/sessions/<id>`. The brief asks for the path segment.
//    A new mounted route must be listed in `web/src/features/route-census.json` or
//    `registry.route-conservation.test.ts` fails it as `unrecorded` — and that file, with
//    `registry.tsx`, is the feature registry, which this module does not edit. A search param
//    needs neither, and this repository already treats search params as canonical
//    shareable state (`lib/hooks/use-url-state.ts`: *"this repo treats search params as
//    canonical shareable state"*). Every property the brief's oracle names — a cold deep
//    link, a reload, Back, Forward, a shared link — holds for `?session=`; the promotion
//    to a path segment is a two-line change once a mounted route exists, and `targetFromAddress` is
//    where it would read its param.
//
// ⛔ EVERYTHING HERE IS UNTRUSTED INPUT. A URL is typed, pasted, edited and forged. Each
//    decoder below returns the in-code default and NAMES the key it refused, so the view
//    can say so out loud (`useValidatedUrlState` renders it) instead of silently showing
//    the recipient a different screen than the author saw.
import type { SessionTarget } from './session-target'

/** The search param naming which session the work surface is showing. */
export const SESSION_PARAM = 'session'
/** Which of the three panes is in front (below `xl`, where only one fits). */
export const PANE_PARAM = 'pane'
/** Which evidence block in the narrative is expanded. */
export const EVIDENCE_PARAM = 'evidence'

/** The keys this surface OWNS. Anything else on the route is left untouched. */
export const SESSION_ADDRESS_KEYS = [
  SESSION_PARAM,
  PANE_PARAM,
  EVIDENCE_PARAM,
] as const

/**
 * The three panes, in reading order.
 *  - `rail`      the work rail: every session, grouped by what it needs from you.
 *  - `narrative` what this session did, told as sentences, with the evidence inline.
 *  - `context`   what it applies to, and the trace of what it touched.
 */
export const WORK_PANES = ['rail', 'narrative', 'context'] as const
export type WorkPane = (typeof WORK_PANES)[number]
export const DEFAULT_PANE: WorkPane = 'rail'

/**
 * The evidence blocks of the narrative pane. All three are always ON SCREEN; this
 * names the one that is expanded to its full list rather than its first rows.
 *  - `checks`    what the session's own findings say — the validation list.
 *  - `activity`  the last turns: the tool and MCP calls it made.
 *  - `resources` what it touched, once per distinct resource.
 */
export const EVIDENCE_BLOCKS = ['checks', 'activity', 'resources'] as const
export type EvidenceBlock = (typeof EVIDENCE_BLOCKS)[number]
export const DEFAULT_EVIDENCE: EvidenceBlock = 'checks'

/**
 * A ceiling on the address, because an unbounded value from the URL reaches a cache
 * key, a `title` attribute and — through `targetFromAddress` — a path segment of an
 * engine request. Our own keys are a prefix plus a UUID or a provider id; 200 bytes is
 * far above every shape this plane mints and far below anything worth carrying.
 */
export const MAX_ADDRESS_LENGTH = 200

/** The three shapes a row key can have. Their meanings are `provenance.sessionTarget`'s. */
const ADDRESS_KINDS = ['live', 'sess', 'run'] as const

/**
 * The address of a row, which IS its `UnifiedSession.key`. It exists as a named
 * function so a caller says what it means rather than reaching for a field, and so the
 * two directions of this module sit next to each other.
 */
export function addressOf(session: { key: string }): string {
  return session.key
}

/**
 * Resolve an address to what the card should open on, WITHOUT a list.
 *
 * This is the whole value of the change: a cold deep link has no rows loaded, so the
 * address has to carry enough for the card to ask the engine itself. It does — the key
 * names which of the three references it holds, and `SessionCard` already resolves each
 * one (`liveById`, `liveOne`, the run lookup).
 *
 * Returns null for anything it does not recognise. Null is not an error here: it is a
 * URL that names no session, which the view reports and clears.
 */
export function targetFromAddress(address: string): SessionTarget | null {
  if (!address || address.length > MAX_ADDRESS_LENGTH) return null
  const cut = address.indexOf(':')
  if (cut <= 0) return null
  const kind = address.slice(0, cut)
  const ref = address.slice(cut + 1)
  if (!ref.trim()) return null
  if (!(ADDRESS_KINDS as readonly string[]).includes(kind)) return null
  if (kind === 'live') return { liveRef: ref }
  if (kind === 'sess') return { sessionRef: ref }
  return { runRef: ref }
}

/**
 * The address of a target the CARD resolved for itself — a related session it
 * navigated to, which was never a row on this page.
 *
 * The preference order is `sessionTarget`'s, reversed: the row's own opaque id first
 * because it is the only unambiguous name a scoped row has, then the bare provider id,
 * then the run. A target with nothing in it addresses nothing, and says so.
 */
export function addressFromTarget(target: SessionTarget): string | null {
  if (target.liveRef?.trim()) return `live:${target.liveRef}`
  if (target.sessionRef?.trim()) return `sess:${target.sessionRef}`
  if (target.runRef?.trim()) return `run:${target.runRef}`
  return null
}

/** True when this string names a session on this plane. */
export function isSessionAddress(address: string | undefined): boolean {
  return !!address && targetFromAddress(address) !== null
}

/** One view's decoded address, with the keys it refused. */
export interface SessionAddress {
  /** The session on screen, or null when the URL names none. */
  address: string | null
  pane: WorkPane
  evidence: EvidenceBlock
}

/**
 * Decode the three owned params. Pure and total: it never throws, and every rejected
 * key falls back to its in-code default AND is reported.
 *
 * The `pane` and `evidence` keys are only meaningful with a session on screen, but they
 * are NOT rejected without one: a link built while a session was open and then edited
 * down should keep the pane it named rather than teach the recipient that half their
 * URL was wrong. They are validated against their own closed sets, which is the check
 * that matters.
 */
export function decodeSessionAddress(raw: {
  [key: string]: string | undefined
}): { value: SessionAddress; issues: string[] } {
  const issues: string[] = []

  const rawAddress = raw[SESSION_PARAM]
  let address: string | null = null
  if (rawAddress !== undefined) {
    if (isSessionAddress(rawAddress)) address = rawAddress
    else issues.push(SESSION_PARAM)
  }

  const rawPane = raw[PANE_PARAM]
  let pane: WorkPane = DEFAULT_PANE
  if (rawPane !== undefined) {
    if ((WORK_PANES as readonly string[]).includes(rawPane))
      pane = rawPane as WorkPane
    else issues.push(PANE_PARAM)
  }

  const rawEvidence = raw[EVIDENCE_PARAM]
  let evidence: EvidenceBlock = DEFAULT_EVIDENCE
  if (rawEvidence !== undefined) {
    if ((EVIDENCE_BLOCKS as readonly string[]).includes(rawEvidence))
      evidence = rawEvidence as EvidenceBlock
    else issues.push(EVIDENCE_PARAM)
  }

  return { value: { address, pane, evidence }, issues }
}
