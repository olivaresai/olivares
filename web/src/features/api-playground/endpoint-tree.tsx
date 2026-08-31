// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ChevronDown, ChevronRight, Search } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import type { ParsedEndpoint, TagGroup } from './openapi-parser'

const METHOD_COLORS: Record<string, string> = {
  GET: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400',
  POST: 'bg-blue-500/15 text-blue-600 dark:text-blue-400',
  PUT: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
  PATCH: 'bg-orange-500/15 text-orange-600 dark:text-orange-400',
  DELETE: 'bg-red-500/15 text-red-600 dark:text-red-400',
}

function MethodBadge({ method }: { method: string }) {
  return (
    <span
      className={cn(
        'inline-flex w-16 items-center justify-center rounded px-1.5 py-0.5 font-mono text-[10px] font-semibold uppercase',
        METHOD_COLORS[method] || 'bg-muted text-muted-foreground',
      )}
    >
      {method}
    </span>
  )
}

interface EndpointTreeProps {
  groups: TagGroup[]
  selected: ParsedEndpoint | null
  onSelect: (ep: ParsedEndpoint) => void
}

export function EndpointTree({
  groups,
  selected,
  onSelect,
}: EndpointTreeProps) {
  const { t } = useTranslation('apiPlayground')
  const [filter, setFilter] = useState('')
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const filtered = useMemo(() => {
    if (!filter.trim()) return groups
    const q = filter.toLowerCase()
    return groups
      .map((g) => ({
        ...g,
        endpoints: g.endpoints.filter(
          (ep) =>
            ep.path.toLowerCase().includes(q) ||
            ep.operationId.toLowerCase().includes(q) ||
            ep.summary.toLowerCase().includes(q) ||
            ep.method.toLowerCase().includes(q),
        ),
      }))
      .filter((g) => g.endpoints.length > 0)
  }, [groups, filter])

  const toggleGroup = (tag: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(tag)) next.delete(tag)
      else next.add(tag)
      return next
    })

  return (
    <div className="flex h-full flex-col">
      <div className="border-b p-2">
        <div className="relative">
          <Search className="absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t('filterPlaceholder')}
            className="h-8 pl-8 text-xs"
            aria-label={t('filterPlaceholder')}
          />
        </div>
      </div>
      <nav
        className="flex-1 overflow-y-auto"
        aria-label={t('endpointTree.ariaLabel')}
      >
        {filtered.map((group) => (
          <div key={group.tag}>
            <button
              type="button"
              onClick={() => toggleGroup(group.tag)}
              className="flex w-full items-center gap-1.5 border-b px-3 py-2 text-left text-xs font-semibold uppercase tracking-wider text-muted-foreground hover:bg-muted/50"
              aria-expanded={!collapsed.has(group.tag)}
            >
              {collapsed.has(group.tag) ? (
                <ChevronRight className="h-3 w-3" />
              ) : (
                <ChevronDown className="h-3 w-3" />
              )}
              {group.tag}
              {group.beta && (
                <span className="rounded bg-amber-500/15 px-1 py-0.5 text-[9px] font-semibold uppercase tracking-normal text-amber-600 dark:text-amber-400">
                  {t('beta')}
                </span>
              )}
              <span className="ml-auto font-normal tabular-nums">
                {group.endpoints.length}
              </span>
            </button>
            {!collapsed.has(group.tag) && (
              <ul className="py-0.5">
                {group.endpoints.map((ep) => {
                  const isSelected =
                    selected?.method === ep.method && selected?.path === ep.path
                  return (
                    <li key={`${ep.method}:${ep.path}`}>
                      <button
                        type="button"
                        onClick={() => onSelect(ep)}
                        className={cn(
                          'flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs hover:bg-muted/60',
                          isSelected && 'bg-muted',
                        )}
                        aria-current={isSelected ? 'true' : undefined}
                      >
                        <MethodBadge method={ep.method} />
                        <span className="min-w-0 flex-1 truncate font-mono text-foreground">
                          {ep.path}
                        </span>
                      </button>
                    </li>
                  )
                })}
              </ul>
            )}
          </div>
        ))}
        {filtered.length === 0 && (
          <div className="px-3 py-6 text-center text-xs text-muted-foreground">
            {t('noMatch')}
          </div>
        )}
      </nav>
    </div>
  )
}
