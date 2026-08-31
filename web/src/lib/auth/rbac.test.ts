// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { Grant, Whoami } from '@/lib/api/types'
import {
  can,
  confinedWorkspaceIn,
  grantInTenant,
  roleInTenant,
  roleRank,
} from './rbac'

const TENANT = 'tenant-1'
const OTHER = 'tenant-2'

function grant(tenant: string, role: string, permissions: string[]): Grant {
  return { tenant, role, permissions }
}

function principal(grants: Grant[], superadmin = false): Whoami {
  return {
    kind: 'user',
    user_id: 'u',
    actor: 'user:u',
    display_name: 'U',
    superadmin,
    grants,
  }
}

// A viewer-shaped set as the engine emits it: the ordinary entity read, and NOT the IAM
// roster read that reads exactly like it. Which permissions belong to which role is the
// ENGINE's business now — this module only has to look them up, and scripts/
// check-console-perms.mjs is what proves the lookup agrees with auth.RoleGrants.
const VIEWER_SET = ['agent:read', 'session:read', 'audit:read']
const ADMIN_SET = [
  ...VIEWER_SET,
  'agent:write',
  'user:read',
  'accessgraph:read',
]

describe('can — membership of the engine-computed set', () => {
  it('answers from the set, not from the permission string', () => {
    const p = principal([grant(TENANT, 'viewer', VIEWER_SET)])
    expect(can('agent:read', { principal: p, tenant: TENANT })).toBe(true)
    expect(can('agent:write', { principal: p, tenant: TENANT })).toBe(false)
    // `user:read` has the shape of an ordinary read and is admin-tier in the engine.
    // Nothing here re-derives that: it is simply absent from the set.
    expect(can('user:read', { principal: p, tenant: TENANT })).toBe(false)
  })

  it('does no verb arithmetic: an unknown string is not a read', () => {
    // The defect that started this. `voice:policy:write` and `voice:write` do not exist
    // server-side; a client-side verb fallback turned both into reads every viewer
    // holds. A set lookup cannot make that mistake — the strings are simply not in it.
    const p = principal([grant(TENANT, 'editor', VIEWER_SET)])
    for (const perm of [
      'voice:write',
      'voice:policy:write',
      'compliance:read',
      'totally:invented',
      'somemodule:thing:read',
    ]) {
      expect(can(perm, { principal: p, tenant: TENANT })).toBe(false)
    }
  })

  it('is exact: a prefix or a superstring of a held permission is not held', () => {
    const p = principal([grant(TENANT, 'viewer', ['agent:read'])])
    expect(can('agent:read', { principal: p, tenant: TENANT })).toBe(true)
    expect(can('agent:rea', { principal: p, tenant: TENANT })).toBe(false)
    expect(can('agent:read:extra', { principal: p, tenant: TENANT })).toBe(
      false,
    )
    expect(can('gent:read', { principal: p, tenant: TENANT })).toBe(false)
  })
})

describe('can — the target tenant selects the set', () => {
  it('uses the grant for the requested tenant, not the first one', () => {
    // The capability this protects: can(permission, { tenant }) accepts a tenant other
    // than the active one, and a principal's role — hence its set — differs per tenant.
    // A single principal-wide set would answer tenant-2 with tenant-1's authority.
    const p = principal([
      grant(TENANT, 'viewer', VIEWER_SET),
      grant(OTHER, 'admin', ADMIN_SET),
    ])
    expect(can('user:read', { principal: p, tenant: TENANT })).toBe(false)
    expect(can('user:read', { principal: p, tenant: OTHER })).toBe(true)
    expect(can('agent:read', { principal: p, tenant: TENANT })).toBe(true)
  })

  it('denies with no principal, no tenant, or no grant in the tenant', () => {
    const p = principal([grant(TENANT, 'viewer', VIEWER_SET)])
    expect(can('agent:read', { principal: null, tenant: TENANT })).toBe(false)
    expect(can('agent:read', { principal: p, tenant: null })).toBe(false)
    expect(can('agent:read', { principal: p, tenant: 'nowhere' })).toBe(false)
  })

  it('denies everything to a principal with an EMPTY set', () => {
    // What the engine emits for a role it does not know: empty, never absent. It denies
    // here with no branch of its own.
    const p = principal([grant(TENANT, 'archduke', [])])
    expect(can('agent:read', { principal: p, tenant: TENANT })).toBe(false)
    expect(roleInTenant(p, TENANT)).toBe('archduke')
  })
})

describe('can — superadmin', () => {
  it('allows anything, including system operations and with no tenant', () => {
    // A superadmin's authority is cross-tenant and is not expressed by any per-tenant
    // grant, so no set could carry it. This mirrors the engine's rbacAllows, which
    // returns true for a superadmin before it looks at any membership.
    const p = principal([], true)
    expect(can('agent:write', { principal: p, tenant: TENANT })).toBe(true)
    expect(can('system:admin', { principal: p, tenant: TENANT })).toBe(true)
    expect(can('user:read', { principal: p, tenant: null })).toBe(true)
  })

  it('does not give system:admin to a non-superadmin, whatever its role', () => {
    // The engine grants system:admin to no tenant role, so it is in no set. Owner
    // included.
    const p = principal([grant(TENANT, 'owner', ADMIN_SET)])
    expect(can('system:admin', { principal: p, tenant: TENANT })).toBe(false)
  })
})

describe('can — a payload whose permission set is not an array of strings', () => {
  // The type is only a compile-time claim about a WIRE payload, so each case below is a
  // shape the engine could actually send if it broke. The casts are the point.
  //
  // MEASURED, because the first version of this case asserted the wrong thing: `new
  // Set(undefined)` does NOT throw, it yields an empty set. It is a NON-ITERABLE that
  // throws — `new Set(5)` and `new Set({})` raise TypeError — and a bare string is the
  // quiet one, since `new Set('agent:read')` is a set of CHARACTERS. All of them must
  // deny, and none of them may take the panel down.
  const shapes: Array<[string, unknown]> = [
    ['absent', undefined],
    ['null', null],
    ['a number', 5],
    ['an object', { 'agent:read': true }],
    ['a bare string', 'agent:read'],
  ]
  for (const [name, permissions] of shapes) {
    it(`denies instead of throwing when permissions is ${name}`, () => {
      const p = principal([
        { tenant: TENANT, role: 'admin', permissions } as unknown as Grant,
      ])
      expect(() =>
        can('agent:read', { principal: p, tenant: TENANT }),
      ).not.toThrow()
      expect(can('agent:read', { principal: p, tenant: TENANT })).toBe(false)
    })
  }

  it('still answers from a well-formed set, so the guard above is not a blanket deny', () => {
    const p = principal([grant(TENANT, 'admin', ['agent:read'])])
    expect(can('agent:read', { principal: p, tenant: TENANT })).toBe(true)
  })
})

describe('grantInTenant / roleInTenant / confinedWorkspaceIn', () => {
  it('reads the grant, its role and its confinement', () => {
    const confined: Grant = {
      tenant: TENANT,
      role: 'admin',
      permissions: VIEWER_SET,
      confined_workspace: 'ws-payments',
    }
    const p = principal([confined, grant(OTHER, 'viewer', VIEWER_SET)])
    expect(grantInTenant(p, TENANT)).toBe(confined)
    expect(roleInTenant(p, TENANT)).toBe('admin')
    expect(confinedWorkspaceIn(p, TENANT)).toBe('ws-payments')
    // Confinement is per MEMBERSHIP: the same principal is tenant-wide elsewhere.
    expect(confinedWorkspaceIn(p, OTHER)).toBeNull()
    expect(confinedWorkspaceIn(p, 'nowhere')).toBeNull()
    expect(confinedWorkspaceIn(null, TENANT)).toBeNull()
    expect(grantInTenant(p, null)).toBeNull()
    expect(roleInTenant(null, TENANT)).toBeNull()
  })
})

describe('roleRank', () => {
  it('orders the built-in roles and ranks an unknown one below all of them', () => {
    expect(roleRank('viewer')).toBeLessThan(roleRank('editor'))
    expect(roleRank('editor')).toBeLessThan(roleRank('admin'))
    expect(roleRank('admin')).toBeLessThan(roleRank('owner'))
    expect(roleRank('auditor')).toBe(-1)
    expect(roleRank(undefined)).toBe(-1)
  })
})

// The privileged-read tier is NOT asserted here any more, and its absence is the point
// rather than an omission.
//
// A block here used to assert that a viewer is denied every privileged read and that
// editor and up hold them, by re-deriving the tier on the client. Removed the
// client-side rule entirely: /v1/auth/whoami hands each grant its EFFECTIVE permission
// set and can() is set membership, so there is no tier left in this module to assert
// against. Keeping the block would have meant testing a fixture built to satisfy it.
//
// The guarantee did not go with it. core/auth/effective_test.go
// (TestEffectivePermissionsEqualsRoleGrants) asserts, for EVERY built-in role over a
// universe that explicitly includes every privileged read, that the served set equals
// RoleGrants exactly — with a vacuity control on both the universe and the set, and a
// check that the set invents nothing outside it. That is strictly stronger than the
// four cases removed here, and it sits where the rule now lives.
