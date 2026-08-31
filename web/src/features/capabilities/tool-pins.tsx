// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, PinOff, ShieldCheck } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Skeleton } from '@/components/ui/skeleton'
import { CaveatNotice } from '@/features/_intel/notices'
import { RelTimeLabel } from '@/features/shared'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  buildApproveIntent,
  buildUnpinIntent,
  capabilitiesApi,
  capabilitiesKeys,
  isEnterprisePending,
  isPreconditionConflict,
  resolveToolPinIntent,
  type DriftedToolPin,
  type ToolPinIntent,
} from './api'
import type { ToolPinActionResultDTO, ToolPinDTO } from './types'

/**
 * A confirmation carries the INTENT, not just the row. Clicking confirm again re-sends
 * this exact object, so a retry reuses the same Idempotency-Key.
 *
 * Closing and reopening the dialog does NOT mint a new key for the same decision:
 * buildApproveIntent/buildUnpinIntent return the live intent for that verb-plus-
 * preconditions until it resolves (api.ts intentFor). An earlier version of this comment
 * claimed reopening was "correctly" a new intention — it is not, when the previous
 * attempt's outcome is unknown, and the the model contrast of caught the claim.
 */
type Confirmation =
  | { kind: 'approve'; pin: DriftedToolPin; intent: ToolPinIntent }
  | { kind: 'unpin'; pin: ToolPinDTO; intent: ToolPinIntent }
  | null

/** What the operator submitted, kept so a 409 can be shown as a DIVERGENCE against the
 * refetched row rather than as a bare "conflict". */
type Conflict = {
  tool: string
  expectedVersion: number
  /** The fingerprint that was actually ON SCREEN when they decided — the drifted one for
   * an approve, the pinned one for an unpin. Captured rather than re-derived from the
   * refetched row: after a 409 that row is the NEW state, and labelling it "you reviewed"
   * would show the operator current data under a stale caption, which is the one thing a
   * divergence panel must never do. */
  reviewedFingerprint: string
  /** True once the refetch THIS conflict started has come back. Until then the cache
   * still holds the row the operator reviewed, and nothing may be labelled "current". */
  fresh: boolean
}

const FINGERPRINT_VISIBLE_LENGTH = 16

function Fingerprint({ value }: { value: string }) {
  const truncated =
    value.length > FINGERPRINT_VISIBLE_LENGTH
      ? `${value.slice(0, FINGERPRINT_VISIBLE_LENGTH)}…`
      : value
  return (
    <code
      className="block max-w-52 truncate font-mono text-xs text-muted-foreground"
      title={value}
    >
      {truncated}
    </code>
  )
}

/** Only a DRIFTED pin has two fingerprints to compare — the type says so, which is why
 * this renders `drift_fingerprint` without a non-null assertion. */
function FingerprintPair({ pin }: { pin: DriftedToolPin }) {
  const { t } = useTranslation('capabilities')
  return (
    <dl className="grid min-w-0 flex-1 gap-2 sm:grid-cols-2">
      <div className="min-w-0">
        <dt className="mb-1 text-xs font-medium text-muted-foreground">
          {t('toolPins.pinnedFingerprint')}
        </dt>
        <dd>
          <Fingerprint value={pin.fingerprint} />
        </dd>
      </div>
      <div className="min-w-0">
        <dt className="mb-1 text-xs font-medium text-danger">
          {t('toolPins.driftFingerprint')}
        </dt>
        <dd>
          <Fingerprint value={pin.drift_fingerprint} />
        </dd>
      </div>
    </dl>
  )
}

export function ToolPinsTab({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation(['capabilities', 'common'])
  const { activeTenant } = useAuth()
  const [confirmation, setConfirmation] = useState<Confirmation>(null)
  const [conflict, setConflict] = useState<Conflict | null>(null)

  const query = useQuery({
    queryKey: capabilitiesKeys.toolPins(activeTenant),
    queryFn: () => capabilitiesApi.listToolPins(),
    retry: (failureCount, error) =>
      !isEnterprisePending(error) && failureCount < 2,
  })

  /**
   * A 409 means the durable state moved between the read and the write. Re-read and show
   * the divergence; the human decides what to do with it. We deliberately do NOT resend
   * with the refreshed version: that would apply the operator's decision to a state they
   * never reviewed, and succeed quietly.
   */
  const onPinError = (err: unknown, intent: ToolPinIntent): boolean => {
    if (!isPreconditionConflict(err)) return false
    setConflict({
      tool: intent.body.tool,
      expectedVersion: intent.body.expected_version,
      // An approve reviewed the DRIFT; an unpin reviewed the pinned fingerprint. The
      // `?? ''` is not defensive padding: the approve arm of ToolPinApproveInput types
      // this key as `never`, so the `in` narrowing yields `string | undefined`.
      reviewedFingerprint:
        ('expected_drift_fingerprint' in intent.body
          ? intent.body.expected_drift_fingerprint
          : undefined) ??
        confirmation?.pin.fingerprint ??
        '',
      // The refetch this conflict triggers has not landed yet, and until it does the
      // cache still holds the row the operator ALREADY reviewed. Rendering that under
      // "Current" is the same lie as the one just fixed on the other side, pointing the
      // other way — so the panel shows nothing as current until this flips.
      fresh: false,
    })
    // A CAS refusal is definitive AND left no effect behind, so this intention is over:
    // release its key. The next attempt reads a different version anyway, which would
    // make it a different intention regardless — but leaving the entry would keep a dead
    // key alive for a state nobody can reach again.
    resolveToolPinIntent(intent)
    // The dialog held a review of state that is now stale — close it so the stale
    // fingerprint can never be re-confirmed, and refetch what is actually there.
    setConfirmation(null)
    void query
      .refetch()
      .then(() => setConflict((c) => (c ? { ...c, fresh: true } : c)))
    return true
  }

  const send = (successMessage: string) => ({
    mutationFn: capabilitiesApi.sendToolPinIntent,
    invalidateKeys: [capabilitiesKeys.toolPins(activeTenant)],
    successMessage,
    onDone: (_data: ToolPinActionResultDTO, intent: ToolPinIntent) => {
      // Applied: the intention reached a definitive answer, so its key retires. Anything
      // that did NOT get a definitive answer — a network failure, an aborted request —
      // deliberately keeps its key, so the operator's next attempt is still a retry.
      resolveToolPinIntent(intent)
      setConflict(null)
      setConfirmation(null)
    },
    onError: onPinError,
  })

  const approve = usePrivilegedMutation<ToolPinIntent, ToolPinActionResultDTO>(
    send(t('toolPins.approveDone')),
  )
  const unpin = usePrivilegedMutation<ToolPinIntent, ToolPinActionResultDTO>(
    send(t('toolPins.unpinDone')),
  )

  const pins = query.data?.items ?? []
  const drifts = pins.filter(
    (pin): pin is DriftedToolPin => !!pin.drift_fingerprint,
  )
  // Only after this conflict's own refetch resolved: a row from the pre-conflict cache is
  // by definition NOT current, and a refetch that failed leaves us with no current state
  // at all — which the panel must say rather than paper over.
  const conflictPin =
    conflict?.fresh === true
      ? pins.find((pin) => pin.tool === conflict.tool)
      : undefined
  const columns: TableColumn<ToolPinDTO, unknown>[] = [
    {
      accessorKey: 'tool',
      header: t('toolPins.tool'),
      cell: ({ row }) => (
        <span className="font-mono text-xs font-medium text-foreground">
          {row.original.tool}
        </span>
      ),
    },
    {
      accessorKey: 'fingerprint',
      header: t('toolPins.fingerprint'),
      cell: ({ row }) => <Fingerprint value={row.original.fingerprint} />,
    },
    {
      accessorKey: 'pinned_at',
      header: t('toolPins.pinnedAt'),
      cell: ({ row }) => <RelTimeLabel ts={row.original.pinned_at} />,
    },
    {
      accessorKey: 'pin_count',
      header: t('toolPins.pinCount'),
      cell: ({ row }) => (
        <span className="font-mono tabular-nums">{row.original.pin_count}</span>
      ),
    },
    ...(canWrite
      ? [
          {
            id: 'actions',
            header: () => (
              <span className="sr-only">{t('toolPins.actions')}</span>
            ),
            enableSorting: false,
            enableGlobalFilter: false,
            cell: ({ row }: { row: { original: ToolPinDTO } }) => (
              <div className="flex justify-end">
                <Button
                  variant="destructive"
                  size="sm"
                  aria-label={t('toolPins.unpinAria', {
                    tool: row.original.tool,
                  })}
                  onClick={() =>
                    setConfirmation({
                      kind: 'unpin',
                      pin: row.original,
                      intent: buildUnpinIntent(row.original),
                    })
                  }
                >
                  <PinOff aria-hidden />
                  {t('toolPins.unpin')}
                </Button>
              </div>
            ),
          },
        ]
      : []),
  ]

  if (query.isLoading) {
    return (
      <div role="status" className="flex flex-col gap-4">
        <span className="sr-only">{t('common:states.loading')}</span>
        <Skeleton className="h-28 rounded-lg" />
        <Skeleton className="h-64 rounded-lg" />
      </div>
    )
  }
  if (isEnterprisePending(query.error)) {
    return <CaveatNotice tone="info">{t('toolPins.enterprise')}</CaveatNotice>
  }
  // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
  // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que leerlo
  // primero sustituía la pantalla por «no tienes autorización» —falso, y sin salida—.
  //
  // DEFENSA EN PROFUNDIDAD, y lo digo porque en esta campaña ya presenté dos veces como
  // «camino vivo» algo que no lo era: HOY esta ruta no emite el código. Los emisores medidos
  // son las dos escrituras de `modules/governance` y las 21 llamadas a `requireAAL3` de
  // `core/api`, todas cubiertas ya. Esto se arregla porque el defecto es de FORMA y sobrevive
  // al día en que el gate llegue aquí, no porque alguien lo esté sufriendo ahora.
  if (query.error instanceof ApiError && query.error.isStepUpRequired) {
    return (
      <StepUpRequiredState
        action="generic"
        onElevated={() => void query.refetch()}
      />
    )
  }
  if (query.error instanceof ApiError && query.error.isForbidden) {
    return <ForbiddenState />
  }
  if (query.error) {
    return <ErrorState retry={() => void query.refetch()} />
  }

  const approving = confirmation?.kind === 'approve'

  return (
    <div className="flex flex-col gap-5">
      {conflict && (
        <section
          role="alert"
          className="rounded-lg border border-warning-line bg-warning-soft/40 p-4"
        >
          <div className="mb-1.5 flex items-center gap-2 text-warning">
            <AlertTriangle className="size-4" aria-hidden />
            <h2 className="text-sm font-semibold">
              {t('toolPins.conflictTitle', { tool: conflict.tool })}
            </h2>
          </div>
          <p className="mb-3 text-xs text-muted-foreground">
            {t('toolPins.conflictBody')}
          </p>
          {!conflict.fresh ? (
            // Three states, not two: until this conflict's refetch lands there is no
            // current state to show, and saying so beats showing the reviewed row twice
            // or announcing that the pin is gone.
            <p className="text-xs text-muted-foreground">
              {t('toolPins.conflictReloading')}
            </p>
          ) : conflictPin ? (
            <dl className="grid gap-3 sm:grid-cols-2">
              <div className="min-w-0">
                <dt className="mb-1 text-xs font-medium text-muted-foreground">
                  {t('toolPins.conflictReviewed', {
                    version: conflict.expectedVersion,
                  })}
                </dt>
                <dd>
                  <Fingerprint value={conflict.reviewedFingerprint} />
                </dd>
              </div>
              <div className="min-w-0">
                <dt className="mb-1 text-xs font-medium text-foreground">
                  {t('toolPins.conflictCurrent', {
                    version: conflictPin.version,
                  })}
                </dt>
                <dd>
                  <Fingerprint
                    value={
                      conflictPin.drift_fingerprint ?? conflictPin.fingerprint
                    }
                  />
                </dd>
              </div>
            </dl>
          ) : (
            <p className="text-xs font-medium text-foreground">
              {t('toolPins.conflictGone')}
            </p>
          )}
          <Button
            variant="secondary"
            size="sm"
            className="mt-3"
            onClick={() => setConflict(null)}
          >
            {t('toolPins.conflictDismiss')}
          </Button>
        </section>
      )}

      {drifts.length > 0 && (
        <section className="rounded-lg border border-danger-line bg-danger-soft/40 p-4">
          <div className="mb-1.5 flex items-center gap-2 text-danger">
            <AlertTriangle className="size-4" aria-hidden />
            <h2 className="text-sm font-semibold">
              {t('toolPins.driftsTitle')}
            </h2>
            <Badge variant="danger" className="tabular-nums">
              {drifts.length}
            </Badge>
          </div>
          <p className="mb-3 text-xs text-muted-foreground">
            {t('toolPins.driftsHint')}
          </p>
          <ul className="flex flex-col gap-2">
            {drifts.map((pin) => (
              <li
                key={pin.tool}
                className="flex flex-col gap-3 rounded-md border border-danger-line bg-surface p-3 lg:flex-row lg:items-center"
              >
                <div className="min-w-0 lg:w-56">
                  <p
                    className="truncate font-mono text-xs font-medium text-foreground"
                    title={pin.tool}
                  >
                    {pin.tool}
                  </p>
                  {pin.drift_at && (
                    <p className="mt-1 text-xs text-muted-foreground">
                      {t('toolPins.driftObserved')}{' '}
                      <RelTimeLabel ts={pin.drift_at} />
                    </p>
                  )}
                </div>
                <FingerprintPair pin={pin} />
                {canWrite && (
                  <Button
                    variant="secondary"
                    size="sm"
                    aria-label={t('toolPins.approveAria', { tool: pin.tool })}
                    onClick={() =>
                      setConfirmation({
                        kind: 'approve',
                        pin,
                        intent: buildApproveIntent(pin),
                      })
                    }
                  >
                    <ShieldCheck aria-hidden />
                    {t('toolPins.approve')}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      <section aria-labelledby="tool-pins-heading">
        <div className="mb-3">
          <h2
            id="tool-pins-heading"
            className="text-sm font-semibold text-foreground"
          >
            {t('toolPins.title')}
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            {t('toolPins.description')}
          </p>
        </div>
        <DataTable
          columns={columns}
          data={pins}
          searchable
          searchPlaceholder={t('toolPins.search')}
          getRowId={(pin) => pin.tool}
          label={t('toolPins.title')}
          empty={
            <EmptyState
              icon={<PinOff />}
              title={t('toolPins.empty')}
              description={t('toolPins.emptyHint')}
            />
          }
        />
      </section>

      {confirmation && (
        <ConfirmDialog
          open
          onOpenChange={(open) => !open && setConfirmation(null)}
          title={t(
            approving ? 'toolPins.approveTitle' : 'toolPins.unpinTitle',
            { tool: confirmation.pin.tool },
          )}
          description={t(
            approving ? 'toolPins.approveBody' : 'toolPins.unpinBody',
          )}
          tone={approving ? 'default' : 'danger'}
          confirmLabel={t(
            approving ? 'toolPins.approveConfirm' : 'toolPins.unpinConfirm',
          )}
          pending={approving ? approve.isPending : unpin.isPending}
          onConfirm={() =>
            // The SAME intent object on every click: a second confirm after a network
            // failure is a retry of one intention, not a second one.
            approving
              ? approve.mutate(confirmation.intent)
              : unpin.mutate(confirmation.intent)
          }
        >
          {confirmation.kind === 'approve' && (
            <FingerprintPair pin={confirmation.pin} />
          )}
        </ConfirmDialog>
      )}
    </div>
  )
}
