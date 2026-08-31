// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Activity, Network } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { Separator } from '@/components/ui/separator'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { humanize } from '@/lib/format'
import { inventoryApi, inventoryKeys } from './api'
import { Box, ENTITY_ICON } from './entity-icons'
import { InvStatus } from './status'
import type { CatalogEntry } from './types'

const ORIGIN_KINDS = new Set(['agent', 'session', 'identity', 'resource'])

/** Render a core-detail value (primitives only — never an object/secret blob). */
function renderValue(v: unknown): string | null {
  if (v === null || v === undefined || v === '') return null
  if (typeof v === 'object') return null
  return String(v)
}

export function EntityDetailSheet({
  entry,
  onClose,
}: {
  entry: CatalogEntry | null
  onClose: () => void
}) {
  const { t } = useTranslation('inventory')
  const { activeTenant } = useAuth()
  const open = entry !== null
  const Icon = entry ? (ENTITY_ICON[entry.kind] ?? Box) : Activity

  const detailQuery = useQuery({
    queryKey: entry
      ? inventoryKeys.detail(activeTenant, entry.kind, entry.entity_id)
      : ['inventory', 'detail', 'none'],
    queryFn: () => inventoryApi.detail(entry!.kind, entry!.entity_id),
    enabled: open,
  })

  const detailFields = Object.entries(detailQuery.data?.detail ?? {})
    .map(([k, v]) => [k, renderValue(v)] as const)
    .filter(([, v]) => v !== null)

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full sm:max-w-md">
        {entry && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-center gap-2">
                <Icon className="size-4 text-accent-text" />
                <span className="truncate">
                  {entry.name || entry.ref || entry.entity_id.slice(0, 8)}
                </span>
              </SheetTitle>
              <SheetDescription className="flex items-center gap-2">
                <Badge variant="outline">
                  {t(`kinds.${entry.kind}`, { defaultValue: entry.kind })}
                </Badge>
                <InvStatus status={entry.status} />
              </SheetDescription>
            </SheetHeader>

            <div className="overflow-y-auto">
              <KvList>
                {entry.ref && (
                  <KvRow label={t('detail.ref')} mono align="start">
                    {entry.ref}
                  </KvRow>
                )}
                <KvRow label={t('detail.id')} mono align="start">
                  {entry.entity_id}
                </KvRow>
                <KvRow label={t('cols.signals')} align="start">
                  <span className="flex flex-wrap justify-end gap-1">
                    {entry.signal_sources.map((s) => (
                      <Badge key={s} variant="neutral" className="font-mono">
                        {s}
                      </Badge>
                    ))}
                  </span>
                </KvRow>
                {entry.hosts && entry.hosts.length > 0 && (
                  <KvRow label={t('cols.hosts')} align="start">
                    <span className="flex flex-wrap justify-end gap-1 font-mono text-xs">
                      {entry.hosts.map((h) => (
                        <span key={h}>{h}</span>
                      ))}
                    </span>
                  </KvRow>
                )}
                <KvRow label={t('cols.occurrences')} mono>
                  {entry.occurrence_count}
                </KvRow>
                <KvRow label={t('detail.firstSeen')}>
                  <RelTimeLabel ts={entry.first_seen} />
                </KvRow>
                <KvRow label={t('detail.lastSeen')}>
                  <RelTimeLabel ts={entry.last_seen} />
                </KvRow>
              </KvList>

              {/* Core entity relations (composition / usage). */}
              {detailQuery.isLoading ? (
                <div className="flex justify-center py-4">
                  <Spinner />
                </div>
              ) : detailFields.length > 0 ? (
                <>
                  <h3 className="mt-3 mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                    {t('detail.relations')}
                  </h3>
                  <KvList>
                    {detailFields.map(([k, v]) => (
                      <KvRow key={k} label={humanize(k)} mono align="start">
                        {v}
                      </KvRow>
                    ))}
                  </KvList>
                </>
              ) : null}

              <Separator className="my-3" />
              <div className="flex flex-wrap gap-2">
                {ORIGIN_KINDS.has(entry.kind) && (
                  <Button variant="secondary" size="sm" asChild>
                    {/* Feature routes are generated dynamically from the registry,
                        so their paths aren't in the static route union — the shell
                        uses the same `as never` escape hatch (sidebar/command-menu). */}
                    <Link to={'/access-map' as never}>
                      <Network className="size-3.5" />
                      {t('detail.viewAccess')}
                    </Link>
                  </Button>
                )}
                {(entry.kind === 'session' || entry.kind === 'agent') && (
                  <Button variant="secondary" size="sm" asChild>
                    <Link to={'/sessions' as never}>
                      <Activity className="size-3.5" />
                      {t('detail.viewSessions')}
                    </Link>
                  </Button>
                )}
              </div>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
