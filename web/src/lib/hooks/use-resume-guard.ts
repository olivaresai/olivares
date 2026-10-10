// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useResumeGuard wraps the retry passed to step-up so an unmounted form cannot write.
//
// The Codex `sol max` review of demonstrated why: `useFailedActionReporter` stores
// the retry globally (`lib/hooks/use-privileged-mutation.ts:33-46`), and the host lives
// alongside the whole application (`app/providers.tsx:45-55`). Closing a dialog or
// navigating unmounts the form but leaves the host alive. Completing step-up would then
// call an action the operator abandoned.
//
// This can perform a real write: `useMutation` retains a callback to `observer.mutate`,
// and `MutationObserver.mutate` creates and executes a new Mutation even without listeners.
//
// The host calls `clear()` before retry (`components/layout/step-up-host.tsx:77-84`), so
// step-up closes normally. Prevent the orphaned write and explain why it did not resume,
// because the panel promises that the action resumes.
import { useCallback, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from '@/components/ui/toaster'

/**
 * Devuelve un envoltorio para el callback de reintento. Mientras el componente siga montado el
 * reintento corre igual; si ya no lo está, no ejecuta nada y lo dice.
 *
 * ⛔ TECHO DECLARADO: esto protege al PROPIETARIO del reintento, no al store. Si el operador
 * abandona la acción, la demanda global sigue en pie hasta que la ceremonia termine o alguien la
 * cierre — no se limpia desde aquí porque el store no distingue de quién es cada petición
 * (`stores/step-up.ts:48-56`) y borrarla a ciegas cancelaría la ceremonia de otro.
 */
export function useResumeGuard() {
  const { t } = useTranslation('common')
  const montado = useRef(true)
  useEffect(() => {
    montado.current = true
    return () => {
      montado.current = false
    }
  }, [])

  return useCallback(
    (reintento: () => void) => () => {
      if (!montado.current) {
        toast.warning(t('common:privileged.stepUp.abandonedToast'))
        return
      }
      reintento()
    },
    [t],
  )
}
