// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-IDN-01 — SSO/SAML/OIDC config + SCIM 2.0 status (consumes).
//  • SSO config is ENV-ONLY (no write API): the panel documents the env keys,
//    computes + checks the EXACT redirect URI (RFC 9700 exact-match), reflects the
//    always-on PKCE S256, and surfaces the connection state — incl. the explicit
//    ErrSSONotConfigured (501 sso_not_configured) state.
//  • SCIM status reads the REAL service-provider config + Users; the bearer is
//    NEVER rendered in clear (only the scheme + a redacted reference); the leaver
//    is evidenced from the audit ledger (scim.user.deprovision = sessions+tokens
//    revoked). Group provisioning is REAL inbound since — the panel used to
//    claim otherwise, which was a false statement about our own capability.
//  • The claude-console posture surfaces the HONEST Admin-API blind-spot finding.
import { useQuery } from '@tanstack/react-query'
import { CircleAlert, CircleCheck, ExternalLink } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SectionCard, SelfAuditNotice } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { StatusBadge } from '@/components/data/badges'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { SecretRef } from '@/components/data/secret-ref'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import {
  identityApi,
  identityKeys,
  isContractPending,
  isSsoNotConfigured,
} from './api'
import {
  ContractPendingNotice,
  DeclaredSection,
  FindingList,
  PostureUnavailableNotice,
} from './components'
import { AuthorityReferences } from './references'
import type { ScimUser, SsoEnvField } from './types'

/** The env keys that configure SSO (read-only documentation — there is NO config
 *  write API; these are read once at boot via FromEnv).*/
const SSO_ENV_FIELDS: SsoEnvField[] = [
  { key: 'OLIVARES_SSO_PROTOCOL', required: true, protocol: 'common' },
  { key: 'OLIVARES_OIDC_ISSUER', required: true, protocol: 'oidc' },
  { key: 'OLIVARES_OIDC_CLIENT_ID', required: true, protocol: 'oidc' },
  {
    key: 'OLIVARES_OIDC_CLIENT_SECRET',
    required: false,
    protocol: 'oidc',
    secret: true,
  },
  { key: 'OLIVARES_SAML_IDP_METADATA_URL', required: true, protocol: 'saml' },
  { key: 'OLIVARES_SAML_SP_ENTITY_ID', required: true, protocol: 'saml' },
  { key: 'OLIVARES_SAML_ACS_URL', required: true, protocol: 'saml' },
  { key: 'OLIVARES_SAML_IDP_SSO_URL', required: true, protocol: 'saml' },
]

/** The server derives the callback from the request host (RFC 9700 exact-match);
 *  we compute the same value so the operator can register it before the backend
 *  lands, and check their paste against it exactly. */
function expectedRedirectUri(): string {
  if (typeof window === 'undefined') return ''
  return `${window.location.origin}/v1/auth/federation/callback`
}

export function FederationTab() {
  return (
    <div className="flex flex-col gap-6">
      <SsoSection />
      <ScimSection />
      <ClaudeConsolePostureSection />
      <AuthorityReferences
        area="sso"
        keys={['scim', 'oauthSecurityBcp', 'pkce']}
      />
    </div>
  )
}

function SsoSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const status = useQuery({
    queryKey: identityKeys.sso(activeTenant),
    queryFn: () => identityApi.ssoStatus(),
    retry: false,
  })
  //the SERVER now reports the exact callback the login leg will actually send
  // (it honours a trusted proxy's X-Forwarded-*, which the browser cannot see). Prefer
  // it; the client-side computation stays as the fallback for an engine that predates
  // the route, which is what it was always for.
  const expected = status.data?.redirect_uri || expectedRedirectUri()
  const [pasted, setPasted] = useState('')
  const trimmed = pasted.trim()
  const exactMatch = trimmed.length > 0 && trimmed === expected

  return (
    <SectionCard title={t('sso.title')} description={t('sso.description')}>
      <div className="flex flex-col gap-4">
        {/* Connection state — the explicit ErrSSONotConfigured state is a real,
            known state (not a backend-pending seam). */}
        {status.data?.reason ? (
          /* The engine answered but could NOT determine the posture. Rendering this as
             "SSO is off" would tell an operator their federation is disabled when the
             truth is that we did not look — the exact confusion the reason exists to
             prevent, and it is discarded unless it is rendered here. */
          <PostureUnavailableNotice reason={status.data.reason} />
        ) : status.isError && isSsoNotConfigured(status.error) ? (
          <div
            className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-3 py-2.5"
            role="status"
          >
            <CircleAlert
              className="mt-0.5 size-4 shrink-0 text-warning"
              aria-hidden
            />
            <div>
              <p className="text-sm font-medium text-foreground">
                {t('sso.notConfigured')}
              </p>
              <p className="text-xs text-muted-foreground">
                {t('sso.notConfiguredHint')}
              </p>
            </div>
          </div>
        ) : status.isError && isContractPending(status.error) ? (
          <ContractPendingNotice what={t('sso.seamWhat')} />
        ) : status.isError ? (
          <ErrorState className="py-6" retry={() => void status.refetch()} />
        ) : status.data ? (
          <KvList>
            <KvRow label={t('sso.protocol')}>
              {status.data.protocol
                ? status.data.protocol.toUpperCase()
                : t('sso.notConfigured')}
            </KvRow>
            <KvRow label={t('sso.state')}>
              <StatusBadge
                status={status.data.configured ? 'active' : 'inactive'}
              />
            </KvRow>
          </KvList>
        ) : null}

        {/* Redirect URI — exact-match checker (RFC 9700). */}
        <div className="flex flex-col gap-2">
          <KvList>
            <KvRow label={t('sso.redirectUri')} mono align="start">
              {expected || '—'}
            </KvRow>
            <KvRow label={t('sso.pkce')}>
              <Badge variant="info">{t('sso.pkceValue')}</Badge>
            </KvRow>
          </KvList>
          <Field
            label={t('sso.redirectCheckLabel')}
            description={t('sso.redirectCheckHint')}
          >
            <Input
              value={pasted}
              mono
              onChange={(e) => setPasted(e.target.value)}
              placeholder={expected}
              aria-invalid={trimmed.length > 0 && !exactMatch}
            />
          </Field>
          {trimmed.length > 0 ? (
            <p
              role="status"
              className={cn(
                'flex items-center gap-1.5 text-xs',
                exactMatch ? 'text-success' : 'text-danger',
              )}
            >
              {exactMatch ? (
                <CircleCheck className="size-3.5 shrink-0" aria-hidden />
              ) : (
                <CircleAlert className="size-3.5 shrink-0" aria-hidden />
              )}
              {exactMatch ? t('sso.redirectMatch') : t('sso.redirectMismatch')}
            </p>
          ) : null}
        </div>

        {/* Env config (read-only docs) + test login. */}
        <KvList>
          {SSO_ENV_FIELDS.map((f) => (
            <KvRow
              key={f.key}
              label={<code className="font-mono text-xs">{f.key}</code>}
              align="start"
            >
              <span className="flex flex-wrap items-center gap-1.5">
                <Badge variant="outline">{t(`sso.proto.${f.protocol}`)}</Badge>
                {f.required ? (
                  <Badge variant="neutral">{t('sso.required')}</Badge>
                ) : null}
                {f.secret ? (
                  <Badge variant="warning">{t('sso.secret')}</Badge>
                ) : null}
              </span>
            </KvRow>
          ))}
        </KvList>
        <p className="text-xs text-muted-foreground">{t('sso.envNote')}</p>
        <div>
          <Button asChild variant="outline" size="sm">
            <a
              href="/v1/auth/federation/start"
              target="_blank"
              rel="noreferrer noopener"
            >
              <ExternalLink className="size-4" aria-hidden />
              {t('sso.testLogin')}
            </a>
          </Button>
        </div>
      </div>
    </SectionCard>
  )
}

function scimUserColumns(
  t: (k: string, o?: Record<string, unknown>) => string,
): TableColumn<ScimUser>[] {
  return [
    {
      accessorKey: 'userName',
      header: t('scim.col.userName'),
      cell: ({ row }) => (
        <span className="font-mono text-xs break-all">
          {row.original.userName}
        </span>
      ),
    },
    {
      id: 'active',
      header: t('scim.col.state'),
      enableSorting: false,
      cell: ({ row }) => (
        <StatusBadge status={row.original.active ? 'active' : 'inactive'} />
      ),
    },
    {
      accessorKey: 'externalId',
      header: t('scim.col.externalId'),
      cell: ({ row }) =>
        row.original.externalId ? (
          <span className="font-mono text-xs break-all">
            {row.original.externalId}
          </span>
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
    {
      id: 'lastModified',
      header: t('scim.col.lastModified'),
      enableSorting: false,
      cell: ({ row }) =>
        row.original.meta?.lastModified ? (
          <RelTimeLabel ts={row.original.meta.lastModified} />
        ) : (
          <span className="text-muted-foreground">—</span>
        ),
    },
  ]
}

function ScimSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const spConfig = useQuery({
    queryKey: identityKeys.scimConfig(activeTenant),
    queryFn: () => identityApi.scimServiceProviderConfig(),
    retry: false,
  })
  const users = useQuery({
    queryKey: identityKeys.scimUsers(activeTenant),
    queryFn: () => identityApi.scimUsers({ count: 200 }),
    retry: false,
  })
  const leaver = useQuery({
    queryKey: identityKeys.audit(activeTenant, 'scim.user.deprovision'),
    queryFn: () =>
      identityApi.audit({ action: 'scim.user.deprovision', limit: 10 }),
    retry: false,
  })
  const columns = useMemo(() => scimUserColumns(t), [t])

  return (
    <SectionCard title={t('scim.title')} description={t('scim.description')}>
      <div className="flex flex-col gap-4">
        {/* Service-provider posture + bearer scheme (never the token). */}
        <DeclaredSection
          query={spConfig}
          what={t('scim.seamWhat')}
          skeletonHeight={80}
        >
          {(cfg) => (
            <KvList>
              <KvRow label={t('scim.patch')}>
                {cfg.patch.supported
                  ? t('common:status.enabled')
                  : t('common:status.disabled')}
              </KvRow>
              <KvRow label={t('scim.filter')}>
                {cfg.filter.supported
                  ? t('scim.filterMax', { n: cfg.filter.maxResults })
                  : t('common:status.disabled')}
              </KvRow>
              <KvRow label={t('scim.auth')} align="start">
                <span className="flex flex-col gap-1">
                  <Badge variant="neutral">
                    {cfg.authenticationSchemes[0]?.type ?? 'oauthbearertoken'}
                  </Badge>
                  <SecretRef name={t('scim.bearerName')} />
                  <span className="text-xs text-muted-foreground">
                    {t('scim.bearerNote')}
                  </span>
                </span>
              </KvRow>
            </KvList>
          )}
        </DeclaredSection>

        {/* Provisioned Users (joiner/mover/leaver — active:false = disabled).
            A pending status routes to the honest seam (consistent with the config
            section above) instead of a red error; DataTable still handles 403 and
            genuine 5xx errors. */}
        {/* ⛔ h3, NO h4. Los dos títulos de esta tarjeta cuelgan del h2 «SCIM 2.0
            provisioning» y no hay ningún h3 entre medias, así que el nivel 4 producía un
            salto 2→4: un lector de pantalla anuncia una jerarquía que la página no tiene.
            Estaba SIN DETECTAR porque /identity caía al error boundary, y el boundary
            puntúa h1=1 skips=0 —idéntico a una vista limpia—, así que el arnés lo contaba
            como evidencia buena. El tamaño visual lo pone la clase, no la etiqueta:
            `text-sm` se queda igual. */}
        <div>
          <h3 className="mb-1.5 text-sm font-medium text-foreground">
            {t('scim.usersTitle')}
          </h3>
          {users.isError && isContractPending(users.error) ? (
            <ContractPendingNotice what={t('scim.seamWhat')} />
          ) : (
            <DataTable
              columns={columns}
              data={users.data?.Resources ?? []}
              isLoading={users.isLoading}
              error={users.error}
              onRetry={() => void users.refetch()}
              getRowId={(u) => u.id}
              label={t('scim.usersTitle')}
              empty={
                <EmptyState
                  title={t('scim.noUsers')}
                  description={t('scim.noUsersHint')}
                />
              }
            />
          )}
        </div>

        {/* Groups — provisionable inbound since the role mapping is
            deliberately operator-only (core/api/server.go wires /Groups and
            core/api/scim advertises the Group resource type). */}
        <div
          className="flex items-start gap-2 rounded-md border border-border bg-muted/30 px-3 py-2"
          role="note"
        >
          <CircleAlert
            className="mt-0.5 size-4 shrink-0 text-muted-foreground"
            aria-hidden
          />
          <p className="text-xs text-muted-foreground">
            {t('scim.groupsSupported')}
          </p>
        </div>

        {/* Leaver evidence from the audit ledger. */}
        <div>
          <h3 className="mb-1.5 flex items-center gap-2 text-sm font-medium text-foreground">
            {t('scim.leaverTitle')}
          </h3>
          <SelfAuditNotice />
          <DeclaredSection
            query={leaver}
            what={t('scim.leaverSeamWhat')}
            skeletonHeight={80}
          >
            {(entries) =>
              entries.items.length === 0 ? (
                <EmptyState
                  title={t('scim.noLeaver')}
                  description={t('scim.noLeaverHint')}
                />
              ) : (
                <KvList>
                  {entries.items.map((e, i) => (
                    <KvRow
                      key={e.id ?? `${e.target_id}-${i}`}
                      label={
                        <code className="font-mono text-xs break-all">
                          {e.target_id ?? '—'}
                        </code>
                      }
                      align="start"
                    >
                      <span className="flex flex-wrap items-center gap-2">
                        <Badge variant="danger">{t('scim.revoked')}</Badge>
                        {e.at ? <RelTimeLabel ts={e.at} /> : null}
                      </span>
                    </KvRow>
                  ))}
                </KvList>
              )
            }
          </DeclaredSection>
        </div>
      </div>
    </SectionCard>
  )
}

function ClaudeConsolePostureSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const posture = useQuery({
    queryKey: identityKeys.findings(activeTenant, 'iam_posture'),
    queryFn: () => identityApi.findings({ kind: 'iam_posture' }),
    retry: false,
  })
  // The honest, structural Admin-API blind-spot facts. Source: the
  // claude-console connector's `iam_posture` postureFinding detail (sso_enforcement /
  // scim_provisioning unknown-by-api, members listable, sso_jit=team+,
  // scim_audit_export=enterprise, continuous audit = Compliance Activity Feed) — NOT
  // which documents the WIF/svac write-Console-only (read via org:admin OAuth) fact.
  const blindSpots = [
    'ssoEnforcement',
    'scimProvisioning',
    'members',
    'ssoJit',
    'auditExport',
    'continuousAudit',
  ] as const

  return (
    <SectionCard
      title={t('console.title')}
      description={t('console.description')}
    >
      <div className="flex flex-col gap-4">
        <SelfAuditNotice />
        <DeclaredSection
          query={posture}
          what={t('console.seamWhat')}
          skeletonHeight={80}
        >
          {(findings) => (
            <FindingList
              findings={findings.items}
              label={t('console.title')}
              emptyTitle={t('console.noFindings')}
              emptyDescription={t('console.noFindingsHint')}
            />
          )}
        </DeclaredSection>
        <div className="rounded-md border border-border bg-muted/30 p-3">
          <p className="mb-2 text-xs font-medium text-muted-foreground">
            {t('console.blindSpotTitle')}
          </p>
          <ul className="flex flex-col gap-1">
            {blindSpots.map((k) => (
              <li
                key={k}
                className="flex items-start gap-2 text-xs text-foreground"
              >
                <span className="mt-px text-muted-foreground" aria-hidden>
                  ·
                </span>
                {t(`console.blindSpots.${k}`)}
              </li>
            ))}
          </ul>
        </div>
      </div>
    </SectionCard>
  )
}
