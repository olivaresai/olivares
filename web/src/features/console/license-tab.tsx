// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { EditionBadge } from './edition-badge'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { currentLanguage } from '@/lib/i18n'
import {
  BadgeCheck,
  Power,
  RotateCw,
  ShieldAlert,
  Trash2,
  Upload,
} from 'lucide-react'
import { type ReactNode, useId, useState, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { queryKeys } from '@/lib/api/query'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useResumeGuard } from '@/lib/hooks/use-resume-guard'
import { useAuth } from '@/lib/auth/context'
import { LicenseReadStatus, classifyLicenseRead } from './license-read'
import { LicenseBuildGuidance } from './license-build-guidance'
import { PANEL_EXTENSIONS } from '@/features/extensions'
import { useOfferedPanels } from '@/features/panels'
import {
  type ActivationAddonDTO,
  type ActivationFamilyDTO,
  type ActivationSelection,
  type ActivationStatusDTO,
  consoleApi,
  consoleKeys,
  type LicenseStatusDTO,
} from './api'
import { StaticTable } from '@/components/data/static-table'
import {
  activationDetail,
  activationLabel,
  activationText,
  activationTone,
} from './activation-label'
import { waitForEngineRestart } from './engine-restart'

// Badge tone for each license lifecycle status.
function statusVariant(
  status: string,
): 'success' | 'warning' | 'danger' | 'neutral' {
  switch (status) {
    case 'valid':
      return 'success'
    // A blob with no expiry is NOT a green state: commercial entitlements are
    // term-only, so no payment earns one. It renders as a caution, never success.
    case 'perpetual':
      return 'warning'
    case 'expired':
      return 'warning'
    case 'invalid':
      return 'danger'
    default:
      return 'neutral'
  }
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-start justify-between gap-4 py-2.5">
      <dt className="text-body text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right text-body break-words text-foreground">
        {children}
      </dd>
    </div>
  )
}

/** Historic facts are last success, never current green entitlement. */
function historicStatusVariant(
  status: string,
): 'warning' | 'danger' | 'neutral' {
  switch (status) {
    case 'invalid':
      return 'danger'
    case 'valid':
    case 'perpetual':
    case 'expired':
      return 'warning'
    default:
      return 'neutral'
  }
}

function LicenseFacts({
  license,
  historic = false,
}: {
  license: LicenseStatusDTO
  historic?: boolean
}) {
  const { t } = useTranslation(['console'])
  const editionVariant = historic
    ? 'neutral'
    : license.edition === 'enterprise'
      ? 'accent'
      : 'neutral'
  const statusTone = historic
    ? historicStatusVariant(license.status)
    : statusVariant(license.status)
  const showCheck =
    !historic && (license.status === 'valid' || license.status === 'perpetual')

  return (
    <div
      className="rounded-lg border border-border p-5"
      data-slot="license-facts"
      data-kind={historic ? 'historic' : 'current'}
    >
      <dl className="divide-y divide-border">
        <Row label={t('console:license.edition')}>
          <Badge variant={editionVariant}>
            {t(`console:license.editions.${license.edition}`, {
              defaultValue: license.edition,
            })}
          </Badge>
        </Row>
        <Row label={t('console:license.status')}>
          <Badge variant={statusTone}>
            {showCheck ? (
              <BadgeCheck className="size-3 shrink-0" aria-hidden />
            ) : null}
            {t(`console:license.statuses.${license.status}`, {
              defaultValue: license.status,
            })}
          </Badge>
        </Row>
        {license.licensee && (
          <Row label={t('console:license.licensee')}>{license.licensee}</Row>
        )}
        {license.plan && (
          <Row label={t('console:license.plan')}>{license.plan}</Row>
        )}
        {license.support_tier && (
          <Row label={t('console:license.supportTier')}>
            {license.support_tier}
          </Row>
        )}
        {/* B10: honest USAGE, never a quota — the count stands alone, labelled "no limit". */}
        <Row label={t('console:license.activeUsers')}>
          <span className="font-mono tabular-nums">
            {license.active_users}
            {license.active_users_capped ? '+' : ''}
          </span>
          <span className="ml-2 text-caption text-muted-foreground">
            {t('console:license.noUserLimit')}
          </span>
        </Row>
        {/* An expired licence keeps the single "Expires" row: its paid_through says nothing a
            buyer can rely on once the lease has ended unrenewed (a refund looks the same). */}
        {license.paid_through && license.status !== 'expired' ? (
          <>
            <Row label={t('console:license.expires')}>
              {new Date(license.paid_through).toLocaleDateString(
                currentLanguage(),
              )}
            </Row>
            {license.lease_until && (
              <Row label={t('console:license.leaseUntil')}>
                {new Date(license.lease_until).toLocaleDateString(
                  currentLanguage(),
                )}
                <span className="ml-2 text-caption text-muted-foreground">
                  {t('console:license.leaseNote')}
                </span>
              </Row>
            )}
          </>
        ) : (
          license.expires_at && (
            <Row label={t('console:license.expires')}>
              {new Date(license.expires_at).toLocaleDateString(
                currentLanguage(),
              )}
            </Row>
          )
        )}
        {license.issued_at && (
          <Row label={t('console:license.issued')}>
            {new Date(license.issued_at).toLocaleDateString(currentLanguage())}
          </Row>
        )}
        <Row label={t('console:license.sourceLabel')}>
          <span className="font-mono text-caption break-all">
            {t(`console:license.sources.${license.source}`, {
              defaultValue: license.source,
            })}
            {license.source_path ? ` (${license.source_path})` : ''}
          </span>
        </Row>
      </dl>
    </div>
  )
}

/**
 * LicenseTab is the FASE X console panel over the LIVE edition/license: it
 * shows the build edition, the installed license's status + attested entitlements +
 * live ACTIVE-USER usage + expiry, and lets a superadmin INSTALL / UPDATE / REMOVE a
 * license that hot-applies WITHOUT a restart (the Grafana/Elastic in-place model).
 * Like the SSO/secrets panels it is deployment-wide, superadmin-only, and every write
 * is step-up-protected (AAL3) and self-audited.
 *
 * HONESTY (LICENSING.md): the license is attestation-only. The community build verifies
 * and displays it (ready for an in-place swap to the enterprise binary) but gates NO
 * feature on it. Since B10 (2026-07-27) that includes USERS: self-hosted accounts are
 * unlimited in every tier, so this panel reports usage — an active-account count with
 * no denominator, no quota bar and no licensed-seat row. Rendering a limit the engine
 * does not enforce would be the dishonest half of an honest panel.
 */
export function LicenseTab() {
  const { t } = useTranslation(['console', 'common'])
  const { isSuperadmin } = useAuth()
  const extensionCards = useOfferedPanels(PANEL_EXTENSIONS.licenseCards)
  const [installOpen, setInstallOpen] = useState(false)
  const [removeOpen, setRemoveOpen] = useState(false)
  const installBtnRef = useRef<HTMLButtonElement>(null)
  const removeBtnRef = useRef<HTMLButtonElement>(null)

  const query = useQuery({
    queryKey: consoleKeys.license(),
    queryFn: () => consoleApi.getLicense(),
    enabled: isSuperadmin,
  })

  if (!isSuperadmin) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-muted-foreground">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-warning"
          aria-hidden
        />
        {t('console:license.superadminOnly')}
      </div>
    )
  }

  const lic = query.data
  const licenseRead = classifyLicenseRead({
    isPending: query.isPending,
    isError: query.isError,
    error: query.error,
    data: query.data,
  })
  const retryLicense = () => {
    void query.refetch()
  }
  const knownLicense =
    licenseRead.kind === 'success'
      ? licenseRead.license
      : licenseRead.kind === 'failed'
        ? licenseRead.lastSuccess
        : undefined

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div
        data-slot="license-intro-header"
        className="flex flex-wrap items-start justify-between gap-3"
      >
        <div className="min-w-[min(100%,20rem)] flex-1 basis-[20rem]">
          <h2 className="flex flex-wrap items-center gap-2 text-heading text-foreground">
            {t('console:license.title')}
            <EditionBadge />
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:license.caption')}
          </p>
        </div>
        <div
          data-slot="license-actions"
          className="flex min-w-0 flex-wrap items-center gap-2"
        >
          <Button
            ref={installBtnRef}
            onClick={() => setInstallOpen(true)}
            disabled={lic?.managed_externally}
          >
            <Upload />
            {t('console:license.install')}
          </Button>
          {lic && lic.status !== 'none' && !lic.managed_externally && (
            <Button
              ref={removeBtnRef}
              variant="ghost"
              onClick={() => setRemoveOpen(true)}
            >
              <Trash2 />
              {t('console:license.remove')}
            </Button>
          )}
        </div>
      </div>

      {licenseRead.kind === 'loading' ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : licenseRead.kind === 'failed' ? (
        <div
          className="flex flex-col gap-4"
          data-slot="license-facts-panel"
          data-kind="failed"
          data-had-prior={licenseRead.hadPriorData ? 'true' : 'false'}
        >
          <LicenseReadStatus read={licenseRead} onRetry={retryLicense} />
          {licenseRead.lastSuccess ? (
            <LicenseFacts license={licenseRead.lastSuccess} historic />
          ) : null}
        </div>
      ) : (
        <div
          className="flex flex-col gap-4"
          data-slot="license-facts-panel"
          data-kind="current"
        >
          {/* Renewal banner — the honest degradation prompt when expired. */}
          {licenseRead.license.status === 'expired' && (
            <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-warning">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t('console:license.expiredBanner')}
            </p>
          )}
          {/* Managed-externally note: the data-dir install is shadowed by an override. */}
          {licenseRead.license.managed_externally && (
            <p className="flex items-start gap-2 rounded-lg border border-accent-line bg-accent-soft px-4 py-3 text-body text-accent-soft-foreground">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t('console:license.managedExternally', {
                source: licenseRead.license.source,
              })}
            </p>
          )}
          <LicenseFacts license={licenseRead.license} />
          {licenseRead.license.edition === 'community' &&
          (licenseRead.license.status === 'valid' ||
            licenseRead.license.status === 'perpetual') ? (
            <LicenseBuildGuidance license={licenseRead.license} />
          ) : licenseRead.license.edition !== 'enterprise' ? (
            <p className="text-body text-muted-foreground">
              {t('console:license.communityNote')}
            </p>
          ) : null}
        </div>
      )}

      {licenseRead.kind !== 'loading' ? (
        <details
          className="text-body text-muted-foreground"
          data-slot="license-apply-help"
        >
          <summary className="cursor-pointer rounded-sm text-body text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
            {t('console:license.helpSummary')}
          </summary>
          <p className="mt-2">{t('console:license.helpBody')}</p>
        </details>
      ) : null}

      {/*enterprise activation — per-add-on state + enable a preset. Shown
          only for the enterprise build (the community build 501s the endpoint). */}
      {knownLicense?.edition === 'enterprise' && <ActivationSection />}

      {/* Every dialog on this panel keeps its DialogTitle OUTSIDE RequireAssurance.
          The gate REPLACES its children with the step-up panel, so a title inside
          it disappears precisely when the session is below AAL3 and the modal is
          left with no accessible name — an invariant dialog.test.tsx pins. The
 contrast found it in the promote dialog; these three had it too. */}
      <Dialog open={installOpen} onOpenChange={setInstallOpen}>
        <DialogContent
          className="max-h-[85vh] max-w-lg overflow-y-auto"
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            installBtnRef.current?.focus()
          }}
        >
          <DialogHeader>
            <DialogTitle>{t('console:license.installTitle')}</DialogTitle>
            <DialogDescription>
              {t('console:license.installHint')}
            </DialogDescription>
          </DialogHeader>
          {installOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <InstallForm onClose={() => setInstallOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={removeOpen} onOpenChange={setRemoveOpen}>
        <DialogContent
          className="max-w-lg"
          onCloseAutoFocus={(event) => {
            event.preventDefault()
            removeBtnRef.current?.focus()
          }}
        >
          <DialogHeader>
            <DialogTitle>{t('console:license.removeTitle')}</DialogTitle>
            <DialogDescription>
              {lic?.licensee
                ? t('console:license.removeBody', { licensee: lic.licensee })
                : t('console:license.removeBodyGeneric')}
            </DialogDescription>
          </DialogHeader>
          {removeOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <RemoveForm onClose={() => setRemoveOpen(false)} />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>
      {extensionCards.map((p) => (
        <p.Component key={p.id} />
      ))}
    </div>
  )
}

// InstallForm pastes/uploads a license blob and installs it, hot-applying without a
// restart. The acknowledge round-trip below is retained but no longer reachable for
// SEAT reasons: since B10 no license entitles fewer user accounts, so the engine
// never answers license_downgrade_requires_acknowledge. The handler stays wired (it
// is the contract for any future non-seat downgrade signal) and simply renders
// whatever reason the engine gives.
type InstalarVars = { license: string; acknowledge: boolean }

function InstallForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['console', 'common'])
  const queryClient = useQueryClient()
  const [blob, setBlob] = useState('')
  const [needsAck, setNeedsAck] = useState(false)
  const [ackMessage, setAckMessage] = useState('')

  // El reintento llama a una mutación que aún se construye: va por un ref, igual que el hook
  // (use-privileged-mutation.ts:135-138).
  const report = useFailedActionReporter('console')
  // No ejecutes la escritura de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  // ⛔ LAS VARIABLES LLEVAN EL BLOB (H8 del contraste). `mutationFn` releía `blob` del ESTADO, así
  // que el reintento tras la ceremonia instalaba lo que hubiera en pantalla en ese momento — y
  // `blob` se actualiza de forma ASÍNCRONA desde `file.text()`. Un operador que cambia el fichero
  // mientras resuelve el step-up acababa instalando una licencia distinta de la que el motor
  // rechazó, sin que nada se lo dijera. Reintentar tiene que repetir la petición RECHAZADA.
  const instalarRef = useRef<((v: InstalarVars) => void) | null>(null)
  const mutation = useMutation<LicenseStatusDTO, unknown, InstalarVars>({
    mutationFn: ({ license, acknowledge }: InstalarVars) =>
      consoleApi.installLicense({ license, acknowledge }),
    onSuccess: async (data) => {
      await queryClient.invalidateQueries({ queryKey: consoleKeys.license() })
      // server-info drives the read-only Settings>About status too. ⛔ LA CLAVE ES LA DE
      // `queryKeys`, no un literal parecido: `useServerInfo` registra `['server-info']` y este
      // fichero invalidaba `['serverInfo']` — no casan ni en el primer segmento, así que las dos
      // invalidaciones no tocaban nada y «About» seguía enseñando la edición anterior.
      await queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
      // The modules' coverage follows the license: read it now, not at the next apply.
      await queryClient.invalidateQueries({
        queryKey: consoleKeys.activation(),
      })
      toast.success(t('console:license.installed', { status: data.status }))
      onClose()
    },
    onError: (err, vars) => {
      if (
        err instanceof ApiError &&
        err.code === 'license_downgrade_requires_acknowledge'
      ) {
        // Pivot to the explicit downgrade-acknowledge step instead of a red toast.
        setNeedsAck(true)
        setAckMessage(err.message)
        return
      }
      // ⛔ ASEGURAMIENTO ANTES QUE ROJO. Escritura gateada por AAL3 (server.go:732 → handleInstallLicense),
      // y este `toast.error` pintaba el `step_up_required` como un fallo cuando es una
      // ceremonia pendiente CON salida. El `RequireAssurance` de esta pantalla no lo cubre:
      // decide sobre el `principal.aal` CACHEADO (identity/assurance.tsx:49-78), `whoami` no
      // tiene `refetchInterval` (lib/auth/context.tsx:68-78) y el motor degrada AAL3 a AAL1 a
      // los 15 min (core/auth/assurance.go:31-54). **Pre-gateado no es cubierto**.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => instalarRef.current?.(vars)),
        )
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('console:license.installFailed'),
        description ? { description } : undefined,
      )
    },
  })
  // ⛔ DENTRO DE UN EFFECT, no en render. Lo escribí en render diciendo «igual que el
  // hook», y el hook lo hace en un effect (use-privileged-mutation.ts:215-220): eslint
  // `react-hooks/refs` lo rechaza —«Cannot update ref during render»— y mi rama estaba
  // ROJA. Lo cazó el contraste; el comentario afirmaba una paridad que no comprobé.
  useEffect(() => {
    instalarRef.current = mutation.mutate
  }, [mutation.mutate])

  return (
    <>
      <div className="flex flex-col gap-4">
        <Field
          label={t('console:license.blobLabel')}
          htmlFor="license-blob"
          description={t('console:license.blobHint')}
          required
        >
          <Textarea
            id="license-blob"
            value={blob}
            onChange={(e) => {
              setBlob(e.target.value)
              setNeedsAck(false)
            }}
            rows={5}
            className="font-mono text-caption"
            placeholder="eyJ...license-blob...="
          />
        </Field>
        <label className="inline-flex cursor-pointer items-center gap-2 text-body text-accent-text">
          <Upload className="size-4" aria-hidden />
          {t('console:license.uploadFile')}
          <input
            type="file"
            className="sr-only"
            onChange={(e) => {
              const file = e.target.files?.[0]
              if (!file) return
              void file.text().then((txt) => {
                setBlob(txt.trim())
                setNeedsAck(false)
              })
            }}
          />
        </label>

        {needsAck && (
          <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-warning">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
            {ackMessage || t('console:license.downgradeWarn')}
          </p>
        )}
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant={needsAck ? 'destructive' : 'primary'}
          onClick={() =>
            mutation.mutate({ license: blob.trim(), acknowledge: needsAck })
          }
          disabled={blob.trim() === '' || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {needsAck
            ? t('console:license.confirmDowngrade')
            : t('console:license.install')}
        </Button>
      </DialogFooter>
    </>
  )
}

// RemoveForm uninstalls the license, reverting to the community edition. It costs the
// deployment no user account (B10), and carries the same retained acknowledge step.
// Its title/body live in the dialog shell (outside the assurance gate) so the modal
// keeps an accessible name while the step-up panel is showing.
function RemoveForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['console', 'common'])
  const queryClient = useQueryClient()
  const [needsAck, setNeedsAck] = useState(false)
  const [ackMessage, setAckMessage] = useState('')

  // El reintento llama a una mutación que aún se construye: va por un ref, igual que el hook
  // (use-privileged-mutation.ts:135-138).
  const report = useFailedActionReporter('console')
  // No ejecutes la escritura de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const quitarRef = useRef<((v: boolean) => void) | null>(null)
  const mutation = useMutation<LicenseStatusDTO, unknown, boolean>({
    mutationFn: (acknowledge: boolean) =>
      consoleApi.uninstallLicense(acknowledge),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: consoleKeys.license() })
      await queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
      // The modules' coverage follows the license: read it now, not at the next apply.
      await queryClient.invalidateQueries({
        queryKey: consoleKeys.activation(),
      })
      toast.success(t('console:license.removed'))
      onClose()
    },
    onError: (err, vars) => {
      if (
        err instanceof ApiError &&
        err.code === 'license_downgrade_requires_acknowledge'
      ) {
        setNeedsAck(true)
        setAckMessage(err.message)
        return
      }
      // ⛔ ASEGURAMIENTO ANTES QUE ROJO. Escritura gateada por AAL3 (server.go:733 → handleUninstallLicense),
      // y este `toast.error` pintaba el `step_up_required` como un fallo cuando es una
      // ceremonia pendiente CON salida. El `RequireAssurance` de esta pantalla no lo cubre:
      // decide sobre el `principal.aal` CACHEADO (identity/assurance.tsx:49-78), `whoami` no
      // tiene `refetchInterval` (lib/auth/context.tsx:68-78) y el motor degrada AAL3 a AAL1 a
      // los 15 min (core/auth/assurance.go:31-54). **Pre-gateado no es cubierto**.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => quitarRef.current?.(vars)),
        )
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('console:license.removeFailed'),
        description ? { description } : undefined,
      )
    },
  })
  // ⛔ DENTRO DE UN EFFECT, no en render. Lo escribí en render diciendo «igual que el
  // hook», y el hook lo hace en un effect (use-privileged-mutation.ts:215-220): eslint
  // `react-hooks/refs` lo rechaza —«Cannot update ref during render»— y mi rama estaba
  // ROJA. Lo cazó el contraste; el comentario afirmaba una paridad que no comprobé.
  useEffect(() => {
    quitarRef.current = mutation.mutate
  }, [mutation.mutate])

  return (
    <>
      {needsAck && (
        <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-body text-warning">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
          {ackMessage || t('console:license.downgradeWarn')}
        </p>
      )}

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="destructive"
          onClick={() => mutation.mutate(needsAck)}
          disabled={mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {needsAck
            ? t('console:license.confirmDowngrade')
            : t('console:license.remove')}
        </Button>
      </DialogFooter>
    </>
  )
}

// --- enterprise activation ----------------------------------------------

/**
 * ActivationSection is the console surface over the enterprise activation
 * manifest: it lists every add-on's state and lets a superadmin enable a preset
 * (with a diff PREVIEW + AAL3 step-up), or promote a staged add-on once its config
 * is filled. Activation applies at the next engine RESTART (add-ons are wired at
 * boot), which the section states plainly — it never implies a live flip. The
 * endpoint 501s in the community build; the section then shows an honest note.
 */
function ActivationSection() {
  const { t } = useTranslation(['console', 'common'])
  const queryClient = useQueryClient()
  const [enablePreset, setEnablePreset] = useState<string | null>(null)
  const [familyChange, setFamilyChange] = useState<{
    key: string
    action: 'enable' | 'disable'
  } | null>(null)
  // An apply that answered `restarting: true`: the engine goes down and comes back
  // with the new modules wired. Wait for it, then reload the catalog by itself.
  const [restarting, setRestarting] = useState(false)
  useEffect(() => {
    if (!restarting) return
    const ctl = new AbortController()
    void waitForEngineRestart({ signal: ctl.signal }).then(() => {
      if (ctl.signal.aborted) return
      setRestarting(false)
      void queryClient.invalidateQueries({ queryKey: consoleKeys.activation() })
      // The first read of which modules run may have reached the stopping engine.
      void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    })
    return () => ctl.abort()
  }, [restarting, queryClient])
  const onApplied = (status: ActivationStatusDTO) => {
    // The modules that run changed: the navigation and the route gate follow server-info.
    void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    // A plan computed before the change is no longer the plan of what is left to do.
    queryClient.removeQueries({ queryKey: ['console', 'activation-preview'] })
    if (status.restarting) {
      // Show what the engine answered; a read now could reach the stopping
      // process and paint the section as unavailable.
      queryClient.setQueryData(consoleKeys.activation(), {
        ...status,
        restarting: false,
      })
      setRestarting(true)
      return
    }
    void queryClient.invalidateQueries({ queryKey: consoleKeys.activation() })
  }

  const query = useQuery<ActivationStatusDTO>({
    queryKey: consoleKeys.activation(),
    queryFn: () => consoleApi.getActivation(),
    retry: false, // a community/older binary 501s this — surface a note, not an error
  })

  if (query.isLoading) {
    return (
      <div className="flex justify-center py-6">
        <Spinner />
      </div>
    )
  }
  if (query.isError || !query.data) {
    return (
      <p className="rounded-lg border border-border bg-muted/30 px-4 py-3 text-body text-muted-foreground">
        {t('console:activation.unavailable')}
      </p>
    )
  }
  const data = query.data
  // The family a dialog is about is read from the status as it is now.
  const changing = familyChange
    ? data.families?.find((f) => f.key === familyChange.key)
    : undefined
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-5">
      <div>
        <h3 className="text-body font-semibold text-foreground">
          {t('console:activation.title')}
        </h3>
        <p className="max-w-2xl text-body text-muted-foreground">
          {t('console:activation.caption')}
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-body text-muted-foreground">
          {t('console:activation.enablePreset')}:
        </span>
        {data.presets.map((p) => (
          <Button
            key={p.name}
            variant="secondary"
            size="sm"
            onClick={() => setEnablePreset(p.name)}
          >
            <Power className="size-3.5" />
            {p.name}
          </Button>
        ))}
      </div>

      {data.families && data.families.length > 0 && (
        <section
          aria-labelledby="activation-families"
          className="flex flex-col gap-2"
        >
          <div>
            <h4
              id="activation-families"
              className="text-body font-semibold text-foreground"
            >
              {t('console:activation.families')}
            </h4>
            <p className="text-caption text-muted-foreground">
              {t('console:activation.familiesHint')}
            </p>
          </div>
          <ul className="flex flex-col divide-y divide-border rounded-lg border border-border">
            {data.families.map((family) => (
              <FamilyRow
                key={family.key}
                family={family}
                addons={data.addons}
                onChange={(action) =>
                  setFamilyChange({ key: family.key, action })
                }
              />
            ))}
          </ul>
        </section>
      )}

      <div className="overflow-x-auto">
        <StaticTable>
          <thead>
            <tr>
              <th>{t('console:activation.addon')}</th>
              <th>{t('console:activation.state')}</th>
              <th>{t('console:activation.tier')}</th>
              <th aria-hidden />
            </tr>
          </thead>
          <tbody>
            {data.addons.map((a) => (
              <AddonRow
                key={a.key}
                addon={a}
                restartRequired={data.restart_required}
                onApplied={onApplied}
              />
            ))}
          </tbody>
        </StaticTable>
      </div>

      {restarting ? (
        <p
          role="status"
          className="flex items-center gap-2 text-caption text-muted-foreground"
          data-slot="engine-restarting"
        >
          <Spinner size="sm" aria-hidden />
          {t('console:activation.restarting')}
        </p>
      ) : (
        <p className="flex items-start gap-2 text-caption text-muted-foreground">
          <RotateCw className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          {data.restart_required
            ? t('console:activation.restartNote')
            : t('console:activation.liveNote')}
        </p>
      )}

      {enablePreset && (
        <Dialog open onOpenChange={(o) => !o && setEnablePreset(null)}>
          <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
            {/* Title outside the gate — see the note on the license dialogs. */}
            <DialogHeader>
              <DialogTitle>
                {t('console:activation.enableTitle', { preset: enablePreset })}
              </DialogTitle>
              <DialogDescription>
                {t('console:activation.enableHint')}
              </DialogDescription>
            </DialogHeader>
            <RequireAssurance minAal={AAL.HARDWARE} action="modules">
              <EnablePresetForm
                selection={{ preset: enablePreset }}
                enabledText={t('console:activation.enabled', {
                  preset: enablePreset,
                })}
                failedText={t('console:activation.enableFailed')}
                onClose={() => setEnablePreset(null)}
                onApplied={onApplied}
              />
            </RequireAssurance>
          </DialogContent>
        </Dialog>
      )}

      {familyChange && changing && (
        <Dialog open onOpenChange={(o) => !o && setFamilyChange(null)}>
          <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
            {/* Title outside the gate — see the note on the license dialogs. */}
            <DialogHeader>
              <DialogTitle>
                {t(
                  familyChange.action === 'enable'
                    ? 'console:activation.familyEnableTitle'
                    : 'console:activation.familyDisableTitle',
                  { family: changing.title },
                )}
              </DialogTitle>
              <DialogDescription>
                {familyChange.action === 'enable'
                  ? t('console:activation.enableHint')
                  : t('console:activation.familyDisableHint')}
              </DialogDescription>
            </DialogHeader>
            <RequireAssurance minAal={AAL.HARDWARE} action="modules">
              {familyChange.action === 'enable' ? (
                <EnablePresetForm
                  selection={{ family: changing.key }}
                  enabledText={t('console:activation.familyEnabled', {
                    family: changing.title,
                  })}
                  failedText={t('console:activation.familyChangeFailed', {
                    family: changing.title,
                  })}
                  onClose={() => setFamilyChange(null)}
                  onApplied={onApplied}
                />
              ) : (
                <DisableFamilyForm
                  family={changing}
                  addons={data.addons}
                  onClose={() => setFamilyChange(null)}
                  onApplied={onApplied}
                />
              )}
            </RequireAssurance>
          </DialogContent>
        </Dialog>
      )}
    </div>
  )
}

// FamilyRow is one family: how many of its add-ons are on, and the actions that
// turn it on or off as a whole. A staged add-on that waits for input is not on.
function FamilyRow({
  family,
  addons,
  onChange,
}: {
  family: ActivationFamilyDTO
  addons: ActivationAddonDTO[]
  onChange: (action: 'enable' | 'disable') => void
}) {
  const { t } = useTranslation(['console'])
  const titleId = useId()
  const total = family.addons.length
  const on = family.active.length
  // Staged add-ons wait for a secret or a review the administrator provides.
  const waiting = addons.filter(
    (a) => family.addons.includes(a.key) && a.state === 'pending',
  ).length
  return (
    <li className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
      <div className="min-w-0">
        <div id={titleId} className="font-medium text-foreground">
          {family.title}
        </div>
        <div className="text-caption text-muted-foreground">
          {t('console:activation.familyCount', { on, total })}
          {waiting > 0 &&
            ` · ${t('console:activation.familyWaiting', { count: waiting })}`}
        </div>
      </div>
      <div className="flex gap-2">
        {on < total && (
          <Button
            variant="secondary"
            size="sm"
            aria-describedby={titleId}
            onClick={() => onChange('enable')}
          >
            <Power className="size-3.5" aria-hidden />
            {t('console:activation.turnOn')}
          </Button>
        )}
        {on + waiting > 0 && (
          <Button
            variant="ghost"
            size="sm"
            aria-describedby={titleId}
            onClick={() => onChange('disable')}
          >
            {t('console:activation.turnOff')}
          </Button>
        )}
      </div>
    </li>
  )
}

// DisableFamilyForm names what turns off and applies it (AAL3-gated by its
// caller). There is no preview: turning off removes configuration, it never stages.
function DisableFamilyForm({
  family,
  addons,
  onClose,
  onApplied,
}: {
  family: ActivationFamilyDTO
  addons: ActivationAddonDTO[]
  onClose: () => void
  onApplied: (status: ActivationStatusDTO) => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const report = useFailedActionReporter('console')
  const guardarReanudacion = useResumeGuard()
  const retryRef = useRef<(() => void) | null>(null)
  const apply = useMutation({
    mutationFn: () =>
      consoleApi.applyActivation({ action: 'disable', family: family.key }),
    onSuccess: (status) => {
      onApplied(status)
      toast.success(
        t('console:activation.familyDisabled', { family: family.title }),
      )
      onClose()
    },
    onError: (err) => {
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => retryRef.current?.()),
        )
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('console:activation.familyChangeFailed', { family: family.title }),
        description ? { description } : undefined,
      )
    },
  })
  useEffect(() => {
    retryRef.current = apply.mutate
  }, [apply.mutate])
  // What turning the family off stops: its add-ons that are on or waiting for input.
  const affected = addons.filter(
    (a) =>
      family.addons.includes(a.key) &&
      (a.state === 'active' || a.state === 'pending'),
  )
  return (
    <>
      <ul className="flex flex-col gap-1 rounded-lg border border-border p-3 text-caption">
        {affected.map((a) => (
          <li key={a.key} className="text-foreground">
            {a.title}
          </li>
        ))}
      </ul>
      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={apply.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          disabled={apply.isPending || affected.length === 0}
          onClick={() => apply.mutate()}
        >
          {apply.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:activation.apply')}
        </Button>
      </DialogFooter>
    </>
  )
}

// AddonRow renders one add-on's state, with a Promote action for a staged add-on.
//
// the promote action opens the same Dialog > RequireAssurance > form shape
// the preset enable and the license install/remove use, and NOT because the
// client is the security boundary — it never is here. The engine gates the whole
// endpoint: handleActivationApply calls requireAAL3 BEFORE it dispatches on the
// action (core/api/handlers_activation.go:75-82), so `promote` was already
// refused below AAL3, exactly like `enable`.
//
// What was missing is the OTHER half: nothing ROUTES that refusal. The 403 code
// the engine returns (core/api/errors.go:224) has no runtime consumer in the
// console — the client builds an ApiError and rethrows any 403 to the caller
// (lib/api/client.ts), and this mutation just shows a red toast. So an operator
// below AAL3 was told "no" on this screen with no way forward FROM this screen,
// while the button fifty lines up offered them the ceremony inline.
//
// ⚠ Corrected after the contrast refuted the stronger claim this comment
// used to make. RequireAssurance is NOT the console's only access to the
// ceremony: StepUpPanel is also rendered directly by the privileged-login tab
// (features/identity/privileged-login.tsx) and by the inference-proxy view. An
// operator COULD have navigated to /identity, elevated there, and come back.
// What this fixes is a dead end in context, not a total absence — worth having,
// and worth stating at its real size. Deleting this gate opens no hole.
function AddonRow({
  addon,
  restartRequired,
  onApplied,
}: {
  addon: ActivationAddonDTO
  restartRequired: boolean
  onApplied: (status: ActivationStatusDTO) => void
}) {
  const { t } = useTranslation(['console'])
  const [confirmOpen, setConfirmOpen] = useState(false)
  const label = activationLabel(addon, restartRequired)
  const detail = activationDetail(label)
  return (
    <tr className="border-border/60">
      <td>
        <div className="font-medium text-foreground">{addon.title}</div>
        <div className="text-caption text-muted-foreground">
          {addon.summary}
        </div>
      </td>
      <td className="align-top">
        <Badge variant={activationTone(label)}>
          {activationText(label, t)}
        </Badge>
        {detail && (
          <div className="mt-1 max-w-[42ch] text-caption text-muted-foreground [overflow-wrap:anywhere]">
            {detail}
          </div>
        )}
      </td>
      <td className="align-top text-caption text-muted-foreground">
        {addon.preset}
      </td>
      <td className="text-right align-top">
        {addon.state === 'pending' && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setConfirmOpen(true)}
            title={addon.reason}
          >
            {t('console:activation.promote')}
          </Button>
        )}
        {confirmOpen && (
          <Dialog open onOpenChange={(o) => !o && setConfirmOpen(false)}>
            <DialogContent className="max-w-lg">
              {/* The title lives OUTSIDE the gate on purpose: RequireAssurance
                  REPLACES its children with the step-up panel, so a title inside
                  it vanishes exactly when the operator is below AAL3 and the
                  modal loses its accessible name (dialog.test.tsx treats "name
                  from the title" as an invariant). Caught by the contrast. */}
              <DialogHeader>
                <DialogTitle>
                  {t('console:activation.promoteTitle', { addon: addon.title })}
                </DialogTitle>
                <DialogDescription>
                  {t('console:activation.promoteHint')}
                </DialogDescription>
              </DialogHeader>
              <RequireAssurance minAal={AAL.HARDWARE} action="modules">
                <PromoteAddonForm
                  addon={addon}
                  onClose={() => setConfirmOpen(false)}
                  onApplied={onApplied}
                />
              </RequireAssurance>
            </DialogContent>
          </Dialog>
        )}
      </td>
    </tr>
  )
}

// PromoteAddonForm confirms and applies the promotion of one staged add-on
// (AAL3-gated by its caller, like EnablePresetForm).
function PromoteAddonForm({
  addon,
  onClose,
  onApplied,
}: {
  addon: ActivationAddonDTO
  onClose: () => void
  onApplied: (status: ActivationStatusDTO) => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const report = useFailedActionReporter('console')
  // No ejecutes la escritura de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const promoverRef = useRef<(() => void) | null>(null)
  const promote = useMutation({
    mutationFn: () =>
      consoleApi.applyActivation({ action: 'promote', addon: addon.key }),
    onSuccess: (status) => {
      onApplied(status)
      toast.success(t('console:activation.promoted', { addon: addon.key }))
      onClose()
    },
    onError: (err) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROJO. Escritura gateada por AAL3 (server.go:742 → handleActivationApply),
      // y este `toast.error` pintaba el `step_up_required` como un fallo cuando es una
      // ceremonia pendiente CON salida. El `RequireAssurance` de esta pantalla no lo cubre:
      // decide sobre el `principal.aal` CACHEADO (identity/assurance.tsx:49-78), `whoami` no
      // tiene `refetchInterval` (lib/auth/context.tsx:68-78) y el motor degrada AAL3 a AAL1 a
      // los 15 min (core/auth/assurance.go:31-54). **Pre-gateado no es cubierto**.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => promoverRef.current?.()),
        )
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('console:activation.promoteFailed'),
        description ? { description } : undefined,
      )
    },
  })
  // ⛔ DENTRO DE UN EFFECT, no en render. Lo escribí en render diciendo «igual que el
  // hook», y el hook lo hace en un effect (use-privileged-mutation.ts:215-220): eslint
  // `react-hooks/refs` lo rechaza —«Cannot update ref during render»— y mi rama estaba
  // ROJA. Lo cazó el contraste; el comentario afirmaba una paridad que no comprobé.
  useEffect(() => {
    promoverRef.current = promote.mutate
  }, [promote.mutate])
  return (
    <>
      {addon.reason && (
        <p className="rounded-lg border border-border bg-muted/30 px-4 py-3 text-body text-muted-foreground">
          {addon.reason}
        </p>
      )}

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={promote.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          disabled={promote.isPending}
          onClick={() => promote.mutate()}
        >
          {promote.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:activation.promote')}
        </Button>
      </DialogFooter>
    </>
  )
}

// EnablePresetForm previews the enable diff of a preset or a family, then applies
// it (AAL3-gated).
function EnablePresetForm({
  selection,
  enabledText,
  failedText,
  onClose,
  onApplied,
}: {
  selection: ActivationSelection
  enabledText: string
  failedText: string
  onClose: () => void
  onApplied: (status: ActivationStatusDTO) => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const preview = useQuery({
    queryKey: ['console', 'activation-preview', selection],
    queryFn: () => consoleApi.previewActivation(selection),
  })
  const report = useFailedActionReporter('console')
  // No ejecutes la escritura de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const activarRef = useRef<(() => void) | null>(null)
  const apply = useMutation({
    mutationFn: () =>
      consoleApi.applyActivation({ action: 'enable', ...selection }),
    onSuccess: (status) => {
      onApplied(status)
      toast.success(enabledText)
      onClose()
    },
    onError: (err) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROJO. Escritura gateada por AAL3 (server.go:742 → handleActivationApply),
      // y este `toast.error` pintaba el `step_up_required` como un fallo cuando es una
      // ceremonia pendiente CON salida. El `RequireAssurance` de esta pantalla no lo cubre:
      // decide sobre el `principal.aal` CACHEADO (identity/assurance.tsx:49-78), `whoami` no
      // tiene `refetchInterval` (lib/auth/context.tsx:68-78) y el motor degrada AAL3 a AAL1 a
      // los 15 min (core/auth/assurance.go:31-54). **Pre-gateado no es cubierto**.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(
          err,
          guardarReanudacion(() => activarRef.current?.()),
        )
        return
      }
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(failedText, description ? { description } : undefined)
    },
  })
  // ⛔ DENTRO DE UN EFFECT, no en render. Lo escribí en render diciendo «igual que el
  // hook», y el hook lo hace en un effect (use-privileged-mutation.ts:215-220): eslint
  // `react-hooks/refs` lo rechaza —«Cannot update ref during render»— y mi rama estaba
  // ROJA. Lo cazó el contraste; el comentario afirmaba una paridad que no comprobé.
  useEffect(() => {
    activarRef.current = apply.mutate
  }, [apply.mutate])
  return (
    <>
      {preview.isLoading ? (
        <div className="flex justify-center py-6">
          <Spinner />
        </div>
      ) : preview.isError || !preview.data ? (
        <QueryErrorState
          error={preview.error}
          retry={() => void preview.refetch()}
        />
      ) : (
        <div className="flex flex-col gap-1 rounded-lg border border-border p-3 font-mono text-caption">
          {preview.data.entries.map((e) => (
            <div key={e.addon} className="flex items-start gap-2">
              <span
                className={
                  e.action === 'activate'
                    ? 'text-success'
                    : e.action === 'stage'
                      ? 'text-warning'
                      : 'text-muted-foreground'
                }
              >
                {e.action === 'activate'
                  ? '+'
                  : e.action === 'stage'
                    ? '~'
                    : '='}
              </span>
              <span className="text-foreground">{e.addon}</span>
              <span className="min-w-0 text-muted-foreground">
                {e.action}
                {e.reason ? ` — ${e.reason}` : ''}
              </span>
            </div>
          ))}
        </div>
      )}
      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={apply.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          disabled={
            apply.isPending || preview.isLoading || !preview.data?.changes
          }
          onClick={() => apply.mutate()}
        >
          {apply.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:activation.apply')}
        </Button>
      </DialogFooter>
    </>
  )
}
