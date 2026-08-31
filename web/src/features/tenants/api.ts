// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-02 — ciclo de vida de tenant: retirar y restaurar el servicio.
//
// ⛔ POR QUÉ ESTA SUPERFICIE EXISTE Y POR QUÉ AQUÍ. `PUT /v1/system/orgs/{tenant_id}/status` lleva
// en el motor y en el OpenAPI desde hace tiempo, y en la consola aparecía en UN solo sitio:
// `lib/api/openapi.gen.ts` — es decir, como TIPO GENERADO y sin un solo llamante. Un tipo no es una
// superficie: retirar el servicio a un tenant sólo se podía hacer con `curl`.
//
// ⚠ Y NO va en la vista de residencia aunque ella ya liste los orgs. Esa pantalla se llama «Data
// residency» y trata de dónde viven los datos; colgarle una acción de ciclo de vida sería una
// pantalla que miente sobre lo que es. Además, el roster de residencia **ni siquiera muestra el
// `status`**, así que hoy un operador no puede ver que un tenant está suspendido.
//
// ⚠ C07-09 pide una superficie para `/admin/tenants*`, y esa API **no existe** (404 medido en motor
// vivo el 2026-08-18). Esto NO la sustituye: se construye sobre las rutas `/v1/system/orgs*` que sí
// existen, y cuando C05-05 aterrice su API, extenderá esta vista en vez de estrenar otra.
//
// ── EL RECORTE DE `GET /v1/system/orgs`, MEDIDO (2026-08-27) ─────────────────────────
//
// Paso 1 de la receta de `scripts/check-list-truncation-witness.sh` («MIDE EL MOTOR PRIMERO»).
// Veredicto: caso (a), DRENA. Las sondas, para que la siguiente sesion no lo re-derive:
//
//   · `handleListOrgs` (core/api/handlers_core.go:739-761) NO llama a `parseListQuery`, no lee
//     `?limit` ni `?cursor`, y nunca fija `Cursor` ni el campo de recorte de la respuesta.
//   · Su almacen (`core/internal/store/sqlstore/system.go:497` -> `listOrgsVisibleRows`) emite
//     `SELECT ... ORDER BY id ASC` SIN `LIMIT`. O devuelve todo lo visible, o falla con
//     `store.ErrEnumerationNotAuthoritative` — nunca un parcial callado.
//   · Es el UNICO listador del nucleo que todavia no usa `parseListQuery`: los demas
//     (`handleListUsers` :233, `handleListAgents` :60) si lo usan y si publican el recorte.
//
// ⇒ HOY el campo de recorte llega siempre apagado, y por eso `<ListTruncationBadge>` no pinta
//   nada: exige el valor booleano verdadero (`features/_intel/notices.tsx`) y devuelve `null` en
//   cualquier otro caso. No afirma cobertura: no dice nada.
//
// ⛔ ENTONCES POR QUE VA EL AVISO. Porque manda el CONTRATO, no la implementacion de hoy:
//    `web/openapi/openapi.json` declara ese campo en el 200 de esta operacion (`operationId`
//    `listOrgs`), asi que el motor puede empezar a paginar SIN romper nada publicado — y siendo
//    el unico listador que aun no pagina, es el candidato natural a que alguien lo alinee con
//    sus vecinos. El dia que pase, esta pantalla ya lo dice en vez de ensenar las primeras cien
//    filas como si fueran el censo entero.
//
//    El coste es asimetrico y por eso se decide asi: ponerlo mientras drena no cuesta nada y no
//    miente; omitirlo cuando deje de drenar se descubre con la lista ya mal leida.
//
// ⚠ Y AQUI EL DANO SERIA PEOR QUE EN RESIDENCIA, que pide esta MISMA lista: esta pantalla decide
//   a QUIEN se le retira el servicio. Un roster recortado leido como completo es un tenant
//   suspendido —o no suspendido— por una lista incompleta.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'

/** Un org tal y como lo sirve el roster de sistema (core/api OrgDTO). */
export interface TenantDTO {
  id: string
  tenant_id: string
  name: string
  slug: string
  status: string
  data_region?: string
  created_at: string
}

/**
 * ⛔ EL CONJUNTO ES ABIERTO, Y ESO NO ES PEDANTERÍA. El OpenAPI declara `enum: [active, suspended]`
 * para lo que se ESCRIBE, pero `status` en la respuesta es `type: string` sin enum: lo que el motor
 * devuelva mañana —un tercer estado, una cadena vacía de una fila antigua— llega igual.
 *
 * Colapsar lo desconocido a «activo» pintaría como sano justo el caso en que nadie sabe qué pasa.
 * Es literalmente el defecto que medí en `estadoCentro` de finops el mismo mes: `""` no es
 * «activo». Un tercer valor se muestra como lo que es.
 */
export type EstadoTenant = 'active' | 'suspended' | 'unknown'

export function estadoTenant(bruto: string | undefined): EstadoTenant {
  if (bruto === 'active' || bruto === 'suspended') return bruto
  return 'unknown'
}

export const tenantsApi = {
  list: () => http.get<ListResponse<TenantDTO>>('/v1/system/orgs'),
  /**
   * Retira (`suspended`) o restaura (`active`) el servicio. **No borra nada**: borrar es la otra
   * ruta, `DELETE /v1/system/orgs/{tenant_id}`, y es destructiva. El motor documenta tres cosas que
   * siguen funcionando con el servicio retirado, y la consola las dice antes de confirmar porque
   * son la diferencia entre «retirar el servicio» y «secuestrar los datos del cliente».
   */
  setStatus: (tenantId: string, status: 'active' | 'suspended') =>
    http.put<TenantDTO>(
      `/v1/system/orgs/${encodeURIComponent(tenantId)}/status`,
      { status },
    ),

  /**
   * BORRADO DURO E IRREVERSIBLE. Contrato medido contra el motor, no leído de la ficha OpenAPI:
   *
   *  · `core/api/handlers_core.go:663` (`handleDropOrg`) purga y devuelve **204**. Comprueba que el
   *    org existe antes de llamar a `DropTenant` porque esa primitiva trata un tenant ausente como
   *    un borrado de cero filas — de ahí el **404** cuando no existe.
   *  · **403** para quien no sea `system:admin`. Ni el DUEÑO del tenant puede
   *    (`TestSystemDropOrgNonSuperadminForbidden`).
   *  · **400** para el tenant de sistema y para un id vacío (`TestSystemDropOrgRejectsSystemTenant`).
   *
   * ⛔ Y EL DATO QUE MANDA SOBRE LA COPY, porque la ficha OpenAPI induce a error: su resumen dice
   *    «Hard-delete a tenant org **after the cloud grace period**», y esa gracia de 30 días la aplica
   *    el PLANO DE CONTROL CLOUD — no este motor. Aquí no hay red de seguridad: la llamada purga en
   *    el momento. Un diálogo que insinuara un periodo de gracia estaría prometiendo algo que este
   *    binario no hace, y el operador lo descubriría con los datos ya borrados.
   *
   * ⚠ El **409** que la ficha declara es boilerplate compartido de `op204`, no un conflicto propio de
   *   esta ruta. Por eso la consola NO le pone motivo: enseña el error del motor tal cual en vez de
   *   inventarle una causa.
   */
  remove: (tenantId: string) =>
    http.delete<void>(`/v1/system/orgs/${encodeURIComponent(tenantId)}`),
}

export const tenantKeys = {
  all: () => ['tenants'] as const,
  list: () => ['tenants', 'list'] as const,
}
