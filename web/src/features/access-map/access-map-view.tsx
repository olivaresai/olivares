// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Panel } from '@xyflow/react'
import {
  type UseQueryResult,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  Eye,
  List,
  Maximize2,
  Minimize2,
  Network,
  RefreshCw,
  X,
} from 'lucide-react'
import {
  type KeyboardEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { CaveatNotice } from '@/features/_intel'
import { PageHeader } from '@/components/ui/page-header'
import { Spinner } from '@/components/ui/spinner'
import { RecordingNoticeSlot } from '@/features/recording-notice-slot'
import {
  GraphCanvas,
  SigmaGraph,
  useSessionNames,
  useUrlStateNotice,
} from '@/features/shared'
import { toolName } from '@/features/agentops/engine-tools'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { useUrlState, useValidatedUrlState } from '@/lib/hooks/use-url-state'
import { useMinWidth } from '@/lib/hooks/use-min-width'
import {
  isTypingTarget,
  modalIsOpen,
  navLeader,
  resolveBinding,
} from '@/lib/keybindings/model'
import { KEYBINDINGS } from '@/lib/keybindings/table'
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
  type OriginNodeData,
  type ResourceNodeData,
  buildGraph,
  distinctSignalSources,
} from './graph-model'
import { type GroupNodeData, groupAccessGraph } from './groups'
import { AccessLegend } from './legend'
import { type ArrowKey, connectedNeighbor } from './neighbor-nav'
import { accessNodeTypes } from './nodes'
import { OriginList } from './origin-list'
import { selectionFromNodeData } from './selection'
import {
  ACCESS_URL_KEYS,
  type ExpandedNode,
  MAX_EXPANDED,
  decodeAccessUrl,
  expandedValue,
  filterPatch,
} from './url-state'
import './i18n'
import type {
  AccessNode,
  DiffResponse,
  DriftEntry,
  GraphResponse,
} from './types'
import { LEGIBLE_FIT_MIN_ZOOM } from '@/features/shared/graph/graph-canvas'
import { StaticTable } from '@/components/data/static-table'

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

/** Initial slot for a grouped phone row; GraphCanvas reflows it after measuring. */
const GROUPED_PHONE_ROW_PX = 96

/** The search param that holds full-screen mode: the state is the address, so a
 * reload or a pasted link opens the same view. */
const FULL_PARAM = 'full'

/** The search param that holds the groups the operator opened, comma-separated. */
const GROUP_PARAM = 'group'
const groupsIn = (value: unknown): string[] =>
  String(value ?? '')
    .split(',')
    .filter(Boolean)
    .map(decodeURIComponent)
const groupsValue = (keys: readonly string[]) =>
  keys.length > 0 ? keys.map(encodeURIComponent).join(',') : undefined

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

/** Merge neighbors expansions into the base graph, deduping by node/edge id. */
function mergeGraphs(
  base: GraphResponse | undefined,
  extras: readonly GraphResponse[],
): GraphResponse {
  if (!base) return { nodes: [], edges: [], has_more: false }
  if (extras.length === 0) return base
  const nodes = new Map(base.nodes.map((n) => [n.id, n]))
  const edges = new Map(base.edges.map((e) => [e.id, e]))
  for (const extra of extras) {
    for (const n of extra.nodes) if (!nodes.has(n.id)) nodes.set(n.id, n)
    for (const e of extra.edges) if (!edges.has(e.id)) edges.set(e.id, e)
  }
  return {
    nodes: [...nodes.values()],
    edges: [...edges.values()],
    has_more: base.has_more,
  }
}

/**
 * What the map shows, and which of the address's expansions it can read: the base graph,
 * the endpoints of the drift set's unused grants (pass `drift` only while the overlay
 * shows them) and the nodes of every expansion already read. The last grows as expansions
 * pass, so the list is walked until nothing changes. Shared by the render and by the
 * overlay switch, so "on this map" has one definition.
 */
function resolveMap(
  graph: GraphResponse | undefined,
  drift: DiffResponse | undefined,
  expanded: readonly ExpandedNode[],
  read: (node: ExpandedNode) => GraphResponse | undefined,
) {
  const onMap = new Map<string, AccessNode>()
  const putOnMap = (n: AccessNode) => {
    if (!onMap.has(n.id)) onMap.set(n.id, n)
  }
  graph?.nodes.forEach(putOnMap)
  for (const d of drift?.unused_grants ?? []) {
    putOnMap({
      id: d.edge.origin_id,
      kind: d.edge.origin_kind,
      ref: d.edge.origin_ref,
    })
    putOnMap({
      id: d.edge.resource_id,
      kind: d.edge.resource_kind ?? 'resource',
      ref: d.edge.resource_ref,
    })
  }
  const readable = expanded.map(() => false)
  for (let changed = true; changed;) {
    changed = false
    expanded.forEach((node, i) => {
      if (readable[i] || onMap.get(node.id)?.kind !== node.kind) return
      readable[i] = true
      changed = true
      read(node)?.nodes.forEach(putOnMap)
    })
  }
  return { onMap, readable }
}

/** One expansion's read. Keyed by id alone: a node's kind never changes. The view reads
 * it for as long as the address names it; an expand click fetches it fresh first. */
const neighborsRead = (tenant: string | null, node: ExpandedNode) => ({
  queryKey: accessMapKeys.neighbors(tenant, node.id, 'both'),
  queryFn: () => accessMapApi.neighbors(node.id, 'both', node.kind),
})

const ARROWS: readonly string[] = [
  'ArrowUp',
  'ArrowDown',
  'ArrowLeft',
  'ArrowRight',
]

const keepResults = (results: UseQueryResult<GraphResponse>[]) => results

export function AccessMapView() {
  const { t } = useTranslation('accessMap')
  const { activeTenant, can } = useAuth()
  const canDrift = can(DRIFT_PERMISSION)
  const wideGraph = useMinWidth(768)

  // The view IS the address (url-state.ts): filters, overlay, selection and expanded
  // neighbors round-trip through the URL, so a pasted link restores the same map.
  // Seed from ?focus=<external_id> still works: the NHI roster links here with it
  // so an operator can jump from an identity straight to its PERMITTED-vs-OBSERVED
  // edges, and `focus` is read as the search until the operator edits it.
  const queryClient = useQueryClient()
  const [view, patchUrl, urlIssues] = useValidatedUrlState(
    ACCESS_URL_KEYS,
    decodeAccessUrl,
  )
  const { filter, overlay } = view
  const expanded = view.expanded
  const addressNode = view.node
  const addressEdge = view.edge
  const latest = useRef({ expanded, node: addressNode })
  useEffect(() => {
    latest.current = { expanded, node: addressNode }
  })
  // What the link asked for that is not on this map (below). The hook clears ITS report on
  // the operator's next deliberate change; this one is held here, so `drive` clears it
  // on the same occasion. The view's own cleanup of the address goes through `patchUrl`.
  const [droppedNotice, setDroppedNotice] = useState<string[]>([])
  const drive: typeof patchUrl = (patch, options) => {
    setDroppedNotice((now) => (now.length > 0 ? [] : now))
    patchUrl(patch, options)
  }
  const setFilter = (next: AccessFilterState) => drive(filterPatch(next))
  const hasMapFilter =
    filter.search !== '' ||
    filter.modes.size > 0 ||
    filter.confidence !== 'all' ||
    filter.signalSource !== null
  const [listOpen, setListOpen] = useState(false)
  // FULL SCREEN IS THE ADDRESS (?full=1), the same hook every other deep link uses, so a
  // reload, a pasted link and Back all land on the view the operator was looking at, and
  // ?focus= is kept. It is a window over the page, not the browser's Fullscreen API: that
  // one shows only its own element, so the detail sheet (a portal) would vanish, and
  // iOS Safari has no such API for a div, which is where the phone view needs it most.
  const [urlState, patchFullUrl] = useUrlState([FULL_PARAM, GROUP_PARAM])
  // The optimistic patch is numeric; the location that follows is a string.
  const full = String(urlState[FULL_PARAM] ?? '') === '1'
  // The address's own (encoded) value: a folder name may hold a comma.
  const openKey = String(urlState[GROUP_PARAM] ?? '')
  const openGroups = groupsIn(openKey)
  // Text is 12 px at zoom 1, so a zoom below 1 would draw it smaller. Zooming out past 1
  // groups every side instead (groupAccessGraph), at a zoom floor of 1: fewer, readable
  // labels, never smaller ones. A phone starts grouped so its first view fits the width.
  const [zoomedOut, setZoomedOut] = useState(!wideGraph)
  const [expandFailure, setExpandFailure] = useState<{
    id: string
    kind: string
    error: unknown
    /** The map is at MAX_EXPANDED: said as a notice, not as a failed read. */
    limit?: boolean
  } | null>(null)
  // Bumped on every tenant identity, in layout, so a completion from a previous
  // context is dropped instead of landing. The bump cannot live in render: that
  // is a ref write during render (react-hooks/refs). Layout runs in the same
  // commit, before paint and before network continuations.
  const generationRef = useRef(0)
  useLayoutEffect(() => {
    generationRef.current += 1
  }, [activeTenant])

  const graphQuery = useQuery({
    queryKey: accessMapKeys.graph(activeTenant, { limit: GRAPH_LIMIT }),
    queryFn: () => accessMapApi.graph({ limit: GRAPH_LIMIT }),
  })

  // `?drift=1` in a pasted link starts this audited read at mount, with no click: the
  // overlay is part of the view the link restores, opening the map already seals a graph
  // read, and the page says both reads are audited. It names no id, so a link cannot make
  // the operator read anything the permission does not already allow.
  const driftQuery = useQuery({
    queryKey: accessMapKeys.drift(activeTenant),
    queryFn: () => accessMapApi.drift({ limit: GRAPH_LIMIT }),
    enabled: overlay && canDrift,
  })

  // The expansions the address names. They are reads of their own, so a link that
  // names them fetches them, and Back (which removes one from the address) shrinks the
  // map to the view the operator had.
  //
  // An expansion is read only for a node this map shows, with the kind the map gives it:
  // a read is audited under the signed-in operator, so a pasted link must not make them
  // seal reads of ids that are not here. "This map shows" is ONE set, what the canvas can
  // draw: the base graph (it stops at GRAPH_LIMIT), the endpoints of the unused grants
  // while the overlay shows them (graph-model.ts adds those edges from the drift read,
  // which is drained; its unexpected accesses are the graph's own edges), and the nodes of every
  // expansion already read. The last grows as expansions pass, so the list is walked
  // until nothing changes: where a node sits in the address does not decide it.
  const shownDrift = overlay && canDrift && driftQuery.isSuccess
  const readCached = (node: ExpandedNode) =>
    queryClient.getQueryData<GraphResponse>(
      neighborsRead(activeTenant, node).queryKey,
    )
  const { onMap, readable } = resolveMap(
    graphQuery.data,
    shownDrift ? driftQuery.data : undefined,
    expanded,
    readCached,
  )
  // Turning the overlay off takes the nodes only it drew off the map, so the expansions on
  // them go in the SAME change: they were the operator's own, and left in the address the
  // gate would drop them later and blame the link.
  const overlayPatch = (on: boolean) => {
    const stillReadable = on
      ? readable
      : resolveMap(graphQuery.data, undefined, expanded, readCached).readable
    // A value the gate already refuses is its business, not this switch's.
    const keep = expanded.filter((_, i) => stillReadable[i] || !readable[i])
    return {
      drift: on ? '1' : undefined,
      ...(keep.length < expanded.length ? { expand: expandedValue(keep) } : {}),
    }
  }
  const setOverlay = (on: boolean) => drive(overlayPatch(on))
  // The list opens first. The graph is the address's `view=graph`, and full screen and the
  // drift overlay are the graph's own, so a link naming either opens it too. Back to the
  // list takes the overlay off in the same change.
  const showGraph = view.graph || full || overlay
  const showList = () => drive({ ...overlayPatch(false), view: undefined })
  const neighborReads = useQueries({
    queries: expanded.map((node, i) => ({
      ...neighborsRead(activeTenant, node),
      staleTime: Infinity,
      enabled: readable[i],
    })),
    combine: keepResults,
  })

  const merged = useMemo(
    () =>
      mergeGraphs(
        graphQuery.data,
        neighborReads.flatMap((r) => (r.data ? [r.data] : [])),
      ),
    [graphQuery.data, neighborReads],
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

  // What the canvas draws: each side within what reads at zoom 1 (groups.ts). Sessions
  // group by the tool that ran them, as the live page names it.
  const sessionNames = useSessionNames()
  // The list groups origins the way the graph does.
  const originGroup = useCallback(
    (d: OriginNodeData) => {
      const engine = d.kind === 'session' ? sessionNames.engineOf(d.ref) : null
      return engine
        ? { key: `tool:${engine}`, label: toolName(engine), ref: engine }
        : {
            key: `origin:${d.kind}`,
            label: t(`groupKinds.${d.kind}`, { defaultValue: d.kind }),
            ref: d.kind,
          }
    },
    [sessionNames, t],
  )
  const render = useMemo(() => {
    const result = groupAccessGraph(built.nodes, built.edges, {
      open: new Set(groupsIn(openKey)),
      force: zoomedOut,
      originGroup,
    })
    if (wideGraph || !result.grouped) return result
    // Two columns cannot fit a phone at the readable zoom. Stack only the coarse
    // view; edges keep their meaning and the detail view still pans horizontally.
    return {
      ...result,
      nodes: result.nodes.map((node, index) => ({
        ...node,
        position: { x: 0, y: index * GROUPED_PHONE_ROW_PX },
      })),
    }
  }, [wideGraph, zoomedOut, openKey, originGroup, built.nodes, built.edges])
  // Pick the renderer by what is drawn: WebGL takes the same nodes past the ceiling.
  const useWebGL = render.nodes.length > WEBGL_NODE_THRESHOLD
  // Strictly below or above the readable zoom: exactly at it, which is where the refit
  // after a switch lands, changes nothing, so the two views cannot flip each other.
  const onViewportZoom = (zoom: number) => {
    if (zoom < LEGIBLE_FIT_MIN_ZOOM) setZoomedOut(true)
    else if (zoom > LEGIBLE_FIT_MIN_ZOOM) setZoomedOut(false)
  }
  const grouped = !useWebGL && render.grouped

  const setFull = useCallback(
    (on: boolean) => {
      // The canvas is mounted again in the other place and refits: start at the
      // readable layout for this width.
      setZoomedOut(!wideGraph)
      // TanStack serializes strings as JSON: a numeric 1 keeps the public URL
      // `?full=1`, not `?full=%221%22`. useUrlState owns the string-only type.
      patchFullUrl({ [FULL_PARAM]: on ? (1 as unknown as string) : undefined })
    },
    [patchFullUrl, wideGraph],
  )

  // THE SELECTION IS READ FROM THE ADDRESS, never held beside it. A node is looked up on
  // what the canvas draws (so unused grants and cluster supernodes are addressable), an
  // edge among the engine's edges and the drift set's (which holds the unused grants).
  // The engine's own ref rides along for the session link: the canvas label falls back
  // to a shortened id, and a link must not be built from that.
  // The drift set is looked up while it holds data, even after a failed re-check: the
  // sheet then says "could not look" about the edge instead of closing under the operator.
  const driftHasData = overlay && canDrift && driftQuery.data !== undefined
  const selectedNode =
    addressNode === null
      ? undefined
      : (render.nodes.find((n) => n.id === addressNode) ??
        built.nodes.find((n) => n.id === addressNode))
  const selectedEdge =
    addressEdge === null
      ? undefined
      : (merged.edges.find((e) => e.id === addressEdge) ??
        (driftHasData
          ? findDriftEntry(driftQuery.data ?? null, addressEdge)?.edge
          : undefined))
  const selection: Selection = selectedEdge
    ? { type: 'edge', edge: selectedEdge }
    : selectedNode
      ? {
          ...selectionFromNodeData(selectedNode.id, selectedNode.data),
          originRef: onMap.get(selectedNode.id)?.ref,
        }
      : null

  // The failed expand belongs to the node whose sheet is open, and is said IN that sheet:
  // it is a modal dialog, so a notice on the page behind it is hidden from assistive
  // technology and, on a phone, from the eye.
  const expandFailureOf =
    selection?.type === 'node' &&
    expandFailure?.id === selection.id &&
    expandFailure.kind === selection.kind
      ? expandFailure
      : null

  // A link can name a node, an edge or an expansion that is not on this map (another
  // tenant, an estate that moved on). Say so, and take it out of the address so a copied
  // link stops carrying it. Only once every read it could be found in has ANSWERED: a
  // read that failed is not an answer, and the notice for it is the retry below.
  const settled =
    graphQuery.isSuccess &&
    neighborReads.every((r, i) => !readable[i] || r.isSuccess) &&
    (!overlay || !canDrift || driftQuery.isSuccess)
  const refusedExpansions = settled && readable.some((ok) => !ok)
  const dropped =
    settled &&
    ((addressNode !== null && !selectedNode) ||
      (addressEdge !== null && !selectedEdge))
  const droppedKeys = [
    ...(dropped ? [addressEdge !== null ? 'edge' : 'node'] : []),
    ...(refusedExpansions ? ['expand'] : []),
  ]
  const [droppedSeen, setDroppedSeen] = useState('')
  const droppedSignature = droppedKeys.join(',')
  if (droppedSignature !== droppedSeen) {
    setDroppedSeen(droppedSignature)
    if (droppedKeys.length > 0) setDroppedNotice(droppedKeys)
  }
  // One array per change, never per render: the notice reads a new array as a new event
  // and says itself again.
  const noticeIssues = useMemo(
    () => [...new Set([...urlIssues, ...droppedNotice])],
    [urlIssues, droppedNotice],
  )
  // What the link asked for and the map could not give: a refused value, a node or edge
  // that is not on this map, an expansion whose read failed.
  useUrlStateNotice(noticeIssues)
  useEffect(() => {
    if (!dropped && !refusedExpansions) return
    patchUrl(
      {
        ...(dropped ? { node: undefined, edge: undefined } : {}),
        ...(refusedExpansions
          ? { expand: expandedValue(expanded.filter((_, i) => readable[i])) }
          : {}),
      },
      { history: 'replace', keepReport: true },
    )
    // `readable` is rebuilt every render; the flags and the address are what change it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [dropped, refusedExpansions, patchUrl])

  const onExpand = async (id: string, kind: string) => {
    // Same race as the re-check, and a WORSE outcome: this one writes the address. A
    // neighbours call started in tenant A must not land its expansion in B's address
    // after the switch, so the completion is dropped when the generation moved.
    const askedIn = generationRef.current
    setExpandFailure(null)
    const node = { id, kind }
    // Expanding a node that is already expanded refreshes its read and keeps its place:
    // moving it to the end would put a node that sits on its read before it, and the
    // gate would then see that node first.
    const nextExpanded = () => {
      const now = latest.current.expanded
      return now.some((e) => e.id === id) ? now : [...now, node]
    }
    if (nextExpanded().length > MAX_EXPANDED) {
      setExpandFailure({ id, kind, error: null, limit: true })
      return
    }
    try {
      // Read it first, so a failure leaves the map and the open sheet as they were.
      await queryClient.fetchQuery({
        ...neighborsRead(activeTenant, node),
        staleTime: 0,
      })
      if (askedIn !== generationRef.current) return
      // A PLACE, not a facet: this is the one change that pushes a history entry, so
      // Back returns to the map as it was, selection included. The list is read from the
      // address NOW: another expansion may have landed while this one was in flight.
      drive(
        {
          expand: expandedValue(nextExpanded()),
          ...(latest.current.node === id
            ? { node: undefined, edge: undefined }
            : {}),
        },
        { history: 'push' },
      )
    } catch (error) {
      if (askedIn !== generationRef.current) return
      // Neighbors is privileged/audited too. Preserve the graph, but do not
      // convert a failed read into a silently successful no-op.
      setExpandFailure({ id, kind, error })
    }
  }

  const selectNode = (id: string) => drive({ node: id, edge: undefined })
  const selectEdge = (id: string) => drive({ edge: id, node: undefined })
  const clearSelection = () => drive({ node: undefined, edge: undefined })
  const selectDrift = (entry: DriftEntry) => selectEdge(entry.edge.id)
  // A group opens in place; a "+N more" node and every other node open the sheet.
  const setOpenGroups = (keys: readonly string[]) =>
    patchFullUrl({ [GROUP_PARAM]: groupsValue(keys) })
  const onNode = (id: string) => {
    const data = render.nodes.find((n) => n.id === id)?.data as
      Partial<GroupNodeData> | undefined
    if (!data?.groupKey) return selectNode(id)
    if (zoomedOut) return setZoomedOut(false)
    setOpenGroups([...openGroups, data.groupKey])
  }
  // The chip of an open group: the name its node had (a group key is kind:name).
  const groupName = (key: string) => {
    const [kind, name = ''] = [
      key.slice(0, key.indexOf(':')),
      key.slice(key.indexOf(':') + 1),
    ]
    if (kind === 'tool') return toolName(name)
    if (kind === 'origin')
      return t(`groupKinds.${name}`, { defaultValue: name })
    return name.split('/').filter(Boolean).at(-1) ?? name
  }
  // An edge that stands for several leads into its group.
  const onEdge = (id: string) => {
    if (!id.startsWith('agg:')) return selectEdge(id)
    const edge = render.edges.find((e) => e.id === id)
    const end = [edge?.target, edge?.source].find((n) =>
      n?.startsWith('group:'),
    )
    if (end) onNode(end)
  }

  // Arrow keys walk the canvas from node to connected node (React Flow nodes are
  // focusable); Enter or Space opens the focused one. The list below the graph stays
  // the path that needs no canvas at all.
  const onGraphKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const el = (event.target as HTMLElement).closest<HTMLElement>(
      '.react-flow__node',
    )
    const id = el?.dataset.id
    if (!el || !id) return
    // Alt+Arrow is the browser's Back and Forward, Ctrl and Meta are the platform's.
    if (event.altKey || event.ctrlKey || event.metaKey) return
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      onNode(id)
      return
    }
    if (!ARROWS.includes(event.key)) return
    const to = connectedNeighbor(
      render.nodes,
      render.edges,
      id,
      event.key as ArrowKey,
    )
    if (to === null) return
    event.preventDefault()
    event.currentTarget
      .querySelector<HTMLElement>(
        `.react-flow__node[data-id="${CSS.escape(to)}"]`,
      )
      ?.focus()
  }

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
  //
  // Selection and expansion now live in the address, which a tenant switch does not
  // clear by itself: the effect below does, but not on mount (a link opened in a
  // tenant names its own selection).
  const [seenTenant, setSeenTenant] = useState(activeTenant)
  if (seenTenant !== activeTenant) {
    setSeenTenant(activeTenant)
    setExpandFailure(null)
    setDroppedNotice([])
    setRecheck({ status: 'idle' })
  }
  const tenantInAddress = useRef(activeTenant)
  useEffect(() => {
    const was = tenantInAddress.current
    if (was === activeTenant) return
    tenantInAddress.current = activeTenant
    // The first tenant resolving (null to an id) is the link's own tenant, not a switch.
    if (was !== null)
      patchUrl(
        { node: undefined, edge: undefined, expand: undefined },
        { history: 'replace' },
      )
  }, [activeTenant, patchUrl])
  const onRecheck = async (edgeId: string) => {
    setRecheck({ status: 'checking', edgeId })
    // The context this check was ASKED in. A completion from an older one is dropped, not
    // rendered: it was measured against an estate the sheet is no longer showing.
    const askedIn = generationRef.current
    try {
      const res = await driftQuery.refetch()
      if (askedIn !== generationRef.current) return
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
      if (askedIn !== generationRef.current) return
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
  const showMap = !isLoading && !error && !isEmpty

  // F enters and leaves, Escape leaves. A bare key is the map's only where the operator
  // is not typing, not in an open menu, not behind a dialog (the detail sheet takes its
  // own Escape first) and not the second half of a `g` sequence (`g f` is Cost).
  const enterRef = useRef<HTMLButtonElement>(null)
  const exitRef = useRef<HTMLButtonElement>(null)
  const returnFocus = useRef(false)
  useEffect(() => {
    if (!showMap && !full) return
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.defaultPrevented || e.repeat || e.isComposing) return
      if (navLeader.armed() || modalIsOpen()) return
      if (e.key === 'Escape') {
        if (
          !full ||
          (e.target instanceof Element &&
            e.target.closest('[role="menu"],[role="listbox"]'))
        )
          return
        returnFocus.current = true
        setFull(false)
        return
      }
      if (isTypingTarget(e.target)) return
      if (
        resolveBinding(KEYBINDINGS, e, { accessMap: true }) !==
        'accessMap.fullScreen'
      )
        return
      e.preventDefault()
      if (full) returnFocus.current = true
      setFull(!full)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showMap, full, setFull])
  useEffect(() => {
    if (!full) return
    // The fullscreen surface is portalled beside the shell. Keep the covered shell
    // out of keyboard and screen-reader navigation while detail dialogs remain usable.
    const root = document.getElementById('root')
    const wasInert = root?.inert ?? false
    const overflow = document.body.style.overflow
    if (root) root.inert = true
    document.body.style.overflow = 'hidden'
    return () => {
      if (root) root.inert = wasInert
      document.body.style.overflow = overflow
    }
  }, [full])
  // Keyboard and screen-reader focus follows the mode: into the window on entering, back
  // to the control that opened it on leaving.
  useEffect(() => {
    if (full) exitRef.current?.focus()
    else if (returnFocus.current) {
      returnFocus.current = false
      enterRef.current?.focus()
    }
  }, [full])

  const refreshButton = (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => {
        drive({ expand: undefined })
        setExpandFailure(null)
        void graphQuery.refetch()
        if (overlay) void driftQuery.refetch()
      }}
      disabled={graphQuery.isFetching}
    >
      <RefreshCw
        className={cn('size-3.5', graphQuery.isFetching && 'animate-spin')}
      />
      {t('refresh')}
    </Button>
  )

  const fullScreenButton = (
    <Button
      ref={full ? exitRef : enterRef}
      variant="outline"
      size="sm"
      className="shrink-0"
      onClick={() => {
        returnFocus.current = full
        setFull(!full)
      }}
    >
      {full ? <Minimize2 /> : <Maximize2 />}
      {full ? t('fullscreen.exit') : t('fullscreen.enter')}
    </Button>
  )

  // ⛔ NOT CONVERTED TO ListTruncationBadge ON PURPOSE: `merged` is a derived useMemo,
  // NOT a query object -- it has no .error, which is half of the rule the component
  // applies. Passing it would compile and lie: the notice would depend only on
  // has_more. Doing it right requires deciding WHICH query it inherits the error from.
  // The list is built from the same cut read, so it says so too.
  const truncatedBadge = merged.has_more && (
    <Badge variant="warning" title={t('stats.truncatedHint')}>
      {t('stats.truncated', { n: GRAPH_LIMIT })}
    </Badge>
  )

  // Opening this map is audited: the subtitle says so, in full screen too. A Business
  // build that records this view adds its own notice.
  const auditNotice = <RecordingNoticeSlot namespace="accessmap" />
  const viewButton = showGraph ? (
    <Button variant="outline" size="sm" className="shrink-0" onClick={showList}>
      <List />
      {t('showList')}
    </Button>
  ) : (
    <Button
      variant="outline"
      size="sm"
      className="shrink-0"
      onClick={() => drive({ view: 'graph' })}
    >
      <Network />
      {t('showGraph')}
    </Button>
  )

  const page = (
    <div
      role={full ? 'main' : undefined}
      className={cn(
        'flex flex-col gap-4',
        full
          ? 'fixed inset-0 z-40 gap-3 overflow-y-auto bg-canvas p-4 text-text max-md:p-2'
          : 'h-full',
      )}
    >
      {/* Header. Full screen replaces it with one line: the page header publishes its
          actions to the shell, which the window covers. */}
      {full ? (
        <>
          <div className="flex items-center gap-2">
            <Network className="size-4 shrink-0 text-muted-foreground" />
            <h1 className="min-w-0 flex-1 truncate text-body font-semibold">
              {t('title')}
            </h1>
            {fullScreenButton}
            {refreshButton}
          </div>
          <p className="text-caption text-muted-foreground">{t('subtitle')}</p>
          {auditNotice}
        </>
      ) : (
        <PageHeader
          icon={Network}
          title={t('title')}
          description={t('subtitle')}
          notices={auditNotice}
          actions={
            <>
              {viewButton}
              {showGraph && fullScreenButton}
              {refreshButton}
            </>
          }
        />
      )}

      {/* Stat strip */}
      {showGraph && !isLoading && !error && (
        <div
          className={cn(
            'flex flex-wrap items-center gap-2 text-caption',
            full && 'max-md:hidden',
          )}
        >
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
          {truncatedBadge}
          {/* Make the renderer degradation visible/documented in-UI. */}
          {useWebGL && (
            <Badge
              variant="info"
              title={t('webgl.hint', { n: render.nodes.length })}
            >
              {t('webgl.badge')}
            </Badge>
          )}
        </div>
      )}

      {/* An expansion whose read failed: the map is partial, and the caveat says so. */}
      {neighborReads.some((r) => r.isError) && (
        <CaveatNotice tone="warning">
          {t('expand.partial')}{' '}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              for (const r of neighborReads) if (r.isError) void r.refetch()
            }}
          >
            {t('expand.retry')}
          </Button>
        </CaveatNotice>
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
          compact={!showGraph}
        />
      )}

      {/* Body */}
      {isLoading ? (
        <div className="flex flex-1 items-center justify-center rounded-lg border border-border bg-surface">
          <Spinner />
        </div>
      ) : error ? (
        /* ⛔ ASSURANCE BEFORE ROLE — the same order as the seam
           (_intel/async.tsx:56-63). `isForbidden` is ONLY the 403 status
           (lib/api/errors.ts:59), so a `step_up_required` satisfies it too:
           reading it first erased the whole screen and left a refresh that returns
           the same 403 forever as the only control. */
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
          <QueryErrorState
            error={graphQuery.error}
            retry={() => void graphQuery.refetch()}
          />
        )
      ) : isEmpty ? (
        <EmptyState
          icon={<Network />}
          title={t('empty.title')}
          description={t('empty.description')}
        />
      ) : !showGraph ? (
        <>
          {truncatedBadge && <div>{truncatedBadge}</div>}
          <OriginList
            nodes={built.nodes}
            edges={built.edges}
            groupOf={originGroup}
            onSelect={selectNode}
          />
        </>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col gap-4 lg:flex-row">
          <div className="flex min-h-0 flex-1 flex-col gap-3">
            {/* Graph (desktop). React Flow ≤ threshold (the polished view); Sigma
              WebGL above it — SAME built model, same selection/click contract. */}
            <div
              // In the page the box is 70vh tall and the page scrolls: as a flex-1 item
              // its basis was 0, so it shrank to its 460 px floor and the fit drew the
              // text at half size. Only the full-screen window fills what is left.
              className={cn(
                'relative',
                full
                  ? 'min-h-[16rem] flex-1'
                  : 'hidden h-[70vh] min-h-[460px] shrink-0 md:block',
              )}
              onKeyDown={onGraphKeyDown}
            >
              {!useWebGL && (
                <p className="sr-only">{t('graph.keyboardHint')}</p>
              )}
              {useWebGL ? (
                <SigmaGraph
                  nodes={render.nodes}
                  edges={render.edges}
                  nodeColor={accessNodeColor}
                  ariaLabel={t('graph.label')}
                  onNodeClick={onNode}
                  onEdgeClick={onEdge}
                  onPaneClick={clearSelection}
                  fitKey={`${filter.search}|${[...filter.modes].join(',')}|${filter.confidence}|${filter.signalSource}|${overlay}|${render.nodes.length}|${render.grouped}`}
                >
                  <div className="pointer-events-none absolute bottom-3 left-3 z-10">
                    <AccessLegend overlay={overlay && canDrift} />
                  </div>
                </SigmaGraph>
              ) : (
                <GraphCanvas
                  className="max-sm:[&_.react-flow\_\_minimap]:!hidden"
                  fitMinZoom={LEGIBLE_FIT_MIN_ZOOM}
                  // Grouped: nothing below the readable zoom to shrink the text. Grouped
                  // because the operator zoomed out: the refit lands on exactly it (see
                  // onViewportZoom); a pinch back in lifts the ceiling and keeps its zoom.
                  fitMaxZoom={
                    grouped && zoomedOut ? LEGIBLE_FIT_MIN_ZOOM : undefined
                  }
                  minZoom={grouped ? LEGIBLE_FIT_MIN_ZOOM : undefined}
                  onViewportZoom={onViewportZoom}
                  verticalNodeGap={16}
                  nodes={render.nodes}
                  edges={render.edges}
                  nodeTypes={accessNodeTypes}
                  edgeTypes={accessEdgeTypes}
                  ariaLabel={t('graph.label')}
                  onNodeClick={(n) => onNode(n.id)}
                  onEdgeClick={(e) => onEdge(e.id)}
                  onPaneClick={clearSelection}
                  minimapColor={(n) =>
                    (n.data as { hasUnexpected?: boolean }).hasUnexpected
                      ? 'var(--color-danger)'
                      : (n.data as { role?: string }).role === 'origin'
                        ? 'var(--color-accent-text)'
                        : 'var(--color-graphite-400)'
                  }
                  fitKey={`${filter.search}|${[...filter.modes].join(',')}|${filter.confidence}|${filter.signalSource}|${overlay}|${built.nodes.length}|${full}|${openKey}`}
                >
                  <Panel
                    position="top-left"
                    className="max-w-[calc(100%-2rem)]"
                  >
                    <AccessLegend overlay={overlay && canDrift} />
                  </Panel>
                </GraphCanvas>
              )}
              {(grouped || openGroups.length > 0) && (
                <div className="absolute bottom-12 right-3 z-10 flex max-w-[60%] flex-wrap justify-end gap-1.5 sm:bottom-auto sm:top-3">
                  {openGroups.map((key) => (
                    <Button
                      key={key}
                      variant="outline"
                      size="sm"
                      aria-label={t('group.close', { name: groupName(key) })}
                      onClick={() =>
                        setOpenGroups(openGroups.filter((k) => k !== key))
                      }
                    >
                      {groupName(key)}
                      <X />
                    </Button>
                  ))}
                  {grouped && openGroups.length === 0 && (
                    <Badge variant="info">{t('lod.grouped')}</Badge>
                  )}
                </div>
              )}
            </div>

            {/* Narrow screens: the graph is not drawn in the page, it opens full screen
                (pan with one finger, pinch to zoom). The list below stays. */}
            {!full && (
              <div className="rounded-lg border border-border bg-surface p-6 text-center md:hidden">
                <Network className="mx-auto mb-2 size-6 text-muted-foreground" />
                <p className="text-body font-medium text-foreground">
                  {t('mobile.title')}
                </p>
                <p className="mt-1 text-caption text-muted-foreground">
                  {t('mobile.hint')}
                </p>
                <Button
                  variant="outline"
                  size="sm"
                  className="mt-3"
                  onClick={() => setFull(true)}
                >
                  <Maximize2 />
                  {t('mobile.open')}
                </Button>
              </div>
            )}

            {/* Keyboard path: the canvas is pointer-first; this list is the same
              selection contract without requiring a mouse. */}
            <details
              className="rounded-lg border border-border bg-surface p-3"
              onToggle={(e) =>
                setListOpen((e.currentTarget as HTMLDetailsElement).open)
              }
            >
              <summary className="cursor-pointer text-body font-medium text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring">
                {t('list.summary')}
              </summary>
              {listOpen ? (
                <>
                  <p className="mt-1 text-caption text-muted-foreground">
                    {t('list.hint')}
                  </p>
                  <StaticTable className="mt-3">
                    <caption className="sr-only">{t('list.summary')}</caption>
                    <thead>
                      <tr>
                        <th className="py-1">{t('list.node')}</th>
                        <th className="py-1">{t('list.kind')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {built.nodes.map((n) => {
                        const data = n.data as OriginNodeData | ResourceNodeData
                        return (
                          <tr key={n.id}>
                            <td className="py-1">
                              {/* ⛔ A BUTTON, NOT A LINK. This was written as
                                `<a href="#access-node-${n.id}">` with
                                `preventDefault()`, and that anchor EXISTS
                                nowhere in the tree: a screen reader announces it
                                as a link to a destination that is not there, and
                                the keyboard offers it as navigation when what it
                                does is SELECT. It selects, so it is a
                                button. */}
                              <button
                                type="button"
                                onClick={() => selectNode(n.id)}
                                title={data.ref}
                                className="text-left text-foreground underline-offset-2 hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                {data.label}
                              </button>
                            </td>
                            <td className="py-1 text-muted-foreground">
                              {data.kind}
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </StaticTable>
                </>
              ) : null}
            </details>
          </div>

          {/* Drift side panel */}
          {overlay && (
            <aside
              className={cn(
                'w-full shrink-0 overflow-y-auto rounded-lg border border-border bg-surface p-4 lg:w-80',
                full && 'max-lg:max-h-56',
              )}
            >
              <h2 className="mb-3 flex items-center gap-1.5 text-body font-semibold text-foreground">
                <Eye className="size-4" />
                {t('drift.title')}
              </h2>
              {hasMapFilter && (
                <p className="mb-3 text-caption text-muted-foreground">
                  {t('drift.scopeHint')}
                </p>
              )}
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
                /* This panel did not tell EITHER of the two 403s apart: any
                   refusal was drawn as a red failure with a retry that cannot
                   work. Now ceremony, then role, then failure.
                   (The ForbiddenState above is the capability gate
                   `!canDrift`, not an error branch: that one is not touched.) */
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
                  <QueryErrorState
                    error={driftQuery.error}
                    retry={() => void driftQuery.refetch()}
                  />
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
          clearSelection()
          setExpandFailure(null)
        }}
        onExpand={onExpand}
        expandError={expandFailureOf?.error ?? null}
        expandNotice={
          expandFailureOf?.limit
            ? t('detail.expandLimit', { n: MAX_EXPANDED })
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

  // The window sits on the body, not inside the shell: no ancestor's stacking context or
  // overflow can cover it, and the dialogs the detail sheet opens (z-50) stay above it.
  return full ? createPortal(page, document.body) : page
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
