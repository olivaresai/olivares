// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { KeyRound, PlugZap, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { ListTruncationBadge } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { formatDateTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { providerKeys, providersApi } from './api'
import { useProviderBoundary } from './auth-boundary'
import { ProviderCreateDialog } from './provider-create-dialog'
import { ProviderRotateDialog } from './provider-rotate-dialog'
import type { ProbeState, ProviderRecordDTO } from './types'
import './i18n'

/**
 * ProvidersView — the screen a new operator reaches when they ask "where do I put
 * my API key".
 *
 * Before this screen there was no answer: the console had provider PROFILES (which
 * home directory an official CLI runs under) and the server had environment
 * variables, and nothing in between that an operator could name, test or revoke.
 *
 * Three rules this screen holds itself to, each of them a measured complaint:
 *  1. The empty state names the next action. It is the first screen of a clean
 *     install, so "No results" would be the product's first sentence.
 *  2. A registered provider is NEVER shown as working. Registering is not evidence;
 *     only a connection test is, and an untested one says so in its own column.
 *  3. Nothing ends in a form. Creating offers the test; a tested provider offers
 *     the binding that actually launches something.
 *
 * The room is remounted on the authority boundary: nothing read or half-done under
 * one operator survives into the next.
 */
export function ProvidersView() {
  const boundary = useProviderBoundary()
  return <Inner key={boundary.key} />
}

function Inner() {
  const { t, i18n } = useTranslation('providers')
  const { activeTenant, can } = useAuth()
  const boundary = useProviderBoundary()
  const canRead = can('sessions:provider:read')
  const canWrite = can('sessions:provider:write')
  const canAdmin = can('sessions:provider:admin')

  const [createOpen, setCreateOpen] = useState(false)
  const [rotating, setRotating] = useState<ProviderRecordDTO | null>(null)
  const [revoking, setRevoking] = useState<ProviderRecordDTO | null>(null)
  const [testing, setTesting] = useState<string | null>(null)

  const listQ = useQuery({
    queryKey: providerKeys.list(activeTenant, boundary.epoch),
    queryFn: ({ signal }) => providersApi.list(undefined, { signal }),
    enabled: canRead,
  })

  const test = usePrivilegedMutation<string, ProviderRecordDTO>({
    mutationFn: (ref) => providersApi.test(ref),
    invalidateKeys: () => [providerKeys.list(activeTenant, boundary.epoch)],
    // ⛔ THE DIALOGS ARE NOT WRAPPED IN RequireAssurance, and that is deliberate.
    // That wrapper renders the ceremony IN PLACE of its children, so wrapping a
    // dialog would paint a step-up panel on the page whenever the session is below
    // AAL3 — with the dialog closed and nothing asked for. It is the right tool for
    // an always-visible inline form (the onboarding wizard uses it that way); for a
    // dialog, usePrivilegedMutation raises the demand AT DISPATCH, which is the
    // pattern the profile plane beside this one already uses.
    stepUpAction: 'providers',
    // The toast reports the VERDICT, not "the request worked". A probe that reached
    // the provider and was refused is a successful test with a negative answer, and
    // telling the operator "tested" without the answer is the report this screen exists
    // to remove.
    successMessage: (record) =>
      record.probe_state === 'ok'
        ? t('probe.okHint')
        : record.probe_state === 'refused'
          ? t('probe.refusedHint')
          : t('probe.unreachableHint'),
    onDone: () => setTesting(null),
    onError: () => {
      setTesting(null)
      // Not handled here: an authorization or assurance failure is reported by the
      // hook itself, and suppressing it would hide the only thing worth saying.
      return false
    },
  })

  const revoke = usePrivilegedMutation<string, ProviderRecordDTO>({
    mutationFn: (ref) => providersApi.revoke(ref),
    invalidateKeys: () => [providerKeys.list(activeTenant, boundary.epoch)],
    stepUpAction: 'providers',
    successMessage: t('revoke.success'),
    onDone: () => setRevoking(null),
  })

  if (!canRead) {
    return (
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    )
  }

  const rows = listQ.data?.items ?? []

  const columns: TableColumn<ProviderRecordDTO>[] = [
    {
      id: 'name',
      header: t('columns.name'),
      accessorFn: (r) => r.display_name,
      cell: ({ row }) => (
        <div className="flex flex-col">
          <span className="font-medium text-foreground">
            {row.original.display_name}
          </span>
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.provider_ref}
          </span>
        </div>
      ),
    },
    {
      id: 'kind',
      header: t('columns.kind'),
      accessorFn: (r) => r.kind,
      cell: ({ row }) => (
        <div className="flex flex-col">
          <span>{t(`kinds.${row.original.kind}`)}</span>
          {row.original.base_url ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.base_url}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      id: 'key',
      header: t('columns.key'),
      accessorFn: (r) => r.key_hint ?? '',
      cell: ({ row }) => (
        <span className="font-mono text-xs">
          {row.original.key_hint ?? '—'}
        </span>
      ),
    },
    {
      id: 'state',
      header: t('columns.state'),
      accessorFn: (r) => r.state,
      cell: ({ row }) => (
        <Badge
          variant={row.original.state === 'active' ? 'success' : 'neutral'}
        >
          {t(`states.${row.original.state}`)}
        </Badge>
      ),
    },
    {
      id: 'connection',
      header: t('columns.connection'),
      accessorFn: (r) => r.probe_state ?? '',
      cell: ({ row }) => <ConnectionCell record={row.original} />,
    },
    {
      id: 'actions',
      header: '',
      cell: ({ row }) => {
        const record = row.original
        if (record.state !== 'active') return null
        return (
          <div className="flex items-center justify-end gap-1">
            {canWrite && (
              <Button
                variant="ghost"
                size="sm"
                disabled={test.isPending && testing === record.provider_ref}
                onClick={() => {
                  setTesting(record.provider_ref)
                  test.mutate(record.provider_ref)
                }}
              >
                {test.isPending && testing === record.provider_ref ? (
                  <Spinner className="size-3.5" />
                ) : (
                  <PlugZap className="size-3.5" />
                )}
                {test.isPending && testing === record.provider_ref
                  ? t('actions.testing')
                  : t('actions.test')}
              </Button>
            )}
            {canWrite && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setRotating(record)}
              >
                <RefreshCw className="size-3.5" />
                {t('actions.rotate')}
              </Button>
            )}
            {canAdmin && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setRevoking(record)}
              >
                <Trash2 className="size-3.5" />
                {t('actions.revoke')}
              </Button>
            )}
          </div>
        )
      },
    },
  ]

  return (
    <div className="flex h-full flex-col gap-4">
      <PageHeader
        icon={KeyRound}
        title={t('title')}
        description={t('subtitle')}
        // The verb of this screen goes in the NAMED slot, not in the `actions` bag:
        // registering a provider is what the page is for, and a census cannot tell a
        // verb from a range picker once both are in the same bag
        // (`page-actions.census.test.ts`).
        primaryAction={
          canWrite ? (
            <Button variant="primary" onClick={() => setCreateOpen(true)}>
              <Plus className="size-4" />
              {t('add')}
            </Button>
          ) : undefined
        }
      />

      {/* The list is ONE PAGE. A screen that shows a page says so — otherwise an
          operator reads "these are my providers" off a list that is missing some,
          and binds the wrong one because the right one was on page two. */}
      <ListTruncationBadge
        query={listQ}
        label={t('truncation.label', { n: rows.length })}
        hint={t('truncation.hint')}
        filas={rows.length}
        className="px-0 pt-0"
      />

      <DataTable
        columns={columns}
        data={rows}
        isLoading={listQ.isLoading}
        error={listQ.error}
        onRetry={() => void listQ.refetch()}
        label={t('title')}
        getRowId={(r) => r.provider_ref}
        empty={
          <EmptyState
            icon={<KeyRound />}
            title={t('empty.title')}
            description={t('empty.description')}
            action={
              canWrite ? (
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  <Plus className="size-4" />
                  {t('empty.action')}
                </Button>
              ) : undefined
            }
          />
        }
      />

      {rows.some((r) => r.state === 'active' && r.probe_state === 'ok') ? (
        <NextStep />
      ) : null}

      {canWrite && (
        <ProviderCreateDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onCreated={(record) => {
            // The screen does not stop at "registered". The next question an
            // operator has is whether it works, so the answer is offered as the
            // action rather than left as an exercise.
            setTesting(record.provider_ref)
            test.mutate(record.provider_ref)
          }}
        />
      )}

      {canWrite && rotating && (
        <ProviderRotateDialog
          record={rotating}
          open
          onOpenChange={(o) => (o ? undefined : setRotating(null))}
        />
      )}

      {canAdmin && revoking && (
        <ConfirmDialog
          open
          onOpenChange={(o) => (o ? undefined : setRevoking(null))}
          title={t('revoke.title')}
          description={`${t('revoke.description')} ${t('revoke.consequence')}`}
          confirmLabel={t('revoke.confirm')}
          tone="danger"
          pending={revoke.isPending}
          onConfirm={() => revoke.mutate(revoking.provider_ref)}
        />
      )}
    </div>
  )

  function ConnectionCell({ record }: { record: ProviderRecordDTO }) {
    const state: ProbeState = record.probe_state ?? ''
    const label =
      state === 'ok'
        ? t('probe.ok')
        : state === 'refused'
          ? t('probe.refused')
          : state === 'unreachable'
            ? t('probe.unreachable')
            : t('probe.never')
    const hint =
      state === 'ok'
        ? t('probe.okHint')
        : state === 'refused'
          ? t('probe.refusedHint')
          : state === 'unreachable'
            ? t('probe.unreachableHint')
            : t('probe.neverHint')
    // `refused` is a WARNING and not an error: the call worked, the provider
    // answered, and the answer was no. `unreachable` is neutral for the same
    // reason in reverse — it is not a verdict about the credential at all.
    const variant =
      state === 'ok' ? 'success' : state === 'refused' ? 'warning' : 'outline'
    return (
      <div className="flex flex-col gap-0.5">
        <Badge variant={variant} title={hint}>
          {label}
        </Badge>
        {state === 'ok' && record.models?.length ? (
          <span className="text-xs text-muted-foreground">
            {t('probe.models', { n: record.models.length })}
            {record.probe_latency_ms
              ? ` · ${t('probe.latency', { ms: record.probe_latency_ms })}`
              : ''}
          </span>
        ) : null}
        {record.probed_at ? (
          <span className="text-xs text-muted-foreground">
            {t('probe.at', {
              when: formatDateTime(record.probed_at, i18n.language),
            })}
          </span>
        ) : null}
      </div>
    )
  }

  /** The action after a working credential. A provider on its own launches nothing;
   * the profile it is bound to is what launches. */
  function NextStep() {
    return (
      <div className="rounded-lg border border-border bg-muted/30 p-4">
        <p className="text-sm font-medium text-foreground">{t('next.title')}</p>
        <p className="mt-1 text-sm text-muted-foreground">
          {t('next.description')}
        </p>
        <Button asChild variant="secondary" size="sm" className="mt-3">
          {/* `as never`: the router's generated union covers the shells, not the
              registry-driven feature routes. It is the convention the onboarding
              wizard and the governance panel already use for the same reason. */}
          <Link to={'/provider-profiles' as never}>{t('next.action')}</Link>
        </Button>
      </div>
    )
  }
}
