// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-07 — «no se sabe» NUNCA es «no». Las tres preguntas y las dos que a menudo no se pueden
// contestar, fijadas donde se deciden: en las funciones puras.
import { describe, expect, it } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'
import {
  EntitlementMatrix,
  ejeActivado,
  ejeBinario,
  ejeDerecho,
} from './entitlement-matrix'

describe('la composición de los tres ejes', () => {
  /**
   * ⛔ EL CONTROL QUE DA SENTIDO A TODA LA PANTALLA: `LicenseStatus.Features` es `omitempty` y sólo
   * llega **con la licencia verificada** (`core/api/license.go:88,92`). Sin licencia verificada no
   * hay nada que atestigüe nada.
   *
   * EL MUTANTE: tratar `features` ausente como lista vacía —un `?? []` en el contenedor basta— y
   * responder «no». Eso convierte «no lo hemos podido comprobar» en «no tienes derecho», que es
   * una afirmación sobre el producto que alguien usa para comprar, escalar o descartar. En la
   * pantalla que responde «¿qué tengo?» es el peor sitio del producto para confundirlas.
   */
  it('sin Features, el derecho es «no se sabe» y NO «no»', () => {
    expect(ejeDerecho('addon_airs', undefined)).toBe('unknown')
  })

  /**
   * ⛔ Y EL SILENCIO DE UNA LISTA PRESENTE TAMPOCO ES UNA NEGATIVA: `Features` es una lista libre y
   * no está verificada contra el catálogo de add-ons, así que una clave que no aparece puede ser
   * un derecho expresado con otro nombre. Sigue siendo «no se sabe».
   */
  it('con Features presente pero sin la clave, sigue siendo «no se sabe»', () => {
    expect(ejeDerecho('addon_airs', ['addon_reg'])).toBe('unknown')
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: con la clave presente, SÍ hay derecho atestiguado. */
  it('con la clave en Features, hay derecho', () => {
    expect(ejeDerecho('addon_airs', ['addon_reg', 'addon_airs'])).toBe('yes')
  })

  /**
   * ⛔ EL EJE DEL BINARIO ES «no se sabe» POR DISEÑO, y la tentación que rechaza está nombrada en
   * la cabecera: `Preset` dice qué NIVEL introduce cada add-on y sería fácil deducir «community ⇒
   * no está en el binario». Sería inventar la fuente que falta — un preset es empaquetado y un
   * build tag es compilación.
   *
   * EL MUTANTE: derivarlo de la edición. La columna pasaría a afirmar, por módulo, algo que el
   * motor sólo publica por artefacto.
   */
  it('el eje del binario no se deduce de la edición', () => {
    expect(ejeBinario()).toBe('unknown')
  })

  /**
   * Y el eje que SÍ se sabe siempre, con sus cuatro estados: `pending` es «no se sabe» porque el
   * add-on está a la espera de un reinicio — ni encendido ni apagado.
   */
  it('activado distingue los cuatro estados del motor', () => {
    expect(ejeActivado('active')).toBe('yes')
    expect(ejeActivado('pending')).toBe('unknown')
    expect(ejeActivado('available')).toBe('no')
    expect(ejeActivado('console')).toBe('no')
  })
})

describe('la matriz pintada', () => {
  const ADDONS = [
    { key: 'addon_airs', title: 'AIRS', state: 'available' },
    { key: 'addon_reg', title: 'RegOps', state: 'active' },
  ]

  /** Sin licencia verificada la pantalla lo dice ARRIBA, porque condiciona toda la tabla. */
  it('sin licencia verificada avisa de que el derecho no se sabe para todos', () => {
    renderIntel(<EntitlementMatrix addons={ADDONS} edition="community" />)
    expect(screen.getByText(/no verified licence/i)).toBeInTheDocument()
  })

  /** Y con licencia verificada ese aviso NO sale: un aviso permanente deja de leerse. */
  it('con licencia verificada no avisa', () => {
    renderIntel(
      <EntitlementMatrix
        addons={ADDONS}
        features={['addon_reg']}
        edition="enterprise"
      />,
    )
    expect(screen.queryByText(/no verified licence/i)).toBeNull()
  })

  /**
   * ⛔ Y LA COLUMNA QUE NUNCA SE SABE EXPLICA POR QUÉ donde se lee. Sin eso, una columna entera de
   * avisos parece un fallo de carga y el operador recarga la página en vez de entender la
   * respuesta.
   */
  it('explica por qué el eje del binario no se puede contestar', () => {
    renderIntel(<EntitlementMatrix addons={ADDONS} edition="community" />)
    expect(
      screen.getByText(/property of the BINARY, not of a module/i),
    ).toBeInTheDocument()
  })
})
