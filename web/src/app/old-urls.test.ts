// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Every address the Tools fold retired (#3388), and where it lands now. Each
// one redirects through the real route tree, keeping the query it carried and the anchor;
// a bookmark or a runbook link opens the place that holds the page now, never a 404.
import { createMemoryHistory, createRouter } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { routeTree } from './routes'

const OLD_URLS: readonly {
  from: string
  path: string
  search?: Record<string, string>
  hash?: string
}[] = [
  // Provider keys left Models for AI tools, beside the API keys.
  {
    from: '/models?tab=keys',
    path: '/agent-tools',
    hash: 'provider-key-references',
  },
  // MCP servers: one destination, MCP & skills' Servers tab (the gateway and the catalog).
  { from: '/mcp-servers', path: '/capabilities', search: { tab: 'servers' } },
  {
    from: '/mcp-servers?server=github#trust',
    path: '/capabilities',
    search: { tab: 'servers', server: 'github' },
    hash: 'trust',
  },
  {
    from: '/console?tab=mcpGateway',
    path: '/capabilities',
    search: { tab: 'servers' },
  },
  // Skills: one destination, MCP & skills' Skills tab (the pack catalog and what was found).
  { from: '/skills', path: '/capabilities', search: { tab: 'skills' } },
  {
    from: '/skills?pack=p1',
    path: '/capabilities',
    search: { tab: 'skills', pack: 'p1' },
  },
]

async function land(from: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [from] }),
  })
  await router.load()
  return router.state.location
}

describe('the addresses the Tools fold retired', () => {
  for (const row of OLD_URLS)
    it(`${row.from} lands on ${row.path}`, async () => {
      const at = await land(row.from)
      expect(at.pathname).toBe(row.path)
      if (row.search)
        expect(at.search).toEqual(expect.objectContaining(row.search))
      if (row.hash) expect(at.hash).toBe(row.hash)
    })
})
