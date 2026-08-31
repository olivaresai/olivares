// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the console half of the AgentCore Cedar export.
//
// THIS IS NOT A BUTTON. The engine models plan → review → (approval) → apply, with
// the plan hash tying the two ends together, and the console has to keep that shape:
//
//   1. `plan` is a dry run that writes nothing. Its result is held in EXPLICIT
//      STATE — `reviewed` — together with the enforcement mode it was computed
//      with, because both travel back on apply and neither may drift.
//   2. `apply` sends `pendingApply.plan.PlanHash`. The engine re-plans and compares
//      (agentcoreexport.go:177-188); a mismatch is 409 and the write does not
//      happen. That is the seam that makes "apply a plan nobody reviewed"
//      impossible rather than merely discouraged, so this view NEVER recomputes a
//      hash, never caches one across a mode change, and never re-plans by itself
//      in response to a 409 — a silent re-plan would hand the engine a hash for a
//      diff the operator has not seen, which is the exact thing being prevented.
//   3. The 202 is neither success nor failure. It means the governance gate has
//      not approved the write, so nothing was written and the reviewed plan is
//      still good: the operator can apply again once the approval lands.
//
// ⛔ CREDENTIALS ARE NOT PART OF THIS SURFACE, AND THAT IS A DESIGN CONSTRAINT,
// NOT AN OMISSION. The exporter's AWS write credentials live BY VALUE in an
// operator-secret JSON file named by OLIVARES_AGENTCORE_EXPORT_CONFIG and are
// deliberately never in the store (agentcoreexportwiring.go:16-21). This console
// therefore has no credential field, no credential display, no "test connection"
// that would echo one back, and no editor for that file. The only thing it says
// about the config is the NAME OF THE VARIABLE that enables the capability, which
// is not a secret and is what turns an inert 501 into an actionable one.
import { useMutation } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCircle2,
  CircleSlash,
  ClipboardList,
  FileWarning,
  Hourglass,
  PlugZap,
  RefreshCw,
  Upload,
} from 'lucide-react'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { useFailedActionReporter } from '@/lib/hooks/use-privileged-mutation'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { toast } from '@/components/ui/toaster'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import {
  governanceApi,
  agentCoreResultFailed,
  isAgentCoreExportNotWired,
  isAgentCoreExportPlanChanged,
} from './api'
import type {
  AgentCoreApplyOutcome,
  AgentCoreEnforcementMode,
  AgentCoreExportPlan,
  AgentCoreModeChoice,
  AgentCorePlannedChange,
} from './types'

/** The env var that wires the exporter. NOT a secret — it is the name of the
 *  switch, and printing it is what makes a 501 actionable instead of dead. */
const CONFIG_ENV = 'OLIVARES_AGENTCORE_EXPORT_CONFIG'

/** The plan the operator is looking at, pinned together with the mode that
 *  produced it. Apply reads BOTH from here and never from the live controls:
 *  the engine re-plans with the mode in the apply body, so sending the selector's
 *  current value instead of the planned one changes the hash and 409s. */
interface ReviewedPlan {
  plan: AgentCoreExportPlan
  /** undefined = the tenant's configured mode was used (no override sent). */
  mode?: AgentCoreEnforcementMode
}

function changes(plan: AgentCoreExportPlan): AgentCorePlannedChange[] {
  return [
    ...(plan.Creates ?? []),
    ...(plan.Updates ?? []),
    ...(plan.Deletes ?? []),
  ]
}

/**
 * The mode the plan's changes actually carry, when they all agree. Read off the
 * PLAN rather than off the selector so the confirmation names what the engine
 * rendered, which is the only value that describes what apply will do — and it is
 * the only way to name a concrete mode when the operator chose "tenant default"
 * and the console does not know the configured value. `null` when the plan has no
 * change carrying a mode (a delete-only plan) or when they disagree.
 */
export function effectivePlanMode(plan: AgentCoreExportPlan): string | null {
  const modes = new Set(
    [...(plan.Creates ?? []), ...(plan.Updates ?? [])]
      .map((c) => c.EnforcementMode)
      .filter((m) => !!m),
  )
  return modes.size === 1 ? [...modes][0] : null
}

export function AgentCoreExportView() {
  const { t } = useTranslation(['governance', 'common'])
  const { can } = useAuth()
  // Both engine routes require the SAME admin permission (governance.go:563-564).
  // can() is pure set membership of what /v1/auth/whoami handed us — no
  // verb arithmetic, no read/admin inference.
  const canExport = can('governance:agentcore-export:admin')

  const [mode, setMode] = useState<AgentCoreModeChoice>('default')
  const [reviewed, setReviewed] = useState<ReviewedPlan | null>(null)
  const [outcome, setOutcome] = useState<AgentCoreApplyOutcome | null>(null)
  const [planChanged, setPlanChanged] = useState(false)
  const [denied, setDenied] = useState<string | null>(null)
  /**
   * The plan the OPEN confirmation dialog is about — a snapshot taken when the
   * operator opened it, not a live read of `reviewed`.
   *
   * Found by the Codex contrast (C1.2) and it is the whole promise of this
   * screen: `reviewed` is state, so a plan request still in flight when the
   * dialog opens would land, replace `reviewed`, and the dialog — which stays
   * open — would re-render its counts and submit the NEW plan under a
   * confirmation the operator gave for the old one. Reading a snapshot means the
   * dialog can only ever apply what it described; the landing plan closes it
   * (see planMutation.onSuccess) instead of quietly changing its subject.
   */
  const [pendingApply, setPendingApply] = useState<ReviewedPlan | null>(null)

  // La política de reporte vive en un solo sitio (use-privileged-mutation.ts:25-32): una
  // mutación escrita a mano conserva su `onError` para la limpieza y DELEGA el reporte aquí.
  const report = useFailedActionReporter()
  // El reintento tiene que llamar a una mutación que aún se está construyendo, así que va por
  // un ref — el mismo mecanismo que usa el hook (use-privileged-mutation.ts:135-138).
  const applyRef = useRef<((v: ReviewedPlan) => void) | null>(null)

  const planMutation = useMutation({
    // The mode travels as the mutation VARIABLE, never as a closure over `mode`.
    // Also from the contrast (C1.3): react-query re-binds a pending mutation's
    // options on every render, so an onSuccess closing over `mode` would pair the
    // ACTIVE plan that was requested with a LOG_ONLY selection made while it was
    // in flight — and apply would then send a mode the displayed plan was not
    // computed with, which the engine answers with a spurious 409.
    mutationFn: (choice: AgentCoreModeChoice) =>
      governanceApi.planAgentCoreExport(
        choice === 'default' ? undefined : choice,
      ),
    onSuccess: (plan, choice) => {
      setReviewed({ plan, mode: choice === 'default' ? undefined : choice })
      // A fresh plan retires whatever the previous one produced: showing the last
      // run's results next to a new diff invites reading them as this diff's.
      setOutcome(null)
      setPlanChanged(false)
      setDenied(null)
      // If a dialog is open it was describing the plan this one just replaced.
      setPendingApply(null)
    },
  })

  // NOT usePrivilegedMutation, and the reason is structural rather than stylistic:
  // that hook has ONE success channel and always calls toast.success, while this
  // route has three distinct 2xx meanings (applied / partially failed / pending
  // approval). Routing a pending or a partial failure through a success toast is
  // the lost third response.
  //
  // The outcome is a PERSISTENT panel rather than a toast, because per-policy
  // write results against AWS are evidence an operator has to read, and a toast
  // is gone before they can. The error arms below are deliberate too and they do
  // NOT match the hook's: a 409 and a 501 have their own renderings, and a 403 is
  // shown with the engine's reason instead of the hook's calm "not authorized" —
  // see the block for why that difference is the point, not a divergence.
  const applyMutation = useMutation({
    mutationFn: (r: ReviewedPlan) =>
      governanceApi.applyAgentCoreExport({
        // ⛔ THE HASH OF THE PLAN ON SCREEN. Not recomputed, not remembered from
        // an earlier plan, not derived from the current controls.
        plan_hash: r.plan.PlanHash,
        enforcement_mode: r.mode,
      }),
    onSuccess: (result) => {
      setOutcome(result)
      setPendingApply(null)
      setPlanChanged(false)
      setDenied(null)
      // A pending write has NOT happened (the gate returns before the write loop,
      // exporter.go:343-346), so the reviewed plan is still exactly what will be
      // applied and stays on screen for a retry once the approval lands. An
      // applied or partially-applied plan HAS been consumed: the remote engine
      // has moved, so this diff no longer describes it and the operator must
      // re-plan rather than press apply twice.
      if (result.kind !== 'pending') setReviewed(null)
    },
    onError: (err, vars) => {
      setPendingApply(null)
      if (isAgentCoreExportPlanChanged(err)) {
        // Say it and stop. Re-planning here would silently produce a diff the
        // operator never reviewed and apply that instead.
        setPlanChanged(true)
        return
      }
      if (isAgentCoreExportNotWired(err)) return
      // ⛔ Y ANTES QUE TODO ESO, LA CEREMONIA. `isForbidden` es SÓLO el status
      // (lib/api/errors.ts:59-61), así que un `step_up_required` caía en la rama de abajo y se
      // pintaba como una DENEGACIÓN DE GOBERNANZA — «no aprobado», «doble control sin
      // satisfacer»— cuando lo que hacía falta era confirmar la sesión. El bloque de abajo dice
      // que la consola no debe decidir qué clase de 403 es; esta clase es la excepción, porque
      // es la única que trae remedio y el motor la nombra por su CÓDIGO, no por deducción.
      //
      // ⛔ Y SE PASA EL REINTENTO. La primera versión no lo pasaba, con el argumento de que
      // el amarre `plan_hash` contra TOCTOU desaconsejaba reaplicar sola. El contraste Codex
      // `sol max` demostró que ese argumento es falso en las dos mitades:
      //   · el panel que se acaba de abrir dice literalmente «Complete the step-up below and
      //     the action resumes» (i18n/locales/en/common.json, privileged.stepUp.description),
      //     y el host ejecuta `retry?.()` (step-up-host.tsx:77-84). Con `retry` a undefined la
      //     interfaz PROMETE una reanudación que no ocurre: una mentira, no una cautela.
      //   · y TOCTOU no lo exige: `apply` RECALCULA el plan y compara el hash antes de
      //     escribir; si cambió responde 409 (modules/governance/agentcoreexport.go:177-187).
      //     Reenviar las mismas variables ya confirmadas no puede autorizar un plan distinto —
      //     el amarre sigue haciendo su trabajo del lado del motor, que es donde manda.
      if (err instanceof ApiError && err.isStepUpRequired) {
        report(err, () => applyRef.current?.(vars))
        return
      }
      if (err instanceof ApiError && err.isForbidden) {
        // ⛔ A 403 ON THIS ROUTE IS USUALLY *NOT* "YOU LACK THE PERMISSION".
        // Found by the Codex contrast (C2.2). The export gate answers 403
        // with its own reason for a governance DENIAL — not approved, approval
        // not bound to the plan (anti-TOCTOU), dual-control unsatisfied, engine
        // off the allowlist (exporter.go:320-353) — and the console reached this
        // route only because can() already said the permission is held. Calling
        // that "not authorized" reports a human's REJECTION as a missing
        // permission, which is the exact confusion ApiError documents at
        // lib/api/client.ts:23-30. So the engine's own reason is shown, and the
        // console does not decide which kind of 403 this was.
        setDenied(err.message)
        return
      }
      toast.error(
        t('agentcoreExport.applyFailed'),
        err instanceof Error && err.message
          ? { description: err.message }
          : undefined,
      )
    },
  })

  applyRef.current = applyMutation.mutate

  if (!canExport) return <ForbiddenState />

  // 501 is the honest frontier of this DEPLOYMENT, not an error and not a
  // permission problem: no config file, no exporters, no export. Detected by
  // status (api.ts), and it can arrive from either route.
  const notWired =
    isAgentCoreExportNotWired(planMutation.error) ||
    isAgentCoreExportNotWired(applyMutation.error)

  if (notWired) {
    return (
      <EmptyState
        icon={<PlugZap />}
        title={t('agentcoreExport.notWiredTitle')}
        description={
          <span className="flex flex-col gap-2">
            <span>{t('agentcoreExport.notWiredBody')}</span>
            <code className="mx-auto rounded bg-muted px-2 py-1 font-mono text-xs">
              {CONFIG_ENV}
            </code>
            <span className="text-xs">
              {t('agentcoreExport.notWiredCredentials')}
            </span>
          </span>
        }
      />
    )
  }

  const planError =
    planMutation.error instanceof Error ? planMutation.error : null

  return (
    <div className="flex flex-col gap-4">
      {/* No caption here: the routed page already carries it as the PageHeader
          description (agentcore-export-route.tsx), and rendering it twice was
          visible on the real screen. */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <ClipboardList className="size-4" />
            {t('agentcoreExport.planTitle')}
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1.5">
            <span className="text-xs font-medium">
              {t('agentcoreExport.modeLabel')}
            </span>
            <Select
              value={mode}
              onValueChange={(v) => {
                setMode(v as AgentCoreModeChoice)
                // The plan on screen was computed with the OLD mode, so it no
                // longer describes what this control says. Drop it: leaving it
                // applyable is how an operator ends up applying a diff that does
                // not match the mode they are looking at.
                setReviewed(null)
                setOutcome(null)
                setPlanChanged(false)
              }}
            >
              <SelectTrigger
                className="w-64"
                aria-label={t('agentcoreExport.modeLabel')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">
                  {t('agentcoreExport.modeDefault')}
                </SelectItem>
                <SelectItem value="ACTIVE">
                  {t('agentcoreExport.modeActive')}
                </SelectItem>
                <SelectItem value="LOG_ONLY">
                  {t('agentcoreExport.modeLogOnly')}
                </SelectItem>
              </SelectContent>
            </Select>
          </label>

          <Button
            variant="secondary"
            onClick={() => planMutation.mutate(mode)}
            disabled={planMutation.isPending}
          >
            <RefreshCw />
            {reviewed
              ? t('agentcoreExport.replan')
              : t('agentcoreExport.computePlan')}
          </Button>

          <p className="w-full text-xs text-muted-foreground">
            {t('agentcoreExport.modeHint')}
          </p>
        </CardContent>
      </Card>

      {planError && !notWired && (
        <Card className="border-danger/40">
          <CardContent className="flex items-start gap-2 py-4 text-sm">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-danger" />
            <span>
              <span className="font-medium">
                {t('agentcoreExport.planFailed')}
              </span>
              <span className="block text-xs text-muted-foreground">
                {planError.message}
              </span>
            </span>
          </CardContent>
        </Card>
      )}

      {planChanged && (
        <Card className="border-warning/50">
          <CardContent className="flex items-start gap-2 py-4 text-sm">
            <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" />
            <span>
              <span className="font-medium">
                {t('agentcoreExport.planChangedTitle')}
              </span>
              <span className="block text-xs text-muted-foreground">
                {t('agentcoreExport.planChangedBody')}
              </span>
            </span>
          </CardContent>
        </Card>
      )}

      {denied && (
        <Card className="border-warning/50">
          <CardContent className="flex items-start gap-2 py-4 text-sm">
            <CircleSlash className="mt-0.5 size-4 shrink-0 text-warning" />
            <span>
              <span className="font-medium">
                {t('agentcoreExport.deniedTitle')}
              </span>
              <span className="block text-xs text-muted-foreground">
                {denied}
              </span>
            </span>
          </CardContent>
        </Card>
      )}

      {reviewed && (
        <PlanPanel
          reviewed={reviewed}
          onApply={() => setPendingApply(reviewed)}
          // Disabled while a plan is in flight too: the panel on screen is about
          // to be replaced, so opening a confirmation for it is opening one for a
          // diff that is already stale.
          applyPending={applyMutation.isPending || planMutation.isPending}
        />
      )}

      {!reviewed && !planMutation.isPending && !planError && (
        <EmptyState
          icon={<ClipboardList />}
          title={t('agentcoreExport.emptyTitle')}
          description={t('agentcoreExport.emptyBody')}
        />
      )}

      {outcome && <OutcomePanel outcome={outcome} />}

      {/* Driven by the SNAPSHOT, so everything it shows and everything it submits
          come from the same plan object — see `pendingApply`. */}
      {pendingApply && (
        <ConfirmDialog
          open={!!pendingApply}
          onOpenChange={(o) => !o && setPendingApply(null)}
          title={t('agentcoreExport.confirmTitle')}
          // The mode is in the DESCRIPTION, in words. It changes what apply does,
          // so it does not belong in a tooltip.
          description={t('agentcoreExport.confirmMode', {
            mode:
              effectivePlanMode(pendingApply.plan) ??
              pendingApply.mode ??
              t('agentcoreExport.modeTenantConfigured'),
          })}
          tone={(pendingApply.plan.Deletes ?? []).length > 0 ? 'danger' : 'default'}
          confirmLabel={t('agentcoreExport.confirmApply')}
          pending={applyMutation.isPending}
          onConfirm={() => applyMutation.mutate(pendingApply)}
        >
          <div className="flex flex-col gap-2 text-sm">
            <p>
              {t('agentcoreExport.confirmCounts', {
                creates: (pendingApply.plan.Creates ?? []).length,
                updates: (pendingApply.plan.Updates ?? []).length,
                deletes: (pendingApply.plan.Deletes ?? []).length,
              })}
            </p>
            <p className="text-xs text-muted-foreground">
              {t('agentcoreExport.confirmEngine', {
                engine: pendingApply.plan.EngineID,
              })}
            </p>
            <p className="font-mono text-xs break-all text-muted-foreground">
              {t('agentcoreExport.confirmHash', {
                hash: pendingApply.plan.PlanHash,
              })}
            </p>
          </div>
        </ConfirmDialog>
      )}
    </div>
  )
}

function PlanPanel({
  reviewed,
  onApply,
  applyPending,
}: {
  reviewed: ReviewedPlan
  onApply: () => void
  applyPending: boolean
}) {
  const { t } = useTranslation(['governance', 'common'])
  const { plan } = reviewed
  const all = changes(plan)
  const unsupported = plan.Unsupported ?? []
  const unchanged = plan.Unchanged ?? []
  const unmanaged = plan.Unmanaged ?? []

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle className="flex items-center gap-2 text-sm">
          <ClipboardList className="size-4" />
          {t('agentcoreExport.diffTitle')}
        </CardTitle>
        <Button variant="primary" onClick={onApply} disabled={applyPending}>
          <Upload />
          {t('agentcoreExport.applyAction')}
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {/* Counters interpolate `n`, never `count`: `count` is i18next's plural
            trigger and would demand a CLDR category set per language for what is
            a label with a number after it. */}
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Badge variant="success">
            {t('agentcoreExport.creates', { n: (plan.Creates ?? []).length })}
          </Badge>
          {/* ⛔ El color era INCONDICIONAL: un plan que sólo crea deja las dos
              colecciones vacías —una salida perfectamente válida— y aun así se
              pintaban «0 updates» en ámbar y «0 deletes» en rojo. Un aviso a cero
              no avisa de nada y gasta la atención que el aviso de verdad necesita. */}
          <Badge
            variant={
              (plan.Updates ?? []).length > 0 ? 'warning' : 'neutral'
            }
          >
            {t('agentcoreExport.updates', { n: (plan.Updates ?? []).length })}
          </Badge>
          <Badge
            variant={(plan.Deletes ?? []).length > 0 ? 'danger' : 'neutral'}
          >
            {t('agentcoreExport.deletes', { n: (plan.Deletes ?? []).length })}
          </Badge>
          <Badge variant="neutral">
            {t('agentcoreExport.unchanged', { n: unchanged.length })}
          </Badge>
          <Badge variant="neutral">
            {t('agentcoreExport.unmanaged', { n: unmanaged.length })}
          </Badge>
        </div>

        {/* The plan hash is shown because it IS the identity of what the operator
            reviewed, and it is what travels on apply. */}
        <p className="font-mono text-xs break-all text-muted-foreground">
          {t('agentcoreExport.planHash', { hash: plan.PlanHash })}
        </p>

        {all.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t('agentcoreExport.noChanges')}
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-border rounded border border-border">
            {all.map((c) => (
              <li
                key={`${c.Op}:${c.Name}`}
                className="flex flex-col gap-1 p-3 text-sm"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <Badge
                    variant={
                      c.Op === 'create'
                        ? 'success'
                        : c.Op === 'update'
                          ? 'warning'
                          : 'danger'
                    }
                  >
                    {t(`agentcoreExport.op.${c.Op}`, { defaultValue: c.Op })}
                  </Badge>
                  <span className="font-mono text-xs">{c.Name}</span>
                  {c.EnforcementMode && (
                    <Badge variant="neutral">{c.EnforcementMode}</Badge>
                  )}
                  {/* A remote policy moving from ACTIVE to LOG_ONLY stops being
                      enforced. Show BOTH sides rather than only the target. */}
                  {c.RemoteEnforcementMode &&
                    c.RemoteEnforcementMode !== c.EnforcementMode && (
                      <span className="text-xs text-muted-foreground">
                        {t('agentcoreExport.modeChange', {
                          from: c.RemoteEnforcementMode,
                          to: c.EnforcementMode,
                        })}
                      </span>
                    )}
                </span>
                {c.Statement && (
                  <pre className="overflow-x-auto rounded bg-muted p-2 font-mono text-xs">
                    {c.Statement}
                  </pre>
                )}
              </li>
            ))}
          </ul>
        )}

        {/* NEVER silently dropped (export.go:110-115). A row that could not be
            projected is NOT in this export, and an operator who does not see it
            believes a policy reached AWS that never will. */}
        {unsupported.length > 0 && (
          <div className="flex flex-col gap-2 rounded border border-warning/50 p-3">
            <span className="flex items-center gap-2 text-sm font-medium">
              <FileWarning className="size-4 text-warning" />
              {t('agentcoreExport.unsupportedTitle', {
                n: unsupported.length,
              })}
            </span>
            <p className="text-xs text-muted-foreground">
              {t('agentcoreExport.unsupportedBody')}
            </p>
            <ul className="flex flex-col gap-1">
              {unsupported.map((u, i) => (
                <li
                  key={`${u.Item.Kind}:${u.Item.SubjectRef}:${u.Reason}:${i}`}
                  className="text-xs"
                >
                  <span className="font-mono">
                    {u.Item.Kind}
                    {u.Item.SubjectRef ? ` · ${u.Item.SubjectRef}` : ''}
                  </span>
                  <span className="text-muted-foreground"> — {u.Reason}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function OutcomePanel({ outcome }: { outcome: AgentCoreApplyOutcome }) {
  const { t } = useTranslation(['governance', 'common'])

  if (outcome.kind === 'pending') {
    // NOT a success and NOT a failure. Nothing was written: the governance gate
    // has not approved this plan yet.
    return (
      <Card className="border-warning/50">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-sm">
            <Hourglass className="size-4 text-warning" />
            {t('agentcoreExport.pendingTitle')}
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          <p>{t('agentcoreExport.pendingBody')}</p>
          <p className="font-mono text-xs break-all">
            {t('agentcoreExport.approvalRef', { ref: outcome.approvalRef })}
          </p>
          <p className="font-mono text-xs break-all text-muted-foreground">
            {t('agentcoreExport.planHash', { hash: outcome.planHash })}
          </p>
          <p className="text-xs text-muted-foreground">
            {t('agentcoreExport.pendingRetry')}
          </p>
        </CardContent>
      </Card>
    )
  }

  const failed = outcome.results.filter(agentCoreResultFailed)
  const partial = outcome.kind === 'partial'

  return (
    <Card className={partial ? 'border-danger/40' : undefined}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          {partial ? (
            <CircleSlash className="size-4 text-danger" />
          ) : (
            <CheckCircle2 className="size-4 text-success" />
          )}
          {partial
            ? t('agentcoreExport.partialTitle')
            : t('agentcoreExport.appliedTitle')}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-sm">
        {partial ? (
          <>
            <p className="text-sm">
              {t('agentcoreExport.partialCount', {
                failed: failed.length,
                total: outcome.results.length,
              })}
            </p>
            <p className="text-xs text-muted-foreground">
              {t('agentcoreExport.partialBody')}
            </p>
          </>
        ) : (
          <p className="text-xs text-muted-foreground">
            {t('agentcoreExport.appliedCount', { n: outcome.results.length })}
          </p>
        )}
        <p className="font-mono text-xs break-all text-muted-foreground">
          {t('agentcoreExport.planHash', { hash: outcome.planHash })}
        </p>
        {outcome.results.length > 0 && (
          <ul className="flex flex-col divide-y divide-border rounded border border-border">
            {outcome.results.map((r, i) => (
              <li
                key={`${r.op}:${r.name}:${i}`}
                className="flex flex-col gap-1 p-2 text-xs"
              >
                <span className="flex flex-wrap items-center gap-2">
                  <Badge variant={agentCoreResultFailed(r) ? 'danger' : 'neutral'}>
                    {r.op}
                  </Badge>
                  <span className="font-mono">{r.name}</span>
                  {r.status && (
                    <span className="text-muted-foreground">{r.status}</span>
                  )}
                </span>
                {r.error && <span className="text-danger">{r.error}</span>}
                {r.status_reasons && r.status_reasons.length > 0 && (
                  <span className="text-muted-foreground">
                    {r.status_reasons.join(' · ')}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
