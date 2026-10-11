// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
export const ESTATE_FAMILIES = [
  'workspace',
  'folder',
  'session',
  'work',
  'connector',
  'mcp',
  'policy',
] as const
export type EstateFamily = (typeof ESTATE_FAMILIES)[number]

export interface EstateNode {
  kind: EstateFamily
  ref: string
  label: string
  status: string
  workspaceId?: string
  folderRef?: string
  peerMode?: 'explicit' | 'same-template'
}

export interface EstatePage {
  nodes: EstateNode[]
  hasMore: boolean
  cursor?: string
  readAt: string
}

export function estateNodeKey(tenant: string | null, node: EstateNode): string {
  return JSON.stringify([tenant, node.kind, node.ref])
}
