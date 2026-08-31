// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { describe, expect, it } from 'vitest'

describe('eventing fixtures', () => {
  it('⛔ sirve status y compat como un estado coherente que el motor puede emitir', async () => {
    // Keep the standalone AT fixture outside the application's tsc graph. Vitest still
    // loads the real module and calls its real dispatcher; a literal import here would
    // make every unrelated visual fixture part of the production TypeScript build.
    const fixtureModule = '../../../e2e-visual/fixtures.ts'
    const { fixtureFor } = (await import(/* @vite-ignore */ fixtureModule)) as {
      fixtureFor: (pathname: string) => unknown | null
    }

    const status = fixtureFor('/v1/m/eventing/egress-policy')
    const compat = fixtureFor('/v1/m/eventing/egress-policy/compat')

    expect(status).toStrictEqual({
      in_force: false,
      mode: 'legacy_compat',
      classified_mode: 'legacy_compat',
      enforcement_committed: false,
      writer_fence: {
        armed: false,
        mode: 'legacy_compat',
        generation: 1,
        required_capability: 0,
        binary_capability: 1,
      },
      compat: {
        seeded: true,
        recorded: 0,
        still_needed: 0,
        intact: true,
        unparsable: 0,
      },
    })
    expect(compat).toStrictEqual({
      seeded: true,
      intact: true,
      seeded_at: '2026-08-23T09:00:00Z',
      seed_digest:
        'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
      subscriptions: 0,
      unparsed: 0,
      authorities: [],
      still_needed: 0,
    })

    const statusDTO = status as {
      writer_fence: { binary_capability: number }
      compat: { seeded: boolean; recorded: number; still_needed: number }
    }
    const compatDTO = compat as {
      seeded: boolean
      authorities: unknown[]
      still_needed: number
      seed_digest: string
    }
    expect(statusDTO.writer_fence.binary_capability).toBe(1)
    expect(statusDTO.compat.seeded).toBe(compatDTO.seeded)
    expect(statusDTO.compat.recorded).toBe(compatDTO.authorities.length)
    expect(statusDTO.compat.still_needed).toBe(compatDTO.still_needed)
    expect(compatDTO.seed_digest).toMatch(/^[0-9a-f]{64}$/)
    expect(fixtureFor('/v1/m/eventing/no-existe-esta-ruta')).toBeNull()
  })
})
