// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { KeyRound, ShieldAlert, Trash2 } from 'lucide-react'
import { useState, type ComponentType, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
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
import { PANEL_EXTENSIONS } from '@/features/extensions'
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

/** Console › SSO: the deployment-wide identity provider, for a superadmin. A Business
 * build replaces the tab with its own (PANEL_EXTENSIONS.ssoTab). */
export function SSOTab() {
  const { t } = useTranslation(['console'])
  const { isSuperadmin } = useAuth()
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
  const Replacement = PANEL_EXTENSIONS.ssoTab
  return Replacement ? <Replacement /> : <SingleProviderTab />
}

/** The one identity provider people sign in with: the deployment-wide default. */
function SingleProviderTab() {
  const { t } = useTranslation(['console'])
  const [editOpen, setEditOpen] = useState(false)
  const [removeOpen, setRemoveOpen] = useState(false)

  const sso = useQuery({
    queryKey: consoleKeys.sso(),
    queryFn: () => consoleApi.getSSO(),
  })

  const removeMutation = usePrivilegedMutation<void, void>({
    mutationFn: () => consoleApi.deleteSSO(),
    invalidateKeys: () => [consoleKeys.sso()],
    successMessage: t('console:sso.deleted'),
    onDone: () => setRemoveOpen(false),
  })

  const cfg = sso.data

  return (
    <div className="flex flex-col gap-4 pt-4">
      <SSOOverview
        cfg={cfg}
        loading={sso.isLoading}
        error={sso.error}
        retry={() => void sso.refetch()}
        onEdit={() => setEditOpen(true)}
        onRemove={() => setRemoveOpen(true)}
      />

      <Dialog open={editOpen} onOpenChange={setEditOpen}>
        <DialogContent className="max-h-[85vh] max-w-2xl overflow-y-auto">
          {editOpen && cfg && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <SSOForm current={cfg} onClose={() => setEditOpen(false)} />
            </RequireAssurance>
          )}
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

/** The heading with its Configure/Edit and Remove actions, then one provider's status
 * card. `controls` sits between the two; `children` closes the card. */
export function SSOOverview({
  cfg,
  loading,
  error,
  retry,
  onEdit,
  onRemove,
  controls,
  children,
}: {
  cfg?: SSOConfigDTO
  loading: boolean
  error?: unknown
  retry?: () => void
  onEdit: () => void
  onRemove: () => void
  controls?: ReactNode
  children?: ReactNode
}) {
  const { t } = useTranslation(['console'])
  return (
    <>
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:sso.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:sso.caption')}
          </p>
        </div>
        {cfg && !loading && !error && (
          <div className="flex gap-2">
            {cfg.configured && (
              <Button variant="ghost" onClick={onRemove}>
                <Trash2 />
                {t('console:sso.delete')}
              </Button>
            )}
            <Button onClick={onEdit}>
              <KeyRound />
              {cfg?.configured
                ? t('console:sso.edit')
                : t('console:sso.configure')}
            </Button>
          </div>
        )}
      </div>

      {controls}

      {loading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : error || !cfg ? (
        <QueryErrorState error={error} retry={retry} />
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
          {children}
        </div>
      )}
    </>
  )
}

/** Stored with each provider and replaced verbatim by every save, so the form sends them
 * back as stored. Only a form that offers them (`extra`) changes them. */
export interface SSOStoredFields {
  require_sso: boolean
  network_allowlist: string[]
  oidc_groups_claim: string
  saml_groups_attr: string
  claimed_domains: string[]
}

/** Fields a Business form adds to the provider form, after the protocol fields. */
export interface SSOFormExtraProps {
  current: SSOConfigDTO
  protocol: string
  enabled: boolean
  value: SSOStoredFields
  onChange: (next: SSOStoredFields) => void
}

export function SSOForm({
  current,
  scope,
  alias = 'default',
  onClose,
  extra: Extra,
}: {
  current: SSOConfigDTO
  scope?: string
  alias?: string
  onClose: () => void
  extra?: ComponentType<SSOFormExtraProps>
}) {
  const { t } = useTranslation(['console', 'common'])
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
  // SCIM authority over accounts (inbound SCIM), protocol-independent.
  const [scimAuthoritative, setScimAuthoritative] = useState(
    current.scim_authoritative,
  )
  const [stored, setStored] = useState<SSOStoredFields>({
    require_sso: current.require_sso,
    network_allowlist: current.network_allowlist,
    oidc_groups_claim: current.oidc_groups_claim ?? '',
    saml_groups_attr: current.saml_groups_attr ?? '',
    claimed_domains: current.claimed_domains,
  })

  // Protocol-independent fields; the groups claim/attr is set per protocol in buildInput.
  // The backend validates CIDRs (400) and domains (400/409).
  const posture = {
    require_sso: stored.require_sso,
    network_allowlist: stored.network_allowlist,
    scim_authoritative: scimAuthoritative,
    claimed_domains: stored.claimed_domains,
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
        oidc_groups_claim: stored.oidc_groups_claim.trim(),
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
      saml_groups_attr: stored.saml_groups_attr.trim(),
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
    invalidateKeys: () => [consoleKeys.sso(scope, alias)],
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
      // Handle step-up before displaying a red error. This test performs an AAL3-gated write
      // (core/api/server.go:672 -> handleTestSSOConfig). The catch previously displayed every
      // `ApiError.message`
      // as a failure, including `step_up_required`, which requests step-up.
      //
      // `RequireAssurance` alone does not cover this: it reads cached `principal.aal`
      // (`identity/assurance.tsx:49-78`), and `whoami` has no `refetchInterval`
      // (`lib/auth/context.tsx:68-78`). The engine downgrades AAL3 to AAL1 after 15 minutes
      // (`core/auth/assurance.go:31-54`). The cache can still say AAL3 while the engine says
      // AAL1,
      // allowing the write through the pre-gate before the engine rejects it.
      // The review identified this gap: a pre-gate does not fully cover step-up handling.
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

        {Extra && (
          <Extra
            current={current}
            protocol={protocol}
            enabled={enabled}
            value={stored}
            onChange={setStored}
          />
        )}

        <div className="flex flex-col gap-4 border-t border-border pt-4">
          <div>
            <h3 className="text-body font-medium text-foreground">
              {t('console:sso.groups.provisioningTitle')}
            </h3>
            <p className="text-body text-muted-foreground">
              {t('console:sso.groups.provisioningCaption')}
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
