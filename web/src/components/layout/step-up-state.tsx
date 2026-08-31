// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// StepUpRequiredState — the READ-side answer to a 403 `step_up_required`, and the
// sibling of the write-side StepUpHost. A gated read (the access/WIF graph, an
// access-review export) refused for assurance used to render ForbiddenState —
// "you do not have permission" — which is false: the operator has the
// permission, the SESSION is below AAL3. Here the section is replaced by the
// ceremony itself, which is what `RequireAssurance` already does at render time
// ("never a generic toast, never the gated content"); this is the same rule
// applied to the answer that arrives from the engine instead of from the cache.
//
// Lazy for the same reason as the host: the ceremony carries the WebAuthn
// plumbing and the identity i18n bundle, and a console that never takes a 403
// should never pay for them.
import { lazy, Suspense } from 'react'
import { Skeleton } from '@/components/ui/skeleton'

// See step-up-host.tsx: `step_up_required` always means AAL3
// (core/auth/assurance.go:25-28) and the envelope carries no `required_aal`.
const AAL3 = 3

const StepUpPanel = lazy(() =>
  import('@/features/identity/assurance').then((m) => ({
    default: m.StepUpPanel,
  })),
)

export function StepUpRequiredState({
  action,
  onElevated,
  className,
}: {
  /** i18n key fragment naming the gated read (identity:assurance.actions.*). */
  action: string
  /** Re-run the refused read once the backend has elevated the session. */
  onElevated?: () => void
  className?: string
}) {
  // ⛔ Y SE ANUNCIA SOLO, como sus dos hermanas. El contrato de los estados es que cada uno
  // se autoanuncia —ForbiddenState y EmptyState con role="status", ErrorState con
  // role="alert" (components/ui/error-state.tsx:41 y :99)—, y quien los monta calla: la
  // región live de DataTable, por ejemplo, se vacía en cuanto hay `error`
  // (components/data/data-table.tsx:429-434). Esta era la ÚNICA que no cumplía el contrato,
  // así que un lector de pantalla pasaba de «cargando» a silencio y no llegaba a saber que
  // hay una ceremonia que hacer. Lo cazó el contraste `sol max` sobre. Va aquí y no en
  // la tabla porque el defecto es del estado, y aquí lo arregla para TODOS sus consumidores.
  return (
    // `aria-atomic="false"`: `role="status"` es atómico por defecto, así que CADA cambio de
    // estado del panel —el botón pasando a «verificando», los tres role="alert" que puede
    // insertar (features/identity/assurance.tsx:169-185)— hacía que la cola polite releyera
    // el panel ENTERO. Con `false` se anuncia lo que cambia. RESIDUO DECLARADO, y lo declaro
    // porque no lo he podido medir: sigue habiendo un `role="alert"` (assertive) anidado
    // dentro de una región polite, y qué pronuncia cada pareja navegador+lector no lo sé sin
    // un lector real. El contraste `sol max` tampoco pudo mirarlo y lo marcó SUSPECT.
    <div role="status" aria-live="polite" aria-atomic="false">
      <Suspense fallback={<Skeleton className="h-48 w-full" />}>
        <StepUpPanel
          minAal={AAL3}
          // The engine refused for assurance, so whatever the principal had cached,
          // the level that counted was below AAL3.
          currentAal={1}
          action={action}
          className={className}
          onElevated={onElevated}
        />
      </Suspense>
    </div>
  )
}
