// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import { workErrorCode, workErrorReason } from './api'

/**
 * C-14 (parte del cliente) — el MOTIVO del motor se conserva y se muestra.
 *
 * ⛔ QUÉ SE ARREGLÓ. `ApplyFlow` clasificaba el rechazo en una categoría propia
 *    (`conflict-domain`, `conflict-version`…), pintaba un texto genérico traducido y **tiraba el
 *    `message` del motor**. Para una transición imposible el operador leía «conflicto de dominio»
 *    y un código, nunca «no se puede pasar de draft a complete». El rechazo era visible; la razón,
 *    no.
 *
 * ⛔ POR QUÉ SON DOS FUNCIONES Y NO UNA. El `code` es legible por máquina y estable: con él se
 *    elige la ACCIÓN que se ofrece, y re-leer tras un conflicto de versión no es reintentar. El
 *    `message` es PROSA (`errors.ts:20`) y puede cambiar entre versiones. Separarlos es lo que
 *    impide que alguien acabe ramificando sobre el texto — que funcionaría hasta el día que el
 *    motor reescriba una frase, y entonces fallaría en silencio.
 *
 * ⛔ Y LA PARTE DEL CONTRATO QUE ESTE FICHERO **NO** CUBRE, dicha aquí para que nadie la dé por
 *    hecha: filtrar los comandos por el estado FSM. Eso NO se hace en el cliente — exigiría
 *    duplicar la máquina de estados del motor y fabricar la deriva silenciosa que `types.ts`
 *    documenta. El motor publicará `allowed_transitions` y el cliente consumirá ESE dato. Mostrar
 *    el motivo se queda igualmente: es el cinturón contra cualquier deriva futura.
 */

/**
 * ⛔ LA FIRMA COMPLETA, Y VA COMENTADA PORQUE ME LA COMÍ. `ApiError` es
 *    `(status, code, message, requestId?, details?, body?)` — SEIS parámetros. En la primera
 *    versión pasé cinco y el `body` aterrizó en la posición de `details`, así que
 *    `workErrorReason` no veía nunca el cuerpo y dos casos fallaban como si la función estuviera
 *    rota. Vitest transpila sin comprobar tipos, así que el banco no me protegió: es el mismo
 *    defecto que el `as WorkDecision` del testigo de C-13, con otro disfraz. Un fixture mal
 *    construido acusa al sujeto.
 */
const err = (body: unknown, message = 'http 409') =>
  new ApiError(409, 'conflict', message, undefined, {}, body)

/**
 * ⛔ LA FORMA REAL DEL SOBRE, y este helper existe porque me equivoqué en ella. `WorkErrorBody` es
 *    `{ verdict, code, error: { code, message } }` (types.ts:269-273): el mensaje va ANIDADO.
 *    Mi primera versión lo puso en la raíz **en el código Y en el testigo**, así que los dos
 *    coincidían en el mismo error y los cinco casos pasaban en verde. Lo cazó `tsc -b`, no el
 *    banco: un testigo escrito contra la misma suposición que el código no puede refutarla.
 */
const cuerpo = (code: string, message?: string) => ({
  verdict: 'CONCLUYENTE',
  code,
  error: { code, ...(message === undefined ? {} : { message }) },
})

describe('C-14 · el motivo del motor sobrevive al viaje', () => {
  it('devuelve el `message` del cuerpo tal cual', () => {
    const e = err(
      cuerpo('invalid_transition', 'no se puede pasar de draft a complete'),
    )
    expect(workErrorReason(e)).toBe('no se puede pasar de draft a complete')
  })

  it('codigo y motivo son cosas DISTINTAS y se devuelven por separado', () => {
    const e = err(cuerpo('invalid_transition', 'de draft a complete no'))
    expect(workErrorCode(e)).toBe('invalid_transition')
    expect(workErrorReason(e)).toBe('de draft a complete no')
    expect(workErrorReason(e)).not.toBe(workErrorCode(e))
  })

  it('sin `message` en el cuerpo cae al del error, no a la cadena vacia', () => {
    const e = err(cuerpo('invalid_transition'), 'conflicto al aplicar')
    expect(workErrorReason(e)).toBe('conflicto al aplicar')
  })

  it('un `message` vacio o en blanco NO se muestra: seria un hueco mudo', () => {
    // Un parrafo vacio bajo el titulo es peor que no pintar nada: parece que falta algo.
    expect(workErrorReason(err(cuerpo('x', ''), ''))).toBeNull()
    expect(workErrorReason(err(cuerpo('x', '   '), ''))).toBeNull()
  })

  it('un `message` en la RAIZ no cuenta: el sobre lo lleva ANIDADO', () => {
    // Este caso es el que faltaba. Sin el, codigo y testigo pueden compartir la forma equivocada
    // y pasar los dos: es lo que me ocurrio, y lo cazo el compilador y no el banco.
    const plano = err({ code: 'x', message: 'mensaje en la raiz' }, 'del error')
    expect(workErrorReason(plano)).toBe('del error')
  })

  it('CONTROL: lo que no es un error del motor no inventa motivo', () => {
    // Sin esto, una implementacion que devolviera String(err) pasaria los casos de arriba.
    expect(workErrorReason(new Error('boom'))).toBeNull()
    expect(workErrorReason('boom')).toBeNull()
    expect(workErrorReason(null)).toBeNull()
    expect(workErrorReason(undefined)).toBeNull()
  })
})
