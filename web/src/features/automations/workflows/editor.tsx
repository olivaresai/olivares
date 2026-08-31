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
  type NodeChange,
  type NodeProps,
} from '@xyflow/react'
import {
  ArrowLeft,
  BellRing,
  CalendarClock,
  CirclePause,
  GitMerge,
  History,
  List,
  Plus,
  Radio,
  Save,
  ShieldCheck,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
import { RevisionsSheet } from '@/features/shared/revisions-sheet'
import { layeredLayout } from '@/features/shared/graph/layout'
import { useIsDark } from '@/features/shared/graph/theme'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { cn } from '@/lib/utils'
import { workflowsApi, workflowsKeys } from './api'
import { EVIDENCE_PAGE } from '../api'
import { ListTruncationBadge } from '@/features/_intel'
import { DryRunPanel } from './dry-run-panel'
import { stepSummary, validationMessage } from './presenters'
import { RunPanel } from './run-panel'
import { TopoList } from './topo-list'
import {
  STEP_KINDS,
  serverGraphErrorStepRef,
  stepRefSchema,
  validateGraphClient,
  type GraphValidationError,
  type WorkflowDetail,
  type WorkflowStep,
  type WorkflowStepKind,
} from './types'
import './i18n'

type ViewMode = 'canvas' | 'list'
type PositionMap = Record<string, { x: number; y: number }>

export interface WorkflowEditorProps {
  workflowId: string
  canWrite: boolean
  canAdmin: boolean
  onBack: () => void
}

export function WorkflowEditor({
  workflowId,
  canWrite,
  canAdmin,
  onBack,
}: WorkflowEditorProps) {
  const { t } = useTranslation('automations-workflows')
  const { activeTenant } = useAuth()
  const workflow = useQuery({
    queryKey: workflowsKeys.detail(activeTenant, workflowId),
    queryFn: () => workflowsApi.detail(workflowId),
  })

  if (workflow.isPending) {
    return (
      <div
        role="status"
        className="flex min-h-64 items-center justify-center gap-2"
      >
        <Spinner />
        <span className="text-sm text-muted-foreground">
          {t('editor.loading')}
        </span>
      </div>
    )
  }
  if (workflow.isError || !workflow.data) {
    return (
      <div
        role="alert"
        className="rounded-lg border border-danger-line bg-danger-soft p-4 text-sm text-danger"
      >
        {t('editor.loadFailed')}
      </div>
    )
  }

  return (
    <LoadedWorkflowEditor
      key={`${workflowId}-${workflow.data.version}`}
      workflowId={workflowId}
      canWrite={canWrite}
      canAdmin={canAdmin}
      onBack={onBack}
      initialWorkflow={workflow.data}
    />
  )
}

function LoadedWorkflowEditor({
  workflowId,
  canWrite,
  canAdmin,
  onBack,
  initialWorkflow,
}: WorkflowEditorProps & { initialWorkflow: WorkflowDetail }) {
  const { t } = useTranslation('automations-workflows')
  const { activeTenant } = useAuth()
  const isDark = useIsDark()
  const [steps, setSteps] = useState<WorkflowStep[]>(initialWorkflow.steps)
  const [positions, setPositions] = useState<PositionMap>(() =>
    initialPositions(initialWorkflow.steps),
  )
  const [selectedRef, setSelectedRef] = useState<string | null>(null)
  const [viewMode, setViewMode] = useState<ViewMode>('canvas')
  const [addOpen, setAddOpen] = useState(false)
  const [historyOpen, setHistoryOpen] = useState(false)
  const [serverError, setServerError] = useState<GraphValidationError | null>(
    null,
  )

  const optionParams = { limit: EVIDENCE_PAGE }
  const schedules = useQuery({
    queryKey: workflowsKeys.schedules(activeTenant),
    queryFn: () => workflowsApi.schedules(optionParams),
  })
  const routes = useQuery({
    queryKey: workflowsKeys.routes(activeTenant),
    queryFn: () => workflowsApi.routes(optionParams),
  })

  const clientErrors = useMemo(() => validateGraphClient(steps), [steps])
  const errors = useMemo(
    () => (serverError ? [...clientErrors, serverError] : clientErrors),
    [clientErrors, serverError],
  )
  const errorByRef = useMemo(() => {
    const result = new Map<string, GraphValidationError[]>()
    for (const error of errors) {
      if (!error.stepRef) continue
      result.set(error.stepRef, [...(result.get(error.stepRef) ?? []), error])
    }
    return result
  }, [errors])

  const save = usePrivilegedMutation<WorkflowStep[], WorkflowDetail>({
    mutationFn: async (nextSteps) => {
      setServerError(null)
      try {
        return await workflowsApi.updateSteps(workflowId, nextSteps)
      } catch (error) {
        setServerError({
          stepRef: serverGraphErrorStepRef(error),
          message:
            error instanceof Error ? error.message : t('editor.loadFailed'),
        })
        throw error
      }
    },
    invalidateKeys: [
      workflowsKeys.list(activeTenant),
      workflowsKeys.detail(activeTenant, workflowId),
      workflowsKeys.revisions(activeTenant, workflowId),
    ],
    successMessage: t('editor.saved'),
    onDone: (data) => {
      setSteps(data.steps)
      setServerError(null)
    },
  })

  const patchEnabled = usePrivilegedMutation<boolean, WorkflowDetail>({
    mutationFn: (enabled) => workflowsApi.patch(workflowId, { enabled }),
    invalidateKeys: [
      workflowsKeys.list(activeTenant),
      workflowsKeys.detail(activeTenant, workflowId),
      workflowsKeys.revisions(activeTenant, workflowId),
    ],
    successMessage: t('editor.metadataSaved'),
  })

  const elements = useMemo(
    () => buildElements(steps, positions, errors, selectedRef),
    [steps, positions, errors, selectedRef],
  )

  function updateSteps(mutator: (current: WorkflowStep[]) => WorkflowStep[]) {
    setSteps((current) => mutator(current))
    setServerError(null)
  }

  function updateStep(ref: string, next: WorkflowStep) {
    updateSteps((current) =>
      current.map((step) => (step.ref === ref ? next : step)),
    )
  }

  function deleteStep(ref: string) {
    updateSteps((current) =>
      current
        .filter((step) => step.ref !== ref)
        .map((step) => ({
          ...step,
          depends_on: step.depends_on.filter(
            (dependency) => dependency !== ref,
          ),
        })),
    )
    setPositions((current) => {
      const next = { ...current }
      delete next[ref]
      return next
    })
    if (selectedRef === ref) setSelectedRef(null)
  }

  function toggleDependency(
    stepRef: string,
    dependencyRef: string,
    checked: boolean,
  ) {
    updateSteps((current) =>
      current.map((step) => {
        if (step.ref !== stepRef) return step
        const dependencies = checked
          ? [...new Set([...step.depends_on, dependencyRef])]
          : step.depends_on.filter((dependency) => dependency !== dependencyRef)
        return { ...step, depends_on: dependencies }
      }),
    )
  }

  function addStep(step: WorkflowStep) {
    updateSteps((current) => [...current, step])
    const nextSteps = [...steps, step]
    const derived = initialPositions(nextSteps)
    setPositions((current) => ({
      ...derived,
      ...current,
      [step.ref]: derived[step.ref] ?? { x: 0, y: 0 },
    }))
    setSelectedRef(step.ref)
    setAddOpen(false)
  }

  function onNodeChanges(changes: NodeChange<Node<WorkflowNodeData>>[]) {
    for (const change of changes) {
      if (change.type === 'position' && change.position) {
        setPositions((current) => ({
          ...current,
          [change.id]: change.position!,
        }))
      }
      if (change.type === 'select' && change.selected) setSelectedRef(change.id)
    }
  }

  const selectedStep = steps.find((step) => step.ref === selectedRef)

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="ghost" size="sm" onClick={onBack}>
          <ArrowLeft aria-hidden />
          {t('editor.back')}
        </Button>
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-lg font-semibold text-foreground">
            {initialWorkflow.name}
          </h2>
          {initialWorkflow.description ? (
            <p className="truncate text-sm text-muted-foreground">
              {initialWorkflow.description}
            </p>
          ) : null}
        </div>
        <Field
          label={t('editor.enable')}
          description={t('editor.enableHint')}
          className="mr-2"
        >
          <Switch
            checked={initialWorkflow.enabled}
            disabled={!canWrite || patchEnabled.isPending}
            onCheckedChange={(checked) => patchEnabled.mutate(checked)}
          />
        </Field>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <div className="flex rounded-md border border-border-strong bg-surface p-0.5">
          <Button
            size="sm"
            variant={viewMode === 'canvas' ? 'secondary' : 'ghost'}
            aria-pressed={viewMode === 'canvas'}
            onClick={() => setViewMode('canvas')}
          >
            <GitMerge aria-hidden />
            {t('editor.canvas')}
          </Button>
          <Button
            size="sm"
            variant={viewMode === 'list' ? 'secondary' : 'ghost'}
            aria-pressed={viewMode === 'list'}
            onClick={() => setViewMode('list')}
          >
            <List aria-hidden />
            {t('editor.list')}
          </Button>
        </div>
        <div className="flex-1" />
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setHistoryOpen(true)}
        >
          <History aria-hidden />
          {t('editor.history')}
        </Button>
        <DryRunPanel workflowId={workflowId} />
        <RunPanel workflowId={workflowId} canAdmin={canAdmin} />
        {canWrite ? (
          <>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setAddOpen(true)}
            >
              <Plus aria-hidden />
              {t('editor.add')}
            </Button>
            <Button
              variant="primary"
              size="sm"
              disabled={clientErrors.length > 0 || save.isPending}
              onClick={() => save.mutate(steps)}
            >
              <Save aria-hidden />
              {t('editor.save')}
            </Button>
          </>
        ) : null}
      </div>

      {clientErrors.length > 0 ? (
        <p
          role="alert"
          className="rounded-md border border-danger-line bg-danger-soft p-2 text-sm text-danger"
        >
          {t('editor.validationSummary', { count: clientErrors.length })}
        </p>
      ) : null}
      {serverError ? (
        <p
          role="alert"
          className="rounded-md border border-danger-line bg-danger-soft p-2 text-sm text-danger"
        >
          {t('editor.serverError', { message: serverError.message })}
        </p>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
        {viewMode === 'canvas' ? (
          <div
            role="group"
            aria-label={t('editor.canvasLabel', { name: initialWorkflow.name })}
            className="h-[600px] overflow-hidden rounded-lg border border-border bg-surface"
          >
            <ReactFlow
              nodes={elements.nodes}
              edges={elements.edges}
              nodeTypes={workflowNodeTypes}
              colorMode={isDark ? 'dark' : 'light'}
              fitView
              fitViewOptions={{ padding: 0.18 }}
              minZoom={0.1}
              maxZoom={2.5}
              nodesDraggable={canWrite}
              nodesConnectable={canWrite}
              elementsSelectable
              panOnScroll
              selectionOnDrag={false}
              deleteKeyCode={canWrite ? ['Backspace', 'Delete'] : null}
              proOptions={{ hideAttribution: false }}
              onNodesChange={onNodeChanges}
              onNodeClick={(_, node) => setSelectedRef(node.id)}
              onPaneClick={() => setSelectedRef(null)}
              onConnect={(connection) => {
                if (connection.source && connection.target) {
                  toggleDependency(connection.target, connection.source, true)
                }
              }}
              onEdgesDelete={(deleted) => {
                for (const edge of deleted) {
                  toggleDependency(edge.target, edge.source, false)
                }
              }}
              onNodesDelete={(deleted) => {
                for (const node of deleted) deleteStep(node.id)
              }}
            >
              <Background
                variant={BackgroundVariant.Dots}
                gap={22}
                size={1}
                className="text-border-strong"
              />
              <Controls
                showInteractive={false}
                className="!shadow-md [&_button]:!min-h-6 [&_button]:!min-w-6 [&_button]:!border-border [&_button]:!bg-surface [&_button]:!text-muted-foreground hover:[&_button]:!bg-muted"
              />
              <MiniMap
                pannable
                zoomable
                ariaLabel={t('editor.canvasLabel', {
                  name: initialWorkflow.name,
                })}
                nodeColor={(node) =>
                  (node.data.errorCount as number) > 0
                    ? 'var(--color-danger)'
                    : 'var(--color-accent-text)'
                }
                maskColor="color-mix(in oklab, var(--overlay) 40%, transparent)"
                className="!bg-elevated"
              />
            </ReactFlow>
          </div>
        ) : (
          <TopoList
            steps={steps}
            errors={errors}
            selectedRef={selectedRef ?? undefined}
            canWrite={canWrite}
            onAdd={() => setAddOpen(true)}
            onSelect={setSelectedRef}
            onDelete={deleteStep}
            onToggleDependency={toggleDependency}
          />
        )}

        <aside className="rounded-lg border border-border bg-elevated p-4">
          {/* ⛔ AQUÍ EL RECORTE NO ES UNA CIFRA MAL, ES UN DESPLEGABLE INCOMPLETO. Las dos listas
              alimentan los selectores de paso: si el motor recortó, faltan horarios y rutas que SÍ
              existen y el operador no puede elegir lo que no ve — y nada se lo diría. Van a los
              mismos handlers con `listQuery(r)` que los raíles de la pestaña. */}
          <ListTruncationBadge
            query={schedules}
            label={t('editor.schedulesTruncated')}
            hint={t('editor.optionsTruncatedHint')}
          />
          <ListTruncationBadge
            query={routes}
            label={t('editor.routesTruncated')}
            hint={t('editor.optionsTruncatedHint')}
          />
          {selectedStep ? (
            <StepEditor
              step={selectedStep}
              allSteps={steps}
              errors={errorByRef.get(selectedStep.ref) ?? []}
              schedules={schedules.data?.items ?? []}
              routes={routes.data?.items ?? []}
              disabled={!canWrite}
              onChange={(next) => updateStep(selectedStep.ref, next)}
              onDelete={() => deleteStep(selectedStep.ref)}
              onToggleDependency={(dependency, checked) =>
                toggleDependency(selectedStep.ref, dependency, checked)
              }
            />
          ) : (
            <p className="text-sm text-muted-foreground">
              {t('editor.selectHint')}
            </p>
          )}
        </aside>
      </div>

      {addOpen ? (
        <AddStepDialog
          open
          onOpenChange={setAddOpen}
          existingRefs={steps.map((step) => step.ref)}
          schedules={schedules.data?.items ?? []}
          routes={routes.data?.items ?? []}
          onAdd={addStep}
        />
      ) : null}

      <RevisionsSheet
        open={historyOpen}
        onOpenChange={setHistoryOpen}
        queryKey={workflowsKeys.revisions(activeTenant, workflowId)}
        listRevisions={(params) => workflowsApi.revisions(workflowId, params)}
        restoreRevision={(revisionId) =>
          workflowsApi.restore(workflowId, revisionId)
        }
        invalidateKeys={[
          workflowsKeys.list(activeTenant),
          workflowsKeys.detail(activeTenant, workflowId),
        ]}
        canWrite={canWrite}
        labels={{
          title: t('revisions.title', { name: initialWorkflow.name }),
          description: t('revisions.description'),
          empty: t('revisions.empty'),
          loading: t('revisions.loading'),
          loadMore: t('revisions.loadMore'),
          compareTitle: t('revisions.compare'),
          selectTwo: t('revisions.selectTwo'),
          selectRevision: (operation, actor) =>
            t('revisions.select', { operation, actor }),
          originalLabel: t('revisions.original'),
          modifiedLabel: t('revisions.modified'),
          restore: t('revisions.restore'),
          restoreTitle: t('revisions.restoreTitle'),
          restoreDescription: t('revisions.restoreDescription'),
          restoreConfirm: t('revisions.restoreConfirm'),
          restoreSuccess: t('revisions.restoreSuccess'),
          operations: {
            create: t('revisions.operations.create'),
            update: t('revisions.operations.update'),
            delete: t('revisions.operations.delete'),
            restore: t('revisions.operations.restore'),
          },
        }}
      />
    </div>
  )
}

interface WorkflowNodeData extends Record<string, unknown> {
  step: WorkflowStep
  errorCount: number
  firstError?: string
}

const KIND_ICON: Record<WorkflowStepKind, LucideIcon> = {
  'schedule-fire': CalendarClock,
  'eventing-emit': Radio,
  'notify-test': BellRing,
  wait: CirclePause,
  'approval-gate': ShieldCheck,
}

function WorkflowNode({ data, selected }: NodeProps<Node<WorkflowNodeData>>) {
  const { t } = useTranslation('automations-workflows')
  const Icon = KIND_ICON[data.step.kind]
  const invalid = data.errorCount > 0
  return (
    <div
      //the selected node used to be identified by --accent-line plus a 30%
      // accent glow: colour only, and both below the SC 1.4.11 3:1 floor. The border
      // now carries --accent-strong (>=3:1, gated by at-run.ts), the rail below adds
      // a shape, and aria-current states it for AT. The glow stays as decoration.
      // The rail and aria-current are deliberately INDEPENDENT of `invalid`: an
      // invalid node keeps its danger border, so gating them on !invalid would leave
      // a node that is selected AND invalid with no selection indicator at all — not
      // colour, not shape, not the a11y tree — while its properties panel is open.
      // topo-list.tsx does the same, so the feature's two surfaces agree.
      aria-current={selected ? 'true' : undefined}
      className={cn(
        'min-w-52 rounded-lg border bg-surface px-3 py-2 shadow-sm',
        invalid
          ? 'border-danger ring-2 ring-danger/30'
          : selected
            ? 'border-accent-strong ring-2 ring-accent/30'
            : 'border-border',
      )}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="!size-2 !bg-accent-text"
      />
      <div className="flex items-start gap-2">
        <span
          aria-hidden
          className={cn(
            'h-8 w-1 shrink-0 rounded-full',
            selected ? 'bg-accent-strong' : 'bg-transparent',
          )}
        />
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-accent-soft text-accent-soft-foreground">
          <Icon className="size-4" aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <code className="block truncate font-mono text-xs text-foreground">
            {data.step.ref}
          </code>
          <p className="text-[11px] text-muted-foreground">
            {t(`kind.${data.step.kind}`)}
          </p>
          <p className="mt-1 max-w-44 truncate text-xs text-muted-foreground">
            {stepSummary(data.step, t)}
          </p>
        </div>
        {invalid ? (
          <Badge
            variant="danger"
            title={
              data.firstError
                ? validationMessage(data.firstError, t)
                : undefined
            }
            aria-label={t('editor.nodeError', { ref: data.step.ref })}
          >
            <TriangleAlert aria-hidden />
            {data.errorCount}
          </Badge>
        ) : null}
      </div>
      <Handle
        type="source"
        position={Position.Right}
        className="!size-2 !bg-accent-text"
      />
    </div>
  )
}

const workflowNodeTypes = { workflowStep: WorkflowNode }

function buildElements(
  steps: WorkflowStep[],
  positions: PositionMap,
  errors: GraphValidationError[],
  selectedRef: string | null,
): { nodes: Node<WorkflowNodeData>[]; edges: Edge[] } {
  const errorsByRef = new Map<string, GraphValidationError[]>()
  for (const error of errors) {
    if (!error.stepRef) continue
    errorsByRef.set(error.stepRef, [
      ...(errorsByRef.get(error.stepRef) ?? []),
      error,
    ])
  }
  const invalidRefs = new Set(
    errors.filter((error) => error.stepRef).map((error) => error.stepRef!),
  )
  return {
    nodes: steps.map((step) => ({
      id: step.ref,
      type: 'workflowStep',
      position: positions[step.ref] ?? { x: 0, y: 0 },
      selected: selectedRef === step.ref,
      data: {
        step,
        errorCount: errorsByRef.get(step.ref)?.length ?? 0,
        firstError: errorsByRef.get(step.ref)?.[0]?.message,
      },
    })),
    edges: steps.flatMap((step) =>
      step.depends_on.map((dependency) => {
        const invalid = invalidRefs.has(dependency) || invalidRefs.has(step.ref)
        return {
          id: `${dependency}-${step.ref}`,
          source: dependency,
          target: step.ref,
          animated: false,
          style: invalid
            ? { stroke: 'var(--color-danger)', strokeWidth: 2 }
            : { stroke: 'var(--color-accent-text)', strokeWidth: 1.5 },
        }
      }),
    ),
  }
}

function initialPositions(steps: WorkflowStep[]): PositionMap {
  const depths = workflowDepths(steps)
  const edges = steps.flatMap((step) =>
    step.depends_on.map((dependency) => ({
      source: dependency,
      target: step.ref,
    })),
  )
  return layeredLayout(
    steps.map((step) => ({ id: step.ref, layer: depths.get(step.ref) ?? 0 })),
    edges,
    { layerGapX: 300, nodeGapY: 120 },
  ).positions
}

function workflowDepths(steps: WorkflowStep[]): Map<string, number> {
  const depths = new Map<string, number>()
  const byRef = new Map(steps.map((step) => [step.ref, step]))
  const unresolved = new Set(steps.map((step) => step.ref))
  while (unresolved.size > 0) {
    let progressed = false
    for (const ref of [...unresolved].sort()) {
      const step = byRef.get(ref)!
      const dependencies = step.depends_on.filter((dep) => byRef.has(dep))
      if (dependencies.every((dependency) => depths.has(dependency))) {
        depths.set(
          ref,
          dependencies.length === 0
            ? 0
            : Math.max(
                ...dependencies.map((dependency) => depths.get(dependency)!),
              ) + 1,
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

function StepEditor({
  step,
  allSteps,
  errors,
  schedules,
  routes,
  disabled,
  onChange,
  onDelete,
  onToggleDependency,
}: {
  step: WorkflowStep
  allSteps: WorkflowStep[]
  errors: GraphValidationError[]
  schedules: { id: string; name: string; subject_ref: string }[]
  routes: { id: string; name: string }[]
  disabled: boolean
  onChange: (step: WorkflowStep) => void
  onDelete: () => void
  onToggleDependency: (dependency: string, checked: boolean) => void
}) {
  const { t } = useTranslation('automations-workflows')
  const validDependencies = allSteps.filter(
    (candidate) =>
      candidate.ref !== step.ref &&
      stepRefSchema.safeParse(candidate.ref).success,
  )
  const configErrors = errors.map((error) =>
    validationMessage(error.message, t),
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('config.title')}
        </h3>
        {!disabled ? (
          <Button
            variant="destructive"
            size="sm"
            aria-label={t('editor.deleteNode', { ref: step.ref })}
            onClick={onDelete}
          >
            {t('common:actions.delete')}
          </Button>
        ) : null}
      </div>
      <div>
        <p className="text-xs text-muted-foreground">{t('config.ref')}</p>
        <code className="font-mono text-sm text-foreground">{step.ref}</code>
      </div>
      <Badge variant="outline">{t(`kind.${step.kind}`)}</Badge>
      {step.kind === 'schedule-fire' ? (
        <Field label={t('config.schedule')} error={configErrors[0]} required>
          <Select
            value={step.config.schedule_id || undefined}
            disabled={disabled}
            onValueChange={(scheduleId) =>
              onChange({ ...step, config: { schedule_id: scheduleId } })
            }
          >
            <SelectTrigger>
              <SelectValue placeholder={t('config.noSchedules')} />
            </SelectTrigger>
            <SelectContent>
              {schedules.map((schedule) => (
                <SelectItem key={schedule.id} value={schedule.id}>
                  {schedule.name} · {schedule.subject_ref}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : null}
      {step.kind === 'eventing-emit' ? (
        <Field label={t('config.label')} error={configErrors[0]} required>
          <Input
            value={step.config.label}
            disabled={disabled}
            maxLength={200}
            onChange={(event) =>
              onChange({ ...step, config: { label: event.target.value } })
            }
          />
        </Field>
      ) : null}
      {step.kind === 'notify-test' ? (
        <Field label={t('config.route')} error={configErrors[0]} required>
          <Select
            value={step.config.route_id || undefined}
            disabled={disabled}
            onValueChange={(routeId) =>
              onChange({ ...step, config: { route_id: routeId } })
            }
          >
            <SelectTrigger>
              <SelectValue placeholder={t('config.noRoutes')} />
            </SelectTrigger>
            <SelectContent>
              {routes.map((route) => (
                <SelectItem key={route.id} value={route.id}>
                  {route.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      ) : null}
      {step.kind === 'wait' ? (
        <Field label={t('config.seconds')} error={configErrors[0]} required>
          <Input
            type="number"
            min={1}
            max={86400}
            step={1}
            value={step.config.seconds}
            disabled={disabled}
            onChange={(event) =>
              onChange({
                ...step,
                config: { seconds: Number(event.target.value) },
              })
            }
          />
        </Field>
      ) : null}
      {step.kind === 'approval-gate' ? (
        <Field label={t('config.reason')} error={configErrors[0]}>
          <Input
            value={step.config.reason ?? ''}
            disabled={disabled}
            maxLength={200}
            placeholder={t('config.reasonOptional')}
            onChange={(event) =>
              onChange({
                ...step,
                config: event.target.value
                  ? { reason: event.target.value }
                  : {},
              })
            }
          />
        </Field>
      ) : null}

      <fieldset className="space-y-2 border-t border-border pt-4">
        <legend className="text-sm font-medium text-foreground">
          {t('deps.title')}
        </legend>
        <p className="text-xs text-muted-foreground">{t('deps.description')}</p>
        {validDependencies.length === 0 ? (
          <p className="text-xs text-muted-foreground">{t('deps.none')}</p>
        ) : (
          validDependencies.map((candidate) => {
            const checked = step.depends_on.includes(candidate.ref)
            return (
              <label
                key={candidate.ref}
                className="flex min-h-6 items-center gap-2 text-xs text-foreground"
              >
                <Checkbox
                  checked={checked}
                  disabled={
                    disabled || (!checked && step.depends_on.length >= 8)
                  }
                  aria-label={t('deps.toggle', {
                    step: step.ref,
                    dependency: candidate.ref,
                  })}
                  onCheckedChange={(value) =>
                    onToggleDependency(candidate.ref, value === true)
                  }
                />
                <code>{candidate.ref}</code>
              </label>
            )
          })
        )}
        {step.depends_on.length >= 8 ? (
          <p className="text-xs text-warning">{t('deps.limit')}</p>
        ) : null}
      </fieldset>
    </div>
  )
}

function AddStepDialog({
  open,
  onOpenChange,
  existingRefs,
  schedules,
  routes,
  onAdd,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  existingRefs: string[]
  schedules: { id: string }[]
  routes: { id: string }[]
  onAdd: (step: WorkflowStep) => void
}) {
  const { t } = useTranslation('automations-workflows')
  const [ref, setRef] = useState('')
  const [kind, setKind] = useState<WorkflowStepKind>('wait')
  const refResult = stepRefSchema.safeParse(ref)
  const duplicate = existingRefs.includes(ref)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('add.title')}</DialogTitle>
          <DialogDescription>{t('add.description')}</DialogDescription>
        </DialogHeader>
        <Field
          label={t('add.ref')}
          required
          error={
            ref.length > 0 && !refResult.success
              ? t('validation.refInvalid')
              : duplicate
                ? t('add.duplicate')
                : undefined
          }
        >
          <Input value={ref} onChange={(event) => setRef(event.target.value)} />
        </Field>
        <Field label={t('add.kind')} required>
          <Select
            value={kind}
            onValueChange={(value) => setKind(value as WorkflowStepKind)}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {STEP_KINDS.map((stepKind) => (
                <SelectItem key={stepKind} value={stepKind}>
                  {t(`kind.${stepKind}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={!refResult.success || duplicate}
            onClick={() => onAdd(newStep(ref, kind, schedules, routes))}
          >
            {t('add.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function newStep(
  ref: string,
  kind: WorkflowStepKind,
  schedules: { id: string }[],
  routes: { id: string }[],
): WorkflowStep {
  switch (kind) {
    case 'schedule-fire':
      return {
        ref,
        kind,
        config: { schedule_id: schedules[0]?.id ?? '' },
        depends_on: [],
      }
    case 'eventing-emit':
      return { ref, kind, config: { label: '' }, depends_on: [] }
    case 'notify-test':
      return {
        ref,
        kind,
        config: { route_id: routes[0]?.id ?? '' },
        depends_on: [],
      }
    case 'wait':
      return { ref, kind, config: { seconds: 1 }, depends_on: [] }
    case 'approval-gate':
      return { ref, kind, config: {}, depends_on: [] }
  }
}
