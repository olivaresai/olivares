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
import { useCallback, useState, useRef, useEffect } from 'react'
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
import { ContractPendingNotice, DeclaredSection } from './components'
import { AuthorityReferences } from './references'
import {
  decodeCreationOptions,
  encodeAttestation,
  isWebAuthnSupported,
} from './webauthn'

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
  // El reporte vive en un sitio (use-privileged-mutation.ts:25-32) y el reintento no puede salir
  // de un diálogo ya cerrado (use-resume-guard.ts).
  const report = useFailedActionReporter('identity')
  // ⛔ El reintento va a un store GLOBAL y el host vive junto a toda la aplicación, así que este
  // diálogo puede desmontarse antes de que la ceremonia termine y el callback ejecutaría el
  // registro de un formulario muerto. La guarda va EN LÍNEA a propósito: el hook compartido
  // (`lib/hooks/use-resume-guard.ts`) nace en la rama de #784 y todavía no está en `main`;
  // acoplar las dos ramas para ahorrar seis líneas sería peor que duplicarlas. Cuando #784
  // aterrice, esto se colapsa en el hook.
  const montado = useRef(true)
  useEffect(() => {
    montado.current = true
    return () => {
      montado.current = false
    }
  }, [])

  const valid = name.trim().length > 0

  async function handleRegister() {
    if (
      !isWebAuthnSupported() ||
      typeof navigator.credentials.create !== 'function'
    ) {
      toast.error(t('login.passkeys.unsupported'))
      return
    }
    setPending(true)
    try {
      const options = await identityApi.webauthnRegisterOptions()
      const credential = (await navigator.credentials.create({
        publicKey: decodeCreationOptions(options.publicKey),
      })) as PublicKeyCredential | null
      if (!credential) {
        toast.error(t('login.passkeys.registerFailed'))
        setPending(false)
        return
      }
      await identityApi.webauthnRegister(
        encodeAttestation(credential),
        name.trim(),
      )
      onRegistered()
    } catch (err) {
      // ⛔ AQUÍ EL `catch` ERA PELADO —ni siquiera ligaba el error— y convertía CUALQUIER fallo
      // en «registro fallido». Entre ellos, el único que tiene remedio: registrar una SEGUNDA
      // credencial exige AAL3 y el motor lo dice por su código
      // (`core/auth/webauthn.go:232-235` → `handlers_webauthn.go` → `core/api/errors.go:220-224`).
      //
      // La ironía era completa: tienes una passkey, intentas añadir la segunda, el motor te pide
      // que confirmes CON LA QUE YA TIENES, y la consola contestaba «registro fallido» — el
      // obstáculo sin la puerta, en la pantalla que existe justo para abrirla.
      //
      // Este emisor no pasa por `requireAAL3`, así que no salía en el censo de las 21 llamadas:
      // lo levantó el contraste Codex `sol max` (H2) al refutar mi afirmación de que sólo había
      // dos familias de emisores. Son cuatro.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(err, () => {
          if (montado.current) void handleRegister()
        })
        return
      }
      toast.error(t('login.passkeys.registerFailed'))
    } finally {
      setPending(false)
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
          disabled={!valid || pending}
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

function PivStatusSection() {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant } = useAuth()
  const q = useQuery({
    queryKey: identityKeys.piv(activeTenant),
    queryFn: () => identityApi.pivStatus(),
    retry: false,
  })
  // The explicit "PIV not configured on this deployment" state (the
  // backend route is live; 501 piv_not_configured means OLIVARES_PIV_CONFIG is
  // unset) — a real, known state, not the backend-pending seam. Same pattern
  // as the federation view's ErrSSONotConfigured.
  if (q.isError && isPivNotConfigured(q.error)) {
    return (
      <SectionCard
        title={t('login.pivTitle')}
        description={t('login.pivDescription')}
      >
        <p role="status" className="text-sm text-muted-foreground">
          {t('login.pivNotConfigured')}
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
