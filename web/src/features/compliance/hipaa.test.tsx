// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// el informe de brechas de las salvaguardas TÉCNICAS de HIPAA (45 CFR §164.312).
//
// ⛔ LA TRAMPA QUE ESTE FICHERO EXISTE PARA CERRAR NO ES «falta una pantalla»: es que la consola
// YA listaba un framework llamado HIPAA y no era éste. Medido el 2026-08-20:
//
//   'hipaa' en web/src ...................... 13 ficheros (platforms/ y un string de test)
//   'gap-report' o 'gapReport' en web/src ...  0
//   control positivo 'frameworks' ........... 37 ficheros
//   control negativo (término inventado) ....  0
//
//   hipaa_clinical_ai          «HIPAA Clinical AI Overlay», en el catálogo genérico
//                              (frameworks.go:2136) -> alcanzable por /frameworks/{id}
//   hipaa_technical_safeguards «HIPAA Security Rule — Technical Safeguards», 45 CFR §164.312
//                              -> `hipaaTechnicalFramework()` se usa en UN sitio (hipaa.go:59)
//                              y NO está en `catalog`: la ruta dedicada es la única vía.
//
// NO SE MOCKEA `./api`, y por la misma razón que el fichero hermano de NIS 2: una celda que
// afirma «se llamó a la mutationFn» ha verificado que la consola está de acuerdo consigo misma.
// Aquí el hallazgo ENTERO es «nadie llamaba a esta URL», así que lo que se afirma es la URL que
// el cliente le entrega a `fetch`.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen } from '@/test/intel'
import '@/features/_intel'
import { complianceApi } from './api'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH }),
}))

const { HipaaTab } = await import('./hipaa-view')
await import('./i18n')

const DESCARGO =
  'HIPAA Security Rule technical-safeguards gap report for 45 CFR §164.312(a)-(e). Technical mapping only; NOT a HIPAA compliance certification and NOT legal advice.'

/** Un control CON brecha y otro SIN ella: los dos caminos de `omitempty`. */
const INFORME = {
  framework: 'hipaa_technical_safeguards',
  name: 'HIPAA Security Rule — Technical Safeguards',
  authority:
    'U.S. Department of Health and Human Services (HHS), Office for Civil Rights (OCR)',
  generated_at: '2026-08-20T16:00:00Z',
  summary: {
    total: 2,
    satisfied: 1,
    by_design: 0,
    partial: 0,
    gap: 1,
    unmapped: 0,
  },
  controls: [
    {
      control_id: '164.312(a)',
      citation: '45 CFR §164.312(a)',
      title: 'Access control',
      status: 'satisfied',
      requirement: 'Implement technical policies and procedures…',
      criterion: 'Engine RBAC, governed identities…',
      present_capabilities: ['access_control_rbac', 'identity_governance'],
      missing_capabilities: [],
      evidence: [],
      // sin `gap` ni `recommended_action`: el motor los omite cuando no hay brecha
    },
    {
      control_id: '164.312(b)',
      citation: '45 CFR §164.312(b)',
      title: 'Audit controls',
      status: 'gap',
      requirement: 'Implement hardware, software, and procedural mechanisms…',
      criterion: 'Append-only audit trail…',
      present_capabilities: [],
      missing_capabilities: ['audit_chain_verify'],
      evidence: [],
      gap: 'missing evidence for audit_chain_verify',
      recommended_action: 'Enable live hash-chain verification.',
    },
  ],
  disclaimer: DESCARGO,
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function stubFetch(
  handler: (url: string, init: RequestInit) => Response | Promise<Response>,
) {
  const mock = vi.fn((url: unknown, init: unknown) =>
    Promise.resolve(handler(String(url), (init ?? {}) as RequestInit)),
  )
  vi.stubGlobal('fetch', mock)
  return mock
}

function sentRequest(mock: ReturnType<typeof stubFetch>, call = 0) {
  const [url, init] = mock.mock.calls[call] as [string, RequestInit]
  const parsed = new URL(url, 'https://console.invalid')
  return { path: parsed.pathname, method: init.method }
}

beforeEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('la URL, que es el hallazgo entero', () => {
  it('⛔ pide la ruta DEDICADA, no /frameworks/{id}', async () => {
    const mock = stubFetch(() => jsonResponse(INFORME))

    await complianceApi.hipaaGapReport()

    const req = sentRequest(mock)
    // compliance.go:456 — reg.Handle("GET", "/hipaa/gap-report", permFrameworkRead, …)
    expect(req.method).toBe('GET')
    expect(req.path).toBe('/v1/m/compliance/hipaa/gap-report')
    // FIRES IF: alguien la «simplifica» a la ruta genérica creyendo que es el mismo HIPAA.
    // `hipaaTechnicalFramework()` NO está en `catalog`, así que /frameworks/{id} devolvería
    // 404 o —peor— el overlay clínico, que es otro documento con otra autoridad.
    expect(req.path).not.toMatch(/\/frameworks\//)
  })
})

describe('el descargo, que es lo que no puede faltar', () => {
  it('⛔ se pinta SIEMPRE, incluso sin ninguna brecha', async () => {
    stubFetch(() =>
      jsonResponse({
        ...INFORME,
        summary: { ...INFORME.summary, satisfied: 2, gap: 0 },
        controls: [INFORME.controls[0]],
      }),
    )
    renderIntel(<HipaaTab canRead />)

    // FIRES IF: alguien lo condiciona a que haya brechas, o lo pasa por
    // `FrameworkDisclaimerBanner`, que devuelve null salvo para crosswalks y frameworks en
    // desarrollo (components.tsx:244-255) y se lo tragaría EN SILENCIO. El texto dice «NOT a
    // HIPAA compliance certification and NOT legal advice»: un informe de brecha regulatoria
    // sin esa línea afirma exactamente lo que la línea niega, y lo hace con más fuerza cuando
    // no hay brechas que enseñar.
    expect(
      await screen.findByText(/NOT a HIPAA compliance certification/i),
    ).toBeInTheDocument()
  })

  it('y también cuando SÍ las hay', async () => {
    stubFetch(() => jsonResponse(INFORME))
    renderIntel(<HipaaTab canRead />)
    expect(
      await screen.findByText(/NOT a HIPAA compliance certification/i),
    ).toBeInTheDocument()
  })
})

describe('lo que este informe tiene y la vista genérica no', () => {
  it('⛔ pinta la CITA de cada control', async () => {
    stubFetch(() => jsonResponse(INFORME))
    renderIntel(<HipaaTab canRead />)

    // Sin la cita esta pantalla es un duplicado peor de /gaps: la referencia al CFR es el
    // único dato que el informe dedicado añade sobre el genérico.
    expect(await screen.findByText('45 CFR §164.312(a)')).toBeInTheDocument()
    expect(screen.getByText('45 CFR §164.312(b)')).toBeInTheDocument()
  })

  it('pinta la brecha y la acción recomendada del control que las tiene', async () => {
    stubFetch(() => jsonResponse(INFORME))
    renderIntel(<HipaaTab canRead />)

    expect(
      await screen.findByText(/missing evidence for audit_chain_verify/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/Enable live hash-chain verification/i),
    ).toBeInTheDocument()
  })

  it('⛔ y NO inventa filas para el control que no las tiene', async () => {
    stubFetch(() =>
      jsonResponse({ ...INFORME, controls: [INFORME.controls[0]] }),
    )
    renderIntel(<HipaaTab canRead />)

    await screen.findByText('45 CFR §164.312(a)')
    // FIRES IF: alguien pinta `gap` y `recommended_action` incondicionalmente. Son `omitempty`
    // en el motor: su AUSENCIA significa «no hay brecha», no «campo sin rellenar». Una fila
    // vacía convierte un control satisfecho en uno que parece incompleto.
    expect(screen.queryByText(/^Gap$/)).toBeNull()
    expect(screen.queryByText(/^Recommended action$/)).toBeNull()
  })
})

describe('las tres respuestas', () => {
  it('⛔ sin permiso NO le pregunta al motor', async () => {
    const mock = stubFetch(() => jsonResponse(INFORME))
    renderIntel(<HipaaTab canRead={false} />)

    expect(
      await screen.findByText(/do not have permission/i),
    ).toBeInTheDocument()
    expect(mock).not.toHaveBeenCalled()
  })

  it('⛔ si la consulta falla lo DICE, en vez de quedarse en blanco', async () => {
    stubFetch(() => jsonResponse({ error: 'boom' }, 503))
    renderIntel(<HipaaTab canRead />)

    // FIRES IF: alguien devuelve null al fallar. Una pantalla de cumplimiento en blanco es
    // indistinguible de una sin brechas, y ésa es la más cara de las dos lecturas.
    const aviso = await screen.findByRole('alert')
    expect(aviso.textContent ?? '').toMatch(/could not be read/i)
  })
})

// ─────────────────────────────────────────────────────────────────────────────────────
// EL RESUMEN, DESPUÉS DEL CONTRASTE — el defecto más caro que tuvo esta pantalla.
//
// ⛔ `StatusGap` exige `present == 0` (`assess.go:32-35`) y TODA capacidad ARQUITECTÓNICA se
// evalúa siempre presente por construcción (`capabilities.go:431-440`). Medido: los cinco
// controles de §164.312 llevan al menos una, así que **`summary.gap` es determinísticamente 0
// para este marco** — y la primera versión de esta pantalla titulaba justamente ese contador
// «Gaps». Habría dicho «Brechas 0» arriba mientras las tarjetas enseñaban brechas abajo.
const SUMARIO_REAL = {
  // La forma que HIPAA produce de verdad: nada en `gap`, y aun así nada satisfecho del todo.
  total: 5,
  satisfied: 2,
  by_design: 1,
  partial: 2,
  gap: 0,
  unmapped: 0,
}

/** La fila (`<dt>` + su valor) de una etiqueta EXACTA del resumen. */
async function filaDe(etiqueta: RegExp): Promise<HTMLElement> {
  const dt = await screen.findByText(etiqueta, { selector: 'dt' })
  const fila = dt.parentElement
  if (!fila) throw new Error('el término no está en una fila')
  return fila as HTMLElement
}

describe('el resumen no puede tranquilizar con un contador que no puede subir', () => {
  it('⛔ el agregado que encabeza NO es `gap`, y no es cero cuando no lo es', async () => {
    stubFetch(() =>
      jsonResponse({
        ...INFORME,
        summary: SUMARIO_REAL,
        controls: [INFORME.controls[0]],
      }),
    )
    renderIntel(<HipaaTab canRead />)

    // FIRES IF: alguien vuelve a titular `summary.gap`. Con este sumario —el que HIPAA produce—
    // ese contador es 0 y la pantalla afirmaría conformidad que el motor no afirma.
    //
    // ⛔ Se busca el TÉRMINO de la lista de definición, no cualquier texto: la nota de diseño
    //    repite la misma frase y `findByText` encontraba DOS. Un testigo que casa con su propia
    //    nota al pie no está midiendo la fila.
    const fila = await filaDe(/^Not fully backed$/i)
    expect(fila.textContent ?? '').toMatch(/3/)
  })

  it('⛔ un control POR DISEÑO no se cuenta como satisfecho', async () => {
    stubFetch(() =>
      jsonResponse({
        ...INFORME,
        summary: SUMARIO_REAL,
        controls: [INFORME.controls[0]],
      }),
    )
    renderIntel(<HipaaTab canRead />)

    // El motor lo dice de sí mismo: `gapControls` incluye `by_design` «as a (design-only)
    // caveat» (assess.go:97-107). Fundirlo con satisfecho es exactamente el blanqueo que el
    // árbol ya vigila en la vista OSCAL (compliance.test.tsx:230).
    expect(await filaDe(/^By design only$/i)).toBeTruthy()
    const nota = await screen.findByRole('note')
    expect(nota.textContent ?? '').toMatch(/not as live telemetry/i)
  })

  it('y la nota de diseño NO aparece cuando no hay ninguno', async () => {
    stubFetch(() =>
      jsonResponse({
        ...INFORME,
        summary: { ...SUMARIO_REAL, by_design: 0, satisfied: 3 },
        controls: [INFORME.controls[0]],
      }),
    )
    renderIntel(<HipaaTab canRead />)
    await filaDe(/^Not fully backed$/i)
    // Condicional, no decorativa: si se pintara siempre dejaría de significar nada.
    expect(screen.queryByRole('note')).toBeNull()
  })

  it('⛔ y con TODO satisfecho el agregado es 0 — el contrafactual', async () => {
    stubFetch(() =>
      jsonResponse({
        ...INFORME,
        summary: {
          total: 5,
          satisfied: 5,
          by_design: 0,
          partial: 0,
          gap: 0,
          unmapped: 0,
        },
        controls: [INFORME.controls[0]],
      }),
    )
    renderIntel(<HipaaTab canRead />)
    const fila = await filaDe(/^Not fully backed$/i)
    // Si el agregado se pintara siempre en aviso, esta celda lo caza: un despliegue conforme
    // debe poder verse conforme.
    expect(fila.textContent ?? '').toMatch(/0/)
  })
})
