// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Filter, Search, ShieldAlert, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'
import type { AccessFilterState } from './graph-model'

const ALL = '__all__'

/** Modes the Write toggle owns (readwrite + write are both "RW" to the operator). */
const WRITE_MODES = ['write', 'readwrite']

export interface AccessFiltersProps {
  filter: AccessFilterState
  onChange: (next: AccessFilterState) => void
  signalSources: string[]
  overlay: boolean
  onOverlayChange: (on: boolean) => void
  /** Number of unexpected accesses (drives the overlay button's danger badge). */
  unexpectedCount?: number
  canDrift: boolean
}

export function AccessFilters({
  filter,
  onChange,
  signalSources,
  overlay,
  onOverlayChange,
  unexpectedCount = 0,
  canDrift,
}: AccessFiltersProps) {
  const { t } = useTranslation('accessMap')

  const setModes = (next: Set<string>) => onChange({ ...filter, modes: next })
  const toggleMode = (modes: string[]) => {
    const next = new Set(filter.modes)
    const allOn = modes.every((m) => next.has(m))
    for (const m of modes) {
      if (allOn) next.delete(m)
      else next.add(m)
    }
    setModes(next)
  }
  const modeActive = (modes: string[]) => modes.some((m) => filter.modes.has(m))
  const hasFilters =
    filter.modes.size > 0 ||
    filter.confidence === 'attributed' ||
    filter.signalSource !== null ||
    filter.search !== ''

  return (
    <div className="flex flex-wrap items-center gap-2">
      <div className="relative min-w-[12rem] flex-1 sm:max-w-xs">
        <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={filter.search}
          onChange={(e) => onChange({ ...filter, search: e.target.value })}
          placeholder={t('filters.search')}
          className="pl-8"
          aria-label={t('filters.search')}
        />
      </div>

      {/* Mode toggles — R / RW / ? */}
      <div
        className="inline-flex items-center gap-1"
        role="group"
        aria-label={t('filters.mode')}
      >
        <ModeToggle
          label={t('legend.read')}
          active={modeActive(['read'])}
          onClick={() => toggleMode(['read'])}
          dotClass="bg-info"
        />
        <ModeToggle
          label={t('legend.write')}
          active={modeActive(WRITE_MODES)}
          onClick={() => toggleMode(WRITE_MODES)}
          dotClass="bg-accent-text"
        />
        <ModeToggle
          label={t('legend.unknown')}
          active={modeActive(['unknown'])}
          onClick={() => toggleMode(['unknown'])}
          dotClass="bg-graphite-400"
        />
      </div>

      {/* Confidence: only firmly attributed */}
      <Button
        variant={filter.confidence === 'attributed' ? 'primary' : 'outline'}
        size="sm"
        aria-pressed={filter.confidence === 'attributed'}
        onClick={() =>
          onChange({
            ...filter,
            confidence:
              filter.confidence === 'attributed' ? 'all' : 'attributed',
          })
        }
        title={t('filters.attributedHint')}
      >
        {t('filters.attributedOnly')}
      </Button>

      {/* Signal source */}
      {signalSources.length > 0 && (
        <Select
          value={filter.signalSource ?? ALL}
          onValueChange={(v) =>
            onChange({ ...filter, signalSource: v === ALL ? null : v })
          }
        >
          <SelectTrigger
            className="h-7 w-auto min-w-[8rem] text-xs"
            aria-label={t('filters.signal')}
          >
            <SelectValue placeholder={t('filters.signal')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('filters.allSignals')}</SelectItem>
            {signalSources.map((s) => (
              <SelectItem key={s} value={s} className="font-mono">
                {s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}

      {hasFilters && (
        <Button
          variant="ghost"
          size="sm"
          onClick={() =>
            onChange({
              modes: new Set(),
              confidence: 'all',
              signalSource: null,
              search: '',
            })
          }
        >
          <X className="size-3.5" /> {t('filters.clear')}
        </Button>
      )}

      <div className="ml-auto">
        <Button
          variant={overlay ? 'destructive-solid' : 'secondary'}
          size="sm"
          aria-pressed={overlay}
          disabled={!canDrift}
          onClick={() => onOverlayChange(!overlay)}
          title={
            canDrift ? t('filters.driftHint') : t('filters.driftForbidden')
          }
        >
          <ShieldAlert className="size-3.5" />
          {t('filters.drift')}
          {overlay && unexpectedCount > 0 && (
            <Badge
              variant="neutral"
              className="ml-1 border-0 bg-black/20 text-current tabular-nums"
            >
              {unexpectedCount}
            </Badge>
          )}
        </Button>
      </div>
    </div>
  )
}

function ModeToggle({
  label,
  active,
  onClick,
  dotClass,
}: {
  label: string
  active: boolean
  onClick: () => void
  dotClass: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-xs font-medium transition-colors',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background outline-none',
        active
          ? 'border-accent-line bg-accent-soft text-accent-soft-foreground'
          : 'border-border-strong bg-surface text-muted-foreground hover:bg-muted',
      )}
    >
      <span className={cn('size-2 rounded-full', dotClass)} aria-hidden />
      {label}
    </button>
  )
}

export { Filter }
