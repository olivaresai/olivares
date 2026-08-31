// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Security (module IX) — the container. Tabs over findings / guardrails / anomalies /
// forensics. It wires the queries, the two privileged writes (triage a finding,
// change the enforcement posture — both RBAC-gated), and the guardrail inspect
// service, then composes the pure presentational pieces. The plane is DETECTIVE by
// default: the view never implies the system blocks on its own; a governance denial
// (403) on enabling enforcement surfaces the error message AS the reason, calmly.
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { ShieldAlert } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
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
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Badge } from '@/components/ui/badge'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import {
  ListTruncationBadge,
  AsyncSection,
  CaveatNotice,
  IntelNotice,
  IntelPage,
  SectionCard,
  SelfAuditNotice,
} from '@/features/_intel'
import { formatDateTime } from '@/lib/format'
import { CaseActions, CaseLinksPanel, NewCaseButton } from './case-ops'
import { securityApi, securityKeys } from './api'
import {
  AnomalyList,
  CaseIntegrityPanel,
  EnforcementTable,
  FindingsTable,
  ForensicTimeline,
  GuardrailVerdict,
  IntegrityPanel,
  SafetyPostureSurfaces,
} from './components'
import type {
  EnforcementEntry,
  Finding,
  FindingsExportResult,
  FindingStatus,
  ForensicCase,
  GuardrailSurface,
} from './types'
import './i18n'

const FINDING_STATUSES: FindingStatus[] = [
  'open',
  'triaged',
  'resolved',
  'dismissed',
]
const SURFACES: GuardrailSurface[] = ['input', 'output', 'tool_args']
const SEVERITIES = ['low', 'medium', 'high', 'critical']

// El techo real del motor (`maxLimit` en sqlstore/generic.go). Se pide el máximo y se
// DECLARA el recorte; nunca se pagina desde estas pantallas.
const SECURITY_PAGE = 1000

export function SecurityView() {
  const { t } = useTranslation('security')
  const { can } = useAuth()

  const canTriage = can('security:finding:write')
  const canInspect = can('security:guardrail:write')
  const canEnforce = can('security:enforcement:admin')

  return (
    <IntelPage
      icon={ShieldAlert}
      title={t('title')}
      description={t('description')}
    >
      <Tabs defaultValue="findings">
        <TabsList>
          <TabsTrigger value="findings">{t('tabs.findings')}</TabsTrigger>
          <TabsTrigger value="safety">{t('tabs.safety')}</TabsTrigger>
          <TabsTrigger value="guardrails">{t('tabs.guardrails')}</TabsTrigger>
          <TabsTrigger value="anomalies">{t('tabs.anomalies')}</TabsTrigger>
          <TabsTrigger value="forensics">{t('tabs.forensics')}</TabsTrigger>
        </TabsList>

        <TabsContent value="findings" className="flex flex-col gap-4">
          <FindingsTab canTriage={canTriage} />
        </TabsContent>

        <TabsContent value="safety" className="flex flex-col gap-4">
          <SafetyPostureTab />
        </TabsContent>

        <TabsContent value="guardrails" className="flex flex-col gap-4">
          <GuardrailsTab canInspect={canInspect} canEnforce={canEnforce} />
        </TabsContent>

        <TabsContent value="anomalies" className="flex flex-col gap-4">
          <AnomaliesTab />
        </TabsContent>

        <TabsContent value="forensics" className="flex flex-col gap-4">
          <ForensicsTab />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- findings tab ------------------------------------------------------------

/** Trigger a browser download of the EXACT server bytes (no client recompute):
 *  a SARIF run the browser re-serialized is no longer the server's artifact. */
function downloadFindingsExport(res: FindingsExportResult) {
  const blob = new Blob([res.text], {
    type: res.content_type || 'application/json',
  })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = res.filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

function FindingsTab({ canTriage }: { canTriage: boolean }) {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()
  const [target, setTarget] = useState<Finding | null>(null)
  const [exporting, setExporting] = useState(false)

  // El techo real del motor es `maxLimit = 1000`; pedir más no trae más y no pedir nada
  // trae 100 en silencio. Esta feature YA sabe declarar lo parcial en otros dos sitios —el
  // export avisa con `res.truncated` y la postura de seguridad con `counts_partial`—; lo
  // que faltaba era decirlo en la lista, que es donde se lee «éstos son los hallazgos».
  const findingsQ = useQuery({
    queryKey: securityKeys.findings(activeTenant, { limit: SECURITY_PAGE }),
    queryFn: () => securityApi.findings({ limit: SECURITY_PAGE }),
  })

  const handleExport = () => {
    setExporting(true)
    securityApi
      .exportFindings()
      .then((res) => {
        downloadFindingsExport(res)
        // A capped export is still a valid SARIF run, but it is NOT the whole
        // picture — saying so is the point of the server's truncation header.
        if (res.truncated) {
          toast.warning(t('findings.export.truncated'))
        } else {
          toast.success(t('findings.export.done'))
        }
      })
      .catch((e: unknown) => toast.error(String((e as Error).message ?? e)))
      .finally(() => setExporting(false))
  }

  return (
    <SectionCard
      title={t('findings.title')}
      description={t('findings.description')}
      noPadding
      actions={
        <Button
          variant="outline"
          size="sm"
          onClick={handleExport}
          disabled={exporting || (findingsQ.data?.items.length ?? 0) === 0}
        >
          {exporting ? t('findings.export.busy') : t('findings.export.action')}
        </Button>
      }
    >
      <div className="p-4">
        {/* ⛔ EL RECORTE SE DICE ANTES DE LA TABLA. Una lista de hallazgos cortada en
            silencio no es una lista incompleta: es una pantalla que afirma «no hay más
            problemas», y eso es exactamente lo que alguien usa para dar por revisado un
            estate. Va fuera del `AsyncSection` de la tabla a propósito: si un refetch
            falla, el aviso no debe quedarse flotando sobre un error. */}
        <ListTruncationBadge
          query={findingsQ}
          label={t('findings.truncated', { n: SECURITY_PAGE })}
          hint={t('findings.truncatedHint')}
          className="mb-3"
        />
        <AsyncSection query={findingsQ} skeletonHeight={240}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState
                title={t('findings.empty')}
                description={t('findings.emptyHint')}
              />
            ) : (
              <FindingsTable
                findings={list.items}
                canTriage={canTriage}
                onTriage={(f) => setTarget(f)}
              />
            )
          }
        </AsyncSection>
      </div>
      {canTriage ? (
        <TriageDialog
          finding={target}
          onOpenChange={(open) => {
            if (!open) setTarget(null)
          }}
        />
      ) : null}
    </SectionCard>
  )
}

function TriageDialog({
  finding,
  onOpenChange,
}: {
  finding: Finding | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['security', 'common'])
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [status, setStatus] = useState<FindingStatus>('triaged')

  const triage = useMutation({
    mutationFn: () => securityApi.triageFinding(finding!.id, { status }),
    onSuccess: () => {
      toast.success(t('findings.triage.updated'))
      void qc.invalidateQueries({
        queryKey: securityKeys.findings(activeTenant),
      })
      onOpenChange(false)
    },
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <Dialog open={finding !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('findings.triage.title')}</DialogTitle>
          <DialogDescription>
            {t('findings.triage.description')}
          </DialogDescription>
        </DialogHeader>
        {finding ? (
          <div className="flex flex-col gap-3">
            <p className="text-sm text-foreground">{finding.title}</p>
            {/* The evidence is immutable — triage only moves the flow status. */}
            <CaveatNotice>{t('findings.triage.immutableNote')}</CaveatNotice>
            <Field label={t('findings.triage.statusLabel')}>
              <Select
                value={status}
                onValueChange={(v) => setStatus(v as FindingStatus)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {FINDING_STATUSES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {t(`findings.status.${s}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
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
            type="button"
            variant="primary"
            disabled={triage.isPending}
            onClick={() => triage.mutate()}
          >
            {t('findings.triage.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- safety posture tab (read-first provider AI-safety view) ------------

function SafetyPostureTab() {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()

  const postureQ = useQuery({
    queryKey: securityKeys.safetyPosture(activeTenant),
    queryFn: () => securityApi.safetyPosture(),
  })

  return (
    <SectionCard
      title={t('safetyPosture.title')}
      description={t('safetyPosture.description')}
      noPadding
    >
      <div className="flex flex-col gap-4 p-4">
        <AsyncSection query={postureQ} skeletonHeight={240}>
          {(data) =>
            data.items.length === 0 ? (
              <EmptyState
                title={t('safetyPosture.empty')}
                description={t('safetyPosture.emptyHint')}
              />
            ) : (
              <>
                {data.counts_partial ? (
                  <CaveatNotice>
                    {t('safetyPosture.countsPartial')}
                  </CaveatNotice>
                ) : null}
                <SafetyPostureSurfaces providers={data.providers} />
                <FindingsTable findings={data.items} />
              </>
            )
          }
        </AsyncSection>
      </div>
    </SectionCard>
  )
}

// --- guardrails tab (inspect service + enforcement posture) ------------------

function GuardrailsTab({
  canInspect,
  canEnforce,
}: {
  canInspect: boolean
  canEnforce: boolean
}) {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()
  const [editing, setEditing] = useState<EnforcementEntry | null>(null)

  const enforcementQ = useQuery({
    queryKey: securityKeys.enforcement(activeTenant),
    queryFn: () => securityApi.enforcement(),
  })

  const inspect = useMutation({
    mutationFn: (body: {
      surface: GuardrailSurface
      text: string
      enforce: boolean
    }) => securityApi.inspect(body),
    onError: (e: unknown) => toast.error(String((e as Error).message ?? e)),
  })

  return (
    <>
      {canInspect ? (
        <SectionCard
          title={t('guardrails.title')}
          description={t('guardrails.description')}
        >
          <InspectForm
            pending={inspect.isPending}
            onSubmit={(v) => inspect.mutate(v)}
          />
          {inspect.data ? (
            <div className="mt-4">
              <GuardrailVerdict result={inspect.data} />
            </div>
          ) : null}
        </SectionCard>
      ) : null}

      <SectionCard
        title={t('enforcement.title')}
        description={t('enforcement.description')}
      >
        <AsyncSection query={enforcementQ} skeletonHeight={200}>
          {(data) => (
            <EnforcementTable
              entries={data.items}
              canAdmin={canEnforce}
              onToggle={(e) => setEditing(e)}
            />
          )}
        </AsyncSection>
      </SectionCard>

      {canEnforce ? (
        <EnforcementDialog
          entry={editing}
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
        />
      ) : null}
    </>
  )
}

function InspectForm({
  pending,
  onSubmit,
}: {
  pending: boolean
  onSubmit: (v: {
    surface: GuardrailSurface
    text: string
    enforce: boolean
  }) => void
}) {
  const { t } = useTranslation('security')
  const [surface, setSurface] = useState<GuardrailSurface>('input')
  const [text, setText] = useState('')
  const [enforce, setEnforce] = useState(false)

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (text.trim()) onSubmit({ surface, text: text.trim(), enforce })
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label={t('guardrails.form.surface')}>
          <Select
            value={surface}
            onValueChange={(v) => setSurface(v as GuardrailSurface)}
          >
            <SelectTrigger aria-label={t('guardrails.form.surfaceLabel')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SURFACES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`guardrails.surface.${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <Field
        label={t('guardrails.form.text')}
        description={t('guardrails.form.textHint')}
      >
        {({ id }) => (
          <Input
            id={id}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
        )}
      </Field>
      <label className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
        <span className="flex flex-col">
          <span className="text-sm text-foreground">
            {t('guardrails.form.enforce')}
          </span>
          <span className="text-xs text-muted-foreground">
            {t('guardrails.form.enforceHint')}
          </span>
        </span>
        <Switch checked={enforce} onCheckedChange={setEnforce} />
      </label>
      <div>
        <Button
          type="submit"
          variant="primary"
          disabled={pending || !text.trim()}
        >
          {pending ? t('guardrails.form.running') : t('guardrails.form.submit')}
        </Button>
      </div>
    </form>
  )
}

function EnforcementDialog({
  entry,
  onOpenChange,
}: {
  entry: EnforcementEntry | null
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation(['security', 'common'])
  const report = useFailedActionReporter()
  const { t: tIntel } = useTranslation('intel')
  const { activeTenant } = useAuth()
  const qc = useQueryClient()
  const [enabled, setEnabled] = useState(false)
  const [minSeverity, setMinSeverity] = useState('high')
  const [reason, setReason] = useState('')
  // A governance denial (403) on enabling — surfaced AS the reason, not a toast.
  const [denied, setDenied] = useState<string | null>(null)

  const apply = useMutation({
    mutationFn: () =>
      securityApi.setEnforcement({
        class: entry!.class,
        enabled,
        min_severity: minSeverity,
        reason: reason.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('enforcement.dialog.applied'))
      void qc.invalidateQueries({
        queryKey: securityKeys.enforcement(activeTenant),
      })
      onOpenChange(false)
    },
    onError: (e: unknown) => {
      // 403 = governance denial. Show the engine's message as the reason, calmly.
      // ⛔ ASEGURAMIENTO ANTES QUE ROL. La rama de abajo NO se delega: enseña el mensaje del
      // MOTOR como razón de la denegación de gobernanza, y `report` pone una advertencia
      // genérica. Lo que faltaba es separar la negativa que tiene remedio —la ceremonia—
      // de la que no: `isForbidden` es sólo el status (lib/api/errors.ts:59).
      if (e instanceof ApiError && e.isStepUpRequired) {
        report(e)
        return
      }
      if (e instanceof ApiError && e.isForbidden) {
        setDenied(e.message)
        return
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  // Sync the form to the selected row when it changes — the React-recommended
  // "adjust state during render" pattern (a sentinel on the row's class) rather than
  // a setState-in-effect, which cascades renders.
  const open = entry !== null
  const [syncedClass, setSyncedClass] = useState<string | null>(null)
  if (entry && entry.class !== syncedClass) {
    setSyncedClass(entry.class)
    setEnabled(entry.enabled)
    setMinSeverity(entry.min_severity)
    setReason('')
    setDenied(null)
  }

  // Warn when enabling a class that has no wired governance gate (governed:false).
  const ungovernedRisk = enabled && entry !== null && !entry.governed

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('enforcement.dialog.title')}</DialogTitle>
          <DialogDescription>
            {t('enforcement.dialog.description')}
          </DialogDescription>
        </DialogHeader>
        {entry ? (
          <div className="flex flex-col gap-3">
            <p className="font-mono text-sm text-foreground">
              {entry.class === '*' ? t('enforcement.wildcard') : entry.class}
            </p>
            <label className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
              <span className="text-sm text-foreground">
                {t('enforcement.dialog.enabledLabel')}
              </span>
              <Switch checked={enabled} onCheckedChange={setEnabled} />
            </label>
            <Field label={t('enforcement.dialog.minSeverity')}>
              <Select value={minSeverity} onValueChange={setMinSeverity}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {SEVERITIES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {tIntel(`severity.${s}`, { defaultValue: s })}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field
              label={t('enforcement.dialog.reason')}
              description={t('enforcement.dialog.reasonHint')}
            >
              {({ id }) => (
                <Input
                  id={id}
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                />
              )}
            </Field>
            {ungovernedRisk ? (
              <IntelNotice tone="warning">
                {t('enforcement.dialog.ungovernedWarning')}
              </IntelNotice>
            ) : null}
            {denied ? (
              <IntelNotice tone="warning">
                <span className="font-medium text-warning">
                  {t('enforcement.dialog.deniedTitle')}
                </span>{' '}
                <span className="text-muted-foreground">{denied}</span>
              </IntelNotice>
            ) : null}
          </div>
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
            type="button"
            variant="primary"
            disabled={apply.isPending}
            onClick={() => apply.mutate()}
          >
            {t('enforcement.dialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- anomalies tab (privileged + self-audited) -------------------------------

function AnomaliesTab() {
  const { t } = useTranslation('security')
  const { activeTenant } = useAuth()
  const anomaliesQ = useQuery({
    queryKey: securityKeys.anomalies(activeTenant),
    queryFn: () => securityApi.anomalies(),
  })

  return (
    <SectionCard
      title={t('anomalies.title')}
      description={t('anomalies.description')}
    >
      {/* Querying the queue is a privileged read — say it is recorded. */}
      <SelfAuditNotice className="mb-3" />
      <AsyncSection query={anomaliesQ} skeletonHeight={220}>
        {(data) =>
          data.items.length === 0 ? (
            <EmptyState title={t('anomalies.empty')} />
          ) : (
            <AnomalyList anomalies={data.items} />
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}

// --- forensics tab (cases -> case detail with integrity + timeline) ----------

function ForensicsTab() {
  const { t } = useTranslation('security')
  const { activeTenant, can } = useAuth()
  // ⛔ Esta pestaña NO gateaba nada: se pintaba para todos y sus consultas contestaban 403 a
  // quien no tuviera `security:case:read`. Un 403 no es una pantalla vacía — es un error que
  // el operador no puede resolver y que ni siquiera le dice que le falta un permiso.
  const canReadCases = can('security:case:read')
  const canWriteCases = can('security:case:write')
  const [openCaseId, setOpenCaseId] = useState<string | null>(null)
  // Remember the case row that opened the detail view so focus can be restored
  // to it when the user navigates Back (the row was unmounted during the swap).
  const lastOpenedRef = useRef<string | null>(null)

  const casesQ = useQuery({
    queryKey: securityKeys.cases(activeTenant, { limit: SECURITY_PAGE }),
    queryFn: () => securityApi.cases(undefined, SECURITY_PAGE),
    enabled: canReadCases,
  })

  if (openCaseId) {
    return (
      <CaseDetail
        caseId={openCaseId}
        canWrite={canWriteCases}
        onBack={() => {
          lastOpenedRef.current = openCaseId
          setOpenCaseId(null)
        }}
      />
    )
  }

  return (
    <>
      <SectionCard
        title={t('integrity.title')}
        description={t('integrity.description')}
      >
        <IntegritySection />
      </SectionCard>

      <SectionCard
        title={t('forensics.title')}
        description={t('forensics.description')}
        actions={<NewCaseButton canWrite={canWriteCases} />}
      >
        <SelfAuditNotice className="mb-3" />
        {/* Un expediente forense que no cabe en la página no es un expediente que no
            exista, y esta lista es la que un investigador usa para decir «no hay más». */}
        <ListTruncationBadge
          query={casesQ}
          label={t('forensics.truncated', { n: SECURITY_PAGE })}
          hint={t('forensics.truncatedHint')}
          className="mb-3"
        />
        <AsyncSection query={casesQ} skeletonHeight={200}>
          {(list) =>
            list.items.length === 0 ? (
              <EmptyState title={t('forensics.empty')} />
            ) : (
              <div className="flex flex-col gap-2">
                {list.items.map((c) => (
                  <CaseRow
                    key={c.id}
                    forensicCase={c}
                    restoreFocus={lastOpenedRef.current === c.id}
                    onOpen={() => setOpenCaseId(c.id)}
                  />
                ))}
              </div>
            )
          }
        </AsyncSection>
      </SectionCard>
    </>
  )
}

function IntegritySection() {
  const { activeTenant } = useAuth()
  const integrityQ = useQuery({
    queryKey: securityKeys.integrity(activeTenant),
    queryFn: () => securityApi.integrity(),
  })
  return (
    <>
      {/* Verifying is itself recorded in the chain it checks. */}
      <SelfAuditNotice className="mb-3" />
      <AsyncSection query={integrityQ} skeletonHeight={160}>
        {(data) => <IntegrityPanel integrity={data} />}
      </AsyncSection>
    </>
  )
}

const CASE_STATUS_VARIANT: Record<
  ForensicCase['status'],
  'warning' | 'info' | 'success' | 'neutral'
> = {
  open: 'warning',
  investigating: 'info',
  contained: 'success',
  closed: 'neutral',
}

function CaseRow({
  forensicCase,
  restoreFocus = false,
  onOpen,
}: {
  forensicCase: ForensicCase
  /** When true, focus this row on mount — used to restore focus after Back. */
  restoreFocus?: boolean
  onOpen: () => void
}) {
  const { t, i18n } = useTranslation('security')
  const ref = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (restoreFocus) ref.current?.focus()
  }, [restoreFocus])
  return (
    <button
      ref={ref}
      type="button"
      onClick={onOpen}
      className="flex w-full items-center justify-between gap-3 rounded-lg border border-border bg-surface p-4 text-left transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:outline-none"
    >
      <div className="flex min-w-0 flex-col gap-1">
        <span className="truncate text-sm font-medium text-foreground">
          {forensicCase.title}
        </span>
        <span className="truncate text-xs text-muted-foreground">
          <span className="font-mono">
            {forensicCase.subject_kind}: {forensicCase.subject_ref}
          </span>
          {' · '}
          {formatDateTime(forensicCase.opened_at, i18n.language)}
        </span>
      </div>
      <Badge variant={CASE_STATUS_VARIANT[forensicCase.status] ?? 'neutral'}>
        {t(`forensics.status.${forensicCase.status}`, {
          defaultValue: forensicCase.status,
        })}
      </Badge>
    </button>
  )
}

function CaseDetail({
  caseId,
  canWrite,
  onBack,
}: {
  caseId: string
  canWrite: boolean
  onBack: () => void
}) {
  const { t, i18n } = useTranslation('security')
  const { activeTenant } = useAuth()
  // Move focus to the new view on mount — the activating row was unmounted, so
  // without this keyboard focus falls back to <body> and the swap is unannounced.
  // We focus the Back control's container (tabIndex=-1) rather than the Button
  // itself, since the shared Button primitive does not forward a ref.
  const headerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    headerRef.current?.focus()
  }, [])
  const timelineQ = useQuery({
    queryKey: securityKeys.caseTimeline(activeTenant, caseId),
    queryFn: () => securityApi.caseTimeline(caseId),
  })

  return (
    <>
      <div ref={headerRef} tabIndex={-1} className="outline-none">
        <Button variant="ghost" size="sm" onClick={onBack}>
          {t('forensics.back')}
        </Button>
      </div>
      {/* The case timeline is a privileged, self-audited read. */}
      <SelfAuditNotice />
      <AsyncSection query={timelineQ} skeletonHeight={320}>
        {(data) => (
          <>
            <SectionCard
              title={data.case.title}
              description={data.case.summary}
            >
              <div className="mb-4 flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
                <span>
                  {t('forensics.openedBy')}:{' '}
                  <span className="font-mono text-foreground">
                    {data.case.opened_by}
                  </span>
                </span>
                <span>
                  {t('forensics.opened')}:{' '}
                  {formatDateTime(data.case.opened_at, i18n.language)}
                </span>
                {data.case.closed_at ? (
                  <span>
                    {t('forensics.closed')}:{' '}
                    {formatDateTime(data.case.closed_at, i18n.language)}
                  </span>
                ) : null}
              </div>
              <CaseIntegrityPanel integrity={data.integrity} />
              {/* Operar el caso: estado, severidad y exportación. El PATCH manda SÓLO lo
                  que cambia — el motor lee punteros y un campo ausente se conserva. */}
              <div className="mt-4 border-t border-border pt-4">
                <CaseActions forensicCase={data.case} canWrite={canWrite} />
              </div>
            </SectionCard>
            <SectionCard
              title={t('cases.links')}
              description={t('cases.linksHint')}
            >
              <CaseLinksPanel caseId={caseId} canWrite={canWrite} />
            </SectionCard>
            <ForensicTimeline events={data.events} />
          </>
        )}
      </AsyncSection>
    </>
  )
}
