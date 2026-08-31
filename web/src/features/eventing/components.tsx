// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Eventing presentational pieces — PURE (data in, UI out). Subscription cards,
// delivery tables, event log rows, and the dead-letter queue with redeliver.
import { useState } from 'react'
import {
  Check,
  Copy,
  ExternalLink,
  History,
  Key,
  MoreHorizontal,
  RefreshCw,
  RotateCcw,
  Send,
  Trash2,
} from 'lucide-react'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { formatDateTime, formatRelativeTime } from '@/lib/format'
import type {
  CapturedEvent,
  Delivery,
  DeliveryStatus,
  Subscription,
} from './types'

// --- status badge ------------------------------------------------------------

const STATUS_VARIANT: Record<
  string,
  'success' | 'warning' | 'danger' | 'neutral' | 'accent'
> = {
  delivered: 'success',
  queued: 'neutral',
  delivering: 'accent',
  dead: 'danger',
  denied: 'warning',
}

export function DeliveryStatusBadge({ status }: { status: DeliveryStatus }) {
  const { t } = useTranslation('eventing')
  const variant = STATUS_VARIANT[status] ?? 'neutral'
  return <Badge variant={variant}>{t(`deliveries.statuses.${status}`)}</Badge>
}

// --- subscription card -------------------------------------------------------

export function SubscriptionCard({
  sub,
  canRead,
  canWrite,
  canAdmin,
  onEdit,
  onTest,
  onDelete,
  onRotateSecret,
  onRotateAuth,
  onReplay,
  onHistory,
  testPending,
}: {
  sub: Subscription
  canRead: boolean
  canWrite: boolean
  canAdmin: boolean
  onEdit: (sub: Subscription) => void
  onTest: (id: string) => void
  onDelete: (id: string) => void
  onRotateSecret: (id: string) => void
  onRotateAuth: (id: string) => void
  onReplay: (sub: Subscription) => void
  onHistory: (sub: Subscription) => void
  testPending: boolean
}) {
  const { t } = useTranslation('eventing')
  const [confirmDelete, setConfirmDelete] = useState(false)
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-border bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-foreground">
              {sub.name}
            </span>
            <Badge variant={sub.enabled ? 'success' : 'neutral'}>
              {sub.enabled
                ? t('subscriptions.enabled')
                : t('subscriptions.disabled')}
            </Badge>
            <Badge variant="neutral">
              {t(`subscriptions.authTypes.${sub.auth_type}`)}
            </Badge>
            {sub.sink_kind ? (
              // SIEM sink profile marker — makes the (edit-preserved) sink
              // visible at a glance instead of hiding it until the dialog.
              <Badge variant="accent">
                {t(`dialog.sinkKinds.${sub.sink_kind}`, {
                  defaultValue: sub.sink_kind,
                })}
              </Badge>
            ) : null}
          </div>
          <p className="mt-1 flex items-center gap-1 truncate text-xs font-mono text-muted-foreground">
            <ExternalLink className="h-3 w-3 shrink-0" />
            {sub.endpoint}
          </p>
        </div>
        <div className="flex items-center gap-1">
          {canRead ? (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t('history.action', { name: sub.name })}
              onClick={() => onHistory(sub)}
            >
              <History />
            </Button>
          ) : null}
          {canAdmin ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => onTest(sub.id)}
              disabled={testPending}
            >
              <Send className="h-3.5 w-3.5" />
              {testPending
                ? t('subscriptions.testing')
                : t('subscriptions.test')}
            </Button>
          ) : null}
          {canWrite || canAdmin ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="sm"
                  aria-label={t('subscriptions.actions', { name: sub.name })}
                >
                  <MoreHorizontal className="h-4 w-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {/* Write-tier actions (eventing.go: permSubWrite). */}
                {canWrite ? (
                  <DropdownMenuItem onClick={() => onEdit(sub)}>
                    {t('subscriptions.edit')}
                  </DropdownMenuItem>
                ) : null}
                {canWrite ? (
                  <DropdownMenuItem onClick={() => onRotateSecret(sub.id)}>
                    <Key className="mr-2 h-3.5 w-3.5" />
                    {t('subscriptions.rotateSecret')}
                  </DropdownMenuItem>
                ) : null}
                {canWrite && sub.auth_type !== 'none' ? (
                  <DropdownMenuItem onClick={() => onRotateAuth(sub.id)}>
                    <RefreshCw className="mr-2 h-3.5 w-3.5" />
                    {t('subscriptions.rotateAuth')}
                  </DropdownMenuItem>
                ) : null}
                {/* Admin-tier actions (eventing.go: permSubAdmin). */}
                {canAdmin ? (
                  <DropdownMenuItem onClick={() => onReplay(sub)}>
                    <RotateCcw className="mr-2 h-3.5 w-3.5" />
                    {t('subscriptions.replay')}
                  </DropdownMenuItem>
                ) : null}
                {canAdmin ? (
                  <DropdownMenuItem
                    className="text-danger"
                    onClick={() => setConfirmDelete(true)}
                  >
                    <Trash2 className="mr-2 h-3.5 w-3.5" />
                    {t('subscriptions.delete')}
                  </DropdownMenuItem>
                ) : null}
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>
      </div>
      <div className="flex flex-wrap gap-1">
        {sub.event_types.map((et) => (
          <Badge key={et} variant="accent">
            {et}
          </Badge>
        ))}
      </div>
      <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
        {sub.role ? (
          <div className="flex flex-col">
            <dt className="text-[11px] tracking-wide uppercase">
              {t('subscriptions.columns.role')}
            </dt>
            <dd className="text-foreground">{sub.role}</dd>
          </div>
        ) : null}
        {sub.secret_hint ? (
          <div className="flex flex-col">
            <dt className="text-[11px] tracking-wide uppercase">
              {t('subscriptions.columns.secret')}
            </dt>
            <dd className="font-mono text-foreground">{sub.secret_hint}</dd>
          </div>
        ) : null}
        <div className="flex flex-col">
          <dt className="text-[11px] tracking-wide uppercase">
            {t('subscriptions.columns.created')}
          </dt>
          <dd className="text-foreground">{formatDateTime(sub.created_at)}</dd>
        </div>
      </dl>
      {sub.description ? (
        <p className="text-xs text-muted-foreground">{sub.description}</p>
      ) : null}
      {/* ⛔ ESTO ERA UN `window.confirm` NATIVO, EL ÚNICO DE LA CONSOLA FRENTE A 69 FICHEROS
          QUE USAN `ConfirmDialog`. Y no era inconsistencia de estilo: `ConfirmDialog` trae
          `hideAuditNotice = false` POR DEFECTO, así que toda accion destructiva de esta consola
          muestra su aviso de auditoria — menos esta, que ademas BORRA una suscripcion. El dialogo
          nativo se lo saltaba, bloquea el hilo principal y no es estilable ni testeable como el
          resto. Medido el 2026-08-15. */}
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={t('subscriptions.delete')}
        description={t('subscriptions.confirmDelete')}
        tone="danger"
        onConfirm={() => {
          onDelete(sub.id)
          setConfirmDelete(false)
        }}
      />
    </div>
  )
}

// --- event row ---------------------------------------------------------------

export function EventRow({ event }: { event: CapturedEvent }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div className="rounded-md border border-border bg-surface px-3 py-2">
      <div
        className="flex cursor-pointer items-center gap-3 text-sm"
        onClick={() => setExpanded(!expanded)}
      >
        <span className="w-16 shrink-0 font-mono text-xs text-muted-foreground">
          #{event.seq}
        </span>
        <Badge variant="accent">{event.type}</Badge>
        <span className="truncate text-xs text-muted-foreground">
          {event.source}
        </span>
        <span className="ml-auto shrink-0 text-xs text-muted-foreground">
          {formatDateTime(event.occurred_at)}
        </span>
      </div>
      {expanded && event.payload != null ? (
        <pre className="mt-2 max-h-48 overflow-auto rounded bg-muted p-2 text-xs font-mono">
          {typeof event.payload === 'string'
            ? event.payload
            : JSON.stringify(event.payload, null, 2)}
        </pre>
      ) : null}
    </div>
  )
}

// --- delivery row ------------------------------------------------------------

export function DeliveryRow({
  delivery,
  subscriptionName,
  showRedeliver,
  onRedeliver,
  redeliverPending,
}: {
  delivery: Delivery
  subscriptionName?: string
  showRedeliver?: boolean
  onRedeliver?: (id: string) => void
  redeliverPending?: boolean
}) {
  const { t, i18n } = useTranslation('eventing')
  const retryAt = delivery.next_attempt_at
    ? new Date(delivery.next_attempt_at)
    : null
  const showRetry =
    delivery.status === 'queued' &&
    retryAt != null &&
    !Number.isNaN(retryAt.getTime()) &&
    retryAt.getTime() > Date.now()
  return (
    <div className="flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-sm">
      <span className="w-16 shrink-0 font-mono text-xs text-muted-foreground">
        #{delivery.event_seq}
      </span>
      <Badge variant="accent">{delivery.event_type}</Badge>
      <DeliveryStatusBadge status={delivery.status} />
      <Badge variant="neutral">
        {t(`deliveries.origins.${delivery.origin}`)}
      </Badge>
      <span className="text-xs text-muted-foreground">
        {t('deliveries.columns.subscription')}:{' '}
        {subscriptionName
          ? `${subscriptionName} (${delivery.subscription})`
          : delivery.subscription}
      </span>
      <span className="text-xs text-muted-foreground">
        {t('deliveries.columns.attempts')}: {delivery.attempts}
      </span>
      {delivery.last_attempt_at ? (
        <span className="text-xs text-muted-foreground">
          {formatDateTime(delivery.last_attempt_at)}
        </span>
      ) : null}
      {delivery.last_status ? (
        <Badge variant="neutral">{delivery.last_status}</Badge>
      ) : null}
      {showRetry ? (
        <span className="text-xs text-muted-foreground">
          {t('deliveries.nextRetry', {
            time: formatRelativeTime(delivery.next_attempt_at, i18n.language),
          })}
        </span>
      ) : null}
      {showRedeliver && onRedeliver ? (
        <Button
          variant="secondary"
          size="sm"
          className="ml-auto"
          onClick={() => onRedeliver(delivery.id)}
          disabled={redeliverPending}
        >
          <RefreshCw className="h-3.5 w-3.5" />
          {t('deadLetters.redeliver')}
        </Button>
      ) : null}
    </div>
  )
}

// --- secret reveal modal content ---------------------------------------------

export function SecretReveal({
  secret,
  onDone,
}: {
  secret: string
  onDone: () => void
}) {
  const { t } = useTranslation('eventing')
  const [copied, setCopied] = useState(false)

  const handleCopy = () => {
    void navigator.clipboard.writeText(secret).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-muted-foreground">{t('secret.description')}</p>
      <div className="flex items-center gap-2 rounded-md border border-border bg-muted p-3">
        <code className="flex-1 break-all text-xs font-mono text-foreground">
          {secret}
        </code>
        <Button variant="ghost" size="sm" onClick={handleCopy}>
          {copied ? (
            <Check className="h-4 w-4 text-success" />
          ) : (
            <Copy className="h-4 w-4" />
          )}
        </Button>
      </div>
      {copied ? (
        <p className="text-xs text-success">{t('secret.copied')}</p>
      ) : null}
      <Button variant="primary" onClick={onDone} className="self-end">
        {t('secret.done')}
      </Button>
    </div>
  )
}
