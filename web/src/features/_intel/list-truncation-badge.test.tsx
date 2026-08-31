// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ AQUÍ VIVE LA REGLA, ASÍ QUE AQUÍ VIVE SU TESTIGO. `has_more && !error` la escribí a mano doce
// veces en cuatro features antes de extraerla, y cada copia necesitaba su propia casilla; ésta la
// prueba UNA vez, en las cuatro combinaciones, para que ninguna pantalla tenga que volver a
// demostrarla.
//
// Las dos mitades cuestan, y en direcciones contrarias:
//   · sin `has_more`, el aviso sale SIEMPRE y declara un recorte que no existe;
//   · sin `!error`, se queda flotando sobre una pantalla que ya sólo enseña el fallo — y ése no es
//     un caso teórico: react-query CONSERVA el último dato bueno mientras marca el error.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ListTruncationBadge } from './notices'
import './i18n'

const AVISO = 'Loaded 3 rows; there are more'

function pinta(query: { data?: unknown; error?: unknown }) {
  return render(
    <ListTruncationBadge query={query} label={AVISO} hint="por qué" />,
  )
}

describe('ListTruncationBadge — las cuatro combinaciones', () => {
  it('sale cuando hay más y la lectura fue bien', () => {
    pinta({ data: { has_more: true }, error: null })
    expect(screen.getByText(AVISO)).toBeVisible()
  })

  it('NO sale cuando el motor dice que no hay más', () => {
    pinta({ data: { has_more: false }, error: null })
    expect(screen.queryByText(AVISO)).toBeNull()
  })

  /** El caso que motivó la guarda: dato viejo con `has_more` Y un error encima. */
  it('NO sale cuando hay error, aunque el dato viejo diga que hay más', () => {
    pinta({ data: { has_more: true }, error: new Error('boom') })
    expect(screen.queryByText(AVISO)).toBeNull()
  })

  it('NO sale sin dato ninguno', () => {
    pinta({ data: undefined, error: null })
    expect(screen.queryByText(AVISO)).toBeNull()
  })

  /**
   * ⛔ Y UN `has_more` QUE NO SEA EL BOOLEANO `true` NO ENCIENDE NADA. El tipo de `data` es
   * `unknown` con un cast dentro —una aserción de TypeScript, no una comprobación en ejecución—,
   * así que una cadena `"false"` del transporte sería truthy y pintaría el aviso. La comprobación
   * es `=== true` y esta casilla es la que lo fija; sin ella, el mutante que vuelve al truthy
   * ESCAPA. Lo señaló el contraste externo.
   */
  it('un has_more truthy que no es true NO enciende el aviso', () => {
    pinta({ data: { has_more: 'false' }, error: null })
    expect(screen.queryByText(AVISO)).toBeNull()
  })

  /** El texto del `hint` viaja al atributo que lo enseña; si no, el aviso es un titular sin razón. */
  it('el porqué viaja en el title', () => {
    pinta({ data: { has_more: true }, error: null })
    expect(screen.getByText(AVISO)).toHaveAttribute('title', 'por qué')
  })
})

/**
 * ⛔ LA PROP `filas`, Y LA DIRECCION QUE DE VERDAD IMPORTA ES LA TERCERA. Se añadio porque con
 *    `{items: [], has_more: true}` el aviso se pintaba ENCIMA del estado vacio —«No active
 *    members» y «Loaded 0 …; there are more» a la vez, un mensaje que se contradice solo
 *    (contraste F-04)—. Va OPCIONAL a proposito: este componente tiene ~100 llamantes en
 *    `web/src` y no todos pasan un sobre con `items`, asi que exigirlo dentro habria apagado
 *    avisos ajenos EN SILENCIO. El tercer caso es el que vigila esa promesa.
 */
describe('ListTruncationBadge — la guarda de filas', () => {
  it('con filas 0 NO sale, aunque el motor diga que hay más', () => {
    render(
      <ListTruncationBadge
        query={{ data: { has_more: true }, error: null }}
        label={AVISO}
        hint="por qué"
        filas={0}
      />,
    )
    expect(screen.queryByText(AVISO)).toBeNull()
  })

  it('con filas > 0 sale como siempre', () => {
    render(
      <ListTruncationBadge
        query={{ data: { has_more: true }, error: null }}
        label={AVISO}
        hint="por qué"
        filas={3}
      />,
    )
    expect(screen.getByText(AVISO)).toBeVisible()
  })

  it('⛔ SIN la prop se comporta igual que antes — los llamantes que no la pasan no cambian', () => {
    pinta({ data: { has_more: true }, error: null })
    expect(screen.getByText(AVISO)).toBeVisible()
  })
})
