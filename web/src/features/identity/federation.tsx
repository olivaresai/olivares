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
import {
  ModuleGate,
  QueryErrorState,
} from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'
import { CircleAlert, CircleCheck, ExternalLink } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ListTruncationBadge, SectionCard } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { StatusBadge } from '@/components/data/badges'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { SecretRef } from '@/components/data/secret-ref'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { AUDIT_PAGE_MAX } from '@/lib/api/endpoints'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import { useAuth } from '@/lib/auth/context'
import { useCommunityBuild } from '@/lib/hooks/use-server-info'
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
  PostureUnavailableNotice,
} from './components'
import { AuthorityReferences } from './references'
import type { AuditEntry, ScimUser, SsoEnvField } from './types'

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
      {/* SSO status is the identity module's, the IAM posture the security module's. */}
      <ModuleGate module="identity">
        <SsoSection />
      </ModuleGate>
      <ScimSection />
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
              <p className="text-body font-medium text-foreground">
                {t('sso.notConfigured')}
              </p>
              <p className="text-caption text-muted-foreground">
                {t('sso.notConfiguredHint')}
              </p>
            </div>
          </div>
        ) : status.isError && isContractPending(status.error) ? (
          <ContractPendingNotice what={t('sso.seamWhat')} />
        ) : status.isError ? (
          <QueryErrorState
            error={status.error}
            className="py-6"
            retry={() => void status.refetch()}
          />
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
                'flex items-center gap-1.5 text-caption',
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
              label={<code className="font-mono text-caption">{f.key}</code>}
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
        <p className="text-caption text-muted-foreground">{t('sso.envNote')}</p>
        <div>
          <Button asChild variant="outline" size="sm">
            <a
              href="/v1/auth/federation/start?browser_session=1"
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
        <span className="font-mono text-caption break-all">
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
          <span className="font-mono text-caption break-all">
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

const LEAVER_ACTION = 'scim.user.deprovision'
/** How many leaver events the card lists. */
const LEAVER_ROWS = 10

/**
 * The newest `LEAVER_ROWS` deprovision events, newest first, in the list envelope.
 *
 * The ledger pages FORWARDS only (`from`, ascending sequence), so the newest events are on
 * its LAST page: one `limit: 10` read returned the ten oldest and a tenant past ten leavers
 * never saw the latest. This follows `next_from` to the last page and keeps the tail;
 * `has_more` on the result means older events exist that the card does not list.
 *
 * A page that reports `has_more` without a `next_from` that moves forward cannot be walked
 * to its end, and its rows are the oldest ones: that is an error, never a "latest" list.
 *
 * The walk is several requests, and each one takes the tenant that is active when it is SENT.
 * A tenant switch cancels the query (tenant-cache-isolation), which discards its result but
 * would not stop the later pages: `signal` stops them, so none goes out under the new tenant.
 *
 * ponytail: one read per 1,000 leaver events on every load, and each read appends an
 * `audit.read` event to the ledger. Upgrade trigger: a tenant with tens of thousands of
 * leaver events, which wants a newest-first filter on GET /v1/audit.
 */
async function newestLeavers(
  signal: AbortSignal,
): Promise<ListResponse<AuditEntry>> {
  let from = 1
  let newest: AuditEntry[] = []
  let seen = 0
  for (;;) {
    signal.throwIfAborted()
    const page = await identityApi.audit(
      { action: LEAVER_ACTION, from, limit: AUDIT_PAGE_MAX },
      { signal },
    )
    seen += page.items.length
    newest = [...newest, ...page.items].slice(-LEAVER_ROWS)
    if (!page.has_more) break
    if (!page.next_from || page.next_from <= from) {
      throw new Error('audit page has more rows but no next_from after from')
    }
    from = page.next_from
  }
  return { items: newest.slice().reverse(), has_more: seen > LEAVER_ROWS }
}

function ScimSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  // The inbound SCIM server is Business: a Community build is not asked for it (a 501), and no
  // build is asked before server-info has said which it is.
  const community = useCommunityBuild()
  const spConfig = useQuery({
    queryKey: identityKeys.scimConfig(activeTenant),
    queryFn: () => identityApi.scimServiceProviderConfig(),
    retry: false,
    enabled: community === false,
  })
  const users = useQuery({
    queryKey: identityKeys.scimUsers(activeTenant),
    queryFn: () => identityApi.scimUsers({ count: 200 }),
    retry: false,
    enabled: community === false,
  })
  const leaver = useQuery({
    queryKey: identityKeys.audit(activeTenant, LEAVER_ACTION),
    queryFn: ({ signal }) => newestLeavers(signal),
    retry: false,
  })
  const columns = useMemo(() => scimUserColumns(t), [t])
  // A Community server answers the whole inbound SCIM server with 501: it is Business.
  // Say so once, in place of two "backend pending" seams and a promise of inbound groups.
  const businessOnly =
    community === true ||
    (spConfig.error instanceof ApiError && spConfig.error.status === 501)

  return (
    <SectionCard title={t('scim.title')} description={t('scim.description')}>
      <div className="flex flex-col gap-4">
        {businessOnly ? (
          <div
            className="flex items-start gap-2 rounded-md border border-border bg-muted/30 px-3 py-2"
            role="note"
          >
            <CircleAlert
              className="mt-0.5 size-4 shrink-0 text-muted-foreground"
              aria-hidden
            />
            <p className="text-caption text-muted-foreground">
              {t('scim.businessOnly')}
            </p>
          </div>
        ) : (
          <>
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
                        {cfg.authenticationSchemes[0]?.type ??
                          'oauthbearertoken'}
                      </Badge>
                      <SecretRef name={t('scim.bearerName')} />
                      <span className="text-caption text-muted-foreground">
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
            {/* ⛔ h3, NOT h4. The two titles of this card hang off the h2 "SCIM 2.0
                provisioning" and there is no h3 in between, so level 4 produced a
                2→4 skip: a screen reader announces a hierarchy the page does not have.
                It went UNDETECTED because /identity fell into the error boundary, and the
                boundary scores h1=1 skips=0 (identical to a clean view), so the harness
                counted it as good evidence. The class sets the visual size, not the tag:
                `text-sm` stays the same. */}
            <div>
              <h3 className="mb-1.5 text-body font-medium text-foreground">
                {t('scim.usersTitle')}
              </h3>
              {users.isError && isContractPending(users.error) ? (
                <ContractPendingNotice what={t('scim.seamWhat')} />
              ) : (
                <DataTable
                  columns={columns}
                  data={users.data?.Resources ?? []}
                  isLoading={users.isLoading || community === undefined}
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
              <p className="text-caption text-muted-foreground">
                {t('scim.groupsSupported')}
              </p>
            </div>
          </>
        )}

        {/* Leaver evidence from the audit ledger. */}
        <div>
          <h3 className="mb-1.5 flex items-center gap-2 text-body font-medium text-foreground">
            {t('scim.leaverTitle')}
          </h3>
          {/* The card lists the newest `LEAVER_ROWS` events, so an OLDER leaver can be
              missing from it. The count is the rows LISTED; the notice stays out of
              DeclaredSection so a failed refetch cannot leave it floating over the error. */}
          <ListTruncationBadge
            query={leaver}
            filas={leaver.data?.items.length}
            label={t('scim.leaverTruncated', {
              n: leaver.data?.items.length ?? 0,
            })}
            hint={t('scim.leaverTruncatedHint')}
            className="mb-2 px-0 pt-0"
          />
          <DeclaredSection
            query={leaver}
            what={t('scim.leaverSeamWhat')}
            skeletonHeight={80}
          >
            {(entries) =>
              entries.items.length === 0 ? (
                // No leaver processed is the good state of an identity plane.
                <EmptyState
                  quiet
                  title={t('scim.noLeaver')}
                  description={t('scim.noLeaverHint')}
                />
              ) : (
                <KvList>
                  {entries.items.map((e, i) => (
                    <KvRow
                      key={e.id ?? `${e.target_id}-${i}`}
                      label={
                        <code className="font-mono text-caption break-all">
                          {e.target_id ?? '—'}
                        </code>
                      }
                      align="start"
                      // A 36-character id does not fit beside the badge at 390 px, and KvRow's label
                      // never shrinks while its value has a zero basis: let the value drop under the
                      // id when it does not fit, and the id wrap inside the card.
                      className="flex-wrap gap-y-1.5 [&>dt]:max-w-full [&>dd]:basis-auto"
                    >
                      <span className="flex flex-wrap items-center gap-2">
                        <Badge variant="danger">{t('scim.revoked')}</Badge>
                        <RelTimeLabel ts={e.occurred_at} />
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
