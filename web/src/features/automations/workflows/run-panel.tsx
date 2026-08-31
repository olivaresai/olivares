// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import { CirclePlay } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { GateBadge } from '@/features/_intel'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { layeredLayout } from '@/features/shared/graph/layout'
import { useIsDark } from '@/features/shared/graph/theme'
import { cn } from '@/lib/utils'
import { workflowsApi, workflowsKeys } from './api'
import type {
  RunWorkflowResponse,
  WorkflowRun,
  WorkflowRunStep,
  WorkflowRunStepStatus,
} from './types'
import './i18n'

export function RunPanel({
  workflowId,
  canAdmin,
}: {
  workflowId: string
  canAdmin: boolean
}) {
  const { t } = useTranslation('automations-workflows')
  const { activeTenant } = useAuth()
  const [open, setOpen] = useState(false)
  const [approval, setApproval] = useState<RunWorkflowResponse | null>(null)
  const [selectedRunId, setSelectedRunId] = useState<string | null>(null)

  const history = useQuery({
    queryKey: workflowsKeys.runs(activeTenant, workflowId),
    queryFn: () => workflowsApi.runs(workflowId),
    enabled: open,
  })
  const selectedRun = useQuery({
    queryKey: workflowsKeys.run(activeTenant, workflowId, selectedRunId ?? ''),
    queryFn: () => workflowsApi.runDetail(workflowId, selectedRunId as string),
    enabled: open && selectedRunId !== null,
    refetchInterval: (query) =>
      query.state.data?.status === 'running' ? 4_000 : false,
  })

  const phaseOne = usePrivilegedMutation<void, RunWorkflowResponse>({
    mutationFn: () => workflowsApi.run(workflowId),
    successMessage: t('run.requested'),
    onDone: (response) => setApproval(response),
  })
  const phaseTwo = usePrivilegedMutation<string, RunWorkflowResponse>({
    mutationFn: (approvalRef) => workflowsApi.run(workflowId, approvalRef),
    invalidateKeys: [workflowsKeys.runs(activeTenant, workflowId)],
    successMessage: t('run.started'),
    onDone: (response) => {
      if (response.run) setSelectedRunId(response.run.id)
    },
  })

  function start() {
    setOpen(true)
    setApproval(null)
    setSelectedRunId(null)
    phaseOne.reset()
    phaseTwo.reset()
    phaseOne.mutate()
  }

  const currentRun = selectedRun.data ?? phaseTwo.data?.run
  const error = phaseTwo.error ?? phaseOne.error

  return (
    <>
      {canAdmin ? (
        <Button variant="primary" size="sm" onClick={start}>
          <CirclePlay aria-hidden />
          {t('run.button')}
        </Button>
      ) : (
        <Button variant="secondary" size="sm" onClick={() => setOpen(true)}>
          {t('run.history')}
        </Button>
      )}
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className="overflow-y-auto sm:max-w-3xl">
          <SheetHeader>
            <SheetTitle>{t('run.title')}</SheetTitle>
            <SheetDescription>{t('run.description')}</SheetDescription>
          </SheetHeader>

          {phaseOne.isPending ? (
            <div
              className="flex items-center gap-2 rounded-md border border-border p-3 text-sm text-muted-foreground"
              role="status"
            >
              <Spinner className="size-4" />
              {t('run.requesting')}
            </div>
          ) : null}

          {error ? (
            <RunError error={error} retry={() => phaseOne.mutate()} />
          ) : null}

          {approval?.approval_ref && !currentRun ? (
            <section className="space-y-3 rounded-lg border border-info-line bg-info-soft p-4">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-sm font-medium text-info">
                  {t('run.approvalTitle')}
                </h3>
                {/* ⛔ ESTO ERA `variant="warning"` FIJO: una puerta APROBADA se leía como una
                    advertencia, y una RECHAZADA como la misma advertencia. El estado ya viene
                    en el campo; la variante sale de él. */}
                <GateBadge gate={approval.gate_status} />
              </div>
              <div>
                <p className="text-xs text-muted-foreground">
                  {t('run.approvalRef')}
                </p>
                <code className="break-all font-mono text-sm text-foreground">
                  {approval.approval_ref}
                </code>
              </div>
              <p className="text-sm text-info">{t('run.approvalHint')}</p>
              {canAdmin ? (
                <Button
                  variant="primary"
                  size="sm"
                  disabled={phaseTwo.isPending}
                  onClick={() => phaseTwo.mutate(approval.approval_ref!)}
                >
                  {t('run.execute')}
                </Button>
              ) : null}
            </section>
          ) : null}

          {selectedRun.isError ? (
            <ErrorState
              className="py-6"
              title={t('run.failed')}
              retry={() => void selectedRun.refetch()}
            />
          ) : currentRun ? (
            <RunView run={currentRun} />
          ) : null}

          <section className="space-y-3 border-t border-border pt-4">
            <h3 className="text-sm font-medium text-foreground">
              {t('run.history')}
            </h3>
            {history.isPending ? (
              <div role="status" className="flex justify-center py-4">
                <Spinner />
              </div>
            ) : /* ⛔ EL FALLO VA ANTES QUE EL VACÍO, Y ESE ORDEN ES EL ARREGLO. Con la cadena
                   anterior, un historial que fallaba dejaba `history.data` en `undefined`, así que
                   `(undefined?.items.length ?? 0) === 0` daba TRUE y la pantalla afirmaba «no hay
                   ejecuciones». Eso es una afirmación sobre el MUNDO cuando lo único cierto es que
                   no se pudo mirar: un operador que busca por qué falló anoche lee que no hubo
                   nada. Un 500 y un historial vacío son estados distintos y ahora se distinguen. */
            history.isError ? (
              <ErrorState
                className="py-6"
                title={t('run.historyFailed')}
                retry={() => void history.refetch()}
              />
            ) : (history.data?.items.length ?? 0) === 0 ? (
              <EmptyState className="py-6" title={t('run.historyEmpty')} />
            ) : (
              <ul className="space-y-2">
                {history.data?.items.map((run) => (
                  <li
                    key={run.id}
                    className="flex items-center gap-2 rounded-md border border-border bg-surface p-2"
                  >
                    <Badge variant={runStatusVariant(run.status)}>
                      {t(`run.status.${run.status}`)}
                    </Badge>
                    <code className="min-w-0 flex-1 truncate font-mono text-xs">
                      {run.id}
                    </code>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setSelectedRunId(run.id)}
                    >
                      {t('run.viewRun', { id: run.id })}
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </SheetContent>
      </Sheet>
    </>
  )
}

// ⛔ EXPORTADO PARA PODER PROBARLO, y se dice por qué: es la unidad que DECIDE cuál de los dos 403
// se está viendo, y su llamante (`:127`) le pasa el `error` de la mutación tal cual. Probarlo por
// la pantalla entera obligaría a doblar la consulta y mediría el doble, no esta decisión.
export function RunError({
  error,
  retry,
}: {
  error: unknown
  retry?: () => void
}) {
  const { t } = useTranslation(['automations-workflows', 'common'])
  let message = t('run.failed')
  if (error instanceof ApiError) {
    // ⛔ LA CEREMONIA VA ANTES QUE EL ROL, Y SE RECONOCE POR EL CÓDIGO, NO POR EL STATUS.
    // `step_up_required` TAMBIÉN es un 403 (`lib/api/errors.ts`), así que la rama de abajo lo
    // pintaba como «no tienes permiso» — acusando al operador de carecer de algo que sí tiene, y
    // sin ofrecerle la salida. Esta pantalla decide un MENSAJE, no una vista, así que la respuesta
    // correcta es la copy propia del step-up, igual que `identity/nhi-actions.tsx:112` y
    // `session-viewer/session-viewer-page.tsx:291`.
    //
    // Y la forma importa: leer `error.status === 403` a pelo es invisible para cualquier barrido
    // de `isForbidden` — por eso este fichero sobrevivió a la campaña entera.
    if (error.isStepUpRequired) message = t('common:privileged.stepUp.title')
    else if (error.status === 403) message = t('run.forbidden')
    else if (error.status === 423) message = t('run.locked')
    else if (error.status === 409) message = t('run.conflict')
  }
  return (
    <div
      role="alert"
      className="flex flex-col items-start gap-2 rounded-md border border-danger-line bg-danger-soft p-3 text-sm text-danger"
    >
      <span>{message}</span>
      {/* ⛔ EL REINTENTO FALTABA, y sin él este panel decía «falló» y dejaba al operador sin
          salida: cerrar el diálogo y volver a abrirlo era la única forma de reintentar, que es
          una salida que nadie descubre. Sólo se ofrece cuando el llamante sabe cómo reintentar
          —no todos los errores son reintentables y un botón que no arregla nada es peor que
          ninguno—, así que `retry` es opcional y la rama de step-up NO lo usa: ahí la salida es
          la ceremonia, no repetir la misma llamada. */}
      {retry && !(error instanceof ApiError && error.isStepUpRequired) ? (
        <Button size="sm" variant="outline" onClick={retry}>
          {t('common:actions.retry')}
        </Button>
      ) : null}
    </div>
  )
}

function RunView({ run }: { run: WorkflowRun }) {
  const { t } = useTranslation('automations-workflows')
  return (
    <section className="space-y-4 rounded-lg border border-border bg-elevated p-4">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('run.current', { id: run.id })}
        </h3>
        <Badge variant={runStatusVariant(run.status)}>
          {t(`run.status.${run.status}`)}
        </Badge>
      </div>
      {run.paused_reason ? (
        <p className="rounded-md border border-warning-line bg-warning-soft p-2 text-sm text-warning">
          {t('run.paused', { reason: run.paused_reason })}
        </p>
      ) : null}
      <div>
        <h4 className="mb-2 text-xs font-medium text-muted-foreground">
          {t('run.graph')}
        </h4>
        <RunGraph steps={run.steps} />
      </div>
      <div>
        <h4 className="mb-2 text-xs font-medium text-muted-foreground">
          {t('run.timeline')}
        </h4>
        <ol className="space-y-2">
          {run.steps.map((step) => (
            <li
              key={step.ref}
              className="flex flex-wrap items-start gap-2 border-l-2 border-border py-1 pl-3"
            >
              <code className="font-mono text-xs text-foreground">
                {step.ref}
              </code>
              <Badge variant={stepStatusVariant(step.status)}>
                {t(`run.status.${step.status}`)}
              </Badge>
              {step.detail ? (
                <p className="w-full text-xs text-muted-foreground">
                  {step.detail}
                </p>
              ) : null}
            </li>
          ))}
        </ol>
      </div>
    </section>
  )
}

interface RunNodeData extends Record<string, unknown> {
  ref: string
  status: WorkflowRunStepStatus
}

function RunNode({ data }: NodeProps<Node<RunNodeData>>) {
  const { t } = useTranslation('automations-workflows')
  return (
    <div
      className={cn(
        'min-w-32 rounded-lg border bg-surface px-3 py-2 shadow-sm',
        runNodeBorder(data.status),
      )}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!bg-border-strong"
      />
      <code className="block max-w-40 truncate font-mono text-xs">
        {data.ref}
      </code>
      <Badge className="mt-1" variant={stepStatusVariant(data.status)}>
        {t(`run.status.${data.status}`)}
      </Badge>
      <Handle
        type="source"
        position={Position.Right}
        className="!bg-border-strong"
      />
    </div>
  )
}

const runNodeTypes = { runStep: RunNode }

function RunGraph({ steps }: { steps: WorkflowRunStep[] }) {
  const { t } = useTranslation('automations-workflows')
  const isDark = useIsDark()
  const { nodes, edges } = useMemo(() => runGraphElements(steps), [steps])
  return (
    <div
      role="group"
      aria-label={t('run.graph')}
      className="h-64 overflow-hidden rounded-lg border border-border bg-surface"
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={runNodeTypes}
        colorMode={isDark ? 'dark' : 'light'}
        fitView
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable
        panOnScroll
        proOptions={{ hideAttribution: false }}
      >
        <Background variant={BackgroundVariant.Dots} gap={22} size={1} />
        <Controls
          showInteractive={false}
          className="!shadow-md [&_button]:!min-h-6 [&_button]:!min-w-6 [&_button]:!border-border [&_button]:!bg-surface"
        />
        <MiniMap
          pannable
          zoomable
          ariaLabel={t('run.graph')}
          nodeColor={(node) =>
            runNodeColor(node.data.status as WorkflowRunStepStatus)
          }
          className="!bg-elevated"
        />
      </ReactFlow>
    </div>
  )
}

function runGraphElements(steps: WorkflowRunStep[]): {
  nodes: Node<RunNodeData>[]
  edges: Edge[]
} {
  const depths = graphDepths(steps)
  const edges = steps.flatMap((step) =>
    step.depends_on.map((dependency) => ({
      id: `${dependency}-${step.ref}`,
      source: dependency,
      target: step.ref,
    })),
  )
  const layout = layeredLayout(
    steps.map((step) => ({ id: step.ref, layer: depths.get(step.ref) ?? 0 })),
    edges,
    { layerGapX: 230, nodeGapY: 100 },
  )
  return {
    nodes: steps.map((step) => ({
      id: step.ref,
      type: 'runStep',
      position: layout.positions[step.ref] ?? { x: 0, y: 0 },
      data: { ref: step.ref, status: step.status },
    })),
    edges,
  }
}

function graphDepths(steps: WorkflowRunStep[]): Map<string, number> {
  const depths = new Map<string, number>()
  const byRef = new Map(steps.map((step) => [step.ref, step]))
  const unresolved = new Set(steps.map((step) => step.ref))
  while (unresolved.size > 0) {
    let progressed = false
    for (const ref of [...unresolved].sort()) {
      const step = byRef.get(ref)!
      const knownDependencies = step.depends_on.filter((dep) => byRef.has(dep))
      if (knownDependencies.every((dep) => depths.has(dep))) {
        depths.set(
          ref,
          knownDependencies.length === 0
            ? 0
            : Math.max(...knownDependencies.map((dep) => depths.get(dep)!)) + 1,
        )
        unresolved.delete(ref)
        progressed = true
      }
    }
    if (!progressed) {
      for (const ref of unresolved) depths.set(ref, 0)
      break
    }
  }
  return depths
}

function runStatusVariant(status: WorkflowRun['status']): BadgeVariant {
  if (status === 'completed') return 'success'
  if (status === 'failed') return 'danger'
  return 'warning'
}

function stepStatusVariant(status: WorkflowRunStepStatus): BadgeVariant {
  if (
    [
      'dispatched',
      'declared',
      'emitted',
      'notified',
      'done',
      'gate_passed',
    ].includes(status)
  )
    return 'success'
  if (['blocked', 'budget_blocked', 'failed'].includes(status)) return 'danger'
  if (['executing', 'waiting', 'waiting_approval'].includes(status))
    return 'warning'
  return 'neutral'
}

function runNodeBorder(status: WorkflowRunStepStatus): string {
  const variant = stepStatusVariant(status)
  if (variant === 'success') return 'border-success-line'
  if (variant === 'danger') return 'border-danger-line'
  if (variant === 'warning') return 'border-warning-line'
  return 'border-border'
}

function runNodeColor(status: WorkflowRunStepStatus): string {
  const variant = stepStatusVariant(status)
  if (variant === 'success') return 'var(--color-success)'
  if (variant === 'danger') return 'var(--color-danger)'
  if (variant === 'warning') return 'var(--color-warning)'
  return 'var(--color-graphite-400)'
}
