// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { MCPServerInput } from './mcp-gateway-api'

/** A curated server the administrator can add in one step. It only fills the Add server
 * form: nothing is saved until Save, and a saved server stays off until it is tested and
 * enabled. Versions are pinned; the licence is the one the package or project declares. */
export interface MCPStarter {
  key: 'filesystem' | 'git' | 'github' | 'gitlab' | 'playwright'
  licence: string
  source: string
  input: MCPServerInput
}

const command = (
  name: string,
  program: string,
  args: string[],
  egressHosts: string[],
): MCPServerInput => ({
  name,
  transport: 'stdio',
  url: '',
  command: program,
  args,
  // The command reaches only these hosts: its package registry (K6.A11).
  egress_hosts: egressHosts,
  egress_cidrs: [],
  trust: {},
  enabled: false,
})

const remote = (name: string, url: string): MCPServerInput => ({
  name,
  transport: 'streamable_http',
  // The engine dials only this address, with pinned addresses and no redirects.
  url,
  egress_cidrs: [],
  trust: {},
  enabled: false,
})

export const mcpStarters: readonly MCPStarter[] = [
  {
    key: 'filesystem',
    licence: 'Apache-2.0 and MIT',
    source: 'https://github.com/modelcontextprotocol/servers',
    input: command(
      'Filesystem',
      'npx',
      ['-y', '@modelcontextprotocol/server-filesystem@2026.8.31', '.'],
      ['registry.npmjs.org'],
    ),
  },
  {
    key: 'git',
    licence: 'MIT',
    source: 'https://github.com/modelcontextprotocol/servers',
    input: command(
      'Git',
      'uvx',
      ['mcp-server-git==2026.8.18'],
      ['pypi.org', 'files.pythonhosted.org'],
    ),
  },
  {
    key: 'github',
    licence: 'MIT',
    source: 'https://github.com/github/github-mcp-server',
    input: remote('GitHub', 'https://api.githubcopilot.com/mcp/'),
  },
  {
    key: 'gitlab',
    licence: 'GitLab service',
    source: 'https://docs.gitlab.com/user/model_context_protocol/mcp_server/',
    input: remote('GitLab', 'https://gitlab.com/api/v4/mcp'),
  },
  // ponytail: the egress helper exports its proxy CA only in variables Chrome does
  // not read (an internal design note (not shipped) proxyEnvironment), so HTTPS browsing behind this
  // profile is expected to fail and is not yet measured; give the browser that
  // trust, then qualify it. The starter text says it is not qualified.
  {
    key: 'playwright',
    licence: 'Apache-2.0',
    source: 'https://github.com/microsoft/playwright-mcp',
    input: command(
      'Playwright',
      'npx',
      ['-y', '@playwright/mcp@0.0.83', '--headless', '--isolated'],
      ['registry.npmjs.org'],
    ),
  },
]
