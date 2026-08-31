// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  useMutation,
  useQueryClient,
  type QueryKey,
} from '@tanstack/react-query'
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useStepUpStore } from '@/stores/step-up'
import { useTenantStore } from '@/stores/tenant'

/**
 * useFailedActionReporter is the ONE policy for reporting a privileged action the
 * engine refused, and there are THREE answers, never two:
 *
 *   step_up_required  the operator MAY do this; the SESSION is not assured enough
 *                     → open the ceremony (core/api/errors.go:224)
 *   forbidden         the ROLE cannot → a calm warning, never a red error
 *   anything else     → the engine's message under a localized failure title
 *
 * It exists as its own hook because `usePrivilegedMutation` is not the only way a
 * view talks to the engine: several surfaces hand-roll `useMutation` because they
 * need their own cleanup (clearing a per-row spinner, closing a dialog), and those
 * hand-rolled handlers each re-implemented the reporting — collapsing the first two
 * answers into "your role can't perform this action". That is false for a step-up:
 * it accuses the operator of missing a permission they hold, and sends them to ask
 * for it. A hand-rolled mutation keeps its own onError for cleanup and delegates
 * the REPORTING here, so the policy has one home.
 */
export function useFailedActionReporter(stepUpAction?: string) {
  const { t } = useTranslation(['common', 'errors'])
  // ⛔ SI EL DUEÑO YA NO ESTÁ, NO SE SECUESTRA LA PANTALLA. `useMutation` construye la `Mutation`
  // con las opciones del hook al llamar a `mutate`, y al desmontar sólo se retira el OBSERVER: la
  // mutación conserva sus opciones y llama a `onError` cuando la promesa rechaza (query-core
  // 5.101.4, mutation.ts:273-296). Navegar fuera durante la petición abría después una ceremonia
  // HUÉRFANA — un modal a pantalla completa exigiendo atención por una acción que el operador ya
  // abandonó.
  //
  // Se acota a la ceremonia y no al reporte entero: un modal secuestra, un toast no. Y NO se
  // calla —cae al aviso calmado de abajo— porque una acción que desaparece sin una palabra es la
  // misma clase de mentira que este hook existe para quitar, sólo que más silenciosa.
  //
  // Va aquí y no en los ocho llamantes porque el hook tiene 39 ficheros detrás: parchearlos uno a
  // uno arregla los de hoy y deja fuera a todos los que vengan.
  const montado = useRef(true)
  useEffect(() => {
    montado.current = true
    return () => {
      montado.current = false
    }
  }, [])
  return useCallback(
    (err: unknown, retry?: () => void) => {
      if (err instanceof ApiError && err.isStepUpRequired && montado.current) {
        const accepted = useStepUpStore.getState().require({
          action: stepUpAction ?? 'generic',
          retry,
        })
        // Another ceremony is already on screen. Say so instead of dropping this
        // one on the floor — an action that disappears without a word is the same
        // class of lie as reporting it as a role problem.
        if (!accepted) toast.warning(t('common:privileged.stepUp.busyToast'))
        return
      }
      // A 403 is a permission boundary, not a failure — show it calmly. Normally
      // the action is hidden/disabled when !can(), so this is a race-condition net.
      if (err instanceof ApiError && err.isForbidden) {
        toast.warning(t('common:privileged.notAuthorizedToast'))
        return
      }
      // Surface the engine's message as the toast description (it carries a stable,
      // non-leaking error message), under a generic localized failure title.
      const description =
        err instanceof Error && err.message ? err.message : undefined
      toast.error(
        t('errors:generic'),
        description ? { description } : undefined,
      )
    },
    [t, stepUpAction],
  )
}

/**
 * usePrivilegedMutation is the second half of the privileged-action pattern (the
 * first is ConfirmDialog). After the operator confirms, it: runs the mutation →
 * invalidates the affected query keys (so the view reflects the new state) → toasts
 * the result → calls onDone (typically: close the dialog/sheet). The backend remains
 * the authority: any failure surfaces as an error toast carrying the engine's
 * message. Every privileged route is self-audited server-side (docs/SECURITY-HARDENING.md).
 *
 * A 403 is not one answer but TWO, and the engine says which
 * (core/api/errors.go:224). `step_up_required` means the operator MAY do this and
 * the session is not assured enough → the step-up ceremony opens and this call
 * resumes once the backend elevates. A plain `forbidden` means the role cannot →
 * a calm "not authorized" warning, never a generic red error. Collapsing the two
 * told the operator to go ask for a role they already held.
 */
export interface PrivilegedMutationOptions<TVars, TData> {
  mutationFn: (vars: TVars) => Promise<TData>
  /** Query keys to invalidate on success — pass the narrowest prefixes that changed. */
  invalidateKeys?: QueryKey[] | ((data: TData, vars: TVars) => QueryKey[])
  /** Success toast title (string or derived from the result). */
  successMessage: string | ((data: TData, vars: TVars) => string)
  /** Optional success toast description. */
  successDescription?: string
  /** Run after a successful mutation + invalidation (e.g. close the dialog). */
  onDone?: (data: TData, vars: TVars) => void
  /** i18n key fragment naming the gated action for the step-up ceremony
   *  (identity:assurance.actions.*), when the engine answers `step_up_required`.
   *  Defaults to the generic phrasing — the panel falls back on its own. */
  stepUpAction?: string

  /**
   * Optional first look at a failure, for the errors a feature ANSWERS instead of
   * reporting. Return true to claim the error: the default toast is suppressed and the
   * feature's own UI is the response.
   *
   * It exists for the compare-and-swap routes, where a 409 is not a failure but the
   * engine telling the operator that the state moved between their read and their write
   * (capabilities/api.ts isPreconditionConflict). The correct answer is to refetch and
   * SHOW the divergence, not a red "something went wrong" — and never an automatic
   * resend with the fresh value, which would overwrite the other writer while looking
   * like a success.
   *
   * Claiming an error you do not actually handle silences it, so return false for
   * anything you are not rendering.
   *
   * It is NOT consulted for 401/403: the authorization boundary is resolved before this
   * callback runs and always surfaces, so no feature can suppress it.
   */
  onError?: (err: unknown, vars: TVars) => boolean
}

export function usePrivilegedMutation<TVars = void, TData = unknown>(
  opts: PrivilegedMutationOptions<TVars, TData>,
) {
  const { t } = useTranslation(['common', 'errors'])
  const queryClient = useQueryClient()
  const report = useFailedActionReporter(opts.stepUpAction)
  // One automatic resume per ACTION, and no more. The ceremony grants AAL3, the
  // highest level the engine issues, so a SECOND step_up_required right after a
  // verified ceremony means something other than assurance is refusing — retrying
  // that forever would be a loop the operator cannot see. The panel still opens;
  // only the automatic resume is spent.
  //
  // The budget belongs to the action, NOT to the hook instance. It used to be
  // spent for the lifetime of the hook, so once one action had burned its resume,
  // every LATER action from the same component was refused a retry too: the
  // operator completed a ceremony and the panel just closed. `onMutate` below
  // resets it for any call the operator started, and leaves it alone for the one
  // we resumed ourselves.
  const resumedRef = useRef(false)
  const resumingRef = useRef(false)
  // The resume has to call back into the mutation this hook is still building, so
  // it goes through a ref filled in below. react-query keeps `mutate` stable, so
  // by the time a ceremony can possibly complete this is set.
  const mutateRef = useRef<((vars: TVars) => void) | null>(null)
  // ⛔ EL INQUILINO EN QUE SE PIDIÓ LA ACCIÓN. La reanudación tras la ceremonia es, por diseño,
  //    «la misma acción» —el store lo dice con esas palabras—, pero sólo conserva `vars`. Una
  //    ceremonia de step-up dura lo que el operador tarde, y el selector de inquilino sigue
  //    habilitado: si cambia mientras la resuelve, reanudar aplicaría en B lo que pidió en A.
  //    `mutations.retry` es `false` y no protege de esto, porque no es un reintento: es una
  //    llamada nueva. Se guarda al empezar y se compara al reanudar.
  const inquilinoDeLaAccion = useRef<string | null>(null)

  const mutation = useMutation<TData, unknown, TVars>({
    mutationFn: opts.mutationFn,
    onMutate: () => {
      // A call the OPERATOR started is a new action and gets a fresh resume
      // budget; the one we resumed ourselves keeps the spent one.
      if (resumingRef.current) resumingRef.current = false
      else {
        resumedRef.current = false
        inquilinoDeLaAccion.current = useTenantStore.getState().activeTenant
      }
    },
    onSuccess: async (data, vars) => {
      const keys =
        typeof opts.invalidateKeys === 'function'
          ? opts.invalidateKeys(data, vars)
          : (opts.invalidateKeys ?? [])
      await Promise.all(
        keys.map((queryKey) => queryClient.invalidateQueries({ queryKey })),
      )
      const message =
        typeof opts.successMessage === 'function'
          ? opts.successMessage(data, vars)
          : opts.successMessage
      toast.success(
        message,
        opts.successDescription
          ? { description: opts.successDescription }
          : undefined,
      )
      opts.onDone?.(data, vars)
    },
    onError: (err, vars) => {
      // The step-up branch is the only one that can resume: this hook still holds
      // the variables the refused call was made with, so the operator does not
      // have to refill the form after the ceremony.
      const isStepUp = err instanceof ApiError && err.isStepUpRequired
      const canResume = isStepUp && !resumedRef.current
      // THE AUTHORIZATION BOUNDARY IS NOT DELEGABLE, and that is #700's property kept whole on
      // top of main's ceremony flow. `report` already routes a 403 calmly and a step_up_required
      // to the ceremony; what was missing is that a feature callback must NEVER get first refusal
      // on 401/403. A callback returning true there — by intent or by a `() => true` written in a
      // hurry — would suppress the warning, and the operator would watch a privileged action
      // quietly do nothing when the answer was "you may not". Raised by the the model contrast
      // of. Below the boundary the feature keeps first refusal: an error it renders itself
      // must not also produce a generic toast contradicting its own explanation.
      // The boundary is checked AFTER step-up is identified and BEFORE the feature callback.
      // Order matters in both directions and the merged suite proves each one: step_up_required
      // is itself a 403 with a ceremony behind it, so it must not be short-circuited here; and
      // 401 is NOT handled by `report` — it falls through to the generic error toast — which is
      // why #700's explicit branch is kept rather than delegated to the reporter.
      const isAuthBoundary =
        !isStepUp &&
        err instanceof ApiError &&
        (err.isForbidden || err.isUnauthenticated)
      if (isAuthBoundary) {
        toast.warning(t('common:privileged.notAuthorizedToast'))
        return
      }
      // ⛔ EL STEP-UP TAMPOCO ES DELEGABLE, y por el mismo motivo que el 401/403: es un
      // 403 CON ceremonia detrás. Sin el `!isStepUp`, una feature con `onError: () => true`
      // —el mismo `() => true` escrito con prisa que el comentario de arriba nombra— se
      // traga la demanda de elevación, la ceremonia NO se abre y el operador ve la acción
      // privilegiada no hacer nada. Reproducido con la celda «NEVER lets a feature swallow
      // the STEP-UP demand either»: sin esta guarda, `useStepUpStore.getState().request`
      // se queda en null.
      if (!isStepUp && opts.onError?.(err, vars)) return
      report(
        err,
        canResume
          ? () => {
              // ⛔ NO SE REANUDA EN OTRO INQUILINO. Reanudar es repetir el acto, y el acto se
              //    pidió en un inquilino concreto; si el operador cambió durante la ceremonia,
              //    repetirlo aquí lo aplicaría en el nuevo. Se rechaza y se le dice, que es
              //    recuperable —puede volver y repetirlo— a diferencia de una escritura ya
              //    aplicada donde no tocaba.
              if (
                inquilinoDeLaAccion.current !== useTenantStore.getState().activeTenant
              ) {
                toast.warning(t('common:privileged.tenantChangedToast'))
                return
              }
              resumedRef.current = true
              // Tells onMutate that THIS call is our resume, not a new action.
              resumingRef.current = true
              mutateRef.current?.(vars)
            }
          : undefined,
      )
    },
  })

  // Synced in an effect, not during render: a ref written while rendering is a
  // React violation the lint catches, and there is no race — `mutate` is stable
  // and the effect lands long before any ceremony can complete.
  useEffect(() => {
    mutateRef.current = mutation.mutate
  }, [mutation.mutate])

  return mutation
}
