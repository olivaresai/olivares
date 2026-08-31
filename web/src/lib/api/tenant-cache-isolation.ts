// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { QueryClient } from '@tanstack/react-query'
import { useTenantStore } from '@/stores/tenant'

/**
 * ⛔ POR QUÉ EXISTE: la clave se calcula al PINTAR y la cabecera se lee al ENVIAR.
 *
 * El contrato de `query.ts` dice que un dato de inquilino lleva el inquilino en su clave, «so
 * switching tenant cache-isolates and refetches cleanly», y es cierto para lo que ya está en la
 * caché. No lo es para lo que está EN VUELO. La cabecera no viaja con la clave: para las consultas
 * JSON que pasan por el cliente genérico sin fijar `opts.tenant`, el inquilino se lee en el momento
 * del envío, de un getter al store (`app/providers.tsx` → `lib/api/client.ts:212`). Entre esos dos
 * instantes el operador puede cambiar de inquilino, y entonces la respuesta del inquilino B se
 * guarda bajo la clave del inquilino A.
 *
 * (Acotación medida: NO vale para toda la consola. Los transportes que reciben el inquilino y lo
 * fijan explícitamente —SSE en `features/shared/sse.ts:79`, varias descargas crudas— no tienen esta
 * carrera. `RequestOptions.tenant` ya existía como costura por petición y hoy no lo usa ninguna
 * consulta JSON de producción.)
 *
 * Tres ventanas, las tres en el código y no supuestas:
 *
 *  1 · ANTES DEL ENVÍO, y sólo si la credencial caduca pronto: `apiFetchWithMeta` hace
 *      `await refreshOnce()` (client.ts:200) ANTES de componer las cabeceras.
 *  2 · EL REINTENTO. `createQueryClient` reintenta dos veces los 5xx y los fallos de red
 *      (query.ts:23) con backoff por defecto. Un reintento posterior al cambio lleva la cabecera
 *      NUEVA y se guarda bajo la clave VIEJA.
 *  3 · EL REPLAY TRAS UN 401: el cliente refresca y vuelve a entrar en `apiFetchWithMeta`
 *      (client.ts:269), que recompone las cabeceras y vuelve a leer el inquilino actual. Ésta la
 *      encontró el contraste; es la más deliberada de las tres, porque el código la crea a propósito.
 *
 * Y el daño no se ve al instante: el dato queda cacheado bajo la clave del inquilino A, así que se
 * enseña —sin volver a pedirlo— cuando el operador VUELVE a A. No son sólo los 30 s de `staleTime`:
 * pasado ése, TanStack sigue sirviendo el dato cacheado mientras refresca, y la entrada vive hasta
 * `gcTime` (5 min).
 *
 * EL ARREGLO ES UNO Y ESTÁ EN UN SITIO. No hace falta pasar el inquilino por las ~474 llamadas a
 * las fábricas: basta con que ninguna consulta empezada bajo el inquilino viejo pueda ESCRIBIR
 * después del cambio. `cancelQueries` cancela lo que vuela y los reintentos pendientes, y TanStack
 * Query 5.101.4 descarta el resultado de una consulta cancelada aunque su promesa acabe
 * resolviendo (`retryer.ts:111-115`, `query.ts:565-579`). La excepción documentada son los hooks
 * Suspense, y en `web/src` no se usa ninguno.
 *
 * ⛔ SE CANCELA SÓLO LO DEL INQUILINO VIEJO, Y ESO NO ES UN DETALLE. La primera versión llamaba a
 *    `cancelQueries()` sin filtro. El contraste lo midió con un observador real: una consulta
 *    GLOBAL cancelada al vuelo queda en `{status:'pending', fetchStatus:'idle'}` y **no se
 *    reintenta sola**; como `isLoading` sólo es cierto con `pending + fetching`, `AsyncSection`
 *    devuelve `null` (`features/_intel/async.tsx:36`) y el listado de inquilinos se queda en
 *    spinner (`features/tenants/tenants-view.tsx:104`) mientras la vista siga montada. Es decir:
 *    la primera versión curaba el envenenamiento y a cambio dejaba secciones en blanco.
 *
 *    El filtro se apoya en el trinquete de (`features/tenant-scoped-keys.test.ts`), que exige
 *    que toda clave de inquilino lleve el id dentro. Es exactamente tan fuerte como ese trinquete
 *    y no más: si alguien lo desactivara, este filtro dejaría de alcanzar esas claves.
 *
 * ⛔ LO QUE ESTO **NO** CUBRE, dicho por delante para que nadie lo lea como que sí:
 *
 *  · **Las mutaciones.** `cancelQueries` sólo recorre la `QueryCache`; la `MutationCache` no se
 *    toca, y la versión instalada no tiene un `cancelMutations` público. Una ESCRITURA empezada en
 *    A que se detenga en el refresco previo o en el replay del 401 se aplica en B. Es un defecto
 *    peor que el de lectura y NO se arregla aquí: necesita fijar el inquilino en el acto
 *    (`RequestOptions.tenant` desde las variables de la mutación). Reportado aparte.
 *  · **Las fronteras de autorización.** Conservar la caché del inquilino viejo es correcto para un
 *    cambio ordinario, y no para un cambio de principal, un 401 terminal o una membresía retirada.
 *    Sólo `logout()` limpia (`lib/auth/context.tsx:209`).
 *  · **El primer cambio anterior al efecto.** El enganche toma como «previo» el valor que hay
 *    cuando corre; un cambio ocurrido entre el render y el efecto pasa a ser línea base.
 */
export function isolateCacheOnTenantChange(
  queryClient: QueryClient,
): () => void {
  let previo = useTenantStore.getState().activeTenant
  return useTenantStore.subscribe((s) => {
    if (s.activeTenant === previo) return
    const viejo = previo
    previo = s.activeTenant
    // Sin inquilino previo no hay ninguna clave que lo lleve dentro: nada que aislar.
    if (viejo === null) return
    // `void`: cancelar es fire-and-forget — la promesa sólo dice cuándo terminó de cancelar.
    void queryClient.cancelQueries({
      predicate: (q) => q.queryKey.some((seg) => seg === viejo),
    })
  })
}
