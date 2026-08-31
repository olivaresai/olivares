// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import { PoliciesTable, SessionsTable, VoiceStats } from './components'
import { policiesFixture, sessionsFixture } from './fixtures'
import './i18n'

describe('SessionsTable — honesty invariants', () => {
  it('renders the transcript only as a fingerprint, never text or audio', () => {
    renderIntel(<SessionsTable sessions={sessionsFixture} />)
    const table = screen.getByRole('grid')
    // The 64-hex transcript hash is shown truncated by the HashChip (head…tail),
    // never as a full payload, and there is no transcription text/audio anywhere.
    expect(within(table).getAllByText(/a1b2c3d4…/).length).toBeGreaterThan(0)
    // The HashChip carries the honest caption, proving it is a reference, not content.
    expect(within(table).getAllByText(/transcript/i).length).toBeGreaterThan(0)
    // Nothing resembling transcription text / audio mime leaks into the DOM.
    const body = document.body.textContent ?? ''
    expect(body).not.toMatch(/audio\//i)
    expect(body).not.toMatch(/data:audio/i)
  })

  it('shows BOTH avg and max latency — never a fabricated p50/p95', () => {
    renderIntel(<SessionsTable sessions={sessionsFixture} />)
    const table = screen.getByRole('grid')
    // The live session's avg 132 ms and max 410 ms are both present.
    expect(within(table).getByText(/avg 132 ms/i)).toBeInTheDocument()
    expect(within(table).getByText(/max 410 ms/i)).toBeInTheDocument()
    // No invented percentile is rendered.
    expect(within(table).queryByText(/p50/i)).not.toBeInTheDocument()
    expect(within(table).queryByText(/p95/i)).not.toBeInTheDocument()
  })

  it('flags an ungoverned open and keeps governed sessions marked governed', () => {
    renderIntel(<SessionsTable sessions={sessionsFixture} />)
    const table = screen.getByRole('grid')
    // The shadow-agent open bypassed gating → flagged, never hidden.
    expect(within(table).getByText('shadow-agent')).toBeInTheDocument()
    // Exactly one ungoverned badge across the four fixtures.
    expect(within(table).getAllByText(/^Ungoverned$/)).toHaveLength(1)
    // The other three are marked governed (badge label, plus the column header,
    // both read "Governed" — so at least the three badges are present).
    expect(
      within(table).getAllByText(/^Governed$/).length,
    ).toBeGreaterThanOrEqual(3)
  })
})

describe('VoiceStats', () => {
  it('reports the live count and governed share from the rows in view', () => {
    renderIntel(<VoiceStats sessions={sessionsFixture} />)
    // 1 of 4 sessions is live.
    expect(screen.getByText(/Live sessions/i)).toBeInTheDocument()
    // 3 of 4 governed → 75% — a real ratio, not an invented metric.
    expect(screen.getByText('75%')).toBeInTheDocument()
    expect(screen.getByText(/3 of 4 were policy-gated/i)).toBeInTheDocument()
  })
})

describe('PoliciesTable — default-DENY configuration', () => {
  it('renders the wildcard policy as "All" and the per-agent allow-list', () => {
    renderIntel(<PoliciesTable policies={policiesFixture} />)
    const table = screen.getByRole('grid')
    // agent_ref '*' surfaces as the localized "All" wildcard.
    expect(within(table).getAllByText(/^All$/).length).toBeGreaterThan(0)
    // The per-agent policy is shown with its concrete allow-list.
    expect(within(table).getByText('support-voice')).toBeInTheDocument()
    expect(
      within(table).getAllByText('gpt-4o-realtime').length,
    ).toBeGreaterThan(0)
    // Latency cap is rendered as an honest operator figure.
    expect(within(table).getAllByText(/300 ms/).length).toBeGreaterThan(0)
  })
})

// The console must not gate on a permission the engine never declares. It did: the
// policy button was shown when `can('voice:policy:write') || can('voice:write')`, and
// NEITHER string exists anywhere server-side. `verbOf` reads a permission's LAST
// colon segment, so both invented names resolved to the `write` verb, which `editor`
// holds — the console offered an action the backend answers 403 to, and the empty
// state told the operator to take it.
//
// This guard reads the ENGINE, and specifically its EFFECTIVE state rather than its
// vocabulary. An earlier version treated the string literals in voice.go as "declared",
// which a signature broke without touching a literal: deleting permPolicyAdmin from
// Permissions() alone left the constant in place and the battery green, while the role
// tiers no longer granted anything. A permission exists for a console when the module
// REGISTERS it and a route REQUIRES it — the constant is only its spelling.
//
// So three engine facts are resolved here, identifier by identifier:
//   - the const block maps each Go identifier to its permission string;
//   - Permissions() names the identifiers the built-in roles actually grant;
//   - APIRoutes names the identifier PUT /policies actually demands.
// The parse accepts both `X auth.Permission = "y"` and a bare `X = "y"` inside the
// block, and both quoted and backtick literals, because a guard whose pattern is
// narrower than the language it reads stops guarding without saying so.
describe('voice policy gate — verified against the EFFECTIVE engine state', () => {
  const decls = readFileSync(
    resolve(__dirname, '../../../../modules/voice/voice.go'),
    'utf8',
  )
  const api = readFileSync(
    resolve(__dirname, '../../../../modules/voice/api.go'),
    'utf8',
  )

  /** Go identifier -> permission string, from the const block. */
  const literalOf = new Map(
    [
      ...decls.matchAll(
        /^\s*(\w+)\s*(?:auth\.Permission\s*)?=\s*(?:"([^"]*)"|`([^`]*)`)/gm,
      ),
    ]
      .map((m) => [m[1], m[2] ?? m[3]] as const)
      .filter(([, value]) => value.startsWith('voice:')),
  )

  /** The permissions the module REGISTERS — what the built-in roles grant. */
  const registered = new Set(
    [
      ...(api.match(/func \(m \*Module\) Permissions\(\)[^{]*\{[^}]*\}/) ?? [
        '',
      ])[0].matchAll(/\bperm\w+/g),
    ]
      .map((m) => literalOf.get(m[0]))
      .filter((p): p is string => Boolean(p)),
  )

  /** The permission PUT /policies actually demands. */
  const routePermission = literalOf.get(
    api.match(/reg\.Handle\(\s*"PUT"\s*,\s*"\/policies"\s*,\s*(\w+)/)?.[1] ??
      '',
  )

  const view = readFileSync(resolve(__dirname, 'voice-view.tsx'), 'utf8')
  // Only the `can('…')` CALLS, so the explanatory comment naming the dead pair does
  // not satisfy the assertion it exists to explain.
  const checked = [
    ...new Set(
      [...view.matchAll(/\bcan\(\s*'(voice:[a-z:]+)'/g)].map((m) => m[1]),
    ),
  ]

  it('resolves the engine on all three axes it is asked about', () => {
    // Sanity: if any parse silently returned nothing, every assertion below would be
    // vacuously true — the fail-open this guard was rewritten to remove.
    expect(literalOf.size).toBeGreaterThan(0)
    expect(registered.size).toBeGreaterThan(0)
    expect(routePermission).toBeDefined()
    expect(checked.length).toBeGreaterThan(0)
  })

  it('checks only permissions the module actually REGISTERS', () => {
    // Not "appears in the source": Permissions() is what grants them by verb tier.
    expect(checked.filter((p) => !registered.has(p))).toEqual([])
  })

  it('gates the policy action on exactly what PUT /policies demands', () => {
    expect(registered.has(routePermission as string)).toBe(true)
    expect(checked).toContain(routePermission)
  })

  it('asks for an ADMIN-verb permission, which is the tier this gate needs', () => {
    // This assertion used to call roleAllows for editor and admin. That helper is gone,
    // and removing it was right: the console no longer models the engine's tiers at all
    // — /v1/auth/whoami hands each grant its EFFECTIVE permission set and can() is set
    // membership. A client-side re-assertion of the tier would now be checking a copy of
    // the rule that no longer exists, which is the drift this whole change deletes.
    //
    // So the guarantee MOVED rather than disappeared, and it is not dropped here: the
    // engine asserts it for EVERY module permission at once in
    // cmd/olivares/tools/permsdump (TestModuleAdminVerbIsNeverGrantedToEditor), where
    // the whole declaration inventory is in scope, instead of one feature restating it.
    //
    // What stays client-side is the half that is genuinely the console's: it must ask
    // for a permission of the tier this action needs, and the tests above already pin
    // that it asks for exactly what PUT /policies demands.
    for (const permission of checked) {
      expect(permission.split(':').at(-1)).toBe('admin')
    }
  })
})
