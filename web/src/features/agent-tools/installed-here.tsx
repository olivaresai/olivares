// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// "Installed here": one boxed list that answers "what does a new session in this
// workspace receive". Each row reads what the launch itself reads: the tool program
// from the launch resolver (inventory launch_programs), the programs and npm
// registries from the provider profile the launch resolves (GET
// /v1/m/sessions/provider-profiles/resolve), the skills from the launch's own
// selection (GET /v1/m/skills/selection), the MCP servers from the tenant gateway
// snapshot the launch configures from. Each item links to the page that manages it.
import { useQueries, useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { agentOpsApi } from '@/features/agentops/api'
import { profileName } from '@/features/agentops/types'
import { consoleApi, consoleKeys } from '@/features/console/api'
import {
  mcpGatewayApi,
  type MCPGatewaySnapshot,
} from '@/features/console/mcp-gateway-api'
import { skillsApi, skillsKeys } from '@/features/skills/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { useModulesStore } from '@/stores/modules'
import { useWorkspaceStore } from '@/stores/workspace'
import type { ToolInventory } from './api'
import { toolLabel, type toolFacts } from './inventory'

/** The workspace switcher's page: the store's ceiling (sqlstore maxLimit). */
const WORKSPACE_PAGE = 1000
/** The drivers a session hands the MCP gateway to (cmd/olivares/mcpmanagement_launch.go). */
const MCP_DRIVERS = ['claude', 'codex', 'opencode']

/** The preview's codes for a tool no session can start on yet
 * (sessions provider_profile_resolve.go): such a launch receives nothing. */
const REFUSED = new Set(['tool_not_installed', 'nothing_to_run_on'])

const message = (err: unknown) => (err instanceof Error ? err.message : '')
/** A version is shown only when it reads as one, not a tool's other output. */
const versionOnly = (raw?: string) =>
  raw && /^v?\d+(\.\d+)+/.test(raw) ? raw : undefined

/** The servers a launch configures: a stored gateway's enabled servers, and its own
 * session tools when on (sessionMCPAvailable; a file-sourced gateway configures none). */
function sessionMCPServers(snapshot: MCPGatewaySnapshot) {
  return snapshot.source === 'store'
    ? snapshot.servers.filter((server) => server.enabled)
    : []
}

export function InstalledHere({
  scope,
  inventory,
  facts,
}: {
  scope: readonly unknown[]
  inventory: ToolInventory
  facts: ReturnType<typeof toolFacts>
}) {
  const { t } = useTranslation('agentTools')
  const { activeTenant, can } = useAuth()
  const { activeWorkspace, activeWorkspaceName } = useWorkspaceStore()
  const skillsOff = useModulesStore((s) => s.off.has('skills'))
  // "All workspaces" launches in the default one (sessions resolveRunAuthzWorkspace).
  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, { limit: WORKSPACE_PAGE }),
    queryFn: () => consoleApi.listWorkspaces({ limit: WORKSPACE_PAGE }),
    enabled: !!activeTenant && !activeWorkspace,
    staleTime: 60_000,
  })
  const fallback = workspaces.data?.items.find((w) => w.is_default)
  const workspaceID = activeWorkspace ?? fallback?.id
  const workspaceName = activeWorkspace
    ? (activeWorkspaceName ?? activeWorkspace)
    : fallback?.name
  // No workspace to resolve: the read failed, stopped at its page before the default
  // one, or answered without one.
  const workspaceState =
    workspaceID || workspaces.isPending
      ? undefined
      : workspaces.isError
        ? t('installed.readFailed', { message: message(workspaces.error) })
        : workspaces.data?.has_more
          ? t('installed.workspacesTruncated')
          : t('installed.noWorkspace')
  const skillsReadable = !skillsOff && can('skills:catalog:read')
  const skills = useQuery({
    queryKey: [
      ...scope,
      ...skillsKeys.selection(activeTenant, 'workspace', workspaceID ?? ''),
    ],
    queryFn: ({ signal }) =>
      skillsApi.selection('workspace', workspaceID!, signal),
    enabled: !!activeTenant && !!workspaceID && skillsReadable,
  })
  // Each installed tool's new session runs under the profile the engine's rule picks;
  // the preview names it without creating one (none yet: the launch creates one that
  // declares nothing). A tool no session can start on yet receives nothing.
  const programs = inventory.launch_programs ?? {}
  const drivers = Object.keys(programs).sort()
  const profilesReadable = can('sessions:profile:read')
  const previews = useQueries({
    queries: drivers.map((driver) => ({
      queryKey: [...scope, 'installed-profile', activeTenant, driver],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        agentOpsApi.previewProfile(driver, {
          signal,
          tenant: activeTenant ?? undefined,
        }),
      enabled: !!activeTenant && profilesReadable,
    })),
  })
  // Resolve the workspace and profile together on the server, so a profile's
  // skill of the same name replaces the workspace's skill exactly as at launch.
  const profileSkills = useQueries({
    queries: drivers.map((driver, i) => {
      const ref = previews[i]?.data?.profile?.profile_ref
      return {
        queryKey: [
          ...scope,
          ...skillsKeys.selection(
            activeTenant,
            'workspace',
            workspaceID ?? '',
            ref,
          ),
          driver,
        ],
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          skillsApi.selection('workspace', workspaceID!, signal, ref),
        enabled:
          !!activeTenant &&
          !!workspaceID &&
          !!ref &&
          skillsReadable &&
          profilesReadable,
      }
    }),
  })
  const mcp = useQuery({
    queryKey: [...scope, 'installed-mcp', activeTenant],
    queryFn: ({ signal }) =>
      mcpGatewayApi.get({ signal, tenant: activeTenant ?? undefined }),
    enabled: !!activeTenant,
  })
  if (!activeTenant) return null

  const boxOf = (driver: string) =>
    facts.find((f) => f.driver === driver)?.installed
      ? `#tool-${driver}`
      : undefined
  const tools = drivers.map((driver) => {
    const fact = facts.find((f) => f.driver === driver)
    // The version of the managed install the launch runs; a program found on the
    // PATH (no managed install) has the version the tool itself reports.
    const run = inventory.inventory.installed.find(
      (row) => row.executable === programs[driver],
    )
    return {
      key: driver,
      label: toolLabel(driver),
      detail: versionOnly(
        run ? run.version : fact?.installs.length ? undefined : fact?.version,
      ),
      // Its box on this page, when the page lists the tool.
      href: boxOf(driver),
    }
  })
  const refused = (err: unknown) =>
    err instanceof ApiError && REFUSED.has(err.code)
  const previewFailed = previews.find((q) => q.isError && !refused(q.error))
  const refusedTools = drivers
    .filter((_, i) => refused(previews[i]?.error))
    .map(toolLabel)
  const profiles = drivers.flatMap((driver, i) => {
    const profile = previews[i]?.data?.profile
    return profile ? [{ driver, profile }] : []
  })
  // Programs and registries (URL and scope; the token's vault entry is never shown).
  const packages = profiles.flatMap(({ driver, profile }) => {
    const tool = drivers.length > 1 ? toolLabel(driver) : undefined
    return [
      ...(profile.session_programs ?? []).map((name) => ({
        key: `${driver}/program/${name}`,
        label: name,
        detail: tool,
        href: boxOf(driver),
      })),
      ...(profile.session_registries ?? []).map((reg) => ({
        key: `${driver}/registry/${reg.scope ?? ''}/${reg.url}`,
        label: reg.scope ? `${reg.scope} ${reg.url}` : reg.url,
        detail: tool,
        href: boxOf(driver),
      })),
    ]
  })
  // Hosts an administrator declared a confined session may reach beside its provider.
  const egressHosts = profiles.flatMap(({ driver, profile }) =>
    (profile.session_egress_hosts ?? []).map((host) => ({
      key: `${driver}/host/${host}`,
      label: host,
      detail: drivers.length > 1 ? toolLabel(driver) : undefined,
      href: boxOf(driver),
    })),
  )
  const skillItems = new Map<
    string,
    { key: string; label: string; href: string; detail?: string }
  >()
  const selections =
    drivers.length > 0
      ? drivers.flatMap((driver, i) =>
          refused(previews[i]?.error)
            ? []
            : [
                {
                  driver,
                  selection: previews[i]?.data?.profile
                    ? profileSkills[i]?.data
                    : skills.data,
                },
              ],
        )
      : [{ driver: '', selection: skills.data }]
  for (const { driver, selection } of selections) {
    for (const member of selection?.members ?? []) {
      const key = `${member.pack_revision_id}/${member.name}`
      const item = skillItems.get(key)
      const tool = drivers.length > 1 ? toolLabel(driver) : undefined
      if (item) {
        if (tool && !item.detail?.includes(tool))
          item.detail = [item.detail, tool].filter(Boolean).join(', ')
      } else {
        skillItems.set(key, {
          key,
          label: member.name,
          href: `/skills?pack=${encodeURIComponent(member.pack_id)}`,
          detail: tool,
        })
      }
    }
  }
  const profileSkillsFailed = profileSkills.find((q) => q.isError)
  const profileSkillsPending = drivers.some(
    (_, i) => previews[i]?.data?.profile && profileSkills[i]?.isPending,
  )
  const servers = mcp.data
    ? [
        ...(mcp.data.source === 'store' && mcp.data.session_tools
          ? [{ key: 'olivares', label: t('installed.olivaresTools') }]
          : []),
        ...sessionMCPServers(mcp.data).map((server) => ({
          key: server.id,
          label: server.name,
        })),
      ].map((item) => ({ ...item, href: '/mcp-servers' }))
    : []

  return (
    <section
      aria-labelledby="installed-here"
      data-testid="installed-here"
      className="overflow-hidden rounded-card border border-line bg-surface"
    >
      <div className="flex flex-col gap-0.5 px-4 py-3">
        <h2 id="installed-here" className="text-body-l font-semibold text-text">
          {t('installed.title')}
        </h2>
        <p className="text-caption text-text-2">
          {workspaceName
            ? t('installed.workspace', { name: workspaceName })
            : (workspaceState ?? t('installed.reading'))}
        </p>
      </div>
      <ul className="divide-y divide-line border-t border-line">
        <Row
          label={t('installed.tools')}
          items={tools}
          hint={tools.length > 0 ? t('installed.toolsHint') : undefined}
        />
        <Row
          label={t('installed.packages')}
          items={packages}
          state={
            !profilesReadable
              ? t('installed.profilesNotReadable')
              : previewFailed
                ? t('installed.readFailed', {
                    message: message(previewFailed.error),
                  })
                : previews.some((q) => q.isPending)
                  ? t('installed.reading')
                  : undefined
          }
          hint={[
            profiles.length > 0
              ? t('installed.packagesHint', {
                  profiles: profiles
                    .map(
                      ({ driver, profile }) =>
                        `${toolLabel(driver)}: ${profileName(profile) ?? profile.profile_ref}`,
                    )
                    .join(', '),
                })
              : t('installed.packagesNoProfile'),
            ...(refusedTools.length > 0
              ? [
                  t('installed.packagesRefused', {
                    tools: refusedTools.join(', '),
                  }),
                ]
              : []),
          ].join(' ')}
        />
        {egressHosts.length > 0 && (
          <Row label={t('installed.egressHosts')} items={egressHosts} />
        )}
        <Row
          label={t('installed.skills')}
          items={[...skillItems.values()]}
          state={
            skillsOff
              ? t('installed.skillsOff')
              : !skillsReadable
                ? t('installed.notReadable')
                : !profilesReadable && drivers.length > 0
                  ? t('installed.profilesNotReadable')
                  : (workspaceState ??
                    (skills.isError || profileSkillsFailed || previewFailed
                      ? t('installed.readFailed', {
                          message: message(
                            skills.error ??
                              profileSkillsFailed?.error ??
                              previewFailed?.error,
                          ),
                        })
                      : skills.isPending ||
                          profileSkillsPending ||
                          previews.some((q) => q.isPending)
                        ? t('installed.reading')
                        : undefined))
          }
          hint={t('installed.skillsHint')}
        />
        <Row
          label={t('installed.mcp')}
          items={servers}
          state={
            mcp.isError
              ? t('installed.readFailed', { message: message(mcp.error) })
              : mcp.isPending
                ? t('installed.reading')
                : undefined
          }
          hint={
            servers.length > 0
              ? t('installed.mcpDrivers', {
                  tools: MCP_DRIVERS.map(toolLabel).join(', '),
                })
              : undefined
          }
        />
      </ul>
    </section>
  )
}

/** One kind of resource: its label, then each item as a link to its own page. */
function Row({
  label,
  items,
  state,
  hint,
}: {
  label: string
  items: { key: string; label: string; detail?: string; href?: string }[]
  /** A read still running or failed: said instead of the items. */
  state?: string
  hint?: ReactNode
}) {
  const { t } = useTranslation('agentTools')
  return (
    <li className="flex flex-col gap-1 px-4 py-3 sm:flex-row sm:gap-4">
      <h3 className="shrink-0 text-body font-medium text-text sm:w-32">
        {label}
      </h3>
      <div className="flex min-w-0 flex-col gap-1">
        {state ? (
          <p role="status" className="break-words text-caption text-text-2">
            {state}
          </p>
        ) : items.length === 0 ? (
          <p className="text-caption text-text-3">{t('installed.none')}</p>
        ) : (
          <ul className="flex flex-wrap gap-x-4 gap-y-1">
            {items.map((item) => (
              <li key={item.key} className="min-w-0">
                {item.href ? (
                  <a
                    href={item.href}
                    className="break-words text-body text-accent-text underline underline-offset-4"
                  >
                    {item.label}
                  </a>
                ) : (
                  <span className="break-words text-body text-text">
                    {item.label}
                  </span>
                )}
                {item.detail ? (
                  <span className="ms-1.5 text-overline text-text-3">
                    {item.detail}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        )}
        {hint && !state ? (
          <p className="text-caption text-text-3">{hint}</p>
        ) : null}
      </div>
    </li>
  )
}
