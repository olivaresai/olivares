// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import type { SourceRosterEntry } from './api'
import type { MCPServer } from './mcp-gateway-api'
import {
  collectSecretUsage,
  groupByFolder,
  parseSecretReference,
} from './secret-references'

describe('parseSecretReference', () => {
  it('reads the engine grammar and folds db into store', () => {
    expect(parseSecretReference('store:prod/db/password')).toEqual({
      scheme: 'store',
      locator: 'prod/db/password',
    })
    expect(parseSecretReference('DB:prod/db/password')?.scheme).toBe('store')
    expect(parseSecretReference('vault:kv/data/app#token')).toEqual({
      scheme: 'vault',
      locator: 'kv/data/app#token',
    })
  })

  it('treats literals, unknown schemes and empty locators as no reference', () => {
    expect(parseSecretReference('hunter2')).toBeNull()
    // An inline credential is never painted as a reference.
    expect(parseSecretReference('vault://user:pw@vault.test/kv')).toBeNull()
    expect(parseSecretReference('https://example.test/x')).toBeNull()
    expect(parseSecretReference('store:')).toBeNull()
  })
})

describe('groupByFolder', () => {
  it('puts slash names under their folder and flat names at the root', () => {
    const groups = groupByFolder([
      { name: 'prod/db/password' },
      { name: 'dev/db/password' },
      { name: 'flat' },
      { name: 'prod/db/user' },
    ])
    expect(groups.map((g) => [g.folder, g.items.map((i) => i.name)])).toEqual([
      ['', ['flat']],
      ['dev/db', ['dev/db/password']],
      ['prod/db', ['prod/db/password', 'prod/db/user']],
    ])
  })
})

describe('collectSecretUsage', () => {
  it('names the connectors, MCP servers and sessions that reference a secret', () => {
    const sources = [
      {
        name: 'warehouse',
        tenant: 't',
        enabled: true,
        status: 'running',
        config: {
          host: 'db.internal',
          password: 'store:prod/db/password',
          token: 'vault:kv/warehouse#token',
        },
      },
      {
        name: 'replica',
        tenant: 't',
        enabled: true,
        status: 'running',
        config: { password: 'db:prod/db/password' },
      },
    ] as SourceRosterEntry[]
    const mcpServers = [
      {
        id: 'srv_1',
        name: 'github',
        credential_ref: 'store:mcp/github',
        env_secret_refs: { GH_TOKEN: 'store:mcp/github' },
      },
    ] as unknown as MCPServer[]
    const runs = [
      {
        run_ref: 'run_1',
        name: 'nightly',
        secret_env: [{ env: 'API', secret: 'env/api' }],
      },
      { run_ref: 'run_2', secret_env: [{ env: 'API', secret: 'env/api' }] },
      // Two runs may share a name; each is its own use.
      {
        run_ref: 'run_3',
        name: 'nightly',
        secret_env: [{ env: 'API', secret: 'env/api' }],
      },
    ] as RunDTO[]

    const usage = collectSecretUsage({ sources, mcpServers, runs })

    expect(usage.store.get('prod/db/password')).toEqual([
      { kind: 'source', label: 'warehouse', id: 'warehouse' },
      { kind: 'source', label: 'replica', id: 'replica' },
    ])
    // One server referencing one secret twice is one use.
    expect(usage.store.get('mcp/github')).toEqual([
      { kind: 'mcp', label: 'github', id: 'srv_1' },
    ])
    expect(usage.store.get('env/api')).toEqual([
      { kind: 'session', label: 'nightly', id: 'run_1' },
      { kind: 'session', label: 'run_2', id: 'run_2' },
      { kind: 'session', label: 'nightly', id: 'run_3' },
    ])
    // A literal setting is not a reference; another backend is external.
    expect(usage.store.has('db.internal')).toBe(false)
    expect(usage.external).toEqual([
      {
        reference: 'vault:kv/warehouse#token',
        usedBy: [{ kind: 'source', label: 'warehouse', id: 'warehouse' }],
      },
    ])
  })
})
