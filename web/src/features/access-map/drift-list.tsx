// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { AlertOctagon, ArrowRight, CircleSlash } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { AccessModeBadge } from '@/components/data/badges'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { cn } from '@/lib/utils'
import type { DiffResponse, DriftEntry } from './types'

/**
 * DriftList — the permitted-vs-observed findings as a scannable list beside the
 * graph overlay. Unexpected accesses (observed, not permitted) lead in DANGER — the
 * product's "aha". An unexpected access flagged reconciliation_pending shows AMBER
 * "pending", never red. Unused grants (permitted, never observed) are the lower-emphasis
 * least-privilege drift.
 *
 * ⚠ — THIS COMMENT AND ITS HINT BOTH USED TO SAY "identity link unresolved", naming ONE
 * of the engine's three pending causes as if it were the cause. reconcileDrift also marks
 * pending for an UNKNOWN GRANT MODE and for an UNDECIDABLE OBSERVED MODE
 * (modules/access-map/query.go:216-225, :290-329), and the boolean does not carry which —
 * there is a named engine witness for the unknown-mode case (query_test.go:104). The sheet
 * was corrected for this and the side list was not, so the same false cause survived one
 * component over, in the panel the operator reads FIRST.
 */
export function DriftList({
  diff,
  onSelect,
}: {
  diff: DiffResponse
  onSelect: (entry: DriftEntry) => void
}) {
  const { t } = useTranslation('accessMap')
  const firm = diff.unexpected_accesses.filter((e) => !e.reconciliation_pending)
  const pending = diff.unexpected_accesses.filter(
    (e) => e.reconciliation_pending,
  )

  // "Clean" is a claim about the WHOLE estate, so it needs a whole-estate read. Two empty
  // arrays out of a TRUNCATED window mean only "nothing in the scanned window" — the page
  // cap is 50k raw drift rows, and permitted-only inventory grants can fill it while a real
  // violation sits past the bound (query.go:137-195). Saying "every observed access is
  // permitted" there is the most confident possible way to be wrong.
  const empty =
    diff.unexpected_accesses.length === 0 && diff.unused_grants.length === 0
  if (empty) {
    return diff.truncated ? (
      <div className="flex flex-col gap-2">
        <p className="rounded-md border border-warning-line bg-warning-soft/40 px-2.5 py-1.5 text-xs leading-snug text-warning">
          {t('drift.partial')}
        </p>
        <EmptyState
          icon={<CircleSlash />}
          title={t('drift.emptyWindowTitle')}
          description={t('drift.emptyWindowHint')}
        />
      </div>
    ) : (
      <EmptyState
        icon={<CircleSlash />}
        title={t('drift.cleanTitle')}
        description={t('drift.cleanHint')}
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {/* The engine reconciled this diff over a PARTIAL drift window (drainDrift's page
          bound fired) and says so on the wire specifically so a consumer never presents
          it as authoritative (modules/access-map/query.go:83-87). Saying it here is not
          decoration: without it these counts read as the whole estate. */}
      {diff.truncated && (
        <p className="rounded-md border border-warning-line bg-warning-soft/40 px-2.5 py-1.5 text-xs leading-snug text-warning">
          {t('drift.partial')}
        </p>
      )}
      {firm.length > 0 && (
        <Group
          tone="danger"
          icon={<AlertOctagon className="size-4" />}
          title={t('drift.unexpectedTitle')}
          count={firm.length}
          hint={t('drift.unexpectedHint')}
        >
          {firm.map((e) => (
            <DriftRow
              key={e.edge.id}
              entry={e}
              onSelect={onSelect}
              tone="danger"
            />
          ))}
        </Group>
      )}

      {pending.length > 0 && (
        <Group
          tone="warning"
          icon={<AlertOctagon className="size-4" />}
          title={t('drift.pendingTitle')}
          count={pending.length}
          hint={t('drift.pendingHint')}
        >
          {pending.map((e) => (
            <DriftRow
              key={e.edge.id}
              entry={e}
              onSelect={onSelect}
              tone="warning"
            />
          ))}
        </Group>
      )}

      {diff.unused_grants.length > 0 && (
        <Group
          tone="warning"
          icon={<CircleSlash className="size-4" />}
          title={t('drift.unusedTitle')}
          count={diff.unused_grants.length}
          hint={t('drift.unusedHint')}
        >
          {diff.unused_grants.map((e) => (
            <DriftRow
              key={e.edge.id}
              entry={e}
              onSelect={onSelect}
              tone="muted"
            />
          ))}
        </Group>
      )}
    </div>
  )
}

function Group({
  tone,
  icon,
  title,
  count,
  hint,
  children,
}: {
  tone: 'danger' | 'warning'
  icon: React.ReactNode
  title: string
  count: number
  hint: string
  children: React.ReactNode
}) {
  return (
    <section>
      <header className="mb-1.5 flex items-center gap-2">
        <span
          className={cn(
            'inline-flex items-center gap-1.5 text-sm font-semibold',
            tone === 'danger' ? 'text-danger' : 'text-warning',
          )}
        >
          {icon}
          {title}
        </span>
        <Badge variant={tone} className="tabular-nums">
          {count}
        </Badge>
      </header>
      <p className="mb-2 text-xs text-muted-foreground">{hint}</p>
      <ul className="flex flex-col gap-1">{children}</ul>
    </section>
  )
}

function DriftRow({
  entry,
  onSelect,
  tone,
}: {
  entry: DriftEntry
  onSelect: (entry: DriftEntry) => void
  tone: 'danger' | 'warning' | 'muted'
}) {
  const e = entry.edge
  return (
    <li>
      <button
        type="button"
        onClick={() => onSelect(entry)}
        className={cn(
          'flex w-full items-center gap-2 rounded-md border px-2.5 py-1.5 text-left text-xs transition-colors outline-none',
          'focus-visible:ring-2 focus-visible:ring-ring',
          tone === 'danger'
            ? 'border-danger-line bg-danger-soft/50 hover:bg-danger-soft'
            : tone === 'warning'
              ? 'border-warning-line bg-warning-soft/40 hover:bg-warning-soft'
              : 'border-border bg-surface hover:bg-muted',
        )}
      >
        <span
          className="min-w-0 flex-1 truncate font-mono"
          title={e.origin_ref}
        >
          {e.origin_ref || e.origin_kind}
        </span>
        <ArrowRight className="size-3 shrink-0 text-muted-foreground" />
        <span
          className="min-w-0 flex-1 truncate font-mono"
          title={e.resource_ref}
        >
          {e.resource_ref || e.resource_kind}
        </span>
        <AccessModeBadge mode={e.mode} className="shrink-0" />
      </button>
    </li>
  )
}
