// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Resuelve el propietario de un work item a algo que una persona pueda leer.
//
// ⛔ POR QUE EXISTE. La captura del 2026-08-26 enseñaba las cinco filas de `/work` repitiendo
//    `user:01a03ed2-7563-7851-83de-88fe017065fd`. Un UUID crudo repetido es de las cosas que mas
//    barato delatan una demo, y ante un operador real no dice NADA: no puedes saber si dos filas
//    son del mismo dueño sin comparar 36 caracteres a ojo.
//
// ⛔ POR QUE UN HELPER Y NO EL JOIN EN CADA VISTA. Lo piden DOS componentes (`work-view` y
//    `item-detail`). Dos joins copiados divergen el dia que uno se edita — este repositorio ya tiene
//    medido ese fallo con dos copias de una misma comprobacion envejeciendo aparte. Uno solo.
//
// ⛔ Y POR QUE SOLO `user`. `owner_kind` puede ser `user`, `agent` o `session`
//    (`modules/sessions/work_state.go:29`), y **solo el primero tiene un padron que consultar**:
//    `/v1/members` devuelve `display_name` por `user_id`. Para `agent` y `session` no hay aqui una
//    fuente equivalente, asi que **caen al ref y se dice**, en vez de fingir cobertura de los tres.
//    El arreglo de fondo —que el propio work item traiga el nombre resuelto para los tres, en
//    `modules/sessions/work_read.go:98`— es del carril de sessions y esta escrito en el plan.
import { useQuery } from '@tanstack/react-query'
import { consoleApi } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'

/**
 * Devuelve una funcion que etiqueta un par (owner_kind, owner_ref).
 *
 * UNA consulta por inquilino, cacheada: las cinco filas de la lista comparten el mismo padron, asi
 * que pedirlo por fila serian cinco peticiones para una sola respuesta.
 */
export function useOwnerLabel(): (kind: string, ref: string) => string {
  const { activeTenant } = useAuth()
  const { data } = useQuery({
    queryKey: ['work', activeTenant, 'owner-roster'] as const,
    queryFn: () => consoleApi.listMembers(),
    // El padron cambia poco y esto es decoracion de una lista: que un nombre tarde un minuto en
    // reflejarse no rompe nada, y releerlo en cada montaje si cuesta.
    staleTime: 60_000,
  })

  return (kind: string, ref: string): string => {
    if (kind !== 'user') return ref
    const miembro = data?.items?.find((m) => m.user_id === ref)
    // `display_name` es opcional en el padron (`RosterMemberDTO`), y mientras la consulta esta en
    // vuelo no hay padron ninguno: en los dos casos se cae al ref, que es cierto siempre.
    return miembro?.display_name || ref
  }
}
