// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// Retention schedules, sweeps and destruction certificates in the
// console —.
//
// The engine has registered six routes since (compliance.go:575-580). ONE of
// them had a console: GET /retention/classes, as the class dropdown of the legal-hold
// dialog (api.ts, holds-view.tsx:303-306). The other five — read the schedules,
// author one, delete one, run a sweep, read the certificates — were reachable only
// through curl or the API playground. So the operation gap was total even though the
// route count was not: nobody could see what was scheduled to be destroyed.
//
// Four things drive the design, and each is a statement this screen must never get
// wrong:
//
//   THE REGISTRY IS THE FRAME, THE SCHEDULE IS THE ANNOTATION. A class with no
//   policy is not an empty list — it is a class NOTHING disposes of. Listing only
//   the policies would render "no schedules" identically to "seven classes, none
//   scheduled", and only the second is the truth an operator needs.
//
//   ENABLING A PURGE IS THE GATED ACT, NOT SAVING A DOCUMENT (retention.go:240-246).
//   The engine answers 202 when it opened an approval instead: the policy IS
//   persisted, with enabled=false, and NOTHING will be destroyed. That 202 has the
//   shape of a success and must never be rendered as one.
//
//   A SWEEP DELETES. Its confirmation therefore carries the scope in the dialog
//   itself — which classes, which windows — computed from the enabled purge
//   schedules, never a tooltip and never "are you sure?".
//
//   A SWEEP THAT RAN IS NOT A SWEEP THAT FINISHED. `truncated` means it hit the
//   per-class iteration cap (retention.go:590) and `skipped_class_hold` means a
//   legal hold beat it. Both come back inside a 200, so a status-shaped success
//   check reports them as a clean pass.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CalendarClock,
  FileClock,
  LockKeyhole,
  ShieldAlert,
  Trash2,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  HashChip,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { complianceApi, complianceKeys } from './api'
import type {
  DataClassEntry,
  RetentionDisposition,
  RetentionPolicy,
  RetentionSummary,
} from './types'

/** The phrase an operator types to run a sweep. A sweep DESTROYS rows under every
 *  enabled purge schedule at once, so it gets the typed-phrase guard the release of
 *  a legal hold gets — the two verbs of this module that let data disappear. */
const SWEEP_PHRASE = 'PURGE'

/** One class as the screen reasons about it: the registry entry always, its
 *  schedule when one exists. */
interface ClassRow {
  entry: DataClassEntry
  policy?: RetentionPolicy
}

/** The window the SWEEP actually cuts at — which is NOT always the one the schedule
 *  says, and the difference is a claim about whether records still exist.
 *
 * clamps the cutoff UP to any regulatory floor in force (retention.go:579-581).
 *  The author-time refusal (retention.go:262-267) only guards NEW writes, so a stored
 *  30-day purge is left behind whenever the floor is raised — or the enterprise add-on
 *  is linked at all — after the schedule was written. Rendering `retention_days` there
 *  would tell a DPO that records under a SEC 17a-4 six-year floor are deleted after a
 *  month, and the direction of that error is the expensive one: it reports as destroyed
 *  data that is still preserved, still discoverable and still disclosable.
 *
 *  Open-core: the governor is nil, no floor is served, and this returns retention_days
 *  unchanged (retention.go:83-84) — so nothing on this screen moves. */
function effectivePurgeDays(policy: RetentionPolicy): number {
  const floor = policy.regulatory_floor?.min_days ?? 0
  return floor > policy.retention_days ? floor : policy.retention_days
}

/** Is the sweep window being held open by a floor rather than by the schedule? */
function isClampedByFloor(policy: RetentionPolicy): boolean {
  return effectivePurgeDays(policy) !== policy.retention_days
}

export function RetentionTab({
  canAdmin,
  canRead,
}: {
  canAdmin: boolean
  canRead: boolean
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [editing, setEditing] = useState<ClassRow | null>(null)
  const [deleting, setDeleting] = useState<ClassRow | null>(null)
  const [sweeping, setSweeping] = useState(false)
  const [lastSweep, setLastSweep] = useState<RetentionSummary | null>(null)

  const classesQ = useQuery({
    queryKey: complianceKeys.dataClasses(activeTenant),
    queryFn: () => complianceApi.dataClasses(),
    enabled: canRead,
  })
  const policiesQ = useQuery({
    queryKey: complianceKeys.retentionPolicies(activeTenant),
    queryFn: () => complianceApi.retentionPolicies(),
    enabled: canRead,
    // The client default is staleTime 30s (lib/api/query.ts:23), which is right for a
    // dashboard and wrong for this list, because this list is ALSO the sweep's scope
    // preview. The same lesson holds-view learned for /holds/check: arm a purge, open
    // the sweep dialog within 30 seconds, and TanStack answers from cache — so the
    // confirmation would omit a class the sweep is about to delete.
    staleTime: 0,
    refetchOnMount: 'always',
  })

  if (!canRead) {
    return (
      <SectionCard title={t('retention.title')}>
        <EmptyState icon={<LockKeyhole />} title={t('retention.noAccess')} />
      </SectionCard>
    )
  }

  const policies = policiesQ.data?.items ?? []
  const byClass = new Map(policies.map((p) => [p.data_class, p]))
  const rows: ClassRow[] = (classesQ.data?.items ?? []).map((entry) => ({
    entry,
    policy: byClass.get(entry.id),
  }))
  /** What a sweep would act on RIGHT NOW: the engine's own worklist rule — enabled
   *  AND disposition=purge (retention.go:551). Everything else is documentation. */
  const armed = policies.filter((p) => p.enabled && p.disposition === 'purge')
  /** ⛔ DO WE ACTUALLY KNOW THE SCOPE? `policies` falls back to [] while the read is
   *  loading or failed, and `armed` is derived from it — so an unread list and a tenant
   *  with nothing armed produce the SAME empty array. The list itself distinguishes the
   *  two (AsyncSection renders the error), but the sweep CONFIRMATION would have said
   *  "this sweep will delete nothing" over a failed read while the engine holds armed
   *  schedules and deletes on the very same click: the engine rebuilds its worklist
   *  server-side and never sees our preview (retention.go:535-565).
   *
   *  That is the most expensive defect in this repository — "nothing" and "I could not
   *  look" rendered the same — reached in the destructive verb after being closed in
   *  the list. `isFetching` counts as unknown too: a refetch in flight means the answer
   *  on screen is the previous one. */
  const scopeKnown = policiesQ.isSuccess && !policiesQ.isFetching

  return (
    <>
      <SectionCard
        title={t('retention.title')}
        description={t('retention.description')}
        actions={
          canAdmin ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setSweeping(true)}
            >
              <Trash2 />
              {t('retention.sweepNow')}
            </Button>
          ) : null
        }
      >
        <SelfAuditNotice className="mb-3" />
        {/* The model, stated where the operator acts: a schedule is a document
            until a human approves the purge, and only then does anything vanish. */}
        <CaveatNotice tone="info" className="mb-3">
          {t('retention.gateHint')}
        </CaveatNotice>
        <AsyncSection query={classesQ} skeletonHeight={260}>
          {(list) =>
            list.items.length === 0 ? (
              // The registry is a compiled-in constant (dataclass.go:80), so this is
              // not "you have no data classes" — it is an engine that served none,
              // and saying so is the difference between a shrug and a bug report.
              <EmptyState
                icon={<CalendarClock />}
                title={t('retention.noClasses')}
                description={t('retention.noClassesHint')}
              />
            ) : (
              <AsyncSection query={policiesQ} skeletonHeight={200}>
                {() => (
                  <div className="flex flex-col gap-2">
                    {rows.map((row) => (
                      <ClassScheduleRow
                        key={row.entry.id}
                        row={row}
                        canAdmin={canAdmin}
                        onEdit={() => setEditing(row)}
                        onDelete={() => setDeleting(row)}
                      />
                    ))}
                  </div>
                )}
              </AsyncSection>
            )
          }
        </AsyncSection>
      </SectionCard>

      {lastSweep ? (
        <SweepResultCard
          summary={lastSweep}
          onDismiss={() => setLastSweep(null)}
        />
      ) : null}

      <RetentionRunsCard classes={classesQ.data?.items ?? []} />

      {canAdmin && editing ? (
        <ScheduleDialog
          row={editing}
          open={editing !== null}
          onOpenChange={(v) => {
            if (!v) setEditing(null)
          }}
        />
      ) : null}

      {canAdmin && deleting ? (
        <DeleteScheduleDialog
          row={deleting}
          open={deleting !== null}
          onOpenChange={(v) => {
            if (!v) setDeleting(null)
          }}
        />
      ) : null}

      {canAdmin ? (
        <SweepDialog
          open={sweeping}
          armed={armed}
          scopeKnown={scopeKnown}
          onRetryScope={() => void policiesQ.refetch()}
          onOpenChange={setSweeping}
          onSwept={setLastSweep}
        />
      ) : null}
    </>
  )
}

// --- one class ---------------------------------------------------------------

/** A class and what governs it. The unscheduled case is rendered as a STATEMENT
 *  ("nothing disposes of this"), not as a blank: an operator reading this list is
 *  answering "what is being deleted, and when". */
function ClassScheduleRow({
  row,
  canAdmin,
  onEdit,
  onDelete,
}: {
  row: ClassRow
  canAdmin: boolean
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation('compliance')
  const { entry, policy } = row
  const purging = policy?.disposition === 'purge' && policy.enabled
  const floor = policy?.regulatory_floor ?? entry.regulatory_floor

  return (
    <div className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border p-3">
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{entry.id}</span>
          {policy ? (
            <Badge variant={purging ? 'warning' : 'neutral'}>
              {t(`retention.disposition.${policy.disposition}`)}
            </Badge>
          ) : (
            <Badge variant="neutral">{t('retention.unscheduled')}</Badge>
          )}
          {policy && !policy.enabled ? (
            <Badge variant="neutral">{t('retention.disabled')}</Badge>
          ) : null}
          {!entry.purgeable ? (
            <Badge variant="neutral">{t('retention.notPurgeable')}</Badge>
          ) : null}
          {entry.model_io ? (
            <Badge variant="neutral">{t('retention.modelIo')}</Badge>
          ) : null}
        </div>

        {policy ? (
          <p className="text-xs text-muted-foreground">
            {!purging
              ? t('retention.rowRetaining', { days: policy.retention_days })
              : isClampedByFloor(policy)
                ? // The schedule and the sweep disagree, and the sweep wins.
                  t('retention.rowPurgingClamped', {
                    days: effectivePurgeDays(policy),
                    scheduled: policy.retention_days,
                    basis: policy.regulatory_floor?.basis ?? '',
                  })
                : t('retention.rowPurging', { days: policy.retention_days })}
          </p>
        ) : (
          // NOT an empty cell: the absence of a schedule is the whole answer.
          <p className="text-xs text-muted-foreground">
            {entry.recommended_days
              ? t('retention.rowUnscheduledAdvised', {
                  days: entry.recommended_days,
                })
              : t('retention.rowUnscheduled')}
          </p>
        )}

        {policy?.basis ? (
          <p className="text-xs text-muted-foreground">
            {t('retention.basis')}:{' '}
            <span className="text-foreground">{policy.basis}</span>
          </p>
        ) : null}

        {entry.note ? (
          <p className="text-xs text-muted-foreground">{entry.note}</p>
        ) : null}

        {/* §7. The number a tenant may DISCLOSE is not the schedule: the provider
            keeps model I/O for its own floor, so "gone after N days" is a promise
            only max(schedule, floor) supports. An ABSENT value means the floor is
            unknown — which is not zero, and must not read as one. */}
        {policy && entry.model_io ? (
          <p className="text-xs text-muted-foreground">
            {policy.effective_disclosure_days
              ? t('retention.disclosure', {
                  days: policy.effective_disclosure_days,
                })
              : t('retention.disclosureUnknown')}
          </p>
        ) : null}

        {floor ? (
          <p className="text-xs text-warning">
            {t('retention.regulatoryFloor', {
              days: floor.min_days,
              basis: floor.basis,
              mode: floor.mode,
            })}
          </p>
        ) : null}

        {policy?.approval_ref ? (
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">
              {t('retention.approvalRef')}
            </span>
            <HashChip hash={policy.approval_ref} />
          </div>
        ) : null}
      </div>

      {canAdmin ? (
        <div className="flex shrink-0 gap-2">
          <Button variant="ghost" size="sm" onClick={onEdit}>
            {policy ? t('retention.edit') : t('retention.author')}
          </Button>
          {policy ? (
            <Button variant="secondary" size="sm" onClick={onDelete}>
              {t('retention.remove')}
            </Button>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

// --- author / edit a schedule -------------------------------------------------

function ScheduleDialog({
  row,
  open,
  onOpenChange,
}: {
  row: ClassRow
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const { entry, policy } = row

  const [days, setDays] = useState(
    String(policy?.retention_days ?? entry.recommended_days ?? 365),
  )
  const [disposition, setDisposition] = useState<RetentionDisposition>(
    policy?.disposition ?? 'retain',
  )
  const [basis, setBasis] = useState(policy?.basis ?? '')
  const [enabled, setEnabled] = useState(policy?.enabled ?? true)
  const [confirming, setConfirming] = useState(false)
  /** Set when the engine answered 202: an approval was opened and the purge is
   *  OFF. Held in state so the dialog says that instead of closing on a toast. */
  const [pending, setPending] = useState<{ approvalRef?: string } | null>(null)

  const numericDays = Number(days)
  /** The engine's own bounds (dataclass.go:179). Mirrored to keep the operator out
   *  of a round-trip for a typo — never to decide the answer, which stays the
   *  engine's: an out-of-range value it rejects is still a 400 here. */
  const validDays =
    Number.isInteger(numericDays) && numericDays >= 1 && numericDays <= 36500
  /** Purge on a non-purgeable class is a 400 (dataclass.go:186). The option is not
   *  rendered at all — the console does not offer what the engine refuses. */
  const dispositions: RetentionDisposition[] = entry.purgeable
    ? ['retain', 'purge']
    : ['retain']
  const arming = disposition === 'purge' && enabled

  const save = useMutation({
    mutationFn: () =>
      complianceApi.putRetentionPolicy(entry.id, {
        retention_days: numericDays,
        disposition,
        // Both optional server-side; sending them only when they carry a value
        // keeps the payload to the four keys the engine knows. It decodes with
        // DisallowUnknownFields (helpers.go:99) — one stray key is a flat 400.
        ...(basis.trim() ? { basis: basis.trim() } : {}),
        enabled,
      }),
    // ⛔ ON SETTLED, NOT ON SUCCESS. Three of this route's REFUSALS mutate first and
    // refuse second: for pending / expired / no-gate / rejected the engine forces
    // enabled=false and PERSISTS the schedule (retention.go:301-312), then answers 202,
    // 409, 503 or 403 (`:313-323`). Invalidating only on success left an operator who
    // edited an ARMED purge into a refusal looking at a cached row that still said
    // "rows older than N days are deleted on every sweep" while the engine had just
    // switched it off. A failed write never leaves the cache more accurate than the
    // server, so every outcome refetches.
    onSettled: () => {
      void qc.invalidateQueries({
        queryKey: complianceKeys.retentionAll(activeTenant),
      })
    },
    onSuccess: (res) => {
      if (res.status === 202) {
        // 202 = the schedule was persisted DISABLED and an approval was opened
        // (retention.go:304,315). Nothing is scheduled for destruction yet, and
        // that is exactly what an operator would otherwise assume happened.
        const body = res.data as { approval_ref?: unknown }
        setPending({
          approvalRef:
            typeof body?.approval_ref === 'string'
              ? body.approval_ref
              : undefined,
        })
        return
      }
      // ALLOWLIST: what got saved is what the returned DTO says, not what was
      // typed. The deny-closed path persists the schedule with enabled=false and
      // the console must report the schedule that EXISTS.
      const dto = res.data as Partial<RetentionPolicy>
      if (typeof dto?.data_class !== 'string') {
        toast.warning(t('retention.dialog.saveUnconfirmed'))
      } else if (dto.disposition === 'purge' && dto.enabled) {
        toast.success(t('retention.dialog.savedPurging'))
      } else {
        toast.success(t('retention.dialog.savedRetaining'))
      }
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      // The engine's refusals on this route are governed answers, not faults, and
      // three of them are indistinguishable from "the server broke" if reported by
      // status class alone. Named by STATUS, never by matching the message text.
      if (e instanceof ApiError) {
        if (e.status === 503) {
          toast.warning(t('retention.dialog.noGate'))
          return
        }
        if (e.status === 409) {
          toast.warning(t('retention.dialog.approvalExpired'))
          return
        }
        if (e.status === 422) {
          // The regulatory floor refused a window shorter than the law's.
          // The engine's message names the basis and the number; it is the answer.
          toast.warning(t('retention.dialog.underFloor'), {
            description: e.message,
          })
          return
        }
        if (e.isForbidden) {
          toast.warning(t('retention.dialog.refused'), {
            description: e.message,
          })
          return
        }
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  if (pending) {
    return (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('retention.dialog.pendingTitle')}</DialogTitle>
            <DialogDescription>
              {t('retention.dialog.pendingDescription')}
            </DialogDescription>
          </DialogHeader>
          <CaveatNotice tone="warning">
            {t('retention.dialog.pendingDisabled', { class: entry.id })}
          </CaveatNotice>
          {pending.approvalRef ? (
            <HashChip
              hash={pending.approvalRef}
              label={t('retention.approvalRef')}
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

  // Arming a purge authorises REPEATED destruction, so it goes through the typed
  // phrase with the scope written into the confirmation itself.
  if (confirming) {
    return (
      <ConfirmDialog
        open={open}
        // Cancelling the review step goes BACK to the form, it does not throw the
        // schedule away: this is the "read what you are about to arm" step, and an
        // operator who reads it and wants to shorten the window should not have to
        // retype the window.
        onOpenChange={(v) => {
          if (!v) setConfirming(false)
        }}
        tone="danger"
        confirmPhrase={SWEEP_PHRASE}
        pending={save.isPending}
        title={t('retention.dialog.armTitle', { class: entry.id })}
        description={t('retention.dialog.armDescription')}
        confirmLabel={t('retention.dialog.confirmArm')}
        onConfirm={() => save.mutate()}
      >
        <div className="flex flex-col gap-3">
          <CaveatNotice tone="warning" className="flex items-start gap-2">
            <ShieldAlert className="mt-0.5 size-4 shrink-0" />
            <span>
              {t('retention.dialog.armScope', {
                class: entry.id,
                days: numericDays,
              })}
            </span>
          </CaveatNotice>
          <CaveatNotice tone="info">
            {t('retention.dialog.armApproval')}
          </CaveatNotice>
        </div>
      </ConfirmDialog>
    )
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t('retention.dialog.title', { class: entry.id })}
          </DialogTitle>
          <DialogDescription>
            {t('retention.dialog.description')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (arming) setConfirming(true)
            else save.mutate()
          }}
        >
          <Field
            label={t('retention.dialog.days')}
            description={t('retention.dialog.daysHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                type="number"
                min={1}
                max={36500}
                value={days}
                onChange={(e) => setDays(e.target.value)}
                required
              />
            )}
          </Field>

          <Field
            label={t('retention.dialog.disposition')}
            description={
              entry.purgeable
                ? t('retention.dialog.dispositionHint')
                : t('retention.dialog.notPurgeableHint')
            }
          >
            <Select
              value={disposition}
              onValueChange={(v) => setDisposition(v as RetentionDisposition)}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {dispositions.map((d) => (
                  <SelectItem key={d} value={d}>
                    {t(`retention.disposition.${d}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field
            label={t('retention.dialog.basis')}
            description={t('retention.dialog.basisHint')}
          >
            {({ id }) => (
              <Textarea
                id={id}
                value={basis}
                onChange={(e) => setBasis(e.target.value)}
                rows={2}
              />
            )}
          </Field>

          <div className="flex items-start gap-2">
            <Checkbox
              className="mt-0.5"
              checked={enabled}
              aria-label={t('retention.dialog.enabled')}
              onCheckedChange={(v) => setEnabled(v === true)}
            />
            <div className="flex flex-col">
              <span className="text-sm">{t('retention.dialog.enabled')}</span>
              <span className="text-xs text-muted-foreground">
                {t('retention.dialog.enabledHint')}
              </span>
            </div>
          </div>

          {arming ? (
            <CaveatNotice tone="warning">
              {t('retention.dialog.willDestroy', {
                class: entry.id,
                days: numericDays,
              })}
            </CaveatNotice>
          ) : null}

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
              disabled={!validDays || save.isPending}
            >
              {arming
                ? t('retention.dialog.review')
                : t('retention.dialog.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- remove a schedule --------------------------------------------------------

/** Deleting a schedule STOPS a purge — the safe direction, ungated in the engine
 *  (retention.go:397-398). It gets a confirmation because it changes governance,
 *  but not the typed phrase: treating "stop deleting things" like "delete things"
 *  teaches operators to type through guards. */
function DeleteScheduleDialog({
  row,
  open,
  onOpenChange,
}: {
  row: ClassRow
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()

  const remove = useMutation({
    mutationFn: () => complianceApi.deleteRetentionPolicy(row.entry.id),
    // Same rule as the PUT, for a different reason: this route refuses BEFORE it
    // mutates (retention.go:405-410), so nothing changed server-side — but a 404 means
    // the row we are showing does not exist any more, and that cached row is exactly
    // what would be wrong on screen afterwards.
    onSettled: () => {
      void qc.invalidateQueries({
        queryKey: complianceKeys.retentionAll(activeTenant),
      })
    },
    onSuccess: () => {
      toast.success(t('retention.dialog.removed'))
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      // A 403 here is the compliance-mode SEAL (retention.go:405-409), not a
      // permission the operator lacks — the row reached this dialog only because
      // can('compliance:retention:admin') was already true. Saying "not authorized"
      // would send them to an admin for something no admin can grant.
      //
      // ⛔ Y NO LLEVA RAMA DE CEREMONIA A PROPÓSITO, medido el 2026-08-14 y no supuesto:
      // ninguna ruta de retención puede devolver `step_up_required` hoy. `requireAAL3`
      // (core/api/middleware.go:298) se llama desde 21 sitios y TODOS están en `core/api/`,
      // ninguno en `modules/compliance/`.
      //
      // La cifra la re-conté sobre ESTE árbol porque la versión anterior de esta nota decía
      // «23 llamantes» y no era una medición: `grep requireAAL3` da 26 apariciones —21
      // llamadas `s.requireAAL3(`, 1 definición y 4 menciones en prosa—. El argumento no
      // cambia, pero una cifra presentada como medida tiene que serlo, y quien venga detrás
      // la usará para decidir si esta exclusión sigue valiendo.
      //
      // Y el cero vale porque el MISMO barrido encuentra la clase donde sí existe, que es la
      // única forma de que un cero signifique algo: `step_up_required` sale 7 veces en
      // `modules/governance/` (breakglass.go:187, approvals.go:579 entre ellas) y 0 en
      // `modules/compliance/`. Añadir aquí la rama sería código para una respuesta que el
      // motor no da. Si algún día se pone el gate en esta ruta, ESTE es el sitio, y entonces
      // la rama va DELANTE de la del sello: un step-up no es un sello.
      if (e instanceof ApiError && e.isForbidden) {
        toast.warning(t('retention.dialog.sealed'), { description: e.message })
        return
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      pending={remove.isPending}
      title={t('retention.dialog.removeTitle', { class: row.entry.id })}
      description={t('retention.dialog.removeDescription')}
      confirmLabel={t('retention.dialog.confirmRemove')}
      onConfirm={() => remove.mutate()}
    >
      <CaveatNotice tone="info">
        {t('retention.dialog.removeScope', { class: row.entry.id })}
      </CaveatNotice>
    </ConfirmDialog>
  )
}

// --- the sweep: the destructive verb ------------------------------------------

function SweepDialog({
  open,
  armed,
  scopeKnown,
  onRetryScope,
  onOpenChange,
  onSwept,
}: {
  open: boolean
  armed: RetentionPolicy[]
  scopeKnown: boolean
  onRetryScope: () => void
  onOpenChange: (v: boolean) => void
  onSwept: (s: RetentionSummary) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()

  const sweep = useMutation({
    mutationFn: () => complianceApi.runRetentionSweep(),
    onSuccess: (summary) => {
      void qc.invalidateQueries({
        queryKey: complianceKeys.retentionAll(activeTenant),
      })
      // NO success toast: the summary decides whether this was a clean pass, and
      // a 200 alone does not. The card below reports what actually happened.
      onSwept(summary)
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      // A FAILED SWEEP IS NOT A SWEEP THAT DID NOTHING. Each batch is its own
      // committed transaction (retention.go:585-646); runRetention returns on the
      // first batch error (`:647-648`) and the handler discards the partial summary
      // (`:486-492`). So rows deleted by earlier batches are already gone, and a bare
      // "request failed" toast reads as "nothing happened".
      void qc.invalidateQueries({
        queryKey: complianceKeys.retentionAll(activeTenant),
      })
      toast.error(t('retention.sweep.failedPartial'), {
        description: String((e as Error).message ?? e),
      })
      onOpenChange(false)
    },
  })

  // ⛔ REFUSE, DO NOT ADJUDICATE. Without the schedules we cannot say what this would
  // delete, and a destructive confirmation that cannot state its own scope must not be
  // offered at all — least of all as "this will delete nothing", which is what an
  // empty `armed` would have rendered.
  if (!scopeKnown) {
    return (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('retention.sweep.unknownTitle')}</DialogTitle>
            <DialogDescription>
              {t('retention.sweep.unknownDescription')}
            </DialogDescription>
          </DialogHeader>
          <CaveatNotice tone="warning">
            {t('retention.sweep.unknownScope')}
          </CaveatNotice>
          <DialogFooter>
            <Button variant="ghost" onClick={() => onOpenChange(false)}>
              {t('common:actions.close')}
            </Button>
            <Button variant="primary" onClick={onRetryScope}>
              {t('retention.sweep.retryScope')}
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
      confirmPhrase={SWEEP_PHRASE}
      pending={sweep.isPending}
      title={t('retention.sweep.title')}
      description={t('retention.sweep.description')}
      confirmLabel={t('retention.sweep.confirm')}
      onConfirm={() => sweep.mutate()}
    >
      <div className="flex flex-col gap-3">
        {/* THE SCOPE, IN THE CONFIRMATION. Not "are you sure": the exact classes
            and windows this sweep will cut, read off the same predicate the engine
            uses to build its worklist (retention.go:551). */}
        {armed.length === 0 ? (
          <CaveatNotice tone="info">
            {t('retention.sweep.noneArmed')}
          </CaveatNotice>
        ) : (
          <>
            <CaveatNotice tone="warning" className="flex items-start gap-2">
              <ShieldAlert className="mt-0.5 size-4 shrink-0" />
              <span>
                {t('retention.sweep.scopeIntro', { count: armed.length })}
              </span>
            </CaveatNotice>
            <ul className="flex flex-col gap-1 text-sm">
              {armed.map((p) => (
                <li
                  key={p.data_class}
                  className="rounded-md border border-border p-2"
                >
                  <span className="font-medium">{p.data_class}</span>{' '}
                  <span className="text-muted-foreground">
                    {isClampedByFloor(p)
                      ? t('retention.sweep.scopeItemClamped', {
                          days: effectivePurgeDays(p),
                          scheduled: p.retention_days,
                          basis: p.regulatory_floor?.basis ?? '',
                        })
                      : t('retention.sweep.scopeItem', {
                          days: p.retention_days,
                        })}
                  </span>
                </li>
              ))}
            </ul>
          </>
        )}
        <CaveatNotice tone="info">{t('retention.sweep.holdsWin')}</CaveatNotice>
      </div>
    </ConfirmDialog>
  )
}

/** What the sweep DID. Rendered as a card rather than a toast because three of its
 *  outcomes are not "done": a truncated run stopped at the iteration cap, a
 *  hold-skipped class was never touched, and rows excluded by a subject hold are
 *  still there. All three arrive inside a 200. */
function SweepResultCard({
  summary,
  onDismiss,
}: {
  summary: RetentionSummary
  onDismiss: () => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  // `classes` is a Go nil slice when no schedule was armed, and nil marshals to
  // JSON null — not []. The shared list-envelope guard only covers `items`
  // (client.ts:216), so this one is on the caller.
  const classes = summary.classes ?? []
  const incomplete = summary.truncated || summary.skipped_class_holds > 0

  return (
    <SectionCard
      title={t('retention.result.title')}
      actions={
        <Button variant="ghost" size="sm" onClick={onDismiss}>
          {t('common:actions.close')}
        </Button>
      }
    >
      <CaveatNotice tone={incomplete ? 'warning' : 'info'} className="mb-3">
        {summary.truncated
          ? t('retention.result.truncated')
          : summary.skipped_class_holds > 0
            ? t('retention.result.heldBack', {
                count: summary.skipped_class_holds,
              })
            : t('retention.result.complete')}
      </CaveatNotice>
      <p className="text-sm">
        {t('retention.result.totals', {
          examined: summary.examined,
          purged: summary.purged,
          excluded: summary.excluded_held,
        })}
      </p>
      {classes.length === 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">
          {t('retention.result.noClasses')}
        </p>
      ) : (
        <ul className="mt-2 flex flex-col gap-1">
          {classes.map((c) => (
            <li
              key={c.data_class}
              className="rounded-md border border-border p-2 text-xs"
            >
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{c.data_class}</span>
                {c.skipped_class_hold ? (
                  <Badge variant="warning">
                    {t('retention.result.skipped')}
                  </Badge>
                ) : null}
                {c.truncated ? (
                  <Badge variant="warning">
                    {t('retention.result.partial')}
                  </Badge>
                ) : null}
              </div>
              <p className="mt-1 text-muted-foreground">
                {t('retention.result.classTotals', {
                  examined: c.examined,
                  purged: c.purged,
                  excluded: c.excluded_held,
                  cutoff: c.cutoff,
                })}
              </p>
            </li>
          ))}
        </ul>
      )}
    </SectionCard>
  )
}

// --- the certificates ---------------------------------------------------------

/** The append-only, ledger-anchored destruction certificates (retention.go:682).
 *  This IS the log of destruction the Sedona third pillar asks for, and it was
 *  previously unreachable outside curl. */
function RetentionRunsCard({ classes }: { classes: DataClassEntry[] }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [filter, setFilter] = useState<string>('')

  const runsQ = useQuery({
    queryKey: complianceKeys.retentionRuns(activeTenant, filter || undefined),
    queryFn: () => complianceApi.retentionRuns(filter || undefined),
  })

  return (
    <SectionCard
      title={t('retention.runs.title')}
      description={t('retention.runs.description')}
      actions={
        <Select
          value={filter === '' ? 'all' : filter}
          onValueChange={(v) => setFilter(v === 'all' ? '' : v)}
        >
          <SelectTrigger className="w-56">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">
              {t('retention.runs.allClasses')}
            </SelectItem>
            {classes.map((c) => (
              <SelectItem key={c.id} value={c.id}>
                {c.id}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={runsQ} skeletonHeight={180}>
        {(list) =>
          list.items.length === 0 ? (
            <EmptyState
              icon={<FileClock />}
              title={t('retention.runs.empty')}
              description={t('retention.runs.emptyHint')}
            />
          ) : (
            <ol className="flex flex-col gap-2">
              {list.items.map((run) => (
                <li
                  key={run.id}
                  className="rounded-md border border-border p-2 text-xs"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="neutral">
                      {t(`retention.runs.trigger.${run.trigger}`, {
                        defaultValue: run.trigger,
                      })}
                    </Badge>
                    <span className="font-medium">{run.data_class}</span>
                    <span className="text-muted-foreground">
                      {run.occurred_at}
                    </span>
                    {run.skipped_class_hold ? (
                      <Badge variant="warning">
                        {t('retention.result.skipped')}
                      </Badge>
                    ) : null}
                    {run.truncated ? (
                      <Badge variant="warning">
                        {t('retention.result.partial')}
                      </Badge>
                    ) : null}
                  </div>
                  <p className="mt-1">
                    {t('retention.runs.counts', {
                      examined: run.examined,
                      purged: run.purged,
                      excluded: run.excluded_held,
                      cutoff: run.cutoff,
                    })}
                  </p>
                  <div className="mt-1 flex flex-wrap items-center gap-2">
                    <span className="text-muted-foreground">
                      {t('retention.runs.ledgerSeq', { seq: run.ledger_seq })}
                    </span>
                    {run.ledger_hash ? (
                      <HashChip hash={run.ledger_hash} />
                    ) : null}
                    <span className="text-muted-foreground">
                      {t('retention.runs.manifest')}
                    </span>
                    <HashChip hash={run.manifest_hash} />
                  </div>
                </li>
              ))}
            </ol>
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}
