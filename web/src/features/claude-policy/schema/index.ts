// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Verified Claude Code policy schemas — barrel + cross-surface helpers.
import type { CodeDiagnostic } from '@/components/ui/code-editor'
import { hooksConfigSchema, validateHooks } from './hooks'
import { managedMcpSchema, validateManagedMcp } from './managed-mcp'
import {
  managedSettingsSchema,
  validateManagedSettings,
} from './managed-settings'
import { sandboxSchema, validateSandbox } from './sandbox'
import { type SchemaIssue, parseJson, topLevelKeyRange } from './types'

export * from './types'
export * from './managed-settings'
export * from './hooks'
export * from './managed-mcp'
export * from './sandbox'

export type PolicySurface =
  | 'managed-settings'
  | 'hooks'
  | 'managed-mcp'
  | 'sandbox'

export const POLICY_SURFACES: readonly PolicySurface[] = [
  'managed-settings',
  'hooks',
  'managed-mcp',
  'sandbox',
]

/** Run the right verified validator for a surface against a parsed value. */
export function validateSurface(
  surface: PolicySurface,
  value: unknown,
): SchemaIssue[] {
  switch (surface) {
    case 'managed-settings':
      return validateManagedSettings(value)
    case 'hooks':
      return validateHooks(value)
    case 'managed-mcp':
      return validateManagedMcp(value)
    case 'sandbox':
      return validateSandbox(value)
  }
}

/** The zod schema backing a surface (for callers that want raw parse). */
export function surfaceSchema(surface: PolicySurface) {
  switch (surface) {
    case 'managed-settings':
      return managedSettingsSchema
    case 'hooks':
      return hooksConfigSchema
    case 'managed-mcp':
      return managedMcpSchema
    case 'sandbox':
      return sandboxSchema
  }
}

/**
 * Build a CodeEditor lint source for a JSON policy surface: JSON syntax errors are
 * handled by the editor's own jsonLint; this layers the VERIFIED-SCHEMA findings
 * as inline markers (mapped to the offending top-level key where possible).
 * Memoize the returned function at the call site (CodeEditor reconfigures on
 * identity change).
 */
export function makeJsonLintSource(
  surface: PolicySurface,
): (doc: string) => CodeDiagnostic[] {
  return (doc: string) => {
    const { value, error } = parseJson(doc)
    if (error || value === undefined) return [] // syntax handled by jsonLint
    return validateSurface(surface, value).map((issue) => {
      const topKey = issue.path.split('.')[0]
      const range = topKey ? topLevelKeyRange(doc, topKey) : null
      const from = range?.from ?? 0
      const to = range?.to ?? Math.min(doc.length, from + 1)
      return {
        from,
        to: Math.max(to, from + 1),
        severity: issue.severity,
        message: issue.message,
      }
    })
  }
}
