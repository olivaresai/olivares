// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Selection } from './detail'

type NodeSelection = Extract<NonNullable<Selection>, { type: 'node' }>

/** Preserve the engine-facing node role for both graph renderers. Resource kinds are
 * vendor-specific (for example `postgres.table`), so they cannot be inferred from
 * `kind`; aggregate nodes are synthetic and must remain identifiable as clusters. */
export function selectionFromNodeData(
  id: string,
  data: unknown,
): NodeSelection {
  const node = data as {
    kind?: unknown
    label?: unknown
    role?: unknown
    cluster?: unknown
  }
  return {
    type: 'node',
    id,
    kind: String(node.kind ?? ''),
    ref: String(node.label ?? ''),
    role: node.role === 'resource' ? 'resource' : 'origin',
    cluster: node.cluster === true,
  }
}
