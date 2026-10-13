// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A PERSON MAY PICK FOR A SESSION, from the live catalog of its profile
// (GET /provider-profiles/{ref}/models). The tool names its models, which one is its
// default, which are new or retired and which effort levels each takes: nothing here
// lists a model, so a model the vendor ships tomorrow is a new catalog entry and no
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
  return catalog?.models.find((m) => m.id === id) ?? { id }
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
