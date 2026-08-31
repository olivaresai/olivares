// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Shared types + helpers for the verified Claude Code policy schemas.
//
// HONESTY CONTRACT (spec "no inventes claves"): every key, event,
// path and ordering in this module was verified VERBATIM against the live
// Anthropic docs (code.claude.com / platform.claude.com) on 2026-06-06. Each
// descriptor carries its source URL. Items that could NOT be confirmed are marked
// `toConfirm: true` and rendered as such — they are never asserted as fact.
import { z } from 'zod'

export type FieldScope = 'managed-only' | 'any'

export interface KeyDescriptor {
  /** The setting key (dotted for nested, e.g. sandbox.network.allowManagedDomainsOnly). */
  key: string
  type: 'boolean' | 'string' | 'number' | 'string[]' | 'object' | 'enum'
  /** managed-only = takes effect ONLY in managed-settings; any = works from any scope. */
  scope: FieldScope
  summary: string
  enum?: readonly string[]
  /** Minimum Claude Code version that introduced the key, if documented. */
  since?: string
  /** Verified against the live doc, but with a caveat — render with a marker. */
  toConfirm?: boolean
  /** Authoritative source URL the descriptor was verified against. */
  source: string
}

export type IssueSeverity = 'error' | 'warning' | 'info'

export interface SchemaIssue {
  /** Dotted JSON path, '' for the document root. */
  path: string
  message: string
  severity: IssueSeverity
}

export interface SchemaValidation {
  ok: boolean
  issues: SchemaIssue[]
}

/** Parse JSON, returning a single syntax issue on failure (offsets live with the
 *  editor's own jsonLint; this is for the validation panel). */
export function parseJson(text: string): {
  value?: unknown
  error?: SchemaIssue
} {
  const trimmed = text.trim()
  if (!trimmed) return { value: undefined }
  try {
    return { value: JSON.parse(trimmed) }
  } catch (e) {
    return {
      error: {
        path: '',
        message: e instanceof Error ? e.message : 'Invalid JSON',
        severity: 'error',
      },
    }
  }
}

/** Map a zod error into our flat issue list (dotted paths). */
export function zodIssues(
  error: z.ZodError,
  severity: IssueSeverity = 'error',
): SchemaIssue[] {
  return error.issues.map((i) => ({
    path: i.path.map(String).join('.'),
    message: i.message,
    severity,
  }))
}

/**
 * Find the [from,to) range of a TOP-LEVEL JSON key in the raw text so an editor
 * can place an inline marker. Best-effort (top-level keys only) — nested keys are
 * surfaced in the validation panel instead. Returns null if not found.
 */
export function topLevelKeyRange(
  text: string,
  key: string,
): { from: number; to: number } | null {
  // Quote the key, allow escaped chars in the source; match the first occurrence
  // that looks like a top-level property (preceded by { or , ignoring whitespace).
  const needle = `"${key}"`
  let idx = text.indexOf(needle)
  while (idx !== -1) {
    // Walk back over whitespace; a top-level key follows '{' or ',' or doc start.
    let j = idx - 1
    while (j >= 0 && /\s/.test(text[j]!)) j--
    if (j < 0 || text[j] === '{' || text[j] === ',') {
      return { from: idx, to: idx + needle.length }
    }
    idx = text.indexOf(needle, idx + needle.length)
  }
  return null
}

/** Validate the parsed object against a zod schema, returning issues (never throws). */
export function validateWith(
  schema: z.ZodType,
  value: unknown,
  severity: IssueSeverity = 'error',
): SchemaIssue[] {
  const res = schema.safeParse(value)
  return res.success ? [] : zodIssues(res.error, severity)
}

export { z }
