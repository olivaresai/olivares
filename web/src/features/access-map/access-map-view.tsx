// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Panel } from '@xyflow/react'
import { useQuery } from '@tanstack/react-query'
import { Eye, Network, RefreshCw, ShieldCheck } from 'lucide-react'
import { useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { RecordingNotice } from '@/features/recordings/recording-notice'
import { clusterByHost, GraphCanvas, SigmaGraph } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { cn } from '@/lib/utils'
import { accessMapApi, accessMapKeys } from './api'
import {
  DRIFT_PERMISSION,
  DRIFT_UNREAD,
  type DriftLookup,
  driftRead,
  findDriftEntry,
  recheckVerdict,
} from './authority'
import { AccessDetailSheet, type RecheckState, type Selection } from './detail'
import { DriftList } from './drift-list'
import { accessEdgeTypes } from './edges'
import { AccessFilters } from './filters'
import {
  type AccessFilterState,
  buildGraph,
  distinctSignalSources,
  emptyFilter,
} from './graph-model'
import { AccessLegend } from './legend'
import { accessNodeTypes } from './nodes'
import { selectionFromNodeData } from './selection'
import './i18n'
import type { DriftEntry, GraphResponse } from './types'
import { LEGIBLE_FIT_MIN_ZOOM } from '@/features/shared/graph/graph-canvas'

const GRAPH_LIMIT = 500

/**
 * Above this node count the polished React Flow view (per-node DOM + SVG edges)
 * gets sluggish, so we switch to the Sigma.js WebGL renderer, which draws the SAME
 * built model on the GPU. 400 is a conservative ceiling for smooth DOM rendering
 * of the access graph; below it the operator keeps the fully-detailed view (icons,
 * coverage badges, danger rings). The WebGL path renders the identical, already-
 * redacted contract — it adds NO data, only a scalable presentation.
 */
const WEBGL_NODE_THRESHOLD = 400

/**
 * At extreme scale even WebGL benefits from level-of-detail: above this we collapse
 * resources by host into labelled supernodes (clusterByHost) — honest aggregation
 * that never hides a finding (any group with unexpected access stays flagged).
 */
const WEBGL_CLUSTER_THRESHOLD = 1200

/** Node fill (a CSS-var token string) shared by the minimap and the WebGL renderer:
 * danger for a resource with unexpected access, accent for origins, graphite for
 * plain resources — the same encoding the React Flow nodes use. */
function accessNodeColor(node: { data: unknown }): string {
  const d = node.data as { hasUnexpected?: boolean; role?: string }
  if (d.hasUnexpected) return 'var(--color-danger)'
  return d.role === 'origin'
    ? 'var(--color-accent-text)'
    : 'var(--color-graphite-400)'
}

/** Merge a neighbors expansion into the base graph, deduping by node/edge id. */
function mergeGraphs(
  base: GraphResponse | undefined,
  extra: GraphResponse | null,
): GraphResponse {
  if (!base) return { nodes: [], edges: [], has_more: false }
  if (!extra) return base
  const nodes = new Map(base.nodes.map((n) => [n.id, n]))
  for (const n of extra.nodes) if (!nodes.has(n.id)) nodes.set(n.id, n)
  const edges = new Map(base.edges.map((e) => [e.id, e]))
  for (const e of extra.edges) if (!edges.has(e.id)) edges.set(e.id, e)
  return {
    nodes: [...nodes.values()],
    edges: [...edges.values()],
    has_more: base.has_more,
  }
}

export function AccessMapView() {
  const { t } = useTranslation('accessMap')
  const { activeTenant, can } = useAuth()
  const canDrift = can(DRIFT_PERMISSION)

  // Deep-link seam: the NHI roster links here with ?focus=<external_id> so an
  // operator can jump from an identity straight to its PERMITTED-vs-OBSERVED edges.
  // external_id == AccessEdge.origin_id/ref and filter.search matches over the refs,
  // so seeding search focuses the graph on that identity. No route/shell change.
  const [filter, setFilter] = useState<AccessFilterState>(() => {
    const base = emptyFilter()
    if (typeof window === 'undefined') return base
    const focus = new URLSearchParams(window.location.search).get('focus')
    return focus ? { ...base, search: focus } : base
  })
  const [overlay, setOverlay] = useState(false)
  const [selection, setSelection] = useState<Selection>(null)
  const [extra, setExtra] = useState<GraphResponse | null>(null)
  const [expandFailure, setExpandFailure] = useState<{
    id: string
    kind: string
    error: unknown
  } | null>(null)
  // Bumped on every tenant switch (below). Captured before any await that will later WRITE
  // state, so a completion from a previous context is dropped instead of landing.
  const generation = useRef(0)

  const graphQuery = useQuery({
    queryKey: accessMapKeys.graph(activeTenant, { limit: GRAPH_LIMIT }),
    queryFn: () => accessMapApi.graph({ limit: GRAPH_LIMIT }),
  })

  const driftQuery = useQuery({
    queryKey: accessMapKeys.drift(activeTenant),
    queryFn: () => accessMapApi.drift({ limit: GRAPH_LIMIT }),
    enabled: overlay && canDrift,
  })

  const merged = useMemo(
    () => mergeGraphs(graphQuery.data, extra),
    [graphQuery.data, extra],
  )
  const signalSources = useMemo(() => distinctSignalSources(merged), [merged])

  const built = useMemo(
    () =>
      buildGraph({
        graph: merged,
        // isSuccess here for the SAME reason the sheet needs it: react-query keeps the last
        // good data after a failed refetch, so without it the canvas would keep colouring
        // edges and nodes from findings it can no longer confirm while the sheet — which
        // does check — has already degraded to "not read". Two surfaces disagreeing about
        // whether the estate is in drift is the defect, whichever one is right.
        diff: driftQuery.isSuccess ? (driftQuery.data ?? null) : null,
        overlay: overlay && canDrift,
        filter,
      }),
    [merged, driftQuery.data, driftQuery.isSuccess, overlay, canDrift, filter],
  )

  // Pick the renderer by node count. The WebGL view draws the SAME built model;
  // above an extreme ceiling we cluster resources by host first (LOD). The cluster
  // transform is honest aggregation (labelled "host (N)", never hides a finding).
  const useWebGL = built.nodes.length > WEBGL_NODE_THRESHOLD
  const render = useMemo(() => {
    if (!useWebGL)
      return { nodes: built.nodes, edges: built.edges, clustered: false }
    return clusterByHost(built.nodes, built.edges, {
      threshold: WEBGL_CLUSTER_THRESHOLD,
    })
  }, [useWebGL, built.nodes, built.edges])

  const onExpand = async (id: string, kind: string) => {
    // Same race as the re-check, and a WORSE outcome: this one writes graph state. A
    // neighbours call started in tenant A closes over A's `merged`; the switch to B clears
    // `extra`, so the late continuation finds `prev === null` and merges A's WHOLE captured
    // graph plus A's response into B — and then clears B's selection on top. The recheck
    // guard does not cover it because it guards a different piece of state. Found by the
    // second the model contrast, which is right that "closed for settled state and for
    // recheck" is not "closed for every async write of that state".
    const askedIn = generation.current
    setExpandFailure(null)
    try {
      const res = await accessMapApi.neighbors(id, 'both', kind)
      if (askedIn !== generation.current) return
      setExtra((prev) => mergeGraphs(prev ?? merged, res))
      setSelection(null)
    } catch (error) {
      if (askedIn !== generation.current) return
      // Neighbors is privileged/audited too. Preserve the graph, but do not
      // convert a failed read into a silently successful no-op.
      setExpandFailure({ id, kind, error })
    }
  }

  const selectEdge = (id: string) => {
    const e = merged.edges.find((x) => x.id === id)
    if (e) setSelection({ type: 'edge', edge: e })
  }
  const selectDrift = (entry: DriftEntry) =>
    setSelection({ type: 'edge', edge: entry.edge })

  // Step 4 — VERIFY BY RE-ASKING. The button re-fetches /drift and reads the
  // answer out of the FRESH response, never out of the cached one that produced the
  // finding. Three verdicts, not two: a refetch that fails is `unknown` ("could not
  // look"), which must never be shown as `clear`.
  const [recheck, setRecheck] = useState<RecheckState>({ status: 'idle' })

  // TENANT SWITCH DROPS EVERYTHING THAT NAMES AN EDGE. The queries are keyed by tenant, but
  // selection / expansion / verdict were tenantless local state, and the tenant switcher
  // does not remount this route. Selecting a drift edge in tenant A, switching to B and
  // re-checking would read B's diff, not find A's edge, and report A's finding CLEARED —
  // graded against an estate it never looked at. Found by the the model contrast.
  //
  // ...AND A RE-CHECK ALREADY IN FLIGHT IS NOT UNDONE BY DROPPING THE STATE. Resetting
  // cannot reach a promise that is already awaiting: its continuation runs afterwards and
  // writes a verdict for the edge it closed over. `generation` is bumped on every switch and
  // captured at request time, so a completion from a previous context is discarded instead of
  // landing. It is a GENERATION and not the tenant id on purpose — comparing tenants passes a
  // check issued in A, resolved while the operator was in B, and applied after they came back
  // to A, which is the same lie with more steps.
  const [seenTenant, setSeenTenant] = useState(activeTenant)
  if (seenTenant !== activeTenant) {
    setSeenTenant(activeTenant)
    generation.current += 1
    setSelection(null)
    setExtra(null)
    setExpandFailure(null)
    setRecheck({ status: 'idle' })
  }
  const onRecheck = async (edgeId: string) => {
    setRecheck({ status: 'checking', edgeId })
    // The context this check was ASKED in. A completion from an older one is dropped, not
    // rendered: it was measured against an estate the sheet is no longer showing.
    const askedIn = generation.current
    try {
      const res = await driftQuery.refetch()
      if (askedIn !== generation.current) return
      // isSuccess is load-bearing: react-query hands back the PREVIOUS data on a
      // failed refetch, so trusting res.data alone would grade a failed check against
      // stale findings and could report "clear" without having measured anything.
      setRecheck({
        status: res.isSuccess
          ? recheckVerdict(res.data ?? null, edgeId)
          : 'unknown',
        edgeId,
      })
    } catch {
      if (askedIn !== generation.current) return
      setRecheck({ status: 'unknown', edgeId })
    }
  }

  // WHAT IS KNOWN ABOUT THE SELECTED EDGE'S DRIFT STATE — three answers, not two.
  //
  // /drift is a separate permissioned, audited read that only runs while the overlay is
  // open, so most selections have never asked. Passing `null` for those said "I looked and
  // this edge is not pending", and the sheet then called an edge the engine might hold
  // reconciliation_pending a firm finding, with remedies. `unread` is now its own answer.
  //
  // isSuccess, not `data !== undefined`: react-query keeps the last good data on a failed
  // refetch (status flips to 'error'), and a diff we can no longer confirm is not one we
  // may reason from. The conservative direction of that error is to stop explaining.
  const driftLookup: DriftLookup =
    selection?.type === 'edge' && canDrift && driftQuery.isSuccess
      ? driftRead(
          findDriftEntry(driftQuery.data ?? null, selection.edge.id),
          Boolean(driftQuery.data?.truncated),
        )
      : DRIFT_UNREAD
  const selectedDrift = driftLookup.status === 'read' ? driftLookup.entry : null

  const isLoading = graphQuery.isLoading
  const error = graphQuery.error
  // "This estate has no access edges" is a CLAIM, and it must not be made out of a read that
  // failed. Unused grants enter the model only from the diff (graph-model.ts), so when they
  // were the only thing on screen the isSuccess gate added above emptied the canvas on a failed
  // refetch — and the empty branch replaces the whole body, hiding even the drift panel's own
  // retry. That turns "I could not revalidate" into "there is nothing here", which is the exact
  // substitution this session exists to stop. Found by the third the model pass, in my own fix.
  // isLoading as well as isError: during the FIRST drift read on an otherwise empty graph the
  // body would flash "no access observed yet" before the diff that populates it arrives — the
  // same false claim, just briefly. "Not yet answered" is not "nothing here" either.
  const driftUnreadable =
    overlay && canDrift && (driftQuery.isError || driftQuery.isLoading)
  const isEmpty =
    !isLoading && !error && built.nodes.length === 0 && !driftUnreadable

  return (
    <div className="flex h-full flex-col gap-4">
      {/* Header */}
      <PageHeader
        icon={Network}
        title={t('title')}
        description={
          <span className="space-y-1">
            <span className="block max-w-2xl">{t('subtitle')}</span>
            <span className="flex items-center gap-1.5 text-xs">
              <ShieldCheck className="size-3.5 shrink-0 text-confidence-attributed" />
              {t('auditedNote')}
            </span>
          </span>
        }
        actions={
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setExtra(null)
              setExpandFailure(null)
              void graphQuery.refetch()
              if (overlay) void driftQuery.refetch()
            }}
            disabled={graphQuery.isFetching}
          >
            <RefreshCw
              className={cn(
                'size-3.5',
                graphQuery.isFetching && 'animate-spin',
              )}
            />
            {t('refresh')}
          </Button>
        }
      />

      <RecordingNotice namespace="accessmap" />

      {/* Stat strip */}
      {!isLoading && !error && (
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <StatChip label={t('stats.origins')} value={built.stats.origins} />
          <StatChip
            label={t('stats.resources')}
            value={built.stats.resources}
          />
          <StatChip
            label={t('stats.read')}
            value={built.stats.read}
            dot="bg-info"
          />
          <StatChip
            label={t('stats.write')}
            value={built.stats.write}
            dot="bg-accent-text"
          />
          {built.stats.approximate > 0 && (
            <StatChip
              label={t('stats.approximate')}
              value={built.stats.approximate}
              dot="bg-confidence-approximate"
            />
          )}
          {overlay && canDrift && built.stats.unexpected > 0 && (
            <StatChip
              label={t('stats.unexpected')}
              value={built.stats.unexpected}
              dot="bg-danger"
              tone="danger"
            />
          )}
          {/* ⛔ NO CONVERTIDO A ListTruncationBadge A PROPOSITO: `merged` es un useMemo derivado,
              NO un objeto de consulta -- no tiene .error, que es la mitad de la regla que el
              componente aplica. Pasarselo compilaria y mentiria: el aviso dependeria solo de
              has_more. Atarlo bien pide decidir DE QUE consulta hereda el error. */}
          {merged.has_more && (
            <Badge variant="warning" title={t('stats.truncatedHint')}>
              {t('stats.truncated', { n: GRAPH_LIMIT })}
            </Badge>
          )}
          {/* Make the renderer degradation visible/documented in-UI. */}
          {useWebGL && (
            <Badge
              variant="info"
              title={
                render.clustered
                  ? t('webgl.clustered')
                  : t('webgl.hint', { n: built.nodes.length })
              }
            >
              {t('webgl.badge')}
            </Badge>
          )}
        </div>
      )}

      {/* Filters */}
      {!isLoading && !error && (
        <AccessFilters
          filter={filter}
          onChange={setFilter}
          signalSources={signalSources}
          overlay={overlay}
          onOverlayChange={setOverlay}
          unexpectedCount={driftQuery.data?.unexpected_count}
          canDrift={canDrift}
        />
      )}

      {/* Body */}
      {isLoading ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-border bg-surface">
          <Spinner />
        </div>
      ) : error ? (
        /* ⛔ ASEGURAMIENTO ANTES QUE ROL — el mismo orden que la costura
           (_intel/async.tsx:56-63). `isForbidden` es SÓLO el status 403
           (lib/api/errors.ts:59), así que un `step_up_required` lo satisface
           también: leerlo primero borraba la pantalla entera y dejaba como único
           control un refresh que devuelve el mismo 403 para siempre. */
        error instanceof ApiError && error.isStepUpRequired ? (
          <StepUpRequiredState
            action="generic"
            onElevated={() => void graphQuery.refetch()}
          />
        ) : error instanceof ApiError && error.isForbidden ? (
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        ) : (
          <ErrorState retry={() => void graphQuery.refetch()} />
        )
      ) : isEmpty ? (
        <EmptyState
          icon={<Network />}
          title={t('empty.title')}
          description={t('empty.description')}
        />
      ) : (
        <div className="flex min-h-0 flex-1 flex-col gap-4 lg:flex-row">
          {/* Graph (desktop). React Flow ≤ threshold (the polished view); Sigma
              WebGL above it — SAME built model, same selection/click contract. */}
          <div className="hidden h-[70vh] min-h-[460px] flex-1 md:block">
            {useWebGL ? (
              <SigmaGraph
                nodes={render.nodes}
                edges={render.edges}
                nodeColor={accessNodeColor}
                ariaLabel={t('subtitle')}
                onNodeClick={(id, data) =>
                  setSelection(selectionFromNodeData(id, data))
                }
                onEdgeClick={(id) => selectEdge(id)}
                onPaneClick={() => setSelection(null)}
                fitKey={`${filter.search}|${[...filter.modes].join(',')}|${filter.confidence}|${filter.signalSource}|${overlay}|${render.nodes.length}|${render.clustered}`}
              >
                <div className="pointer-events-none absolute bottom-3 left-3 z-10">
                  <AccessLegend overlay={overlay && canDrift} />
                </div>
              </SigmaGraph>
            ) : (
              <GraphCanvas
                fitMinZoom={LEGIBLE_FIT_MIN_ZOOM}
                nodes={built.nodes}
                edges={built.edges}
                nodeTypes={accessNodeTypes}
                edgeTypes={accessEdgeTypes}
                ariaLabel={t('subtitle')}
                onNodeClick={(n) =>
                  setSelection(selectionFromNodeData(n.id, n.data))
                }
                onEdgeClick={(e) => selectEdge(e.id)}
                onPaneClick={() => setSelection(null)}
                minimapColor={(n) =>
                  (n.data as { hasUnexpected?: boolean }).hasUnexpected
                    ? 'var(--color-danger)'
                    : (n.data as { role?: string }).role === 'origin'
                      ? 'var(--color-accent-text)'
                      : 'var(--color-graphite-400)'
                }
                fitKey={`${filter.search}|${[...filter.modes].join(',')}|${filter.confidence}|${filter.signalSource}|${overlay}|${built.nodes.length}`}
              >
                <Panel position="bottom-left">
                  <AccessLegend overlay={overlay && canDrift} />
                </Panel>
              </GraphCanvas>
            )}
          </div>

          {/* Mobile fallback: the graph needs width; show a dignified summary. */}
          <div className="rounded-lg border border-border bg-surface p-6 text-center md:hidden">
            <Network className="mx-auto mb-2 size-6 text-muted-foreground" />
            <p className="text-sm font-medium text-foreground">
              {t('mobile.title')}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              {t('mobile.hint')}
            </p>
          </div>

          {/* Drift side panel */}
          {overlay && (
            <aside className="w-full shrink-0 overflow-y-auto rounded-lg border border-border bg-surface p-4 lg:w-80">
              <h2 className="mb-3 flex items-center gap-1.5 text-sm font-semibold text-foreground">
                <Eye className="size-4" />
                {t('drift.title')}
              </h2>
              {!canDrift ? (
                <ForbiddenState
                  title={t('drift.forbiddenTitle')}
                  description={t('drift.forbiddenHint')}
                />
              ) : driftQuery.isLoading ? (
                <div className="flex justify-center py-8">
                  <Spinner />
                </div>
              ) : driftQuery.error ? (
                /* Este panel no distinguía NINGUNO de los dos 403: cualquier
                   negativa se pintaba como avería roja con un reintento que no
                   puede funcionar. Ahora ceremonia, luego rol, luego avería.
                   (El ForbiddenState de arriba es la puerta de capacidad
                   `!canDrift`, no una rama de error: ésa no se toca.) */
                driftQuery.error instanceof ApiError &&
                driftQuery.error.isStepUpRequired ? (
                  <StepUpRequiredState
                    action="generic"
                    onElevated={() => void driftQuery.refetch()}
                  />
                ) : driftQuery.error instanceof ApiError &&
                  driftQuery.error.isForbidden ? (
                  <ForbiddenState
                    title={t('drift.forbiddenTitle')}
                    description={t('drift.forbiddenHint')}
                  />
                ) : (
                  <ErrorState retry={() => void driftQuery.refetch()} />
                )
              ) : driftQuery.data ? (
                <DriftList diff={driftQuery.data} onSelect={selectDrift} />
              ) : null}
            </aside>
          )}
        </div>
      )}

      <AccessDetailSheet
        selection={selection}
        onClose={() => {
          setSelection(null)
          setExpandFailure(null)
        }}
        onExpand={onExpand}
        expandError={
          selection?.type === 'node' &&
          expandFailure?.id === selection.id &&
          expandFailure.kind === selection.kind
            ? expandFailure.error
            : null
        }
        drift={driftLookup}
        recheck={recheck}
        // Turning the overlay on is what makes the drift read happen, so it is the honest
        // next step for an edge whose drift state was never read — the action that RESOLVES
        // the uncertainty, offered in place of a remedy for a finding nobody has confirmed.
        // Offered with the overlay closed, and ALSO after a failed read even when it is
        // open: the sheet is a modal dialog, so the panel's own retry sits behind it and the
        // honest "not read" state would otherwise be a step with nowhere to go exactly where
        // the failure happened. NOT offered while a read is merely in flight — a second
        // button cannot make the same call answer differently.
        onCheckDrift={
          canDrift && (!overlay || driftQuery.isError)
            ? () => {
                // Also offered after a FAILED read: the sheet is a modal dialog and the
                // panel's own retry sits behind it, so without this the honest "not read"
                // state was a step with nowhere to go exactly where the failure happened.
                setOverlay(true)
                void driftQuery.refetch()
              }
            : undefined
        }
        canDrift={canDrift}
        // Offered only for an edge the drift set actually contains — on a normal
        // permitted+observed edge a complete diff correctly omits it, so a re-check would
        // have answered "no longer in the drift set" about an edge that was never in it. A
        // verdict on a question nobody asked is the same lie as a fix nobody measured.
        //
        // ...OR for an edge this session has already graded: a successful clear REMOVES the
        // edge from the drift set, so gating on the live lookup alone made the whole section
        // — verdict included — disappear at the moment it had something to say.
        onRecheck={
          canDrift &&
          (selectedDrift !== null ||
            (selection?.type === 'edge' &&
              recheck.edgeId === selection.edge.id))
            ? onRecheck
            : undefined
        }
      />
    </div>
  )
}

function StatChip({
  label,
  value,
  dot,
  tone,
}: {
  label: string
  value: number
  dot?: string
  tone?: 'danger'
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md border border-border bg-surface px-2 py-1',
        tone === 'danger' && 'border-danger-line bg-danger-soft',
      )}
    >
      {dot && <span className={cn('size-2 rounded-full', dot)} aria-hidden />}
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono font-semibold tabular-nums text-foreground">
        {value}
      </span>
    </span>
  )
}
