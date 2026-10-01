// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
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
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
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
  mcpGatewayApi,
  mcpGatewayKeys,
  mcpHTTPSURL,
  mcpPublicJWKS,
  type MCPGatewaySnapshot,
  type MCPServer,
  type MCPServerInput,
} from './mcp-gateway-api'
import './i18n'

export function MCPGatewayTab() {
  const boundary = useAuthBoundary()
  return <GatewayInner key={boundary.key} />
}

function GatewayInner() {
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
  const [remove, setRemove] = useState<MCPServer | null>(null)
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
    | { kind: 'test' | 'toggle' | 'remove'; row: MCPServer; version: number }
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
      const { id: _, probe: __, ...input } = a.row
      return mcpGatewayApi.put(
        a.version,
        { ...input, enabled: !a.row.enabled },
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
      <ErrorState retry={() => void query.refetch()} />
    )
  const data = query.data
  const disabled = data.read_only || mutation.isPending
  const act = (kind: 'test' | 'toggle' | 'remove', row: MCPServer) => {
    setActionError('')
    mutation.mutate({ kind, row, version: data.version })
  }
  return (
    <div className="flex min-w-0 flex-col gap-4 pt-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-heading">{t('console:mcpGateway.title')}</h2>
          <p className="max-w-3xl text-body text-muted-foreground">
            {t('console:mcpGateway.caption')}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={data.read_only}
            onClick={() => setCredentials(true)}
          >
            <KeyRound />
            {t('console:mcpGateway.credentials')}
          </Button>
          <Button disabled={data.read_only} onClick={() => setEditing(null)}>
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
          <h3 className="text-body font-medium">
            {t('console:mcpGateway.sessionTitle')}
          </h3>
          <p className="text-body text-muted-foreground">
            {t('console:mcpGateway.sessionHint')}
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
                <h3 className="text-body font-semibold">{row.name}</h3>
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
                {row.url}
              </p>
              <p className="mt-1 break-all text-caption text-muted-foreground">
                {row.credential_ref || t('console:mcpGateway.noCredential')}
                {row.probe.tested_at && ` · ${row.probe.tested_at}`}
              </p>
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
                      (row.probe.state !== 'ok' ||
                        !row.trust.resource ||
                        !row.trust.issuer))
                  }
                  onClick={() => act('toggle', row)}
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
              <details className="mt-3 text-body">
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
                          {policy
                            ? `${policy.required_scope} · ${t(policy.destructive ? 'console:mcpGateway.destructive' : 'console:mcpGateway.readTool')}`
                            : t('console:mcpGateway.deniedTool')}
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
      <section
        className="rounded-lg border border-border p-3"
        aria-label={t('console:mcpGateway.governance')}
      >
        <h3 className="text-body font-medium">
          {t('console:mcpGateway.governance')}
        </h3>
        <div className="mt-2 grid gap-2 text-caption sm:grid-cols-2">
          {Object.entries(data.governance).map(([name, value]) => (
            <p key={name}>
              <span className="font-medium">
                {t(`console:mcpGateway.governanceKeys.${name}`)}
              </span>
              {' · '}
              {t(`console:mcpGateway.governanceValues.${value}`)}
            </p>
          ))}
        </div>
      </section>
      <Dialog
        open={editing !== undefined}
        onOpenChange={(open) => !open && setEditing(undefined)}
      >
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          {editing !== undefined && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <GatewayForm
                key={editing?.id ?? 'new'}
                row={editing ?? undefined}
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

function GatewayForm({
  row,
  version,
  tenant,
  queryKey,
  onClose,
}: {
  row?: MCPServer
  version: number
  tenant: string
  queryKey: readonly unknown[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const [name, setName] = useState(row?.name ?? '')
  const [url, setURL] = useState(row?.url ?? '')
  const [credential, setCredential] = useState(row?.credential_ref ?? 'none')
  const [cidrs, setCIDRs] = useState(row?.egress_cidrs?.join('\n') ?? '')
  const [resource, setResource] = useState(row?.trust.resource ?? '')
  const [issuer, setIssuer] = useState(row?.trust.issuer ?? '')
  const [jwks, setJWKS] = useState(
    row?.trust.jwks ? JSON.stringify(row.trust.jwks, null, 2) : '',
  )
  const [jwksURL, setJWKSURL] = useState(row?.trust.jwks_url ?? '')
  const [policies, setPolicies] = useState(row?.allowed_tools ?? [])
  const [error, setError] = useState('')
  const refs = useQuery({
    queryKey: [...queryKey, 'credentials'],
    queryFn: ({ signal }) =>
      consoleApi.listSecrets('tenant', { signal, tenant }),
    gcTime: 0,
  })
  const publicKeys = jwks.trim() ? mcpPublicJWKS(jwks) : undefined
  const trustValid =
    (!resource || mcpHTTPSURL(resource)) &&
    (!issuer || mcpHTTPSURL(issuer)) &&
    (!jwksURL || mcpHTTPSURL(jwksURL)) &&
    (!jwks.trim() || publicKeys !== null) &&
    !(jwksURL && jwks.trim())
  const valid =
    name.trim().length > 0 &&
    name.trim().length <= 128 &&
    mcpHTTPSURL(url) &&
    trustValid &&
    policies.every((p) => !!p.required_scope.trim()) &&
    !refs.isError
  const save = usePrivilegedMutation<MCPServerInput, MCPGatewaySnapshot>({
    mutationKey: queryKey,
    mutationFn: (input, authority) =>
      mcpGatewayApi.put(version, input, row?.id, { ...authority, tenant }),
    invalidateKeys: [queryKey],
    successMessage: t('console:mcpGateway.saved'),
    onDone: onClose,
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) {
        setError(t('console:mcpGateway.conflict'))
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
    const trust = {
      resource,
      issuer,
      ...(jwksURL ? { jwks_url: jwksURL } : {}),
      ...(publicKeys ? { jwks: publicKeys } : {}),
    }
    save.mutate({
      name: name.trim(),
      transport: 'streamable_http',
      url,
      credential_ref: credential === 'none' ? '' : credential,
      egress_cidrs: cidrs.split(/\s+/).filter(Boolean),
      trust,
      allowed_tools: policies,
      enabled: false,
    })
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>
          {t(row ? 'console:mcpGateway.configure' : 'console:mcpGateway.add')}
        </DialogTitle>
        <DialogDescription>
          {t('console:mcpGateway.formHint')}
        </DialogDescription>
      </DialogHeader>
      <div className="grid gap-4 py-2">
        <Field label={t('console:mcpGateway.name')} htmlFor="mcp-name" required>
          <Input
            id="mcp-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={128}
          />
        </Field>
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
        {refs.isError && <ErrorState retry={() => void refs.refetch()} />}
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
        <details open={!!row}>
          <summary className="cursor-pointer text-body font-medium">
            {t('console:mcpGateway.inboundTrust')}
          </summary>
          <div className="mt-3 grid gap-3">
            <p className="break-all text-caption text-muted-foreground">
              {t('console:mcpGateway.resourceHint', {
                path: row
                  ? `/mcp/gateway/${tenant}/${row.id}`
                  : t('console:mcpGateway.resourceAfterCreate'),
              })}
            </p>
            <Field
              label={t('console:mcpGateway.resource')}
              htmlFor="mcp-resource"
            >
              <Input
                id="mcp-resource"
                value={resource}
                onChange={(e) => setResource(e.target.value)}
              />
            </Field>
            <Field label={t('console:mcpGateway.issuer')} htmlFor="mcp-issuer">
              <Input
                id="mcp-issuer"
                value={issuer}
                onChange={(e) => setIssuer(e.target.value)}
              />
            </Field>
            <Field
              label={t('console:mcpGateway.jwksURL')}
              htmlFor="mcp-jwks-url"
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
                                    name: tool.name,
                                    required_scope: '',
                                    destructive: true,
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
