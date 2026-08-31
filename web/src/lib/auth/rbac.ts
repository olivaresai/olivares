// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Grant, Role, Whoami } from '@/lib/api/types'

/**
 * RBAC reflection. The BACKEND is the source of truth (core/auth) — every request is
 * authorized server-side. This module lets the UI MIRROR those decisions to hide/disable
 * actions a principal can't take, so the operator isn't offered buttons that would 403.
 * It NEVER grants access the backend wouldn't.
 *
 * IT NO LONGER MIRRORS THE RULE. The engine hands each grant the principal's EFFECTIVE
 * PERMISSION SET (GET /v1/auth/whoami) and can() is set membership. There is no
 * verb arithmetic, no per-role table and no privileged-read list in the browser, because
 * every one of those was a second implementation of a rule that lives in core/auth, and
 * the second implementation drifted OPEN three separate ways:
 *
 *   - a verb re-derivation fell back to 'read' for anything it did not recognise, where
 *     the engine's Verb() returns "". `voice:policy:write` and `voice:write`, neither of
 *     which exists server-side, became reads every viewer holds.
 *   - the verb tier was applied to CORE permissions too, where the engine consults an
 *     explicit set. `membership:read` reads like an ordinary viewer read and is
 *     admin-tier; the console showed the roster to viewers.
 *   - there was no notion of a privileged read, so the access-graph trio and the
 *     per-developer adoption drill-down — gated above viewer because reconstructing the
 *     access matrix is recon — were offered to viewers.
 *
 * Generating the two data branches from the engine fixed the drift but not the shape:
 * the client still DECIDED. Now it looks up.
 *
 * WHAT THE SET DOES NOT CARRY, and it matters when reading a `can()` that returns true:
 * authored scoped grants and forbids and the ABAC/PDP deny-overlay are decided
 * per RESOURCE at request time, so the engine may still refuse an action this set
 * allows. That limit is unchanged from every earlier version of this module — behind a
 * rendered button the server door is the real one. The set DOES carry the part of the
 * workspace confinement that holds whatever the action targets. See
 * core/auth/effective.go for the authoritative statement.
 *
 * `superadmin` is a user flag = cross-tenant system role, and it short-circuits here
 * exactly as it does in the engine's rbacAllows: a superadmin's authority is not
 * expressed by any per-tenant grant, so no per-grant set could carry it.
 */

const ROLE_ORDER: Role[] = ['viewer', 'editor', 'admin', 'owner']

/**
 * Per-grant permission index, keyed by the grant OBJECT.
 *
 * can() is called from a few hundred render paths and the set runs to ~250 entries for
 * an owner, so a linear scan per call is real work on every render. The grants come from
 * the cached whoami query, so their identity is stable until the principal is refetched
 * — and a WeakMap keyed on them cannot outlive the payload it indexes.
 */
const permIndex = new WeakMap<Grant, ReadonlySet<string>>()

function permissionsOf(grant: Grant): ReadonlySet<string> {
  const hit = permIndex.get(grant)
  if (hit) return hit
  // `permissions` is required by the contract, but this is a WIRE payload and the type is
  // only a compile-time claim about it. The guard is Array.isArray and not `?? []`, and a
  // mutation run is why: `new Set(undefined)` does NOT throw — it yields an empty set — so
  // the nullish fallback defended against nothing, and the case that claimed to prove it
  // stayed green with the fallback deleted. What DOES throw is a non-iterable:
  // `new Set(5)` and `new Set({})` raise TypeError, which would take the whole panel down
  // rather than deny one action. A bare string is worse than useless — `new Set('a:read')`
  // is a set of CHARACTERS that silently matches nothing. Anything that is not an array of
  // strings is therefore treated as "I do not know", which DENIES.
  const raw = grant.permissions
  const set: ReadonlySet<string> = new Set(Array.isArray(raw) ? raw : [])
  permIndex.set(grant, set)
  return set
}

/** The principal's grant in a specific tenant, if any. */
export function grantInTenant(
  principal: Whoami | null,
  tenant: string | null,
): Grant | null {
  if (!principal || !tenant) return null
  return principal.grants.find((g: Grant) => g.tenant === tenant) ?? null
}

/** The principal's role in a specific tenant, if any. */
export function roleInTenant(
  principal: Whoami | null,
  tenant: string | null,
): string | null {
  return grantInTenant(principal, tenant)?.role ?? null
}

/**
 * The workspace this principal's membership in `tenant` is confined to, or null when the
 * membership is tenant-wide. Reported so a view can say WHY an action is missing;
 * the narrowing itself is already applied to the grant's permission set.
 */
export function confinedWorkspaceIn(
  principal: Whoami | null,
  tenant: string | null,
): string | null {
  return grantInTenant(principal, tenant)?.confined_workspace ?? null
}

export interface CanContext {
  principal: Whoami | null
  /** The tenant the action targets (defaults to the active tenant in the hook). */
  tenant: string | null
}

/**
 * can decides whether the UI should offer an action: superadmin always, otherwise
 * membership of the effective set the engine computed for this principal in the target
 * tenant.
 *
 * No tenant means no grant in it, which denies — the same deny-closed answer the engine
 * gives a principal with no membership. An unknown role produces an EMPTY set server-side
 * rather than a missing one, so it denies here without a branch of its own.
 */
export function can(permission: string, ctx: CanContext): boolean {
  const { principal, tenant } = ctx
  if (!principal) return false
  if (principal.superadmin) return true
  const grant = grantInTenant(principal, tenant)
  if (!grant) return false
  return permissionsOf(grant).has(permission)
}

/** Compare two roles (for UI like "can I grant a role ≤ mine"). */
export function roleRank(role: string | undefined): number {
  return isRole(role) ? ROLE_ORDER.indexOf(role) : -1
}

function isRole(role: string | undefined): role is Role {
  return (
    role === 'viewer' ||
    role === 'editor' ||
    role === 'admin' ||
    role === 'owner'
  )
}
