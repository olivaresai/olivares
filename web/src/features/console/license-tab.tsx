// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
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
import { type ReactNode, useState, useRef, useEffect } from 'react'
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
import { ErrorState } from '@/components/ui/error-state'
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
import { EntitlementMatrixCard } from './entitlement-matrix'
import {
  type ActivationAddonDTO,
  type ActivationStatusDTO,
  consoleApi,
  consoleKeys,
  type LicenseStatusDTO,
} from './api'

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
    <div className="flex items-start justify-between gap-4 py-2.5">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right text-sm text-foreground">{children}</dd>
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
  const [installOpen, setInstallOpen] = useState(false)
  const [removeOpen, setRemoveOpen] = useState(false)

  const query = useQuery({
    queryKey: consoleKeys.license(),
    queryFn: () => consoleApi.getLicense(),
    enabled: isSuperadmin,
  })

  if (!isSuperadmin) {
    return (
      <div className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-muted-foreground">
        <ShieldAlert
          className="mt-0.5 size-4 shrink-0 text-warning"
          aria-hidden
        />
        {t('console:license.superadminOnly')}
      </div>
    )
  }

  const lic = query.data

  return (
    <div className="flex flex-col gap-4 pt-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-foreground">
            {t('console:license.title')}
          </h2>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:license.caption')}
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            onClick={() => setInstallOpen(true)}
            disabled={lic?.managed_externally}
          >
            <Upload />
            {t('console:license.install')}
          </Button>
          {lic && lic.status !== 'none' && !lic.managed_externally && (
            <Button variant="ghost" onClick={() => setRemoveOpen(true)}>
              <Trash2 />
              {t('console:license.remove')}
            </Button>
          )}
        </div>
      </div>

      {query.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : query.isError || !lic ? (
        <ErrorState retry={() => void query.refetch()} />
      ) : (
        <div className="flex flex-col gap-4">
          {/* Renewal banner — the honest degradation prompt when expired. */}
          {lic.status === 'expired' && (
            <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-warning">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t('console:license.expiredBanner')}
            </p>
          )}
          {/* Managed-externally note: the data-dir install is shadowed by an override. */}
          {lic.managed_externally && (
            <p className="flex items-start gap-2 rounded-lg border border-accent-line bg-accent-soft px-4 py-3 text-sm text-accent-soft-foreground">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
              {t('console:license.managedExternally', { source: lic.source })}
            </p>
          )}

          <div className="rounded-lg border border-border p-5">
            <dl className="divide-y divide-border">
              <Row label={t('console:license.edition')}>
                <Badge
                  variant={lic.edition === 'enterprise' ? 'accent' : 'neutral'}
                >
                  {t(`console:license.editions.${lic.edition}`, {
                    defaultValue: lic.edition,
                  })}
                </Badge>
              </Row>
              <Row label={t('console:license.status')}>
                <Badge variant={statusVariant(lic.status)}>
                  {lic.status === 'valid' || lic.status === 'perpetual' ? (
                    <BadgeCheck className="size-3 shrink-0" aria-hidden />
                  ) : null}
                  {t(`console:license.statuses.${lic.status}`, {
                    defaultValue: lic.status,
                  })}
                </Badge>
              </Row>
              {lic.licensee && (
                <Row label={t('console:license.licensee')}>{lic.licensee}</Row>
              )}
              {lic.plan && (
                <Row label={t('console:license.plan')}>{lic.plan}</Row>
              )}
              {lic.support_tier && (
                <Row label={t('console:license.supportTier')}>
                  {lic.support_tier}
                </Row>
              )}
              {/* B10: honest USAGE, never a quota. Self-hosted user accounts are
                  unlimited in every tier, so there is no denominator to divide by
                  and no licensed-seat figure to advertise — showing one would imply
                  a cap that no build enforces. */}
              <Row label={t('console:license.activeUsers')}>
                <span className="font-mono tabular-nums">
                  {lic.active_users}
                  {lic.active_users_capped ? '+' : ''}
                </span>
                <span className="ml-2 text-xs text-muted-foreground">
                  {t('console:license.noUserLimit')}
                </span>
              </Row>
              {lic.expires_at && (
                <Row label={t('console:license.expires')}>
                  {new Date(lic.expires_at).toLocaleDateString(
                    currentLanguage(),
                  )}
                </Row>
              )}
              {lic.issued_at && (
                <Row label={t('console:license.issued')}>
                  {new Date(lic.issued_at).toLocaleDateString(
                    currentLanguage(),
                  )}
                </Row>
              )}
              <Row label={t('console:license.sourceLabel')}>
                <span className="font-mono text-xs">
                  {t(`console:license.sources.${lic.source}`, {
                    defaultValue: lic.source,
                  })}
                  {lic.source_path ? ` (${lic.source_path})` : ''}
                </span>
              </Row>
            </dl>
          </div>

          {/* Attestation-only honesty: a community build never gates on the license. */}
          {lic.edition !== 'enterprise' && (
            <p className="text-sm text-muted-foreground">
              {t('console:license.communityNote')}
            </p>
          )}

          {/*enterprise activation — per-add-on state + enable a preset. Shown
              only for the enterprise build (the community build 501s the endpoint). */}
          {lic.edition === 'enterprise' && <ActivationSection />}
        </div>
      )}

      {/* Every dialog on this panel keeps its DialogTitle OUTSIDE RequireAssurance.
          The gate REPLACES its children with the step-up panel, so a title inside
          it disappears precisely when the session is below AAL3 and the modal is
          left with no accessible name — an invariant dialog.test.tsx pins. The
 contrast found it in the promote dialog; these three had it too. */}
      <Dialog open={installOpen} onOpenChange={setInstallOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
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
        <DialogContent className="max-w-lg">
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
      {/* ⛔ C07-07: la matriz va DEBAJO de la activación a propósito. La tabla de arriba
          contesta «qué está encendido»; ésta contesta las TRES preguntas y dice cuáles no se
          pueden contestar. Ponerla encima invitaría a leerla como el estado, que es sólo una de
          las tres columnas. */}
      <EntitlementMatrixCard />
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
            className="font-mono text-xs"
            placeholder="eyJ...license-blob...="
          />
        </Field>
        <label className="inline-flex cursor-pointer items-center gap-2 text-sm text-accent-text">
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
          <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-warning">
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
        <p className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 text-sm text-warning">
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

// addonStateTone maps an activation state to a badge tone.
function addonStateTone(
  state: string,
): 'success' | 'warning' | 'neutral' | 'accent' {
  switch (state) {
    case 'active':
      return 'success'
    case 'pending':
      return 'warning'
    case 'console':
      return 'accent'
    default:
      return 'neutral' // available
  }
}

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
  const [enablePreset, setEnablePreset] = useState<string | null>(null)

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
      <p className="rounded-lg border border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground">
        {t('console:activation.unavailable')}
      </p>
    )
  }
  const data = query.data
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-5">
      <div>
        <h3 className="text-sm font-semibold text-foreground">
          {t('console:activation.title')}
        </h3>
        <p className="max-w-2xl text-sm text-muted-foreground">
          {t('console:activation.caption')}
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-muted-foreground">
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

      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border text-left text-xs text-muted-foreground">
              <th className="py-1.5 pr-3 font-medium">
                {t('console:activation.addon')}
              </th>
              <th className="py-1.5 pr-3 font-medium">
                {t('console:activation.state')}
              </th>
              <th className="py-1.5 pr-3 font-medium">
                {t('console:activation.tier')}
              </th>
              <th className="py-1.5 font-medium" aria-hidden />
            </tr>
          </thead>
          <tbody>
            {data.addons.map((a) => (
              <AddonRow key={a.key} addon={a} />
            ))}
          </tbody>
        </table>
      </div>

      <p className="flex items-start gap-2 text-xs text-muted-foreground">
        <RotateCw className="mt-0.5 size-3.5 shrink-0" aria-hidden />
        {t('console:activation.restartNote')}
      </p>

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
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <EnablePresetForm
                preset={enablePreset}
                onClose={() => setEnablePreset(null)}
              />
            </RequireAssurance>
          </DialogContent>
        </Dialog>
      )}
    </div>
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
function AddonRow({ addon }: { addon: ActivationAddonDTO }) {
  const { t } = useTranslation(['console'])
  const [confirmOpen, setConfirmOpen] = useState(false)
  return (
    <tr className="border-b border-border/60">
      <td className="py-1.5 pr-3">
        <div className="font-medium text-foreground">{addon.title}</div>
        <div className="text-xs text-muted-foreground">{addon.summary}</div>
      </td>
      <td className="py-1.5 pr-3 align-top">
        <Badge variant={addonStateTone(addon.state)}>{addon.state}</Badge>
      </td>
      <td className="py-1.5 pr-3 align-top text-xs text-muted-foreground">
        {addon.preset}
      </td>
      <td className="py-1.5 text-right align-top">
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
              <RequireAssurance minAal={AAL.HARDWARE} action="console">
                <PromoteAddonForm
                  addon={addon}
                  onClose={() => setConfirmOpen(false)}
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
}: {
  addon: ActivationAddonDTO
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const queryClient = useQueryClient()
  const report = useFailedActionReporter('console')
  // No ejecutes la escritura de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const promoverRef = useRef<(() => void) | null>(null)
  const promote = useMutation({
    mutationFn: () =>
      consoleApi.applyActivation({ action: 'promote', addon: addon.key }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: consoleKeys.activation(),
      })
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
        <p className="rounded-lg border border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground">
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

// EnablePresetForm previews the enable diff then applies it (AAL3-gated).
function EnablePresetForm({
  preset,
  onClose,
}: {
  preset: string
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const queryClient = useQueryClient()
  const preview = useQuery({
    queryKey: ['console', 'activation-preview', preset],
    queryFn: () => consoleApi.previewActivation(preset),
  })
  const report = useFailedActionReporter('console')
  // No ejecutes la escritura de un formulario ya desmontado: ver use-resume-guard.ts.
  const guardarReanudacion = useResumeGuard()
  const activarRef = useRef<(() => void) | null>(null)
  const apply = useMutation({
    mutationFn: () => consoleApi.applyActivation({ action: 'enable', preset }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: consoleKeys.activation(),
      })
      toast.success(t('console:activation.enabled', { preset }))
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
      toast.error(
        t('console:activation.enableFailed'),
        description ? { description } : undefined,
      )
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
        <ErrorState retry={() => void preview.refetch()} />
      ) : (
        <div className="flex flex-col gap-1 rounded-lg border border-border p-3 font-mono text-xs">
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
