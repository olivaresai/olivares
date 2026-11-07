// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A PERSON MAY PICK FOR A SESSION, from the live catalog of its profile
// (GET /provider-profiles/{ref}/models). The tool names its models, which one is its
// default, which are new or retired and which effort levels each takes: nothing here
// maintains the live list, so a model the vendor ships tomorrow needs no
// console release. The list is only grouped and filtered for what a launch accepts.
import { EFFORT_LEVELS } from './types'
import type { ProfileModelCatalogDTO, ProfileModelDTO } from './types'

export interface ModelGroups {
  /** The person's favorites the tool still lists, retired ones included. */
  favorites: ProfileModelDTO[]
  models: ProfileModelDTO[]
  /** Retired models: listed last, because a launch on one may fail. */
  legacy: ProfileModelDTO[]
}

/** Groups the catalog's models in the tool's own order. A favorite the tool no longer
 * lists is not shown: it is a preference, not a fact about the tool. */
export function groupModels(
  models: readonly ProfileModelDTO[],
  favorites: ReadonlySet<string>,
): ModelGroups {
  const groups: ModelGroups = { favorites: [], models: [], legacy: [] }
  for (const m of models) {
    if (favorites.has(m.id)) groups.favorites.push(m)
    else if (m.is_legacy) groups.legacy.push(m)
    else groups.models.push(m)
  }
  return groups
}

/** The catalog entry for `id`: the model of that id, else the selector that launches on
 * it (a session records the exact id, not the selector it was chosen as). */
export function findModel(
  catalog: { models: readonly ProfileModelDTO[] } | undefined,
  id: string,
): ProfileModelDTO | undefined {
  return (
    catalog?.models.find((m) => m.id === id) ??
    catalog?.models.find((m) => m.launch_model === id && m.id !== 'default') ??
    catalog?.models.find((m) => m.launch_model === id)
  )
}

/** The model a session starts on: the one chosen, else the tool's own default as the
 * catalog says it. A typed id the catalog does not list stays that id alone. Undefined
 * when nothing is chosen and the tool names no default. */
export function startModel(
  catalog: ProfileModelCatalogDTO | undefined,
  chosen: string,
): ProfileModelDTO | undefined {
  const id =
    chosen ||
    catalog?.default_model ||
    catalog?.models.find((m) => m.is_default)?.id ||
    ''
  if (!id) return undefined
  return findModel(catalog, id) ?? { id }
}

/** The effort levels to offer for `model`, or null while no model is known: an effort is
 * a level of a model, so none is asked for before a model is. A tool that reports none
 * leaves the launch's own levels, which the engine validates. A Claude launch accepts
 * only those (modules/sessions/runtime_profile.go), so a vendor level outside them is
 * not offered: it would end in a 400. */
export function effortChoices(
  driver: string,
  model: ProfileModelDTO | undefined,
): string[] | null {
  if (!model) return null
  const reported = model.effort_levels
  if (!reported?.length) return [...EFFORT_LEVELS]
  if (driver !== 'claude') return [...reported]
  const accepted: readonly string[] = EFFORT_LEVELS
  return reported.filter((level) => accepted.includes(level))
}

/** Before the first own-login profile, offer only documented launch choices. These
 * are release data, never a probe or a reason to create a profile during a read. */
export function firstProfileCatalog(driver: string): ProfileModelCatalogDTO {
  let models: ProfileModelDTO[] = []
  if (driver === 'claude') {
    // Labels: https://platform.claude.com/docs/en/models/overview
    // Launch aliases and efforts: https://code.claude.com/docs/en/model-config
    models = [
      { id: 'opus', display_name: 'Claude Opus 5.5' },
      { id: 'sonnet', display_name: 'Claude Sonnet 5.5' },
      { id: 'haiku', display_name: 'Claude Haiku 5.5' },
      { id: 'fable', display_name: 'Claude Fable 5.1' },
    ].map((model) => ({
      ...model,
      effort_levels: ['low', 'medium', 'high', 'xhigh', 'max'],
    }))
  } else if (driver === 'codex') {
    // Model IDs and per-model effort: https://learn.chatgpt.com/docs/models
    models = ['gpt-6-astra', 'gpt-6.1-sol', 'gpt-6.1-terra', 'gpt-6-luna'].map(
      (id) => ({
        id,
        effort_levels: ['low', 'medium', 'high', 'xhigh', 'max'],
      }),
    )
  } else if (driver === 'gemini') {
    // Model IDs (no effort levels): https://geminicli.com/docs/cli/model/
    models = [
      'gemini-3-pro-preview',
      'gemini-3-flash-preview',
      'gemini-2.5-pro',
      'gemini-2.5-flash',
    ].map((id) => ({ id }))
  } else if (driver === 'opencode') {
    // Model ID (no effort levels): https://opencode.ai/docs/cli/commands/
    models = [{ id: 'anthropic/claude-sonnet-5-5' }]
  }
  // Other CLI references name no model IDs: https://docs.x.ai/build/cli/reference,
  // https://cursor.com/docs/cli/reference/parameters.
  // Those drivers offer only the tool's default until their live catalog exists.
  return {
    profile_ref: '',
    driver,
    state: 'fresh',
    models,
    default_model: '',
    free_text: false,
    checked_at: '',
    refresh_interval_seconds: 0,
  }
}
