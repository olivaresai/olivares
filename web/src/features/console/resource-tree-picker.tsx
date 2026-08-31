// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, FolderTree } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { consoleApi, consoleKeys, type ResourceNodeDTO } from './api'

export interface ResourceTreePickerProps {
  value: string
  onChange: (id: string, node: ResourceNodeDTO) => void
  workspaceId?: string
}

/** El máximo que el repositorio genérico acepta (`maxLimit`, sqlstore/generic.go:29). */
const RESOURCE_PAGE = 1000

export function ResourceTreePicker({
  value,
  onChange,
  workspaceId,
}: ResourceTreePickerProps) {
  const { t } = useTranslation('console')
  const { activeTenant } = useAuth()
  // ⛔ ESTE SELECTOR PAGINA POR NIVEL, y es el que más caro sale de los cuatro de esta pantalla:
  //    el id que se elija aquí se guarda como ANCLA del enlace y el motor lo usa en runtime para
  //    permitir, prohibir y elegir credencial (`modules/sourcescope/resolver.go`). Un recurso que
  //    no se puede ver no se puede elegir, y no se puede teclear: se elige OTRO, y el ancla queda
  //    puesta en el sitio equivocado. Lo señaló el contraste externo como el mayor coste de
  //    silencio de la pantalla.
  const params = useMemo(
    () =>
      workspaceId
        ? { workspace_id: workspaceId, limit: RESOURCE_PAGE }
        : { limit: RESOURCE_PAGE },
    [workspaceId],
  )
  const roots = useQuery({
    queryKey: consoleKeys.resources(activeTenant, params),
    queryFn: () => consoleApi.listResources(params),
  })

  if (roots.isLoading) {
    return (
      <div className="flex justify-center rounded-lg border border-border py-6">
        <Spinner />
      </div>
    )
  }
  if (roots.isError) {
    return <ErrorState retry={() => void roots.refetch()} />
  }
  const items = roots.data?.items ?? []
  const raicesIncompletas = roots.data?.has_more === true && !roots.error
  if (items.length === 0) {
    return (
      <EmptyState
        icon={<FolderTree />}
        title={t('bindings.folderPicker.empty')}
        description={t('bindings.folderPicker.emptyHint')}
      />
    )
  }

  return (
    <div
      role="tree"
      aria-label={t('bindings.folderPicker.label')}
      className="max-h-72 overflow-y-auto rounded-lg border border-border bg-background p-1"
    >
      {raicesIncompletas ? (
        <p className="px-2 py-1 text-[11px] text-warning">
          {t('bindings.folderPicker.truncated')}
        </p>
      ) : null}
      {items.map((node) => (
        <ResourceTreeItem
          key={node.id}
          node={node}
          level={1}
          selectedId={value}
          onSelect={onChange}
        />
      ))}
    </div>
  )
}

function ResourceTreeItem({
  node,
  level,
  selectedId,
  onSelect,
}: {
  node: ResourceNodeDTO
  level: number
  selectedId: string
  onSelect: (id: string, node: ResourceNodeDTO) => void
}) {
  const { t } = useTranslation('console')
  const { activeTenant } = useAuth()
  const [expanded, setExpanded] = useState(false)
  // El recorte es POR NIVEL: cada rama se pide por separado, así que el aviso también.
  const childParams = { parent: node.id, limit: RESOURCE_PAGE }
  const children = useQuery({
    queryKey: consoleKeys.resources(activeTenant, childParams),
    queryFn: () => consoleApi.listResources(childParams),
    enabled: expanded,
  })
  const hijosIncompletos = children.data?.has_more === true && !children.error
  const selected = selectedId === node.id
  const hasLoadedChildren = (children.data?.items.length ?? 0) > 0

  const select = () => onSelect(node.id, node)
  const toggle = () => setExpanded((v) => !v)

  return (
    <div>
      <div
        role="treeitem"
        aria-level={level}
        aria-expanded={expanded}
        aria-selected={selected}
        tabIndex={0}
        onKeyDown={(event) => {
          if (event.key === 'ArrowRight') {
            event.preventDefault()
            setExpanded(true)
          } else if (event.key === 'ArrowLeft') {
            event.preventDefault()
            setExpanded(false)
          } else if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault()
            select()
          }
        }}
        className={cn(
          'group flex items-center gap-2 rounded-md px-2 py-1.5 text-sm outline-none',
          'focus-visible:ring-2 focus-visible:ring-ring',
          selected ? 'bg-accent-soft text-foreground' : 'hover:bg-muted',
        )}
      >
        <button
          type="button"
          aria-label={
            expanded
              ? t('bindings.folderPicker.collapse', { name: node.name })
              : t('bindings.folderPicker.expand', { name: node.name })
          }
          onClick={toggle}
          className="flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-muted"
        >
          {expanded ? <ChevronDown /> : <ChevronRight />}
        </button>
        <button
          type="button"
          onClick={select}
          className="min-w-0 flex-1 text-left"
        >
          <span className="truncate font-medium">{node.name}</span>
          <span className="ml-2 font-mono text-xs text-muted-foreground">
            {node.kind}
          </span>
        </button>
        {node.sensitivity ? (
          <Badge variant="warning">{node.sensitivity}</Badge>
        ) : null}
        <Button type="button" size="sm" variant="ghost" onClick={select}>
          {t('bindings.folderPicker.selectSubtree')}
        </Button>
      </div>
      {expanded ? (
        <div role="group" className="ml-5 border-l border-border pl-2">
          {hijosIncompletos ? (
            <p className="px-1 py-1 text-[11px] text-warning">
              {t('bindings.folderPicker.truncated')}
            </p>
          ) : null}
          {children.isLoading ? (
            <div className="flex py-2">
              <Spinner size="sm" />
            </div>
          ) : children.isError ? (
            <p role="alert" className="py-2 text-xs text-danger">
              {t('bindings.folderPicker.loadError')}
            </p>
          ) : hasLoadedChildren ? (
            children.data?.items.map((child) => (
              <ResourceTreeItem
                key={child.id}
                node={child}
                level={level + 1}
                selectedId={selectedId}
                onSelect={onSelect}
              />
            ))
          ) : (
            <p className="py-2 text-xs text-muted-foreground">
              {t('bindings.folderPicker.noChildren')}
            </p>
          )}
        </div>
      ) : null}
    </div>
  )
}
