// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// privileged, phishing-resistant login for the panel operator:
// WebAuthn/FIDO2 passkey registration (AAL3, NIST SP 800-63B-4) + PIV/CAC X.509
// client-cert status (FIPS 201-3, cert-to-role + OCSP). The browser runs the
// ceremony; the backend (first-party auth seam) issues the challenge and
// VERIFIES. All of this is a DECLARED seam today (no backend) → the panel
// orchestrates + fails closed, shows the honest pending seam, and makes NO
// NIST/FIPS conformance claim the backend does not guarantee. The
// session AAL drives the gate on the WIF/identity views.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Fingerprint, IdCard, Pencil, Plus, Trash2 } from 'lucide-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SectionCard, SelfAuditNotice } from '@/features/_intel'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Input } from '@/components/ui/input'
import { KvList, KvRow } from '@/components/ui/kv'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { useSessionStore } from '@/stores/session'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { ApiError } from '@/lib/api/errors'
import {
  identityApi,
  identityKeys,
  isContractPending,
  isPivNotConfigured,
} from './api'
import type { WebAuthnCredentialItem } from './types'
import { AAL, StepUpPanel, aalLabel, useAssurance } from './assurance'
import {
  AuthorityLink,
  ContractPendingNotice,
  DeclaredSection,
} from './components'
import { AuthorityReferences } from './references'
import {
  canEnrollPasskey,
  enrollPasskey,
  RegistrationOutcomeUnknown,
} from './enroll-passkey'
import { useStepUpOwner } from '@/stores/step-up'
import { useResumeGuard } from '@/lib/hooks/use-resume-guard'
import { isPivKnownUnconfigured, pivStatusQueryKey } from './piv-configuration'

export function PrivilegedLoginTab() {
  return (
    <div className="flex flex-col gap-6">
      <AssuranceStatusSection />
      <PasskeysManagementSection />
      <PivStatusSection />
      <AuthorityReferences area="login" keys={['webauthn', 'piv']} />
    </div>
  )
}

function AssuranceStatusSection() {
  const { t } = useTranslation('identity')
  const { aal, amr } = useAssurance()
  return (
    <SectionCard
      title={t('login.statusTitle')}
      description={t('login.statusDescription')}
    >
      <KvList>
        <KvRow label={t('login.currentAal')}>
          <Badge variant={aal >= AAL.HARDWARE ? 'success' : 'warning'}>
            {aalLabel(aal, t)}
          </Badge>
        </KvRow>
        <KvRow label={t('login.methods')} align="start">
          {amr.length > 0 ? (
            <span className="flex flex-wrap gap-1">
              {amr.map((m) => (
                <Badge key={m} variant="outline">
                  {m}
                </Badge>
              ))}
            </span>
          ) : (
            <span className="text-muted-foreground">
              {t('login.methodsUnknown')}
            </span>
          )}
        </KvRow>
      </KvList>
      <p className="mt-2 text-xs text-muted-foreground">
        {t('login.targetStandardsNote')}
      </p>
      {aal < AAL.HARDWARE ? (
        <div className="mt-3">
          <StepUpPanel
            minAal={AAL.HARDWARE}
            currentAal={aal}
            action="identity"
          />
        </div>
      ) : null}
    </SectionCard>
  )
}

function PasskeysManagementSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()

  const [registerOpen, setRegisterOpen] = useState(false)
  const [renaming, setRenaming] = useState<WebAuthnCredentialItem | null>(null)
  const [deleting, setDeleting] = useState<WebAuthnCredentialItem | null>(null)

  const credentialsQuery = useQuery({
    queryKey: identityKeys.webauthnCredentials(activeTenant),
    queryFn: () => identityApi.webauthnCredentials(),
    retry: false,
  })

  const deleteMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => identityApi.webauthnDelete(id),
    invalidateKeys: () => [identityKeys.webauthnCredentials(activeTenant)],
    successMessage: t('login.passkeys.deleted'),
    onDone: () => setDeleting(null),
  })

  const handleRegistered = useCallback(() => {
    setRegisterOpen(false)
    void qc.invalidateQueries({
      queryKey: identityKeys.webauthnCredentials(activeTenant),
    })
    toast.success(t('login.passkeys.registered'))
  }, [qc, activeTenant, t])

  const handleRenamed = useCallback(() => {
    setRenaming(null)
    void qc.invalidateQueries({
      queryKey: identityKeys.webauthnCredentials(activeTenant),
    })
    toast.success(t('login.passkeys.renamed'))
  }, [qc, activeTenant, t])

  const credentials = credentialsQuery.data?.items ?? []

  // Handle pending seam (backend not yet live)
  if (credentialsQuery.isError && isContractPending(credentialsQuery.error)) {
    return (
      <SectionCard
        title={t('login.passkeys.title')}
        description={t('login.passkeys.description')}
      >
        <SelfAuditNotice />
        <div className="mt-3">
          <ContractPendingNotice what={t('login.passkeySeamWhat')} />
        </div>
      </SectionCard>
    )
  }

  // A genuine read failure is not an empty credential inventory. In particular,
  // keep registration/rename/delete unavailable until the operator has either a
  // successful list or an explicit pending-seam answer from the engine.
  if (credentialsQuery.isError) {
    return (
      <SectionCard
        title={t('login.passkeys.title')}
        description={t('login.passkeys.description')}
      >
        <SelfAuditNotice />
        <ErrorState
          className="py-6"
          retry={() => void credentialsQuery.refetch()}
        />
      </SectionCard>
    )
  }

  return (
    <SectionCard
      title={t('login.passkeys.title')}
      description={t('login.passkeys.description')}
    >
      <SelfAuditNotice />

      <div className="mt-3 flex justify-end">
        <Button onClick={() => setRegisterOpen(true)}>
          <Plus className="size-4" aria-hidden />
          {t('login.passkeys.register')}
        </Button>
      </div>

      {credentialsQuery.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : credentials.length === 0 ? (
        <EmptyState
          title={t('login.passkeys.none')}
          description={t('login.passkeys.noneHint')}
          icon={<Fingerprint />}
        />
      ) : (
        <div className="mt-3 overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium">
                  {t('login.passkeys.colName')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('login.passkeys.colCreated')}
                </th>
                <th className="px-3 py-2 font-medium">
                  {t('login.passkeys.colBackup')}
                </th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {credentials.map((cred) => (
                <tr key={cred.id} className="border-t border-border align-top">
                  <td className="px-3 py-2">
                    <span className="font-medium text-foreground">
                      {cred.name}
                    </span>
                  </td>
                  <td className="px-3 py-2">
                    <RelTimeLabel ts={cred.created_at} />
                  </td>
                  <td className="px-3 py-2">
                    <Badge
                      variant={cred.backup_eligible ? 'accent' : 'outline'}
                    >
                      {cred.backup_eligible
                        ? t('login.passkeys.backupEligible')
                        : t('login.passkeys.backupBound')}
                    </Badge>
                  </td>
                  <td className="px-3 py-2 text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setRenaming(cred)}
                      >
                        <Pencil className="size-3.5" aria-hidden />
                        {t('login.passkeys.rename')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setDeleting(cred)}
                      >
                        <Trash2 className="size-3.5" aria-hidden />
                        {t('login.passkeys.delete')}
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Register dialog */}
      <Dialog open={registerOpen} onOpenChange={setRegisterOpen}>
        <DialogContent className="max-w-md">
          {registerOpen && (
            <RegisterPasskeyForm
              onRegistered={handleRegistered}
              onClose={() => setRegisterOpen(false)}
            />
          )}
        </DialogContent>
      </Dialog>

      {/* Rename dialog */}
      <Dialog
        open={renaming !== null}
        onOpenChange={(o) => !o && setRenaming(null)}
      >
        <DialogContent className="max-w-md">
          {renaming && (
            <RenamePasskeyForm
              credential={renaming}
              onRenamed={handleRenamed}
              onClose={() => setRenaming(null)}
            />
          )}
        </DialogContent>
      </Dialog>

      {/* Delete confirmation */}
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={t('login.passkeys.deleteTitle')}
        description={t('login.passkeys.deleteBody', {
          name: deleting?.name ?? '',
        })}
        confirmLabel={t('login.passkeys.delete')}
        tone="danger"
        pending={deleteMutation.isPending}
        onConfirm={() => {
          if (!deleting) return
          deleteMutation.mutate(deleting.id)
        }}
      />
    </SectionCard>
  )
}

function RegisterPasskeyForm({
  onRegistered,
  onClose,
}: {
  onRegistered: () => void
  onClose: () => void
}) {
  const { t } = useTranslation(['identity', 'common'])
  const [name, setName] = useState('')
  const [pending, setPending] = useState(false)
  const report = useFailedActionReporter('identity')
  const captureOwner = useStepUpOwner()
  const guardResume = useResumeGuard()
  const [failure, setFailure] = useState<
    'unknown' | 'sessionExpired' | 'pending' | null
  >(null)
  const valid = name.trim().length > 0

  async function handleRegister() {
    if (
      pending ||
      failure === 'unknown' ||
      failure === 'sessionExpired' ||
      !valid
    )
      return
    if (!canEnrollPasskey()) {
      toast.error(t('login.passkeys.unsupported'))
      return
    }
    const owner = captureOwner()
    const attempt = owner.begin()
    setPending(true)
    setFailure(null)
    try {
      await enrollPasskey(name, attempt)
      attempt.dispatchGuard()
      onRegistered()
    } catch (err) {
      if (!attempt.current()) return
      // Additional credentials still require authentication with an existing factor.
      // Resuming starts a NEW challenge, never resends the prior attestation.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardResume(() => void handleRegister()),
          owner,
        )
        return
      }
      if (err instanceof RegistrationOutcomeUnknown) setFailure('unknown')
      else if (err instanceof ApiError && err.isUnauthenticated)
        setFailure('sessionExpired')
      else if (isContractPending(err)) setFailure('pending')
      else toast.error(t('login.passkeys.registerFailed'))
    } finally {
      if (attempt.current()) setPending(false)
      attempt.retire()
    }
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('login.passkeys.registerTitle')}</DialogTitle>
      </DialogHeader>
      <p className="text-sm text-muted-foreground">
        {t('login.passkeys.registerHint')}
      </p>
      {failure === 'pending' && (
        <ContractPendingNotice what={t('assurance.seamWhat')} />
      )}
      {(failure === 'unknown' || failure === 'sessionExpired') && (
        <p role="alert" className="text-sm text-warning">
          {t(
            failure === 'unknown'
              ? 'assurance.registrationUnknownElsewhere'
              : 'assurance.sessionExpired',
          )}
        </p>
      )}
      <div className="flex flex-col gap-4">
        <label className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">
            {t('login.passkeys.nameLabel')}
          </span>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('login.passkeys.namePlaceholder')}
          />
        </label>
      </div>
      <DialogFooter>
        <Button variant="secondary" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => void handleRegister()}
          disabled={
            !valid ||
            pending ||
            failure === 'unknown' ||
            failure === 'sessionExpired'
          }
        >
          {pending && <Spinner size="sm" aria-hidden />}
          {pending
            ? t('login.passkeys.registering')
            : t('login.passkeys.register')}
        </Button>
      </DialogFooter>
    </>
  )
}

function RenamePasskeyForm({
  credential,
  onRenamed,
  onClose,
}: {
  credential: WebAuthnCredentialItem
  onRenamed: () => void
  onClose: () => void
}) {
  const { t } = useTranslation(['identity', 'common'])
  const [name, setName] = useState(credential.name)
  const [pending, setPending] = useState(false)

  const valid = name.trim().length > 0 && name.trim() !== credential.name

  async function handleRename() {
    setPending(true)
    try {
      await identityApi.webauthnRename(credential.id, name.trim())
      onRenamed()
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : t('common:errors.generic'),
      )
    } finally {
      setPending(false)
    }
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('login.passkeys.renameTitle')}</DialogTitle>
      </DialogHeader>
      <div className="flex flex-col gap-4">
        <label className="flex flex-col gap-1.5">
          <span className="text-sm font-medium">
            {t('login.passkeys.nameLabel')}
          </span>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('login.passkeys.namePlaceholder')}
          />
        </label>
      </div>
      <DialogFooter>
        <Button variant="secondary" onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => void handleRename()}
          disabled={!valid || pending}
        >
          {pending && <Spinner size="sm" aria-hidden />}
          {t('login.passkeys.rename')}
        </Button>
      </DialogFooter>
    </>
  )
}

/**
 * THE OPERATOR'S SETUP GUIDE, not the engine's variable name.
 *
 * The unconfigured callout used to read "an operator enables it by setting
 * OLIVARES_PIV_CONFIG (agency CA, cert-to-role map) on the engine" — an engine
 * internal printed as the primary, and only, thing to do about a card that does
 * not work. A console user who reads it cannot act on it, and an administrator
 * who can does not learn it here first. The configuration surface belongs in the
 * documentation, which already carries it; the callout says who has to act and
 * points at where it is written.
 *
 * The page is the configuration reference, which documents the smart-card
 * configuration surface, at the same documentation base the topbar help icon
 * uses. It exists in this repository's docs tree as
 * docs-site/src/content/docs/reference/configuration.md — this constant is not a
 * new destination invented for the copy.
 */
const PIV_SETUP_GUIDE =
  'https://docs.olivares.ai/reference/configuration/'

function PivStatusSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant, principal } = useAuth()
  const credentialGeneration = useSessionStore((s) => s.credentialGeneration)
  const knownUnconfigured = isPivKnownUnconfigured(principal)
  const q = useQuery({
    queryKey: pivStatusQueryKey(activeTenant, principal, credentialGeneration),
    queryFn: () => identityApi.pivStatus(),
    retry: false,
    enabled: !knownUnconfigured,
  })
  // The explicit "PIV not configured on this deployment" state (the
  // backend route is live; 501 piv_not_configured means the smart-card
  // configuration is unset) — a real, known state, not the backend-pending
  // seam. Same pattern as the federation view's ErrSSONotConfigured. A
  // known-false whoami field is the same card and must not paint cached
  // presented status.
  //
  // THE PREDICATE IS UNCHANGED AND MUST STAY THAT WAY: only a known-false
  // whoami field or a typed 501 reaches this card. A transport failure, a 401
  // or any other error still falls through to DeclaredSection below, which
  // reports it as the failed read it is. Telling an operator that PIV is "not
  // configured" because a request did not arrive would be a worse answer than
  // the one this correction replaces.
  if (knownUnconfigured || (q.isError && isPivNotConfigured(q.error))) {
    return (
      <SectionCard
        title={t('login.pivTitle')}
        description={t('login.pivDescription')}
      >
        <p role="status" className="text-sm text-muted-foreground">
          {t('login.pivNotConfigured')}
        </p>
        <p className="mt-2 text-sm">
          <AuthorityLink
            href={PIV_SETUP_GUIDE}
            className="font-sans text-sm break-normal"
          >
            {t('login.pivSetupGuide')}
          </AuthorityLink>
        </p>
        <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
          <IdCard className="size-3.5 shrink-0" aria-hidden />
          {t('login.pivNote')}
        </p>
      </SectionCard>
    )
  }
  return (
    <SectionCard
      title={t('login.pivTitle')}
      description={t('login.pivDescription')}
    >
      <DeclaredSection
        query={q}
        what={t('login.pivSeamWhat')}
        skeletonHeight={100}
      >
        {(piv) => (
          <KvList>
            <KvRow label={t('login.pivPresented')}>
              <Badge variant={piv.presented ? 'success' : 'neutral'}>
                {piv.presented
                  ? t('common:status.active')
                  : t('login.pivAbsent')}
              </Badge>
            </KvRow>
            {piv.subject ? (
              <KvRow label={t('login.pivSubject')} mono align="start">
                {piv.subject}
              </KvRow>
            ) : null}
            {piv.mapped_role ? (
              <KvRow label={t('login.pivRole')}>
                <Badge variant="accent">{piv.mapped_role}</Badge>
              </KvRow>
            ) : null}
            <KvRow label={t('login.pivOcsp')}>
              <Badge
                variant={
                  piv.ocsp === 'good'
                    ? 'success'
                    : piv.ocsp === 'revoked'
                      ? 'danger'
                      : 'neutral'
                }
              >
                {t(`login.ocsp.${piv.ocsp ?? 'unknown'}`)}
              </Badge>
            </KvRow>
          </KvList>
        )}
      </DeclaredSection>
      <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
        <IdCard className="size-3.5 shrink-0" aria-hidden />
        {t('login.pivNote')}
      </p>
    </SectionCard>
  )
}
