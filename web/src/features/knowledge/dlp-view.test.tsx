// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — las reglas DLP, y las TRES cosas que un listado plano funde en una.
//
// La política es DENY-CLOSED y lo declara `modules/knowledge/dlp.go:28-40,84-92`. Las dos
// confusiones posibles van, las dos, en la dirección peligrosa: hacen creer que se está
// protegido cuando no se está, o al revés.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
  useRouterState: () => '/',
}))
// Mutable por casilla: el motor separa `knowledge:dlp:admin` (escribir política de egreso) de
// `knowledge:dlp:read`, y la pantalla tiene que separar lo mismo. Por defecto, todo concedido —
// las casillas de lectura que ya existían no cambian de comportamiento.
let permisos: (p: string) => boolean = () => true
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => permisos(p) }),
}))

const dlpMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    knowledgeApi: {
      ...actual.knowledgeApi,
      dlpRules: (...a: unknown[]) => dlpMock(...a),
      listKbs: lista,
      listLineage: lista,
      listPrompts: lista,
      listMemory: lista,
      listDataProducts: lista,
    },
  }
})

const KnowledgeView = (await import('./knowledge-view')).default

function montar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <KnowledgeView />
    </QueryClientProvider>,
  )
}

async function abrirDlp(user: ReturnType<typeof userEvent.setup>) {
  montar()
  await user.click(await screen.findByRole('tab', { name: /^DLP$/i }))
}

beforeEach(() => {
  permisos = () => true
  dlpMock.mockReset().mockResolvedValue({ items: [] })
})

describe('las reglas DLP', () => {
  /**
   * ⛔ EL CONTROL MÁS IMPORTANTE: CERO reglas significa que **la puerta está inerte**, no que
   * todo esté bloqueado (`dlp.go:28-30`). Un operador que vea una lista vacía y entienda «nada
   * sale» tiene el bloqueo exactamente al revés, y actuaría sobre esa creencia.
   *
   * EL MUTANTE: no distinguir, o decir «sin reglas, todo denegado». La pantalla afirmaría una
   * protección que no existe.
   */
  it('sin reglas dice que la puerta está INERTE, no que todo se deniegue', async () => {
    const user = userEvent.setup()
    await abrirDlp(user)
    expect(await screen.findByText(/gate is inert/i)).toBeInTheDocument()
    expect(screen.queryByText(/DLP is enabled/i)).toBeNull()
  })

  /**
   * ⛔ EL SEGUNDO: con reglas, el contenido SIN ESCANEAR se deniega salvo una regla explícita, y
   * **`*` NO lo cubre** (`dlp.go:84-88`: «unprovable sensitivity needs its own opt-out»).
   *
   * Esta fixture tiene un `*: allow` y NINGUNA regla `unscanned`. La lectura ingenua —«tengo un
   * comodín que permite, luego todo sale»— es FALSA, y la pantalla tiene que decirlo.
   *
   * EL MUTANTE: tratar el `*` como cobertura total. La pantalla diría que lo no escaneado sale
   * cuando el motor lo deniega.
   */
  it('un «*: allow» NO permite el contenido sin escanear', async () => {
    dlpMock.mockResolvedValue({
      items: [{ id: 'r1', class: '*', action: 'allow', created_by: 'a@b' }],
    })
    const user = userEvent.setup()
    await abrirDlp(user)
    expect(await screen.findByText(/DLP is enabled/i)).toBeInTheDocument()
    expect(
      await screen.findByText(/Unscanned content is DENIED/i),
    ).toBeInTheDocument()
    // Y el alcance del comodín se explica DONDE se lee la regla, no en una nota al pie.
    expect(
      await screen.findByText(/does NOT cover unscanned content/i),
    ).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: con una regla explícita `unscanned: allow`, la pantalla
   * lo dice — y lo dice como ADVERTENCIA, porque contenido que nadie clasificó puede salir.
   *
   * Sin esta casilla, una pantalla que dijera siempre «denegado» pasaría la de arriba y mentiría
   * en el caso en que de verdad hay riesgo.
   */
  it('con una regla explícita, dice que lo no escaneado SÍ sale', async () => {
    dlpMock.mockResolvedValue({
      items: [
        { id: 'r1', class: '*', action: 'deny', created_by: 'a@b' },
        { id: 'r2', class: 'unscanned', action: 'allow', created_by: 'a@b' },
      ],
    })
    const user = userEvent.setup()
    await abrirDlp(user)
    expect(
      await screen.findByText(/Unscanned content is ALLOWED/i),
    ).toBeInTheDocument()
    expect(screen.queryByText(/Unscanned content is DENIED/i)).toBeNull()
  })

  it('pide las reglas al abrir la pestaña', async () => {
    const user = userEvent.setup()
    await abrirDlp(user)
    await waitFor(() => expect(dlpMock).toHaveBeenCalled())
  })
  /**
   * ⛔ EL CONTROL DE ESCRITURA, y es el más peligroso de toda la pantalla: `PUT /dlp/rules` es un
   * **UPSERT POR CLASE** (`dlp.go:193,222-232`), no un alta. Guardar una clase que ya tiene regla
   * **sustituye la que había**, sin preguntar.
   *
   * EL MUTANTE: no avisar. Un diálogo titulado «nueva regla» que en realidad reemplaza una
   * política de egreso vigente es la forma más silenciosa de abrir un perímetro cerrado: quien
   * teclea `pii` creyendo que añade un `allow` para un caso concreto **borra el `deny` que
   * protegía esa clase**, y la pantalla se lo agradece con un mensaje de éxito.
   */
  it('avisa de que guardar una clase existente SUSTITUYE su regla', async () => {
    dlpMock.mockResolvedValue({
      items: [{ id: 'r1', class: 'pii', action: 'deny', created_by: 'a@b' }],
    })
    const user = userEvent.setup()
    await abrirDlp(user)
    await user.click(
      await screen.findByRole('button', { name: /New DLP rule/i }),
    )
    await user.type(await screen.findByLabelText(/^Class$/i), 'pii')
    expect(await screen.findByText(/Saving REPLACES it/i)).toBeInTheDocument()
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: una clase NUEVA no reemplaza nada y no se avisa. Un aviso
   * permanente en cada alta se deja de leer justo el día que sí reemplaza algo.
   */
  it('una clase nueva no avisa de sustitución', async () => {
    dlpMock.mockResolvedValue({
      items: [{ id: 'r1', class: 'pii', action: 'deny', created_by: 'a@b' }],
    })
    const user = userEvent.setup()
    await abrirDlp(user)
    await user.click(
      await screen.findByRole('button', { name: /New DLP rule/i }),
    )
    await user.type(await screen.findByLabelText(/^Class$/i), 'secretos')
    expect(screen.queryByText(/Saving REPLACES it/i)).toBeNull()
  })

  /**
   * ⛔ Y EL PERMISO: escribir política de egreso es `knowledge:dlp:admin`
   * (`knowledge.go:356-357`), no la lectura. Un lector no debe ver botones de escritura que el
   * motor le va a negar — y menos en la pantalla que decide qué sale del perímetro.
   */
  it('quien sólo puede LEER no ve los botones de escritura', async () => {
    permisos = (p: string) => p === 'knowledge:dlp:read'
    dlpMock.mockResolvedValue({
      items: [{ id: 'r1', class: 'pii', action: 'deny', created_by: 'a@b' }],
    })
    const user = userEvent.setup()
    await abrirDlp(user)
    expect(await screen.findByText('pii')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /New DLP rule/i })).toBeNull()
    expect(
      screen.queryByRole('button', { name: /Delete the rule/i }),
    ).toBeNull()
  })
})
