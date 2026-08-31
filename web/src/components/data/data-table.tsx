// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  type ColumnDef,
  type SortingState,
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
} from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ArrowDown, ArrowUp, ChevronsUpDown, Search } from 'lucide-react'
import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
} from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError, NetworkError } from '@/lib/api/errors'
import { usePreferencesStore } from '@/stores/preferences'

/**
 * DataTable — THE reusable tabular primitive the catalog views build on,
 * so they never reinvent sorting, density, sticky headers, or the loading / empty /
 * error / forbidden states. Sorting + global filter run client-side over the loaded
 * page; server cursor pagination is surfaced via `hasMore`/`onLoadMore`. Density
 * follows the global preference.
 *
 * Scale (ADM-CORE-07): rows are ROW-VIRTUALIZED with TanStack Virtual once the
 * loaded page exceeds `virtualizeThreshold` — an enterprise estate (tens/hundreds
 * of thousands of rows) renders only the visible window. `aria-rowcount` carries
 * the true total and every rendered row its real `aria-rowindex`, so the position
 * is announced even though only a window is in the DOM.
 *
 * Keyboard (ADM-CORE-02, APG `grid`): the table is a `role="grid"` with a single
 * tab stop into the data region; arrow keys move a roving-tabindex active cell
 * (Right/Left/Up/Down, Home/End = row ends, Ctrl+Home/End = grid corners, PageUp/
 * Down = ±10 rows). Enter/Space on the active cell activates the row (`onRowClick`).
 * Moving to a row outside the virtual window scrolls it into view first, so focus
 * is never lost at scale. Column headers stay `columnheader` cells exposing
 * `aria-sort`; their sort buttons remain operable buttons (the data grid owns the
 * single tab stop, the sort controls are reachable separately). With selection
 * enabled, Space on the first body cell toggles that row without activating it.
 */
/**
 * ⛔ EL TIPO DE COLUMNA DE ESTA TABLA, Y POR QUÉ EXISTE ESTA INDIRECCIÓN.
 *
 *    Medido el 2026-08-18: **55 ficheros importaban `ColumnDef` directamente de
 *    `@tanstack/react-table`** y CERO lo tomaban de aquí. Con eso, un cambio de versión mayor de la
 *    librería no cuesta un fichero: cuesta 56. Es exactamente lo que mide el PR #844 (v8 → v9), y su
 *    coste no lo causa v9 — lo causa **una abstracción que faltaba**.
 *
 *    En v9 la firma pasa de `ColumnDef<TData>` a **`ColumnDef<TFeatures, TData, TValue>`**
 *    (verificado en el SKILL de migración oficial de TanStack). Con este alias, ese tercer argumento
 *    se absorbe **aquí y en ningún otro sitio**: las pantallas escriben `TableColumn<Fila>` y no se
 *    enteran.
 *
 * ⚠ Y SE LLAMA DISTINTO A PROPÓSITO. Re-exportarlo como `ColumnDef` habría ahorrado renombrados,
 *   pero deja al lector de una pantalla creyendo que toca la librería — que es la ambigüedad que
 *   causó el acoplamiento. Un nombre propio hace visible la indirección.
 */
export type TableColumn<TData, TValue = unknown> = ColumnDef<TData, TValue>

export interface DataTableProps<TData> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any -- heterogeneous accessor columns
  columns: ColumnDef<TData, any>[]
  data: TData[]
  isLoading?: boolean
  /** An error from the query (ApiError / NetworkError / unknown). The engine sends TWO
   * different 403s and they do not mean the same thing: one of ROLE renders a calm "not
   * authorized" state, never a red error; one of ASSURANCE (`step_up_required`) renders
   * the ceremony that lifts it, because the operator HAS the permission and the session
   * is below AAL3. Never red for either. */
  error?: unknown
  /** Re-runs the refused read. It is also the ceremony's way out, so a table that can
   * take a 403 of assurance should pass it — all 45 that pass `error` today do. */
  onRetry?: () => void
  /** REQUIRED: what this table means when it holds NO ROWS AT ALL. There is no
   * default, deliberately — the previous one ("No results") was not a decision made
   * once, it was the ABSENCE of a decision inherited by 69 surfaces across 36 files,
   * and on a clean install every list is empty, so it was the customer's first screen.
   * An honest empty state names what is missing AND what makes a first row appear.
   *
   * `ReactElement`, not `ReactNode`, and that is the whole point: `ReactNode` includes
   * `undefined`, so a required `ReactNode` would still accept `empty={undefined}` —
   * the exact shape six call sites shipped, indistinguishable at runtime from omitting
   * the prop. `ReactElement` also rejects `null`, `false` and `''`. A caller that
   * genuinely wants nothing has to say so with a named component that renders null —
   * `<></>` is refused by `task lint:datatable-empty`, because an empty fragment is how
   * "no decision" was spelled at six sites, not how a decision is spelled. That gate is
   * also what covers the residue this type cannot: an element is not proof that
   * anything renders. */
  empty: ReactElement
  getRowId?: (row: TData, index: number) => string
  /** Opt-in controlled row selection. `getRowId` should return the stable ID sent
   * to bulk APIs; otherwise TanStack's page-relative row ID is used. */
  selectable?: boolean
  selectedIds?: Set<string>
  onSelectedIdsChange?: (ids: Set<string>) => void
  onRowClick?: (row: TData) => void
  searchable?: boolean
  searchPlaceholder?: string
  /** Observe free-text changes when the owning view also applies a server-side
   * search. The table keeps filtering the loaded rows with the same value. */
  onSearchChange?: (value: string) => void
  /** Extra controls rendered in the toolbar, right of the search box. */
  toolbar?: ReactNode
  stickyHeader?: boolean
  hasMore?: boolean
  onLoadMore?: () => void
  isFetchingMore?: boolean
  /** Accessible name for the grid (WCAG 4.1.2 / axe `aria-required-attr`). Defaults
   * to a generic label; pass a specific one (e.g. "Inventory"). */
  label?: string
  /** Enable APG grid keyboard navigation (default true). */
  gridNavigation?: boolean
  /** Force row virtualization on/off. Default: auto — on above `virtualizeThreshold`. */
  virtualized?: boolean
  /** Auto-virtualize above this many loaded rows (default 100). */
  virtualizeThreshold?: number
  /** Max height (px or CSS length) of the scroll region WHEN virtualizing. */
  maxBodyHeight?: number | string
  className?: string
}

const PAGE_JUMP = 10
const EMPTY_SELECTED_IDS = new Set<string>()

export function DataTable<TData>({
  columns,
  data,
  isLoading = false,
  error,
  onRetry,
  empty,
  getRowId,
  selectable = false,
  selectedIds,
  onSelectedIdsChange,
  onRowClick,
  searchable = false,
  searchPlaceholder,
  onSearchChange,
  toolbar,
  stickyHeader = false,
  hasMore = false,
  onLoadMore,
  isFetchingMore = false,
  label,
  gridNavigation = true,
  virtualized,
  virtualizeThreshold = 100,
  maxBodyHeight = 600,
  className,
}: DataTableProps<TData>) {
  const { t } = useTranslation('common')
  const density = usePreferencesStore((s) => s.density)
  const [sorting, setSorting] = useState<SortingState>([])
  const [globalFilter, setGlobalFilter] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)
  const gridId = useId()
  const controlledSelectedIds = selectedIds ?? EMPTY_SELECTED_IDS

  const toggleRowSelection = useCallback(
    (id: string) => {
      const next = new Set(controlledSelectedIds)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      onSelectedIdsChange?.(next)
    },
    [controlledSelectedIds, onSelectedIdsChange],
  )

  const selectionColumn = useMemo<ColumnDef<TData>>(
    () => ({
      id: '__selection',
      size: 40,
      enableSorting: false,
      enableGlobalFilter: false,
      header: ({ table: currentTable }) => {
        const visibleIds = currentTable.getRowModel().rows.map((row) => row.id)
        const selectedVisible = visibleIds.filter((id) =>
          controlledSelectedIds.has(id),
        ).length
        const allVisible =
          visibleIds.length > 0 && selectedVisible === visibleIds.length
        const someVisible = selectedVisible > 0 && !allVisible
        return (
          <Checkbox
            checked={allVisible ? true : someVisible ? 'indeterminate' : false}
            // ⛔ Y CON `error` NO SE SELECCIONA NADA, porque no hay nada a la vista: el
            // cuerpo lo ocupa el estado (:660-668) mientras `rows` conserva las filas
            // anteriores, así que «seleccionar visibles» marcaba entidades que el operador
            // NO ve y encendía la barra de acciones masivas sobre ellas. Lo midió el
            // contraste `sol max`: dos filas conservadas + error + Space ⇒ Set{r1,r2}.
            disabled={
              visibleIds.length === 0 || !onSelectedIdsChange || Boolean(error)
            }
            aria-label={t('table.selectAllVisible')}
            onClick={(event) => event.stopPropagation()}
            onCheckedChange={(checked) => {
              const next = new Set(controlledSelectedIds)
              for (const id of visibleIds) {
                if (checked === true) next.add(id)
                else next.delete(id)
              }
              onSelectedIdsChange?.(next)
            }}
          />
        )
      },
      cell: ({ row }) => (
        <Checkbox
          checked={controlledSelectedIds.has(row.id)}
          disabled={!onSelectedIdsChange}
          tabIndex={-1}
          aria-label={t('table.selectRow', { id: row.id })}
          onClick={(event) => event.stopPropagation()}
          onCheckedChange={() => toggleRowSelection(row.id)}
        />
      ),
    }),
    [controlledSelectedIds, error, onSelectedIdsChange, t, toggleRowSelection],
  )
  const tableColumns = useMemo(
    () => (selectable ? [selectionColumn, ...columns] : columns),
    [columns, selectable, selectionColumn],
  )

  const table = useReactTable({
    data,
    columns: tableColumns,
    state: { sorting, globalFilter },
    onSortingChange: setSorting,
    onGlobalFilterChange: setGlobalFilter,
    getRowId,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: searchable ? getFilteredRowModel() : undefined,
  })

  const rowH = density === 'compact' ? 'h-7' : 'h-8'
  const estRowPx = density === 'compact' ? 29 : 33
  const rows = table.getRowModel().rows
  const colCount = table.getAllLeafColumns().length

  const enableVirtual =
    (virtualized ?? rows.length > virtualizeThreshold) &&
    !isLoading &&
    !error &&
    rows.length > 0
  const sticky = stickyHeader || enableVirtual

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => estRowPx,
    overscan: 14,
    initialRect: { width: 0, height: 640 },
    measureElement:
      typeof window !== 'undefined' && !navigator.userAgent.includes('Firefox')
        ? (el) => el?.getBoundingClientRect().height
        : undefined,
  })
  const virtualItems = enableVirtual ? virtualizer.getVirtualItems() : []
  const padTop = virtualItems.length ? virtualItems[0].start : 0
  const padBottom = virtualItems.length
    ? virtualizer.getTotalSize() - virtualItems[virtualItems.length - 1].end
    : 0

  // --- APG grid keyboard navigation (roving tabindex over body cells) -----------
  const nav = gridNavigation
  const cellId = useCallback(
    (r: number, c: number) => `${gridId}-cell-${r}-${c}`,
    [gridId],
  )
  const [active, setActive] = useState<{ r: number; c: number } | null>(null)

  // ⛔ UN ESTADO NO ES UNA REJILLA. Cuando llega `error`, el cuerpo pinta el estado en vez de
  // las filas (:534) pero `rows` de TanStack conserva las anteriores, así que el roving grid
  // seguía navegando y ACTIVANDO filas que ya no están en pantalla. El contraste `sol max` lo
  // reprodujo sobre el caso peor: entrar en la rejilla, que llegue un step_up_required con las
  // filas conservadas, enfocar el botón de la ceremonia y pulsar Space → `onRetry=0`,
  // `onRowClick=1`. Es decir, el teclado NO empezaba la ceremonia y abría una fila vieja.
  // No lo introdujo la rama de ceremonia: el botón «reintentar» de ErrorState lleva ahí el
  // mismo problema desde siempre. Se arregla en la raíz — mientras haya estado, la rejilla no
  // navega, y el foco activo se suelta para no dejarlo apuntando a una celda que ya no existe.
  useEffect(() => {
    if (error) setActive(null)
  }, [error])

  // ⛔ Y SI LAS COLUMNAS ENCOGEN, la marca puede quedarse FUERA. `selectable` pasando de true
  // a false quita la columna de selección: con la marca en la última columna, `active.c`
  // apuntaba a una celda que ya no existe y —como el tab stop de la tabla es -1 mientras haya
  // marca (:445)— la rejilla se quedaba sin NINGÚN punto de entrada por teclado. Lo mismo con
  // las filas. Se recorta al rango, y sólo si hace falta, para no reenfocar por gusto.
  useEffect(() => {
    setActive((prev) => {
      if (!prev) return prev
      const r = Math.min(prev.r, rows.length - 1)
      const c = Math.min(prev.c, colCount - 1)
      if (rows.length === 0 || colCount === 0) return null
      return r === prev.r && c === prev.c ? prev : { r, c }
    })
  }, [rows.length, colCount])

  // Focus the active cell after it (and any just-scrolled virtual row) commits.
  useEffect(() => {
    if (!nav || !active) return
    if (enableVirtual) virtualizer.scrollToIndex(active.r, { align: 'auto' })
    const id = requestAnimationFrame(() => {
      document.getElementById(cellId(active.r, active.c))?.focus()
    })
    return () => cancelAnimationFrame(id)
    // virtualizer identity is stable across renders for the same instance.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, nav, enableVirtual, cellId])

  const NAV_KEYS = [
    'ArrowDown',
    'ArrowUp',
    'ArrowRight',
    'ArrowLeft',
    'Home',
    'End',
    'PageDown',
    'PageUp',
  ]
  const onGridKeyDown = (e: KeyboardEvent<HTMLTableElement>) => {
    // `error` va en la guarda por lo mismo que `rows.length === 0`: lo que hay bajo la
    // cabecera no son filas, es un estado, y sus controles son suyos.
    // ⛔ UN CONTROL NO ES UNA CELDA. Este manejador está en el <table>, así que le llega
    // por burbujeo TODO lo que pase en cualquier descendiente: el botón de orden de la
    // cabecera, el «reintentar» de ErrorState, el «autenticar» de la ceremonia. Space y
    // Enter les hacían `preventDefault` y actuaban sobre la FILA activa. El contrato de
    // esta tabla ya prometía lo contrario desde el principio (:57-59: «sus botones de orden
    // siguen siendo botones operables»), así que no es una regla nueva: es la que faltaba
    // por implementar. Medido por el contraste `sol max`: rejilla → ArrowDown → Shift+Tab
    // al botón «Name» → Space daba `onRowClick=1` y `aria-sort` seguía en `none`.
    // ⛔ UN CONTROL NO ES UNA CELDA, PERO SIGUE ESTANDO EN LA REJILLA. Este manejador vive en
    // el <table> y le llega por burbujeo todo lo de sus descendientes, así que se comía el
    // Space del botón de orden, el del «reintentar» y el de la ceremonia (contra su propio
    // contrato, :57-59). Pero retirarse ANTE TODA TECLA fue pasarme, y el contraste lo midió:
    // con el menú de celda de orchestration enfocado, ArrowLeft dejaba de navegar. APG dice lo
    // contrario — una celda con un solo widget conserva la navegación por flechas. Así que la
    // frontera no es «control sí / control no», es QUÉ TECLA POSEE CADA COSA:
    // ⛔ Y SI EL CONTROL YA LA HA CONSUMIDO, NO ES NUESTRA. Un menú real —el de
    // `orchestration/components.tsx:478-550`, Radix— usa ArrowDown para ABRIRSE y llama a
    // `preventDefault`. Dejar pasar las NAV_KEYS «porque vienen de un botón» hacía que la
    // rejilla las procesara TAMBIÉN: el menú se abría y, en silencio, la fila activa se movía
    // a la de abajo; al cerrar con Escape y seguir con ArrowLeft, el foco aparecía una fila
    // más abajo de donde el operador lo dejó. Lo midió el contraste `sol max` con el
    // SchedulesTable y el Radix de verdad. `defaultPrevented` es la regla de composición que
    // faltaba, y es general: vale para cualquier widget que gestione su propia tecla, no sólo
    // para los que yo sepa enumerar hoy.
    if (e.defaultPrevented) return
    const from = e.target as HTMLElement | null
    if (from && from !== e.currentTarget) {
      // Una entrada de texto posee TODAS: las flechas mueven el cursor, Space escribe.
      // `[contenteditable]` a secas y `plaintext-only` son tan editables como `true`
      // (WHATWG HTML §6.8.1); mirar sólo `="true"` dejaba fuera dos formas válidas.
      if (
        from.closest(
          'input, select, textarea, [contenteditable]:not([contenteditable="false"])',
        )
      ) {
        return
      }
      // Un botón o un enlace poseen sólo la ACTIVACIÓN. Las de navegación siguen siendo de
      // la rejilla, que es justo lo que pedía el patrón.
      const control = from.closest(
        'button, a[href], [role="button"], [role="menuitem"], [role="checkbox"]',
      )
      if (control && !NAV_KEYS.includes(e.key)) return
    }
    if (!nav || error || rows.length === 0) return
    // ⛔ Y LA POSICIÓN SALE DE LA CELDA QUE TIENE EL FOCO, no de `active`. Los dos no siempre
    // coinciden: un botón de celda que forma parte del recorrido de Tab —el menú de
    // `orchestration/components.tsx:478-500` lo es, porque `Button` no le pone tabIndex=-1—
    // se alcanza SIN entrar en la rejilla, así que `active` puede ser null o apuntar a otra
    // fila. Con `active` como única referencia, ArrowLeft desde el menú de la SEGUNDA fila
    // aterrizaba en la primera celda de la PRIMERA. Lo midió el contraste `sol max` con el
    // SchedulesTable real. Si el evento nace dentro de una celda, esa celda ES la posición.
    // La CABECERA cuenta: sus `th` llevan aria-colindex y su fila aria-rowindex=1, y el foco
    // llega ahí por el botón de orden. Mirando sólo `td`, ArrowDown desde la cabecera de la
    // columna 1 navegaba desde la marca del CUERPO —otra fila y otra columna—, que es lo que
    // midió el contraste `sol max`.
    const desdeCelda =
      from?.closest('td[aria-colindex], th[aria-colindex]') ?? null
    const filaDeCelda = desdeCelda?.closest('tr[aria-rowindex]') ?? null
    const posicionDelFoco =
      desdeCelda && filaDeCelda
        ? {
            // aria-* son 1-based y la cabecera ocupa la fila 1 (:416, :431).
            r: Number(filaDeCelda.getAttribute('aria-rowindex')) - 2,
            c: Number(desdeCelda.getAttribute('aria-colindex')) - 1,
          }
        : null
    const enRango =
      posicionDelFoco !== null &&
      // -1 es la CABECERA (aria-rowindex 1). Es una posición de partida válida, no una fila.
      posicionDelFoco.r >= -1 &&
      posicionDelFoco.r < rows.length &&
      posicionDelFoco.c >= 0 &&
      posicionDelFoco.c < colCount
    const desde = enRango ? posicionDelFoco : active
    // First navigation key with nothing active ENTERS the grid at {0,0} rather
    // than moving away from it.
    if (desde === null) {
      if (NAV_KEYS.includes(e.key)) {
        e.preventDefault()
        setActive({ r: 0, c: 0 })
      }
      return
    }
    const last = rows.length - 1
    const lastCol = colCount - 1
    // ⛔ DESDE LA CABECERA SÓLO SE BAJA. Sus celdas no son focalizables (sólo el botón de
    // orden que llevan dentro), así que moverse DENTRO de la cabecera no podría enfocar nada,
    // y subir desde la primera fila no es un movimiento. Bajar sí: entra en el cuerpo por la
    // MISMA columna, que es lo que el operador acaba de señalar. Lo demás se deja pasar al
    // control, que es de quien es.
    if (desde.r < 0) {
      if (e.key === 'ArrowDown' || e.key === 'PageDown') {
        e.preventDefault()
        setActive({ r: 0, c: Math.min(Math.max(desde.c, 0), colCount - 1) })
      }
      return
    }
    const cur = desde
    // Every case below either assigns `next` or returns, so it is definitely
    // assigned by the time setActive runs (no useless initialiser).
    let next: { r: number; c: number }
    switch (e.key) {
      case 'ArrowDown':
        next = { r: Math.min(cur.r + 1, last), c: cur.c }
        break
      case 'ArrowUp':
        next = { r: Math.max(cur.r - 1, 0), c: cur.c }
        break
      case 'ArrowRight':
        next = { r: cur.r, c: Math.min(cur.c + 1, lastCol) }
        break
      case 'ArrowLeft':
        next = { r: cur.r, c: Math.max(cur.c - 1, 0) }
        break
      case 'Home':
        next = e.ctrlKey ? { r: 0, c: 0 } : { r: cur.r, c: 0 }
        break
      case 'End':
        next = e.ctrlKey ? { r: last, c: lastCol } : { r: cur.r, c: lastCol }
        break
      case 'PageDown':
        next = { r: Math.min(cur.r + PAGE_JUMP, last), c: cur.c }
        break
      case 'PageUp':
        next = { r: Math.max(cur.r - PAGE_JUMP, 0), c: cur.c }
        break
      // ⛔ ACTIVAR ES SOBRE `cur`, NO SOBRE `active`, y el typecheck REAL lo cazó: al pasar
      // la posición a salir de la celda enfocada, `active` dejó de ser la referencia y estos
      // usos se quedaron sin estrechar. Dejarlos en `active` sería algo peor que un error de
      // tipos: Enter/Space activarían UNA FILA DISTINTA de aquella donde está el foco en
      // cuanto las dos no coincidan — que es justo el caso que esta vuelta vino a arreglar.
      case 'Enter':
        if (onRowClick) {
          e.preventDefault()
          onRowClick(rows[cur.r].original)
        }
        return
      case ' ':
        e.preventDefault()
        if (selectable && cur.c === 0) {
          toggleRowSelection(rows[cur.r].id)
        } else if (onRowClick) {
          onRowClick(rows[cur.r].original)
        }
        return
      default:
        return
    }
    e.preventDefault()
    setActive(next)
  }

  const clickable = !!onRowClick
  const renderRow = (row: (typeof rows)[number], dataRowIndex: number) => (
    <tr
      key={row.id}
      role="row"
      {...(enableVirtual ? { 'data-index': dataRowIndex } : {})}
      ref={enableVirtual ? virtualizer.measureElement : undefined}
      aria-rowindex={dataRowIndex + 2}
      aria-selected={selectable ? controlledSelectedIds.has(row.id) : undefined}
      className={cn(
        'border-b border-border transition-colors last:border-0 hover:bg-muted',
        clickable && 'cursor-pointer',
      )}
      onClick={clickable ? () => onRowClick(row.original) : undefined}
    >
      {row.getVisibleCells().map((cell, c) => {
        const isActive = nav && active?.r === dataRowIndex && active?.c === c
        return (
          <td
            key={cell.id}
            role="gridcell"
            id={nav ? cellId(dataRowIndex, c) : undefined}
            aria-colindex={c + 1}
            tabIndex={nav ? (isActive ? 0 : -1) : undefined}
            className={cn(
              'px-3 align-middle outline-none',
              rowH,
              isActive &&
                'bg-muted ring-2 ring-ring -outline-offset-2 ring-inset',
            )}
          >
            {flexRender(cell.column.columnDef.cell, cell.getContext())}
          </td>
        )
      })}
    </tr>
  )

  // The grid takes the single tab stop until a cell is active (roving tabindex).
  const gridTabIndex = nav ? (active ? -1 : 0) : undefined

  return (
    <div className={cn('flex flex-col gap-3', className)}>
      {(searchable || toolbar) && (
        <div className="flex items-center gap-2">
          {searchable && (
            <div className="relative max-w-xs flex-1">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={globalFilter}
                onChange={(e) => {
                  const value = e.target.value
                  setGlobalFilter(value)
                  onSearchChange?.(value)
                }}
                placeholder={searchPlaceholder ?? t('table.search')}
                className="pl-8"
                aria-label={t('actions.search')}
              />
            </div>
          )}
          {toolbar && <div className="flex items-center gap-2">{toolbar}</div>}
        </div>
      )}

      <div className="overflow-hidden rounded-lg border border-border bg-surface">
        <div
          ref={scrollRef}
          className={cn(
            enableVirtual ? 'overflow-auto' : 'overflow-x-auto',
            // WCAG 2.4.11 Focus Not Obscured: reserve the sticky-header height so a
            // focused cell scrolled to the top is not hidden under it (CSS C43).
            sticky && 'scroll-pt-10',
          )}
          style={
            enableVirtual
              ? {
                  maxHeight:
                    typeof maxBodyHeight === 'number'
                      ? `${maxBodyHeight}px`
                      : maxBodyHeight,
                }
              : undefined
          }
        >
          {/* Announce the busy→idle transition (4.1.3): a SR user hears "Loading…"
              then "Loaded" when rows arrive. Empty/forbidden/error self-announce via
              EmptyState/ForbiddenState (role=status) and ErrorState (role=alert). */}
          <div role="status" aria-live="polite" className="sr-only">
            {isLoading
              ? t('states.loading')
              : !error && rows.length > 0
                ? t('states.loaded')
                : ''}
          </div>
          <table
            className="w-full border-collapse text-sm outline-none"
            role={nav ? 'grid' : undefined}
            aria-label={nav ? (label ?? t('table.label')) : undefined}
            aria-busy={isLoading || undefined}
            // Con `error` el cuerpo es un ESTADO, no filas: anunciar el recuento de las
            // filas conservadas describe una rejilla que el lector no puede recorrer.
            // Son DOS: la cabecera y la fila del estado. Puse 1 y el contraste lo midió
            // contra el árbol accesible de verdad —Testing Library y Chromium cuentan dos
            // nodos `row`—, así que 1 era otra cifra falsa, sólo que en la otra dirección.
            aria-rowcount={
              error ? 2 : rows.length > 0 ? rows.length + 1 : undefined
            }
            aria-colcount={nav ? colCount : undefined}
            tabIndex={gridTabIndex}
            onKeyDown={nav ? onGridKeyDown : undefined}
          >
            <thead role={nav ? 'rowgroup' : undefined}>
              {table.getHeaderGroups().map((hg) => (
                <tr
                  key={hg.id}
                  role={nav ? 'row' : undefined}
                  aria-rowindex={1}
                  className="border-b border-border-strong"
                >
                  {hg.headers.map((header, c) => {
                    const canSort = header.column.getCanSort()
                    const sorted = header.column.getIsSorted()
                    return (
                      <th
                        key={header.id}
                        role={nav ? 'columnheader' : undefined}
                        aria-colindex={nav ? c + 1 : undefined}
                        aria-sort={
                          sorted === 'asc'
                            ? 'ascending'
                            : sorted === 'desc'
                              ? 'descending'
                              : canSort
                                ? 'none'
                                : undefined
                        }
                        className={cn(
                          'bg-muted px-3 py-2 text-left text-xs font-medium tracking-wide text-muted-foreground uppercase',
                          sticky && 'sticky top-0 z-10',
                        )}
                        style={{
                          width:
                            header.getSize() !== 150
                              ? header.getSize()
                              : undefined,
                        }}
                      >
                        {header.isPlaceholder ? null : canSort ? (
                          // No aria-label here: it would OVERRIDE the header text and
                          // make every sort button announce identically ("Sort
                          // ascending"), dropping the column identity. The visible
                          // header text names the button; the current sort state is
                          // exposed on the parent columnheader via aria-sort.
                          <button
                            type="button"
                            onClick={header.column.getToggleSortingHandler()}
                            className="inline-flex min-h-6 items-center gap-1 rounded-sm uppercase outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            {flexRender(
                              header.column.columnDef.header,
                              header.getContext(),
                            )}
                            {sorted === 'asc' ? (
                              <ArrowUp
                                className="size-3 text-accent-text"
                                aria-hidden="true"
                              />
                            ) : sorted === 'desc' ? (
                              <ArrowDown
                                className="size-3 text-accent-text"
                                aria-hidden="true"
                              />
                            ) : (
                              <ChevronsUpDown
                                className="size-3 opacity-50"
                                aria-hidden="true"
                              />
                            )}
                          </button>
                        ) : (
                          flexRender(
                            header.column.columnDef.header,
                            header.getContext(),
                          )
                        )}
                      </th>
                    )
                  })}
                </tr>
              ))}
            </thead>
            <tbody role={nav ? 'rowgroup' : undefined}>
              {isLoading ? (
                <SkeletonRows rows={6} cols={colCount} rowH={rowH} />
              ) : error ? (
                <tr>
                  <td colSpan={colCount} className="p-0">
                    <TableError error={error} onRetry={onRetry} />
                  </td>
                </tr>
              ) : rows.length === 0 ? (
                <tr>
                  <td colSpan={colCount} className="p-0">
                    {/* TWO causes reach zero rows and they are NOT the same screen.
                        `data` is what the caller loaded; `rows` is what survives
                        filtering. `data.length > 0` with zero rows therefore means
                        something filtered them out — today only the global search box
                        can (no production column declares a `filterFn` and nothing
                        calls `setFilterValue`), but a column filter would land in this
                        same arm and the generic copy stays true for it, which is why
                        the branch tests `data.length` rather than the search text.
                        Painting the caller's "nothing here yet" copy here would
                        announce an empty estate over records that DO exist: a poor
                        string swapped for a false one. */}
                    {data.length === 0 ? (
                      empty
                    ) : (
                      <EmptyState title={t('states.noResults')} />
                    )}
                  </td>
                </tr>
              ) : enableVirtual ? (
                <>
                  {padTop > 0 && (
                    <tr aria-hidden style={{ height: padTop }}>
                      <td colSpan={colCount} className="border-0 p-0" />
                    </tr>
                  )}
                  {virtualItems.map((vi) =>
                    renderRow(rows[vi.index], vi.index),
                  )}
                  {padBottom > 0 && (
                    <tr aria-hidden style={{ height: padBottom }}>
                      <td colSpan={colCount} className="border-0 p-0" />
                    </tr>
                  )}
                </>
              ) : (
                rows.map((row, i) => renderRow(row, i))
              )}
            </tbody>
          </table>
        </div>

        {hasMore && onLoadMore && (
          <div className="flex justify-center border-t border-border p-2">
            <Button
              variant="ghost"
              size="sm"
              onClick={onLoadMore}
              disabled={isFetchingMore}
            >
              {isFetchingMore ? t('states.loading') : t('table.loadMore')}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}

function SkeletonRows({
  rows,
  cols,
  rowH,
}: {
  rows: number
  cols: number
  rowH: string
}) {
  return (
    <>
      {Array.from({ length: rows }).map((_, r) => (
        <tr key={r} className="border-b border-border last:border-0">
          {Array.from({ length: cols }).map((__, c) => (
            <td key={c} className={cn('px-3 align-middle', rowH)}>
              <Skeleton className="h-3.5 w-[60%]" />
            </td>
          ))}
        </tr>
      ))}
    </>
  )
}

function TableError({
  error,
  onRetry,
}: {
  error: unknown
  onRetry?: () => void
}) {
  const { t } = useTranslation('errors')
  // ⛔ ASEGURAMIENTO ANTES QUE ROL, y aquí importa más que en ningún otro sitio: esta tabla
  // la comparten 52 ficheros y 87 usos productivos —de los que 45 pasan `error`, que son los
  // que pueden llegar aquí—, así que la rama de abajo decidía por TODA la
  // consola. `ApiError.isForbidden` es SÓLO el status (lib/api/errors.ts:59) y
  // `isStepUpRequired` es el código (:77): un `step_up_required` satisface los dos, de modo
  // que leyendo el status primero cualquier tabla que el motor refuse por aseguramiento
  // acusaba al operador de no tener un permiso que SÍ tiene, y sin salida. La costura es la
  // misma que ya usa la lectura de _intel/async.tsx:55-62.
  if (error instanceof ApiError && error.isStepUpRequired) {
    return <StepUpRequiredState action="generic" onElevated={onRetry} />
  }
  // A 403 is NOT a failure — it's a permission boundary. Render it calmly, never red.
  if (error instanceof ApiError && error.isForbidden) {
    return (
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    )
  }
  const isNetwork = error instanceof NetworkError
  return (
    <ErrorState
      title={isNetwork ? t('network.title') : t('serverError.title')}
      description={
        isNetwork ? t('network.description') : t('serverError.description')
      }
      retry={onRetry}
      // ⛔ EL SEGUNDO CAMINO DEL ERROR, y me lo perdí al medir. `AsyncSection` cubre 31 vistas;
      //    ÉSTE cubre 36 — la mitad más grande. Lo destapó una prueba de extremo a extremo sobre
      //    `/catalog`, que no monta `AsyncSection`: el 500 traía su `X-Request-ID` y la pantalla
      //    no lo enseñaba. Un error de red no trae id, y aquí tampoco se inventa ninguno.
      requestId={error instanceof ApiError ? error.requestId : undefined}
    />
  )
}
