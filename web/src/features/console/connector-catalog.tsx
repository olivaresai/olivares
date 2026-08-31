// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Cloud, HelpCircle, Search, Server } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import type { ConnectorHosting, ConnectorInfo } from './api'

//WHY THIS EXISTS, measured in a browser on 2026-08-09 against a live engine.
//
// The engine serves 104 connector kinds from GET /v1/console/connectors, and that read
// is a plain superadmin read: handlers_connectors.go:29 gates it with authzSystem only,
// deliberately NOT requireAAL3, because the catalog carries no secret. But the console's
// only render of it was inside the add/edit dialog, which is wrapped in
// <RequireAssurance minAal={AAL.HARDWARE}>. So on a password-only session, clicking
// "Add connector" produced a step-up wall and the catalog was never shown.
//
// The consequence is the one this session was sent to close: openai, gemini and local
// have been composed into the binary and served by the engine all along, and an operator
// still had no screen that says so. The gap was never the TYPE (the console's provider
// unions are open) — it was that nothing enumerated them.
//
// This component is therefore READ-ONLY and step-up free, exactly like the endpoint it
// renders. Adding a connector still requires AAL3; SEEING what this build supports does
// not, and pretending otherwise was stricter than the engine without being safer.

/** The three answers the engine derives, plus the honest fallback for an engine that
 *  predates the field or a value this console does not recognise. */
function normalizeHosting(value: ConnectorHosting | undefined): string {
  return value === 'self_hosted' || value === 'vendor_hosted'
    ? value
    : 'unknown'
}

/**
 * HostingBadge paints WHERE the observed system runs. The value is the engine's —
 * derived from the connector's own declared endpoint defaults — and is never
 * recomputed here.
 *
 * `unknown` gets its own visible badge rather than rendering nothing: an absent badge
 * beside vendor-hosted rows reads as "vendor", which is precisely the wrong answer to
 * infer from "we could not tell".
 */
export function HostingBadge({ hosting }: { hosting?: ConnectorHosting }) {
  const { t } = useTranslation('console')
  const value = normalizeHosting(hosting)
  const Icon =
    value === 'self_hosted'
      ? Server
      : value === 'vendor_hosted'
        ? Cloud
        : HelpCircle
  return (
    <Badge
      variant={value === 'self_hosted' ? 'success' : 'outline'}
      title={t(`connectors.hosting.${value}Hint`)}
    >
      <Icon className="size-3 shrink-0" aria-hidden />
      {t(`connectors.hosting.${value}`)}
    </Badge>
  )
}

/**
 * ConnectorCatalog lists every connector kind THIS BUILD can wire, with the connector's
 * own title, description and hosting. It is the answer to "does this thing support
 * OpenAI / Gemini / a local Ollama?" — a question the console previously could not
 * answer without a hardware key.
 *
 * The search matches the kind AND the title, because the two differ in exactly the
 * cases that matter: `gemini`, `gemini-cli` and `vertex` are THREE different connectors
 * (the Gemini API, Google's CLI agent governance, and the Gemini Enterprise Agent
 * Platform) and an operator typing "gemini" must see all three, labelled, rather than
 * pick the first.
 */
export function ConnectorCatalog({ kinds }: { kinds: ConnectorInfo[] }) {
  const { t } = useTranslation(['console', 'common'])
  const [q, setQ] = useState('')

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase()
    if (!needle) return kinds
    return kinds.filter(
      (c) =>
        c.kind.toLowerCase().includes(needle) ||
        (c.title ?? '').toLowerCase().includes(needle),
    )
  }, [kinds, q])

  if (kinds.length === 0) return null

  return (
    <section className="flex flex-col gap-3 pt-2">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold text-foreground">
            {t('console:connectors.catalog.title')}
          </h3>
          <p className="max-w-2xl text-sm text-muted-foreground">
            {t('console:connectors.catalog.caption', { count: kinds.length })}
          </p>
        </div>
        <div className="relative">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden
          />
          <Input
            className="h-8 w-56 pl-8"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            aria-label={t('console:connectors.catalog.search')}
            placeholder={t('console:connectors.catalog.search')}
          />
        </div>
      </div>

      {rows.length === 0 ? (
        <EmptyState
          title={t('console:connectors.catalog.noMatch', { query: q.trim() })}
          icon={<Search />}
        />
      ) : (
        <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {rows.map((c) => (
            <li
              key={c.kind}
              className="flex flex-col gap-1.5 rounded-lg border border-border px-3 py-2.5"
            >
              <div className="flex items-start justify-between gap-2">
                <span className="min-w-0 text-sm font-medium text-foreground">
                  {c.title || c.kind}
                </span>
                <HostingBadge hosting={c.hosting} />
              </div>
              {/* The KIND is always shown, never only the title: it is what the
                  operator types and what the source row stores, and three Gemini-ish
                  titles are only unambiguous next to their kinds. */}
              <span className="font-mono text-xs text-muted-foreground">
                {c.kind}
              </span>
              {c.description && (
                <p className="line-clamp-2 text-xs text-muted-foreground">
                  {c.description}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
