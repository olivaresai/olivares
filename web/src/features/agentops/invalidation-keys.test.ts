// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// La clave que se INVALIDA tiene que ser prefijo de la que se CONSULTA.
//
// ⛔ ESTE DEFECTO ESTUVO VIVO Y NO DEJABA RASTRO. `invalidateQueries({ queryKey })` casa por
// PREFIJO. El panel consultaba con `workspaces(t, { limit: PAGE })` y las dos mutaciones
// invalidaban con `workspaces(t)`, que resuelve a `[…,'workspaces', null]`. Ese `null` final NO es
// prefijo de `[…,'workspaces', {limit}]`, así que la invalidación no casaba con nada.
//
// Para el operador: registra un directorio, el diálogo se cierra SIN error —la llamada tuvo éxito,
// 201 verificado contra el motor— y la pantalla sigue diciendo «No workspaces registered». Ni
// toast, ni error de consola. Un fallo que no se ve.
//
// ⚠ Y por eso la aserción es sobre las CLAVES y no sobre un render: el defecto no está en lo que se
// pinta, está en dos arrays que no encajan. Una celda de componente lo taparía en cuanto el doble
// devolviera la lista ya actualizada.
import { describe, expect, it } from 'vitest'
import { agentOpsKeys } from './api'

/** La regla de react-query, escrita como función para poder mutarla y ver la celda caer. */
function esPrefijo(prefijo: readonly unknown[], clave: readonly unknown[]) {
  if (prefijo.length > clave.length) return false
  return prefijo.every((x, i) => JSON.stringify(x) === JSON.stringify(clave[i]))
}

describe('claves de invalidación de agentops', () => {
  const t = 'tenant-1'

  it('la clave sin params es prefijo de la que usa la LISTA', () => {
    const lista = agentOpsKeys.workspaces(t, { limit: 50 })
    expect(
      esPrefijo(agentOpsKeys.workspaces(t), lista),
      `invalidar con ${JSON.stringify(agentOpsKeys.workspaces(t))} no alcanza a ` +
        `${JSON.stringify(lista)}: la lista no se refrescaría`,
    ).toBe(true)
  })

  it('y es idéntica a sí misma', () => {
    expect(
      esPrefijo(agentOpsKeys.workspaces(t), agentOpsKeys.workspaces(t)),
    ).toBe(true)
  })

  /** ⛔ AQUÍ HABÍA UNA CELDA QUE FIJABA EL DEFECTO: afirmaba que `workspaces(t)` NO era prefijo de
   *  `workspaces(t, {limit})`, como guarda de regresión. Se retiró al arreglar el constructor, que
   *  es lo correcto — una celda que exige que el fallo siga ahí impide arreglarlo.
   *
   *  Lo que la sustituye es la comprobación de la FORMA: la clave sin params no puede terminar en
   *  un elemento extra, porque ése es el `null` que rompía el prefijo. */
  it('la clave sin params no arrastra un elemento de más', () => {
    expect(agentOpsKeys.workspaces(t)).toHaveLength(3)
    expect(agentOpsKeys.workspaces(t, { limit: 50 })).toHaveLength(4)
  })

  // B1: las dos listas del plano de perfiles siguen la MISMA regla que workspaces — la
  // clave sin params es prefijo de la que usa la lista y no arrastra un elemento de más —,
  // y la configuración (la única lectura con rutas) cuelga de SU perfil: invalidar el perfil
  // la alcanza, invalidar la lista no la toca, y todas llevan el inquilino en el mismo hueco.
  it('perfiles y vínculos: sin params es prefijo de la lista y no arrastra nada', () => {
    const e = 7
    expect(
      esPrefijo(
        agentOpsKeys.profiles(t, e),
        agentOpsKeys.profiles(t, e, { limit: 100 }),
      ),
    ).toBe(true)
    expect(
      esPrefijo(
        agentOpsKeys.bindings(t, e),
        agentOpsKeys.bindings(t, e, { profile_ref: 'ppf_a', limit: 100 }),
      ),
    ).toBe(true)
    expect(agentOpsKeys.profiles(t, e)).toHaveLength(5)
    expect(agentOpsKeys.bindings(t, e)).toHaveLength(5)
    // The configuration read (the only one that carries a path) has NO key on
    // purpose: it never enters the QueryCache — it lives in the sheet's explicit
    // reveal cycle and leaves with it (useFreshRead).
    expect('profileConfiguration' in agentOpsKeys).toBe(false)
    expect(
      esPrefijo(
        agentOpsKeys.profiles(t, e),
        agentOpsKeys.profile(t, e, 'ppf_a'),
      ),
    ).toBe(false)
    expect(
      esPrefijo(
        agentOpsKeys.profile(t, e, 'ppf_a'),
        agentOpsKeys.launchReadiness(t, e, 'ppf_a', {
          transport: 'stream-json',
          isolation: 'native',
        }),
      ),
    ).toBe(true)
    expect(
      esPrefijo(
        agentOpsKeys.launchReadiness(t, e, 'ppf_a', {
          transport: 'stream-json',
          isolation: 'native',
        }),
        agentOpsKeys.launchReadiness(t, e, 'ppf_b', {
          transport: 'stream-json',
          isolation: 'native',
        }),
      ),
    ).toBe(false)
    for (const k of [
      agentOpsKeys.profiles(t, e),
      agentOpsKeys.profile(t, e, 'ppf_a'),
      agentOpsKeys.bindings(t, e),
      agentOpsKeys.binding(t, e, 'psb_1'),
    ]) {
      expect(k[0]).toBe('agentops')
      expect(k[1]).toBe(t)
    }
  })

  // C3: el plano de perfiles está PARTICIONADO por frontera de autoridad (una época
  // opaca), no sólo por inquilino. La época va DEBAJO del inquilino, de modo que el
  // ámbito de una frontera es prefijo de todas sus claves —y de ninguna de otra
  // frontera— y `all(t)` sigue alcanzándolo todo.
  it('la frontera de autoridad separa dos principales del mismo inquilino', () => {
    const a = agentOpsKeys.boundaryScope(t, 1)
    const b = agentOpsKeys.boundaryScope(t, 2)
    for (const mk of [
      (e: number) => agentOpsKeys.profiles(t, e, { limit: 100 }),
      (e: number) => agentOpsKeys.profile(t, e, 'ppf_a'),
      (e: number) =>
        agentOpsKeys.launchReadiness(t, e, 'ppf_a', {
          transport: 'stream-json',
          isolation: 'native',
        }),
      (e: number) => agentOpsKeys.bindings(t, e, { limit: 100 }),
      (e: number) => agentOpsKeys.binding(t, e, 'psb_1'),
    ]) {
      expect(esPrefijo(a, mk(1))).toBe(true)
      expect(esPrefijo(a, mk(2))).toBe(false)
      expect(esPrefijo(b, mk(2))).toBe(true)
      expect(esPrefijo(agentOpsKeys.all(t), mk(1))).toBe(true)
      expect(esPrefijo(agentOpsKeys.all('otro'), mk(1))).toBe(false)
    }
    // Una época es un número opaco: ni id de usuario, ni id de sesión, ni token.
    expect(typeof a[3]).toBe('number')
  })

  // CONTROL: `all` sí alcanza, pero invalida TAMBIÉN runs y ficheros. Se prefiere el prefijo
  // estrecho; esta celda existe para que quede escrito que la alternativa ancha funcionaba y se
  // descartó a propósito, no por desconocerla.
  it('all alcanzaría, y es más ancho de lo necesario', () => {
    expect(
      esPrefijo(agentOpsKeys.all(t), agentOpsKeys.workspaces(t, { limit: 50 })),
    ).toBe(true)
    expect(esPrefijo(agentOpsKeys.all(t), agentOpsKeys.runs(t, {}))).toBe(true)
  })
})
