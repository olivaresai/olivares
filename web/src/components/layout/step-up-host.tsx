// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The ONE step-up ceremony host, mounted once beside the Toaster. It answers the
// ERROR half of the assurance story: `RequireAssurance` already replaces a gated
// subtree with the step-up panel at RENDER time, but a session's assurance
// expires on a timer (core/auth/assurance.go:35 — StepUpTTL 15 min) while the
// render gate reads the AAL cached in the principal. Between painting a button
// and pressing it the elevation can lapse, and the engine answers 403
// `step_up_required` — the same demand, arriving through a different door. Before
// this host that door led to a warning toast reading "your role can't perform
// this action", which is false: the role is fine, the SESSION is not elevated.
//
// The panel is loaded lazily: the ceremony pulls in the WebAuthn plumbing and the
// identity i18n bundle, and neither belongs in the first paint of a console that
// may never demand a step-up. The dialog SHELL translates from `common`, which is
// a foundation namespace bundled at init, so the title never renders as a raw key
// while the chunk is still in flight.
import { lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { useStepUpStore } from '@/stores/step-up'

// AAL3 is not a guess: `step_up_required` is raised by requireAAL3
// (core/api/middleware.go:298) and by core/auth's credential lifecycle, and
// core/auth/assurance.go:25-28 defines AAL3 as the only level a verified
// ceremony grants. The error envelope carries no `required_aal`, so the console
// states the level the engine actually enforces rather than inventing a field.
const AAL3 = 3

const StepUpPanel = lazy(() =>
  import('@/features/identity/assurance').then((m) => ({
    default: m.StepUpPanel,
  })),
)

export function StepUpHost() {
  const { t } = useTranslation('common')
  const request = useStepUpStore((s) => s.request)
  const clear = useStepUpStore((s) => s.clear)

  return (
    <Dialog
      open={request !== null}
      onOpenChange={(open) => {
        // Dismissing leaves the action DENIED and says so in the panel copy —
        // closing this dialog never completes anything.
        if (!open) clear()
      }}
    >
      {request !== null && (
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('privileged.stepUp.title')}</DialogTitle>
            <DialogDescription>
              {/* ⛔ LA COPY DEPENDE DE SI HAY REINTENTO, y no es un matiz: la de por defecto dice
                  «complete the step-up below and the action resumes», mientras `onElevated` de
                  abajo sólo ejecuta `retry?.()`. Un llamador que NO entrega reintento es legítimo
                  —el contrato del store lo permite expresamente (stores/step-up.ts:22-29)— pero
                  entonces el panel estaba prometiendo una reanudación que nunca ocurría: ocho
                  llamadas de la consola están hoy en ese caso.
                  Se arregla aquí y no en las ocho porque el que miente es el TEXTO, no ellas. */}
              {request.retry
                ? t('privileged.stepUp.description')
                : t('privileged.stepUp.descriptionNoResume')}
            </DialogDescription>
          </DialogHeader>
          <Suspense fallback={<Skeleton className="h-48 w-full" />}>
            <StepUpPanel
              minAal={AAL3}
              // The panel's copy contrasts "required" against "current". The
              // engine refused for assurance, so whatever the principal cached,
              // the level that COUNTED was below AAL3 — reporting the cached
              // value here would show a session claiming an assurance the engine
              // had just rejected.
              currentAal={1}
              action={request.action}
              onElevated={() => {
                // Order matters: hand the retry the elevated session, then close.
                // The engine has already verified the ceremony and whoami has been
                // re-read by the panel, so the resumed call carries the new AAL.
                const { retry } = request
                clear()
                retry?.()
              }}
            />
          </Suspense>
        </DialogContent>
      )}
    </Dialog>
  )
}
