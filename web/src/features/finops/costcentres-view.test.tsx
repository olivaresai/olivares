// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C07-04 — centros de coste: la pantalla que da dueño al gasto sin atribuir, y las dos cosas que
// el motor hace y una lista plana escondería.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, userEvent, waitFor, within } from '@/test/intel'
import '@/features/_intel'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const centrosMock = vi.fn()
const reglasMock = vi.fn()
const tarifasMock = vi.fn()
const crearMock = vi.fn()
const actualizarMock = vi.fn()
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  const {
    summaryFixture,
    forecastFixture,
    spendFixture,
    recommendationsFixture,
  } = await import('./fixtures')
  const lista = () => Promise.resolve({ items: [], has_more: false })
  return {
    ...actual,
    finopsApi: {
      ...actual.finopsApi,
      costCenters: (...a: unknown[]) => centrosMock(...a),
      createCostCenter: (...a: unknown[]) => crearMock(...a),
      updateCostCenter: (...a: unknown[]) => actualizarMock(...a),
      costCenterMappings: (...a: unknown[]) => reglasMock(...a),
      seatUtilization: () =>
        Promise.resolve({ provider: 'anthropic', days: [] }),
      valueSummary: () => Promise.resolve({ cancellation_risk: [] }),
      summary: () => Promise.resolve(summaryFixture),
      forecast: () => Promise.resolve(forecastFixture),
      spend: () => Promise.resolve(spendFixture),
      recommendations: () => Promise.resolve({ items: recommendationsFixture }),
      budgets: lista,
      statements: lista,
      modelRates: (...a: unknown[]) => tarifasMock(...a),
    },
  }
})

const { FinOpsView, estadoCentro } = await import('./finops-view')

const CC = {
  id: 'cc-1',
  code: 'ENG-01',
  // ⛔ `name`, NO `cc_name`. Esta fixture decía `cc_name` — el campo equivocado de la CONSOLA,
  //    no el del motor (`costCenterDTO` declara `json:"name"`). Un oráculo copiado del sujeto no
  //    puede discrepar de él: por eso 54 celdas estaban verdes con todos los centros SIN NOMBRE.
  name: 'Ingeniería',
  status: 'active',
  description: 'Departamento de ingeniería',
  owner: 'cto@demo',
}

async function abrir(user: ReturnType<typeof userEvent.setup>) {
  renderIntel(<FinOpsView />)
  await user.click(await screen.findByRole('tab', { name: /Cost centres/i }))
}

const TARIFA = {
  id: 'mr-1',
  provider: 'anthropic',
  model_ref: 'claude-opus-5',
  input_rate_micro_usd: 15_000_000,
  output_rate_micro_usd: 75_000_000,
  effective_from: '2026-01-01T00:00:00Z',
}

beforeEach(() => {
  centrosMock.mockReset().mockResolvedValue({ items: [CC] })
  crearMock.mockReset().mockResolvedValue({ id: 'cc-2' })
  actualizarMock.mockReset().mockResolvedValue({ ...CC })
  reglasMock.mockReset().mockResolvedValue({ items: [] })
  tarifasMock.mockReset().mockResolvedValue({ items: [TARIFA] })
})

describe('los centros de coste', () => {
  /**
   * ⛔ EL RECORTE SILENCIOSO. Sin `limit`, el motor sirve su página por defecto (100) y esta
   * pantalla la pintaba entera SIN decir que faltaban filas: ninguna vista de finops miraba
   * `has_more`. En centros de coste eso no es cosmético — el que falta se lee como que no existe,
   * y el gasto que le tocaba parece sin atribuir.
   *
   * Se fija la cadena EXACTA a propósito: un `/\d+/` no distingue «1» de «1000», y ya me pasó
   * interpolar la constante del techo en vez del número de filas cargadas.
   */
  it('avisa cuando la lista viene recortada', async () => {
    // ⛔ MIL FILAS, NO UNA. Un contraste demostró que `has_more: true` con una sola fila es un
    //    estado que el motor NO produce: el store pide `limit+1` y sólo enciende `HasMore` cuando
    //    recorta, así que con el techo en 1000 implica exactamente 1000 filas. Un caso que fija un
    //    estado imposible prueba el componente, no el contrato — y hacía morir a un mutante
    //    («interpola el techo») que bajo el estado REAL es equivalente.
    const muchos = Array.from({ length: 1000 }, (_, i) => ({
      ...CC,
      id: `cc-${i}`,
      code: `ENG-${i}`,
    }))
    centrosMock.mockResolvedValue({ items: muchos, has_more: true })
    const user = userEvent.setup()
    await abrir(user)
    expect(
      await screen.findByText('Loaded 1000 cost centres; there are more'),
    ).toBeInTheDocument()
  })

  it('CONTRAFACTUAL · sin recorte no hay aviso', async () => {
    centrosMock.mockResolvedValue({ items: [CC], has_more: false })
    const user = userEvent.setup()
    await abrir(user)
    // Cota: la pantalla SÍ pintó la lista (si no, la ausencia del aviso no diría nada).
    expect(await screen.findByText('ENG-01')).toBeInTheDocument()
    expect(screen.queryByText(/there are more/i)).not.toBeInTheDocument()
  })

  it('lista los centros al abrir la pestaña', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(await screen.findByText('ENG-01')).toBeInTheDocument()
  })

  /**
   * ⛔ LA CELDA QUE FALTABA, Y SU AUSENCIA COSTÓ LA PANTALLA ENTERA. La de arriba afirma sobre
   * `ENG-01` — el CÓDIGO — y ninguna de las 54 celdas de finops afirmaba el NOMBRE. Mientras tanto
   * la lista leía `c.cc_name`, un campo que el motor no envía (su DTO dice `json:"name"`,
   * `modules/finops/costcenter.go:33`), y como la forma inline lo declaraba OPCIONAL, TypeScript
   * tampoco se quejaba: **todos los centros de coste se pintaban sin nombre**.
   *
   * Y la fixture de este fichero decía `cc_name`, o sea que el oráculo estaba copiado del sujeto.
   * Un oráculo que sale del defecto no puede discrepar de él.
   *
   * EL MUTANTE: devolver `c.cc_name` en el render. Esta celda se pone roja; la de arriba, no.
   */
  /**
   * ⛔ `""` NO ES «activo», y ésta es la celda que lo fija. La atribución de coste exige EXACTAMENTE
   * `"active"`, así que un centro con el estado sin fijar **no atribuye nada** — y hasta hoy podía
   * quedarse así, porque el `PUT` es un reemplazo completo y `validate()` defaulteaba en una copia.
   *
   * EL MUTANTE: colapsar el desconocido a `active` (un `!== 'archived' ? 'active'`). La lista
   * pintaría como sano justo el caso en que el producto no funciona.
   */
  it('un estado vacío es DESCONOCIDO, no activo', async () => {
    expect(estadoCentro('active')).toBe('active')
    expect(estadoCentro('archived')).toBe('archived')
    expect(estadoCentro('')).toBe('unknown')
    expect(estadoCentro(undefined)).toBe('unknown')
    expect(estadoCentro('lo-que-sea')).toBe('unknown')
  })

  /**
   * ⛔ EL `PUT` ES UN REEMPLAZO COMPLETO: el motor escribe code, name, description, owner y status
   * con lo que llegue, así que **omitir un campo lo BORRA**. Esta celda afirma el CUERPO entero,
   * no que «se llamó»: mandar un parche es exactamente el defecto que destruiría el código del
   * centro y dejaría su estado vacío — o sea, dejaría de atribuir.
   */
  it('al editar, manda el registro ENTERO y conserva código y estado', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(
      await screen.findByRole('button', { name: /edit cost centre/i }),
    )
    const dialog = await screen.findByRole('dialog')
    const nombre = within(dialog).getByLabelText(/^name/i)
    await user.clear(nombre)
    await user.type(nombre, 'Plataforma')
    await user.click(within(dialog).getByRole('button', { name: /^save$/i }))

    await waitFor(() => expect(actualizarMock).toHaveBeenCalled())
    const [id, cuerpo] = actualizarMock.mock.calls[0] as [
      string,
      Record<string, unknown>,
    ]
    expect(id).toBe('cc-1')
    expect(cuerpo).toMatchObject({
      code: 'ENG-01',
      name: 'Plataforma',
      status: 'active',
      owner: 'cto@demo',
    })
  })

  /**
   * ⛔ ARCHIVAR TAMPOCO PUEDE SER `{status:'archived'}` A SECAS, por la misma razón: sería un parche
   * sobre un endpoint de reemplazo y borraría el resto. Y la dirección que no debe dispararse: sobre
   * un centro ya archivado, el mismo botón RESTAURA.
   */
  it('al archivar, manda el registro entero con el estado cambiado', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(
      await screen.findByRole('button', { name: /archive cost centre/i }),
    )
    await waitFor(() => expect(actualizarMock).toHaveBeenCalled())
    const [, cuerpo] = actualizarMock.mock.calls[0] as [
      string,
      Record<string, unknown>,
    ]
    expect(cuerpo).toMatchObject({
      code: 'ENG-01',
      name: 'Ingeniería',
      status: 'archived',
    })
  })

  it('pinta el NOMBRE del centro, no sólo su código', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(await screen.findByText('Ingeniería')).toBeInTheDocument()
  })

  /**
   * ⛔ Y LA OTRA MITAD, EN EL CABLE: el diálogo recogía el nombre y lo enviaba como `cc_name`, que
   * el motor ignora en silencio porque su handler no usa `DisallowUnknownFields`. El usuario
   * tecleaba un nombre y el centro nacía vacío.
   *
   * Se afirma sobre el CUERPO de la petición, no sobre «se llamó al método»: llamar con la forma
   * equivocada es exactamente el defecto, así que una aserción de invocación lo habría dejado
   * pasar. Y se afirma también la AUSENCIA de `cc_name`, porque mandar los dos también valdría
   * para el motor y volvería a esconder el error.
   */
  it('al crear, manda `name` en el cuerpo — y NO `cc_name`', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(
      await screen.findByRole('button', { name: /new cost centre/i }),
    )
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText(/^code/i), 'FIN-02')
    await user.type(within(dialog).getByLabelText(/^name/i), 'Finanzas')
    await user.click(within(dialog).getByRole('button', { name: /^create$/i }))

    await waitFor(() => expect(crearMock).toHaveBeenCalled())
    const cuerpo = crearMock.mock.calls[0][0] as Record<string, unknown>
    expect(cuerpo).toMatchObject({ code: 'FIN-02', name: 'Finanzas' })
    expect(Object.keys(cuerpo)).not.toContain('cc_name')
  })

  /**
   * ⛔ EL CONTROL QUE MÁS IMPORTA, y no es de formato: **las reglas se aplican EN LA INGESTA** y se
   * graban en cada fila (`ingest.go:71-72`, `schema.go:151-154`). Una regla nueva **no reatribuye
   * el gasto ya registrado**.
   *
   * EL MUTANTE: quitar el aviso. La pantalla queda correcta y silenciosa, y quien crea una regla
   * para arreglar los «410 000 € sin atribuir» que la pestaña de valor enseña vuelve mañana, ve
   * el MISMO número, y concluye que el producto no funciona. El aviso va ARRIBA y ANTES de crear
   * nada, no en una nota al pie: condiciona la decisión, no la explica después.
   */
  it('avisa de que una regla no reatribuye el gasto ya registrado', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(
      await screen.findByText(/does NOT re-attribute spend already recorded/i),
    ).toBeInTheDocument()
  })

  /**
   * ⛔ EL SEGUNDO: `resolveCostCenter` consulta **una sola regla por dimensión**
   * (`costcenter.go:427`, `Limit: 1`) y la prioridad sólo decide **entre dimensiones distintas**.
   * Dos reglas con la MISMA dimensión y la MISMA clave son un empate que el motor resuelve
   * cogiendo la que le dé el store — no la de mayor prioridad.
   *
   * EL MUTANTE: presentar las reglas como una lista ordenada por prioridad. La pantalla afirmaría
   * que la de prioridad 900 gana, y el motor puede aplicar la de 100. Quien audite por qué un
   * coste fue a parar a otro centro leería una explicación falsa **en la pantalla que existe para
   * explicarlo**.
   */
  it('marca como ambigua una pareja dimensión+clave duplicada', async () => {
    reglasMock.mockResolvedValue({
      items: [
        {
          id: 'm1',
          source_dimension: 'team',
          source_key: 'plataforma',
          priority: 900,
        },
        {
          id: 'm2',
          source_dimension: 'team',
          source_key: 'plataforma',
          priority: 100,
        },
      ],
    })
    const user = userEvent.setup()
    await abrir(user)
    await user.click(await screen.findByText('ENG-01'))
    const avisos = await screen.findAllByText(/engine picks one arbitrarily/i)
    // Las DOS reglas del empate se marcan, no sólo la perdedora: desde la pantalla no se sabe
    // cuál aplicará, que es justo lo que hay que decir.
    expect(avisos).toHaveLength(2)
  })

  /**
   * LA DIRECCIÓN QUE NO DEBE DISPARAR: dos reglas de la misma dimensión con claves DISTINTAS no
   * compiten entre sí — cada una casa su propio valor. Sin esta casilla, una pantalla que gritara
   * «ambigua» en cuanto ve dos reglas de la misma dimensión pasaría la de arriba y convertiría la
   * configuración normal en una alarma permanente, que es como se deja de leer una alarma.
   */
  it('no marca ambigüedad cuando las claves difieren', async () => {
    reglasMock.mockResolvedValue({
      items: [
        {
          id: 'm1',
          source_dimension: 'team',
          source_key: 'plataforma',
          priority: 900,
        },
        {
          id: 'm2',
          source_dimension: 'team',
          source_key: 'datos',
          priority: 100,
        },
      ],
    })
    const user = userEvent.setup()
    await abrir(user)
    await user.click(await screen.findByText('ENG-01'))
    expect(await screen.findByText('plataforma')).toBeInTheDocument()
    expect(screen.queryByText(/engine picks one arbitrarily/i)).toBeNull()
  })

  /**
   * Y sin reglas el centro no está «vacío»: el tráfico se queda SIN ATRIBUIR, que es un hallazgo
   * — lo dice el propio esquema («unmapped traffic — a useful finding in itself»).
   */
  it('sin reglas dice que el tráfico se queda sin atribuir', async () => {
    const user = userEvent.setup()
    await abrir(user)
    await user.click(await screen.findByText('ENG-01'))
    expect(
      await screen.findByText(/traffic stays unattributed/i),
    ).toBeInTheDocument()
  })
  /**
   * ⛔ `effective_until` VACÍO no es «le falta la fecha»: es que **la tarifa sigue vigente**
   * (`ratecatalog.go:26-28` — «a null/empty effective_until means the rate is still current»).
   *
   * EL MUTANTE: pintar un guion. Se lee como un registro incompleto, y en una tabla ordenable por
   * caducidad esconde justo la tarifa que se está aplicando hoy — la que multiplica cada token de
   * cada estimación de coste del producto.
   */
  it('una tarifa sin fecha de fin se dice VIGENTE, no con un guion', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(await screen.findByText('current')).toBeInTheDocument()
  })

  /** LA DIRECCIÓN QUE NO DEBE DISPARAR: con fecha de fin NO se dice vigente. */
  it('una tarifa con fecha de fin no se dice vigente', async () => {
    tarifasMock.mockResolvedValue({
      items: [{ ...TARIFA, effective_until: '2026-06-30T00:00:00Z' }],
    })
    const user = userEvent.setup()
    await abrir(user)
    expect(await screen.findByText(/until 2026-06-30/i)).toBeInTheDocument()
    expect(screen.queryByText('current')).toBeNull()
  })

  /**
   * Y la UNIDAD se dice, no se supone: micro-USD ENTEROS por 1M de tokens, sin floats
   * (`ratecatalog.go:22-23`). Sin la etiqueta, «15000000» se lee como dólares y el lector se
   * equivoca en seis órdenes de magnitud sobre el precio de su propio modelo.
   */
  it('declara la unidad del catálogo', async () => {
    const user = userEvent.setup()
    await abrir(user)
    expect(
      await screen.findByText(/micro-USD per 1M tokens/i),
    ).toBeInTheDocument()
  })
})
