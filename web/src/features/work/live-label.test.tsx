// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// La cabecera de Work nombra el estado del stream UNA vez.
//
// ⛔ POR QUE EXISTE, y es un defecto MIO cazado por una captura, no por un test. En la muestra del
//    2026-08-26 la cabecera salia literalmente «Live Live»: `LiveDot` ya pinta su propia etiqueta
//    (`features/shared/live-dot.tsx`) y esta vista le pegaba ADEMAS `{t('stream.<status>')}`. En
//    estado `open` las dos claves valen «Live», asi que el operador leia la palabra dos veces.
//
// ⛔ ERA INSTANCIA, NO CLASE: de las SIETE llamadas a `<LiveDot>` del arbol, solo esta doblaba la
//    etiqueta; las otras seis ya se apoyaban en la del componente. Por eso el arreglo es del sitio
//    de llamada y no del componente compartido.
//
// ⛔ Y POR QUE HACIA FALTA ESCRIBIRLO. Los 120 tests de `work` pasaban ANTES y DESPUES del arreglo:
//    ninguno miraba la cabecera. Un arreglo sin testigo se deshace en el siguiente refactor y nadie
//    se entera hasta la proxima captura. La unica assercion de 'Live' que existia
//    (`lease-tab.test.tsx`) es de la pestana de lease, otra pantalla.
//
// ⛔ SE CUENTA, NO SE BUSCA. `findByText` pasaria con DOS coincidencias en algunas versiones y, peor,
//    un `queryByText` daria verde con cero. Aqui se afirma la CARDINALIDAD: exactamente una.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
    confinedWorkspace: null,
  }),
}))

const api = vi.hoisted(() => ({ listWorkItems: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, ...api }
})

const { WorkView } = await import('./work-view')

// Las etiquetas de estado de las DOS familias de claves que colisionaban: `shared:live.*` (la del
// componente) y `work:stream.*` (la que esta vista pintaba encima). Se anclan con ^…$ para no casar
// con `stream.unavailableBody`, que es un parrafo y no una etiqueta.
const ETIQUETAS =
  /^(Live|Connecting…|Reconnecting…|Offline|Not streaming|Stream interrupted)$/

beforeEach(() => {
  vi.clearAllMocks()
  api.listWorkItems.mockResolvedValue({ items: [], has_more: false })
})

describe('la cabecera de Work no dice el estado del stream dos veces', () => {
  it('el estado aparece exactamente una vez', async () => {
    renderIntel(<WorkView />)
    // Control de alcance: si la cabecera dejara de pintar el estado, esto saldria 0 y la celda
    // seria un gate ciego que certifica. Se exige 1, ni 0 ni 2.
    const vistas = await screen.findAllByText(ETIQUETAS)
    expect(
      vistas.length,
      `el estado del stream se pinta ${vistas.length} veces: ${vistas.map((n) => n.textContent).join(' | ')}`,
    ).toBe(1)
  })
})
