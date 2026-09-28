// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Check, ChevronsUpDown, Layers } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
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

/** The workspace card of the sidebar (redesign §3.2): the initial on an accent tile, the
 * name, and the slug beneath it. It is shown even with one workspace, as a plain card,
 * because it names the scope every journey below it operates on. */
function WorkspaceCardFace({ name, detail }: { name: string; detail: string }) {
  return (
    <>
      <span
        aria-hidden
        className="grid size-7 shrink-0 place-items-center rounded-lg bg-accent-soft text-[13px] leading-none font-bold text-accent-text"
      >
        {name.trim().charAt(0).toUpperCase() || '·'}
      </span>
      <span className="flex min-w-0 flex-1 flex-col text-left">
        <span
          className="truncate text-body font-semibold text-text"
          title={name}
        >
          {name}
        </span>
        <span className="truncate text-caption text-text-3" title={detail}>
          {detail}
        </span>
      </span>
    </>
  )
}

const CARD_CLASS =
  'flex w-full min-w-0 items-center gap-2.5 rounded-card border border-line bg-surface p-2 outline-none'

export function WorkspaceSwitcher({
  className,
  variant = 'row',
}: { className?: string; variant?: 'row' | 'card' } = {}) {
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
  if (workspaces.length <= 1) {
    if (variant !== 'card') return null
    const only = workspaces[0]
    return (
      <div data-slot="workspace-card" className={cn(CARD_CLASS, className)}>
        <WorkspaceCardFace
          name={only?.name ?? t('nav:workspace.all')}
          detail={only?.slug ?? t('nav:workspace.allHint')}
        />
      </div>
    )
  }

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

  const detail = active
    ? active.slug
    : t('nav:shell.workspaceCount', { count: workspaces.length })

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        {variant === 'card' ? (
          <button
            type="button"
            data-slot="workspace-card"
            aria-label={t('nav:shell.workspaceSwitch', { name: label })}
            className={cn(
              CARD_CLASS,
              'cursor-pointer hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus',
              className,
            )}
          >
            <WorkspaceCardFace name={label} detail={detail} />
            <ChevronsUpDown
              aria-hidden
              className="size-3.5 shrink-0 text-text-3"
            />
          </button>
        ) : (
          // `min-w-0 shrink`: a narrow host truncates this label rather than letting it
          // push its neighbours out; the full name stays the accessible name and is
          // offered as a tooltip.
          <Button
            variant="ghost"
            size="base"
            className={cn('min-w-24 max-w-[14rem] shrink gap-1.5', className)}
            title={label}
          >
            <Layers className="size-4 text-muted-foreground" />
            <span className="min-w-0 flex-1 truncate text-left" title={label}>
              {label}
            </span>
            <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" />
          </Button>
        )}
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
            <span className="truncate" title={t('nav:workspace.all')}>
              {t('nav:workspace.all')}
            </span>
            <span
              className="truncate font-mono text-caption text-muted-foreground"
              title={t('nav:workspace.allHint')}
            >
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
                <span className="truncate" title={w.name}>
                  {w.name}
                </span>
                <span
                  className="truncate font-mono text-caption text-muted-foreground"
                  title={
                    w.is_default
                      ? `${w.slug} · ${t('nav:workspace.default')}`
                      : w.slug
                  }
                >
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
