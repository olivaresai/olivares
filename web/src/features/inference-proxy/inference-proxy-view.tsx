// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// inference-proxy administration. The engine's per-tenant proxy gates, egress
// DLP rules and device-authorization approval were API-only; this surfaces all three
// with the console quality bar — reads mirror the viewer tier, writes gate on the
// editor/admin tier AND an AAL3 step-up, and every mutation is self-audited server-
// side. There is deliberately NO "test against sample text" for DLP: the engine
// exposes no such route, and the console never fabricates one — classification runs
// server-side at egress, which the section states honestly.
import './i18n'
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Waypoints, Plus, Trash2, Pencil } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/ui/page-header'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { toast } from '@/components/ui/toaster'
import { AAL, StepUpPanel, useAssurance } from '@/features/identity/assurance'
import { ListTruncationBadge } from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import {
  useFailedActionReporter,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import {
  inferenceProxyApi,
  inferenceProxyKeys,
  type DLPRule,
  type ProxyConfig,
  type ResponseDLPMode,
} from './api'

const GATE_KEYS = [
  'gate_model_access',
  'gate_budget',
  'gate_residency',
  'gate_context_window',
  'gate_dlp_request',
  'gate_dlp_response',
] as const
const DLP_MODES: ResponseDLPMode[] = ['off', 'flag', 'buffer']
const MIN_TASK_BUDGET = 20_000

/**
 * Fill the pointer-defaulted fields so a save never flips a gate by omission.
 *
 * `record_mandatory` defaults TRUE here because that is what the server applies to a
 * tenant with no row. It read `?? false` — the opposite polarity — so a response that
 * ever omitted the field would have shown the operator "recording off" for a tenant
 * whose recording is mandatory, and then written that back.
 */
function normalizeConfig(c: Partial<ProxyConfig>): ProxyConfig {
  return {
    fail_open: c.fail_open ?? false,
    response_dlp_mode: c.response_dlp_mode ?? 'off',
    record_mandatory: c.record_mandatory ?? true,
    gate_model_access: c.gate_model_access ?? true,
    gate_budget: c.gate_budget ?? false,
    gate_residency: c.gate_residency ?? false,
    gate_context_window: c.gate_context_window ?? false,
    gate_dlp_request: c.gate_dlp_request ?? false,
    gate_dlp_response: c.gate_dlp_response ?? false,
    ceilings_enforce: c.ceilings_enforce ?? false,
    ceiling_max_tokens: c.ceiling_max_tokens ?? 0,
    ceiling_max_tool_uses: c.ceiling_max_tool_uses ?? 0,
    ceiling_task_budget_tokens: c.ceiling_task_budget_tokens ?? 0,
  }
}

export function InferenceProxyView() {
  const { t } = useTranslation(['inferenceProxy', 'common'])
  const { can } = useAuth()
  if (!can('inferenceproxy:config:read')) return <ForbiddenState />
  return (
    <div className="flex flex-col gap-6 p-6">
      <PageHeader
        icon={Waypoints}
        title={t('title')}
        description={t('description')}
      />
      <ConfigSection />
      <DLPSection />
      <DeviceSection />
    </div>
  )
}

// --- config gates + ceilings -------------------------------------------------

function ConfigSection() {
  const { t } = useTranslation(['inferenceProxy', 'common'])
  const { can, activeTenant } = useAuth()
  const { aal } = useAssurance()
  const canWrite = can('inferenceproxy:config:admin')
  const stepUpNeeded = canWrite && aal < AAL.HARDWARE

  const configQ = useQuery({
    queryKey: inferenceProxyKeys.config(activeTenant),
    queryFn: () => inferenceProxyApi.getConfig(),
  })
  const [draft, setDraft] = useState<ProxyConfig | null>(null)
  const effective = draft ?? configQ.data ?? null
  const dirty = draft != null

  // Did the operator touch the evidence switch in THIS editing session? The server
  // reads the mere presence of `record_mandatory` as an explicit choice, so echoing
  // the value we just read would record a decision nobody made — and a tenant marked
  // as having chosen no longer yields to the audit spool's declared `degrade`. Every
  // other field is safe to re-send; this one is a claim about intent.
  const [mandatoryTouched, setMandatoryTouched] = useState(false)

  // Both pieces of edit state are DISCARDED TOGETHER, always. They were separate, and the
  // gap between them was reachable two ways. Reset cleared the draft and left the intent
  // flag set, so the next save recorded an evidence choice that Reset was pressed to
  // cancel. And neither was tied to the tenant, while the HTTP client stamps the header
  // from whatever tenant is active when the request is built — so an unsaved edit made in
  // one tenant would be PUT, whole, against another.
  const discard = () => {
    setDraft(null)
    setMandatoryTouched(false)
  }

  // Switching tenants abandons the edit. Carrying a draft across is not a convenience: the
  // form is bound to a config that no longer belongs to what is on screen.
  useEffect(discard, [activeTenant])

  const save = usePrivilegedMutation({
    mutationFn: () => {
      const body = normalizeConfig(effective!)
      if (!mandatoryTouched) delete body.record_mandatory
      return inferenceProxyApi.putConfig(body)
    },
    invalidateKeys: [inferenceProxyKeys.config(activeTenant)],
    successMessage: t('config.saved'),
    onDone: discard,
  })

  const patch = (p: Partial<ProxyConfig>) => {
    if ('record_mandatory' in p) setMandatoryTouched(true)
    setDraft({ ...normalizeConfig(effective ?? {}), ...p })
  }

  const taskBudget = effective?.ceiling_task_budget_tokens ?? 0
  const taskBudgetInvalid = taskBudget !== 0 && taskBudget < MIN_TASK_BUDGET
  const ceilingsOn = effective?.ceilings_enforce ?? false

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('config.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-sm text-muted-foreground">{t('config.intro')}</p>
        {configQ.isLoading ? (
          <div role="status" className="flex justify-center py-6">
            <span className="sr-only">{t('common:states.loading')}</span>
            <Spinner />
          </div>
        ) : configQ.isError ? (
          <ErrorState retry={() => void configQ.refetch()} />
        ) : effective ? (
          <>
            <div className="flex flex-col divide-y">
              <SwitchRow
                label={t('config.failOpen')}
                hint={t('config.failOpenHint')}
                checked={effective.fail_open ?? false}
                disabled={!canWrite}
                onChange={(v) => patch({ fail_open: v })}
              />
              <SwitchRow
                label={t('config.recordMandatory')}
                hint={t('config.recordMandatoryHint')}
                checked={effective.record_mandatory ?? true}
                disabled={!canWrite}
                onChange={(v) => patch({ record_mandatory: v })}
              />
              {GATE_KEYS.map((k) => (
                <SwitchRow
                  key={k}
                  label={t(`config.gates.${k}`)}
                  hint={t(`config.gatesHint.${k}`)}
                  checked={effective[k] ?? k === 'gate_model_access'}
                  disabled={!canWrite}
                  onChange={(v) => patch({ [k]: v })}
                />
              ))}
            </div>

            <Field
              label={t('config.responseDlpMode')}
              description={t('config.responseDlpModeHint')}
            >
              <Select
                value={effective.response_dlp_mode ?? 'off'}
                onValueChange={(v) =>
                  patch({ response_dlp_mode: v as ResponseDLPMode })
                }
                disabled={!canWrite}
              >
                <SelectTrigger aria-label={t('config.responseDlpMode')}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {DLP_MODES.map((m) => (
                    <SelectItem key={m} value={m}>
                      {t(`config.dlpModes.${m}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <div className="flex flex-col gap-3 rounded-md border p-3">
              <SwitchRow
                label={t('config.ceilingsEnforce')}
                hint={t('config.ceilingsEnforceHint')}
                checked={ceilingsOn}
                disabled={!canWrite}
                onChange={(v) => patch({ ceilings_enforce: v })}
              />
              {ceilingsOn ? (
                <div className="grid gap-3 sm:grid-cols-3">
                  <Field label={t('config.ceilingMaxTokens')}>
                    {({ id }) => (
                      <Input
                        id={id}
                        type="number"
                        min="0"
                        value={effective.ceiling_max_tokens ?? 0}
                        disabled={!canWrite}
                        onChange={(e) =>
                          patch({ ceiling_max_tokens: Number(e.target.value) })
                        }
                      />
                    )}
                  </Field>
                  <Field label={t('config.ceilingMaxToolUses')}>
                    {({ id }) => (
                      <Input
                        id={id}
                        type="number"
                        min="0"
                        value={effective.ceiling_max_tool_uses ?? 0}
                        disabled={!canWrite}
                        onChange={(e) =>
                          patch({
                            ceiling_max_tool_uses: Number(e.target.value),
                          })
                        }
                      />
                    )}
                  </Field>
                  <Field
                    label={t('config.ceilingTaskBudget')}
                    description={t('config.ceilingTaskBudgetHint')}
                    error={
                      taskBudgetInvalid
                        ? t('config.ceilingTaskBudgetError', {
                            min: MIN_TASK_BUDGET,
                          })
                        : undefined
                    }
                  >
                    {({ id }) => (
                      <Input
                        id={id}
                        type="number"
                        min="0"
                        value={taskBudget}
                        disabled={!canWrite}
                        onChange={(e) =>
                          patch({
                            ceiling_task_budget_tokens: Number(e.target.value),
                          })
                        }
                      />
                    )}
                  </Field>
                </div>
              ) : null}
            </div>

            {stepUpNeeded ? (
              <StepUpPanel
                minAal={AAL.HARDWARE}
                currentAal={aal}
                action="proxy"
              />
            ) : canWrite ? (
              <div className="flex justify-end gap-2">
                <Button
                  variant="ghost"
                  disabled={!dirty || save.isPending}
                  onClick={discard}
                >
                  {t('config.reset')}
                </Button>
                <Button
                  variant="primary"
                  disabled={!dirty || taskBudgetInvalid || save.isPending}
                  onClick={() => save.mutate()}
                >
                  {save.isPending && <Spinner size="sm" aria-hidden />}
                  {t('config.save')}
                </Button>
              </div>
            ) : null}
          </>
        ) : null}
      </CardContent>
    </Card>
  )
}

function SwitchRow({
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  label: string
  hint: string
  checked: boolean
  disabled?: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div className="flex items-start justify-between gap-4 py-2">
      <span className="min-w-0">
        <span className="text-sm font-medium text-foreground">{label}</span>
        <span className="block text-xs text-muted-foreground">{hint}</span>
      </span>
      <Switch
        checked={checked}
        disabled={disabled}
        onCheckedChange={onChange}
        aria-label={label}
      />
    </div>
  )
}

// --- egress DLP rules --------------------------------------------------------

function DLPSection() {
  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()
  const { t } = useTranslation(['inferenceProxy', 'common', 'intel'])
  const { can, activeTenant } = useAuth()
  const { aal } = useAssurance()
  const canRead = can('inferenceproxy:dlp:read')
  const canAdmin = can('inferenceproxy:dlp:admin')
  const stepUpNeeded = canAdmin && aal < AAL.HARDWARE
  const canEdit = canAdmin && aal >= AAL.HARDWARE
  const qc = useQueryClient()

  const [editing, setEditing] = useState<DLPRule | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<DLPRule | null>(null)

  const rulesQ = useQuery({
    queryKey: inferenceProxyKeys.dlpRules(activeTenant),
    queryFn: () => inferenceProxyApi.listDLPRules(),
    enabled: canRead,
  })

  const del = useMutation({
    mutationFn: (id: string) => inferenceProxyApi.deleteDLPRule(id),
    onSuccess: () => {
      toast.success(t('dlp.deleted'))
      void qc.invalidateQueries({
        queryKey: inferenceProxyKeys.dlpRules(activeTenant),
      })
      setDeleting(null)
    },
    onError: (e: unknown) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida. Defensa en
      // profundidad: esta ruta no está en ninguna de las cuatro familias de emisores medidas.
      if (e instanceof ApiError && e.isStepUpRequired) {
        report(e)
        return
      }
      if (e instanceof ApiError && e.isForbidden) {
        toast.warning(t('common:privileged.notAuthorizedToast'))
        setDeleting(null)
        return
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2">
        <CardTitle>{t('dlp.title')}</CardTitle>
        {canEdit ? (
          <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
            <Plus />
            {t('dlp.add')}
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-sm text-muted-foreground">{t('dlp.intro')}</p>
        {/* El aviso va con la CUENTA CARGADA, no con el techo: «se muestran 1000» le dice al
            operador lo que tiene delante; «el techo es 1000» le dice un numero de configuracion
            que no le sirve para decidir si mirar mas. */}
        <ListTruncationBadge
          query={rulesQ}
          label={t('dlp.truncated', { n: rulesQ.data?.items?.length ?? 0 })}
          hint={t('dlp.truncatedHint')}
        />
        {!canRead ? (
          <EmptyState title={t('dlp.noRead')} />
        ) : rulesQ.isLoading ? (
          <div role="status" className="flex justify-center py-6">
            <span className="sr-only">{t('common:states.loading')}</span>
            <Spinner />
          </div>
        ) : rulesQ.isError ? (
          <ErrorState retry={() => void rulesQ.refetch()} />
        ) : (rulesQ.data?.items.length ?? 0) === 0 ? (
          <EmptyState title={t('dlp.empty')} description={t('dlp.emptyHint')} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                  <th className="py-2 pr-4 font-medium">{t('dlp.colClass')}</th>
                  <th className="py-2 pr-4 font-medium">
                    {t('dlp.colAction')}
                  </th>
                  <th className="py-2 pr-4 font-medium">{t('dlp.colNote')}</th>
                  <th className="py-2 pl-4 text-right font-medium">
                    <span className="sr-only">{t('dlp.colActions')}</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rulesQ.data?.items.map((r) => (
                  <tr key={r.id ?? r.class} className="border-b last:border-0">
                    <td className="py-2 pr-4 font-mono">{r.class}</td>
                    <td className="py-2 pr-4">
                      <Badge
                        variant={r.action === 'deny' ? 'danger' : 'success'}
                      >
                        {t(`dlp.actions.${r.action}`)}
                      </Badge>
                    </td>
                    <td className="py-2 pr-4 text-muted-foreground">
                      {r.note ?? ''}
                    </td>
                    <td className="py-2 pl-4 text-right">
                      {canEdit ? (
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t('dlp.editAction', { class: r.class })}
                            onClick={() => setEditing(r)}
                          >
                            <Pencil />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t('dlp.deleteAction', {
                              class: r.class,
                            })}
                            onClick={() => setDeleting(r)}
                          >
                            <Trash2 />
                          </Button>
                        </div>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {stepUpNeeded ? (
          <StepUpPanel minAal={AAL.HARDWARE} currentAal={aal} action="proxy" />
        ) : null}
      </CardContent>

      {creating ? <DLPRuleDialog onClose={() => setCreating(false)} /> : null}
      {editing ? (
        <DLPRuleDialog
          key={editing.id ?? editing.class}
          rule={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
      <Dialog
        open={deleting != null}
        onOpenChange={(v) => {
          if (!v) setDeleting(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('dlp.deleteDialog.title')}</DialogTitle>
            <DialogDescription>
              {t('dlp.deleteDialog.description', {
                class: deleting?.class ?? '',
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="secondary"
              onClick={() => setDeleting(null)}
              disabled={del.isPending}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button
              variant="destructive-solid"
              onClick={() => deleting?.id && del.mutate(deleting.id)}
              disabled={del.isPending}
            >
              {t('dlp.deleteDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

function DLPRuleDialog({
  rule,
  onClose,
}: {
  rule?: DLPRule
  onClose: () => void
}) {
  const { t } = useTranslation(['inferenceProxy', 'common'])
  const { activeTenant } = useAuth()
  const isEdit = rule != null
  const [cls, setCls] = useState(rule?.class ?? '')
  const [action, setAction] = useState<'allow' | 'deny'>(rule?.action ?? 'deny')
  const [note, setNote] = useState(rule?.note ?? '')

  const save = usePrivilegedMutation({
    mutationFn: () =>
      inferenceProxyApi.putDLPRule({
        class: cls.trim(),
        action,
        note: note.trim() || undefined,
      }),
    invalidateKeys: [inferenceProxyKeys.dlpRules(activeTenant)],
    successMessage: isEdit ? t('dlp.updated') : t('dlp.created'),
    onDone: onClose,
  })

  const valid = cls.trim().length > 0

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o && !save.isPending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isEdit ? t('dlp.dialog.editTitle') : t('dlp.dialog.title')}
          </DialogTitle>
          <DialogDescription>{t('dlp.dialog.subtitle')}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) save.mutate()
          }}
        >
          <Field
            label={t('dlp.dialog.class')}
            description={t('dlp.dialog.classHint')}
            required
          >
            {({ id }) => (
              <Input
                id={id}
                value={cls}
                mono
                // The class keys the rule; editing an existing rule keeps its class
                // (upsert-by-class) so it edits in place rather than forking a rule.
                disabled={isEdit}
                onChange={(e) => setCls(e.target.value)}
              />
            )}
          </Field>
          <Field label={t('dlp.dialog.action')}>
            <Select
              value={action}
              onValueChange={(v) => setAction(v as 'allow' | 'deny')}
            >
              <SelectTrigger aria-label={t('dlp.dialog.action')}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="deny">{t('dlp.actions.deny')}</SelectItem>
                <SelectItem value="allow">{t('dlp.actions.allow')}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field label={t('dlp.dialog.note')}>
            {({ id }) => (
              <Textarea
                id={id}
                value={note}
                rows={2}
                onChange={(e) => setNote(e.target.value)}
              />
            )}
          </Field>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button
              type="submit"
              variant="primary"
              disabled={!valid || save.isPending}
            >
              {save.isPending && <Spinner size="sm" aria-hidden />}
              {isEdit ? t('dlp.dialog.save') : t('dlp.dialog.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// --- device authorization approval -------------------------------------------

function DeviceSection() {
  // El reporte vive en un solo sitio (use-privileged-mutation.ts:25-32).
  const report = useFailedActionReporter()
  const { t } = useTranslation(['inferenceProxy', 'common'])
  const { can } = useAuth()
  const { aal } = useAssurance()
  const canApprove = can('inferenceproxy:config:admin')
  const stepUpNeeded = canApprove && aal < AAL.HARDWARE
  const canAct = canApprove && aal >= AAL.HARDWARE
  const [code, setCode] = useState('')

  const decide = useMutation({
    mutationFn: (deny: boolean) =>
      inferenceProxyApi.approveDevice({ user_code: code.trim(), deny }),
    onSuccess: (res, deny) => {
      toast.success(
        deny
          ? t('device.denied', { code: res.user_code })
          : t('device.approved', { code: res.user_code }),
      )
      setCode('')
    },
    onError: (e: unknown) => {
      if (e instanceof ApiError && e.status === 404) {
        toast.error(t('device.notFound'))
        return
      }
      if (e instanceof ApiError && e.status === 410) {
        toast.error(t('device.expired'))
        return
      }
      // ⛔ ASEGURAMIENTO ANTES QUE ROL: `isForbidden` es SÓLO el status 403
      // (lib/api/errors.ts:59-61) y un `step_up_required` lo satisface también, así que esta rama
      // acusaba al operador de un permiso que SÍ tiene y le escondía la salida. Defensa en
      // profundidad: esta ruta no está en ninguna de las cuatro familias de emisores medidas.
      if (e instanceof ApiError && e.isStepUpRequired) {
        report(e)
        return
      }
      if (e instanceof ApiError && e.isForbidden) {
        toast.warning(t('common:privileged.notAuthorizedToast'))
        return
      }
      toast.error(String((e as Error).message ?? e))
    },
  })

  if (!canApprove) return null

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('device.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="text-sm text-muted-foreground">{t('device.intro')}</p>
        {stepUpNeeded ? (
          <StepUpPanel minAal={AAL.HARDWARE} currentAal={aal} action="proxy" />
        ) : (
          <form
            className="flex flex-col gap-3 sm:flex-row sm:items-end"
            onSubmit={(e) => {
              e.preventDefault()
              if (code.trim()) decide.mutate(false)
            }}
          >
            <div className="flex-1">
              <Field label={t('device.userCode')}>
                {({ id }) => (
                  <Input
                    id={id}
                    value={code}
                    mono
                    autoComplete="off"
                    placeholder={t('device.userCodePlaceholder')}
                    onChange={(e) => setCode(e.target.value)}
                  />
                )}
              </Field>
            </div>
            <div className="flex gap-2">
              <Button
                type="button"
                variant="destructive"
                disabled={!code.trim() || !canAct || decide.isPending}
                onClick={() => decide.mutate(true)}
              >
                {t('device.deny')}
              </Button>
              <Button
                type="submit"
                variant="primary"
                disabled={!code.trim() || !canAct || decide.isPending}
              >
                {decide.isPending && <Spinner size="sm" aria-hidden />}
                {t('device.approve')}
              </Button>
            </div>
          </form>
        )}
      </CardContent>
    </Card>
  )
}

export default InferenceProxyView
