// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EL CONTROL DE DESTINOS, EN LA PANTALLA DE QUIEN LO SUFRE.
//
// ⛔ ESTE PANEL NACE DE UNA PREMISA FALSA EN UNA DECISIÓN DE SEGURIDAD.
// `cmd/olivares/cmd_eventing_egress.go:28-34` justifica que la palanca sea de CLI diciendo, como
// hecho, que «the console shows the state and the diff (GET /egress-policy, GET
// /egress-policy/compat)». Medido el 2026-08-20: CERO ficheros de `web/src` mencionaban
// `egress-policy`. Se construyen las dos LECTURAS; la palanca no se toca.
//
// Lo que estos testigos vigilan no es que el panel «pinte»: es que NO FUNDA estados que el motor
// separó a propósito. Cada fusión que aquí se caza cambia el remedio que el operador aplicaría.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import type { ReactElement } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import './i18n'

const { api, authState } = vi.hoisted(() => ({
  api: {
    egressPolicyStatus: vi.fn(),
    egressCompatReport: vi.fn(),
  },
  authState: {
    can: (_p: string): boolean => true,
    activeTenant: 't1' as string | null,
  },
}))

vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  eventingApi: api,
}))

import { EgressCompatReport, EgressPolicyPanel } from './egress-policy-panel'

const FENCE = {
  armed: true,
  mode: 'enforced',
  generation: 4,
  required_capability: 2,
  binary_capability: 2,
}

/** Un despliegue sano: política escrita, modo aplicado, barrera armada. */
const SANO = {
  in_force: true,
  source: 'ops/egress.yaml',
  mode: 'enforced',
  classified_mode: 'enforced',
  enforcement_committed: true,
  writer_fence: FENCE,
}

function wrap(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.can = (_p: string) => true
  api.egressPolicyStatus.mockResolvedValue(SANO)
  api.egressCompatReport.mockResolvedValue({
    seeded: true,
    intact: true,
    subscriptions: 3,
    unparsed: 0,
    authorities: [],
    still_needed: 0,
  })
})

describe('el estado de la política de destinos', () => {
  it('se lo pregunta al motor', async () => {
    wrap(<EgressPolicyPanel canAdmin />)
    expect(await screen.findByText(/policy in force/i)).not.toBeNull()
    expect(api.egressPolicyStatus).toHaveBeenCalled()
  })

  it('⛔ «no la puedo LEER» no se pinta como «no hay ninguna»', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      in_force: false,
      unavailable: true,
    })
    wrap(<EgressPolicyPanel canAdmin />)
    // FIRES IF: alguien colapsa los dos a un booleano. El motor los separa porque los remedios
    // son OPUESTOS: sin política, escribe una; ilegible, mira el host — y mientras tanto las
    // entregas se RE-ENCOLAN en vez de rechazarse (egressapi.go:38-41).
    expect(await screen.findByText(/^Unreadable$/i)).not.toBeNull()
    expect(screen.queryByText(/none authored/i)).toBeNull()
  })

  it('y con `in_force: false` de verdad SÍ dice que no hay ninguna', async () => {
    api.egressPolicyStatus.mockResolvedValue({ ...SANO, in_force: false })
    wrap(<EgressPolicyPanel canAdmin />)
    // El contrafactual del anterior: si «no legible» se pintara SIEMPRE, esta celda lo caza.
    expect(await screen.findByText(/none authored/i)).not.toBeNull()
  })

  it('⛔ un modo que no se ha podido leer no se pinta como el modo', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      mode: '',
      mode_unavailable: true,
    })
    wrap(<EgressPolicyPanel canAdmin />)
    // ⛔ Se afirma sobre LA FILA del modo, no sobre «algún Unknown de la tarjeta»: desde que
    //    `mode_unavailable` arrastra clasificación y compromiso (C2) hay varios, y una aserción
    //    que se contenta con encontrar uno dejaría de distinguir cuál.
    const modo = (await screen.findByText(/^Deployment mode$/i)).parentElement
    expect(modo?.textContent ?? '').toMatch(/Unknown/i)
  })

  it('⛔ una barrera cuyo estado se desconoce no es «inactiva»', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      writer_fence: { ...FENCE, armed: false, unavailable: true },
    })
    wrap(<EgressPolicyPanel canAdmin />)
    // FIRES IF: `unavailable` cae a la rama del `false`. El motor lo dice con todas las letras:
    // «neither armed nor dormant — it is unknown, and it is reported as such rather than
    // resolved to the convenient answer» (egressapi.go:91-94).
    const armada = (await screen.findByText(/^Armed$/i)).parentElement
    expect(armada?.textContent ?? '').toMatch(/Unknown/i)
    expect(screen.queryByText(/^Dormant$/i)).toBeNull()
  })

  it('pinta LAS DOS capacidades, no el veredicto', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      writer_fence: { ...FENCE, required_capability: 3, binary_capability: 1 },
    })
    wrap(<EgressPolicyPanel canAdmin />)
    // «an operator debugging a refusal needs the comparison, not the verdict».
    expect(await screen.findByText('3')).not.toBeNull()
    expect(screen.getByText('1')).not.toBeNull()
  })

  it('⛔ un resumen que NO llega no es un cero', async () => {
    api.egressPolicyStatus.mockResolvedValue(SANO) // sin `compat`
    wrap(<EgressPolicyPanel canAdmin />)
    // FIRES IF: alguien pinta `compat?.recorded ?? 0`. Decir «0» afirma sobre lo que no se ha
    // mirado, y encima tranquiliza.
    //
    // ⛔ Y CON TIER LA PANTALLA NO ATRIBUYE CAUSA. Antes explicaba toda ausencia como falta de
    //    permiso; el servidor también omite el resumen cuando `m.compat == nil`, cuando el
    //    informe falla y cuando la disposición no se pudo leer (`egressapi.go:176-200`). Sin
    //    tier la causa se sabe y se dice; con tier, no.
    expect(
      await screen.findByText(/not provided in this response/i),
    ).not.toBeNull()
    expect(
      screen.queryByText(/not served at your permission level/i),
    ).toBeNull()
    // ⛔ Y QUE NO SE PINTE EL CERO, que era el defecto original: el contraste señaló (M2) que
    //    exigir sólo el mensaje deja pasar un mutante que además enseñe los conteos.
    expect(screen.queryByText(/destinations recorded/i)).toBeNull()
    expect(screen.queryByText(/would stop working/i)).toBeNull()
  })

  it('⛔ avisa cuando el registro de compatibilidad ha perdido miembros', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      compat: {
        seeded: true,
        intact: false,
        recorded: 7,
        still_needed: 2,
        unparsable: 0,
      },
    })
    wrap(<EgressPolicyPanel canAdmin />)
    const aviso = await screen.findByRole('alert')
    expect(aviso.textContent ?? '').toMatch(/no longer intact/i)
  })

  it('y NO avisa cuando está íntegro', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      compat: {
        seeded: true,
        intact: true,
        recorded: 7,
        still_needed: 0,
        unparsable: 0,
      },
    })
    wrap(<EgressPolicyPanel canAdmin />)
    await screen.findByText(/policy in force/i)
    // El aviso es CONDICIONAL. Si se pintara siempre dejaría de significar nada.
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('⛔ dice que no ha podido mirar en vez de desaparecer', async () => {
    api.egressPolicyStatus.mockRejectedValue(new Error('503'))
    wrap(<EgressPolicyPanel canAdmin />)
    // FIRES IF: alguien devuelve `null` al fallar. Un panel que se esconde enseña lo mismo que
    // enseñaba antes de existir, y encima parece que no hay nada que ver.
    const aviso = await screen.findByRole('alert')
    expect(aviso.textContent ?? '').toMatch(/could not be read/i)
  })
})

describe('el informe detallado, que es de tier ADMIN', () => {
  it('⛔ NO se le pide al motor sin el permiso', async () => {
    const { container } = wrap(<EgressCompatReport canAdmin={false} />)
    await Promise.resolve()
    // FIRES IF: alguien quita el `enabled`. No filtra nada —el servidor deniega— pero le enseña
    // al operador un error que no es suyo, en la pantalla cuyo trabajo es distinguir la regla
    // de un fallo propio.
    expect(api.egressCompatReport).not.toHaveBeenCalled()
    expect(container.textContent).toBe('')
  })

  it('y SÍ con el permiso', async () => {
    wrap(<EgressCompatReport canAdmin />)
    expect(
      await screen.findByText(/what enforcing would break/i),
    ).not.toBeNull()
    expect(api.egressCompatReport).toHaveBeenCalled()
  })

  it('⛔ pone PRIMERO lo que deja de funcionar', async () => {
    api.egressCompatReport.mockResolvedValue({
      seeded: true,
      intact: true,
      subscriptions: 3,
      unparsed: 0,
      still_needed: 1,
      authorities: [
        {
          authority: 'a.example',
          kind: 'https',
          subscriptions: 2,
          covered: true,
        },
        {
          authority: 'b.example',
          kind: 'https',
          subscriptions: 1,
          covered: false,
        },
      ],
    })
    wrap(<EgressCompatReport canAdmin />)
    await screen.findByText('b.example')
    const filas = screen.getAllByRole('row').slice(1) // sin la cabecera
    // Lo accionable es el complemento de `covered`: `covered` significa «sobrevive por sus
    // propios méritos», o sea lo que NO se rompe. Un orden que lo ponga arriba entierra la
    // lista que el operador ha venido a leer.
    expect(filas[0]?.textContent ?? '').toMatch(/b\.example/)
  })

  it('⛔ un registro sin trazar no describe nada, y lo dice', async () => {
    api.egressCompatReport.mockResolvedValue({
      seeded: false,
      intact: true,
      subscriptions: 0,
      unparsed: 0,
      authorities: [],
      still_needed: 0,
    })
    wrap(<EgressCompatReport canAdmin />)
    const aviso = await screen.findByRole('alert')
    // FIRES IF: se pintan los ceros sin el aviso. «A decision approved against it would be
    // approved against an unknown» (egressrollout.go:720-722): los ceros de un informe no
    // sembrado se leen como «no rompe nada», que es la conclusión contraria a la verdadera.
    expect(aviso.textContent ?? '').toMatch(/never drawn/i)
  })
})

// ─────────────────────────────────────────────────────────────────────────────────────
// EL ANCLA QUE MANTIENE CIERTA UNA FRASE DE OTRO FICHERO.
//
// ⛔ ESTE BLOQUE NO PRUEBA UN COMPORTAMIENTO: PRUEBA UNA PREMISA. `cmd_eventing_egress.go` afirma
// como hecho que la consola enseña estas dos lecturas, y sobre esa frase se apoya la decisión de
// que la palanca viva sólo en el CLI. Hoy es cierta porque este panel existe y la vista lo monta.
// Si alguien lo desmonta, la frase vuelve a ser falsa EN SILENCIO y una decisión de seguridad se
// queda apoyada en nada — que es exactamente el estado que este trabajo vino a corregir, y que no
// se detectó en meses porque nada lo vigilaba.
//
// Va en las DOS direcciones a propósito: también lee el fichero Go. Si alguien retira la
// afirmación de allí, este ancla deja de tener a quién sostener y el testigo lo dice en vez de
// seguir vigilando un contrato que ya no existe.
//
// Se mide sobre el FUENTE de la vista, no montándola: montar `EventingView` probaría el render
// —que ya tiene su propio fichero— y no el enganche, que es lo que aquí importa.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const REPO = resolve(__dirname, '../../../..')
const leer = (rel: string) => readFileSync(resolve(REPO, rel), 'utf8')

describe('la premisa que `cmd_eventing_egress.go` da por hecha', () => {
  it('el CLI sigue afirmando que la consola enseña las dos lecturas', () => {
    const cli = leer('cmd/olivares/cmd_eventing_egress.go')
    // Control positivo del instrumento: sin esto, un fichero vacío o mal resuelto haría pasar
    // el resto por ausencia en vez de por acuerdo.
    expect(cli.length).toBeGreaterThan(1000)
    // FIRES IF: la frase se retira o se reescribe. Entonces este ancla ya no sostiene nada y
    // hay que decidir a conciencia si se retira también, en vez de dejarlo vigilando un
    // contrato muerto.
    // ⛔ LÍMITE DECLARADO (M10): vigila una CADENA, no su significado. Una negación que
    //    conserve la subcadena pasaría y una reformulación verdadera fallaría; se compensa con
    //    lo mínimo que un léxico puede.
    expect(cli).toMatch(/The console shows the state and the diff/)
    expect(cli).not.toMatch(
      /(no longer|does not|never) shows the state and the diff/i,
    )
  })

  it('⛔ y la vista de eventing MONTA esas dos lecturas', () => {
    const vista = leer('web/src/features/eventing/eventing-view.tsx')
    expect(vista.length).toBeGreaterThan(1000)
    // El ancla vigila que la vista lo MONTE, no con qué props: `\b` para que añadir una
    // prop no la rompa. (La rompió al añadir `canAdmin`, que es señal de que vigila algo.)
    expect(vista).toMatch(/<EgressPolicyPanel\b/)
    expect(vista).toMatch(/<EgressCompatReport\b/)
    // ⛔ LÍMITE DECLARADO (el contraste lo señaló, M9): esto es LÉXICO. Un
    //    `{false && <EgressPolicyPanel />}`, código muerto o el literal dentro de un comentario
    //    pasarían sin montar nada. Se estrecha lo estrechable y lo demás se dice en vez de
    //    fingirlo — la prueba de montaje REAL es el fichero de tests de la vista, no ésta.
    expect(vista).not.toMatch(/false\s*&&\s*<EgressPolicyPanel/)
    expect(vista).not.toMatch(/\/\/[^\n]*<EgressPolicyPanel/)
  })

  it('⛔ la vista pasa el PERMISO REAL, no un literal', () => {
    const vista = leer('web/src/features/eventing/eventing-view.tsx')
    // ⛔ LO PIDIÓ EL CONTRASTE (M7) Y AHORA PESA MÁS QUE CUANDO LO PIDIÓ: desde que el resumen de
    //    compatibilidad también depende de `canAdmin`, un `canAdmin={true}` cableado en la vista
    //    abriría por la puerta de al lado lo que el panel cierra. Los tests del componente
    //    prueban las dos ramas y no pueden ver con qué se le llama.
    expect(vista).toMatch(/canAdmin=\{canAdmin\}/)
    expect(vista).toMatch(
      /const canAdmin = can\('eventing:subscription:admin'\)/,
    )
    expect(vista).not.toMatch(/canAdmin=\{true\}/)
  })

  it('⛔ la consola NO ofrece la palanca', () => {
    const panel = leer('web/src/features/eventing/egress-policy-panel.tsx')
    const api = leer('web/src/features/eventing/api.ts')
    // FIRES IF: alguien añade la actuación por HTTP. El razonamiento de seguridad que este panel
    // deja EN PIE es justamente que la palanca no es alcanzable por ahí; construir la lectura no
    // autoriza a construir la escritura, y la tentación es evidente teniendo el estado delante.
    expect(api).not.toMatch(/egress-policy\/(actuate|commit|mode)/)
    expect(api).not.toMatch(/http\.(post|put|patch|delete)[^\n]*egress-policy/)
    expect(panel).not.toMatch(/useMutation/)
  })
})

// ─────────────────────────────────────────────────────────────────────────────────────
// LO QUE EL CONTRASTE CODEX `sol max` ENCONTRÓ, Y QUE NINGÚN TEST MÍO PODÍA VER.
//
// ⛔ EL SERVIDOR ACOTA LA RESPUESTA; LA CACHÉ NO ACOTA NADA. `compat` se sirve desde una ruta
// READ **sólo** si el principal además pasa `permSubAdmin` (`egressapi.go:67-74`, `:183-200`), y
// yo di por hecho que con eso bastaba: «el panel pinta lo que llega». Pero la clave de la consulta
// es `['eventing', tenant, 'egress-policy']` — **sin principal ni tier**. Tras un paso de admin a
// sólo-lectura sobre el MISMO `QueryClient` y tenant, la respuesta admin cacheada tiene la misma
// clave que la que el servidor le negaría al llamante nuevo.
//
// ⇒ **Una frontera de permiso que sólo vive en el servidor no sobrevive a una caché indexada por
// tenant.** El informe itemizado ya se defendía así; el resumen no, y era el mismo dato.
describe('la frontera de permiso, que la caché no conserva', () => {
  it('⛔ NO pinta el resumen de compatibilidad sin el tier, aunque venga en la respuesta', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      // Exactamente lo que quedaría en caché de una sesión admin anterior.
      compat: {
        seeded: true,
        intact: true,
        recorded: 7,
        still_needed: 2,
        unparsable: 0,
      },
    })
    wrap(<EgressPolicyPanel canAdmin={false} />)
    await screen.findByText(/policy in force/i)
    // FIRES IF: alguien vuelve a pintar `s.compat` a secas. Los conteos son un oráculo de
    // pertenencia —lo dice el motor de sí mismo— y aquí llegarían por una caché que no sabe a
    // quién sirve.
    expect(
      screen.queryByText(/not served at your permission level/i),
    ).not.toBeNull()
    // ⛔ Se afirma sobre las ETIQUETAS del resumen, no sobre los números: `2` también es la
    //    capacidad exigida de la barrera y `queryByText('2')` encontraba dos elementos. Un
    //    testigo que casa con otra fila de la misma tarjeta no mide la que dice medir.
    expect(screen.queryByText(/destinations recorded/i)).toBeNull()
    expect(screen.queryByText(/would stop working/i)).toBeNull()
  })

  it('y SÍ lo pinta con el tier — el contrafactual', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      compat: {
        seeded: true,
        intact: true,
        recorded: 7,
        still_needed: 2,
        unparsable: 0,
      },
    })
    wrap(<EgressPolicyPanel canAdmin />)
    // Si `canAdmin` se colara en la condición general, el operador con permiso dejaría de ver lo
    // que sí le corresponde: sobrecorregir tiene su propio testigo.
    expect(await screen.findByText(/destinations recorded/i)).not.toBeNull()
    expect(screen.queryByText(/would stop working/i)).not.toBeNull()
    expect(
      screen.queryByText(/not served at your permission level/i),
    ).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────────────────
// LOS TRES «MEDIOS» DEL CONTRASTE, Y LOS TRES SON LA MISMA CLASE QUE ESTE PANEL VINO A CERRAR:
// una tercera respuesta aplicada a UN campo y no a los que la acompañan.
describe('lo desconocido no se detiene en el campo que lo declara', () => {
  it('⛔ C2 · `mode_unavailable` arrastra clasificación Y compromiso', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      mode: '',
      mode_unavailable: true,
      classified_mode: '',
      enforcement_committed: false, // el cero de Go, no una decisión
    })
    wrap(<EgressPolicyPanel canAdmin />)
    await screen.findByText(/policy in force/i)
    // FIRES IF: alguien deja «Not committed» cuando la disposición no se pudo leer. Es una
    // CONCLUSIÓN sobre lo no leído, y la que tranquiliza: dice que nadie ha cerrado la puerta.
    expect(screen.queryByText(/^Not committed$/i)).toBeNull()
    expect(screen.getAllByText(/^Unknown$/i).length).toBeGreaterThanOrEqual(2)
  })

  it('y con la disposición legible SÍ afirma — el contrafactual', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      enforcement_committed: false,
    })
    wrap(<EgressPolicyPanel canAdmin />)
    // Si «desconocido» se pintara siempre, el operador perdería la afirmación que sí es cierta.
    expect(await screen.findByText(/^Not committed$/i)).not.toBeNull()
  })

  it('⛔ C3 · una barrera ilegible no publica un requisito CERO', async () => {
    api.egressPolicyStatus.mockResolvedValue({
      ...SANO,
      writer_fence: {
        armed: false,
        unavailable: true,
        required_capability: 0, // el cero de Go
        binary_capability: 2, // el único lado que el servidor sí conoce
      },
    })
    wrap(<EgressPolicyPanel canAdmin />)
    await screen.findByText(/required capability/i)
    // FIRES IF: se pinta el 0 como requisito y se calcula la suficiencia contra él —
    // «este binario cumple de sobra» sobre un requisito que nadie leyó.
    const req = screen.getByText(/^Required capability$/i).parentElement
    expect(req?.textContent ?? '').toMatch(/Unknown/i)
    expect(req?.textContent ?? '').not.toMatch(/0/)
    // el lado local sigue visible, porque ése sí se conoce
    expect(
      screen.getByText(/^This binary$/i).parentElement?.textContent ?? '',
    ).toMatch(/2/)
  })

  it('⛔ C4 · un registro SIN TRAZAR no enseña sus ceros', async () => {
    api.egressCompatReport.mockResolvedValue({
      seeded: false,
      intact: true,
      subscriptions: 0,
      unparsed: 0,
      authorities: [],
      still_needed: 0,
    })
    wrap(<EgressCompatReport canAdmin />)
    const aviso = await screen.findByRole('alert')
    expect(aviso.textContent ?? '').toMatch(/never drawn/i)
    // FIRES IF: se pintan los contadores tras el aviso. Go dice que con `seeded=false` el resto
    // «describes nothing»; un `0` ahí colapsa «no se midió» con «se midió y dio cero», que es la
    // distinción entera de este panel.
    expect(screen.queryByText(/subscriptions at the line/i)).toBeNull()
    expect(screen.queryByText(/no legacy destination/i)).toBeNull()
  })

  it('y con el registro trazado SÍ los enseña — el contrafactual', async () => {
    api.egressCompatReport.mockResolvedValue({
      seeded: true,
      intact: true,
      subscriptions: 3,
      unparsed: 0,
      authorities: [],
      still_needed: 0,
    })
    wrap(<EgressCompatReport canAdmin />)
    expect(await screen.findByText(/subscriptions at the line/i)).not.toBeNull()
    expect(screen.queryByText(/no legacy destination/i)).not.toBeNull()
  })
})

describe('el arnés de accesibilidad renderiza la jerarquía real de egress', () => {
  it('⛔ cuelga los subtítulos del h2 del panel sin saltar a h4', async () => {
    wrap(<EgressPolicyPanel canAdmin />)

    expect(
      await screen.findByRole('heading', { level: 3, name: /writer fence/i }),
    ).not.toBeNull()
    expect(
      screen.getByRole('heading', { level: 3, name: /compatibility/i }),
    ).not.toBeNull()
    expect(screen.queryAllByRole('heading', { level: 4 })).toEqual([])
  })
})
