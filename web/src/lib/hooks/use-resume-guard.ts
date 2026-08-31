// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// useResumeGuard — envuelve el reintento que se entrega a la ceremonia para que NO ejecute la
// escritura de un formulario que ya no existe.
//
// ⛔ POR QUÉ HACE FALTA, medido por el contraste Codex `sol max` sobre. El reintento no se
// queda en el componente: `useFailedActionReporter` lo mete en un store GLOBAL
// (`lib/hooks/use-privileged-mutation.ts:33-46`) y el host vive junto a toda la aplicación
// (`app/providers.tsx:45-55`). Cerrar el diálogo o navegar **desmonta el formulario pero no el
// host**, así que al terminar la ceremonia se ejecuta el callback de una acción que el operador
// ya abandonó.
//
// Y no es un `setState` inocuo: `useMutation` conserva un callback que llama a `observer.mutate`,
// y `MutationObserver.mutate` construye y ejecuta una `Mutation` nueva aunque haya perdido sus
// listeners. Es decir, **la escritura sale**.
//
// El panel no se queda colgado: el host hace `clear()` ANTES de invocar el retry
// (`components/layout/step-up-host.tsx:77-84`), así que la ceremonia se cierra sola. Lo único
// que hay que impedir es la escritura huérfana — y decirlo, porque la copy del panel promete
// «the action resumes» y callarse sería cambiar una promesa rota por otra.
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
