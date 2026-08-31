// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Las dos listas de la consola de SEGURIDAD declaran su propio recorte. Aquí la afirmación
// por omisión es la más cara del producto: sin `limit` el motor pagina a 100 y devuelve un
// `has_more: true` que nadie miraba, así que la pantalla enseñaba cien hallazgos y se leía
// «éstos son los hallazgos» — la frase con la que alguien da por revisado un estate.
//
// La feature YA sabía decirlo en otros dos sitios: el export avisa con `res.truncated` y la
// postura de seguridad con `counts_partial`. Lo que faltaba era decirlo en la lista.
import { readFileSync } from 'node:fs'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@/features/_intel'
import type { Finding, ForensicCase } from './types'

const authz = vi.hoisted(() => ({ can: (_p: string): boolean => true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: authz.can }),
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const api = vi.hoisted(() => ({
  findings: vi.fn(),
  cases: vi.fn(),
  exportFindings: vi.fn(),
  safetyPosture: vi.fn(),
  enforcement: vi.fn(),
  anomalies: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, securityApi: { ...actual.securityApi, ...api } }
})

import { securityKeys } from './api'
import { SecurityView } from './security-view'
import './i18n'

const hallazgo = {
  id: 'f-1',
  kind: 'policy_violation',
  severity: 'high',
  status: 'open',
  source: 'engine',
  subject_kind: 'agent',
  subject_ref: 'agent-1',
  summary: 'egress to an undeclared endpoint',
  detected_at: '2026-08-20T10:00:00Z',
} as unknown as Finding

const expediente = {
  id: 'c-1',
  title: 'Exfiltración sospechosa',
  status: 'open',
  opened_at: '2026-08-20T10:00:00Z',
} as unknown as ForensicCase

function pinta(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    qc,
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authz.can = () => true
  api.findings.mockResolvedValue({ items: [hallazgo], has_more: false })
  api.cases.mockResolvedValue({ items: [expediente], has_more: false })
  api.exportFindings.mockResolvedValue({
    filename: 'f.json',
    content_type: 'application/json',
    text: '{}',
    truncated: false,
  })
  api.safetyPosture.mockResolvedValue({ items: [], counts_partial: false })
  api.enforcement.mockResolvedValue({ items: [] })
  api.anomalies.mockResolvedValue({ items: [] })
})

describe('Hallazgos — la lista dice cuánto NO se está viendo', () => {
  it('pide el techo real del motor, no el de por defecto', async () => {
    pinta(<SecurityView />)
    await waitFor(() => expect(api.findings).toHaveBeenCalled())
    // FIRES IF: alguien quita el `limit` — el motor pagina a 100 en silencio.
    expect(api.findings).toHaveBeenCalledWith({ limit: 1000 })
  })

  it('DECLARA el recorte con la cifra, y dice que el resto NO prueba nada', async () => {
    api.findings.mockResolvedValue({ items: [hallazgo], has_more: true })
    pinta(<SecurityView />)
    const aviso = await screen.findByText(/Showing the first/i)
    expect(aviso.textContent).toMatch(/1000/)
    expect(aviso.textContent).not.toMatch(/\b100\b/)
    const title = aviso.getAttribute('title') ?? ''
    expect(title).toMatch(/not evidence/i)
    // ⛔ Y DICE **QUÉ** MIL, no sólo cuántos. La página va por id de ingesta, que NO es el
    //    orden en que ocurrieron los problemas, así que puede dejar fuera hallazgos
    //    recientes. «Los primeros 1000» a secas es literalmente cierto y engañoso.
    expect(title).toMatch(/ingestion id/i)
    expect(title).toMatch(/recent findings can be missing/i)
  })

  it('y NO lo declara cuando no lo hay — el contrafactual en la otra dirección', async () => {
    pinta(<SecurityView />)
    await waitFor(() => expect(api.findings).toHaveBeenCalled())
    expect(screen.queryByText(/Showing the first/i)).not.toBeInTheDocument()
  })

  it('el aviso no sobrevive a un error posterior', async () => {
    api.findings.mockResolvedValue({ items: [hallazgo], has_more: true })
    const { qc } = pinta(<SecurityView />)
    expect(await screen.findByText(/Showing the first/i)).toBeInTheDocument()
    api.findings.mockRejectedValue(new Error('boom'))
    await qc.refetchQueries()
    await waitFor(() =>
      expect(screen.queryByText(/Showing the first/i)).not.toBeInTheDocument(),
    )
  })
})

// La pestaña forense no es la de por defecto: `TabsContent` no monta lo que no está
// activo, así que sin pulsarla los asertos medirían «no aparece» por otra razón.
async function abrirForense() {
  const user = userEvent.setup()
  await user.click(
    await screen.findByRole('tab', { name: /Forensic|Forense/i }),
  )
}

describe('Expedientes forenses — lo mismo, y con el permiso que ya existía', () => {
  it('pide el techo real y declara el recorte', async () => {
    api.cases.mockResolvedValue({ items: [expediente], has_more: true })
    pinta(<SecurityView />)
    await abrirForense()
    await waitFor(() => expect(api.cases).toHaveBeenCalled())
    expect(api.cases).toHaveBeenCalledWith(undefined, 1000)
    const aviso = await screen.findByText(/Showing the first/i)
    expect(aviso.textContent).toMatch(/1000/)
  })

  it('sin `security:case:read` no consulta nada, y por tanto no hay aviso', async () => {
    authz.can = (p: string) => p !== 'security:case:read'
    pinta(<SecurityView />)
    await abrirForense()
    await waitFor(() => expect(api.cases).not.toHaveBeenCalled())
    expect(screen.queryByText(/Showing the first/i)).not.toBeInTheDocument()
  })
})

describe('securityKeys — la clave base tiene que ser PREFIJO de la filtrada', () => {
  it('invalidar la lista alcanza a la consulta viva con params', async () => {
    // ⛔ ESTE CASO EXISTE POR UNA REGRESIÓN QUE INTRODUJE YO. Al pasar `limit`, la clave
    //    viva pasó a `[…,'findings',{limit:1000}]` mientras el factory sin params daba
    //    `[…,'findings',null]`, y `null` NO es prefijo de un objeto: el triaje dejó de
    //    refrescar la fila. Lo devolvió el contraste comprobándolo contra el TanStack
    //    instalado (`invalidated: false`).
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const viva = securityKeys.findings('t1', { limit: 1000 })
    qc.setQueryData(viva, { items: [], has_more: false })
    await qc.invalidateQueries({ queryKey: securityKeys.findings('t1') })
    expect(qc.getQueryState(viva)?.isInvalidated).toBe(true)

    // Lo mismo para expedientes, que se invalidan al crear y al editar.
    const vivaCasos = securityKeys.cases('t1', { limit: 1000 })
    qc.setQueryData(vivaCasos, { items: [], has_more: false })
    await qc.invalidateQueries({ queryKey: securityKeys.cases('t1') })
    expect(qc.getQueryState(vivaCasos)?.isInvalidated).toBe(true)

    // Control POSITIVO de que el aserto discrimina: otra entrada NO se invalida.
    const ajena = securityKeys.enforcement('t1')
    qc.setQueryData(ajena, { items: [] })
    await qc.invalidateQueries({ queryKey: securityKeys.findings('t1') })
    expect(qc.getQueryState(ajena)?.isInvalidated).toBe(false)
  })
})

describe('P3 — la celda FINDING puede truncar de verdad', () => {
  /**
   * ⛔ EL DEFECTO, y la parte instructiva es que la cura pedida YA ESTABA. Los dos
   * `<p>` de la celda llevan `truncate`, y `getComputedStyle` sobre el `dist`
   * confirma `text-overflow: ellipsis`, `overflow: hidden` y `white-space: nowrap`.
   * Aun así el texto salía cortado A MITAD DE PALABRA y SIN puntos suspensivos.
   *
   * Medido en Chrome: nada acotaba el ancho del `<p>`, así que con un título largo
   * crecía a 1136 px y `scrollWidth === clientWidth` — **no había desbordamiento que
   * truncar**. La columna estiraba la tabla a 1189 px dentro de un contenedor de
   * 1116, y quien cortaba era el BORDE de la tabla. De ahí la ausencia de elipsis.
   *
   * `truncate` sólo produce elipsis cuando algo limita la anchura. Con la columna
   * acotada: tabla 1116 = contenedor 1116 (deja de desbordar), `<p>` 420 contra
   * 1136 de contenido, y la elipsis aparece. Además vuelven a verse EVIDENCE,
   * STATUS y ACTIONS, que el desbordamiento empujaba fuera de pantalla.
   *
   * EL MUTANTE: quitar el `size`/`max-w` de la columna `title`. Falla aquí.
   */
  it('la cabecera FINDING declara una anchura que permite truncar', async () => {
    pinta(<SecurityView />)
    const th = await screen.findByRole('columnheader', { name: /finding/i })
    const ancho = Number.parseFloat((th as HTMLElement).style.width)
    expect(
      ancho,
      'sin anchura declarada el `<p>` crece, nunca desborda y la elipsis no puede dispararse',
    ).toBeGreaterThan(0)
    expect(ancho).toBeLessThanOrEqual(520)
  })

  /**
   * ⛔ Y ESTE CASO EXISTE PORQUE EL DE ARRIBA NO BASTABA, cosa que descubrí midiendo:
   * quitar el `max-w-[420px]` de la celda —que es la cura REAL— dejaba la batería en
   * 8/8 VERDE. El caso de arriba mira `th.style.width`, que lo pone `size`; y `size`
   * NO acota nada por sí solo.
   *
   * Medido a 1440 con hallazgos de kill switch: con `size: 150` y con `size: 148` la
   * columna `source` se quedó en sus **175 px** las dos veces, byte a byte igual. La
   * tabla es `w-full border-collapse` con `table-layout: auto` (data-table.tsx:596),
   * y ahí **el `width` de un `<th>` es una SUGERENCIA que el contenido pisa**. Lo que
   * de verdad acota es un `max-width` CSS en el contenido de la celda: al ponerlo,
   * `source` cayó a 148, la tabla de 1133 a 1116 y ACTIONS volvió dentro.
   *
   * ⇒ `size` fija el objetivo; `max-w` impide que el contenido lo ignore. Hacen falta
   * los DOS, y un test que sólo mire el `<th>` certifica la mitad que no cura.
   */
  it('la celda acota su contenido, no sólo la cabecera', () => {
    const src = readFileSync('src/features/security/components.tsx', 'utf8')
    // No-vacuidad: el caso sólo significa algo mientras la columna siga declarando su
    // objetivo. Si `size` desaparece, que falle y se relea.
    expect(src).toContain('size: 420')
    // La celda FINDING tiene que llevar SU PROPIA cota; sin ella, `table-layout:
    // auto` deja que el contenido estire la columna y `size` no lo impide.
    expect(
      src,
      'la celda FINDING perdió su `max-w`: con table-layout auto el contenido vuelve a estirar la columna',
    ).toMatch(/min-w-0 max-w-\[\d+px\]/)
  })

  /**
   * ⛔ Y LA MISMA COTA EN `source`, por la misma razón y con su medida: con hallazgos
   * de kill switch (`governance.killswitch`) esa columna se iba a **175 px**, la
   * tabla a **1133** dentro de una tarjeta de **1116**, y ACTIONS quedaba FUERA —
   * «ACTIO» recortado y **TRES de seis** botones «Triage» sin llegar a verse.
   * Con la cota: source 148, tabla 1116 = tarjeta, **0 botones fuera**.
   *
   * Este caso nació porque el mutante que quita esa cota SOBREVIVIÓ a la batería
   * cuando sólo cubría FINDING. Una cota sin testigo dura hasta el siguiente que la
   * vea «redundante».
   */
  it('la celda SOURCE también acota, o vuelve a empujar a ACTIONS fuera', () => {
    const src = readFileSync('src/features/security/components.tsx', 'utf8')
    expect(src, 'la columna source dejó de declarar su objetivo').toContain(
      'size: 148',
    )
    expect(
      src,
      'la celda `source` perdió su `max-w`: crecerá con el dato y ACTIONS se saldrá de la tarjeta',
    ).toMatch(/block max-w-\[\d+px\] truncate font-mono/)
  })
})
