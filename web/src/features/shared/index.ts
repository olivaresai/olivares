// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Shared building blocks for the visibility views (live streaming, formatters,
// the graph chrome). Importing this barrel also registers the `shared` i18n
// namespace as a side effect.
import './i18n'

export {
  createSSEParser,
  subscribeStream,
  useLiveStream,
  type SSEMessage,
  type StreamStatus,
  type SubscribeOptions,
  type UseLiveStreamOptions,
} from './sse'
export { humanDurationSeconds, parseTs, ppmToPercent } from './format'
export { LiveDot } from './live-dot'
export { RelTime as RelTimeLabel } from './rel-time'
export { UrlStateNotice, type UrlStateNoticeProps } from './url-state-notice'
export { isoDayBound, isoMinuteBound, parseIsoBound } from './iso-bound'
export {
  normalizeSourceMode,
  type NormalizedSourceMode,
  type SourceMode,
} from './source-mode'
export { SourceModeBadge } from './source-mode-badge'
export { GraphCanvas, type GraphCanvasProps } from './graph/graph-canvas'
export { SigmaGraph, type SigmaGraphProps } from './graph/sigma-graph'
export {
  buildSigmaGraph,
  createColorResolver,
  type ColorResolver,
} from './graph/sigma-graph-build'
export {
  clusterByHost,
  type ClusterOptions,
  type ClusterResult,
} from './graph/cluster'
export { layeredLayout, type LayoutResult } from './graph/layout'
export { useIsDark } from './graph/theme'
