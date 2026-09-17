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
import { useStepUpStore, type StepUpRequest } from '@/stores/step-up'

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

/** The same owned panel is hosted inline by Add, or in the global dialog.
 * Every closure carries only an expected instance; live state owns the retry.
 */
export function StepUpRequestPanel({ request }: { request: StepUpRequest }) {
  return (
    <Suspense fallback={<Skeleton className="h-48 w-full" />}>
      <StepUpPanel
        key={request.instance}
        minAal={AAL3}
        currentAal={1}
        action={request.action}
        allowEnrollment={request.enrollment === 'connector-add'}
        isCurrentRequest={() =>
          useStepUpStore.getState().current(request.instance)
        }
        onUnenrolled={() =>
          useStepUpStore.getState().dropRetry(request.instance)
        }
        onElevated={() => useStepUpStore.getState().consume(request.instance)}
      />
    </Suspense>
  )
}

export function StepUpHost() {
  const { t } = useTranslation('common')
  const request = useStepUpStore((s) => s.request)
  // Add owns its existing dialog and its Save demand. A second dialog would hide
  // that intent and create a nested focus trap during first enrollment.
  if (request?.enrollment === 'connector-add') return null
  return (
    <Dialog
      open={request !== null}
      onOpenChange={(open) => {
        if (!open && request) useStepUpStore.getState().clear(request.instance)
      }}
    >
      {request !== null && (
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('privileged.stepUp.title')}</DialogTitle>
            <DialogDescription>
              {request.retry
                ? t('privileged.stepUp.description')
                : t('privileged.stepUp.descriptionNoResume')}
            </DialogDescription>
          </DialogHeader>
          <StepUpRequestPanel request={request} />
        </DialogContent>
      )}
    </Dialog>
  )
}
