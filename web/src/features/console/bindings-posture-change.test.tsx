// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
//
// LA POSTURA QUE SE VA A CAMBIAR, EN LA PANTALLA QUE LA APRUEBA.
//
// ⛔ ESTA COLA ES LA MITAD DE UN DOBLE CONTROL. `public_only` RELAJA la guarda y por eso el motor
// exige un segundo par de ojos (`modules/sourcescope/api.go:105-109`). Medido el 2026-08-20, el
// aprobador veía Fuente · Motivo · Proponente · Estado y NADA MÁS: `guard_profile` no aparecía en
// un solo `.tsx` del árbol. Un doble control cuyo segundo revisor no ve el estado que gobierna
// aprueba a ciegas justo en la dirección que abre.
//
// Los tres estados de la celda no son ceremonia. `acl_aware` llega por DOS caminos que significan
// cosas distintas —hay fila guardada, o no hay ninguna y `defaultGuardPosture` lo aplica
// (`modules/sourcescope/guardposture.go:37-39`)— y un tercero, «no he podido mirar», que jamás
// puede pintarse como si fuera el primero.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState, navigate } = vi.hoisted(() => ({
  api: {
    listBindings: vi.fn(),
    listPostureRequests: vi.fn(),
    listGuardPostures: vi.fn(),
    listWorkspaces: vi.fn(),
    listAgentGroups: vi.fn(),
    listGroups: vi.fn(),
    rbacCatalog: vi.fn(),
    listRoles: vi.fn(),
    listAgents: vi.fn(),
    listResources: vi.fn(),
    resolvePreview: vi.fn(),
  },
  authState: {
    activeTenant: 't1' as string | null,
    activeRole: 'owner' as string | null,
    isSuperadmin: true,
    principal: { aal: 3 } as { aal?: number } | null,
    can: (_p: string): boolean => true,
  },
  navigate: vi.fn(),
}))

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => navigate }))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('@/features/identity/assurance', () => ({
  AAL: { PASSWORD: 1, MFA: 2, HARDWARE: 3 },
  RequireAssurance: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, consoleApi: api }
})

import { BindingsTab } from './bindings-tab'

const emptyList = { items: [], has_more: false }

// La petición que se está revisando: relajar `kb-prod` a `public_only`.
const RELAJACION = {
  id: 'pr-1',
  source_type: 'mcp' as const,
  source_ref: 'kb-prod',
  op: 'set_guard_posture',
  reason: 'el equipo de soporte necesita el catálogo',
  proposer: 'usr-9',
  status: 'pending' as const,
  guard_profile: 'public_only',
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  api.listBindings.mockResolvedValue(emptyList)
  api.listPostureRequests.mockResolvedValue({
    items: [RELAJACION],
    has_more: false,
  })
  api.listGuardPostures.mockResolvedValue(emptyList)
  api.listWorkspaces.mockResolvedValue(emptyList)
  api.listAgentGroups.mockResolvedValue(emptyList)
  api.listGroups.mockResolvedValue(emptyList)
  api.listRoles.mockResolvedValue(emptyList)
  api.listAgents.mockResolvedValue(emptyList)
  api.listResources.mockResolvedValue(emptyList)
  api.rbacCatalog.mockResolvedValue({ permissions: [], roles: [] })
})

/** El texto de la fila de la petición, sin depender del orden de las columnas. */
async function celdaPostura(): Promise<string> {
  const fuente = await screen.findByText('mcp:kb-prod')
  const fila = fuente.closest('tr')
  if (!fila) throw new Error('la fuente no está en una fila')
  return fila.textContent ?? ''
}

describe('la postura que la petición cambiaría', () => {
  it('le pregunta al motor por las posturas guardadas', async () => {
    wrap(<BindingsTab />)
    await celdaPostura()
    // FIRES IF: alguien retira la consulta y pinta la columna desde la propia petición —
    // se vería el destino sin el origen, que es la mitad que no informa.
    //
    // ⛔ Y NO CUBRE MÁS QUE ESO, medido: un mutante que CONSERVA la llamada y tira los datos
    // (índice vacío constante) deja verde esta celda. Lo caza «pinta la postura ACTUAL», que
    // es donde debe cazarse. Una aserción de llamada prueba que se preguntó, nunca que se usó.
    expect(api.listGuardPostures).toHaveBeenCalled()
  })

  it('pinta el perfil PROPUESTO por la petición', async () => {
    wrap(<BindingsTab />)
    expect(await celdaPostura()).toMatch(/public only/i)
  })

  it('pinta la postura ACTUAL cuando hay fila guardada', async () => {
    api.listGuardPostures.mockResolvedValue({
      items: [
        {
          source_type: 'mcp',
          source_ref: 'kb-prod',
          profile: 'acl_aware',
          updated_by: 'usr-1',
        },
      ],
      has_more: false,
    })
    wrap(<BindingsTab />)
    const texto = await celdaPostura()
    expect(texto).toMatch(/acl-aware/i)
    // ⛔ CONTRAFACTUAL: con fila guardada NO se dice «por defecto». Si se dijera siempre,
    // la pantalla llamaría ausencia a una decisión que alguien tomó y firmó.
    expect(texto).not.toMatch(/\(default\)/i)
  })

  it('⛔ dice POR DEFECTO cuando la fuente no tiene fila', async () => {
    api.listGuardPostures.mockResolvedValue(emptyList)
    wrap(<BindingsTab />)
    const texto = await celdaPostura()
    // FIRES IF: alguien pinta `acl_aware` a secas para la ausencia. Es el mismo perfil por un
    // camino distinto: nadie lo eligió, lo aplica `defaultGuardPosture`.
    expect(texto).toMatch(/acl-aware \(default\)/i)
  })

  it('⛔ dice que NO SE HA CARGADO en vez de inventarse el defecto', async () => {
    api.listGuardPostures.mockRejectedValue(new Error('503'))
    wrap(<BindingsTab />)
    const texto = await celdaPostura()
    // FIRES IF: el fallo de la consulta cae en la rama del defecto. El aprobador leería
    // «acl-aware» —una afirmación sobre el estado— cuando no hemos podido mirarlo.
    expect(texto).toMatch(/not loaded/i)
    expect(texto).not.toMatch(/acl-aware/i)
  })
})

// ─────────────────────────────────────────────────────────────────────────────────────
// LO QUE EL CONTRASTE CODEX `sol max` DEJÓ VIVO, Y LO QUE ARREGLARLO ENSEÑÓ.
//
// El informe (the Codex posture contrast of 2026-08-20) reprodujo DOS mutantes
// que mi batería dejaba en verde, y tenía razón en los dos:
//
//   VALUE      get(key) -> has(key) ? 'acl_aware' : undefined      ... 5/5 pasaban
//   DIRECTION  pintar PROPUESTA antes que ACTUAL                    ... 5/5 pasaban
//
// El VALUE sobrevivía por una razón que me interesa dejar escrita: **mi fixture guardaba
// precisamente `acl_aware`**, que es el valor que el mutante inventa. Un testigo cuyo dato de
// prueba coincide con la constante que el defecto usaría no puede distinguir uno de otro. Y no fue
// mala suerte: elegí `acl_aware` porque es el defecto del motor, o sea **por la misma razón que lo
// hace inútil como testigo**.
//
// El DIRECTION sobrevivía porque mi ayuda `celdaPostura()` lee el texto de la FILA entera y lo
// declara: «sin depender del orden de las columnas». Era cierto y era el problema — la dirección
// ACTUAL → PROPUESTA es la mitad del significado del cambio.
//
// ⛔ Y EL FIXTURE NO ERA EL PAYLOAD DEL MOTOR. La guarda sólo existe para `source_type:
// knowledge` (`modules/sourcescope/guardposture.go:67-88`) y la operación se registra como
// `public_only` (`modules/sourcescope/posture.go:123-125`). Yo probaba con `mcp` y
// `set_guard_posture`: ramas correctas sobre un caso que el motor no produce.
const CANONICA = {
  id: 'pr-2',
  source_type: 'knowledge' as const,
  source_ref: 'kb-prod',
  op: 'public_only',
  reason: 'el equipo de soporte necesita el catálogo',
  proposer: 'usr-9',
  status: 'pending' as const,
  guard_profile: 'public_only',
}

/** La celda de postura de la fila, aislada — no el texto de toda la fila. */
async function celda(): Promise<HTMLElement> {
  // ⛔ RENDERIZA AQUÍ, no en el `beforeEach`: cada caso fija sus mocks ANTES de montar, y montar
  //    en el beforeEach los congelaría con el fixture por defecto. La primera versión de esta
  //    ayuda no montaba nada y los seis casos fallaron contra un <body /> vacío.
  wrap(<BindingsTab />)
  const fuente = await screen.findByText('knowledge:kb-prod')
  const fila = fuente.closest('tr')
  if (!fila) throw new Error('la fuente no está en una fila')
  const c = fila.querySelector('td:nth-child(2)')
  if (!c) throw new Error('no encuentro la celda de postura')
  return c as HTMLElement
}

describe('lo que el contraste dejó vivo', () => {
  beforeEach(() => {
    api.listPostureRequests.mockResolvedValue({
      items: [CANONICA],
      has_more: false,
    })
  })

  it('⛔ VALUE · pinta el valor GUARDADO, no una constante', async () => {
    api.listGuardPostures.mockResolvedValue({
      items: [
        {
          source_type: 'knowledge',
          source_ref: 'kb-prod',
          // El único perfil que el escritor PERSISTE: al volver a `acl_aware` borra la fila
          // (`guardposture.go:219-225`). Y es distinto del que el mutante VALUE inventaría.
          profile: 'public_only',
          updated_by: 'usr-1',
        },
      ],
      has_more: false,
    })
    const c = await celda()
    const txt = c.textContent ?? ''
    // FIRES IF: alguien conserva la EXISTENCIA de la fila y tira su valor.
    //
    // ⛔ Y LA ASERCIÓN QUE VALE ES LA NEGATIVA. La primera versión de este caso comprobaba
    //    `toMatch(/Public only/i)` y **el mutante seguía escapando**, porque la PROPUESTA
    //    también es `public_only`: la etiqueta aparecía en la celda por el otro lado. Un
    //    testigo cuya afirmación positiva la puede satisfacer otro campo de la misma celda no
    //    distingue nada. Lo que sólo puede aparecer si el valor se descartó es `ACL-aware`.
    expect(txt).not.toMatch(/ACL-aware/i)
    expect(txt).not.toMatch(/\(default\)/i)
    // y el positivo se ancla al lado ACTUAL, antes de la flecha
    expect(txt.slice(0, txt.indexOf('→'))).toMatch(/Public only/i)
  })

  it('⛔ DIRECTION · la secuencia es ACTUAL, flecha, PROPUESTA', async () => {
    api.listGuardPostures.mockResolvedValue(emptyList)
    const c = await celda()
    const txt = c.textContent ?? ''
    const iActual = txt.search(/ACL-aware \(default\)/i)
    const iFlecha = txt.indexOf('→')
    const iProp = txt.search(/Public only/i)
    // FIRES IF: se invierte el orden o se retira la flecha. «De qué a qué» es la mitad del
    // significado: una relajación leída al revés parece un endurecimiento.
    expect(iActual).toBeGreaterThanOrEqual(0)
    expect(iFlecha).toBeGreaterThan(iActual)
    expect(iProp).toBeGreaterThan(iFlecha)
  })

  it('⛔ PAIR · cada fila lee SU par, no la primera de la lista', async () => {
    api.listPostureRequests.mockResolvedValue({
      items: [CANONICA, { ...CANONICA, id: 'pr-3', source_ref: 'kb-docs' }],
      has_more: false,
    })
    api.listGuardPostures.mockResolvedValue({
      // En orden INVERSO al de las peticiones, y con una fila ajena por delante.
      items: [
        {
          source_type: 'knowledge',
          source_ref: 'kb-otro',
          profile: 'public_only',
        },
        {
          source_type: 'knowledge',
          source_ref: 'kb-docs',
          profile: 'public_only',
        },
      ],
      has_more: false,
    })
    const cProd = await celda()
    const docs = await screen.findByText('knowledge:kb-docs')
    const cDocs = docs.closest('tr')?.querySelector('td:nth-child(2)')
    // `kb-prod` NO tiene fila -> por defecto. `kb-docs` SÍ -> su valor.
    expect(cProd.textContent ?? '').toMatch(/\(default\)/i)
    expect(cDocs?.textContent ?? '').toMatch(/Public only/i)
    expect(cDocs?.textContent ?? '').not.toMatch(/\(default\)/i)
  })

  it('⛔ PAGE · una respuesta CORRECTA pero incompleta no autoriza a decir «por defecto»', async () => {
    api.listGuardPostures.mockResolvedValue({
      // La fuente buscada NO está… y hay más páginas. Podría estar en la siguiente.
      items: [
        {
          source_type: 'knowledge',
          source_ref: 'kb-otro',
          profile: 'public_only',
        },
      ],
      has_more: true,
    })
    const c = await celda()
    // FIRES IF: se trata `has_more` como si no existiera. El handler hace UNA llamada a
    // `repo.List` sin drenar el cursor (`guardposture.go:112-121`) y el repositorio pagina a 100
    // (`sqlstore/generic.go:28`): a partir de 101 overrides, una fila de la página 2 se pintaría
    // como ausencia — un HECHO FALSO, que es peor que no pintar nada.
    expect(c.textContent ?? '').toMatch(/Not loaded/i)
    expect(c.textContent ?? '').not.toMatch(/\(default\)/i)
  })

  it('⛔ NO-FIRE · una fila PRESENTE sigue siendo autoritativa aunque la página esté truncada', async () => {
    api.listGuardPostures.mockResolvedValue({
      items: [
        {
          source_type: 'knowledge',
          source_ref: 'kb-prod',
          profile: 'public_only',
        },
      ],
      has_more: true,
    })
    const c = await celda()
    // ⛔ LA DIRECCIÓN DE NO DISPARO, y es la mitad que casi nunca se escribe. La paginación sólo
    // puede fabricar AUSENCIAS falsas, jamás un valor falso. Meter `incomplete` en la condición
    // general ocultaría valores buenos: sería sobrecorregir el defecto que se acaba de arreglar.
    expect(c.textContent ?? '').toMatch(/Public only/i)
    expect(c.textContent ?? '').not.toMatch(/Not loaded/i)
  })

  it('NO-FIRE · una página COMPLETA y vacía sí significa «por defecto»', async () => {
    api.listGuardPostures.mockResolvedValue(emptyList) // has_more: false
    const c = await celda()
    // El contrafactual del PAGE: sin más páginas, la ausencia es un hecho y se afirma.
    expect(c.textContent ?? '').toMatch(/ACL-aware \(default\)/i)
    expect(c.textContent ?? '').not.toMatch(/Not loaded/i)
  })
})
