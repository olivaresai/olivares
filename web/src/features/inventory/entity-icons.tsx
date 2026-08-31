// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  Bot,
  Box,
  Boxes,
  Cloud,
  Cpu,
  Database,
  KeyRound,
  type LucideIcon,
  Radio,
  Server,
  Sparkles,
  Wrench,
} from 'lucide-react'

/** Icon per entity kind — shared by the catalog table, the detail sheet and the
 * topology view so a kind reads the same everywhere. */
export const ENTITY_ICON: Record<string, LucideIcon> = {
  agent: Bot,
  session: Radio,
  identity: KeyRound,
  mcp_server: Server,
  tool: Wrench,
  resource: Database,
  skill: Sparkles,
  model: Cpu,
  provider: Cloud,
}

// NOTE: select an icon by INDEXING ENTITY_ICON (with Box as the fallback) at the
// call site — e.g. `const Icon = ENTITY_ICON[kind] ?? Box`. Don't wrap it in a
// function: a call returning a component during render trips the static-components
// lint (it reads as "creating" a component). Index access does not.

/** Ordered kinds for facets and the topology lanes (composition before capabilities). */
export const KIND_ORDER = [
  'agent',
  'session',
  'identity',
  'mcp_server',
  'tool',
  'skill',
  'model',
  'provider',
  'resource',
]

export { Box, Boxes }
