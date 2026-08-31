// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Types for workspace templates. Templates capture a reusable snapshot
// of hooks, settings, connector references, and policies that can be applied to
// a workspace session. Built-in templates are read-only; user templates can be
// created, edited, duplicated, and archived. Shapes mirror the backend DTOs from
// /v1/m/sessions/templates (core/modules/sessions — template CRUD).

/** A single hook command with an optional timeout. */
export interface HookEntry {
  command: string
  timeout_ms?: number
}

/** Hook lifecycle configuration carried in a template. */
export interface TemplateHooks {
  pre_tool?: HookEntry[]
  post_tool?: HookEntry[]
  pre_session?: HookEntry[]
  post_session?: HookEntry[]
}

/** Agent runtime settings carried in a template. */
export interface TemplateSettings {
  permission_mode?: string
  effort?: string
  model?: string
  custom_instructions?: string
}

/** Governance policy settings carried in a template. */
export interface TemplatePolicies {
  dlp_mode?: string
  max_session_duration_minutes?: number
  allowed_tools?: string[]
  record_io?: boolean
}

/**
 * The body of a workspace template — all fields are optional so a template can
 * carry a partial configuration (e.g. only policies, no hooks).
 */
export interface TemplateBody {
  hooks?: TemplateHooks
  settings?: TemplateSettings
  /** List of connector IDs to attach when the template is applied. */
  connectors?: string[]
  policies?: TemplatePolicies
}

/**
 * GET /v1/m/sessions/templates/{id} (and list items).
 * `archived_at` is present only for soft-deleted templates.
 * `builtin` marks templates shipped with the platform (read-only).
 */
export interface TemplateDTO {
  id: string
  name: string
  description: string
  version: number
  author: string
  builtin: boolean
  archived_at?: string
  body: TemplateBody
  created_at: string
  updated_at: string
}

/** One conflict entry returned by the /apply endpoint: a field the target chose and
 * the template overrode. */
export interface ApplyConflict {
  field: string
  old_value: unknown
  new_value: unknown
}

/** The launch configuration a template is merged ONTO, and the merge's result. Mirrors
 * the POST /runs body, so the preview and the launch describe the same thing. */
export interface ApplyTarget {
  transport?: string
  permission_mode?: string
  effort?: string
  model?: string
  allowed_tools?: string[]
  custom_instructions?: string
  record_io?: boolean
  max_session_duration_minutes?: number
  workspace_ref?: string
}

/**
 * Response from POST /v1/m/sessions/templates/{id}/apply — a MERGE PREVIEW. It changes
 * nothing: a template governs a session by being named at launch (`template_id` on
 * POST /runs), where the server merges it before the governance gates.
 *
 * `applied` is a fact and no longer a constant: false when the template declares a term
 * the launch cannot keep, and `unenforceable` then names which one and why — the same
 * list a launch naming this template is refused with.
 */
export interface ApplyResult {
  applied: boolean
  conflicts: ApplyConflict[]
  merged?: ApplyTarget
  unenforceable?: string[]
}
