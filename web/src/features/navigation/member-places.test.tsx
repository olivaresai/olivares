// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MEMBER'S PORTAL: a person who works in their folders through sessions meets Overview,
// Sessions, Files and Settings, and no administration page. Nothing here reads a role name:
// the projection is `can()` over the permission set whoami reports, through the permission
// projection the production gate applies to every ordinary view (authorization.ts adds the
// capability questions and switched-off modules, neither of which a member's places use).
import i18n from 'i18next'
import { describe, expect, it } from 'vitest'
import '@/lib/i18n'
import { FEATURE_VIEWS } from '@/features/registry'
import type { Whoami } from '@/lib/api/types'
import { can } from '@/lib/auth/rbac'
import { ADMIN_READS } from './admin-reads.fixture'
import {
  areaLabel,
  authorizedEntries,
  fold,
  permissionGate,
  viewById,
  type ViewGate,
} from './model'
import {
  PLACES,
  buildNavSearchIndex,
  placeOfView,
  placePages,
  settingsPlace,
  sidebarPlaces,
} from './places'

// The member's permission set, as a fixture until the engine role lands: the session work of
// a person in their own folders (live sessions, runs, messages, stop, the folder's files) and
// nothing tenant-wide. Every name exists in the engine today.
const MEMBER: Whoami = {
  kind: 'user',
  user_id: 'ana',
  display_name: 'Ana',
  actor: 'user:ana',
  superadmin: false,
  grants: [
    {
      tenant: 't1',
      role: 'member',
      permissions: [
        'sessions:live:read',
        'sessions:run:read',
        'sessions:run:write',
        'sessions:stop:write',
        'sessions:message:read',
        'sessions:message:write',
        'sessions:workspace:read',
        'sessions:workspace:write',
      ],
    },
  ],
}

const gate = permissionGate((p) => can(p, { principal: MEMBER, tenant: 't1' }))

/** The places the ⌘K palette offers pages of. */
function palettePlaces(listed: ViewGate) {
  const t = i18n.getFixedT('en')
  return new Set(
    authorizedEntries(buildNavSearchIndex(t), listed).flatMap((e) => {
      const view = viewById(e.id)
      return e.kind === 'view' && view ? [placeOfView(view)] : []
    }),
  )
}

/** The gate of a principal holding exactly these permissions in t1. */
function gateOf(role: string, permissions: string[]): ViewGate {
  const principal: Whoami = {
    kind: 'user',
    user_id: role,
    display_name: role,
    actor: `user:${role}`,
    superadmin: false,
    grants: [{ tenant: 't1', role, permissions }],
  }
  return permissionGate((p) => can(p, { principal, tenant: 't1' }))
}

describe("a member's portal", () => {
  it('lists Overview, Sessions and Files, with Settings at the foot', () => {
    expect(sidebarPlaces(gate).map((p) => p.id)).toEqual([
      'now',
      'sessions',
      'files',
    ])
    expect(settingsPlace().id).toBe('settings')
  })

  it('offers no page of any other place, and only the workspace in Files', () => {
    const others = PLACES.filter(
      (p) => !['now', 'sessions', 'files'].includes(p.id),
    )
    for (const place of others)
      expect([place.id, placePages(place.id, gate)]).toEqual([place.id, []])
    expect(placePages('files', gate).map((p) => p.id)).toEqual([
      'workspaceDashboard',
    ])
  })

  it('keeps People, Tools and Rules out of the palette', () => {
    expect(palettePlaces(gate)).toEqual(new Set(['now', 'sessions', 'files']))
  })
})

// An editor's set, by the engine's role rule (core/auth/permission.go) over what the console's
// views ask for: every module read and write (the verb tier, :294-305 and :323-324), so the
// machine-identity roster read governance:identity:read, security:finding:read and
// health:status:read among them; audit:read and tenant:read from the viewer's core set
// (:249-251); and the privileged posture read governance:idposture:read (:216, editor and up
// at :320-321). No user, membership, token or tenant:admin permission: those begin at the
// administrator (:258-263).
const EDITOR = [
  ...new Set(
    FEATURE_VIEWS.flatMap((v) => v.permission ?? []).filter((p) =>
      /^[^:]+:[^:]+:(read|write)$/.test(p),
    ),
  ),
  'audit:read',
  'tenant:read',
  'governance:idposture:read',
]
const ADMINISTRATOR = [
  ...EDITOR,
  ...['user', 'membership', 'token', 'tenant'].flatMap((r) => [
    `${r}:read`,
    `${r}:write`,
  ]),
  'tenant:admin',
]

describe('People is listed for whom its roster answers', () => {
  it('offers an editor no People: no sidebar place, no page, no palette entry', () => {
    const editor = gateOf('editor', EDITOR)
    expect(EDITOR).toContain('governance:identity:read')
    expect(sidebarPlaces(editor).map((p) => p.id)).not.toContain('people')
    expect(placePages('people', editor)).toEqual([])
    expect(palettePlaces(editor)).not.toContain('people')
    // Rules is listed on the same identity read on purpose, and stays.
    expect(sidebarPlaces(editor).map((p) => p.id)).toContain('rules')
  })

  it('still lists People with its pages for an administrator', () => {
    const admin = gateOf('admin', ADMINISTRATOR)
    expect(sidebarPlaces(admin).find((p) => p.id === 'people')?.path).toBe(
      '/identity',
    )
    expect(placePages('people', admin).map((p) => p.id)).toEqual([
      'identity',
      'console',
    ])
    expect(palettePlaces(admin)).toContain('people')
  })
})

// The administrator's reads (admin-reads.fixture.ts) begin at the administrator: an editor
// holds none of them. Three reads stay with an editor because its own pages need them:
// tenant:read (a session's context pane, skills, new work items, scope lists),
// governance:identity:read (Rules) and governance:killswitch:read (the status line and a
// session's governance panel say why sessions stopped). No page asks for that last one, so it
// is named here.
const EDITOR_KEEPS = [
  ...new Set([
    ...EDITOR.filter((p) => !ADMIN_READS.has(p)),
    'governance:killswitch:read',
  ]),
]

/** Every page a principal is offered, in every place and in Settings. */
function offeredPages(listed: ViewGate) {
  return new Set(
    [...PLACES.map((p) => p.id), 'settings' as const].flatMap((id) =>
      placePages(id, listed).map((p) => p.id),
    ),
  )
}

/** The pages the ⌘K palette offers. */
function paletteViews(listed: ViewGate) {
  const t = i18n.getFixedT('en')
  return new Set(
    authorizedEntries(buildNavSearchIndex(t), listed).flatMap((e) =>
      e.kind === 'view' ? [e.id] : [],
    ),
  )
}

describe("an editor's places hold only what is theirs", () => {
  const editor = gateOf('editor', EDITOR_KEEPS)
  // The administrator holds every module verb, the kill switch's own admin verb among them.
  const admin = gateOf('admin', [
    ...ADMINISTRATOR,
    'governance:killswitch:admin',
  ])

  it('offers an editor no Estate, executive view or Kill switch page on the reads it keeps', () => {
    expect(EDITOR_KEEPS).toEqual(
      expect.arrayContaining([
        'tenant:read',
        'governance:identity:read',
        'governance:killswitch:read',
      ]),
    )
    for (const id of ['estate', 'dashboards', 'killswitch']) {
      expect(offeredPages(editor), id).not.toContain(id)
      expect(paletteViews(editor), id).not.toContain(id)
    }
  })

  it('reaches Vault from Settings, never from Security', () => {
    expect(placePages('settings', editor).map((p) => p.id)).toContain('vault')
    expect(placePages('security', editor).map((p) => p.id)).not.toContain(
      'vault',
    )
    // The palette finds it by the words of Settings, not by those of the Security area.
    const t = i18n.getFixedT('en')
    const vault = buildNavSearchIndex(t).find((e) => e.id === 'vault')
    expect(vault?.context).toBe(t('nav:places.settings'))
    expect(vault?.haystack.context).toContain(fold(areaLabel(t, 'system')))
    expect(vault?.haystack.context).not.toContain(
      fold(areaLabel(t, 'security-identity')),
    )
  })

  it('files Reach with the pages of Audit, never under the AI area, and offers it to no editor', () => {
    // The palette finds it by the words of Audit's own area, not by those of the AI area.
    const t = i18n.getFixedT('en')
    const reach = buildNavSearchIndex(t).find((e) => e.id === 'reach')
    expect(reach?.context).toBe(t('nav:places.activity'))
    expect(reach?.haystack.context).toContain(fold(areaLabel(t, 'observation')))
    expect(reach?.haystack.context).not.toContain(fold(areaLabel(t, 'ai')))
    // Its read is the administrator's: an editor meets it nowhere, an administrator under
    // Audit's More.
    expect(offeredPages(editor)).not.toContain('reach')
    expect(paletteViews(editor)).not.toContain('reach')
    expect(placePages('activity', admin)).toContainEqual({
      id: 'reach',
      path: '/reach',
      more: true,
    })
  })

  it('still lists every place, Estate, the executive view, the Kill switch and Vault for an administrator', () => {
    expect(sidebarPlaces(admin).map((p) => p.id)).toEqual(
      PLACES.map((p) => p.id),
    )
    for (const id of ['estate', 'dashboards', 'killswitch', 'vault']) {
      expect(offeredPages(admin), id).toContain(id)
      expect(paletteViews(admin), id).toContain(id)
    }
  })
})
