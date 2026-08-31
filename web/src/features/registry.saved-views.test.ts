// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Structural guard (plan 3.7): the saved-views namespace is a DATA key,
// not a label. `savedViewsApi.list(featureId)` and the server's
// (tenant, feature, owner, name) unique index partition stored views by it, and
// consoleviews validates only the slug FORMAT — nothing anywhere enforces that
// two views do not claim the same namespace. When they do, one feature's saved
// views appear in the other's menu and can be overwritten by name collision.
// That is not a UI blemish; it is two features writing each other's rows.
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS } from './registry'

/** consoleviews.go featureIDPattern — the server refuses anything else with a 400. */
const FEATURE_ID = /^[a-z0-9][a-z0-9-]{0,63}$/

describe('FEATURE_VIEWS saved-views namespaces', () => {
  it('never lets two views share one namespace', () => {
    const byId = new Map<string, string[]>()
    for (const v of FEATURE_VIEWS) {
      if (!v.savedViewsFeatureId) continue
      byId.set(v.savedViewsFeatureId, [
        ...(byId.get(v.savedViewsFeatureId) ?? []),
        v.id,
      ])
    }
    const duplicated = [...byId.entries()]
      .filter(([, ids]) => ids.length > 1)
      .map(([fid, ids]) => `${fid}: ${ids.join(', ')}`)

    expect(
      duplicated,
      `A reused savedViewsFeatureId MIXES the stored views of two features (server-side, by feature_id):\n${duplicated.join('\n')}`,
    ).toEqual([])
  })

  it('only declares namespaces the engine will accept', () => {
    const bad = FEATURE_VIEWS.filter(
      (v) => v.savedViewsFeatureId && !FEATURE_ID.test(v.savedViewsFeatureId),
    ).map((v) => `${v.id}: ${v.savedViewsFeatureId}`)

    expect(
      bad,
      `consoleviews validates feature_id against ${FEATURE_ID} and 400s otherwise. Note the registry's own ids are camelCase and would fail:\n${bad.join('\n')}`,
    ).toEqual([])
  })

  it('keeps the namespace already in production for the audit explorer', () => {
    // audit-view has shipped saved views under the literal 'audit' since.
    // Renaming the namespace would orphan every view an auditor already saved,
    // silently: the rows stay in the database and stop being listed.
    const audit = FEATURE_VIEWS.find((v) => v.id === 'audit')
    expect(audit?.savedViewsFeatureId).toBe('audit')
  })
})
