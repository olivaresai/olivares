// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Bus,
  Copy,
  Database,
  Download,
  ExternalLink,
  FileCog,
  HardDrive,
  KeyRound,
  LockKeyhole,
  Plug,
  RefreshCw,
  Server,
  Shield,
  Users,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { StatusBadge } from '@/components/data/badges'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { RelTimeLabel } from '@/features/shared'
import {
  consoleApi,
  consoleKeys,
  downloadSupportBundle,
  isUpdateCheckingUnavailable,
} from '@/features/console/api'
import type {
  AuditSpoolDTO,
  BusSnapshotDTO,
  EffectiveConfigDTO,
  HealthSummaryDTO,
  KeyCustodyDTO,
  KeyInfoDTO,
  UpdateStatusDTO,
} from '@/features/console/api'
import { ApiError } from '@/lib/api/errors'
import { formatBytes, formatInt, formatPercent } from '@/lib/format'
import { cn } from '@/lib/utils'

const REFETCH_INTERVAL = 30_000
const WARN_PERCENT = 80

export function SystemHealthTab() {
  const { t } = useTranslation('health')

  const summaryQuery = useQuery({
    queryKey: consoleKeys.healthSummary(),
    queryFn: () => consoleApi.healthSummary(),
    refetchInterval: REFETCH_INTERVAL,
  })
  const keysQuery = useQuery({
    queryKey: consoleKeys.keyCustody(),
    queryFn: () => consoleApi.keyCustody(),
  })
  const configQuery = useQuery({
    queryKey: consoleKeys.effectiveConfig(),
    queryFn: () => consoleApi.effectiveConfig(),
  })
  const busQuery = useQuery({
    queryKey: consoleKeys.busSnapshot(),
    queryFn: () => consoleApi.busSnapshot(),
    refetchInterval: REFETCH_INTERVAL,
  })

  const data = summaryQuery.data
  const isLoading = summaryQuery.isLoading

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      {/* Existing system status card. */}
      <Card className="p-5">
        <CardTitle icon={<Server />} title={t('system.systemStatus')} />
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-5 w-20" />
            <Skeleton className="h-4 w-32" />
            <Skeleton className="h-4 w-24" />
          </div>
        ) : data ? (
          <div className="space-y-2">
            <div>
              <Badge variant={data.healthy ? 'success' : 'danger'}>
                {data.healthy ? t('system.healthy') : t('system.unhealthy')}
              </Badge>{' '}
              <Badge variant={data.ready ? 'success' : 'warning'}>
                {data.ready ? t('system.ready') : t('system.notReady')}
              </Badge>
            </div>
            <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
              <Database className="size-3.5 shrink-0" aria-hidden />
              {t('system.storeEngine')}:{' '}
              <span className="font-mono">{data.store_engine}</span>
            </div>
            <div className="text-xs text-muted-foreground">
              {t('system.version')}:{' '}
              <span className="font-mono">{data.version}</span>
            </div>
          </div>
        ) : (
          <CardUnavailable />
        )}
      </Card>

      {/* Existing connectors card. */}
      <Card className="p-5">
        <CardTitle icon={<Plug />} title={t('system.connectors')} />
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-5 w-24" />
            <Skeleton className="h-4 w-28" />
          </div>
        ) : data ? (
          // The headline number is what is RUNNING; the catalog size is never it.
          // `connectors_available` counts the kinds this build can wire (~100 on
          // every install), so rendering it as "active" told a freshly installed
          // deployment it had a hundred live connectors. A deployment with
          // nothing configured reads as ready to configure, not as busy.
          <div className="space-y-2">
            {data.connectors_configured > 0 ? (
              <>
                <div className="font-display text-xl font-semibold tabular-nums text-foreground">
                  {t('system.connectorsRunning', {
                    count: data.connectors_running,
                  })}
                </div>
                <p className="text-xs text-muted-foreground">
                  {t('system.connectorsConfigured', {
                    count: data.connectors_configured,
                  })}
                </p>
                {data.connectors_error > 0 ? (
                  <Badge variant="danger">
                    {t('system.connectorsError', {
                      count: data.connectors_error,
                    })}
                  </Badge>
                ) : null}
              </>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">
                  {t('system.noConnectors')}
                </p>
                {/* A build that advertises no kinds says nothing rather than
                    offering "0 available to configure". */}
                {data.connectors_available > 0 ? (
                  <p className="text-xs text-muted-foreground">
                    {t('system.connectorsAvailable', {
                      count: data.connectors_available,
                    })}
                  </p>
                ) : null}
              </>
            )}
          </div>
        ) : (
          <CardUnavailable />
        )}
      </Card>

      {/* Existing users card. */}
      <Card className="p-5">
        <CardTitle icon={<Users />} title={t('system.users')} />
        {isLoading ? (
          <Skeleton className="h-5 w-28" />
        ) : data ? (
          <div className="font-display text-xl font-semibold tabular-nums text-foreground">
            {t('system.usersCount', { count: data.users })}
          </div>
        ) : (
          <CardUnavailable />
        )}
      </Card>

      {/* Existing identity card. */}
      <Card className="p-5">
        <CardTitle icon={<Shield />} title={t('system.identity')} />
        {isLoading ? (
          <Skeleton className="h-5 w-32" />
        ) : data ? (
          <Badge variant={data.sso_configured ? 'success' : 'neutral'}>
            {data.sso_configured
              ? t('system.ssoConfigured')
              : t('system.ssoNotConfigured')}
          </Badge>
        ) : (
          <CardUnavailable />
        )}
      </Card>

      {data?.tls_not_after || data?.tls_days_left !== undefined ? (
        <TLSCard notAfter={data.tls_not_after} daysLeft={data.tls_days_left} />
      ) : null}

      <UpdateCard update={data?.update} />

      <KeysCard
        data={keysQuery.data}
        loading={keysQuery.isLoading}
        error={keysQuery.error}
      />

      <EffectiveConfigCard
        data={configQuery.data}
        loading={configQuery.isLoading}
        error={configQuery.error}
      />

      <BusCard
        data={busQuery.data}
        loading={busQuery.isLoading}
        error={busQuery.error}
      />

      <SupportBundleCard />

      {/* Existing audit spool card. */}
      {data?.audit_spool ? <AuditSpoolCard spool={data.audit_spool} /> : null}
    </div>
  )
}

function CardTitle({
  icon,
  title,
}: {
  icon: React.ReactNode
  title: React.ReactNode
}) {
  return (
    <div className="mb-3 flex items-center gap-2 text-sm font-medium text-foreground">
      <span className="shrink-0 text-muted-foreground">{icon}</span>
      {title}
    </div>
  )
}

function CardUnavailable() {
  const { t } = useTranslation('health')
  return (
    <p role="status" className="text-sm text-muted-foreground">
      {t('system.unavailable')}
    </p>
  )
}

function TLSCard({
  notAfter,
  daysLeft,
}: {
  notAfter?: string
  daysLeft?: number
}) {
  const { t } = useTranslation('health')
  const expiring = daysLeft !== undefined && daysLeft < 30
  const expired = daysLeft !== undefined && daysLeft < 0
  return (
    <Card className={cn('p-5', expiring && 'border-warning-line')}>
      <CardTitle icon={<LockKeyhole />} title={t('tls.title')} />
      <div className="space-y-2">
        {daysLeft !== undefined ? (
          <Badge
            variant={expired ? 'danger' : expiring ? 'warning' : 'success'}
          >
            {expired
              ? t('tls.expired', { count: Math.abs(daysLeft) })
              : t('tls.daysLeft', { count: daysLeft })}
          </Badge>
        ) : null}
        {notAfter ? (
          <p className="text-xs text-muted-foreground">
            {t('tls.notAfter')}:{' '}
            <time dateTime={notAfter} className="font-mono">
              {new Date(notAfter).toLocaleString()}
            </time>
          </p>
        ) : null}
      </div>
    </Card>
  )
}

function KeysCard({
  data,
  loading,
  error,
}: {
  data?: KeyCustodyDTO
  loading: boolean
  error: unknown
}) {
  const { t } = useTranslation('health')
  const keys = data?.keys ?? []
  const sealers = keys.filter(isSealer)
  const custodial = keys.filter((key) => !isSealer(key))

  return (
    <Card className="p-5 sm:col-span-2">
      <CardTitle icon={<KeyRound />} title={t('keys.title')} />
      {loading ? (
        <Skeleton className="h-24 w-full" />
      ) : error ? (
        <CardUnavailable />
      ) : keys.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('keys.empty')}</p>
      ) : (
        <div className="space-y-4">
          {custodial.length > 0 ? (
            <div className="overflow-x-auto rounded-md border border-border">
              <table className="w-full text-left text-xs">
                <thead className="bg-muted/40 text-muted-foreground">
                  <tr>
                    <th className="px-2.5 py-2 font-medium">
                      {t('keys.cols.purpose')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('keys.cols.algorithm')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('keys.cols.mode')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('keys.cols.kek')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('keys.cols.created')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('keys.cols.fingerprint')}
                    </th>
                    <th className="px-2.5 py-2 text-right font-medium">
                      {t('keys.cols.prior')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {custodial.map((key) => (
                    <tr
                      key={key.purpose}
                      className="border-t border-border align-middle"
                    >
                      <td className="px-2.5 py-2 font-medium text-foreground">
                        {key.purpose}
                      </td>
                      <td className="px-2.5 py-2 font-mono text-muted-foreground">
                        {key.algorithm || '—'}
                      </td>
                      <td className="px-2.5 py-2 text-muted-foreground">
                        {key.custody_mode || key.origin || '—'}
                      </td>
                      <td className="px-2.5 py-2 font-mono text-muted-foreground">
                        {key.kek || '—'}
                      </td>
                      <td className="px-2.5 py-2">
                        <RelTimeLabel ts={key.created} />
                      </td>
                      <td className="px-2.5 py-2">
                        {key.fingerprint ? (
                          <FingerprintCopy
                            purpose={key.purpose}
                            fingerprint={key.fingerprint}
                          />
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>
                      <td className="px-2.5 py-2 text-right font-mono tabular-nums text-muted-foreground">
                        {formatInt(key.prior_count ?? 0)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : null}

          {sealers.length > 0 ? (
            <div>
              <h3 className="mb-2 text-xs font-medium text-muted-foreground">
                {t('keys.sealers')}
              </h3>
              <div className="grid gap-2 sm:grid-cols-3">
                {sealers.map((key) => (
                  <div
                    key={key.purpose}
                    className="flex items-center justify-between gap-2 rounded-md border border-border px-2.5 py-2"
                  >
                    <div className="min-w-0">
                      <div className="truncate text-xs font-medium text-foreground">
                        {key.purpose}
                      </div>
                      <div className="truncate font-mono text-xs text-muted-foreground">
                        {key.source || t('keys.sourceUnknown')}
                      </div>
                    </div>
                    <StatusBadge
                      status={key.present ? 'enabled' : 'disabled'}
                    />
                  </div>
                ))}
              </div>
            </div>
          ) : null}
        </div>
      )}
    </Card>
  )
}

function isSealer(key: KeyInfoDTO): boolean {
  return (
    key.source !== undefined ||
    key.purpose === 'eventing' ||
    key.purpose === 'sso' ||
    key.purpose === 'secret-store'
  )
}

function FingerprintCopy({
  purpose,
  fingerprint,
}: {
  purpose: string
  fingerprint: string
}) {
  const { t } = useTranslation('health')
  const short =
    fingerprint.length > 18
      ? `${fingerprint.slice(0, 10)}…${fingerprint.slice(-6)}`
      : fingerprint
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className="font-mono"
      aria-label={t('keys.copyFingerprint', { purpose })}
      onClick={() => {
        void navigator.clipboard.writeText(fingerprint)
        toast.success(t('keys.copied'))
      }}
    >
      <span>{short}</span>
      <Copy aria-hidden />
    </Button>
  )
}

function EffectiveConfigCard({
  data,
  loading,
  error,
}: {
  data?: EffectiveConfigDTO
  loading: boolean
  error: unknown
}) {
  const { t } = useTranslation('health')
  return (
    <Card className="p-5 sm:col-span-2">
      <CardTitle icon={<FileCog />} title={t('config.title')} />
      {loading ? (
        <Skeleton className="h-24 w-full" />
      ) : error ? (
        <CardUnavailable />
      ) : (
        <div className="space-y-3">
          {(data?.strict_violations.length ?? 0) > 0 ? (
            <div
              role="alert"
              className="rounded-md border border-warning-line bg-warning-soft p-3 text-sm text-warning"
            >
              <div className="font-medium">{t('config.strictViolations')}</div>
              <ul className="mt-1 list-disc space-y-0.5 pl-5">
                {data?.strict_violations.map((violation) => (
                  <li key={violation}>{violation}</li>
                ))}
              </ul>
            </div>
          ) : null}
          {(data?.entries.length ?? 0) > 0 ? (
            <div className="max-h-80 overflow-auto rounded-md border border-border">
              <table className="w-full text-left text-xs">
                <thead className="sticky top-0 bg-muted text-muted-foreground">
                  <tr>
                    <th className="px-2.5 py-2 font-medium">
                      {t('config.cols.key')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('config.cols.value')}
                    </th>
                    <th className="px-2.5 py-2 font-medium">
                      {t('config.cols.source')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {data?.entries.map((entry) => (
                    <tr key={entry.key} className="border-t border-border">
                      <td className="px-2.5 py-2 font-mono text-foreground">
                        {entry.key}
                      </td>
                      <td className="max-w-md truncate px-2.5 py-2 font-mono text-muted-foreground">
                        {entry.value}
                        {entry.redacted ? (
                          <Badge variant="neutral" className="ml-2">
                            {t('config.redacted')}
                          </Badge>
                        ) : null}
                      </td>
                      <td className="px-2.5 py-2">
                        <Badge variant="outline">{entry.source}</Badge>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">{t('config.empty')}</p>
          )}
        </div>
      )}
    </Card>
  )
}

function BusCard({
  data,
  loading,
  error,
}: {
  data?: BusSnapshotDTO
  loading: boolean
  error: unknown
}) {
  const { t } = useTranslation('health')
  const saturated = data?.subscribers.some(
    (subscriber) =>
      subscriber.capacity > 0 &&
      (subscriber.depth / subscriber.capacity) * 100 >= WARN_PERCENT,
  )

  return (
    <Card
      className={cn('p-5 sm:col-span-2', saturated && 'border-warning-line')}
    >
      <CardTitle icon={<Bus />} title={t('bus.title')} />
      {loading ? (
        <Skeleton className="h-28 w-full" />
      ) : error ? (
        <CardUnavailable />
      ) : data ? (
        <div className="space-y-4">
          {data.subscribers.length > 0 ? (
            <div className="grid gap-2 sm:grid-cols-2">
              {data.subscribers.map((subscriber) => {
                const percent =
                  subscriber.capacity > 0
                    ? Math.min(
                        100,
                        (subscriber.depth / subscriber.capacity) * 100,
                      )
                    : 0
                const degraded = percent >= WARN_PERCENT
                return (
                  <div
                    key={subscriber.name}
                    className={cn(
                      'rounded-md border border-border p-2.5',
                      degraded && 'border-warning-line bg-warning-soft/40',
                    )}
                  >
                    <div className="mb-1.5 flex items-center justify-between gap-2 text-xs">
                      <span className="truncate font-medium text-foreground">
                        {subscriber.name}
                      </span>
                      <span className="font-mono tabular-nums text-muted-foreground">
                        {subscriber.depth}/{subscriber.capacity}
                      </span>
                    </div>
                    <div
                      role="progressbar"
                      aria-label={t('bus.depthAria', {
                        subscriber: subscriber.name,
                      })}
                      aria-valuemin={0}
                      aria-valuemax={subscriber.capacity}
                      aria-valuenow={subscriber.depth}
                      className="h-1.5 overflow-hidden rounded-full bg-muted"
                    >
                      <div
                        className={cn(
                          'h-full rounded-full',
                          degraded ? 'bg-warning' : 'bg-accent-text',
                        )}
                        style={{ width: `${percent}%` }}
                      />
                    </div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {subscriber.class}
                    </div>
                  </div>
                )
              })}
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">
              {t('bus.noSubscribers')}
            </p>
          )}

          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            <Counter
              label={t('bus.counters.publishBlocked')}
              value={data.publish_blocked}
              loss
            />
            <Counter
              label={t('bus.counters.dropped')}
              value={data.dropped}
              loss
            />
            <Counter
              label={t('bus.counters.droppedTelemetry')}
              value={data.dropped_telemetry}
              loss
            />
            <Counter
              label={t('bus.counters.droppedNotify')}
              value={data.dropped_notify}
              loss
            />
            <Counter
              label={t('bus.counters.handlerErrors')}
              value={data.handler_errors}
              loss
            />
            <Counter label={t('bus.counters.enqueued')} value={data.enqueued} />
            <Counter label={t('bus.counters.handled')} value={data.handled} />
          </div>

          {data.bridge ? <BridgeStatus bridge={data.bridge} /> : null}
        </div>
      ) : (
        <CardUnavailable />
      )}
    </Card>
  )
}

function Counter({
  label,
  value,
  loss = false,
}: {
  label: string
  value: number
  loss?: boolean
}) {
  return (
    <div className="rounded-md border border-border px-2.5 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div
        className={cn(
          'font-mono text-sm tabular-nums',
          loss && value > 0 ? 'text-danger' : 'text-foreground',
        )}
      >
        {formatInt(value)}
      </div>
    </div>
  )
}

function BridgeStatus({
  bridge,
}: {
  bridge: NonNullable<BusSnapshotDTO['bridge']>
}) {
  const { t } = useTranslation('health')
  return (
    <div className="rounded-md border border-border p-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-xs font-medium text-foreground">
          {t('bus.bridge.title')}
        </span>
        <StatusBadge status={bridge.connected ? 'connected' : 'disconnected'} />
      </div>
      <div className="grid grid-cols-2 gap-2 text-xs text-muted-foreground sm:grid-cols-4">
        <span>
          {t('bus.bridge.pendingMessages')}: {formatInt(bridge.pending_msgs)}
        </span>
        <span>
          {t('bus.bridge.pendingBytes')}: {formatBytes(bridge.pending_bytes)}
        </span>
        <span>
          {t('bus.bridge.dropped')}: {formatInt(bridge.dropped)}
        </span>
        <span>
          {t('bus.bridge.publishErrors')}: {formatInt(bridge.publish_errors)}
        </span>
        <span>
          {t('bus.bridge.decodeErrors')}: {formatInt(bridge.decode_errors)}
        </span>
        <span>
          {t('bus.bridge.gateSkipped')}: {formatInt(bridge.gate_skipped)}
        </span>
        <span>
          {t('bus.bridge.invalidSubject')}: {formatInt(bridge.invalid_subject)}
        </span>
      </div>
    </div>
  )
}

function UpdateCard({ update }: { update?: UpdateStatusDTO }) {
  const { t } = useTranslation('health')
  const queryClient = useQueryClient()
  const mutation = useMutation({
    mutationFn: () => consoleApi.updateCheck(),
    onSuccess: (result) => {
      queryClient.setQueryData<HealthSummaryDTO>(
        consoleKeys.healthSummary(),
        (current) => (current ? { ...current, update: result } : current),
      )
      toast.success(t('update.checked'))
    },
  })
  const current = mutation.data ?? update
  const unavailable = isUpdateCheckingUnavailable(mutation.error)

  return (
    <Card className="p-5">
      <CardTitle icon={<RefreshCw />} title={t('update.title')} />
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-1.5">
          {current?.error ? (
            <Badge variant="danger">{t('update.failed')}</Badge>
          ) : current?.available ? (
            <>
              <Badge variant={current.security ? 'danger' : 'warning'}>
                {current.security
                  ? t('update.security')
                  : t('update.available')}
              </Badge>
              <span className="font-mono text-xs text-foreground">
                {current.latest_version}
              </span>
            </>
          ) : current?.up_to_date ? (
            <Badge variant="success">{t('update.upToDate')}</Badge>
          ) : (
            <Badge variant="neutral">{t('update.notChecked')}</Badge>
          )}
          {current?.channel ? (
            <span className="text-xs text-muted-foreground">
              {t('update.channel')}: {current.channel}
            </span>
          ) : null}
        </div>

        {(current?.advisories?.length ?? 0) > 0 ? (
          <div>
            <div className="mb-1 text-xs font-medium text-muted-foreground">
              {t('update.advisories')}
            </div>
            <ul className="space-y-1">
              {current?.advisories?.map((advisory) => (
                <li key={advisory}>
                  <a
                    href={`https://osv.dev/vulnerability/${encodeURIComponent(advisory)}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-1 font-mono text-xs text-accent-text hover:underline"
                  >
                    {advisory}
                    <ExternalLink className="size-3" aria-hidden />
                  </a>
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        {unavailable ? (
          <p role="status" className="text-sm text-muted-foreground">
            {t('update.unconfigured')}
          </p>
        ) : mutation.error ? (
          <p role="alert" className="text-sm text-danger">
            {mutation.error instanceof Error
              ? mutation.error.message
              : t('update.failed')}
          </p>
        ) : current?.error ? (
          <p role="alert" className="text-sm text-danger">
            {current.error}
          </p>
        ) : null}

        <Button
          type="button"
          size="sm"
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending}
          aria-label={t('update.checkNow')}
        >
          {mutation.isPending ? (
            <Spinner size="sm" aria-hidden />
          ) : (
            <RefreshCw aria-hidden />
          )}
          {t('update.checkNow')}
        </Button>
      </div>
    </Card>
  )
}

function SupportBundleCard() {
  const { t } = useTranslation(['health', 'common'])
  const [confirmOpen, setConfirmOpen] = useState(false)
  const mutation = useMutation({
    mutationFn: () => consoleApi.supportBundle(),
    onSuccess: (bundle) => {
      downloadSupportBundle(bundle)
      setConfirmOpen(false)
      toast.success(t('support.downloaded'))
    },
    // ⛔ Y EL DIÁLOGO SE CIERRA TAMBIÉN AL FALLAR. Sólo se cerraba en `onSuccess`, así que ante
    // cualquier error el `ConfirmDialog` seguía abierto —modal, con overlay y focus trap
    // (components/ui/dialog.tsx:11-17, :59-89)— mientras el mensaje, y ahora la ceremonia, se
    // pintan en la TARJETA DE DEBAJO. El operador se quedaba con un diálogo que no explica nada
    // y sin poder llegar a lo que sí. Afecta a TODOS los errores, no sólo al step-up; lo destapó
    // el contraste al preguntar si la ceremonia era ALCANZABLE, no sólo si se renderizaba.
    onError: () => setConfirmOpen(false),
  })
  // ⛔ ESTO ERA `status === 403`, QUE COLAPSA LAS DOS RESPUESTAS. Un 403 de ROL —el operador de
  // verdad no puede— acababa diciendo «hace falta AAL3», que es falso y le manda a una ceremonia
  // que no arregla nada; y un `step_up_required` decía «hace falta AAL3» sin OFRECERLO: cierto, y
  // aun así sin salida — se le nombra el obstáculo y no la puerta.
  //
  // El pre-gate `RequireAssurance` de abajo no lo cubre: decide sobre el `principal.aal`
  // CACHEADO (identity/assurance.tsx:49-78), `whoami` no tiene `refetchInterval`
  // (lib/auth/context.tsx:68-78) y el motor degrada AAL3 a AAL1 a los 15 minutos
  // (core/auth/assurance.go:31-54). La caché puede decir AAL3 con el motor en AAL1.
  const necesitaCeremonia =
    mutation.error instanceof ApiError && mutation.error.isStepUpRequired
  const negativaDeRol =
    mutation.error instanceof ApiError &&
    mutation.error.isForbidden &&
    !mutation.error.isStepUpRequired

  return (
    <Card className="p-5">
      <CardTitle icon={<Download />} title={t('support.title')} />
      <p className="mb-3 text-sm text-muted-foreground">
        {t('support.description')}
      </p>
      <RequireAssurance minAal={AAL.HARDWARE} action="supportBundle">
        <div className="space-y-2">
          <Button
            type="button"
            size="sm"
            onClick={() => setConfirmOpen(true)}
            aria-label={t('support.download')}
          >
            <Download aria-hidden />
            {t('support.download')}
          </Button>
          {necesitaCeremonia ? (
            // La ceremonia EN LUGAR del aviso: nombrar el obstáculo sin abrir la puerta es
            // justo lo que esta campaña vino a quitar de la consola.
            <StepUpRequiredState
              action="supportBundle"
              onElevated={() => mutation.mutate()}
            />
          ) : negativaDeRol ? (
            // ⛔ COPY DE ROL, NO DE ASEGURAMIENTO. Aquí ponía `support.needsAal3` —«hace falta un
            // step-up AAL3»— para una negativa de ROL, y esa ceremonia no cambia la autorización:
            // mandaba al operador a resolver algo que no le desbloquea nada. Partir el predicado
            // no partía la copia. Se reusa la cadena común, que ya está traducida en todos los
            // idiomas, en vez de crear una clave nueva y su deuda de traducción.
            <p role="alert" className="text-sm text-warning">
              {t('common:privileged.notAuthorized')}
            </p>
          ) : mutation.error ? (
            <p role="alert" className="text-sm text-danger">
              {mutation.error instanceof Error
                ? mutation.error.message
                : t('support.failed')}
            </p>
          ) : null}
        </div>
      </RequireAssurance>

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={(open) => {
          if (!mutation.isPending) setConfirmOpen(open)
        }}
        title={t('support.confirmTitle')}
        description={t('support.confirmDescription')}
        confirmLabel={t('support.generate')}
        pending={mutation.isPending}
        onConfirm={() => mutation.mutate()}
      >
        {t('support.contents')}
      </ConfirmDialog>
    </Card>
  )
}

/** Usage share (0-100) above which the spool is flagged before it engages. */
const SPOOL_WARN_PCT = 80

function AuditSpoolCard({ spool }: { spool: AuditSpoolDTO }) {
  const { t } = useTranslation('health')
  const pct =
    spool.max_bytes > 0
      ? Math.min(100, (spool.used_bytes / spool.max_bytes) * 100)
      : 0
  const nearFull = !spool.engaged && pct >= SPOOL_WARN_PCT

  return (
    <Card className="p-5">
      <CardTitle icon={<HardDrive />} title={t('auditSpool.title')} />
      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge
            variant={
              spool.engaged ? 'danger' : nearFull ? 'warning' : 'success'
            }
          >
            {spool.engaged
              ? t('auditSpool.engaged')
              : nearFull
                ? t('auditSpool.nearFull')
                : t('auditSpool.withinBudget')}
          </Badge>
          <Badge variant="neutral">
            {t(`auditSpool.modes.${spool.mode}`, { defaultValue: spool.mode })}
          </Badge>
        </div>
        <div className="text-xs text-muted-foreground">
          {t('auditSpool.usage', {
            used: formatBytes(spool.used_bytes),
            max: formatBytes(spool.max_bytes),
            pct: formatPercent(pct, { digits: 0 }),
          })}
        </div>
        {(spool.pending_drops ?? 0) > 0 ? (
          <div className="text-xs text-danger">
            {t('auditSpool.pendingDrops', {
              drops: formatInt(spool.pending_drops ?? 0),
              tenants: formatInt(spool.pending_drop_tenants ?? 0),
            })}
          </div>
        ) : null}
      </div>
    </Card>
  )
}
