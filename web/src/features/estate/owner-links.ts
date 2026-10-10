// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { EstateFamily, EstateNode } from './types'

export const estateOwnerPaths: Record<EstateFamily, string> = {
  workspace: '/console',
  folder: '/agentops',
  session: '/agentops',
  work: '/work',
  connector: '/console',
  mcp: '/mcp-servers',
  policy: '/permissions',
}

export function estateOwnerHref(node: EstateNode): string {
  const path = estateOwnerPaths[node.kind]
  if (node.kind === 'session')
    return `${path}?${new URLSearchParams({ session: `run:${node.ref}`, pane: 'context' })}`
  if (node.kind === 'work')
    return `${path}?${new URLSearchParams({ item: node.ref, detail: 'dependencies' })}`
  if (node.kind === 'policy') return `${path}?tab=policies`
  if (node.kind === 'workspace') return `${path}?tab=scopes`
  if (node.kind === 'connector') return `${path}?tab=connectors`
  return path
}
