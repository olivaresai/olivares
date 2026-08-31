// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { selectionFromNodeData } from './selection'

describe('access-map node selection contract', () => {
  it('preserves a vendor resource kind instead of guessing from it', () => {
    expect(
      selectionFromNodeData('resource-7', {
        kind: 'postgres.table',
        label: 'appdb.public.secrets',
        role: 'resource',
      }),
    ).toEqual({
      type: 'node',
      id: 'resource-7',
      kind: 'postgres.table',
      ref: 'appdb.public.secrets',
      role: 'resource',
      cluster: false,
    })
  })

  it('preserves the synthetic-cluster guard used by both renderers', () => {
    expect(
      selectionFromNodeData('cluster:appdb.public', {
        kind: 'postgres.table',
        label: 'appdb.public (1201)',
        role: 'resource',
        cluster: true,
      }),
      'EXFIL_CLUSTER_CONTRACT: synthetic cluster IDs must remain marked and must not reach resource-scoped exfil',
    ).toMatchObject({ role: 'resource', cluster: true })
  })
})
