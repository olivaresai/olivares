// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Log Viewer filter bar — level toggle chips (multi-select), module text input,
// free-text search input, and a clear button. All state is managed by the parent
// (controlled components). The level chips are sent to the SSE stream as query
// params; the search input is client-side only.
import { Search, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import type { LogFilters, LogLevel } from './types'

const LEVELS: LogLevel[] = ['DEBUG', 'INFO', 'WARN', 'ERROR']

const LEVEL_COLORS: Record<LogLevel, { active: string; inactive: string }> = {
  DEBUG: {
    active: 'bg-muted text-muted-foreground border-border-strong',
    inactive: 'text-muted-foreground/60 border-border',
  },
  INFO: {
    active: 'bg-info-soft text-info border-info-line',
    inactive: 'text-muted-foreground/60 border-border',
  },
  WARN: {
    active: 'bg-warning-soft text-warning border-warning-line',
    inactive: 'text-muted-foreground/60 border-border',
  },
  ERROR: {
    active: 'bg-danger-soft text-danger border-danger-line',
    inactive: 'text-muted-foreground/60 border-border',
  },
}

export interface LogFiltersBarProps {
  filters: LogFilters
  onChange: (filters: LogFilters) => void
}

export function LogFiltersBar({ filters, onChange }: LogFiltersBarProps) {
  const { t } = useTranslation('logs')
  const toggleLevel = (level: LogLevel) => {
    const next = new Set(filters.levels)
    if (next.has(level)) next.delete(level)
    else next.add(level)
    onChange({ ...filters, levels: next })
  }

  const hasFilters =
    filters.levels.size > 0 || filters.module !== '' || filters.search !== ''

  return (
    <div className="flex flex-wrap items-center gap-2">
      {/* Level toggle chips */}
      <div className="flex items-center gap-1">
        {LEVELS.map((level) => {
          const active = filters.levels.size === 0 || filters.levels.has(level)
          const colors = LEVEL_COLORS[level]
          return (
            <button
              key={level}
              type="button"
              onClick={() => toggleLevel(level)}
              className={cn(
                'rounded-sm border px-1.5 py-0.5 text-xs font-medium transition-colors',
                'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background outline-none',
                active ? colors.active : colors.inactive,
              )}
              aria-pressed={active}
              aria-label={t('filters.levelAria', {
                level: t(`levels.${level}`),
              })}
            >
              {t(`levels.${level}`)}
            </button>
          )
        })}
      </div>

      {/* Module filter */}
      <Input
        value={filters.module}
        onChange={(e) => onChange({ ...filters, module: e.target.value })}
        placeholder={t('filters.modulePlaceholder')}
        mono
        className="h-7 w-32 text-xs"
        aria-label={t('filters.moduleAria')}
      />

      {/* Search in message */}
      <div className="relative">
        <Search className="pointer-events-none absolute left-2 top-1/2 size-3 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={filters.search}
          onChange={(e) => onChange({ ...filters, search: e.target.value })}
          placeholder={t('filters.searchPlaceholder')}
          className="h-7 w-40 pl-7 text-xs"
          aria-label={t('filters.searchAria')}
        />
      </div>

      {/* Clear all */}
      {hasFilters ? (
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={() =>
            onChange({ levels: new Set(), module: '', search: '' })
          }
          aria-label={t('filters.clearAria')}
        >
          <X />
        </Button>
      ) : null}
    </div>
  )
}
