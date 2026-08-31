// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Guardian-agent rules (modules/governance/guardian.go): operator-authored,
// semi-autonomous containment over the finding rail. A rule matches findings by
// kind allowlist + severity floor and fires ONE containment (stop_agent /
// quarantine_nhi / stop_estate), immediately (auto) or behind one human approval.
// The view authors rules and renders the containment trail — matching and
// execution live entirely in the engine (deny-closed, anti-feedback-loop,
// idempotent), the web never simulates them.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, ShieldHalf, Trash2 } from 'lucide-react'
import { useMemo, useState } from 'react'
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
import { Label } from '@/components/ui/label'
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
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { ListTruncationBadge, SeverityBadge } from '@/features/_intel'
import { RelTime } from '@/features/shared/rel-time'
import { ApiError } from '@/lib/api/errors'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { killswitchApi, killswitchKeys } from './api'
import { GuardianStatusBadge } from './components'
import './i18n'
import {
  GUARDIAN_ACTIONS,
  GUARDIAN_DEFAULT_SEVERITY,
  GUARDIAN_MODES,
  GUARDIAN_SEVERITIES,
  GUARDIAN_AGENT_TIERS,
} from './types'
import type {
  CreateGuardianRuleRequest,
  GuardianAction,
  GuardianActionDTO,
  GuardianMode,
  GuardianRuleDTO,
  GuardianAgentTier,
} from './types'

// The containment trail is a live feed (the loop fires on findings, the sweep
// executes approvals) — poll it gently; rules change only by operator action.
const GUARDIAN_PAGE = 1000

const GUARDIAN_TRAIL_POLL_MS = 30_000

export function GuardianSection() {
  const { t } = useTranslation(['killswitch', 'common', 'errors'])
  const report = useFailedActionReporter()
  const { activeTenant, can } = useAuth()
  const canAdmin = can('governance:guardian:admin')
  const queryClient = useQueryClient()

  const [createOpen, setCreateOpen] = useState(false)
  const [deleting, setDeleting] = useState<GuardianRuleDTO | null>(null)
  const [deleteError, setDeleteError] = useState<{
    title: string
    message: string
  } | null>(null)

  // Mismo techo y misma razón que en las paradas: sin `limit` el motor pagina a 100 y el
  // rastro de contención se ve entero sin serlo.
  const rules = useQuery({
    queryKey: killswitchKeys.guardianRules(activeTenant),
    queryFn: () => killswitchApi.listGuardianRules({ limit: GUARDIAN_PAGE }),
  })
  const actions = useQuery({
    queryKey: killswitchKeys.guardianActions(activeTenant, {
      limit: GUARDIAN_PAGE,
    }),
    queryFn: () => killswitchApi.listGuardianActions({ limit: GUARDIAN_PAGE }),
    refetchInterval: GUARDIAN_TRAIL_POLL_MS,
  })

  const toggle = usePrivilegedMutation<{
    rule: GuardianRuleDTO
    enabled: boolean
  }>({
    mutationFn: ({ rule, enabled }) =>
      killswitchApi.updateGuardianRule(rule.id, { enabled }),
    invalidateKeys: () => [killswitchKeys.guardianRules(activeTenant)],
    successMessage: (_d, vars) =>
      vars.enabled ? t('guardian.ruleEnabled') : t('guardian.ruleDisabled'),
  })

  const deleteRule = useMutation({
    mutationFn: (id: string) => killswitchApi.deleteGuardianRule(id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({
          queryKey: killswitchKeys.guardianRules(activeTenant),
        }),
        queryClient.invalidateQueries({
          queryKey: killswitchKeys.guardianActions(activeTenant),
        }),
      ])
      toast.success(t('guardian.delete.deleted'))
      setDeleting(null)
      setDeleteError(null)
    },
    onError: (err, id) => {
      // ⛔ ASEGURAMIENTO ANTES QUE ROL, y este sitio lo encontró la GUARDA de esta sesión, no
      // mi lista de residuo: lo había excluido por «tocado por una rama abierta» (#750, que
      // añade `agent_tier`), y tocado no es arreglado. `isForbidden` es sólo el status
      // (lib/api/errors.ts:59) y un `step_up_required` lo satisface también.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(err, () => deleteRule.mutate(id))
        return
      }
      if (err instanceof ApiError && err.isForbidden) {
        toast.warning(t('common:privileged.notAuthorizedToast'))
        return
      }
      if (
        err instanceof ApiError &&
        (err.status === 404 || err.status === 409)
      ) {
        setDeleteError({
          title: t(
            err.status === 404
              ? 'guardian.delete.notFound'
              : 'guardian.delete.conflict',
          ),
          message: err.message,
        })
        if (err.status === 404) {
          void queryClient.invalidateQueries({
            queryKey: killswitchKeys.guardianRules(activeTenant),
          })
        }
        return
      }
      toast.error(
        t('errors:generic'),
        err instanceof Error && err.message
          ? { description: err.message }
          : undefined,
      )
    },
  })

  // Memoized for cell-identity stability across query settles (flexRender treats
  // a fresh inline cell function as a new component type and remounts the cell).
  const { mutate: toggleRule, isPending: togglePending } = toggle
  const ruleColumns = useMemo<TableColumn<GuardianRuleDTO, unknown>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('guardian.name'),
        cell: ({ row }) => (
          <span
            className="font-mono text-xs font-medium text-foreground"
            title={row.original.note || undefined}
          >
            {row.original.name}
          </span>
        ),
      },
      {
        accessorKey: 'match_kinds',
        header: t('guardian.kinds'),
        cell: ({ row }) =>
          row.original.match_kinds ? (
            <span className="truncate font-mono text-xs text-muted-foreground">
              {row.original.match_kinds}
            </span>
          ) : (
            <span className="text-xs text-muted-foreground">
              {t('guardian.kindsAny')}
            </span>
          ),
      },
      {
        // ⛔ SIN ESTA COLUMNA EL PR NO TERMINABA SU PROPIO TRABAJO. El contraste `sol max` lo
        // dijo con file:line: el formulario ya mandaba `agent_tier` y el DTO ya lo conservaba,
        // pero la tabla saltaba de `match_kinds` a `min_severity`, así que **una regla acotada
        // seguía viéndose como si aplicara a todos** — el defecto que el propio comentario de
        // `types.ts:182-185` describe. Enviar sin poder LEER no cierra el hueco: lo mueve.
        accessorKey: 'agent_tier',
        header: t('guardian.agentTier'),
        cell: ({ row }) =>
          row.original.agent_tier ? (
            <span className="font-mono text-xs">{row.original.agent_tier}</span>
          ) : (
            <span className="text-xs text-muted-foreground">
              {t('guardian.create.agentTierAny')}
            </span>
          ),
      },
      {
        accessorKey: 'min_severity',
        header: t('guardian.minSeverity'),
        cell: ({ row }) => (
          <SeverityBadge severity={row.original.min_severity} />
        ),
      },
      {
        accessorKey: 'action',
        header: t('guardian.action'),
        cell: ({ row }) => (
          <Badge
            variant={
              row.original.action === 'stop_estate' ? 'danger' : 'neutral'
            }
          >
            {t(`guardian.actionLabel.${row.original.action}`, {
              defaultValue: row.original.action,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'mode',
        header: t('guardian.mode'),
        cell: ({ row }) => (
          <Badge variant={row.original.mode === 'auto' ? 'warning' : 'info'}>
            {t(`guardian.modeLabel.${row.original.mode}`, {
              defaultValue: row.original.mode,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'enabled',
        header: t('guardian.enabled'),
        cell: ({ row }) => (
          <Switch
            checked={row.original.enabled}
            disabled={!canAdmin || togglePending}
            aria-label={t(
              row.original.enabled
                ? 'guardian.toggleDisable'
                : 'guardian.toggleEnable',
              { name: row.original.name },
            )}
            onCheckedChange={(enabled) =>
              toggleRule({ rule: row.original, enabled })
            }
          />
        ),
      },
      {
        id: 'actions',
        header: () => <span className="sr-only">{t('guardian.actions')}</span>,
        enableSorting: false,
        cell: ({ row }) =>
          canAdmin ? (
            <Button
              variant="destructive"
              size="sm"
              aria-label={t('guardian.delete.actionAria', {
                name: row.original.name,
              })}
              disabled={deleteRule.isPending}
              onClick={() => {
                setDeleteError(null)
                setDeleting(row.original)
              }}
            >
              <Trash2 aria-hidden />
              {t('guardian.delete.action')}
            </Button>
          ) : null,
      },
    ],
    [t, canAdmin, deleteRule.isPending, toggleRule, togglePending],
  )

  const actionColumns = useMemo<TableColumn<GuardianActionDTO, unknown>[]>(
    () => [
      {
        accessorKey: 'rule_name',
        header: t('guardian.trail.rule'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.rule_name}
          </span>
        ),
      },
      {
        id: 'finding',
        header: t('guardian.trail.finding'),
        cell: ({ row }) => (
          <span className="flex items-center gap-1.5">
            <span className="truncate font-mono text-xs text-muted-foreground">
              {row.original.finding_kind}
            </span>
            <SeverityBadge severity={row.original.finding_severity} />
          </span>
        ),
      },
      {
        id: 'target',
        header: t('guardian.trail.target'),
        cell: ({ row }) => (
          <span className="flex items-center gap-1.5">
            <Badge variant="neutral">{row.original.target_kind}</Badge>
            {row.original.target_ref && (
              <span className="truncate font-mono text-xs text-muted-foreground">
                {row.original.target_ref}
              </span>
            )}
          </span>
        ),
      },
      {
        id: 'containment',
        header: t('guardian.trail.action'),
        cell: ({ row }) => (
          <span className="flex items-center gap-1.5">
            <Badge variant="neutral">
              {t(`guardian.actionLabel.${row.original.action}`, {
                defaultValue: row.original.action,
              })}
            </Badge>
            <Badge variant="outline">
              {t(`guardian.modeLabel.${row.original.mode}`, {
                defaultValue: row.original.mode,
              })}
            </Badge>
          </span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('guardian.trail.status'),
        cell: ({ row }) => (
          <span className="flex items-center gap-1.5">
            <GuardianStatusBadge status={row.original.status} />
            {row.original.approval_id && (
              <span
                className="truncate font-mono text-xs text-muted-foreground"
                title={t('guardian.trail.approval')}
              >
                {row.original.approval_id}
              </span>
            )}
          </span>
        ),
      },
      {
        id: 'when',
        header: t('guardian.trail.when'),
        cell: ({ row }) =>
          row.original.executed_at ? (
            <RelTime ts={row.original.executed_at} />
          ) : (
            '—'
          ),
      },
      {
        accessorKey: 'detail',
        header: t('guardian.trail.detail'),
        cell: ({ row }) => (
          <span
            className="truncate text-xs text-muted-foreground"
            title={row.original.detail || undefined}
          >
            {row.original.detail || '—'}
          </span>
        ),
      },
    ],
    [t],
  )

  return (
    <div className="flex flex-col gap-3">
      <div>
        <h2 className="text-base font-medium">{t('guardian.title')}</h2>
        <p className="text-xs text-muted-foreground">{t('guardian.caption')}</p>
      </div>

      <ListTruncationBadge
        query={rules}
        label={t('guardian.truncated', { n: GUARDIAN_PAGE })}
        hint={t('guardian.truncatedHint')}
      />

      <DataTable
        columns={ruleColumns}
        data={rules.data?.items ?? []}
        isLoading={rules.isLoading}
        error={rules.error}
        onRetry={() => rules.refetch()}
        getRowId={(r) => r.id}
        label={t('guardian.label')}
        empty={
          <EmptyState
            icon={<ShieldHalf />}
            title={t('guardian.empty')}
            description={t('guardian.emptyHint')}
          />
        }
        toolbar={
          canAdmin ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setCreateOpen(true)}
            >
              <Plus />
              {t('guardian.newRule')}
            </Button>
          ) : undefined
        }
      />

      <div>
        <h3 className="text-sm font-medium">{t('guardian.trail.title')}</h3>
        <p className="text-xs text-muted-foreground">
          {t('guardian.trail.caption')}
        </p>
      </div>

      {/* El rastro de contención cortado por arriba se lee «no ha hecho nada más», que es
          lo contrario de lo que significa un corte en un registro de acciones. */}
      <ListTruncationBadge
        query={actions}
        label={t('guardian.trailTruncated', { n: GUARDIAN_PAGE })}
        hint={t('guardian.trailTruncatedHint')}
      />

      <DataTable
        columns={actionColumns}
        data={actions.data?.items ?? []}
        isLoading={actions.isLoading}
        error={actions.error}
        onRetry={() => actions.refetch()}
        getRowId={(r) => r.id}
        label={t('guardian.trail.label')}
        empty={
          <EmptyState
            icon={<ShieldHalf />}
            title={t('guardian.trail.empty')}
            description={t('guardian.trail.emptyHint')}
          />
        }
      />

      <GuardianRuleDialog open={createOpen} onOpenChange={setCreateOpen} />

      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open && !deleteRule.isPending) {
            setDeleting(null)
            setDeleteError(null)
          }
        }}
        title={t('guardian.delete.title', { name: deleting?.name ?? '' })}
        description={t('guardian.delete.description')}
        confirmLabel={t('guardian.delete.confirm')}
        tone="danger"
        pending={deleteRule.isPending}
        onConfirm={() => {
          if (deleting) {
            setDeleteError(null)
            deleteRule.mutate(deleting.id)
          }
        }}
      >
        {deleteError ? (
          <div
            role="alert"
            className="rounded-md border border-danger-line bg-danger-soft px-3 py-2"
          >
            <p className="text-xs font-medium text-danger">
              {deleteError.title}
            </p>
            <p className="mt-1 text-xs text-foreground">
              {deleteError.message}
            </p>
          </div>
        ) : null}
      </ConfirmDialog>
    </div>
  )
}

// --- rule authoring dialog -------------------------------------------------------

export interface GuardianRuleDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function GuardianRuleDialog({
  open,
  onOpenChange,
}: GuardianRuleDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] max-w-xl overflow-y-auto">
        {open && <RuleForm onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function RuleForm({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation(['killswitch', 'common'])
  const { activeTenant } = useAuth()

  const [name, setName] = useState('')
  const [matchKinds, setMatchKinds] = useState('')
  // '' = «cualquier tier», que es el DEFECTO y lo que la consola venía haciendo siempre
  // sin poder decirlo. No es un valor del motor: es la ausencia del campo.
  const [agentTier, setAgentTier] = useState<string>('')
  const [minSeverity, setMinSeverity] = useState<string>(
    GUARDIAN_DEFAULT_SEVERITY,
  )
  const [action, setAction] = useState<GuardianAction>('stop_agent')
  const [mode, setMode] = useState<GuardianMode>('approval')
  const [note, setNote] = useState('')
  const [enabled, setEnabled] = useState(true)

  const valid = name.trim().length > 0

  const create = usePrivilegedMutation<
    CreateGuardianRuleRequest,
    GuardianRuleDTO
  >({
    mutationFn: (input) => killswitchApi.createGuardianRule(input),
    invalidateKeys: () => [killswitchKeys.guardianRules(activeTenant)],
    successMessage: t('guardian.create.created'),
    onDone: onClose,
  })

  function submit() {
    if (!valid) return
    create.mutate({
      name: name.trim(),
      enabled,
      ...(matchKinds.trim() ? { match_kinds: matchKinds.trim() } : {}),
      min_severity: minSeverity,
      action,
      mode,
      // ⛔ SÓLO si el operador eligió uno. Mandar `agent_tier: ''` sería inventar una
      // intención que no expresó: para el motor vacío y ausente significan lo mismo
      // («cualquiera»), así que el cuerpo no lleva ruido — y hay celda que lo prueba.
      ...(agentTier ? { agent_tier: agentTier as GuardianAgentTier } : {}),
      ...(note.trim() ? { note: note.trim() } : {}),
    })
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('guardian.create.title')}</DialogTitle>
        <DialogDescription>{t('guardian.create.body')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field
          label={t('guardian.create.name')}
          htmlFor="gr-name"
          description={t('guardian.create.nameHint')}
          required
        >
          <Input
            id="gr-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            mono
          />
        </Field>

        <Field
          label={t('guardian.create.kinds')}
          htmlFor="gr-kinds"
          description={t('guardian.create.kindsHint')}
        >
          <Input
            id="gr-kinds"
            value={matchKinds}
            onChange={(e) => setMatchKinds(e.target.value)}
            mono
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-3">
          <Field
            label={t('guardian.create.agentTier')}
            htmlFor="gr-agent-tier"
            description={t('guardian.create.agentTierHint')}
          >
            <Select
              value={agentTier || 'any'}
              onValueChange={(v) => setAgentTier(v === 'any' ? '' : v)}
            >
              <SelectTrigger id="gr-agent-tier">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="any">
                  {t('guardian.create.agentTierAny')}
                </SelectItem>
                {GUARDIAN_AGENT_TIERS.map((x) => (
                  <SelectItem key={x} value={x}>
                    {x}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('guardian.create.minSeverity')}
            htmlFor="gr-severity"
            description={t('guardian.create.minSeverityHint')}
          >
            <Select value={minSeverity} onValueChange={setMinSeverity}>
              <SelectTrigger id="gr-severity">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {GUARDIAN_SEVERITIES.map((s) => (
                  <SelectItem key={s} value={s}>
                    {s}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('guardian.create.action')}
            htmlFor="gr-action"
            description={t('guardian.create.actionHint')}
          >
            <Select
              value={action}
              onValueChange={(v) => setAction(v as GuardianAction)}
            >
              <SelectTrigger id="gr-action">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {GUARDIAN_ACTIONS.map((a) => (
                  <SelectItem key={a} value={a}>
                    {t(`guardian.actionLabel.${a}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            label={t('guardian.create.mode')}
            htmlFor="gr-mode"
            description={t('guardian.create.modeHint')}
          >
            <Select
              value={mode}
              onValueChange={(v) => setMode(v as GuardianMode)}
            >
              <SelectTrigger id="gr-mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {GUARDIAN_MODES.map((m) => (
                  <SelectItem key={m} value={m}>
                    {t(`guardian.modeLabel.${m}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>

        <Field
          label={t('guardian.create.note')}
          htmlFor="gr-note"
          description={t('guardian.create.noteHint')}
        >
          <Textarea
            id="gr-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
          />
        </Field>

        <div className="flex items-center gap-2">
          <Switch
            id="gr-enabled"
            checked={enabled}
            onCheckedChange={setEnabled}
          />
          <Label htmlFor="gr-enabled">{t('guardian.create.enabled')}</Label>
        </div>
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={create.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={submit}
          disabled={!valid || create.isPending}
        >
          {create.isPending && <Spinner size="sm" aria-hidden />}
          {t('guardian.create.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
