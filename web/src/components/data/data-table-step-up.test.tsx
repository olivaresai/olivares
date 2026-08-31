// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ LA RAMA DE ERROR DE ESTA TABLA DECIDE POR MEDIA CONSOLA. Radio medido por AST en el
// contraste `sol max`, y corrige la cifra que yo había escrito de memoria: 52 ficheros y 87
// usos PRODUCTIVOS (53/101 mezclaba las tablas del propio banco de pruebas). De esos 87, los
// que pueden entrar aquí son los 45 que pasan `error` — y los 45 pasan también `onRetry`, así
// que la salida de la ceremonia existe en todos. Los otros 42 no montan esta rama nunca. Hasta este cambio leía `isForbidden` primero —que es SÓLO el status
// (lib/api/errors.ts:59)— así que un `step_up_required`, que satisface los dos, salía como
// «no tienes autorización»: una acusación falsa, sin salida, en cualquier tabla que el motor
// refuse por aseguramiento.
//
// Va en fichero aparte del banco grande de `data-table.test.tsx` por una razón: ese banco
// mockea nada y monta tablas de 100k filas. Aquí hace falta un doble de la ceremonia, y un
// `vi.mock` es de MÓDULO — contaminaría las 20 celdas de virtualización que no lo piden.
import type { TableColumn } from '@/components/data/data-table'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { EmptyState } from '@/components/ui/empty-state'
import { ApiError } from '@/lib/api/errors'

// ⛔ SE DOBLA EL PANEL INTERNO, NO EL ENVOLTORIO, y el matiz es todo: doblando
// `StepUpRequiredState` la celda no habría visto nunca que ese envoltorio no se anunciaba a
// tecnología de asistencia —lo cazó el contraste `sol max`, no yo—. Doblando sólo el panel
// (el `lazy()` de step-up-state.tsx:24) el envoltorio REAL se renderiza y queda bajo prueba.
// Y el doble expone `onElevated`, que es la ÚNICA salida de la negativa: un doble sin props
// deja pasar `onElevated={undefined}` en el sitio productivo sin que nadie se entere.
vi.mock('@/features/identity/assurance', () => ({
  StepUpPanel: ({
    action,
    onElevated,
  }: {
    minAal: number
    currentAal: number
    action: string
    className?: string
    onElevated?: () => void
  }) => (
    <div>
      <span>step-up ceremony</span>
      <span>{`action:${action}`}</span>
      <button type="button" onClick={() => onElevated?.()}>
        elevar
      </button>
    </div>
  ),
}))

import { DataTable } from './data-table'

interface Row {
  id: string
  name: string
}

const columns: TableColumn<Row, string>[] = [
  { accessorKey: 'name', header: 'Name' },
]
const EMPTY = <EmptyState title="Nothing here" />

const stepUp = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')

// ⛔ ESTA TABLA YA TRAE UN `role="status"` PROPIO: el anunciador sr-only de la transición
// busy→idle (data-table.tsx:429), que está en TODAS las tablas y en todos los estados. Es un
// extraño que satisface por sí solo cualquier aserción sobre `role="status"` — mi primera
// versión de estas celdas se rompió contra él («Found multiple elements with the role
// status»), que es la forma amable de romperse; la forma cara habría sido pasar. Así que la
// consulta se recorta al SUJETO, y la exclusión se VERIFICA abajo en vez de declararse.
// Y el criterio es un TOKEN de clase, no una subcadena: `className.includes('sr-only')` da
// verdadero para `not-sr-only`, que es la utility que lo hace VISIBLE. El contraste mutó el
// anunciador a `not-sr-only` y las cuatro celdas siguieron verdes con la región live a la
// vista. `classList.contains` compara tokens y no se traga eso.
// Deja pasar los frames en los que el efecto de foco de la rejilla actuaría. Afirmar una
// AUSENCIA en el mismo tick no distingue «no se movió» de «todavía no le ha dado tiempo»:
// el efecto enfoca dentro de un requestAnimationFrame (data-table.tsx:280-289), así que sin
// esto el mutante que sólo respeta `defaultPrevented` para ArrowDown seguía verde.
const dosFrames = async () => {
  await new Promise<void>((r) => requestAnimationFrame(() => r()))
  await new Promise<void>((r) => requestAnimationFrame(() => r()))
}

const stateStatus = () =>
  screen
    .queryAllByRole('status')
    .filter((el) => !el.classList.contains('sr-only'))
const announcer = () =>
  screen
    .queryAllByRole('status')
    .filter((el) => el.classList.contains('sr-only'))

describe('DataTable — los dos 403 no son el mismo, y esta tabla los ve por 45 sitios', () => {
  it('ofrece la CEREMONIA cuando el motor refusa por aseguramiento', async () => {
    render(
      <DataTable columns={columns} data={[]} error={stepUp()} empty={EMPTY} />,
    )

    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()
    // Con la acción que le toca: `action` elige la copy que el operador lee
    // (features/identity/i18n/en.json), así que un valor equivocado NOMBRA otra cosa. Sin
    // esta línea, mutar `generic` a `wif` sobrevivía.
    expect(screen.getByText('action:generic')).toBeInTheDocument()
    // Y SE ANUNCIA: la región live de la tabla se vacía en cuanto hay error (:429-434), así
    // que si el estado no se autoanuncia el lector de pantalla pasa de «cargando» a silencio.
    expect(stateStatus()).toHaveLength(1)
    // Y no la acompaña ninguna de las otras dos respuestas: ni la avería roja
    // (`role="alert"`, error-state.tsx:41) ni la frontera de permiso (`role="status"`, :99).
    // Anclado a lo POSITIVO, que ya está en pantalla: una ausencia sola se cumpliría en el
    // primer tick, antes de que nada hubiera podido pintarse.
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    // Y no acompaña a la acusación: «Not authorized» es la copy EXACTA de ForbiddenState
    // (lib/i18n/locales/en/errors.json:8), no un rol genérico que cualquier estado satisface.
    expect(screen.queryByText('Not authorized')).not.toBeInTheDocument()
    // La exclusión se comprueba, no se declara: el anunciador sr-only SIGUE ahí. Si el filtro
    // se llevara todo por delante, la aserción de arriba pasaría sin mirar nada.
    expect(announcer()).toHaveLength(1)
  })

  it('y elevar REINTENTA la lectura refusada, que es la salida', async () => {
    const onRetry = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={[]}
        error={stepUp()}
        onRetry={onRetry}
        empty={EMPTY}
      />,
    )

    await userEvent.click(
      await screen.findByRole('button', { name: /elevar/i }),
    )
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('un 403 SIN código de ceremonia conserva la negativa de ROL', () => {
    // Control negativo, y es la mitad que de verdad protege el cambio: la negativa de rol
    // es CIERTA y se queda como está. Afirmar sólo «sale la ceremonia» dejaría verde un
    // cambio que mandara los dos 403 a la ceremonia, que es el defecto simétrico.
    render(
      <DataTable
        columns={columns}
        data={[]}
        error={new ApiError(403, 'forbidden', 'no')}
        empty={EMPTY}
      />,
    )

    // ⛔ POR SU COPY EXACTA, no por `role="status"`: el contraste mutó `ForbiddenState` a
    // `EmptyState` —que TAMBIÉN lleva role="status" (components/ui/empty-state.tsx:36)— y
    // esta celda, titulada «conserva la negativa de ROL», siguió verde enseñando «Nothing
    // here» a quien de verdad no tiene el permiso. Un rol no identifica un estado.
    expect(screen.getByText('Not authorized')).toBeInTheDocument()
    expect(stateStatus()).toHaveLength(1)
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })

  it('el teclado sobre la ceremonia INICIA la ceremonia, no la fila que quedó debajo', async () => {
    // ⛔ EL DEFECTO QUE EL CONTRASTE REPRODUJO, Y NO LO INTRODUJO LA CEREMONIA. Cuando llega
    // `error` el cuerpo pinta el estado, pero `rows` de TanStack conserva las filas y el
    // roving grid seguía navegando: Space sobre CUALQUIER descendiente hacía preventDefault y
    // activaba la fila vieja (data-table.tsx:330). Medido por `sol max`: `onRetry=0`,
    // `onRowClick=1` — el teclado no empezaba la ceremonia y abría otra cosa. El botón real
    // que dispara WebAuthn es descendiente de esta tabla (features/identity/assurance.tsx:158),
    // así que esto dejaba la única salida de la negativa inalcanzable sin ratón.
    // El mismo agujero se lo comía el botón «reintentar» de ErrorState desde siempre.
    const onRetry = vi.fn()
    const onRowClick = vi.fn()
    const row = [{ id: 'r1', name: 'row-1' }]
    const { rerender } = render(
      <DataTable
        columns={columns}
        data={row}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )

    // Se ENTRA en la rejilla, que es lo que arma el estado `active`.
    screen.getByRole('grid').focus()
    await userEvent.keyboard('{ArrowDown}')

    // Y llega la negativa CON las filas conservadas, que es el caso real: react-query
    // mantiene los datos anteriores mientras refetch.
    rerender(
      <DataTable
        columns={columns}
        data={row}
        onRowClick={onRowClick}
        error={stepUp()}
        onRetry={onRetry}
        empty={EMPTY}
      />,
    )

    const elevar = await screen.findByRole('button', { name: /elevar/i })
    elevar.focus()
    // Misma carrera que en la celda del botón de orden: se comprueba dónde está el foco
    // antes de teclear, para que un fallo signifique el sujeto y no el reloj.
    expect(document.activeElement).toBe(elevar)
    await userEvent.keyboard(' ')

    expect(onRetry).toHaveBeenCalledTimes(1)
    expect(onRowClick).not.toHaveBeenCalled()
  })

  it('con la negativa puesta, una tecla sobre la TABLA no navega las filas ocultas', async () => {
    // ⛔ UNA CELDA POR INVARIANTE. Mi control anterior quitaba la guarda Y el efecto a la vez,
    // así que no medía ninguno de los dos: el contraste `sol max` quitó SÓLO `error ||` y las
    // cinco celdas siguieron verdes, porque el efecto limpiaba `active` antes del Space. Es mi
    // propia regla —un mutante sobre un invariante no mide nada si otro redundante bloquea el
    // mismo camino— y me la salté. Ésta aísla la GUARDA: la tecla llega desde la tabla misma,
    // no desde un control, que es el único camino que la otra línea no cubre.
    const onRowClick = vi.fn()
    const row = [{ id: 'r1', name: 'row-1' }]
    render(
      <DataTable
        columns={columns}
        data={row}
        onRowClick={onRowClick}
        error={stepUp()}
        onRetry={vi.fn()}
        empty={EMPTY}
      />,
    )
    await screen.findByText('step-up ceremony')

    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    await userEvent.keyboard(' ')

    expect(onRowClick).not.toHaveBeenCalled()
  })

  it('la negativa SUELTA el foco activo: la rejilla vuelve a ser un solo tab stop', async () => {
    // Y ésta aísla el EFECTO. Sin él, la tabla se queda con `tabindex="-1"` —el valor que
    // significa «hay una celda activa dentro»— apuntando a una celda que el error ya
    // desmontó: el operador pierde el tab stop de la rejilla. El contraste lo cazó con el
    // mutante que borra sólo el efecto: 5/5 verdes.
    const row = [{ id: 'r1', name: 'row-1' }]
    const { rerender } = render(
      <DataTable columns={columns} data={row} empty={EMPTY} />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    // Control positivo: con celda activa, la rejilla NO es el tab stop.
    expect(grid).toHaveAttribute('tabindex', '-1')

    rerender(
      <DataTable
        columns={columns}
        data={row}
        error={stepUp()}
        onRetry={vi.fn()}
        empty={EMPTY}
      />,
    )
    await screen.findByText('step-up ceremony')
    expect(screen.getByRole('grid')).toHaveAttribute('tabindex', '0')
  })

  it('el botón de ORDEN de la cabecera ordena, y no abre la fila activa', async () => {
    // El mismo secuestro, en el control que lo tiene desde el principio y que el contrato de
    // la tabla ya prometía operable (data-table.tsx:57-59). No es de la ceremonia: es de
    // CUALQUIER control dentro del <table>, y por eso se arregla en el manejador y no con un
    // parche para el error. Entrada medida por `sol max`: rejilla → ArrowDown → el botón
    // «Name» → Space daba onRowClick=1 y aria-sort seguía en `none`.
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={[
          { id: 'r1', name: 'b' },
          { id: 'r2', name: 'a' },
        ]}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')

    // ⛔ ESPERAR A QUE EL FOCO ASIENTE, y no es paranoia: la rejilla enfoca su celda activa
    // dentro de un `requestAnimationFrame` (data-table.tsx:280-289). En aislado ese rAF corre
    // antes de la línea siguiente; con la suite entera y la caja cargada aterrizaba DESPUÉS de
    // mi `.focus()` y le robaba el foco al botón, así que Space acababa en la celda y la celda
    // fallaba por una carrera, no por el sujeto. 2060 verdes y ésta roja: exactamente el
    // aspecto que tiene una prueba que mide el reloj en vez del código.
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))

    const sortBtn = screen.getByRole('button', { name: /name/i })
    sortBtn.focus()
    expect(document.activeElement).toBe(sortBtn)
    await userEvent.keyboard(' ')

    expect(onRowClick).not.toHaveBeenCalled()
    expect(screen.getByRole('columnheader', { name: /name/i })).toHaveAttribute(
      'aria-sort',
      'ascending',
    )
  })

  it('con la negativa puesta, aria-rowcount no anuncia filas que nadie puede recorrer', async () => {
    render(
      <DataTable
        columns={columns}
        data={[
          { id: 'r1', name: 'a' },
          { id: 'r2', name: 'b' },
        ]}
        error={stepUp()}
        onRetry={vi.fn()}
        empty={EMPTY}
      />,
    )
    await screen.findByText('step-up ceremony')
    // Queda la cabecera. Antes decía 3, describiendo una rejilla de dos filas que el cuerpo
    // ya había sustituido por el estado.
    // DOS: la cabecera y la fila del estado, que es lo que el árbol accesible expone de
    // verdad (medido por el contraste con Testing Library y con Chromium). Puse 1 y era
    // otra cifra falsa, sólo que en la otra dirección; el literal no es la verdad.
    expect(screen.getByRole('grid')).toHaveAttribute('aria-rowcount', '2')
    expect(screen.getAllByRole('row')).toHaveLength(2)
  })

  it('el botón de orden también responde a ENTER, y tampoco abre la fila', async () => {
    // ⛔ SPACE NO ES «EL TECLADO». El contraste mutó la guarda para que protegiera SÓLO
    // Space y las nueve celdas siguieron verdes, porque la única que la ejercía pulsaba
    // Space: con Enter, el botón seguía abriendo la fila en vez de ordenar. Un invariante
    // sobre un conjunto de teclas no queda fijado por una sola de ellas.
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={[
          { id: 'r1', name: 'b' },
          { id: 'r2', name: 'a' },
        ]}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))

    const sortBtn = screen.getByRole('button', { name: /name/i })
    sortBtn.focus()
    expect(document.activeElement).toBe(sortBtn)
    await userEvent.keyboard('{Enter}')

    expect(onRowClick).not.toHaveBeenCalled()
    expect(screen.getByRole('columnheader', { name: /name/i })).toHaveAttribute(
      'aria-sort',
      'ascending',
    )
  })

  it('desde un control de celda, las FLECHAS siguen navegando la rejilla', async () => {
    // ⛔ Y LA MITAD QUE FALTABA, que es una regresión que introduje yo: retirarme ante TODA
    // tecla nacida en un control dejaba la rejilla sin navegación en cuanto el foco estaba
    // en un botón de celda —el menú de `orchestration/components.tsx:478-550` es real—.
    // APG dice justo lo contrario: una celda con un solo widget conserva las flechas. La
    // frontera es qué tecla POSEE cada cosa: la activación es del botón, la navegación de
    // la rejilla.
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={[
          { accessorKey: 'name', header: 'Name' },
          { accessorKey: 'kind', header: 'Kind' },
          {
            id: 'acciones',
            header: 'Acciones',
            cell: () => (
              <button type="button" tabIndex={-1}>
                menú
              </button>
            ),
          },
        ]}
        data={[{ id: 'r1', name: 'a', kind: 'x' }]}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}{ArrowRight}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    // La celda activa es la 2ª columna. Ahora el foco pasa al botón que vive dentro.
    const menu = screen.getByRole('button', { name: /menú/i })
    menu.focus()
    expect(document.activeElement).toBe(menu)

    await userEvent.keyboard('{ArrowLeft}')

    // ⛔ TRES columnas y el control en la TERCERA, a propósito: con dos, «una columna a la
    // izquierda» y «la columna 1» son el mismo sitio, y el contraste dejó vivo un mutante que
    // fijaba `c: 0`. Con tres, la respuesta correcta es la 2 y la del mutante es la 1.
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    expect(document.activeElement).toHaveAttribute('aria-colindex', '2')
    // Y navegar no activa nada.
    expect(onRowClick).not.toHaveBeenCalled()
  })

  it('una entrada de texto en celda posee TODAS sus teclas, incluida `plaintext-only`', async () => {
    // El contraste midió que el selector sólo reconocía `[contenteditable="true"]`, y
    // `plaintext-only` (y el valor vacío) son igual de editables según HTML §6.8.1: con el
    // editor enfocado, Space abría la fila en vez de escribir. No hay hoy un callsite
    // productivo así, pero el defecto está en la primitiva que usan las 52, y una rama sin
    // celda es una rama que alguien revierte sin enterarse.
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={[
          { accessorKey: 'name', header: 'Name' },
          {
            id: 'nota',
            header: 'Nota',
            cell: () => (
              <div
                contentEditable="plaintext-only"
                suppressContentEditableWarning
              >
                nota
              </div>
            ),
          },
        ]}
        data={[{ id: 'r1', name: 'a' }]}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))

    const editor = screen.getByText('nota')
    editor.focus()
    expect(document.activeElement).toBe(editor)
    await userEvent.keyboard(' ')

    expect(onRowClick).not.toHaveBeenCalled()
  })

  it('si el control ya consumió la tecla, la rejilla NO la procesa otra vez', async () => {
    // ⛔ LA REGLA DE COMPOSICIÓN. Un menú de celda real (Radix, en
    // `orchestration/components.tsx:478-550`) usa ArrowDown para ABRIRSE y llama a
    // `preventDefault`. Dejar pasar las teclas de navegación «porque vienen de un botón»
    // hacía que la rejilla las procesara también: el menú se abría y la fila activa se movía
    // en silencio. Aquí el doble hace exactamente lo que hace Radix — consumir la tecla.
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={[
          { accessorKey: 'name', header: 'Name' },
          {
            id: 'menu',
            header: 'Menú',
            cell: () => (
              <button
                type="button"
                tabIndex={-1}
                onKeyDown={(e) => {
                  // Como Radix: el widget consume las suyas.
                  if (e.key === 'ArrowDown' || e.key === 'Home')
                    e.preventDefault()
                }}
              >
                abrir
              </button>
            ),
          },
        ]}
        data={[
          { id: 'r1', name: 'a' },
          { id: 'r2', name: 'b' },
        ]}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    const activa = document.activeElement as HTMLElement
    const filaAntes = activa.closest('tr')?.getAttribute('aria-rowindex')

    // Dos filas ⇒ dos botones «abrir»: el sujeto es el de la fila ACTIVA, no «el botón».
    const abrir = screen.getAllByRole('button', { name: /abrir/i })[0]
    abrir.focus()

    // ⛔ LA ASERCIÓN ES DÓNDE QUEDA EL FOCO, no dónde queda la fila. Mi primera versión
    // comprobaba la fila, y con `Home` eso no distingue nada: la rejilla la habría procesado
    // moviéndose a la columna 0 de LA MISMA fila. Por eso el mutante que sólo respetaba
    // `defaultPrevented` para ArrowDown seguía verde. Si la rejilla procesa la tecla, mueve
    // `active` y su efecto se lleva el foco a una celda — así que el foco ES la señal.
    // DOS teclas consumidas, no una, por la misma razón que lo era el ArrowLeft de antes.
    await userEvent.keyboard('{ArrowDown}')
    await dosFrames()
    expect(document.activeElement).toBe(abrir)
    await userEvent.keyboard('{Home}')
    await dosFrames()
    expect(document.activeElement).toBe(abrir)

    // Y la fila activa tampoco se movió: al volver a la rejilla, sigue donde se dejó.
    grid.focus()
    await userEvent.keyboard('{ArrowLeft}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    expect(
      (document.activeElement as HTMLElement).closest('tr'),
    ).toHaveAttribute('aria-rowindex', filaAntes!)
  })

  it('desde un control, TODAS las de navegación siguen siendo de la rejilla, no sólo una', async () => {
    // El contraste dejó vivo un mutante que sólo dejaba pasar ArrowLeft: mi celda anterior
    // ejercía justo esa. `Home` es de la misma familia y basta para separar «dejo pasar las
    // NAV_KEYS» de «dejo pasar la que probaron».
    render(
      <DataTable
        columns={[
          { accessorKey: 'name', header: 'Name' },
          { accessorKey: 'kind', header: 'Kind' },
          {
            id: 'acciones',
            header: 'Acciones',
            cell: () => (
              <button type="button" tabIndex={-1}>
                menú
              </button>
            ),
          },
        ]}
        data={[{ id: 'r1', name: 'a', kind: 'x' }]}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}{ArrowRight}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))

    const menu = screen.getByRole('button', { name: /menú/i })
    menu.focus()
    expect(document.activeElement).toBe(menu)
    await userEvent.keyboard('{Home}')

    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    expect(document.activeElement).toHaveAttribute('aria-colindex', '1')
  })

  it('llegando por TAB a un control de otra fila, la flecha parte de ESA celda', async () => {
    // ⛔ LA ENTRADA QUE MIS DOS CELDAS DE NAVEGACIÓN NO EJERCÍAN, y por eso no vieron el
    // defecto: las dos prearmaban `active` y enfocaban botones con `tabIndex={-1}`. El menú
    // productivo (`orchestration/components.tsx:478-500`) NO lleva tabIndex=-1, así que es
    // alcanzable por Tab sin pasar por la rejilla — y entonces `active` es null o apunta a
    // otra fila. Medido por el contraste `sol max` con el SchedulesTable real: ArrowLeft
    // desde el menú de la SEGUNDA fila aterrizaba en la primera celda de la PRIMERA.
    render(
      <DataTable
        columns={[
          { accessorKey: 'name', header: 'Name' },
          {
            id: 'menu',
            header: 'Menú',
            cell: ({ row }) => (
              <button type="button">{`abrir ${row.original.id}`}</button>
            ),
          },
        ]}
        data={[
          { id: 'r1', name: 'a' },
          { id: 'r2', name: 'b' },
        ]}
        empty={EMPTY}
      />,
    )

    // Nada de prearmar: se llega al control de la SEGUNDA fila directamente, como con Tab.
    const menu2 = screen.getByRole('button', { name: /abrir r2/i })
    menu2.focus()
    expect(document.activeElement).toBe(menu2)

    await userEvent.keyboard('{ArrowLeft}')

    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    const celda = document.activeElement as HTMLElement
    // Segunda fila de datos: aria-rowindex 3 (la cabecera es 1). Y la columna de la
    // izquierda: 1. Antes daba fila 2, columna 1 — otra fila.
    expect(celda.closest('tr')).toHaveAttribute('aria-rowindex', '3')
    expect(celda).toHaveAttribute('aria-colindex', '1')
  })

  it('Enter/Space activan la fila DEL FOCO, no la que quedó marcada antes', async () => {
    // ⛔ EL INVARIANTE QUE INTRODUJE AL ARREGLAR LA NAVEGACIÓN, y que casi no pruebo: si la
    // posición sale de la celda enfocada, la ACTIVACIÓN tiene que salir de la misma o las
    // dos se separan. El typecheck real me obligó a mirarlo (`active` dejó de estar
    // estrechado) y lo que había detrás no era un error de tipos: Space habría abierto una
    // fila DISTINTA de aquella donde está el foco en cuanto las dos dejaran de coincidir.
    const onRowClick = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={[
          { id: 'r1', name: 'a' },
          { id: 'r2', name: 'b' },
        ]}
        onRowClick={onRowClick}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))

    // El foco se lleva a la celda de la SEGUNDA fila sin tocar `active`, que es lo que pasa
    // cuando se llega a una celda por fuera de las flechas.
    const filas = screen.getAllByRole('row')
    const celda2 = filas[2].querySelector(
      'td[aria-colindex="1"]',
    ) as HTMLElement
    celda2.focus()
    expect(document.activeElement).toBe(celda2)

    await userEvent.keyboard(' ')

    expect(onRowClick).toHaveBeenCalledTimes(1)
    expect(onRowClick).toHaveBeenCalledWith({ id: 'r2', name: 'b' })

    // Y con ENTER, que es la otra tecla de activación: el contraste dejó vivo un mutante que
    // devolvía sólo el caso de Enter a `active`, porque esta celda pulsaba únicamente Space.
    onRowClick.mockClear()
    celda2.focus()
    await userEvent.keyboard('{Enter}')
    expect(onRowClick).toHaveBeenCalledTimes(1)
    expect(onRowClick).toHaveBeenCalledWith({ id: 'r2', name: 'b' })
  })

  it('desde la CABECERA se baja a la columna señalada, no a la marca del cuerpo', async () => {
    // ⛔ La cabecera también es una posición. Sus `th` llevan aria-colindex y su fila
    // aria-rowindex=1, pero el foco llega ahí por el BOTÓN de orden, y mirando sólo `td` la
    // navegación caía en la marca del CUERPO: con active en fila 2/columna 2 y el foco en la
    // cabecera de la columna 1, ArrowDown se iba a la fila 2/columna 2. Lo midió `sol max`.
    render(
      <DataTable
        columns={[
          { accessorKey: 'name', header: 'Name' },
          { accessorKey: 'kind', header: 'Kind' },
        ]}
        data={[
          { id: 'r1', name: 'a', kind: 'x' },
          { id: 'r2', name: 'b', kind: 'y' },
        ]}
        empty={EMPTY}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    // Se deja la marca del cuerpo en la SEGUNDA fila, SEGUNDA columna.
    await userEvent.keyboard('{ArrowDown}{ArrowDown}{ArrowRight}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))

    // Y el foco se lleva a la cabecera de la PRIMERA columna.
    screen.getByRole('button', { name: /name/i }).focus()
    await userEvent.keyboard('{ArrowDown}')

    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    const celda = document.activeElement as HTMLElement
    // Primera fila de datos (aria-rowindex 2) y la columna que la cabecera señalaba.
    expect(celda.closest('tr')).toHaveAttribute('aria-rowindex', '2')
    expect(celda).toHaveAttribute('aria-colindex', '1')

    // Y con PageDown, que es la otra que baja: sin esta línea, el mutante que sólo dejaba
    // ArrowDown sobrevivía. Una pareja de teclas no queda fijada por una de ellas.
    screen.getByRole('button', { name: /name/i }).focus()
    await userEvent.keyboard('{PageDown}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    expect(
      (document.activeElement as HTMLElement).closest('tr'),
    ).toHaveAttribute('aria-rowindex', '2')
  })

  it('con la negativa puesta, «seleccionar visibles» no selecciona lo que nadie ve', async () => {
    // ⛔ EL PEOR DE TODOS LOS QUE HA SACADO EL CONTRASTE, y no es de teclado: con `error` el
    // cuerpo lo ocupa el estado, pero `rows` conserva las filas anteriores, así que la casilla
    // de la cabecera marcaba entidades que el operador NO VE y encendía la barra de acciones
    // masivas sobre ellas (`bulk-action-bar.tsx`). Medido: dos filas conservadas + error +
    // Space ⇒ Set{r1,r2}. Seleccionar es un acto sobre lo que se está mirando.
    const onSelectedIdsChange = vi.fn()
    render(
      <DataTable
        columns={columns}
        data={[
          { id: 'r1', name: 'a' },
          { id: 'r2', name: 'b' },
        ]}
        getRowId={(row) => row.id}
        selectable
        selectedIds={new Set<string>()}
        onSelectedIdsChange={onSelectedIdsChange}
        error={stepUp()}
        onRetry={vi.fn()}
        empty={EMPTY}
      />,
    )
    await screen.findByText('step-up ceremony')

    const todas = screen.getByRole('checkbox', { name: /select all/i })
    expect(todas).toBeDisabled()
    await userEvent.click(todas)
    expect(onSelectedIdsChange).not.toHaveBeenCalled()
  })

  it('si la rejilla ENCOGE, la marca se recorta y la tabla conserva su tab stop', async () => {
    // Quitar `selectable` retira la columna de selección. Con la marca en la última columna,
    // apuntaba a una celda que ya no existe y —como el tab stop de la tabla es -1 mientras
    // haya marca— la rejilla se quedaba sin NINGÚN punto de entrada por teclado.
    const props = {
      columns,
      data: [{ id: 'r1', name: 'a' }],
      getRowId: (row: Row) => row.id,
      empty: EMPTY,
    }
    const { rerender } = render(
      <DataTable
        {...props}
        selectable
        selectedIds={new Set<string>()}
        onSelectedIdsChange={vi.fn()}
      />,
    )
    const grid = screen.getByRole('grid')
    grid.focus()
    // Hasta la última columna, que es la que desaparecerá al encoger.
    await userEvent.keyboard('{ArrowDown}{End}')
    await waitFor(() => expect(document.activeElement?.tagName).toBe('TD'))
    expect(grid).toHaveAttribute('tabindex', '-1')

    rerender(<DataTable {...props} />)

    await waitFor(() => {
      const celda = screen
        .getByRole('grid')
        .querySelector('td[tabindex="0"], td[tabindex="-1"]')
      expect(celda).not.toBeNull()
    })
    // La marca cayó dentro del rango: hay celda activa o la tabla recupera el tab stop, pero
    // NO las dos en -1, que es como se queda una rejilla sin entrada.
    const tabla = screen.getByRole('grid')
    const activa = tabla.querySelector('td[tabindex="0"]')
    expect(tabla.getAttribute('tabindex') === '0' || activa !== null).toBe(true)
  })

  it('un 500 sigue siendo una avería, no una ceremonia', () => {
    // Tercer control: el cambio no puede haberse comido la rama de avería.
    render(
      <DataTable
        columns={columns}
        data={[]}
        error={new ApiError(500, 'internal', 'boom')}
        empty={EMPTY}
      />,
    )

    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })
})
