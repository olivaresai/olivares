// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// registry.list-ceilings.test.ts — el trinquete de techos, aplicado a TODAS las features que
// tienen `api.ts` propio, y no feature a feature.
//
// ⛔ POR QUÉ EXISTE, Y POR QUÉ NO ES UN GUION NUEVO. El trinquete ya estaba escrito y ya era
//    genérico: `@/test/list-ceiling-ratchet` exporta nueve funciones que toman la FUENTE como
//    parámetro, no la feature, y su propio autor lo dejó dicho en `finops/list-ceilings.test.ts:15`
//    —«el trinquete vive en `@/test/list-ceiling-ratchet`; aquí sólo se aplica a finops»—. Lo que
//    faltaba no era el motor: era el BUCLE. Medido el 2026-08-28:
//
//      features en web/src/features/ ............................ 73
//      con `api.ts` PROPIO (el universo del trinquete) ........... 47
//      que lo aplicaban (fichero `list-ceilings.test.ts` propio) . 10
//      ⇒ sin control, con superficie propia ...................... 37
//
//    Las otras 26 no tienen `api.ts`: consumen listas de otras features, así que el trinquete no
//    tiene qué mirar en ellas. Decir «73 sin control» era ancho, y la cifra que vale es 37 de 47.
//
// ⛔ Y POR QUÉ EL CONTROL VA AQUÍ Y NO EN CADA FEATURE. Un control por feature sólo protege a las
//    features que alguien se acordó de proteger: es una propiedad declarada donde ya se cumple.
//    Este fichero la exige donde SE PUEDE INCUMPLIR — en la lista de features, que crece sola. Una
//    feature nueva con `api.ts` entra en el bucle el día que nace, sin que nadie escriba nada.
//
// ⛔ LO QUE ESTE FICHERO NO VE, dicho para que nadie lo lea como más de lo que es:
//    · Es ANÁLISIS DE FUENTE. No monta nada ni ejecuta el cliente.
//    · Hereda los dos techos falsos que un contraste construyó —`limit: undefined` y `limit: 0`—
//      porque usa las mismas funciones que ya los cazan; el control de sonda de abajo lo fija.
//    · Y es CIEGO AL MOTOR. Un handler que pide bien y luego descarta la página en Go
//      (`recs, _, err := repo.List(...)`) deja la lista recortada sin que ninguna sonda de cliente
//      lo note. Medido el mismo día: 413 llamadas descartan la página, 56 % del total, y cero
//      gates lo vigilan. Ésa es otra familia y le toca su propio control del lado Go.
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { pideTecho, rutasSinTecho } from '@/test/list-ceiling-ratchet'

const RAIZ = resolve(__dirname)

/**
 * Features con `api.ts` propio. Se DERIVA del árbol, nunca se escribe a mano: una lista a mano
 * envejece en silencio y deja fuera justo a la feature nueva, que es la que nadie ha revisado.
 */
function featuresConApi(): string[] {
  const out: string[] = []
  for (const nombre of readdirSync(RAIZ)) {
    const dir = join(RAIZ, nombre)
    if (!existsSync(dir) || !statSync(dir).isDirectory()) continue
    if (existsSync(join(dir, 'api.ts'))) out.push(nombre)
  }
  return out.sort()
}

/**
 * Rutas de lista que hoy no piden techo. DOS CLASES DISTINTAS EN EL MISMO MAPA, y conviene no
 * confundirlas al leerlo:
 *
 *   · EXENCIÓN RAZONADA — la razón dice dónde se comprobó que la superficie DRENA o no pagina, y
 *     es refutable leyendo ese sitio. Ésa es una exención de verdad.
 *   · LÍNEA BASE («linea base 2026-08-28; sin clasificar drena/recorta») — NO es una razón: es una
 *     DEUDA DECLARADA CON FECHA. Dice «esto estaba así el día que se puso el control», y nada más.
 *     Cada una de estas 126 entradas tiene que acabar convertida en exención razonada o en un
 *     `limit`. Se distinguen por su texto a propósito, para que un censo las pueda contar.
 *
 * ⛔ CADA LÍNEA ES UNA DEUDA CON NOMBRE, no una exclusión. La razón dice DÓNDE se comprobó que la
 *    superficie drena o no pagina, para que el que la lea pueda refutarla. Una exención sin razón
 *    verificable es un `grep -v` con ropa de control.
 *
 * ⛔ Y SE SIEMBRA CON LO MEDIDO HOY, no vacía. Vaciarla obligaría a arreglar 37 features en el
 *    mismo commit o a desactivar el gate: las dos salidas malas. Con la lista sembrada, el gate
 *    entra en verde, ninguna feature NUEVA puede incumplir, y la deuda se drena por su cuenta —
 *    y el último caso de abajo impide que la lista se pudra mientras tanto.
 */
const SIN_TECHO_CONOCIDAS: Record<string, Record<string, string>> = {
  'agent-artifacts': {
    aibomSeals: 'linea base 2026-08-28; sin clasificar drena/recorta',
    artifacts: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  agentops: {
    listRuns: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listWorkspaces: 'linea base 2026-08-28; sin clasificar drena/recorta',
    runEvents: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  alerting: {
    listDeliveries: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listOutbox: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listRoutes: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  capabilities: {
    listRevisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  'claude-policy': {
    listVersions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    pdpVersions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    threadEvents: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  compliance: {
    aimsPacks: 'linea base 2026-08-28; sin clasificar drena/recorta',
    ccmDrift: 'linea base 2026-08-28; sin clasificar drena/recorta',
    ccmSnapshots: 'linea base 2026-08-28; sin clasificar drena/recorta',
    doraIncidents: 'linea base 2026-08-28; sin clasificar drena/recorta',
    doraRegisters: 'linea base 2026-08-28; sin clasificar drena/recorta',
    erasureEvents: 'linea base 2026-08-28; sin clasificar drena/recorta',
    erasures: 'linea base 2026-08-28; sin clasificar drena/recorta',
    fedrampPacks: 'linea base 2026-08-28; sin clasificar drena/recorta',
    holdEvents: 'linea base 2026-08-28; sin clasificar drena/recorta',
    holds: 'linea base 2026-08-28; sin clasificar drena/recorta',
    nis2Incidents: 'linea base 2026-08-28; sin clasificar drena/recorta',
    oscalProfiles: 'linea base 2026-08-28; sin clasificar drena/recorta',
    residency: 'linea base 2026-08-28; sin clasificar drena/recorta',
    retentionPolicies: 'linea base 2026-08-28; sin clasificar drena/recorta',
    retentionRuns: 'linea base 2026-08-28; sin clasificar drena/recorta',
    risk: 'linea base 2026-08-28; sin clasificar drena/recorta',
    sectorPacks: 'linea base 2026-08-28; sin clasificar drena/recorta',
    usLawPacks: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  deploy: {
    listRevisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  evals: {
    runResults: 'linea base 2026-08-28; sin clasificar drena/recorta',
    scorecards: 'linea base 2026-08-28; sin clasificar drena/recorta',
    suiteCases: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  eventing: {
    deadLetters: 'linea base 2026-08-28; sin clasificar drena/recorta',
    deliveries: 'linea base 2026-08-28; sin clasificar drena/recorta',
    subscriptions: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  governance: {
    listAgentRiskProfiles:
      'linea base 2026-08-28; sin clasificar drena/recorta',
    listApprovals: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listBindings: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listBreakGlass: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listBreakGlassUses: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listDecisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listGroupMembers: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listGroups: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listIdentities: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listPolicies: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listRoutinePolicies: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  health: {
    checks: 'linea base 2026-08-28; sin clasificar drena/recorta',
    events: 'linea base 2026-08-28; sin clasificar drena/recorta',
    incidents: 'linea base 2026-08-28; sin clasificar drena/recorta',
    status: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  identity: {
    audit: 'linea base 2026-08-28; sin clasificar drena/recorta',
    cryptoInventory: 'linea base 2026-08-28; sin clasificar drena/recorta',
    externalKeys: 'linea base 2026-08-28; sin clasificar drena/recorta',
    findings: 'linea base 2026-08-28; sin clasificar drena/recorta',
    groups: 'linea base 2026-08-28; sin clasificar drena/recorta',
    identities: 'linea base 2026-08-28; sin clasificar drena/recorta',
    nhiLifecycle: 'linea base 2026-08-28; sin clasificar drena/recorta',
    workspaceResidency: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  inventory: {
    entities: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  killswitch: {
    list: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listGuardianActions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    listGuardianRules: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  knowledge: {
    listRevisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  models: {
    aibomSeals: 'linea base 2026-08-28; sin clasificar drena/recorta',
    datasets: 'linea base 2026-08-28; sin clasificar drena/recorta',
    deployments: 'linea base 2026-08-28; sin clasificar drena/recorta',
    finetuneJobs: 'linea base 2026-08-28; sin clasificar drena/recorta',
    gpaiPostures: 'linea base 2026-08-28; sin clasificar drena/recorta',
    keys: 'linea base 2026-08-28; sin clasificar drena/recorta',
    modelAccess: 'linea base 2026-08-28; sin clasificar drena/recorta',
    modelAdmissions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    modelVersions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    ownedModels: 'linea base 2026-08-28; sin clasificar drena/recorta',
    routingPolicies: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  observability: {
    traces: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  orchestration: {
    decisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    flows: 'linea base 2026-08-28; sin clasificar drena/recorta',
    scheduleDecisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    schedules: 'linea base 2026-08-28; sin clasificar drena/recorta',
    timeline: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  recordings: {
    listSessions: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  redteam: {
    results: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  residency: {
    listOrgs: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  sandbox: {
    outputs: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  security: {
    caseLinks: 'linea base 2026-08-28; sin clasificar drena/recorta',
    cases: 'linea base 2026-08-28; sin clasificar drena/recorta',
    findings: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  sessions: {
    live: 'linea base 2026-08-28; sin clasificar drena/recorta',
    timeline: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  tenants: {
    list: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
  voice: {
    allDecisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    decisions: 'linea base 2026-08-28; sin clasificar drena/recorta',
    policies: 'linea base 2026-08-28; sin clasificar drena/recorta',
    sessions: 'linea base 2026-08-28; sin clasificar drena/recorta',
  },
}

describe('registro · toda ruta de lista pide su techo, en TODAS las features con api.ts', () => {
  it('el universo se deriva del árbol y no está vacío', () => {
    const features = featuresConApi()
    // Si esto cae a cero, el gate se ha quedado sin sujeto y estaría pasando por vacío:
    // un control que no mira nada no es un control que no encuentra nada.
    expect(features.length).toBeGreaterThanOrEqual(40)
    expect(features).toContain('finops')
  })

  it('ninguna ruta de lista sale sin `limit`, salvo las declaradas con razón', () => {
    const incumplen: string[] = []
    for (const f of featuresConApi()) {
      const src = readFileSync(join(RAIZ, f, 'api.ts'), 'utf8')
      const { sinTecho } = rutasSinTecho(src, SIN_TECHO_CONOCIDAS[f] ?? {})
      for (const ruta of sinTecho) incumplen.push(`${f}.${ruta}`)
    }
    expect(
      incumplen,
      `rutas de lista sin techo y sin exencion declarada (anadelas a SIN_TECHO_CONOCIDAS con su razon medida, o ponles el limit):\n  ${incumplen.join('\n  ')}`,
    ).toEqual([])
  })

  it('CONTROL de la sonda — ve la que falta, no acusa a la que lo lleva, y rechaza los dos techos falsos', () => {
    // Sin esto, un cambio que rompiera `pideTecho` dejaría el bucle en verde sobre 47 features a
    // la vez. Un gate ciego no falla: certifica.
    const conTecho =
      'http.get<ListResponse<X>>(`${BASE}/x`, { query: { limit: 1000 } })'
    const sinTecho =
      'http.get<ListResponse<X>>(`${BASE}/x`, { query: { status } })'
    const anidada =
      'http.get<ListResponse<X>>(`${BASE}/x`, { query: { limit: F(g(1)) } })'
    expect(pideTecho(conTecho, 0)).toBe(true)
    expect(pideTecho(sinTecho, 0)).toBe(false)
    expect(pideTecho(anidada, 0)).toBe(true)
    // Los dos que `limit:` a secas aceptaba y no son techo:
    const indefinido =
      'http.get<ListResponse<X>>(`${BASE}/x`, { query: { limit: undefined } })'
    const cero =
      'http.get<ListResponse<X>>(`${BASE}/x`, { query: { limit: 0 } })'
    expect(pideTecho(indefinido, 0)).toBe(false)
    expect(pideTecho(cero, 0)).toBe(false)
  })

  it('las exenciones declaradas siguen siendo ciertas: ninguna sobra', () => {
    // El caso anti-podredumbre. Sin él la lista sólo crece, y una exención que ya no hace falta
    // esconde una regresión futura: la ruta puede perder su techo y nadie se entera.
    const sobran: string[] = []
    for (const [f, rutas] of Object.entries(SIN_TECHO_CONOCIDAS)) {
      const api = join(RAIZ, f, 'api.ts')
      if (!existsSync(api)) {
        sobran.push(`${f} (la feature ya no tiene api.ts)`)
        continue
      }
      const src = readFileSync(api, 'utf8')
      const { sinTecho } = rutasSinTecho(src)
      for (const ruta of Object.keys(rutas)) {
        if (!sinTecho.includes(ruta)) sobran.push(`${f}.${ruta}`)
      }
    }
    expect(
      sobran,
      `exenciones que ya no hacen falta (borra su linea):\n  ${sobran.join('\n  ')}`,
    ).toEqual([])
  })
})
