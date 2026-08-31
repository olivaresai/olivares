// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Check, ChevronsUpDown, Layers } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceStore } from '@/stores/workspace'

/** El máximo que el repositorio genérico acepta (`maxLimit`, sqlstore/generic.go:29). */
const WORKSPACE_PAGE = 1000

export function WorkspaceSwitcher() {
  const { t } = useTranslation(['nav', 'common'])
  const { activeTenant } = useAuth()
  const { activeWorkspace, setActiveWorkspace } = useWorkspaceStore()

  // ⛔ EL CONMUTADOR TAMBIÉN SE RECORTA, y aquí el recorte no oculta filas: impide CAMBIARSE. Con
  //    más workspaces que la página, el que falte no se puede elegir desde la cabecera y no hay
  //    nada en pantalla que lo diga. Se pide el techo del store (`maxLimit`).
  //
  //    ⛔ Y LA FUNCIÓN VA ENVUELTA, no pasada por referencia: react-query llama a la `queryFn` con
  //    su CONTEXTO como primer argumento, así que en cuanto el cliente acepta un parámetro, un
  //    `queryFn: consoleApi.listWorkspaces` le pasaría `{ client, queryKey, signal, … }` como
  //    query string. Lo cazó el compilador al añadir el parámetro; en JavaScript habría viajado.
  const { data } = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant, { limit: WORKSPACE_PAGE }),
    queryFn: () => consoleApi.listWorkspaces({ limit: WORKSPACE_PAGE }),
    enabled: !!activeTenant,
    staleTime: 60_000,
  })

  const workspaces = data?.items ?? []
  if (workspaces.length <= 1) return null

  const incompleta = data?.has_more === true
  const active = workspaces.find((w) => w.id === activeWorkspace)
  // ⛔ «TODOS» ES UNA AFIRMACIÓN, Y CON LA LISTA RECORTADA PUEDE SER FALSA. Si hay un workspace
  //    activo guardado y no está en la página, `active` sale vacío y la etiqueta caía a «All» —
  //    mientras el id SIGUE en el store y las vistas lo SIGUEN mandando como filtro. O sea: la
  //    cabecera decía «todos los workspaces» con un filtro puesto. Se distingue: sin selección es
  //    «All»; con una selección que no se ha podido resolver, se dice que no se ha podido.
  //    Lo devolvió el contraste externo como hallazgo ALTO.
  const label = active
    ? active.name
    : activeWorkspace
      ? t('nav:workspace.unresolved')
      : t('nav:workspace.all')

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="base" className="max-w-[14rem] gap-1.5">
          <Layers className="size-4 text-muted-foreground" />
          <span className="truncate">{label}</span>
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-56">
        <DropdownMenuLabel>{t('nav:workspace.switch')}</DropdownMenuLabel>
        {/* El motor dice que hay más de los cargados: el que falte no se puede elegir aquí. */}
        {incompleta ? (
          <DropdownMenuLabel className="font-normal text-warning">
            {t('nav:workspace.truncated')}
          </DropdownMenuLabel>
        ) : null}
        <DropdownMenuItem onSelect={() => setActiveWorkspace(null)}>
          <span className="flex min-w-0 flex-col">
            <span className="truncate">{t('nav:workspace.all')}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">
              {t('nav:workspace.allHint')}
            </span>
          </span>
          {activeWorkspace === null && (
            <Check className="ml-auto text-accent-text" />
          )}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        {workspaces
          .filter((w) => w.status === 'active')
          .map((w) => (
            <DropdownMenuItem
              key={w.id}
              onSelect={() => setActiveWorkspace(w.id, w.name)}
            >
              <span className="flex min-w-0 flex-col">
                <span className="truncate">{w.name}</span>
                <span className="truncate font-mono text-xs text-muted-foreground">
                  {w.slug}
                  {w.is_default ? ` · ${t('nav:workspace.default')}` : ''}
                </span>
              </span>
              {w.id === activeWorkspace && (
                <Check className="ml-auto text-accent-text" />
              )}
            </DropdownMenuItem>
          ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
