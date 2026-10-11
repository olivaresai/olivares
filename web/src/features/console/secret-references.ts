// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SECRET STORE, ORGANIZED: folders, references and "used by", read from what the
// engine already serves. Nothing here is a second store: folders are the `/` in a
// secret's own name, and "used by" is every `<scheme>:<locator>` reference the source
// roster and the MCP gateway already persist, plus the vault names a session run
// records (modules/sessions/session_secret_env.go).

import type { RunDTO } from '@/features/agentops/types'
import { SECRET_SCHEMES } from '@/features/deploy/types'
import { looksLikeCredential } from '@/lib/credentials'
import type { SourceRosterEntry } from './api'
import type { MCPServer } from './mcp-gateway-api'

/** The schemes the engine resolves (core/secret/resolver.go knownSchemes): the
 * store, its `db` alias, and the external backends. */
const SCHEMES = new Set<string>(['store', 'db', ...SECRET_SCHEMES])

export interface SecretReference {
  /** Lower-cased, `db` folded to `store`. */
  scheme: string
  locator: string
}

/** A config value as the engine reads it: a reference, or `null` for a literal.
 * A value carrying an inline credential is never a reference (the engine's
 * ParseReference refuses it too), so it is never painted as one. */
export function parseSecretReference(value: string): SecretReference | null {
  if (looksLikeCredential(value)) return null
  const colon = value.indexOf(':')
  if (colon < 0) return null
  const scheme = value.slice(0, colon).trim().toLowerCase()
  const locator = value.slice(colon + 1).trim()
  if (!SCHEMES.has(scheme) || !locator) return null
  return { scheme: scheme === 'db' ? 'store' : scheme, locator }
}

/** `prod/db/password` → `prod/db`; a flat name lives at the root (`''`). */
export function secretFolder(name: string): string {
  const slash = name.lastIndexOf('/')
  return slash < 0 ? '' : name.slice(0, slash)
}

/** Items grouped by folder: the root first, then folders in name order. */
export function groupByFolder<T extends { name: string }>(
  items: readonly T[],
): { folder: string; items: T[] }[] {
  const groups = new Map<string, T[]>()
  for (const item of items) {
    const folder = secretFolder(item.name)
    const group = groups.get(folder)
    if (group) group.push(item)
    else groups.set(folder, [item])
  }
  return [...groups.entries()]
    .sort(([a], [b]) => (a === '' ? -1 : b === '' ? 1 : a.localeCompare(b)))
    .map(([folder, grouped]) => ({ folder, items: grouped }))
}

/** One thing that reads a secret: a connection or a session. */
export interface SecretUse {
  kind: 'source' | 'mcp' | 'session'
  /** What the user reads: the name, or the run reference of an unnamed run. */
  label: string
  /** The identity two uses are told apart by: two runs may share a name. */
  id: string
}

/** A reference to a secret kept outside the store: shown, never edited here. */
export interface ExternalReference {
  reference: string
  usedBy: SecretUse[]
}

export interface SecretUsage {
  /** Store secret name → what references it. */
  store: Map<string, SecretUse[]>
  /** References to other backends, in reference order. */
  external: ExternalReference[]
}

/** Every reference the given rosters hold, keyed by the secret it names. */
export function collectSecretUsage(input: {
  sources?: readonly SourceRosterEntry[]
  mcpServers?: readonly MCPServer[]
  runs?: readonly RunDTO[]
}): SecretUsage {
  const store = new Map<string, SecretUse[]>()
  const external = new Map<string, SecretUse[]>()
  const add = (to: Map<string, SecretUse[]>, key: string, use: SecretUse) => {
    const uses = to.get(key)
    if (!uses) to.set(key, [use])
    else if (!uses.some((u) => u.kind === use.kind && u.id === use.id))
      uses.push(use)
  }
  const addRef = (value: string | undefined, use: SecretUse) => {
    const ref = value ? parseSecretReference(value) : null
    if (!ref) return
    if (ref.scheme === 'store') add(store, ref.locator, use)
    else add(external, `${ref.scheme}:${ref.locator}`, use)
  }
  for (const source of input.sources ?? []) {
    for (const value of Object.values(source.config ?? {}))
      addRef(value, { kind: 'source', label: source.name, id: source.name })
  }
  for (const server of input.mcpServers ?? []) {
    const use: SecretUse = { kind: 'mcp', label: server.name, id: server.id }
    addRef(server.credential_ref, use)
    for (const value of Object.values(server.env_secret_refs ?? {}))
      addRef(value, use)
  }
  for (const run of input.runs ?? []) {
    // A run records vault NAMES (env/…), not references.
    for (const ref of run.secret_env ?? [])
      add(store, ref.secret, {
        kind: 'session',
        label: run.name || run.run_ref,
        id: run.run_ref,
      })
  }
  return {
    store,
    external: [...external.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([reference, usedBy]) => ({ reference, usedBy })),
  }
}
