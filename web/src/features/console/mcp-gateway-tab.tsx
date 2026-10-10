// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Plus,
  PlugZap,
  KeyRound,
  RefreshCw,
  Pencil,
  Trash2,
} from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { consoleApi } from './api'
import { SecretsTab } from './secrets-tab'
import {
  mcpEgressHost,
  mcpGatewayApi,
  mcpGatewayKeys,
  mcpHTTPSURL,
  mcpPublicJWKS,
  type MCPGatewaySnapshot,
  type MCPServer,
  type MCPServerInput,
  type MCPToolPolicy,
} from './mcp-gateway-api'
import { MCPEnableDialog, toolTrust } from './mcp-enable-dialog'
import { mcpStarters, type MCPStarter } from './mcp-starters'
import './i18n'

/** `titled` is false where the page already names the surface (the MCP servers page). */
export function MCPGatewayTab({ titled = true }: { titled?: boolean }) {
  const boundary = useAuthBoundary()
  return <GatewayInner key={boundary.key} titled={titled} />
}

/** "NAME=value" lines as a map; null when a line has no name or no '='. */
function parsePairs(text: string): Record<string, string> | null {
  const out: Record<string, string> = {}
  for (const line of text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)) {
    const at = line.indexOf('=')
    if (at <= 0) return null
    out[line.slice(0, at).trim()] = line.slice(at + 1).trim()
  }
  return out
}

/** The default name of a command server: its package or program ("server-everything"
 * for npx -y @modelcontextprotocol/server-everything). */
function commandName(command: string, args: string[]): string {
  const last = [...args].reverse().find((a) => a && !a.startsWith('-'))
  const pick = last ?? command
  return (
    pick
      .split(/[\\/]/)
      .pop()
      ?.replace(/@[^@/]*$/, '') || pick
  )
}

/** The host of an endpoint, for messages and the default name; '' when it does not parse. */
function hostOf(endpoint: string): string {
  try {
    return new URL(endpoint).host
  } catch {
    return ''
  }
}

function GatewayInner({ titled }: { titled: boolean }) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const key = useMemo(
    () => mcpGatewayKeys.scope(activeTenant, boundary.epoch),
    [activeTenant, boundary.epoch],
  )
  const admitted = !!activeTenant && can('tenant:admin')
  const [editing, setEditing] = useState<MCPServer | null | undefined>(
    undefined,
  )
  // The curated server whose Add filled the form; nothing is saved until Save.
  const [starter, setStarter] = useState<MCPStarter | undefined>(undefined)
  const [remove, setRemove] = useState<MCPServer | null>(null)
  const [enabling, setEnabling] = useState<MCPServer | null>(null)
  const [credentials, setCredentials] = useState(false)
  const [actionError, setActionError] = useState('')
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) =>
      mcpGatewayApi.get({ signal, tenant: activeTenant ?? undefined }),
    enabled: admitted,
    gcTime: 0,
  })
  useEffect(
    () => () => {
      void qc.cancelQueries({ queryKey: key })
      qc.removeQueries({ queryKey: key })
      const cache = qc.getMutationCache()
      for (const m of cache.findAll({ mutationKey: key })) cache.remove(m)
    },
    [qc, key],
  )
  type Action =
    | {
        kind: 'test' | 'toggle' | 'remove'
        row: MCPServer
        version: number
        /** The list the administrator confirmed when turning the server on. */
        allowed?: MCPToolPolicy[]
      }
    | { kind: 'session'; enabled: boolean; version: number }
  const mutation = usePrivilegedMutation<Action, MCPGatewaySnapshot>({
    mutationKey: key,
    mutationFn: (a, authority) => {
      const opts = { ...authority, tenant: activeTenant ?? undefined }
      if (a.kind === 'session')
        return mcpGatewayApi.session(a.version, a.enabled, opts)
      if (a.kind === 'test')
        return mcpGatewayApi.test(a.version, a.row.id, opts)
      if (a.kind === 'remove')
        return mcpGatewayApi.remove(a.version, a.row.id, opts)
      const {
        id: _,
        probe: __,
        proposed_allow: ___,
        allowed_tools: tools,
        ...input
      } = a.row
      // Turning it on sends the list the administrator confirmed in the enable dialog
      // (Root 2026-10-02: run without approval is an explicit choice). Turning it off
      // keeps the list as set; an empty one is what a never-enabled server holds.
      const allowed = a.allowed ?? tools
      return mcpGatewayApi.put(
        a.version,
        {
          ...input,
          ...(allowed?.length ? { allowed_tools: allowed } : {}),
          enabled: !a.row.enabled,
        },
        a.row.id,
        opts,
      )
    },
    invalidateKeys: [key],
    successMessage: t('console:mcpGateway.saved'),
    onDone: () => {
      setRemove(null)
      setActionError('')
    },
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) {
        setActionError(t('console:mcpGateway.conflict'))
        void query.refetch()
        return true
      }
      if (err instanceof ApiError && err.code === 'mcp_gateway_invalid') {
        setActionError(t('console:mcpGateway.enableHint'))
        return true
      }
      return false
    },
  })
  if (!admitted) return <ForbiddenState />
  if (query.isPending) return <Spinner />
  if (query.isError)
    return query.error instanceof ApiError && query.error.isForbidden ? (
      <ForbiddenState />
    ) : (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  const data = query.data
  const disabled = data.read_only || mutation.isPending
  const act = (
    kind: 'test' | 'toggle' | 'remove',
    row: MCPServer,
    allowed?: MCPToolPolicy[],
  ) => {
    setActionError('')
    mutation.mutate({ kind, row, version: data.version, allowed })
  }
  // The sections sit one level under whatever names the surface: this tab's own h2, or the
  // page's h1 where the page already names it.
  const Section = titled ? 'h3' : 'h2'
  return (
    <div className="flex min-w-0 flex-col gap-4 pt-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        {titled && (
          <div>
            <h2 className="text-heading">{t('console:mcpGateway.title')}</h2>
            <p className="max-w-3xl text-body text-muted-foreground">
              {t('console:mcpGateway.caption')}
            </p>
          </div>
        )}
        <div className="ml-auto flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={data.read_only}
            onClick={() => setCredentials(true)}
          >
            <KeyRound />
            {t('console:mcpGateway.credentials')}
          </Button>
          <Button
            disabled={data.read_only}
            onClick={() => {
              setStarter(undefined)
              setEditing(null)
            }}
          >
            <Plus />
            {t('console:mcpGateway.add')}
          </Button>
        </div>
      </div>
      <div className="rounded-lg border border-border bg-surface p-3 text-body">
        <Badge variant={data.read_only ? 'warning' : 'neutral'}>
          {t(`console:mcpGateway.source.${data.source}`)}
        </Badge>
        <p className="mt-2 text-muted-foreground">
          {t(
            data.read_only
              ? 'console:mcpGateway.fileOwned'
              : 'console:mcpGateway.storeOwned',
          )}
        </p>
      </div>
      <section
        className="flex flex-wrap items-start justify-between gap-3 rounded-lg border border-border p-3"
        aria-label={t('console:mcpGateway.sessionTitle')}
      >
        <div className="min-w-0 flex-1">
          <Section className="text-body font-medium">
            {t('console:mcpGateway.sessionTitle')}
          </Section>
          <p className="text-body text-muted-foreground">
            {t('console:mcpGateway.sessionHint')}
          </p>
          {/* The engine's state, never a default sentence: a new tenant starts ON on 09
              (MC bae7853d), an older one may not. */}
          <p className="text-body" data-slot="session-tools-state">
            {data.session_tools
              ? t('console:mcpGateway.sessionOn')
              : t('console:mcpGateway.sessionOff')}
          </p>
          <code className="break-all text-caption">
            {data.session_endpoint}
          </code>
        </div>
        <Switch
          aria-label={t('console:mcpGateway.sessionTitle')}
          checked={data.session_tools}
          disabled={disabled}
          onCheckedChange={(enabled) =>
            mutation.mutate({ kind: 'session', enabled, version: data.version })
          }
        />
      </section>
      {actionError && (
        <p role="alert" className="text-body text-danger">
          {actionError}
        </p>
      )}
      {data.servers.length === 0 ? (
        <EmptyState
          title={t('console:mcpGateway.empty')}
          description={t('console:mcpGateway.emptyHint')}
          icon={<PlugZap />}
        />
      ) : (
        <div className="grid min-w-0 gap-3 xl:grid-cols-2">
          {data.servers.map((row) => (
            <article
              key={row.id}
              className="min-w-0 rounded-lg border border-border p-3"
              aria-label={row.name}
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <Section className="text-body font-semibold">
                  {row.name}
                </Section>
                <div className="flex flex-wrap gap-2">
                  <Badge variant={row.enabled ? 'success' : 'neutral'}>
                    {t(
                      row.enabled
                        ? 'console:mcpGateway.enabled'
                        : 'console:mcpGateway.disabled',
                    )}
                  </Badge>
                  <Badge
                    variant={row.probe.state === 'ok' ? 'success' : 'warning'}
                  >
                    {t(`console:mcpGateway.verdict.${row.probe.state}`)}
                  </Badge>
                </div>
              </div>
              <p className="mt-2 break-all font-mono text-caption text-muted-foreground">
                {row.transport === 'stdio'
                  ? [row.command, ...(row.args ?? [])].join(' ')
                  : row.url}
              </p>
              <p className="mt-1 break-all text-caption text-muted-foreground">
                {row.credential_ref || t('console:mcpGateway.noCredential')}
                {row.probe.tested_at && ` · ${row.probe.tested_at}`}
              </p>
              {row.probe.state !== 'ok' &&
                row.probe.state !== 'never_tested' && (
                  // The failure in plain words with the next step; the engine names the
                  // cause of an unreachable test when it can (DNS, TLS, timeout, ...).
                  <p className="mt-2 text-body text-warning">
                    {row.transport === 'stdio' &&
                    row.probe.state !== 'credential_unavailable'
                      ? row.probe.detail
                        ? // The engine's own line for why the command did not run
                          // (MC: one redacted stderr line or OS error, ≤ 512 bytes).
                          t('console:mcpGateway.why.stdioDetail', {
                            detail: row.probe.detail,
                          })
                        : t('console:mcpGateway.why.stdio')
                      : t(
                          `console:mcpGateway.why.${row.probe.reason ?? row.probe.state}`,
                          {
                            host: hostOf(row.url),
                            status: row.probe.http_status,
                            defaultValue: t(
                              'console:mcpGateway.why.unreachable',
                              {
                                host: hostOf(row.url),
                              },
                            ),
                          },
                        )}
                  </p>
                )}
              {!row.enabled &&
                row.probe.state === 'ok' &&
                !trustComplete(row.trust) && (
                  <p className="mt-2 text-caption text-muted-foreground">
                    {t('console:mcpGateway.enableNeedsTrust')}
                  </p>
                )}
              <div className="mt-3 flex flex-wrap gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={disabled}
                  onClick={() => act('test', row)}
                >
                  <RefreshCw />
                  {t('console:mcpGateway.test')}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={disabled}
                  onClick={() => setEditing(row)}
                >
                  <Pencil />
                  {t('console:mcpGateway.configure')}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={
                    disabled ||
                    (!row.enabled &&
                      (row.probe.state !== 'ok' || !trustComplete(row.trust)))
                  }
                  onClick={() =>
                    // Off is one click; on is a choice made with the tools in view.
                    row.enabled ? act('toggle', row) : setEnabling(row)
                  }
                >
                  {t(
                    row.enabled
                      ? 'console:mcpGateway.disable'
                      : 'console:mcpGateway.enable',
                  )}
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  disabled={disabled}
                  onClick={() => setRemove(row)}
                >
                  <Trash2 />
                  {t('console:mcpGateway.remove')}
                </Button>
              </div>
              {row.enabled ? (
                // As the CLI says it after mcp enable (CLX copy 19).
                <div className="mt-3 text-body" data-slot="mcp-callable">
                  {(['allow', 'ask', 'deny'] as const).map((k) => {
                    const tools = toolTrust(row)[k]
                    return tools.length ? (
                      <p key={k}>
                        {t(`console:mcpGateway.trust.${k}`, {
                          tools: tools.join(', '),
                        })}
                      </p>
                    ) : null
                  })}
                </div>
              ) : (
                <p className="mt-3 text-body" data-slot="mcp-callable">
                  {t('console:mcpGateway.callableOff')}
                </p>
              )}
              <details className="mt-1 text-body">
                <summary className="cursor-pointer font-medium">
                  {t('console:mcpGateway.tools', {
                    count: row.probe.tools?.length ?? 0,
                  })}
                </summary>
                <ul className="mt-2 space-y-2">
                  {(row.probe.tools ?? []).map((tool) => {
                    const policy = row.allowed_tools?.find(
                      (p) => p.name === tool.name,
                    )
                    return (
                      <li key={tool.name} className="break-all">
                        <code>{tool.name}</code>
                        <p className="text-caption text-muted-foreground">
                          {!row.enabled || !policy
                            ? t('console:mcpGateway.toolNotCallable')
                            : policy.destructive
                              ? t('console:mcpGateway.toolAsks')
                              : t('console:mcpGateway.toolCallable')}
                        </p>
                      </li>
                    )
                  })}
                </ul>
                <p className="mt-2 text-caption text-muted-foreground">
                  {t('console:mcpGateway.observationHint')}
                </p>
              </details>
            </article>
          ))}
        </div>
      )}
      {!data.read_only && (
        <StarterList
          Section={Section}
          added={data.servers.map((s) => s.name)}
          onAdd={(s) => {
            setStarter(s)
            setEditing(null)
          }}
        />
      )}
      <section
        className="rounded-lg border border-border p-3"
        aria-label={t('console:mcpGateway.governance')}
      >
        <Section className="text-body font-medium">
          {t('console:mcpGateway.governance')}
        </Section>
        <div className="mt-2 grid gap-2 text-caption sm:grid-cols-2">
          {Object.entries(data.governance).map(([name, value]) => (
            <p key={name}>
              <span className="font-medium">
                {t(`console:mcpGateway.governanceKeys.${name}`, {
                  defaultValue: readable(name),
                })}
              </span>
              {' · '}
              {t(`console:mcpGateway.governanceValues.${value}`, {
                defaultValue: readable(value),
              })}
            </p>
          ))}
        </div>
      </section>
      {enabling ? (
        <MCPEnableDialog
          row={enabling}
          busy={mutation.isPending}
          onClose={() => setEnabling(null)}
          onConfirm={(allowed) => {
            act('toggle', enabling, allowed)
            setEnabling(null)
          }}
        />
      ) : null}
      <Dialog
        open={editing !== undefined}
        onOpenChange={(open) => !open && setEditing(undefined)}
      >
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          {editing !== undefined && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <GatewayForm
                key={editing?.id ?? starter?.key ?? 'new'}
                row={editing ?? undefined}
                starter={editing ? undefined : starter}
                version={data.version}
                tenant={activeTenant!}
                queryKey={key}
                onClose={() => setEditing(undefined)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>
      <Dialog open={credentials} onOpenChange={setCredentials}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t('console:mcpGateway.credentials')}</DialogTitle>
            <DialogDescription>
              {t('console:mcpGateway.credentialCaption')}
            </DialogDescription>
          </DialogHeader>
          {credentials && <SecretsTab scope="tenant" />}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={remove !== null}
        onOpenChange={(open) => !open && setRemove(null)}
        title={t('console:mcpGateway.removeTitle')}
        description={t('console:mcpGateway.removeHint', { name: remove?.name })}
        confirmLabel={t('console:mcpGateway.remove')}
        tone="danger"
        pending={mutation.isPending}
        onConfirm={() => remove && act('remove', remove)}
      />
    </div>
  )
}

/** An engine fact this console has no words for yet, said readably instead of as its
 * raw key (HU-R21: three keys showed as "console:mcpGateway.governanceKeys.…"). */
function readable(id: string): string {
  const text = id.replace(/_/g, ' ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** The curated servers not added yet (K6.A11). Add only fills the form; the server is
 * saved off and runs nothing until it is tested and enabled. */
function StarterList({
  Section,
  added,
  onAdd,
}: {
  Section: 'h2' | 'h3'
  added: string[]
  onAdd: (starter: MCPStarter) => void
}) {
  const { t } = useTranslation(['console'])
  const open = mcpStarters.filter((s) => !added.includes(s.input.name))
  if (open.length === 0) return null
  return (
    <section
      className="rounded-lg border border-border p-3"
      aria-label={t('console:mcpGateway.startersTitle')}
    >
      <Section className="text-body font-medium">
        {t('console:mcpGateway.startersTitle')}
      </Section>
      <p className="text-body text-muted-foreground">
        {t('console:mcpGateway.startersHint')}
      </p>
      <ul className="mt-3 grid min-w-0 gap-3 xl:grid-cols-2">
        {open.map((s) => (
          <li key={s.key} className="min-w-0 rounded border border-border p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-body font-semibold">{s.input.name}</span>
              <Button size="sm" variant="outline" onClick={() => onAdd(s)}>
                <Plus />
                {t('console:mcpGateway.starterAdd', { name: s.input.name })}
              </Button>
            </div>
            <p className="mt-1 text-body">
              {t(`console:mcpGateway.starters.${s.key}.summary`)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(`console:mcpGateway.starters.${s.key}.profile`)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t('console:mcpGateway.starterLicence', { licence: s.licence })}
              {' · '}
              <a
                className="underline"
                href={s.source}
                target="_blank"
                rel="noopener noreferrer"
              >
                {t('console:mcpGateway.starterSource')}
              </a>
            </p>
          </li>
        ))}
      </ul>
    </section>
  )
}

/** A Save keeps an enabled server on unless it changes where the server is: the engine
 * withdraws the test result when the transport, address, command, arguments, environment,
 * secrets, credential or egress change (core/auth/mcp_gateway.go), and then it must be
 * tested again. An untouched Save turned an enabled server off (HU-R19). */
function keepsEnabled(
  row: MCPServer | undefined,
  next: MCPServerInput,
): boolean {
  if (!row?.enabled) return false
  const same = (a: unknown, b: unknown) =>
    JSON.stringify(a ?? null) === JSON.stringify(b ?? null)
  return (
    row.transport === next.transport &&
    (row.url ?? '') === (next.url ?? '') &&
    (row.command ?? '') === (next.command ?? '') &&
    same(row.args ?? [], next.args ?? []) &&
    same(row.env ?? {}, next.env ?? {}) &&
    same(row.env_secret_refs ?? {}, next.env_secret_refs ?? {}) &&
    (row.credential_ref ?? '') === (next.credential_ref ?? '') &&
    same(row.egress_cidrs ?? [], next.egress_cidrs ?? []) &&
    same(row.egress_hosts ?? [], next.egress_hosts ?? [])
  )
}

/** The engine's rule (core/auth/mcp_gateway.go mcpGatewayTrustComplete): no external
 * trust at all is complete — sessions authenticate exactly — and trust that is
 * supplied needs resource, issuer and keys. A stdio server is enabled without any. */
function trustComplete(trust: MCPServer['trust']): boolean {
  const keys = !!trust.jwks_url || !!trust.jwks
  if (!trust.resource && !trust.issuer && !keys) return true
  return !!trust.resource && !!trust.issuer && keys
}

/** The tested tools sessions can call on an enabled server: the ones it permits. */
/** The Save's answer did not keep these tool permissions. */
class PoliciesNotKept extends Error {
  readonly tools: string[]
  constructor(tools: string[]) {
    super('tool permissions not kept')
    this.tools = tools
  }
}

/** The tools whose permission was sent and is not in the engine's answer. */
export function unkeptPolicies(
  snapshot: MCPGatewaySnapshot,
  sent: MCPServerInput,
  id?: string,
): string[] {
  if (!sent.allowed_tools?.length) return []
  const saved = snapshot.servers.find((s) =>
    id ? s.id === id : s.name === sent.name,
  )
  if (!saved) return sent.allowed_tools.map((p) => p.name)
  return sent.allowed_tools
    .filter(
      (p) =>
        !saved.allowed_tools?.some(
          (k) => k.name === p.name && k.required_scope === p.required_scope,
        ),
    )
    .map((p) => p.name)
}

function GatewayForm({
  row,
  starter,
  version,
  tenant,
  queryKey,
  onClose,
}: {
  row?: MCPServer
  /** A curated server's settings, as the starting point of a new one. */
  starter?: MCPStarter
  version: number
  tenant: string
  queryKey: readonly unknown[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const qc = useQueryClient()
  const from = row ?? starter?.input
  const [name, setName] = useState(from?.name ?? '')
  // How Olivares reaches the server: an HTTPS address, or a command it starts on this
  // server (stdio), which runs with the session in its folder.
  const [kind, setKind] = useState<'url' | 'command'>(
    from?.transport === 'stdio' ? 'command' : 'url',
  )
  const [url, setURL] = useState(from?.url ?? '')
  const [command, setCommand] = useState(from?.command ?? '')
  const [args, setArgs] = useState((from?.args ?? []).join('\n'))
  const [env, setEnv] = useState(
    Object.entries(from?.env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join('\n'),
  )
  const [envSecrets, setEnvSecrets] = useState(
    Object.entries(from?.env_secret_refs ?? {})
      .map(([k, v]) => `${k}=${v.replace(/^store:/, '')}`)
      .join('\n'),
  )
  const argList = args
    .split('\n')
    .map((a) => a.trim())
    .filter(Boolean)
  const envMap = parsePairs(env)
  const secretMap = parsePairs(envSecrets)
  // The hosts a command may reach; empty keeps the network of its launch.
  const [hosts, setHosts] = useState((from?.egress_hosts ?? []).join('\n'))
  const hostList = hosts
    .split('\n')
    .map((h) => h.trim())
    .filter(Boolean)
  const hostsValid =
    hostList.length <= 16 &&
    new Set(hostList).size === hostList.length &&
    hostList.every(mcpEgressHost)
  const [credential, setCredential] = useState(from?.credential_ref ?? 'none')
  const [cidrs, setCIDRs] = useState(from?.egress_cidrs?.join('\n') ?? '')
  // The address callers' tokens must name: this console's origin plus the server's
  // gateway path, until someone sets another public origin.
  // Not pre-filled: a Save of an untouched form wrote this address with no issuer, which
  // is half-set trust, and Enable then failed (HU-R19). The address is the placeholder.
  const [resource, setResource] = useState(row?.trust.resource ?? '')
  const resourceSuggestion = row
    ? `${window.location.origin}/mcp/gateway/${tenant}/${row.id}`
    : ''
  const [issuer, setIssuer] = useState(row?.trust.issuer ?? '')
  const [jwks, setJWKS] = useState(
    row?.trust.jwks ? JSON.stringify(row.trust.jwks, null, 2) : '',
  )
  const [jwksURL, setJWKSURL] = useState(row?.trust.jwks_url ?? '')
  const [policies, setPoliciesState] = useState(row?.allowed_tools ?? [])
  // Sent only when it says something: a list the server already holds, or one changed
  // here. Otherwise the Save leaves the choice to enable, which accepts the tested tools.
  const [policiesChanged, setPoliciesChanged] = useState(false)
  const setPolicies: typeof setPoliciesState = (next) => {
    setPoliciesChanged(true)
    setPoliciesState(next)
  }
  const sendsPolicies = policiesChanged || (row?.allowed_tools?.length ?? 0) > 0
  const [error, setError] = useState('')
  const refs = useQuery({
    queryKey: [...queryKey, 'credentials', tenant],
    queryFn: ({ signal }) =>
      consoleApi.listSecrets('tenant', { signal, tenant }),
    gcTime: 0,
  })
  const publicKeys = jwks.trim() ? mcpPublicJWKS(jwks) : undefined
  // The engine's rule: no external trust, or all of it (mcpGatewayTrustComplete).
  const trustWhole = trustComplete({
    resource,
    issuer,
    jwks_url: jwksURL,
    jwks: publicKeys ?? undefined,
  })
  const trustValid =
    (!resource || mcpHTTPSURL(resource)) &&
    (!issuer || mcpHTTPSURL(issuer)) &&
    (!jwksURL || mcpHTTPSURL(jwksURL)) &&
    (!jwks.trim() || publicKeys !== null) &&
    !(jwksURL && jwks.trim()) &&
    trustWhole
  // An empty name takes the server's host name.
  const finalName =
    name.trim() ||
    (kind === 'url'
      ? hostOf(url)
      : command.trim()
        ? commandName(command.trim(), argList)
        : '')
  const valid =
    finalName.length > 0 &&
    finalName.length <= 128 &&
    (kind === 'url'
      ? mcpHTTPSURL(url)
      : command.trim() !== '' &&
        envMap !== null &&
        secretMap !== null &&
        hostsValid) &&
    trustValid &&
    policies.every((p) => !!p.required_scope.trim()) &&
    !refs.isError
  const save = usePrivilegedMutation<MCPServerInput, MCPGatewaySnapshot>({
    mutationKey: queryKey,
    mutationFn: async (input, authority) => {
      const snapshot = await mcpGatewayApi.put(version, input, row?.id, {
        ...authority,
        tenant,
      })
      // A Save is done when the engine's answer holds what was sent (HU 026: the dialog
      // closed and the tool permission was not there). Otherwise it stays open and says so.
      const missing = unkeptPolicies(snapshot, input, row?.id)
      if (missing.length > 0) throw new PoliciesNotKept(missing)
      return snapshot
    },
    invalidateKeys: [queryKey],
    // A Save that moved an enabled server turned it off (the engine withdraws its test):
    // say so on that Save rather than leave a silently disabled row (Root, HU-R19).
    successMessage: (_data, input) =>
      row?.enabled && !input.enabled
        ? t('console:mcpGateway.savedTestAgain')
        : t('console:mcpGateway.saved'),
    onDone: onClose,
    onError: (err) => {
      if (err instanceof PoliciesNotKept) {
        setError(
          t('console:mcpGateway.policiesNotKept', {
            tools: err.tools.join(', '),
          }),
        )
        return true
      }
      if (err instanceof ApiError && err.status === 409) {
        setError(t('console:mcpGateway.conflict'))
        // The next Save must carry the current snapshot version.
        void qc.invalidateQueries({ queryKey })
        return true
      }
      if (err instanceof ApiError && err.message) {
        setError(err.message)
        return true
      }
      return false
    },
  })
  const setPolicy = (tool: string, scope?: string, destructive?: boolean) =>
    setPolicies((old) =>
      old.map((p) =>
        p.name === tool
          ? {
              ...p,
              required_scope: scope ?? p.required_scope,
              destructive: destructive ?? p.destructive,
            }
          : p,
      ),
    )
  const submit = () => {
    // Only what is set: stored empty trust goes back as {} (MC), never as empty fields.
    const trust = {
      ...(resource.trim() ? { resource: resource.trim() } : {}),
      ...(issuer.trim() ? { issuer: issuer.trim() } : {}),
      ...(jwksURL ? { jwks_url: jwksURL } : {}),
      ...(publicKeys ? { jwks: publicKeys } : {}),
    }
    if (kind === 'command') {
      const input: MCPServerInput = {
        name: finalName,
        transport: 'stdio',
        url: '',
        command: command.trim(),
        args: argList,
        env: envMap ?? {},
        env_secret_refs: Object.fromEntries(
          Object.entries(secretMap ?? {}).map(([k, v]) => [
            k,
            v.startsWith('store:') ? v : `store:${v}`,
          ]),
        ),
        egress_cidrs: [],
        ...(hostList.length ? { egress_hosts: hostList } : {}),
        trust,
        ...(sendsPolicies ? { allowed_tools: policies } : {}),
        enabled: false,
      }
      save.mutate({ ...input, enabled: keepsEnabled(row, input) })
      return
    }
    const input: MCPServerInput = {
      name: finalName,
      transport: 'streamable_http',
      url,
      credential_ref: credential === 'none' ? '' : credential,
      egress_cidrs: cidrs.split(/\s+/).filter(Boolean),
      trust,
      ...(sendsPolicies ? { allowed_tools: policies } : {}),
      enabled: false,
    }
    save.mutate({ ...input, enabled: keepsEnabled(row, input) })
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {t(row ? 'console:mcpGateway.configure' : 'console:mcpGateway.add')}
        </DialogTitle>
        <DialogDescription>
          {t(
            row
              ? 'console:mcpGateway.formHintEdit'
              : 'console:mcpGateway.formHint',
          )}
        </DialogDescription>
      </DialogHeader>
      <div className="grid gap-4 py-2">
        {starter && (
          <div
            className="rounded-lg border border-border bg-surface p-3 text-body"
            data-slot="mcp-starter"
          >
            <p>{t(`console:mcpGateway.starters.${starter.key}.profile`)}</p>
            <p className="mt-1 text-muted-foreground">
              {t(`console:mcpGateway.starters.${starter.key}.setup`)}
            </p>
            <p className="mt-1 text-caption text-muted-foreground">
              {t('console:mcpGateway.starterLicence', {
                licence: starter.licence,
              })}
            </p>
          </div>
        )}
        <Field
          label={t('console:mcpGateway.name')}
          htmlFor="mcp-name"
          description={t('console:mcpGateway.nameHint')}
        >
          <Input
            id="mcp-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={
              (kind === 'url'
                ? hostOf(url)
                : command.trim()
                  ? commandName(command.trim(), argList)
                  : '') || undefined
            }
            maxLength={128}
          />
        </Field>
        <Field label={t('console:mcpGateway.kind')} htmlFor="mcp-kind">
          <Select
            value={kind}
            onValueChange={(v) => setKind(v as 'url' | 'command')}
          >
            <SelectTrigger id="mcp-kind">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="url">
                {t('console:mcpGateway.kindUrl')}
              </SelectItem>
              <SelectItem value="command">
                {t('console:mcpGateway.kindCommand')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {kind === 'command' ? (
          <>
            <Field
              label={t('console:mcpGateway.command')}
              htmlFor="mcp-command"
              required
              description={t('console:mcpGateway.commandHint')}
            >
              <Input
                id="mcp-command"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                placeholder="npx"
              />
            </Field>
            <Field
              label={t('console:mcpGateway.args')}
              htmlFor="mcp-args"
              description={t('console:mcpGateway.argsHint')}
            >
              <Textarea
                id="mcp-args"
                value={args}
                onChange={(e) => setArgs(e.target.value)}
                rows={3}
                placeholder={'-y\n@modelcontextprotocol/server-everything'}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.env')}
              htmlFor="mcp-env"
              description={t('console:mcpGateway.envHint')}
              error={
                envMap === null
                  ? t('console:mcpGateway.pairsInvalid')
                  : undefined
              }
            >
              <Textarea
                id="mcp-env"
                value={env}
                onChange={(e) => setEnv(e.target.value)}
                rows={2}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.envSecrets')}
              htmlFor="mcp-env-secrets"
              description={t('console:mcpGateway.envSecretsHint')}
              error={
                secretMap === null
                  ? t('console:mcpGateway.pairsInvalid')
                  : undefined
              }
            >
              <Textarea
                id="mcp-env-secrets"
                value={envSecrets}
                onChange={(e) => setEnvSecrets(e.target.value)}
                rows={2}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.egressHosts')}
              htmlFor="mcp-egress-hosts"
              description={t('console:mcpGateway.egressHostsHint')}
              error={
                hostsValid
                  ? undefined
                  : t('console:mcpGateway.egressHostsInvalid')
              }
            >
              <Textarea
                id="mcp-egress-hosts"
                value={hosts}
                onChange={(e) => setHosts(e.target.value)}
                rows={2}
              />
            </Field>
          </>
        ) : null}
        {kind === 'url' ? (
          <>
            <Field
              label={t('console:mcpGateway.url')}
              htmlFor="mcp-url"
              required
              description={t('console:mcpGateway.urlHint')}
            >
              <Input
                id="mcp-url"
                value={url}
                onChange={(e) => setURL(e.target.value)}
                placeholder="https://tools.example/mcp"
              />
            </Field>
            <Field
              label={t('console:mcpGateway.credential')}
              htmlFor="mcp-credential"
              description={t('console:mcpGateway.credentialHint')}
            >
              <Select
                value={credential}
                onValueChange={setCredential}
                disabled={refs.isPending || refs.isError}
              >
                <SelectTrigger id="mcp-credential">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">
                    {t('console:mcpGateway.noCredential')}
                  </SelectItem>
                  {refs.data?.secrets.map((s) => (
                    <SelectItem
                      key={s.name}
                      value={`store:${s.name}`}
                    >{`store:${s.name}`}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </>
        ) : null}
        {kind === 'url' && refs.isError ? (
          <QueryErrorState
            error={refs.error}
            retry={() => void refs.refetch()}
          />
        ) : null}
        <details
          open={
            !!row && (!!row.trust.issuer || (row.egress_cidrs ?? []).length > 0)
          }
        >
          <summary className="cursor-pointer text-body font-medium">
            {t('console:mcpGateway.advanced')}
          </summary>
          <div className="mt-3 grid gap-3">
            <p className="text-caption text-muted-foreground">
              {t('console:mcpGateway.advancedHint')}
            </p>
            {kind === 'url' ? (
              <Field
                label={t('console:mcpGateway.cidrs')}
                htmlFor="mcp-cidrs"
                description={t('console:mcpGateway.cidrsHint')}
              >
                <Textarea
                  id="mcp-cidrs"
                  value={cidrs}
                  onChange={(e) => setCIDRs(e.target.value)}
                  rows={2}
                />
              </Field>
            ) : null}
            <h3 className="text-body font-medium">
              {t('console:mcpGateway.inboundTrust')}
            </h3>
            <Field
              label={t('console:mcpGateway.resource')}
              htmlFor="mcp-resource"
              description={
                row
                  ? t('console:mcpGateway.resourceHint')
                  : t('console:mcpGateway.resourceAfterCreate')
              }
            >
              <Input
                id="mcp-resource"
                value={resource}
                placeholder={resourceSuggestion}
                onChange={(e) => setResource(e.target.value)}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.issuer')}
              htmlFor="mcp-issuer"
              description={t('console:mcpGateway.issuerHint')}
            >
              <Input
                id="mcp-issuer"
                value={issuer}
                onChange={(e) => setIssuer(e.target.value)}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.jwksURL')}
              htmlFor="mcp-jwks-url"
              description={t('console:mcpGateway.jwksURLHint')}
            >
              <Input
                id="mcp-jwks-url"
                value={jwksURL}
                onChange={(e) => setJWKSURL(e.target.value)}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.jwks')}
              htmlFor="mcp-jwks"
              description={t('console:mcpGateway.jwksHint')}
              error={
                jwks.trim() && publicKeys === null
                  ? t('console:mcpGateway.invalidJWKS')
                  : undefined
              }
            >
              <Textarea
                id="mcp-jwks"
                value={jwks}
                onChange={(e) => setJWKS(e.target.value)}
                rows={4}
              />
            </Field>
          </div>
        </details>
        {!!row && (
          <section>
            <h3 className="text-body font-medium">
              {t('console:mcpGateway.policy')}
            </h3>
            <p className="text-caption text-muted-foreground">
              {t('console:mcpGateway.observationHint')}
            </p>
            <div className="mt-3 space-y-3">
              {row.probe.tools?.map((tool) => {
                const p = policies.find((v) => v.name === tool.name)
                return (
                  <div
                    key={tool.name}
                    className="rounded border border-border p-2"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <code className="break-all text-body">{tool.name}</code>
                      <Switch
                        aria-label={t('console:mcpGateway.allowTool', {
                          name: tool.name,
                        })}
                        checked={!!p}
                        onCheckedChange={(on) =>
                          setPolicies((old) =>
                            on
                              ? [
                                  ...old,
                                  {
                                    // What enable would grant: callable by
                                    // sessions, asking first unless read-only.
                                    name: tool.name,
                                    required_scope: 'tools:call',
                                    destructive: !tool.read_only,
                                  },
                                ]
                              : old.filter((v) => v.name !== tool.name),
                          )
                        }
                      />
                    </div>
                    {p && (
                      <div className="mt-2 grid gap-2">
                        <Field
                          label={t('console:mcpGateway.scope', {
                            name: tool.name,
                          })}
                          htmlFor={`mcp-scope-${tool.name}`}
                          required
                        >
                          <Input
                            id={`mcp-scope-${tool.name}`}
                            value={p.required_scope}
                            onChange={(e) =>
                              setPolicy(tool.name, e.target.value)
                            }
                          />
                        </Field>
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-caption">
                            {t('console:mcpGateway.destructive')}
                          </span>
                          <Switch
                            aria-label={t(
                              'console:mcpGateway.destructiveTool',
                              { name: tool.name },
                            )}
                            checked={p.destructive}
                            onCheckedChange={(on) =>
                              setPolicy(tool.name, undefined, on)
                            }
                          />
                        </div>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </section>
        )}
        {error && (
          <p role="alert" className="text-body text-danger">
            {error}
          </p>
        )}
      </div>
      {!trustWhole && (
        <p className="text-caption text-warning">
          {t('console:mcpGateway.trustPartial')}
        </p>
      )}
      {!valid && policies.some((p) => !p.required_scope.trim()) && (
        <p className="text-caption text-warning">
          {t('console:mcpGateway.scopeMissing')}
        </p>
      )}
      <DialogFooter>
        <Button variant="ghost" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>
        <Button disabled={!valid || save.isPending} onClick={submit}>
          {t('console:mcpGateway.saveDisabled')}
        </Button>
      </DialogFooter>
    </>
  )
}
