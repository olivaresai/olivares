// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { CodeLine } from '@/components/ui/code-line'
import { toolName } from '@/features/agentops/tool-names'
import { ConsumptionBar } from '@/features/_intel/consumption'
import { formatDateTime, formatRelativeTime } from '@/lib/format'
import type { ProviderSnapshot } from './api'
import './i18n'

const STATE_BADGE: Record<ProviderSnapshot['state'], BadgeVariant> = {
  ready: 'success',
  not_signed_in: 'warning',
  not_installed: 'neutral',
  unknown: 'outline',
  error: 'danger',
}

/** The tool's own account, usage and models, inside that tool's disclosure. */
export function ProviderDetails({ snap }: { snap: ProviderSnapshot }) {
  const { t } = useTranslation('agentTools')
  const name = toolName(snap.driver)
  const login = t(snap.default ? 'providers.default' : 'providers.olivares')
  // The plan is the tool's raw value; a login without one (an API key) shows its method.
  const plan = snap.plan || snap.auth_method
  return (
    <article
      aria-label={`${name} · ${login}`}
      className="flex min-w-0 flex-col gap-3 border-t border-border pt-3"
    >
      <div className="flex flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-body font-semibold">{login}</h3>
          {snap.version && <Badge variant="outline">{snap.version}</Badge>}
          <Badge variant={STATE_BADGE[snap.state] ?? 'outline'}>
            {t(`providers.state.${snap.state}`, { defaultValue: snap.state })}
          </Badge>
          {/* A stale card is the last good answer, not the present one. */}
          {snap.stale && (
            <Badge variant="warning">{t('providers.staleBadge')}</Badge>
          )}
        </div>
        {snap.config_dir && (
          <p className="break-all font-mono text-xs text-muted-foreground">
            {snap.config_dir}
          </p>
        )}
      </div>
      {snap.state === 'ready' && (
        <p className="break-words text-body">
          {[
            snap.email
              ? t('providers.signedInAs', { email: snap.email })
              : t('providers.signedIn'),
            plan,
          ]
            .filter(Boolean)
            .join(' · ')}
        </p>
      )}
      {snap.next_command && (
        <CodeLine
          command={snap.next_command}
          label={t(
            snap.state === 'not_installed'
              ? 'firstHour:actions.install'
              : 'providers.signInWith',
          )}
        />
      )}
      {snap.error && (
        <p
          className={
            snap.stale
              ? 'break-words text-sm text-warning'
              : 'break-words text-sm text-destructive'
          }
        >
          {snap.stale
            ? t('providers.stale', { error: snap.error })
            : snap.error}
        </p>
      )}
      {snap.limits.length > 0 && (
        <ul className="flex flex-col gap-3">
          {snap.limits.map((limit, i) => (
            <li key={`${limit.label}-${i}`} className="flex flex-col gap-1">
              <div className="flex flex-wrap justify-between gap-2 text-sm">
                <span>{limit.label}</span>
                {limit.resets_at && (
                  <time
                    dateTime={limit.resets_at}
                    title={formatDateTime(limit.resets_at)}
                    className="text-muted-foreground"
                  >
                    {t('providers.resets', {
                      when: formatRelativeTime(limit.resets_at),
                    })}
                  </time>
                )}
              </div>
              <ConsumptionBar consumedPct={limit.percent} label={limit.label} />
            </li>
          ))}
        </ul>
      )}
      {snap.notes?.length ? (
        <ul className="flex flex-col gap-1 text-sm text-muted-foreground">
          {snap.notes.map((note, i) => (
            <li key={`${note}-${i}`} className="break-words">
              {note}
            </li>
          ))}
        </ul>
      ) : null}
      {snap.models.length > 0 && (
        <details className="text-sm">
          <summary className="cursor-pointer text-muted-foreground">
            {t('providers.models', { count: snap.models.length })}
          </summary>
          <ul className="mt-2 flex flex-wrap gap-1">
            {snap.models.map((model) => (
              <li key={model.id}>
                <Badge variant="neutral" title={model.id}>
                  {model.name || model.id}
                </Badge>
              </li>
            ))}
          </ul>
        </details>
      )}
      <div className="mt-auto flex flex-col gap-0.5 text-xs text-muted-foreground">
        <span>
          {t('providers.checked', { when: formatDateTime(snap.checked_at) })}
        </span>
        {snap.source && (
          <span className="break-all font-mono">{snap.source}</span>
        )}
      </div>
    </article>
  )
}
