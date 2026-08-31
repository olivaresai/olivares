// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the Log Viewer (superadmin-only). The backend pushes individual log
// entries via SSE (event: "log") on GET /v1/console/logs/stream, and serves a
// historical buffer via GET /v1/console/logs/buffer. The frontend merges the two:
// buffer = initial backfill, stream = live tail. Types mirror the engine's JSON
// shapes 1:1.

/** Runtime log level. */
export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

/** One log entry as emitted by the engine (SSE event data or buffer item). */
export interface LogEntry {
  timestamp: string
  level: LogLevel
  message: string
  module?: string
  attrs?: Record<string, unknown>
}

/** GET /v1/console/logs/buffer response envelope. */
export interface LogBufferResponse {
  items: LogEntry[]
  total: number
  /** Minimum level the engine currently captures (OLIVARES_LOG_LEVEL). */
  capture_level: string
}

/** Client-side filter state for the log viewer. */
export interface LogFilters {
  /** Active log levels (multi-select toggle). Empty = all levels shown. */
  levels: Set<LogLevel>
  /** Module substring filter (sent to both buffer and stream). */
  module: string
  /** Free-text search in message (client-side only). */
  search: string
}
