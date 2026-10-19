// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { describe, expect, it } from 'vitest'
import type { WorkspaceContentsDTO } from './api'

// The AT gate answers any path its fixture does not know with `{ items: [], has_more: false }`.
// /workspace filters `kinds` and /agent-tools maps `pins`; the engine always sends both arrays
// (handleWorkspaceContents makes `Kinds`, toolPins starts from an empty slice), so a fixture
// without them renders the error boundary and axe measures the error page, not the view.
describe('AT fixtures for the arrays the views map and filter', () => {
  // Keep the standalone AT fixture outside the application's tsc graph (same reason as
  // egress-policy-fixtures.test.ts): Vitest loads the real module and calls its real dispatcher.
  const load = async () => {
    const fixtureModule = '../../../e2e-visual/fixtures.ts'
    return (await import(/* @vite-ignore */ fixtureModule)) as {
      fixtureFor: (pathname: string) => unknown | null
    }
  }

  it('⛔ answers the workspace contents read with the kinds array and a held kind to render', async () => {
    const { fixtureFor } = await load()

    const contents = fixtureFor(
      '/v1/workspaces/w-default/contents',
    ) as WorkspaceContentsDTO | null

    expect(contents?.workspace_id).toBe('w-default')
    expect(Array.isArray(contents?.kinds)).toBe(true)
    expect(contents?.kinds.some((row) => row.count > 0)).toBe(true)
  })

  it('⛔ answers the tool version pins read with the pins array', async () => {
    const { fixtureFor } = await load()

    const pins = fixtureFor('/v1/m/agenttools/pins') as {
      pins?: unknown
    } | null

    expect(Array.isArray(pins?.pins)).toBe(true)
  })
})
