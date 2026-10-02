// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { grantName, shortId, useOrgs } from '@/components/layout/tenant-label'
import { useQuery } from '@tanstack/react-query'
import { KeyRound, ShieldAlert, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
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
import { KvList, KvRow } from '@/components/ui/kv'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
import { toast } from '@/components/ui/toaster'
import {
  AssuranceMappingEditor,
  DEFAULT_MAPPING,
  MappingSummary,
  mappingProblem,
} from './sso-assurance-mapping'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { useResumeGuard } from '@/lib/hooks/use-resume-guard'
import {
  consoleApi,
  consoleKeys,
  type AssuranceMapping,
  type SSOConfigDTO,
  type SSOConfigInput,
} from './api'

export function SSOTab() {
  const { t } = useTranslation(['console', 'common'])
  const { isSuperadmin, grants } = useAuth()
  const [editOpen, setEditOpen] = useState(false)
  const [removeOpen, setRemoveOpen] = useState(false)
  const [addOpen, setAddOpen] = useState(false)
  const [newAlias, setNewAlias] = useState('')
  //U6: '' = the deployment-wide global config; a tenant id = that tenant's IdP.
  const [scope, setScope] = useState('')
  //U4: which IdP within the scope; "default" = the scope's primary IdP.
  const [idp, setIdp] = useState('default')

  // Reset the selected IdP to the scope's primary whenever the scope changes.
  function selectScope(next: string) {
    setScope(next)
    setIdp('default')
  }

  const orgs = useOrgs()

  // The IdPs configured under the current scope (U4) — feeds the IdP selector.
  const idps = useQuery({
    queryKey: consoleKeys.ssoIdps(scope || undefined),
    queryFn: () => consoleApi.listIdPs(scope || undefined),
    enabled: isSuperadmin,
  })

  const sso = useQuery({
    queryKey: consoleKeys.sso(scope || undefined, idp),
    queryFn: () => consoleApi.getSSO(scope || undefined, idp),
    enabled: isSuperadmin,
  })

  const removeMutation = usePrivilegedMutation<void, void>({
    mutationFn: () => consoleApi.deleteSSO(scope || undefined, idp),
    invalidateKeys: () => [
      consoleKeys.sso(scope || undefined, idp),
      consoleKeys.ssoIdps(scope || undefined),
    ],
    successMessage: t('console:sso.deleted'),
    // Removing a non-default IdP frees its slot; fall back to the primary.
    onDone: () => {
      setRemoveOpen(false)
      if (idp !== 'default') setIdp('default')
    },
  })

  // Normalize a candidate alias the way the backend does (trim + lowercase), so the
  // client shows the operator exactly what will be stored / used as the routing key.
  const normalizedNewAlias = newAlias.trim().toLowerCase()
  const addValid = /^[a-z0-9](?:[a-z0-9-]{0,30})$/.test(normalizedNewAlias)

  function confirmAddIdp() {
    setIdp(normalizedNewAlias)
    setAddOpen(false)
    setNewAlias('')
    setEditOpen(true)
  }

  if (!isSuperadmin) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-muted-foreground">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-warning"
          aria-hidden
        />
        {t('console:sso.superadminOnly')}
      </div>
    )
  }

  const cfg = sso.data
  // The server answers routed_by "unavailable" when this build cannot select among
  // several IdPs (no per-tenant or by-domain selection). Unread yet: offer nothing.
  const multiIdp = cfg ? cfg.routed_by !== 'unavailable' : false

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:sso.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:sso.caption')}
          </p>
        </div>
        <div className="flex gap-2">
          {cfg?.configured && (
            <Button variant="ghost" onClick={() => setRemoveOpen(true)}>
              <Trash2 />
              {t('console:sso.delete')}
            </Button>
          )}
          <Button onClick={() => setEditOpen(true)}>
            <KeyRound />
            {cfg?.configured
              ? t('console:sso.edit')
              : t('console:sso.configure')}
          </Button>
        </div>
      </div>

      {/*U6: a superadmin manages the deployment-wide IdP or a specific tenant's
          IdP. Only a build that selects among several IdPs (routed_by, the server's
          multi-IdP answer) offers the scope and IdP choice; one that cannot manages
          the deployment-wide default IdP, the only one its login resolves. */}
      {multiIdp && (
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="sso-scope">{t('console:sso.scope.label')}</Label>
            <Select
              value={scope || 'global'}
              onValueChange={(v) => selectScope(v === 'global' ? '' : v)}
            >
              <SelectTrigger id="sso-scope">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="global">
                  {t('console:sso.scope.global')}
                </SelectItem>
                {/* The organization list, else the principal's own grants by name
                    (whoami tenant_name) where the list is not served. */}
                {(
                  orgs.data?.items.map((o) => ({
                    id: o.tenant_id,
                    name: o.name,
                  })) ??
                  (grants ?? []).map((g) => ({
                    id: g.tenant,
                    name: grantName(grants, g.tenant) ?? shortId(g.tenant),
                  }))
                ).map((o) => (
                  <SelectItem key={o.id} value={o.id}>
                    {o.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-caption text-muted-foreground">
              {scope
                ? t('console:sso.scope.tenantHint')
                : t('console:sso.scope.globalHint')}
            </p>
          </div>

          {/*U4: a scope can federate more than one IdP (first-class IdP entity,
            keyed by alias). "default" is the primary. U5 HAS landed: a tenant scope may
            run several active IdPs when each extra one claims its own domains, subject to
            the MultiIDP entitlement; the GLOBAL scope still activates only "default". */}
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="sso-idp">{t('console:sso.idp.label')}</Label>
            <div className="flex gap-2">
              <Select
                value={idp}
                onValueChange={(v) => {
                  if (v === '__add__') setAddOpen(true)
                  else setIdp(v)
                }}
              >
                <SelectTrigger id="sso-idp">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {idpAliases(idps.data?.idps, idp).map((a) => {
                    // The administrator's name first, the alias only as its fallback, and
                    // whether people can sign in with it now (multi-IdP clarity).
                    const row = idps.data?.idps.find(
                      (i) => (i.alias || 'default') === a,
                    )
                    const name = idpName(t, a, row?.display_name)
                    return (
                      <SelectItem key={a} value={a}>
                        {row
                          ? `${name} · ${
                              row.status === 'active'
                                ? t('console:sso.idp.on')
                                : t('console:sso.idp.off')
                            }`
                          : name}
                      </SelectItem>
                    )
                  })}
                  <SelectItem value="__add__">
                    {t('console:sso.idp.add')}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <p className="text-caption text-muted-foreground">
              {t('console:sso.idp.hint')}
            </p>
            {idps.data ? (
              <p className="text-body text-foreground" data-slot="sso-active">
                {activeNames(t, idps.data.idps).length > 0
                  ? t('console:sso.idp.activeNow', {
                      names: activeNames(t, idps.data.idps).join(', '),
                    })
                  : t('console:sso.idp.noneActive')}
              </p>
            ) : null}
          </div>
        </div>
      )}

      {sso.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : (
        <div className="flex flex-col gap-3 rounded-lg border border-border p-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={cfg?.configured ? 'success' : 'neutral'}>
              {cfg?.configured
                ? t('console:sso.statusConfigured')
                : t('console:sso.statusNotConfigured')}
            </Badge>
            {cfg?.configured && (
              <Badge variant={cfg.status === 'active' ? 'success' : 'neutral'}>
                {cfg.status === 'active'
                  ? t('console:sso.statusEnabled')
                  : t('console:sso.statusDisabled')}
              </Badge>
            )}
            {cfg?.protocol && (
              <Badge variant="info">{cfg.protocol.toUpperCase()}</Badge>
            )}
          </div>
          {cfg && !cfg.provider_available && (
            <p className="flex items-start gap-2 text-body text-warning">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t('console:sso.builderUnavailable')}
            </p>
          )}
          {cfg?.configured ? (
            <KvList>
              <KvRow label={t('console:sso.displayName')}>
                {cfg.display_name?.trim() ||
                  t('console:sso.displayNameFallback')}
              </KvRow>
              <KvRow label={t('console:sso.mapping.title')}>
                <MappingSummary
                  protocol={cfg.protocol || 'oidc'}
                  mapping={cfg.assurance_mapping}
                />
              </KvRow>
            </KvList>
          ) : null}
          <Field
            label={t('console:sso.redirectUri')}
            htmlFor="sso-redirect"
            description={t('console:sso.redirectUriHint')}
          >
            <Input
              id="sso-redirect"
              readOnly
              value={cfg?.redirect_uri ?? ''}
              mono
            />
          </Field>

          {/* Login enforcement is shown only by a build that enforces it. Where the
              server reports it unavailable (the Community build), there is no control to
              show: a stored posture is kept as it is, and nothing here claims otherwise. */}
          {cfg && cfg.enforced_by !== 'unavailable' && (
            <div className="flex flex-col gap-2 border-t border-border pt-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-body font-medium text-foreground">
                  {t('console:sso.enforcement.title')}
                </span>
                {(() => {
                  // Honest badge: only warn "stored, not enforced" when a posture is
                  // actually set. With nothing stored, neither build is "enforcing"
                  // anything, so show a neutral "no enforcement configured" instead of
                  // overstating what exists (the banner below is gated the same way).
                  const postureSet =
                    cfg.require_sso || cfg.network_allowlist.length > 0
                  if (!postureSet) {
                    return (
                      <Badge variant="neutral">
                        {t('console:sso.enforcement.notConfigured')}
                      </Badge>
                    )
                  }
                  // THREE answers, not two. "out_of_scope" means the build DOES enforce
                  // but not over THIS row, so neither the green "enforced" nor the
                  // "rebuild with the enterprise tag" advice is true. Collapsing it into
                  // either one is the false claim this fixes.
                  if (cfg.enforced_by === 'out_of_scope') {
                    return (
                      <Badge variant="warning">
                        {t('console:sso.enforcement.enforcedOutOfScope')}
                      </Badge>
                    )
                  }
                  return (
                    <Badge variant="success">
                      {t('console:sso.enforcement.enforcedEnterprise')}
                    </Badge>
                  )
                })()}
              </div>
              <p className="text-body text-muted-foreground">
                {cfg.require_sso
                  ? t('console:sso.enforcement.requireSsoOn')
                  : t('console:sso.enforcement.requireSsoOff')}
              </p>
              <div className="flex flex-col gap-1">
                <span className="text-body text-muted-foreground">
                  {t('console:sso.enforcement.networkAllowlist')}
                </span>
                {cfg.network_allowlist.length > 0 ? (
                  <div className="flex flex-wrap gap-1.5">
                    {cfg.network_allowlist.map((cidr) => (
                      <code
                        key={cidr}
                        className="rounded border border-border bg-muted px-1.5 py-0.5 font-mono text-caption text-foreground"
                      >
                        {cidr}
                      </code>
                    ))}
                  </div>
                ) : (
                  <span className="text-body text-muted-foreground">
                    {t('console:sso.enforcement.noNetworkRestriction')}
                  </span>
                )}
              </div>
              {/* A build that enforces login policy, but not over THIS row: the posture
                  stored here is not the one the engine reads, so say so. */}
              {cfg.enforced_by === 'out_of_scope' &&
                (cfg.require_sso || cfg.network_allowlist.length > 0) && (
                  <p className="flex items-start gap-2 text-body text-warning">
                    <ShieldAlert
                      className="mt-0.5 size-4 shrink-0"
                      aria-hidden
                    />
                    {t('console:sso.enforcement.outOfScopeBanner')}
                  </p>
                )}
            </div>
          )}
        </div>
      )}

      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {editOpen && cfg && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SSOForm
                current={cfg}
                scope={scope || undefined}
                alias={idp}
                onClose={() => setEditOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      {/*U4: add an additional IdP to the current scope by choosing an alias. */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('console:sso.idp.addTitle')}</DialogTitle>
            <DialogDescription>
              {t('console:sso.idp.addBody')}
            </DialogDescription>
          </DialogHeader>
          <Field
            label={t('console:sso.idp.aliasLabel')}
            htmlFor="sso-new-alias"
            description={t('console:sso.idp.aliasHint')}
          >
            <Input
              id="sso-new-alias"
              value={newAlias}
              onChange={(e) => setNewAlias(e.target.value)}
              placeholder="okta"
              mono
            />
          </Field>
          <DialogFooter>
            <Button variant="secondary" onClick={() => setAddOpen(false)}>
              {t('common:actions.cancel', { defaultValue: 'Cancel' })}
            </Button>
            <Button
              variant="primary"
              disabled={!addValid || normalizedNewAlias === 'default'}
              onClick={confirmAddIdp}
            >
              {t('console:sso.idp.addConfirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={removeOpen}
        onOpenChange={setRemoveOpen}
        title={t('console:sso.deleteTitle')}
        description={t('console:sso.deleteBody')}
        confirmLabel={t('console:sso.delete')}
        tone="danger"
        pending={removeMutation.isPending}
        onConfirm={() => removeMutation.mutate()}
      />
    </div>
  )
}

// idpAliases lists the scope's IdP aliases for the selector: "default" first, then the
// rest alphabetically, always including the current selection (so a brand-new alias
// being added is selectable before it exists on the server).
function idpAliases(
  list: SSOConfigDTO[] | undefined,
  current: string,
): string[] {
  const set = new Set<string>(['default', current])
  for (const i of list ?? []) set.add(i.alias || 'default')
  return [...set].sort((a, b) =>
    a === 'default' ? -1 : b === 'default' ? 1 : a.localeCompare(b),
  )
}

/** A provider's name: the administrator's display name, else the primary's label or the
 * alias. The alias stays the selection key; it is not the name people read. */
function idpName(
  t: (key: string) => string,
  alias: string,
  displayName?: string,
): string {
  return (
    displayName?.trim() ||
    (alias === 'default' ? t('console:sso.idp.default') : alias)
  )
}

/** The providers people can sign in with now, by name. */
function activeNames(
  t: (key: string) => string,
  list: SSOConfigDTO[],
): string[] {
  return list
    .filter((i) => i.status === 'active')
    .map((i) => idpName(t, i.alias || 'default', i.display_name))
}

function SSOForm({
  current,
  scope,
  alias,
  onClose,
}: {
  current: SSOConfigDTO
  scope?: string
  alias: string
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  // What THIS build serves, as the server reports it per IdP row. A control the build
  // cannot serve is not offered; its stored value travels unchanged in the save below.
  const enforces = current.enforced_by !== 'unavailable'
  const mapsGroups = current.groups_mapped_by !== 'unavailable'
  const routes = current.routed_by !== 'unavailable'
  const [protocol, setProtocol] = useState(current.protocol || 'oidc')
  const [enabled, setEnabled] = useState(current.status === 'active')
  // The name on the sign-in button: sent only when changed (omitted keeps the stored one;
  // empty restores "Single sign-on").
  const [displayName, setDisplayName] = useState(current.display_name ?? '')
  // The MFA mapping: sent only when touched (omitted keeps the stored mapping).
  const [mapping, setMapping] = useState<AssuranceMapping>(
    current.assurance_mapping ?? DEFAULT_MAPPING,
  )
  const [mappingTouched, setMappingTouched] = useState(false)
  // OIDC
  const [issuer, setIssuer] = useState(current.oidc_issuer ?? '')
  const [clientId, setClientId] = useState(current.oidc_client_id ?? '')
  const [clientSecret, setClientSecret] = useState('')
  // SAML
  const [metadataUrl, setMetadataUrl] = useState(
    current.saml_metadata_url ?? '',
  )
  const [entityId, setEntityId] = useState(current.saml_entity_id ?? '')
  const [acsUrl, setAcsUrl] = useState(
    current.saml_acs_url || current.redirect_uri || '',
  )
  const [idpSsoUrl, setIdpSsoUrl] = useState(current.saml_idp_sso_url ?? '')
  const [emailAttr, setEmailAttr] = useState(current.saml_email_attr ?? '')
  const [spCert, setSpCert] = useState(current.saml_sp_cert_pem ?? '')
  const [spKey, setSpKey] = useState('')
  // SP SIGNING keypair — independent of the encryption pair above. The public
  // cert round-trips; the private key is write-only (blank keeps the sealed value).
  const [spSignCert, setSpSignCert] = useState(
    current.saml_sp_sign_cert_pem ?? '',
  )
  const [spSignKey, setSpSignKey] = useState('')
  // Login-enforcement posture — protocol-independent, applies to both OIDC and SAML.
  const [requireSso, setRequireSso] = useState(current.require_sso)
  const [allowlistText, setAllowlistText] = useState(
    current.network_allowlist.join('\n'),
  )
  // Group mapping + JIT coherence. The claim/attr is protocol-specific; SCIM
  // authority is protocol-independent.
  const [groupsClaim, setGroupsClaim] = useState(
    current.oidc_groups_claim ?? '',
  )
  const [groupsAttr, setGroupsAttr] = useState(current.saml_groups_attr ?? '')
  const [scimAuthoritative, setScimAuthoritative] = useState(
    current.scim_authoritative,
  )
  //U5 home-realm domains (protocol-independent), one per line.
  const [domainsText, setDomainsText] = useState(
    current.claimed_domains.join('\n'),
  )

  // The enforcement posture is parsed identically regardless of protocol. The
  // backend validates the CIDRs and rejects malformed ones (HTTP 400).
  // Posture + JIT coherence are protocol-independent; the groups claim/attr is set
  // per protocol in buildInput.
  const posture = {
    require_sso: requireSso,
    network_allowlist: allowlistText
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean),
    scim_authoritative: scimAuthoritative,
    claimed_domains: domainsText
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean),
  }

  const naming = {
    ...(displayName.trim() !== (current.display_name ?? '').trim()
      ? { display_name: displayName.trim() }
      : {}),
    ...(mappingTouched ? { assurance_mapping: mapping } : {}),
  }

  function buildInput(): SSOConfigInput {
    if (protocol === 'oidc') {
      return {
        protocol,
        enabled,
        ...naming,
        oidc_issuer: issuer.trim(),
        oidc_client_id: clientId.trim(),
        oidc_client_secret: clientSecret,
        oidc_groups_claim: groupsClaim.trim(),
        ...posture,
      }
    }
    return {
      protocol,
      enabled,
      ...naming,
      saml_metadata_url: metadataUrl.trim(),
      saml_entity_id: entityId.trim(),
      saml_acs_url: acsUrl.trim(),
      saml_idp_sso_url: idpSsoUrl.trim(),
      saml_email_attr: emailAttr.trim(),
      saml_groups_attr: groupsAttr.trim(),
      saml_sp_cert_pem: spCert.trim(),
      saml_sp_key_pem: spKey,
      // Both halves of the SIGNING keypair travel too. Omitting either one is not a
      // missing feature but a broken login: the engine pairs them, and a config with one
      // half fails to build (ErrNotConfigured). Guarded by
      // TestConsolePayloadCarriesBothHalvesOfEverySAMLKeypair in core/api.
      saml_sp_sign_cert_pem: spSignCert.trim(),
      saml_sp_sign_key_pem: spSignKey,
      ...posture,
    }
  }

  const save = usePrivilegedMutation<void, SSOConfigDTO>({
    mutationFn: () => consoleApi.putSSO(buildInput(), scope, alias),
    invalidateKeys: () => [
      consoleKeys.sso(scope, alias),
      consoleKeys.ssoIdps(scope),
    ],
    successMessage: t('console:sso.saved'),
    onDone: onClose,
  })

  // La política de reporte vive en un solo sitio: una llamada escrita a mano conserva su
  // `catch` para la limpieza y DELEGA el reporte (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter('console')
  // No ejecutes la petición de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const [testing, setTesting] = useState(false)
  async function test() {
    setTesting(true)
    try {
      await consoleApi.testSSO(buildInput(), scope, alias)
      toast.success(t('console:sso.tested'))
    } catch (err) {
      // ⛔ ASEGURAMIENTO ANTES QUE ROJO. Este `test` es una escritura gateada por AAL3
      // (core/api/server.go:672 → handleTestSSOConfig), y este `catch` pintaba cualquier `ApiError.message` en rojo —
      // incluido el `step_up_required`, que NO es un fallo sino una ceremonia pendiente.
      //
      // Y NO BASTA con que el diálogo esté envuelto en `RequireAssurance`: ese pre-gate decide
      // sobre el `principal.aal` CACHEADO (identity/assurance.tsx:49-78) y `whoami` no tiene
      // `refetchInterval` (lib/auth/context.tsx:68-78), mientras el motor degrada AAL3 a AAL1
      // a los 15 minutos (core/auth/assurance.go:31-54). La caché puede decir AAL3 con el
      // motor en AAL1: el pre-gate deja pasar y el rechazo llega igual.
      // **Pre-gateado no es cubierto** — lo levantó el contraste de.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => void test()),
        )
        return
      }
      const msg =
        err instanceof ApiError
          ? err.message
          : t('common:errors.generic', { defaultValue: 'Failed' })
      toast.error(msg)
    } finally {
      setTesting(false)
    }
  }

  const valid =
    [...displayName.trim()].length <= 80 &&
    !mappingProblem(mapping) &&
    (protocol === 'oidc'
      ? issuer.trim() !== '' && clientId.trim() !== ''
      : entityId.trim() !== '' &&
        metadataUrl.trim() !== '' &&
        acsUrl.trim() !== '' &&
        idpSsoUrl.trim() !== '')

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:sso.title')}</DialogTitle>
        <DialogDescription>{t('console:sso.caption')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('console:sso.protocol')} htmlFor="sso-proto">
            <Select value={protocol} onValueChange={setProtocol}>
              <SelectTrigger id="sso-proto">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="oidc">OIDC</SelectItem>
                <SelectItem value="saml">SAML</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <div className="flex items-center gap-2 pt-6">
            <Switch
              id="sso-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
            />
            <Label htmlFor="sso-enabled">{t('console:sso.enabled')}</Label>
          </div>
        </div>

        <Field
          label={t('console:sso.displayName')}
          htmlFor="sso-display-name"
          description={t('console:sso.displayNameHint')}
        >
          <Input
            id="sso-display-name"
            value={displayName}
            maxLength={80}
            placeholder={t('console:sso.displayNameFallback')}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </Field>

        {protocol === 'oidc' ? (
          <div className="flex flex-col gap-4">
            <Field
              label={t('console:sso.issuer')}
              htmlFor="oidc-issuer"
              required
            >
              <Input
                id="oidc-issuer"
                value={issuer}
                onChange={(e) => setIssuer(e.target.value)}
                mono
              />
            </Field>
            <Field
              label={t('console:sso.clientId')}
              htmlFor="oidc-cid"
              required
            >
              <Input
                id="oidc-cid"
                value={clientId}
                onChange={(e) => setClientId(e.target.value)}
                mono
              />
            </Field>
            <Field
              label={t('console:sso.clientSecret')}
              htmlFor="oidc-secret"
              description={
                current.oidc_client_secret_hint
                  ? t('console:sso.secretSet', {
                      hint: current.oidc_client_secret_hint,
                    })
                  : undefined
              }
            >
              <Input
                id="oidc-secret"
                type="password"
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
              />
            </Field>
            {mapsGroups && (
              <Field
                label={t('console:sso.groupsClaim')}
                htmlFor="oidc-groups"
                description={t('console:sso.groupsClaimHint')}
              >
                <Input
                  id="oidc-groups"
                  value={groupsClaim}
                  onChange={(e) => setGroupsClaim(e.target.value)}
                  mono
                />
              </Field>
            )}
          </div>
        ) : (
          <div className="flex flex-col gap-4">
            <Field
              label={t('console:sso.metadataUrl')}
              htmlFor="saml-meta"
              required
            >
              <Input
                id="saml-meta"
                value={metadataUrl}
                onChange={(e) => setMetadataUrl(e.target.value)}
                mono
              />
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t('console:sso.entityId')}
                htmlFor="saml-entity"
                required
              >
                <Input
                  id="saml-entity"
                  value={entityId}
                  onChange={(e) => setEntityId(e.target.value)}
                  mono
                />
              </Field>
              <Field
                label={t('console:sso.idpSsoUrl')}
                htmlFor="saml-idp"
                required
              >
                <Input
                  id="saml-idp"
                  value={idpSsoUrl}
                  onChange={(e) => setIdpSsoUrl(e.target.value)}
                  mono
                />
              </Field>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label={t('console:sso.acsUrl')}
                htmlFor="saml-acs"
                required
              >
                <Input
                  id="saml-acs"
                  value={acsUrl}
                  onChange={(e) => setAcsUrl(e.target.value)}
                  mono
                />
              </Field>
              <Field
                label={t('console:sso.emailAttr')}
                htmlFor="saml-email"
                description={t('console:sso.emailAttrHint')}
              >
                <Input
                  id="saml-email"
                  value={emailAttr}
                  onChange={(e) => setEmailAttr(e.target.value)}
                  mono
                />
              </Field>
            </div>
            {mapsGroups && (
              <Field
                label={t('console:sso.groupsAttr')}
                htmlFor="saml-groups"
                description={t('console:sso.groupsAttrHint')}
              >
                <Input
                  id="saml-groups"
                  value={groupsAttr}
                  onChange={(e) => setGroupsAttr(e.target.value)}
                  mono
                />
              </Field>
            )}
            <Field label={t('console:sso.spCert')} htmlFor="saml-cert">
              <Textarea
                id="saml-cert"
                value={spCert}
                onChange={(e) => setSpCert(e.target.value)}
                rows={3}
              />
            </Field>
            <Field
              label={t('console:sso.spKey')}
              htmlFor="saml-key"
              description={
                current.saml_sp_key_hint
                  ? t('console:sso.keySet', { hint: current.saml_sp_key_hint })
                  : undefined
              }
            >
              <Textarea
                id="saml-key"
                value={spKey}
                onChange={(e) => setSpKey(e.target.value)}
                rows={3}
              />
            </Field>
            {/* SP SIGNING keypair — signs AuthnRequests, published in the SP
                metadata as the use="signing" descriptor. Independent of the encryption
                pair above: RSA or EC is accepted here, RSA only above. */}
            <Field
              label={t('console:sso.spSignCert')}
              htmlFor="saml-sign-cert"
              description={t('console:sso.spSignCertHint')}
            >
              <Textarea
                id="saml-sign-cert"
                value={spSignCert}
                onChange={(e) => setSpSignCert(e.target.value)}
                rows={3}
              />
            </Field>
            <Field
              label={t('console:sso.spSignKey')}
              htmlFor="saml-sign-key"
              description={
                current.saml_sp_sign_key_hint
                  ? t('console:sso.keySet', {
                      hint: current.saml_sp_sign_key_hint,
                    })
                  : undefined
              }
            >
              <Textarea
                id="saml-sign-key"
                value={spSignKey}
                onChange={(e) => setSpSignKey(e.target.value)}
                rows={3}
              />
            </Field>
          </div>
        )}

        {/* Login-enforcement posture — applies to both OIDC and SAML. Offered only by a
            build that enforces it; a hidden field keeps its stored value on save. */}
        {enforces && (
          <div className="flex flex-col gap-4 border-t border-border pt-4">
            <div>
              <h3 className="text-body font-medium text-foreground">
                {t('console:sso.enforcement.title')}
              </h3>
              <p className="text-body text-muted-foreground">
                {t('console:sso.enforcement.caption')}
              </p>
            </div>
            <div className="flex items-start gap-2">
              <Switch
                id="sso-require"
                checked={requireSso}
                onCheckedChange={setRequireSso}
                className="mt-0.5"
              />
              <div className="flex flex-col gap-1">
                <Label htmlFor="sso-require">
                  {t('console:sso.enforcement.requireSso')}
                </Label>
                <p className="text-body text-muted-foreground">
                  {t('console:sso.enforcement.requireSsoHint')}
                </p>
                {requireSso && !enabled && (
                  <p className="flex items-start gap-2 text-body text-warning">
                    <ShieldAlert
                      className="mt-0.5 size-4 shrink-0"
                      aria-hidden
                    />
                    {t('console:sso.enforcement.noIdpWarning')}
                  </p>
                )}
              </div>
            </div>
            <Field
              label={t('console:sso.enforcement.networkAllowlist')}
              htmlFor="sso-allowlist"
              description={t('console:sso.enforcement.networkAllowlistHint')}
            >
              <Textarea
                id="sso-allowlist"
                value={allowlistText}
                onChange={(e) => setAllowlistText(e.target.value)}
                rows={3}
                mono
              />
            </Field>
            {current.enforced_by === 'out_of_scope' && (
              <p className="flex items-start gap-2 text-body text-warning">
                <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
                {t('console:sso.enforcement.outOfScopeBanner')}
              </p>
            )}
          </div>
        )}

        {/* Group mapping + JIT coherence — protocol-independent. */}
        <div className="flex flex-col gap-4 border-t border-border pt-4">
          <div>
            <h3 className="text-body font-medium text-foreground">
              {mapsGroups
                ? t('console:sso.groups.title')
                : t('console:sso.groups.provisioningTitle')}
            </h3>
            <p className="text-body text-muted-foreground">
              {mapsGroups
                ? t('console:sso.groups.caption')
                : t('console:sso.groups.provisioningCaption')}
            </p>
          </div>
          <div className="flex items-start gap-2">
            <Switch
              id="sso-scim-authoritative"
              checked={scimAuthoritative}
              onCheckedChange={setScimAuthoritative}
              className="mt-0.5"
            />
            <div className="flex flex-col gap-1">
              <Label htmlFor="sso-scim-authoritative">
                {t('console:sso.groups.scimAuthoritative')}
              </Label>
              <p className="text-body text-muted-foreground">
                {t('console:sso.groups.scimAuthoritativeHint')}
              </p>
            </div>
          </div>
        </div>

        {/* Home-realm routing (U5) — protocol-independent. Offered only by a build
            that routes by domain; a hidden field keeps its stored value on save. */}
        {routes && (
          <div className="flex flex-col gap-4 border-t border-border pt-4">
            <div>
              <h3 className="text-body font-medium text-foreground">
                {t('console:sso.domains.title')}
              </h3>
              <p className="text-body text-muted-foreground">
                {t('console:sso.domains.caption')}
              </p>
            </div>
            <Field
              label={t('console:sso.domains.label')}
              htmlFor="sso-domains"
              description={t('console:sso.domains.hint')}
            >
              <Textarea
                id="sso-domains"
                value={domainsText}
                onChange={(e) => setDomainsText(e.target.value)}
                rows={3}
                mono
              />
            </Field>
          </div>
        )}
        <AssuranceMappingEditor
          protocol={protocol}
          value={mapping}
          onChange={(next) => {
            setMapping(next)
            setMappingTouched(true)
          }}
        />
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={() => void test()}
          disabled={!valid || testing || save.isPending}
        >
          {testing && <Spinner size="sm" aria-hidden />}
          {t('console:sso.test')}
        </Button>
        <Button
          variant="primary"
          onClick={() => save.mutate()}
          disabled={!valid || save.isPending}
        >
          {save.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:sso.save')}
        </Button>
      </DialogFooter>
    </>
  )
}
