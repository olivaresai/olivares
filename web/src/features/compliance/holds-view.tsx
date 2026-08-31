// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// Legal holds (preservation plane) in the console — E1.
//
// Until this file existed, the ONLY way to place a preservation order was `curl`
// or the API playground: the engine has had the whole plane since (create,
// list, custody trail, dual-control release) and no console reached it.
//
// The two asymmetric verbs of this plane drive the whole design:
//
//   SET is immediate and ungated — the duty to preserve admits no waiting
//   (holds.go:266). So the create dialog does not gate; it INFORMS. What it must
//   not do is let an operator guess: a hold that does not match is silent
//   under-preservation, so the dialog previews the live matching rule (GET
//   /holds/check) before confirming and offers the §2 class registry rather than
//   a text box (a data_class hold is rejected unless the id matches exactly,
//   holds.go:309).
//
//   RELEASE is CRITICAL dual-control, no break-glass (holds.go:520). Two distinct
//   humans, re-verified independently of the gate. The console's job is to never
//   imply it happened when it did not: a 202 leaves the hold ACTIVE and the UI
//   says so in those words.
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Gavel, LockKeyhole, ShieldAlert, Unlock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
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
import { EmptyState } from '@/components/ui/empty-state'
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
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  HashChip,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import {
  approvalRefOf,
  complianceApi,
  complianceKeys,
  confirmedCreate,
  incompleteReason,
  type IncompleteReason,
} from './api'
import type { HoldScopeKind, LegalHold } from './types'

const SCOPE_KINDS: HoldScopeKind[] = ['tenant', 'data_class', 'subject']

/** Hold `value` until it stops changing for `ms`.
 *
 *  Compared structurally, not by reference: the caller builds a fresh object
 *  every render, so a reference check would re-arm the timer forever and the
 *  value would never settle. */
function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  const serialized = JSON.stringify(value)
  useEffect(() => {
    const id = setTimeout(() => setSettled(JSON.parse(serialized) as T), ms)
    return () => clearTimeout(id)
  }, [serialized, ms])
  return settled
}

/** The phrase an operator types to release a hold. Releasing lifts a preservation
 *  order — the one verb in this plane that can let evidence be destroyed — so it
 *  gets the typed-phrase guard, not just a button. */
const RELEASE_PHRASE = 'RELEASE'

export function HoldsTab({
  canAdmin,
  canRead,
}: {
  canAdmin: boolean
  canRead: boolean
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [creating, setCreating] = useState(false)
  const [releasing, setReleasing] = useState<LegalHold | null>(null)
  const [inspecting, setInspecting] = useState<LegalHold | null>(null)

  const holdsQ = useQuery({
    queryKey: complianceKeys.holds(activeTenant),
    queryFn: () => complianceApi.holds(),
    enabled: canRead,
  })

  if (!canRead) {
    return (
      <SectionCard title={t('holds.title')}>
        <EmptyState icon={<LockKeyhole />} title={t('holds.noAccess')} />
      </SectionCard>
    )
  }

  return (
    <>
      <SectionCard
        title={t('holds.title')}
        description={t('holds.description')}
        actions={
          canAdmin ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setCreating(true)}
            >
              <Gavel />
              {t('holds.create')}
            </Button>
          ) : null
        }
      >
        <SelfAuditNotice className="mb-3" />
        {/* The asymmetry is the product rule, so it is stated where the operator
            acts, not buried in docs. */}
        <CaveatNotice tone="info" className="mb-3">
          {t('holds.asymmetryHint')}
        </CaveatNotice>
        <AsyncSection query={holdsQ} skeletonHeight={220}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                icon={<Gavel />}
                title={t('holds.empty')}
                description={t('holds.emptyHint')}
              />
            ) : (
              <div className="flex flex-col gap-2">
                {list.items.map((hold) => (
                  <HoldRow
                    key={hold.id}
                    hold={hold}
                    canAdmin={canAdmin}
                    onRelease={() => setReleasing(hold)}
                    onInspect={() => setInspecting(hold)}
                  />
                ))}
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      {inspecting ? (
        <HoldCustodyTrail
          hold={inspecting}
          onClose={() => setInspecting(null)}
        />
      ) : null}

      {canAdmin ? (
        <CreateHoldDialog open={creating} onOpenChange={setCreating} />
      ) : null}

      {canAdmin && releasing ? (
        <ReleaseHoldDialog
          hold={releasing}
          open={releasing !== null}
          onOpenChange={(v) => {
            if (!v) setReleasing(null)
          }}
        />
      ) : null}
    </>
  )
}

/** One hold. The scope is rendered as what it actually MATCHES, not as a raw enum:
 *  "everything in this tenant" is the honest reading of a tenant-scope hold, and
 *  an operator scanning this list is deciding whether preservation covers a thing. */
function HoldRow({
  hold,
  canAdmin,
  onRelease,
  onInspect,
}: {
  hold: LegalHold
  canAdmin: boolean
  onRelease: () => void
  onInspect: () => void
}) {
  const { t } = useTranslation('compliance')
  const active = hold.status === 'active'
  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={active ? 'warning' : 'neutral'}>
            {t(`holds.status.${hold.status}`)}
          </Badge>
          <span className="font-medium">{hold.matter_ref}</span>
          {hold.title ? (
            <span className="text-sm text-muted-foreground">{hold.title}</span>
          ) : null}
        </div>
        <p className="text-xs text-muted-foreground">
          {holdScopeLabel(hold, t)}
        </p>
        <p className="text-xs text-muted-foreground">
          {t('holds.reason')}:{' '}
          <span className="text-foreground">{hold.reason}</span>
        </p>
        <p className="text-xs text-muted-foreground">
          {t('holds.createdBy', {
            actor: hold.created_by,
            at: hold.created_at,
          })}
        </p>
        {hold.released_at ? (
          <p className="text-xs text-muted-foreground">
            {t('holds.releasedBy', {
              actor: hold.released_by ?? '',
              at: hold.released_at,
            })}
            {hold.release_approval_ref ? (
              <>
                {' · '}
                <HashChip
                  hash={hold.release_approval_ref}
                  label={t('holds.approvalRef')}
                />
              </>
            ) : null}
          </p>
        ) : null}
      </div>
      <div className="flex shrink-0 gap-2">
        <Button variant="ghost" size="sm" onClick={onInspect}>
          {t('holds.custody')}
        </Button>
        {canAdmin && active ? (
          <Button variant="secondary" size="sm" onClick={onRelease}>
            <Unlock />
            {t('holds.release')}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

/** Render a hold's scope as the rule it enforces (holds.go:132 holdCovers). */
function holdScopeLabel(
  hold: LegalHold,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  switch (hold.scope_kind) {
    case 'tenant':
      return t('holds.scope.tenantLabel')
    case 'data_class':
      return t('holds.scope.classLabel', { class: hold.data_class ?? '' })
    case 'subject':
      return t('holds.scope.subjectLabel', {
        kind: hold.subject_kind ?? '',
        ref: hold.subject_ref ?? '',
      })
    default:
      return hold.scope_kind
  }
}

// --- create ------------------------------------------------------------------

function CreateHoldDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [matterRef, setMatterRef] = useState('')
  const [title, setTitle] = useState('')
  const [scopeKind, setScopeKind] = useState<HoldScopeKind>('subject')
  const [dataClass, setDataClass] = useState('')
  const [subjectKind, setSubjectKind] = useState('')
  const [subjectRef, setSubjectRef] = useState('')
  const [reason, setReason] = useState('')

  // The §2 registry. A data_class hold is REJECTED unless this id matches exactly
  // (holds.go:309), so the operator picks rather than types.
  const classesQ = useQuery({
    queryKey: complianceKeys.dataClasses(activeTenant),
    queryFn: () => complianceApi.dataClasses(),
    enabled: open && scopeKind === 'data_class',
  })

  // THE SCOPE PREVIEW. This asks the engine's own §4 matching rule what is
  // ALREADY covered for the identifiers being typed — the same call the erasure
  // path makes before destroying anything. It answers the question an operator
  // otherwise cannot ask without curl: "is this already preserved?"
  //
  // Two things this must not do, and they pull in opposite directions:
  //
  //   Without debouncing it fires one request per KEYSTROKE — typing "u-7" asks
  //   the engine about "u", "u-" and "u-7".
  //
  //   With debouncing, the naive version is WORSE than the noise it fixes: while
  //   the debounce is pending, the query key still holds the previous value, so
  //   the panel would show the answer for "u-" under a field that reads "u-7".
  //   A stale "nothing preserves this" is the expensive failure of this whole
  //   dialog — it is the sentence an operator acts on.
  //
  // So the query is keyed on the SETTLED value, and the result is only rendered
  // when the settled value still equals what is on screen (previewCurrent).
  const query = {
    subject_kind: scopeKind === 'subject' ? subjectKind.trim() : undefined,
    subject_ref: scopeKind === 'subject' ? subjectRef.trim() : undefined,
    data_class: scopeKind === 'data_class' ? dataClass.trim() : undefined,
  }
  const settled = useDebounced(query, 300)
  const previewEnabled =
    open &&
    ((scopeKind === 'subject' &&
      (settled.subject_kind ?? '') !== '' &&
      (settled.subject_ref ?? '') !== '') ||
      (scopeKind === 'data_class' && (settled.data_class ?? '') !== ''))
  const previewQ = useQuery({
    queryKey: complianceKeys.holdCheck(activeTenant, settled),
    queryFn: () => complianceApi.checkHold(settled),
    enabled: previewEnabled,
    // The client default is staleTime 30s (lib/api/query.ts:23), which is right
    // for a dashboard and WRONG here. The sol-max contrast found the case: preview
    // u-7, place the hold, reopen within 30 seconds — TanStack serves the cached
    // held=false without asking the engine, so the panel says nothing preserves a
    // subject that is now preserved. The inverse (still "held" after a release) is
    // the same bug. This one query answers "is it preserved RIGHT NOW", so it is
    // never served from cache.
    staleTime: 0,
    refetchOnMount: 'always',
  })
  /** Is the answer on screen the answer to the question on screen — the same
   *  identifiers AND not a value being refetched? */
  const previewCurrent =
    settled.subject_kind === query.subject_kind &&
    settled.subject_ref === query.subject_ref &&
    settled.data_class === query.data_class &&
    !previewQ.isFetching

  const reset = () => {
    setMatterRef('')
    setTitle('')
    setScopeKind('subject')
    setDataClass('')
    setSubjectKind('')
    setSubjectRef('')
    setReason('')
  }

  const create = useMutation({
    mutationFn: () =>
      complianceApi.createHold({
        matter_ref: matterRef.trim(),
        title: title.trim() || undefined,
        scope_kind: scopeKind,
        data_class: scopeKind === 'data_class' ? dataClass.trim() : undefined,
        subject_kind: scopeKind === 'subject' ? subjectKind.trim() : undefined,
        subject_ref: scopeKind === 'subject' ? subjectRef.trim() : undefined,
        reason: reason.trim(),
      }),
    onSuccess: (res) => {
      // ALLOWLIST: a 2xx is not evidence the hold exists. Only a created record
      // reporting `active` is.
      if (confirmedCreate(res, 'active')) {
        toast.success(t('holds.dialog.created'))
      } else {
        toast.warning(t('holds.dialog.createUnconfirmed'))
      }
      // Same reason as the release path: this invalidates the preview too.
      void qc.invalidateQueries({
        queryKey: complianceKeys.holdsAll(activeTenant),
      })
      onOpenChange(false)
      reset()
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const valid =
    matterRef.trim() !== '' &&
    reason.trim() !== '' &&
    (scopeKind === 'tenant' ||
      (scopeKind === 'data_class' && dataClass.trim() !== '') ||
      (scopeKind === 'subject' &&
        subjectKind.trim() !== '' &&
        subjectRef.trim() !== ''))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('holds.dialog.createTitle')}</DialogTitle>
          <DialogDescription>
            {t('holds.dialog.createDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          <Field
            label={t('holds.dialog.matterRef')}
            description={t('holds.dialog.matterHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={matterRef}
                onChange={(e) => setMatterRef(e.target.value)}
                required
              />
            )}
          </Field>
          <Field label={t('holds.dialog.title')}>
            {({ id }) => (
              <Input
                id={id}
                value={title}
                onChange={(e) => setTitle(e.target.value)}
              />
            )}
          </Field>
          <Field
            label={t('holds.dialog.scope')}
            description={t('holds.dialog.scopeHint')}
          >
            <Select
              value={scopeKind}
              onValueChange={(v) => setScopeKind(v as HoldScopeKind)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SCOPE_KINDS.map((s) => (
                  <SelectItem key={s} value={s}>
                    {t(`holds.scope.${s}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          {scopeKind === 'tenant' ? (
            <CaveatNotice tone="warning">
              {t('holds.dialog.tenantWarning')}
            </CaveatNotice>
          ) : null}

          {scopeKind === 'data_class' ? (
            <Field
              label={t('holds.dialog.dataClass')}
              description={t('holds.dialog.dataClassHint')}
            >
              <Select value={dataClass} onValueChange={setDataClass}>
                <SelectTrigger>
                  <SelectValue
                    placeholder={t('holds.dialog.dataClassPlaceholder')}
                  />
                </SelectTrigger>
                <SelectContent>
                  {(classesQ.data?.items ?? []).map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          ) : null}

          {scopeKind === 'subject' ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label={t('holds.dialog.subjectKind')}>
                {({ id }) => (
                  <Input
                    id={id}
                    value={subjectKind}
                    onChange={(e) => setSubjectKind(e.target.value)}
                    placeholder="user"
                  />
                )}
              </Field>
              <Field label={t('holds.dialog.subjectRef')}>
                {({ id }) => (
                  <Input
                    id={id}
                    value={subjectRef}
                    onChange={(e) => setSubjectRef(e.target.value)}
                  />
                )}
              </Field>
            </div>
          ) : null}

          {/* The scope preview: the engine's own answer, before confirming.
              Rendered ONLY while the answer matches the question on screen — an
              answer about a previous keystroke is worse than no answer. */}
          {previewEnabled && previewQ.data && previewCurrent ? (
            <CaveatNotice tone={previewQ.data.held ? 'warning' : 'info'}>
              {previewQ.data.held
                ? t('holds.dialog.alreadyHeld', {
                    count: previewQ.data.holds?.length ?? 0,
                    matters: (previewQ.data.holds ?? [])
                      .map((h) => h.matter_ref)
                      .join(', '),
                  })
                : t('holds.dialog.notHeld')}
            </CaveatNotice>
          ) : previewEnabled || !previewCurrent ? (
            <CaveatNotice tone="neutral">
              {t('holds.dialog.checkingScope')}
            </CaveatNotice>
          ) : null}

          <Field
            label={t('holds.dialog.reason')}
            description={t('holds.dialog.reasonHint')}
          >
            {({ id }) => (
              <Textarea
                id={id}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                rows={2}
                required
              />
            )}
          </Field>
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || create.isPending}
            >
              {t('holds.dialog.confirmCreate')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- release: the dangerous verb ---------------------------------------------

function ReleaseHoldDialog({
  hold,
  open,
  onOpenChange,
}: {
  hold: LegalHold
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  /** Set when the engine answered 202: two humans must still approve, and the
   *  hold REMAINS ACTIVE. Kept in state so the dialog can say that plainly
   *  instead of closing as if the release had happened. */
  const [pending, setPending] = useState<{
    reason: IncompleteReason
    approvalRef?: string
  } | null>(null)

  const release = useMutation({
    mutationFn: () =>
      complianceApi.releaseHold(hold.id, {
        reason: reason.trim() || undefined,
      }),
    onSuccess: (res) => {
      // Invalidate the whole holds PREFIX, not just the list: the scope preview
      // lives under [compliance, tenant, holds, check, ...] and a release changes
      // exactly what it reports.
      void qc.invalidateQueries({
        queryKey: complianceKeys.holdsAll(activeTenant),
      })
      void qc.invalidateQueries({
        queryKey: complianceKeys.holdEvents(activeTenant, hold.id),
      })
      const incomplete = incompleteReason(res)
      if (incomplete !== null) {
        // NOT released. Say so; do not close on a success toast. The REASON is
        // allowlisted too: only a stated pending_approval gets the dual-control
        // wording, so an unknown 202 does not invent a cause.
        setPending({ reason: incomplete, approvalRef: approvalRefOf(res) })
        return
      }
      // ALLOWLIST: only a hold the engine reports as `released` is a release.
      // Announcing one from the HTTP status alone assumes the outcome.
      const dto = res.data as { status?: unknown } | null
      if (dto?.status === 'released') {
        toast.success(t('holds.dialog.released'))
      } else {
        toast.warning(t('holds.dialog.releaseUnconfirmed'))
      }
      onOpenChange(false)
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  if (pending) {
    return (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('holds.dialog.pendingTitle')}</DialogTitle>
            <DialogDescription>
              {t('holds.dialog.pendingDescription')}
            </DialogDescription>
          </DialogHeader>
          <CaveatNotice tone="warning">
            {pending.reason === 'pending_approval'
              ? t('holds.dialog.stillActive')
              : t('holds.dialog.releaseIncompleteUnknown')}
          </CaveatNotice>
          {pending.approvalRef ? (
            <HashChip
              hash={pending.approvalRef}
              label={t('holds.approvalRef')}
            />
          ) : null}
          <DialogFooter>
            <Button variant="primary" onClick={() => onOpenChange(false)}>
              {t('common:actions.close')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    )
  }

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      tone="danger"
      confirmPhrase={RELEASE_PHRASE}
      pending={release.isPending}
      title={t('holds.dialog.releaseTitle', { matter: hold.matter_ref })}
      description={t('holds.dialog.releaseDescription')}
      confirmLabel={t('holds.dialog.confirmRelease')}
      onConfirm={() => release.mutate()}
    >
      <div className="flex flex-col gap-3">
        {/* What is about to stop being preserved — the scope, in words. */}
        <CaveatNotice tone="warning" className="flex items-start gap-2">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" />
          <span>
            {t('holds.dialog.releaseScope', { scope: holdScopeLabel(hold, t) })}
          </span>
        </CaveatNotice>
        <CaveatNotice tone="info">{t('holds.dialog.dualControl')}</CaveatNotice>
        <Field label={t('holds.dialog.releaseReason')}>
          {({ id }) => (
            <Textarea
              id={id}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              rows={2}
            />
          )}
        </Field>
      </div>
    </ConfirmDialog>
  )
}

// --- custody trail -----------------------------------------------------------

/** The append-only, ledger-anchored chain of custody (holds.go:426). This is the
 *  artefact an auditor asks for, and it was previously unreachable outside curl. */
function HoldCustodyTrail({
  hold,
  onClose,
}: {
  hold: LegalHold
  onClose: () => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const eventsQ = useQuery({
    queryKey: complianceKeys.holdEvents(activeTenant, hold.id),
    queryFn: () => complianceApi.holdEvents(hold.id),
  })

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {t('holds.custodyTitle', { matter: hold.matter_ref })}
          </DialogTitle>
          <DialogDescription>{t('holds.custodyDescription')}</DialogDescription>
        </DialogHeader>
        <AsyncSection query={eventsQ} skeletonHeight={180}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState icon={<Gavel />} title={t('holds.custodyEmpty')} />
            ) : (
              <ol className="flex flex-col gap-2">
                {list.items.map((ev, i) => (
                  <li
                    key={`${ev.hold_id}-${ev.event}-${ev.occurred_at}-${i}`}
                    className="rounded-md border border-border p-2 text-xs"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant="neutral">
                        {t(`holds.event.${ev.event}`, {
                          defaultValue: ev.event,
                        })}
                      </Badge>
                      <span className="text-muted-foreground">
                        {ev.occurred_at}
                      </span>
                      <span>{ev.actor}</span>
                      {ev.actor_kind ? (
                        <span className="text-muted-foreground">
                          ({ev.actor_kind})
                        </span>
                      ) : null}
                    </div>
                    {ev.note ? <p className="mt-1">{ev.note}</p> : null}
                    {ev.approvers && ev.approvers.length > 0 ? (
                      <p className="mt-1 text-muted-foreground">
                        {t('holds.approvers', {
                          list: ev.approvers.join(', '),
                        })}
                      </p>
                    ) : null}
                    <div className="mt-1 flex flex-wrap items-center gap-2">
                      <span className="text-muted-foreground">
                        {t('holds.ledgerSeq', { seq: ev.ledger_seq })}
                      </span>
                      {ev.ledger_hash ? (
                        <HashChip hash={ev.ledger_hash} />
                      ) : null}
                    </div>
                  </li>
                ))}
              </ol>
            )
          }
        </AsyncSection>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t('common:actions.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
