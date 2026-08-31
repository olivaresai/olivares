// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import type { WorkIntent } from './api'
import { sembrarDesde } from './apply-flow'

/**
 * C-12 — la intención conserva lo que ya sabe, y el reintento repite LA FASE QUE FALLÓ.
 *
 * ⛔ DEFECTO 1: DOS EFECTOS PELEÁNDOSE POR EL MISMO ESTADO. Uno sembraba `values` desde
 *    `intent.body`; el otro, al abrir el diálogo con campos requeridos, hacía `setValues({})` y
 *    **borraba lo sembrado**. El resultado era justo lo que el sembrado existe para evitar: el
 *    operador transcribiendo a mano un `holder_sid` que la pantalla de al lado ya exhibe, y cada
 *    errata pagada con un `invalid_command` del motor. Ahora los dos caminos usan la MISMA
 *    función, que es la forma de que no puedan discrepar.
 *
 * ⛔ DEFECTO 2: EL REINTENTO SIEMPRE APLICABA. `onRetrySameKey` estaba cableado a `runApply`,
 *    así que un fallo del PLAN se reintentaba como APPLY: saltaba el paso que existe para que
 *    nadie escriba sin haber visto antes lo que va a escribir, y encima con la misma clave de
 *    idempotencia, así que el motor lo leería como el reintento de una intención ya planificada.
 *    Sólo un APPLY ambiguo se repite como APPLY — ahí la misma clave es lo que lo hace seguro.
 *
 * Este fichero prueba el sembrado, que es la parte pura. El cableado del reintento se prueba por
 * su mutante en el commit: cambiarlo por `runApply` incondicional.
 */

const intent = (body: unknown): WorkIntent =>
  ({ key: 'k1', tenant: 't1', command: 'lease.acquire', body }) as WorkIntent

describe('C-12 · el diálogo conserva lo que la intención ya sabe', () => {
  it('siembra las cadenas del cuerpo', () => {
    expect(sembrarDesde(intent({ holder_sid: 'sesion-7' }))).toEqual({
      holder_sid: 'sesion-7',
    })
  })

  it('siembra los NÚMEROS como texto: el formulario es de cadenas', () => {
    // Sin esto, un campo numérico llegaría vacío y el operador lo transcribiría — el defecto
    // original con otra ropa.
    expect(sembrarDesde(intent({ fence: 7 }))).toEqual({ fence: '7' })
  })

  it('NO siembra lo que no es escalar: un objeto no se puede teclear en un input', () => {
    expect(sembrarDesde(intent({ meta: { a: 1 }, ok: 'x' }))).toEqual({
      ok: 'x',
    })
  })

  it('sin intención devuelve vacío, no revienta', () => {
    expect(sembrarDesde(null)).toEqual({})
  })

  it('CONTROL: no inventa claves que la intención no trae', () => {
    // Calibra los de arriba: una implementación que devolviera un objeto fijo pasaría los otros.
    expect(sembrarDesde(intent({}))).toEqual({})
  })
})
