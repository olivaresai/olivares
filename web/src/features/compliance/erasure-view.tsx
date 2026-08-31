// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// Right to erasure / DSAR in the console — E2.
//
// The engine has run a legally defensible RTBF since — hold gate, then
// CRITICAL dual control, then physical erasure, then crypto-shred, then a LIVE
// ledger re-verification, then a sealed receipt — and no console reached any of
// it. A DPO answering an Art. 17 request had `curl`.
//
// Three things this view refuses to do, because each is how an RTBF console lies:
//
//   1. It never reports an erasure that did not happen. A 202 means two humans
//      must still approve and NOTHING was destroyed (erasure.go:753); the UI says
//      that, and the request stays in the list as pending.
//   2. It never says "blocked" without saying BY WHAT. The engine's 423 carries
//      the covering holds verbatim (erasure.go:675-679); they are rendered.
//      "Blocked" alone is what sends an operator back to curl.
//   3. It never hides what SURVIVES. The receipt's retained[] block is the
//      erasure↔retention reconciliation (what stays and under which article), and
//      the provider floor is disclosed: deleting our copy does not delete the
//      provider's. An RTBF receipt that omitted those would be the dishonest kind.
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  BadgeCheck,
  FileWarning,
  Gavel,
  ShieldAlert,
  Trash2,
  UserSearch,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Skeleton } from '@/components/ui/skeleton'
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
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  DisclaimerNote,
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
import { ClaudeFilesPanel } from './claude-files-view'
import type { ErasureRequest, ErasureStatus, HoldRef } from './types'

/** What a 423 actually was. The engine raises two, with different remedies:
 *
 *   legal_hold        — a preservation order covers the subject (erasure.go:675).
 *                       Remedy: release the hold under dual control.
 *   rtbf_coordinator  — the enterprise readiness check refused (erasure.go:696),
 *                       carrying `blockers` and `warnings`.
 *
 *  Classifying by STATUS alone told the operator to go release a hold that may
 *  not exist, and dropped the blockers that say what to actually fix. */
type BlockedInfo =
  | { kind: 'legal_hold'; holds: HoldRef[]; malformed: boolean }
  | { kind: 'rtbf_coordinator'; blockers: string[]; warnings: string[] }
  | { kind: 'unknown'; message: string }

function asStringList(v: unknown): string[] {
  return Array.isArray(v)
    ? v.filter((x): x is string => typeof x === 'string')
    : []
}

function classifyBlocked(e: ApiError): BlockedInfo {
  if (e.code === 'rtbf_coordinator') {
    return {
      kind: 'rtbf_coordinator',
      blockers: asStringList(e.details?.blockers),
      warnings: asStringList(e.details?.warnings),
    }
  }
  if (e.code === 'legal_hold') {
    const raw = e.details?.holds
    // A hold entry without a matter_ref is a contract violation, not something to
    // render as a blank row: say the payload was incomplete rather than show an
    // empty name next to "this is what blocks your erasure".
    const holds = Array.isArray(raw)
      ? raw.filter(
          (h): h is HoldRef =>
            typeof h === 'object' &&
            h !== null &&
            typeof (h as HoldRef).id === 'string',
        )
      : []
    const malformed = !Array.isArray(raw) || holds.length !== raw.length
    return { kind: 'legal_hold', holds, malformed }
  }
  return { kind: 'unknown', message: e.message }
}

/** The phrase an operator types before an irreversible erasure. */
const ERASE_PHRASE = 'ERASE'

/** Statuses that can still be executed (erasure.go:67-76). completed and
 *  completed_with_gaps are TERMINAL — the engine refuses to overwrite them. */
const EXECUTABLE: ReadonlySet<ErasureStatus> = new Set<ErasureStatus>([
  'received',
  'pending_approval',
  'executing',
  'blocked_hold',
  'denied',
  'failed',
])

function statusTone(
  s: ErasureStatus,
): 'success' | 'warning' | 'danger' | 'neutral' {
  switch (s) {
    case 'completed':
      return 'success'
    case 'completed_with_gaps':
    case 'pending_approval':
    case 'blocked_hold':
    case 'executing':
      return 'warning'
    case 'denied':
    case 'failed':
      return 'danger'
    default:
      return 'neutral'
  }
}

export function ErasureTab({
  canAdmin,
  canRead,
}: {
  canAdmin: boolean
  canRead: boolean
}) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()
  const [registering, setRegistering] = useState(false)
  const [executing, setExecuting] = useState<ErasureRequest | null>(null)
  const [receipt, setReceipt] = useState<ErasureRequest | null>(null)
  const [lookup, setLookup] = useState(false)

  const listQ = useQuery({
    queryKey: complianceKeys.erasures(activeTenant),
    queryFn: () => complianceApi.erasures(),
    enabled: canRead,
  })

  if (!canRead) {
    return (
      <SectionCard title={t('erasure.title')}>
        <EmptyState icon={<Trash2 />} title={t('erasure.noAccess')} />
      </SectionCard>
    )
  }

  return (
    <>
      <SectionCard
        title={t('erasure.title')}
        description={t('erasure.description')}
        actions={
          <div className="flex gap-2">
            <Button variant="ghost" size="sm" onClick={() => setLookup(true)}>
              <UserSearch />
              {t('erasure.lookup')}
            </Button>
            {canAdmin ? (
              <Button
                variant="primary"
                size="sm"
                onClick={() => setRegistering(true)}
              >
                <FileWarning />
                {t('erasure.register')}
              </Button>
            ) : null}
          </div>
        }
      >
        <SelfAuditNotice className="mb-3" />
        <CaveatNotice tone="warning" className="mb-3">
          {t('erasure.irreversibleHint')}
        </CaveatNotice>
        <AsyncSection query={listQ} skeletonHeight={220}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                icon={<Trash2 />}
                title={t('erasure.empty')}
                description={t('erasure.emptyHint')}
              />
            ) : (
              <div className="flex flex-col gap-2">
                {list.items.map((req) => (
                  <ErasureRow
                    key={req.id}
                    req={req}
                    canAdmin={canAdmin}
                    onExecute={() => setExecuting(req)}
                    onReceipt={() => setReceipt(req)}
                  />
                ))}
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>

      {canAdmin ? (
        <RegisterErasureDialog
          open={registering}
          onOpenChange={setRegistering}
        />
      ) : null}

      {canAdmin && executing ? (
        <ExecuteErasureDialog
          req={executing}
          open={executing !== null}
          onOpenChange={(v) => {
            if (!v) setExecuting(null)
          }}
        />
      ) : null}

      {receipt ? (
        <ReceiptDialog req={receipt} onClose={() => setReceipt(null)} />
      ) : null}

      {lookup ? <DataSubjectLookup onClose={() => setLookup(false)} /> : null}

      {/* El almacén de ficheros del proveedor: inventario y borrado puntual gobernado.
          Va aquí y no en pestaña propia porque comparte permisos (`compliance:erasure:*`)
          y el operador que viene a ejercer una supresión tiene que ver AMBAS cosas — sobre
          todo el aviso de que este almacén no lleva metadatos de sujeto. */}
      <ClaudeFilesPanel canAdmin={canAdmin} canRead={canRead} />
    </>
  )
}

function ErasureRow({
  req,
  canAdmin,
  onExecute,
  onReceipt,
}: {
  req: ErasureRequest
  canAdmin: boolean
  onExecute: () => void
  onReceipt: () => void
}) {
  const { t } = useTranslation('compliance')
  const [custodia, setCustodia] = useState(false)
  const terminal =
    req.status === 'completed' || req.status === 'completed_with_gaps'
  return (
    <div className="flex flex-col gap-2 rounded-md border border-border p-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={statusTone(req.status)}>
              {t(`erasure.status.${req.status}`, { defaultValue: req.status })}
            </Badge>
            <span className="font-medium">{req.case_ref}</span>
            <span className="text-sm text-muted-foreground">
              {req.subject_kind}
            </span>
          </div>
          {/* The subject is detokenized while the key lives and becomes "[ERASED]"
            after the crypto-shred. That transition IS the visible proof, so it is
            rendered rather than smoothed over. */}
          <p className="text-xs text-muted-foreground">
            {t('erasure.subject')}:{' '}
            <span className="text-foreground">
              {req.subject ?? req.subject_token}
            </span>
          </p>
          {req.data_classes.length > 0 ? (
            <p className="text-xs text-muted-foreground">
              {t('erasure.classes', { list: req.data_classes.join(', ') })}
            </p>
          ) : null}
          <p className="text-xs text-muted-foreground">
            {t('erasure.requestedBy', {
              actor: req.requested_by,
              at: req.created_at,
            })}
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          {terminal ? (
            <Button variant="ghost" size="sm" onClick={onReceipt}>
              <BadgeCheck />
              {t('erasure.receipt')}
            </Button>
          ) : null}
          {canAdmin && EXECUTABLE.has(req.status) ? (
            <Button variant="secondary" size="sm" onClick={onExecute}>
              <Trash2 />
              {t('erasure.execute')}
            </Button>
          ) : null}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setCustodia((v) => !v)}
          >
            {t('erasure.custody')}
          </Button>
        </div>
      </div>
      {custodia ? <CustodyChain id={req.id} /> : null}
    </div>
  )
}

// --- la cadena de custodia de una solicitud (C07-04) -------------------------
//
// ⛔ POR QUÉ HACE FALTA: el estado dice EN QUÉ acabó la solicitud; la custodia dice POR QUÉ, y es
//    lo que un regulador pide. `GET /erasure/{id}/events` no tenía ninguna pantalla que lo
//    llamara, así que el expediente que justifica una supresión —o su bloqueo— sólo existía en
//    `curl`.
//
// ⛔ Y CADA EVENTO SE SELLA CONTRA LA CABEZA DEL LEDGER, PERO NO SIEMPRE HAY CABEZA.
//    `appendErasureEvent` (`modules/compliance/erasure.go:185-213`) hace
//    `head, ok := sc.Audit().Head(ctx)` y **sólo si `ok`** rellena `seq` y `hash`; si no, quedan
//    `0` y `""` (y `ledger_hash` es `omitempty`, así que ni siquiera viaja).
//
//    ⇒ Un evento **anclado** es prueba: se puede atar a la cadena firmada. Un evento **sin
//    anclar** existe igual y **no se puede probar** — es una afirmación. Pintar los dos como la
//    misma fila convierte un expediente en algo que parece demostrado y no lo está, que es
//    exactamente el error que más caro sale delante de un regulador.
//
// ⛔ Y LOS APROBADORES SE NOMBRAN. La supresión exige quórum de dos (`erasure.go:61`); un
//    «aprobado» sin decir quién pierde la evidencia del quórum, que es lo único que distingue una
//    autorización de una firma sola.
function CustodyChain({ id }: { id: string }) {
  const { t } = useTranslation('compliance')
  const { activeTenant } = useAuth()

  const q = useQuery({
    queryKey: complianceKeys.erasureEvents(activeTenant, id),
    queryFn: () => complianceApi.erasureEvents(id),
  })

  const eventos = ((q.data as { items?: unknown[] })?.items ?? []) as Array<{
    id: string
    event: string
    actor: string
    actor_kind?: string
    note?: string
    approvers?: string[]
    ledger_seq?: number
    ledger_hash?: string
    occurred_at?: string
  }>

  if (q.isPending) return <Skeleton className="h-16 w-full" />
  if (eventos.length === 0)
    return (
      <p className="text-xs text-muted-foreground">{t('erasure.noCustody')}</p>
    )

  return (
    <ol className="flex flex-col gap-1 border-t border-border pt-2">
      {eventos.map((e) => (
        <li key={e.id} className="flex flex-wrap items-center gap-2 text-xs">
          <Badge variant="outline">
            {t(`erasure.event.${e.event}`, { defaultValue: e.event })}
          </Badge>
          <span className="font-mono">{e.actor}</span>
          {/* El quórum, nombrado. */}
          {(e.approvers ?? []).length > 0 ? (
            <span className="text-muted-foreground">
              {t('erasure.approvedBy', {
                list: (e.approvers ?? []).join(', '),
              })}
            </span>
          ) : null}
          {/* ⛔ Anclado ≠ sin anclar, y se dice en la fila. */}
          {e.ledger_hash ? (
            <span className="font-mono text-muted-foreground">
              {t('erasure.anchored', {
                seq: e.ledger_seq,
                hash: e.ledger_hash.slice(0, 12),
              })}
            </span>
          ) : (
            <Badge variant="warning">{t('erasure.unanchored')}</Badge>
          )}
        </li>
      ))}
    </ol>
  )
}

// --- register a DSAR ---------------------------------------------------------

function RegisterErasureDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [subjectKind, setSubjectKind] = useState('user')
  const [subjectRef, setSubjectRef] = useState('')
  const [caseRef, setCaseRef] = useState('')
  const [reason, setReason] = useState('')
  const [aliases, setAliases] = useState('')

  const create = useMutation({
    mutationFn: () =>
      complianceApi.createErasure({
        subject_kind: subjectKind.trim(),
        subject_ref: subjectRef.trim(),
        case_ref: caseRef.trim(),
        reason: reason.trim() || undefined,
        aliases: aliases
          .split(',')
          .map((a) => a.trim())
          .filter(Boolean),
      }),
    onSuccess: (res) => {
      // Same rule: a 2xx does not prove the request was registered.
      if (confirmedCreate(res, 'received')) {
        toast.success(t('erasure.dialog.registered'))
      } else {
        toast.warning(t('erasure.dialog.registerUnconfirmed'))
      }
      void qc.invalidateQueries({
        queryKey: complianceKeys.erasures(activeTenant),
      })
      onOpenChange(false)
      setSubjectRef('')
      setCaseRef('')
      setReason('')
      setAliases('')
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  const valid =
    subjectKind.trim() !== '' &&
    subjectRef.trim() !== '' &&
    caseRef.trim() !== ''

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('erasure.dialog.registerTitle')}</DialogTitle>
          <DialogDescription>
            {t('erasure.dialog.registerDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            create.mutate()
          }}
        >
          {/* Registering is NOT destructive — it mints the subject key and seals
              the "received" custody event. Saying so keeps the operator from
              hesitating over the safe half of the workflow. */}
          <CaveatNotice tone="info">
            {t('erasure.dialog.registerSafe')}
          </CaveatNotice>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t('erasure.dialog.subjectKind')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={subjectKind}
                  onChange={(e) => setSubjectKind(e.target.value)}
                  required
                />
              )}
            </Field>
            <Field label={t('erasure.dialog.subjectRef')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={subjectRef}
                  onChange={(e) => setSubjectRef(e.target.value)}
                  required
                />
              )}
            </Field>
          </div>
          <Field
            label={t('erasure.dialog.caseRef')}
            description={t('erasure.dialog.caseHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={caseRef}
                onChange={(e) => setCaseRef(e.target.value)}
                required
              />
            )}
          </Field>
          <Field
            label={t('erasure.dialog.aliases')}
            description={t('erasure.dialog.aliasesHint')}
          >
            {({ id }) => (
              <Input
                id={id}
                value={aliases}
                onChange={(e) => setAliases(e.target.value)}
              />
            )}
          </Field>
          <Field label={t('erasure.dialog.reason')}>
            {({ id }) => (
              <Textarea
                id={id}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                rows={2}
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
              {t('erasure.dialog.confirmRegister')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- execute: the irreversible verb ------------------------------------------

function ExecuteErasureDialog({
  req,
  open,
  onOpenChange,
}: {
  req: ErasureRequest
  open: boolean
  onOpenChange: (v: boolean) => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  const [pending, setPending] = useState<{
    reason: IncompleteReason
    approvalRef?: string
  } | null>(null)
  /** A 423 the engine raised. There are TWO, and they are not the same event:
   *  `legal_hold` carries the covering holds (erasure.go:675), `rtbf_coordinator`
   *  carries blockers/warnings from the enterprise readiness check
   *  (erasure.go:696). Classifying both as "blocked by a legal hold" tells the
   *  operator to go release a hold that does not exist. */
  const [blocked, setBlocked] = useState<BlockedInfo | null>(null)

  const execute = useMutation({
    mutationFn: () =>
      complianceApi.executeErasure(req.id, {
        reason: reason.trim() || undefined,
      }),
    onSuccess: (res) => {
      void qc.invalidateQueries({
        queryKey: complianceKeys.erasures(activeTenant),
      })
      const incomplete = incompleteReason(res)
      if (incomplete !== null) {
        setPending({ reason: incomplete, approvalRef: approvalRefOf(res) })
        return
      }
      // A 200 can still be a TERMINAL-WITH-GAPS outcome (erasure.go:1346): the
      // account leg was not attempted, the provider is not wired, or the ledger
      // re-verification failed. Reporting that as a clean success is how a DPO
      // closes a DSAR that is not finished.
      //
      // That status is NOT in this response. The 200 body is the receipt
      // (erasure.go:1416) and erasureReceiptDTO has no status field at all
      // (erasure.go:163-181) — a previous version read body.status here, which
      // never matched. Read back the request the engine persisted instead;
      // re-deriving the gap rule client-side would be a second implementation of
      // a compliance decision. If the read-back fails, warn: "could not check"
      // is not "clean".
      const receipt = res.data as { erasure_id?: unknown } | null
      const id =
        typeof receipt?.erasure_id === 'string' ? receipt.erasure_id : req.id
      complianceApi
        .erasure(id)
        .then((persisted) => {
          // ALLOWLIST: only `completed` is a clean erasure. Everything else —
          // completed_with_gaps, a state this build does not know, or a body
          // with no status at all — warns. Written as a denylist, a response
          // without `status` produced a success toast.
          if (persisted.status === 'completed') {
            toast.success(t('erasure.dialog.executed'))
          } else if (persisted.status === 'completed_with_gaps') {
            toast.warning(t('erasure.dialog.executedWithGaps'))
          } else {
            toast.warning(t('erasure.dialog.executedUnconfirmed'))
          }
        })
        .catch(() => toast.warning(t('erasure.dialog.executedUnconfirmed')))
        .finally(() => onOpenChange(false))
    },
    onError: (e: unknown) => {
      // A veto is a legitimate, expected answer — not a generic failure.
      if (e instanceof ApiError && e.status === 423) {
        setBlocked(classifyBlocked(e))
        return
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  if (blocked) {
    return (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            {/* The title must not name a cause the response did not give. An
                unknown code previously fell through to "Blocked by a legal
                hold", asserting the one thing this dialog cannot know. */}
            <DialogTitle>
              {blocked.kind === 'rtbf_coordinator'
                ? t('erasure.dialog.coordinatorTitle')
                : blocked.kind === 'legal_hold'
                  ? t('erasure.dialog.blockedTitle')
                  : t('erasure.dialog.refusedTitle')}
            </DialogTitle>
            <DialogDescription>
              {t('erasure.dialog.blockedDescription')}
            </DialogDescription>
          </DialogHeader>
          <CaveatNotice tone="warning" className="flex items-start gap-2">
            <Gavel className="mt-0.5 size-4 shrink-0" />
            <span>
              {blocked.kind === 'rtbf_coordinator'
                ? t('erasure.dialog.coordinatorExplain')
                : blocked.kind === 'legal_hold'
                  ? t('erasure.dialog.blockedExplain')
                  : blocked.message}
            </span>
          </CaveatNotice>
          {blocked.kind === 'legal_hold' && blocked.holds.length > 0 ? (
            <ul className="flex flex-col gap-2">
              {blocked.holds.map((h) => (
                <li
                  key={h.id}
                  className="rounded-md border border-border p-2 text-xs"
                >
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="warning">
                      {t(`holds.scope.${h.scope_kind}`, {
                        defaultValue: h.scope_kind,
                      })}
                    </Badge>
                    <span className="font-medium">
                      {h.matter_ref || t('erasure.dialog.unnamedMatter')}
                    </span>
                  </div>
                  <HashChip hash={h.id} label={t('erasure.dialog.holdId')} />
                </li>
              ))}
            </ul>
          ) : null}
          {blocked.kind === 'legal_hold' && blocked.malformed ? (
            <CaveatNotice tone="warning">
              {t('erasure.dialog.holdsPayloadIncomplete')}
            </CaveatNotice>
          ) : null}
          {blocked.kind === 'rtbf_coordinator' ? (
            <div className="flex flex-col gap-2 text-xs">
              {blocked.blockers.length > 0 ? (
                <div>
                  <p className="mb-1 font-medium">
                    {t('erasure.dialog.blockers')}
                  </p>
                  <ul className="list-disc pl-4">
                    {blocked.blockers.map((b, i) => (
                      <li key={i}>{b}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
              {blocked.warnings.length > 0 ? (
                <div>
                  <p className="mb-1 font-medium">
                    {t('erasure.dialog.warnings')}
                  </p>
                  <ul className="list-disc pl-4">
                    {blocked.warnings.map((w, i) => (
                      <li key={i}>{w}</li>
                    ))}
                  </ul>
                </div>
              ) : null}
            </div>
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

  if (pending) {
    return (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('erasure.dialog.pendingTitle')}</DialogTitle>
            <DialogDescription>
              {t('erasure.dialog.pendingDescription')}
            </DialogDescription>
          </DialogHeader>
          {/* The two 202s are NOT the same news. pending_approval means nothing
              was destroyed; provider_pending means part of the erasure already
              happened and the provider still owes deletions. Saying "nothing has
              been erased" for the second would be false. */}
          <CaveatNotice tone="warning">
            {pending.reason === 'provider_pending'
              ? t('erasure.dialog.providerPending')
              : pending.reason === 'unknown'
                ? t('erasure.dialog.incompleteUnknown')
                : t('erasure.dialog.nothingErasedYet')}
          </CaveatNotice>
          {pending.approvalRef ? (
            <HashChip
              hash={pending.approvalRef}
              label={t('erasure.approvalRef')}
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
      confirmPhrase={ERASE_PHRASE}
      pending={execute.isPending}
      title={t('erasure.dialog.executeTitle', { case: req.case_ref })}
      description={t('erasure.dialog.executeDescription')}
      confirmLabel={t('erasure.dialog.confirmExecute')}
      onConfirm={() => execute.mutate()}
    >
      <div className="flex flex-col gap-3">
        <CaveatNotice tone="warning" className="flex items-start gap-2">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" />
          <span>{t('erasure.dialog.irreversible')}</span>
        </CaveatNotice>
        <div className="rounded-md border border-border p-2 text-xs">
          <p>
            {t('erasure.subject')}:{' '}
            <span className="font-medium">
              {req.subject ?? req.subject_token}
            </span>
          </p>
          <p className="text-muted-foreground">
            {t('erasure.classes', {
              list: req.data_classes.join(', ') || '—',
            })}
          </p>
        </div>
        <CaveatNotice tone="info">{t('erasure.dialog.twoGates')}</CaveatNotice>
        <Field label={t('erasure.dialog.executeReason')}>
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

// --- the receipt -------------------------------------------------------------

function ReceiptDialog({
  req,
  onClose,
}: {
  req: ErasureRequest
  onClose: () => void
}) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const receiptQ = useQuery({
    queryKey: complianceKeys.erasureReceipt(activeTenant, req.id),
    queryFn: () => complianceApi.erasureReceipt(req.id),
  })

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {t('erasure.receiptTitle', { case: req.case_ref })}
          </DialogTitle>
          <DialogDescription>
            {t('erasure.receiptDescription')}
          </DialogDescription>
        </DialogHeader>
        <AsyncSection query={receiptQ} skeletonHeight={240}>
          {(r) => (
            <div className="flex flex-col gap-3 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant={r.key_shredded ? 'success' : 'warning'}>
                  {r.key_shredded
                    ? t('erasure.keyShredded')
                    : t('erasure.keyNotShredded')}
                </Badge>
                <Badge variant={r.verify_ok ? 'success' : 'danger'}>
                  {r.verify_ok
                    ? t('erasure.verifyOk', { n: r.verify_checked })
                    : t('erasure.verifyFailed')}
                </Badge>
              </div>
              {r.verify_reason ? (
                <CaveatNotice tone="warning">{r.verify_reason}</CaveatNotice>
              ) : null}

              <div className="flex flex-wrap items-center gap-3">
                <HashChip
                  hash={r.manifest_hash}
                  label={t('erasure.manifestHash')}
                />
                {r.ledger_hash ? (
                  <HashChip
                    hash={r.ledger_hash}
                    label={t('erasure.ledgerHash')}
                  />
                ) : null}
                <span className="text-muted-foreground">
                  {t('holds.ledgerSeq', { seq: r.ledger_seq })}
                </span>
              </div>

              {/* The two legs the engine reports separately, and the reason a
                  request ends completed_with_gaps: the account leg may not have
                  been attempted and the provider may not be wired at all
                  ("not_wired: …", erasure.go:1468). A receipt that showed only
                  what WAS deleted would read as a complete erasure. */}
              <div className="grid gap-2 sm:grid-cols-2">
                <div className="rounded border border-border p-1.5">
                  <p className="font-medium">{t('erasure.accountLeg')}</p>
                  <p className="text-muted-foreground">
                    {r.account_outcome || '—'}
                  </p>
                </div>
                <div className="rounded border border-border p-1.5">
                  <p className="font-medium">{t('erasure.providerLeg')}</p>
                  <p className="text-muted-foreground">
                    {r.provider_outcome || '—'}
                  </p>
                </div>
              </div>

              {r.targets.length > 0 ? (
                <div>
                  <p className="mb-1 font-medium">{t('erasure.targets')}</p>
                  <ul className="flex flex-col gap-1">
                    {r.targets.map((tg, i) => (
                      <li
                        key={i}
                        className="rounded border border-border p-1.5"
                      >
                        <span className="font-medium">
                          {tg.label ?? tg.target ?? '—'}
                        </span>
                        {typeof tg.rows === 'number' ? (
                          <span className="text-muted-foreground">
                            {' '}
                            · {t('erasure.rows', { n: tg.rows })}
                          </span>
                        ) : null}
                        {(tg.note ?? tg.reason) ? (
                          <p className="text-muted-foreground">
                            {tg.note ?? tg.reason}
                          </p>
                        ) : null}
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {/* What SURVIVES, and under which legal basis. Not an appendix — the
                  reconciliation is the part that makes the receipt defensible. */}
              {r.retained.length > 0 ? (
                <div>
                  <p className="mb-1 font-medium">{t('erasure.retained')}</p>
                  <ul className="flex flex-col gap-1">
                    {r.retained.map((rr, i) => (
                      <li
                        key={i}
                        className="rounded border border-border p-1.5"
                      >
                        <p className="font-medium">{rr.records}</p>
                        <p className="text-muted-foreground">{rr.basis}</p>
                      </li>
                    ))}
                  </ul>
                </div>
              ) : null}

              {/* Deleting our copy does not delete the provider's. */}
              {r.provider_floor_known ? (
                <CaveatNotice tone="warning">
                  {t('erasure.providerFloor', {
                    days: r.provider_floor_days ?? 0,
                    source: r.provider_floor_source ?? '',
                  })}
                </CaveatNotice>
              ) : null}

              <DisclaimerNote text={t('erasure.receiptDisclaimer')} />
            </div>
          )}
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

// --- per-subject lookup (the Art. 15/17 answer) ------------------------------

function DataSubjectLookup({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['compliance', 'common'])
  const { activeTenant } = useAuth()
  const [subjectId, setSubjectId] = useState('')
  const [subjectKind, setSubjectKind] = useState('user')
  const [query, setQuery] = useState<{ id: string; kind: string } | null>(null)

  const statusQ = useQuery({
    queryKey: complianceKeys.dataSubject(
      activeTenant,
      query?.id ?? '',
      query?.kind,
    ),
    queryFn: () =>
      complianceApi.dataSubjectErasureStatus(query!.id, query!.kind),
    enabled: query !== null,
  })

  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('erasure.lookupTitle')}</DialogTitle>
          <DialogDescription>
            {t('erasure.lookupDescription')}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            setQuery({ id: subjectId.trim(), kind: subjectKind.trim() })
          }}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            <Field label={t('erasure.dialog.subjectKind')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={subjectKind}
                  onChange={(e) => setSubjectKind(e.target.value)}
                />
              )}
            </Field>
            <Field label={t('erasure.dialog.subjectRef')}>
              {({ id }) => (
                <Input
                  id={id}
                  value={subjectId}
                  onChange={(e) => setSubjectId(e.target.value)}
                  required
                />
              )}
            </Field>
          </div>
          <Button type="submit" variant="secondary" size="sm">
            <UserSearch />
            {t('erasure.lookupSubmit')}
          </Button>
        </form>

        {query ? (
          <AsyncSection query={statusQ} skeletonHeight={160}>
            {(s) => (
              <div className="flex flex-col gap-2 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant={s.verified ? 'success' : 'neutral'}>
                    {t(`erasure.state.${s.state}`, { defaultValue: s.state })}
                  </Badge>
                  {s.key_shredded ? (
                    <Badge variant="success">{t('erasure.keyShredded')}</Badge>
                  ) : null}
                </div>
                {s.verify_reason ? (
                  <CaveatNotice tone="warning">{s.verify_reason}</CaveatNotice>
                ) : null}
                {s.approval_ref ? (
                  <HashChip
                    hash={s.approval_ref}
                    label={t('erasure.approvalRef')}
                  />
                ) : null}
                <DisclaimerNote text={s.disclaimer} />
              </div>
            )}
          </AsyncSection>
        ) : null}

        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            {t('common:actions.close')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
