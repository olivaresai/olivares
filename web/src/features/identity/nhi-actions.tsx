// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Confirmed, self-audited NHI lifecycle mutations. Governed actuations reflect
// the backend approval status; the client never interprets a pending request as
// a completed rotation/offboarding. Minted credentials live only in the current
// mutation result and are discarded when their one-time dialog closes.
import { RotateCcw, Settings2, ShieldOff, UserRoundCog } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { identityApi, identityKeys } from './api'
import type {
  NhiActionInput,
  NhiActionResult,
  NhiLifecycleDTO,
  NhiOwnershipInput,
  NhiPolicyInput,
  NhiSweepReport,
} from './types'

type RowAction =
  'ownership' | 'policy' | 'rotate' | 'offboard' | 'finalize' | 'restore'

const CRITICALITIES = ['low', 'medium', 'high', 'critical'] as const

function asLocalDateTime(timestamp?: string): string {
  if (!timestamp) return ''
  const date = new Date(timestamp)
  if (Number.isNaN(date.valueOf())) return ''
  const local = new Date(date.valueOf() - date.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 16)
}

function asRfc3339(timestamp: string): string {
  if (!timestamp) return ''
  const date = new Date(timestamp)
  return Number.isNaN(date.valueOf()) ? '' : date.toISOString()
}

/** Gate statuses a refused actuation can report. `expired` and `rejected` are
 * decisions ABOUT the request; `no_gate` means governance is not wired at all,
 * so nothing could have been approved. None of them is a permission problem. */
const GATE_DENIALS = ['rejected', 'expired', 'no_gate'] as const
type GateDenial = (typeof GATE_DENIALS)[number]

/** gateStatusOf reads the gate's verdict out of a refused actuation's body.
 * Returns undefined when the failure is not a gate denial, so the caller keeps
 * its transport-level reading for genuine authorization and validation errors. */
function gateStatusOf(error: unknown): GateDenial | undefined {
  if (!(error instanceof ApiError)) return undefined
  if (![403, 409, 503].includes(error.status)) return undefined
  const status = (error.body as { status?: unknown } | null)?.status
  return GATE_DENIALS.find((d) => d === status)
}

function ActionError({ error, action }: { error: unknown; action: RowAction }) {
  const { t } = useTranslation(['identity', 'common'])
  if (!error) return null

  // A governed actuation that the approval gate refuses answers with the GATE'S
  // OWN result document (status / approval_ref / detail) under 403, 409 or 503 —
  // not the error envelope. Reading only the HTTP status would report a human's
  // REJECTION, a lapsed approval or an unwired gate as "you lack permission",
  // which blames the operator for a decision someone else made. Route on the
  // gate status first; fall back to the transport reading only when the body is
  // not a gate result.
  const gate = gateStatusOf(error)
  let message = t('lifecycle.errors.mutation')
  if (gate) {
    message = t(`lifecycle.errors.gate.${gate}`)
  } else if (error instanceof ApiError && error.isStepUpRequired) {
    // ⛔ TERCERA FORMA DE RESPONDER A LA CEREMONIA: aquí no se decide una pantalla ni se delega
    // una acción, se elige un MENSAJE. Sin esta rama, un `step_up_required` —que satisface
    // `isForbidden`, porque eso es sólo el status (lib/api/errors.ts:59-61)— se anunciaba como
    // «tu rol no puede»: falso, y sin salida. Se reusa la cadena común, ya traducida en los
    // siete idiomas, en vez de acuñar una clave y su deuda de traducción.
    message = t('common:privileged.stepUp.title')
  } else if (error instanceof ApiError && error.isForbidden) {
    message = t('lifecycle.errors.forbidden')
  } else if (
    error instanceof ApiError &&
    error.status === 400 &&
    action === 'ownership'
  ) {
    message = t('lifecycle.errors.humanOwnerRequired')
  } else if (
    error instanceof ApiError &&
    error.status === 409 &&
    action === 'finalize'
  ) {
    message = t('lifecycle.errors.softDeleteRequired')
  }
  return (
    <p
      role="alert"
      className="rounded-md bg-danger-soft p-3 text-sm text-danger"
    >
      {message}
    </p>
  )
}

function criticalNotice(
  t: (key: string) => string,
  action: 'rotate' | 'finalize',
) {
  // The assurance bar sits on the APPROVERS' decisions, not on opening the
  // request, and the two actions differ on the emergency path: rotation may be
  // authorized under break-glass, finalize may never be. Saying "no actuation
  // until a second human approves" for both would be false for rotation — and a
  // false reassurance about an emergency bypass is worse than no notice at all.
  return (
    <div className="flex flex-col gap-2">
      <p className="font-medium text-foreground">
        {t('lifecycle.actions.critical.twoAccountsAal3')}
      </p>
      <p>
        {action === 'finalize'
          ? t('lifecycle.actions.critical.noBreakGlass')
          : t('lifecycle.actions.critical.breakGlassPossible')}
      </p>
      {action === 'finalize' && (
        <p className="font-medium text-danger">
          {t('lifecycle.actions.finalize.irreversible')}
        </p>
      )}
    </div>
  )
}

function resultMessage(
  t: (key: string, options?: Record<string, unknown>) => string,
  result: NhiActionResult,
): string {
  if (result.status === 'done') return t('lifecycle.actions.result.done')
  if (result.status === 'pending') return t('lifecycle.actions.result.pending')
  if (result.status === 'rejected')
    return t('lifecycle.actions.result.rejected')
  if (result.status === 'expired') return t('lifecycle.actions.result.expired')
  if (result.status === 'no_gate') return t('lifecycle.actions.result.noGate')
  if (result.status === 'unavailable')
    return t('lifecycle.actions.result.unavailable')
  if (result.status === 'break_glass')
    return t('lifecycle.actions.result.breakGlass')
  return t('lifecycle.actions.result.received')
}

export function NhiActions({ identity }: { identity: NhiLifecycleDTO }) {
  const { t } = useTranslation(['identity', 'common'])
  const { activeTenant, can } = useAuth()
  const canWrite = can('governance:nhi:write')
  const canAdmin = can('governance:nhi:admin')
  const [active, setActive] = useState<RowAction | null>(null)
  const [ownerRef, setOwnerRef] = useState(identity.owner_ref ?? '')
  const [sponsorRef, setSponsorRef] = useState(identity.sponsor_ref ?? '')
  const [criticality, setCriticality] = useState(identity.criticality)
  const [maxAge, setMaxAge] = useState(String(identity.max_age_seconds ?? 0))
  const [rotationTarget, setRotationTarget] = useState(
    identity.rotation_target ?? '',
  )
  const [rotatedAt, setRotatedAt] = useState(
    asLocalDateTime(identity.rotated_at),
  )
  const [reason, setReason] = useState('')
  const [credentialOpen, setCredentialOpen] = useState(false)
  const [outcome, setOutcome] = useState<{
    action: RowAction
    result: NhiActionResult
  } | null>(null)

  const invalidateKeys = [
    identityKeys.nhiAll(activeTenant),
    identityKeys.nhiDetail(activeTenant, identity.identity_ref),
    identityKeys.nhiEvents(activeTenant, identity.identity_ref),
  ]

  const ownership = usePrivilegedMutation<NhiOwnershipInput, void>({
    mutationFn: (input) =>
      identityApi.setNhiOwnership(identity.identity_ref, input),
    invalidateKeys,
    successMessage: t('lifecycle.actions.ownership.saved'),
    onDone: () => setActive(null),
  })
  const policy = usePrivilegedMutation<NhiPolicyInput, void>({
    mutationFn: (input) =>
      identityApi.setNhiPolicy(identity.identity_ref, input),
    invalidateKeys,
    successMessage: t('lifecycle.actions.policy.saved'),
    onDone: () => setActive(null),
  })

  const handleActuation = (action: RowAction, result: NhiActionResult) => {
    setActive(null)
    if (action === 'rotate' && result.new_secret) {
      setCredentialOpen(true)
      return
    }
    if (result.status !== 'done') setOutcome({ action, result })
  }

  const rotate = usePrivilegedMutation<NhiActionInput, NhiActionResult>({
    mutationFn: (input) => identityApi.rotateNhi(identity.identity_ref, input),
    invalidateKeys,
    successMessage: (result) => resultMessage(t, result),
    onDone: (result) => handleActuation('rotate', result),
  })
  const offboard = usePrivilegedMutation<NhiActionInput, NhiActionResult>({
    mutationFn: (input) =>
      identityApi.offboardNhi(identity.identity_ref, input),
    invalidateKeys,
    successMessage: (result) => resultMessage(t, result),
    onDone: (result) => handleActuation('offboard', result),
  })
  const finalize = usePrivilegedMutation<NhiActionInput, NhiActionResult>({
    mutationFn: (input) =>
      identityApi.finalizeNhi(identity.identity_ref, input),
    invalidateKeys,
    successMessage: (result) => resultMessage(t, result),
    onDone: (result) => handleActuation('finalize', result),
  })
  const restore = usePrivilegedMutation<void, NhiActionResult>({
    mutationFn: () => identityApi.restoreNhi(identity.identity_ref),
    invalidateKeys,
    successMessage: (result) => resultMessage(t, result),
    onDone: (result) => handleActuation('restore', result),
  })

  const openAction = (action: RowAction) => {
    setReason('')
    if (action === 'ownership') {
      setOwnerRef(identity.owner_ref ?? '')
      setSponsorRef(identity.sponsor_ref ?? '')
      ownership.reset()
    }
    if (action === 'policy') {
      setCriticality(identity.criticality)
      setMaxAge(String(identity.max_age_seconds ?? 0))
      setRotationTarget(identity.rotation_target ?? '')
      setRotatedAt(asLocalDateTime(identity.rotated_at))
      policy.reset()
    }
    if (action === 'rotate') rotate.reset()
    if (action === 'offboard') offboard.reset()
    if (action === 'finalize') finalize.reset()
    if (action === 'restore') restore.reset()
    setActive(action)
  }

  const currentOwnerRef = (identity.owner_ref ?? '').trim()
  const currentSponsorRef = (identity.sponsor_ref ?? '').trim()
  const ownerDraft = ownerRef.trim()
  const sponsorDraft = sponsorRef.trim()
  const ownerChanged = ownerDraft !== currentOwnerRef
  const sponsorChanged = sponsorDraft !== currentSponsorRef
  const ownershipChanged = ownerChanged || sponsorChanged
  const ownershipWouldClear =
    (ownerChanged && ownerDraft === '') ||
    (sponsorChanged && sponsorDraft === '')
  const agentSponsorMissing = identity.kind === 'agent' && sponsorDraft === ''
  const ownershipCannotSubmit = ownershipWouldClear || agentSponsorMissing

  // The server puts the assurance bar on the APPROVERS' decisions
  // (approvals.go: a CRITICAL decision demands an AAL3 session), not on the
  // principal opening the request — the LifecycleGate carries no principal or
  // AAL at all, so it is structurally incapable of gating the requester. The
  // console used to block the request outright on the requester's AAL, which
  // stopped a legitimate operator from even queueing work for a hardware-backed
  // approver to decide. The notice states who needs what instead.

  return (
    <>
      <ActionButtons
        canWrite={canWrite}
        canAdmin={canAdmin}
        offboardState={identity.offboard_state}
        onOpen={openAction}
      />

      <ConfirmDialog
        open={active === 'ownership'}
        onOpenChange={(open) => !open && setActive(null)}
        title={t('lifecycle.actions.ownership.title')}
        description={t('lifecycle.actions.ownership.description')}
        confirmLabel={t('lifecycle.actions.ownership.confirm')}
        confirmDisabled={!ownershipChanged || ownershipCannotSubmit}
        pending={ownership.isPending}
        onConfirm={() =>
          ownership.mutate({
            // Normally send only what the operator changed because the server
            // skips empty refs. Agents are the exception: owner validation also
            // requires their existing sponsor, so carry that field forward even
            // when only the owner changed.
            owner_ref: ownerChanged ? ownerDraft : '',
            sponsor_ref: sponsorChanged
              ? sponsorDraft
              : identity.kind === 'agent' && ownerChanged
                ? currentSponsorRef
                : '',
          })
        }
      >
        <div className="flex flex-col gap-3">
          <Field
            label={t('lifecycle.actions.ownership.owner')}
            htmlFor="nhi-owner-ref"
          >
            <Input
              id="nhi-owner-ref"
              value={ownerRef}
              onChange={(event) => setOwnerRef(event.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('lifecycle.actions.ownership.sponsor')}
            htmlFor="nhi-sponsor-ref"
          >
            <Input
              id="nhi-sponsor-ref"
              value={sponsorRef}
              onChange={(event) => setSponsorRef(event.target.value)}
              mono
            />
          </Field>
          <p>{t('lifecycle.actions.ownership.humanOnly')}</p>
          {ownershipCannotSubmit ? (
            <p>{t('lifecycle.actions.ownership.cannotClear')}</p>
          ) : !ownershipChanged ? (
            <p>{t('lifecycle.actions.ownership.noChanges')}</p>
          ) : null}
          <ActionError error={ownership.error} action="ownership" />
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={active === 'policy'}
        onOpenChange={(open) => !open && setActive(null)}
        title={t('lifecycle.actions.policy.title')}
        description={t('lifecycle.actions.policy.description')}
        confirmLabel={t('lifecycle.actions.policy.confirm')}
        pending={policy.isPending}
        onConfirm={() =>
          policy.mutate({
            criticality,
            max_age_seconds: Number(maxAge) || 0,
            rotation_target: rotationTarget.trim(),
            rotated_at: asRfc3339(rotatedAt),
          })
        }
      >
        <div className="flex flex-col gap-3">
          <p className="rounded-md bg-warning-soft p-3 font-medium text-warning">
            {t('lifecycle.actions.policy.enforcementWarning')}
          </p>
          <Field
            label={t('lifecycle.actions.policy.criticality')}
            htmlFor="nhi-criticality"
          >
            <Select value={criticality} onValueChange={setCriticality}>
              <SelectTrigger
                id="nhi-criticality"
                aria-label={t('lifecycle.actions.policy.criticality')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CRITICALITIES.map((value) => (
                  <SelectItem key={value} value={value}>
                    {t(`lifecycle.criticality.${value}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <p className="text-xs text-muted-foreground">
            {t('lifecycle.actions.policy.partialUpdateNote')}
          </p>
          <Field
            label={t('lifecycle.actions.policy.maxAge')}
            description={t('lifecycle.actions.policy.maxAgeHint')}
            htmlFor="nhi-max-age"
          >
            <Input
              id="nhi-max-age"
              type="number"
              min={0}
              value={maxAge}
              onChange={(event) => setMaxAge(event.target.value)}
            />
          </Field>
          <Field
            label={t('lifecycle.actions.policy.rotationTarget')}
            htmlFor="nhi-rotation-target"
          >
            <Input
              id="nhi-rotation-target"
              value={rotationTarget}
              onChange={(event) => setRotationTarget(event.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('lifecycle.actions.policy.rotatedAt')}
            htmlFor="nhi-rotated-at"
          >
            <Input
              id="nhi-rotated-at"
              type="datetime-local"
              value={rotatedAt}
              onChange={(event) => setRotatedAt(event.target.value)}
            />
          </Field>
          <ActionError error={policy.error} action="policy" />
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={active === 'rotate'}
        onOpenChange={(open) => !open && setActive(null)}
        title={t('lifecycle.actions.rotate.title')}
        description={t('lifecycle.actions.rotate.description')}
        confirmLabel={t('lifecycle.actions.rotate.confirm')}
        pending={rotate.isPending}
        onConfirm={() =>
          rotate.mutate({
            target_ref: rotationTarget.trim() || undefined,
            reason: reason.trim() || undefined,
          })
        }
      >
        <div className="flex flex-col gap-3">
          {criticalNotice(t, 'rotate')}
          <Field
            label={t('lifecycle.actions.rotate.target')}
            htmlFor="nhi-rotate-target"
          >
            <Input
              id="nhi-rotate-target"
              value={rotationTarget}
              onChange={(event) => setRotationTarget(event.target.value)}
              mono
            />
          </Field>
          <Field
            label={t('lifecycle.actions.reason')}
            htmlFor="nhi-rotate-reason"
          >
            <Textarea
              id="nhi-rotate-reason"
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
          <ActionError error={rotate.error} action="rotate" />
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={active === 'offboard'}
        onOpenChange={(open) => !open && setActive(null)}
        title={t('lifecycle.actions.offboard.title')}
        description={t('lifecycle.actions.offboard.description')}
        confirmLabel={t('lifecycle.actions.offboard.confirm')}
        pending={offboard.isPending}
        tone="danger"
        onConfirm={() =>
          offboard.mutate({ reason: reason.trim() || undefined })
        }
      >
        <div className="flex flex-col gap-3">
          <p>{t('lifecycle.actions.offboard.warning')}</p>
          <Field
            label={t('lifecycle.actions.reason')}
            htmlFor="nhi-offboard-reason"
          >
            <Textarea
              id="nhi-offboard-reason"
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
          <ActionError error={offboard.error} action="offboard" />
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={active === 'finalize'}
        onOpenChange={(open) => !open && setActive(null)}
        title={t('lifecycle.actions.finalize.title')}
        description={t('lifecycle.actions.finalize.description')}
        confirmLabel={t('lifecycle.actions.finalize.confirm')}
        confirmPhrase={identity.identity_ref}
        pending={finalize.isPending}
        tone="danger"
        onConfirm={() =>
          finalize.mutate({ reason: reason.trim() || undefined })
        }
      >
        <div className="flex flex-col gap-3">
          {criticalNotice(t, 'finalize')}
          <Field
            label={t('lifecycle.actions.reason')}
            htmlFor="nhi-finalize-reason"
          >
            <Textarea
              id="nhi-finalize-reason"
              value={reason}
              onChange={(event) => setReason(event.target.value)}
            />
          </Field>
          <ActionError error={finalize.error} action="finalize" />
        </div>
      </ConfirmDialog>

      <ConfirmDialog
        open={active === 'restore'}
        onOpenChange={(open) => !open && setActive(null)}
        title={t('lifecycle.actions.restore.title')}
        description={t('lifecycle.actions.restore.description')}
        confirmLabel={t('lifecycle.actions.restore.confirm')}
        pending={restore.isPending}
        onConfirm={() => restore.mutate()}
      >
        <ActionError error={restore.error} action="restore" />
      </ConfirmDialog>

      <Dialog
        open={credentialOpen}
        onOpenChange={(open) => {
          if (!open) {
            setCredentialOpen(false)
            rotate.reset()
          }
        }}
      >
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {t('lifecycle.actions.rotate.credentialTitle')}
            </DialogTitle>
            <DialogDescription>
              {t('lifecycle.actions.rotate.credentialWarning')}
            </DialogDescription>
          </DialogHeader>
          <Field
            label={t('lifecycle.actions.rotate.credential')}
            htmlFor="nhi-one-time-credential"
          >
            <Input
              id="nhi-one-time-credential"
              value={rotate.data?.new_secret ?? ''}
              readOnly
              mono
              autoComplete="off"
            />
          </Field>
          {rotate.data?.new_credential_ref && (
            <p className="text-xs text-muted-foreground">
              {t('lifecycle.actions.rotate.credentialRef', {
                ref: rotate.data.new_credential_ref,
              })}
            </p>
          )}
          <DialogFooter>
            <Button
              variant="primary"
              onClick={() => {
                setCredentialOpen(false)
                rotate.reset()
              }}
            >
              {t('lifecycle.actions.rotate.credentialSaved')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={outcome != null}
        onOpenChange={(open) => !open && setOutcome(null)}
      >
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>{t('lifecycle.actions.result.title')}</DialogTitle>
            <DialogDescription>
              {outcome ? resultMessage(t, outcome.result) : ''}
            </DialogDescription>
          </DialogHeader>
          {outcome?.result.approval_ref && (
            <p className="font-mono text-sm break-all">
              {t('lifecycle.actions.result.approval', {
                ref: outcome.result.approval_ref,
              })}
            </p>
          )}
          <DialogFooter>
            <Button variant="primary" onClick={() => setOutcome(null)}>
              {t('common:actions.close')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

function ActionButtons({
  canWrite,
  canAdmin,
  offboardState,
  onOpen,
}: {
  canWrite: boolean
  canAdmin: boolean
  offboardState: string
  onOpen: (action: RowAction) => void
}) {
  const { t } = useTranslation('identity')
  return (
    <div className="flex flex-wrap gap-2">
      {canWrite && (
        <>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onOpen('ownership')}
          >
            <UserRoundCog className="size-4" aria-hidden />
            {t('lifecycle.actions.ownership.button')}
          </Button>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onOpen('policy')}
          >
            <Settings2 className="size-4" aria-hidden />
            {t('lifecycle.actions.policy.button')}
          </Button>
        </>
      )}
      {canAdmin && (
        <>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => onOpen('rotate')}
          >
            <RotateCcw className="size-4" aria-hidden />
            {t('lifecycle.actions.rotate.button')}
          </Button>
          {offboardState === 'none' && (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => onOpen('offboard')}
            >
              <ShieldOff className="size-4" aria-hidden />
              {t('lifecycle.actions.offboard.button')}
            </Button>
          )}
          {offboardState === 'soft_deleted' && (
            <>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => onOpen('restore')}
              >
                {t('lifecycle.actions.restore.button')}
              </Button>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => onOpen('finalize')}
              >
                {t('lifecycle.actions.finalize.button')}
              </Button>
            </>
          )}
        </>
      )}
    </div>
  )
}

export function NhiSweepAction() {
  const { t } = useTranslation('identity')
  const { activeTenant, can } = useAuth()
  const [open, setOpen] = useState(false)
  const [report, setReport] = useState<NhiSweepReport | null>(null)
  const sweep = usePrivilegedMutation<void, NhiSweepReport>({
    mutationFn: () => identityApi.sweepNhi(),
    invalidateKeys: [identityKeys.nhiAll(activeTenant)],
    successMessage: t('lifecycle.actions.sweep.done'),
    onDone: (result) => {
      setReport(result)
      setOpen(false)
    },
  })

  if (!can('governance:nhi:admin')) return null
  return (
    <div className="flex flex-col items-end gap-1">
      <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>
        <RotateCcw className="size-4" aria-hidden />
        {t('lifecycle.actions.sweep.button')}
      </Button>
      {report && (
        <p role="status" className="text-xs text-muted-foreground">
          {t('lifecycle.actions.sweep.result', {
            scanned: report.scanned,
            blocked: report.blocked,
          })}
        </p>
      )}
      <ConfirmDialog
        open={open}
        onOpenChange={setOpen}
        title={t('lifecycle.actions.sweep.title')}
        description={t('lifecycle.actions.sweep.description')}
        confirmLabel={t('lifecycle.actions.sweep.confirm')}
        pending={sweep.isPending}
        onConfirm={() => sweep.mutate()}
      >
        <div className="flex flex-col gap-3">
          <p className="rounded-md bg-warning-soft p-3 font-medium text-warning">
            {t('lifecycle.actions.sweep.warning')}
          </p>
          <ActionError error={sweep.error} action="policy" />
        </div>
      </ConfirmDialog>
    </div>
  )
}
