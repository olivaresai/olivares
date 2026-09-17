// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FIVE QUESTIONS CHANNEL ADMINISTRATION ASKS, and the only place the console
// declares them.
//
// Each administrative decision this feature makes has EXACTLY ONE question behind it, and
// the five are independent. The feature never:
//
//   · asks for `sessions:channel:read` — administration is not a read tier, and requiring
//     one would invent a prerequisite the engine does not have (a principal with a local
//     ADMIN bit and no read bit administers, and its content read stays refused);
//   · inspects `my_access` — that is the READ catalog's per-row hint, not authority;
//   · infers an entity answer from the surface, or the surface from an entity answer —
//     admission to a collection is not authority over a row in it, and a permit naming one
//     resource is not a licence to enumerate its neighbours;
//   · treats an allowed sibling as evidence — the revoke decision of one grant row says
//     nothing about the next, so each selected row asks for itself.
//
// ⛔ THE OPERATION STRINGS ARE MOUNTED ROUTE PATTERNS, NOT RESOLVED URLS. `{id}` stays
//    literal and the real id travels in `selectors.path`; substituting it would name a
//    route the engine's table does not contain, and the honest answer to that is
//    `not_supported` — an unknown, which enables nothing, so the defect would present as
//    a permanently unavailable screen rather than as a wrong permit.
import {
  capabilityQuestion,
  type NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'

/** The mounted patterns, exactly as `core/api` registers them. */
export const CHANNEL_ADMINISTRATION_SURFACE =
  'GET /v1/m/sessions/channels/administration'
export const CHANNEL_GRANTS_OPERATION =
  'GET /v1/m/sessions/channels/{id}/grants'
export const CHANNEL_PATCH_OPERATION = 'PATCH /v1/m/sessions/channels'
export const CHANNEL_GRANT_OPERATION =
  'POST /v1/m/sessions/channels/{id}/grants'
export const CHANNEL_REVOKE_OPERATION =
  'POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke'

/** A question is only asked with an EXPLICIT workspace. Without one there is nothing to
 *  ask about: the console never selects a workspace on the operator's behalf, and an
 *  entity question with no declared scope is answered `inputs_required`. */
function scoped(workspace: string | null | undefined): string | null {
  return workspace ? workspace : null
}

/**
 * (1) May this principal LOAD the administration collection in this workspace?
 *
 * A surface question: it asserts nothing about rows. An authorized empty list is
 * `reachable`, and a `not_reachable` collection denies no operation on another route.
 */
export function administrationSurfaceQuestion(
  workspace: string | null | undefined,
): NormalizedCapabilityQuestion | null {
  const ws = scoped(workspace)
  if (!ws) return null
  return capabilityQuestion({
    kind: 'surface',
    operation: CHANNEL_ADMINISTRATION_SURFACE,
    workspaceId: ws,
  })
}

/**
 * (2) May this principal read THIS channel's grant sheet?
 *
 * The sheet's own read, and the one an entity-only deep link needs: it is answerable
 * without the collection being reachable, which is exactly what makes a deep link work
 * for a principal admitted to one row and not to the list.
 */
export function grantSheetQuestion(
  workspace: string | null | undefined,
  channelId: string | null | undefined,
): NormalizedCapabilityQuestion | null {
  const ws = scoped(workspace)
  if (!ws || !channelId) return null
  return capabilityQuestion({
    kind: 'operation',
    operation: CHANNEL_GRANTS_OPERATION,
    workspaceId: ws,
    path: { id: channelId },
  })
}

/**
 * (3) May this principal apply a configuration update to THIS channel?
 *
 * `PATCH /channels` locates its row in the BODY, so the selector family is `body`, not
 * `path` — the route declares `channel_id` as its one entity locator. It is an
 * identification input, never the write payload: no other field of the command is
 * admitted, and nothing is executed by asking.
 */
export function configurationQuestion(
  workspace: string | null | undefined,
  channelId: string | null | undefined,
): NormalizedCapabilityQuestion | null {
  const ws = scoped(workspace)
  if (!ws || !channelId) return null
  return capabilityQuestion({
    kind: 'operation',
    operation: CHANNEL_PATCH_OPERATION,
    workspaceId: ws,
    body: { channel_id: channelId },
  })
}

/** (4) May this principal create a grant generation on THIS channel? */
export function grantCreationQuestion(
  workspace: string | null | undefined,
  channelId: string | null | undefined,
): NormalizedCapabilityQuestion | null {
  const ws = scoped(workspace)
  if (!ws || !channelId) return null
  return capabilityQuestion({
    kind: 'operation',
    operation: CHANNEL_GRANT_OPERATION,
    workspaceId: ws,
    path: { id: channelId },
  })
}

/**
 * (5) May this principal revoke THIS EXACT grant generation?
 *
 * ⛔ THE GRANT ID IS PART OF THE QUESTION. Asking once per channel and painting every row
 *    with the answer would project one generation's authority onto all of them; the
 *    selected row asks about itself, and the answer expires with itself.
 */
export function grantRevocationQuestion(
  workspace: string | null | undefined,
  channelId: string | null | undefined,
  grantId: string | null | undefined,
): NormalizedCapabilityQuestion | null {
  const ws = scoped(workspace)
  if (!ws || !channelId || !grantId) return null
  return capabilityQuestion({
    kind: 'operation',
    operation: CHANNEL_REVOKE_OPERATION,
    workspaceId: ws,
    path: { id: channelId, grant_id: grantId },
  })
}

/* ── the administration deep link ─────────────────────────────────────────────── */

/** The url parameter that names ONE channel to administer. Declared here beside the
 *  question it decides, so the route gate and the view read the SAME parameter with the
 *  same validity rule — two readings of one url is how a route opens a sheet while the
 *  screen behind it still fetches the collection. */
export const ADMIN_CHANNEL_PARAM = 'admin_channel'

/** Canonical UUID, the only form the engine's ids take. An invalid value is NOT a deep
 *  link: the route falls back to the collection question rather than asking about a
 *  target that cannot exist. */
const CANONICAL_ID =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** The valid `?admin_channel=` of a location's search string, or null. */
export function adminChannelFromSearch(search: string): string | null {
  const value = new URLSearchParams(search).get(ADMIN_CHANNEL_PARAM)
  return value && CANONICAL_ID.test(value) ? value : null
}

/** True when this string is a channel id the console will act on. */
export function isCanonicalId(value: string | null | undefined): boolean {
  return !!value && CANONICAL_ID.test(value)
}

/**
 * The question a ROUTE asks when its url carries a valid administration deep link: the
 * exact grant-sheet operation on that one channel. It is independent of the collection —
 * a `not_reachable` surface says nothing about a row — so answering it never requires,
 * and never triggers, the administration collection read.
 */
export function administrationDeepLinkQuestion(
  workspace: string | null | undefined,
  search: string,
): NormalizedCapabilityQuestion | null {
  return grantSheetQuestion(workspace, adminChannelFromSearch(search))
}
