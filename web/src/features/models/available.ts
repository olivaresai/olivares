// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The rows of Models > Available: one per tool/provider and model that can run now, with the
// live sessions that run it. Every fact is read from the engine where it already lives (the
// tool's own model list, the installed Ollama models, configured availability, the live runs);
// this file only joins them.
import type { OllamaStatus } from '@/features/agent-tools/api'
import type { ProfileModelCatalogDTO, RunDTO } from '@/features/agentops/types'
import type { ProviderRecordDTO } from '@/features/providers/types'
import { isLiveRun } from '@/features/sessions/provenance'
import type { ModelAvailability } from './types'

export type AvailableSource = 'tool' | 'ollama' | 'provider' | 'session'

export interface AvailableRow {
  key: string
  source: AvailableSource
  /** The tool's driver ("claude"), "ollama", or the provider's name. */
  owner: string
  /** The model id; empty on the one row of a tool that does not list its models. */
  model: string
  label: string
  /** `ready`; Ollama's `starting`, `stopped` or `ollamaFailed`; `unlisted` / `failed` (a tool's
   * list); or the source's availability state. */
  state: string
  /** The list came from the provider the profile is bound to, not from the tool itself. */
  viaProvider?: boolean
  /** The provider record behind this row, when the source names one. */
  providerRef?: string
  /** The provider's engine kind, used for its native model-reference form. */
  providerKind?: string
  /** The tool's default model: what a launch with no model named runs. */
  isDefault?: boolean
  /** The live sessions running this model. */
  sessions: RunDTO[]
}

export interface ToolCatalog {
  driver: string
  /** The profile behind the list; a run names the same reference. */
  profileRef?: string
  /** The provider record behind a bound profile's list. */
  providerRef?: string
  /** The profile's model list: undefined while loading, null when the read failed. */
  catalog: ProfileModelCatalogDTO | null | undefined
  /** The profile is bound to a provider record, so the list is that provider's. */
  viaProvider?: boolean
  /** The read is still on its way: the sessions of this tool wait for it instead of being
   * counted on another row that the list, once it arrives, would take them from. */
  loading?: boolean
}

export interface AvailableInput {
  catalogs: ToolCatalog[]
  ollama?: OllamaStatus
  availability: ModelAvailability[]
  /** Provider metadata used for display names and endpoint identity, when the caller may read it. */
  providers?: ProviderRecordDTO[]
  runs: RunDTO[]
  /** Do not paint provisional rows before the profiles that settle their identity answer. */
  profilesSettled?: boolean
}

function toolKey(driver: string, viaProvider: boolean, model = ''): string {
  const base = `tool:${driver}${viaProvider ? ':provider' : ''}`
  return model === '' ? base : `${base}:${model.toLowerCase()}`
}

function toolRows(
  catalogs: ToolCatalog[],
  defaultDrivers: ReadonlySet<string>,
): AvailableRow[] {
  type Group = { driver: string; viaProvider: boolean; catalogs: ToolCatalog[] }
  const groups: Group[] = []
  for (const entry of catalogs) {
    const viaProvider = !!entry.viaProvider
    let group = groups.find(
      (g) => g.driver === entry.driver && g.viaProvider === viaProvider,
    )
    if (!group) {
      group = { driver: entry.driver, viaProvider, catalogs: [] }
      groups.push(group)
    }
    group.catalogs.push(entry)
  }
  return groups.flatMap((group) => {
    if (group.catalogs.every((entry) => entry.catalog === undefined)) return []
    const models = new Map<string, AvailableRow>()
    let fallbackState = 'unlisted'
    for (const { catalog } of group.catalogs) {
      if (catalog?.state === 'fresh' && catalog.models.length > 0) {
        for (const m of catalog.models) {
          const key = toolKey(group.driver, group.viaProvider, m.id)
          const isDefault =
            !!m.is_default ||
            (catalog.default_model !== '' && catalog.default_model === m.id)
          const known = models.get(key)
          if (known) {
            known.isDefault ||= isDefault
            continue
          }
          models.set(key, {
            key,
            source: 'tool',
            owner: group.driver,
            model: m.id,
            label: m.display_name || m.id,
            state: 'ready',
            viaProvider: group.viaProvider || undefined,
            isDefault,
            sessions: [],
          })
        }
      } else if (!catalog || catalog.state === 'failed') {
        fallbackState = 'failed'
      }
    }
    const rows = [...models.values()]
    // A fresh OpenCode list has no default marker. Add the honest own-default target only
    // when a live launch needs it; do not invent a default by choosing the first model.
    if (
      rows.length === 0 ||
      (defaultDrivers.has(group.driver) && !rows.some((r) => r.isDefault))
    ) {
      rows.push({
        key: toolKey(group.driver, group.viaProvider),
        source: 'tool',
        owner: group.driver,
        model: '',
        label: '',
        state: rows.length > 0 ? 'ready' : fallbackState,
        viaProvider: group.viaProvider || undefined,
        sessions: [],
      })
    }
    return rows
  })
}

function ollamaRows(ollama: OllamaStatus | undefined): AvailableRow[] {
  if (!ollama?.installed) return []
  const state =
    ollama.state === 'running'
      ? 'ready'
      : ollama.state === 'starting'
        ? 'starting'
        : ollama.state === 'failed'
          ? 'ollamaFailed'
          : 'stopped'
  // The engine lists the installed models only while Ollama runs, so an installed Ollama
  // that is not running is one row without a model: its models are on disk, the step is
  // to start it.
  const models = ollama.state === 'running' ? ollama.models : ['']
  return models.map((model) => ({
    key: `ollama:${model}`,
    source: 'ollama' as const,
    owner: 'ollama',
    model,
    label: model,
    state,
    sessions: [],
  }))
}

function sameEndpoint(a: string | undefined, b: string | undefined): boolean {
  return !!a && !!b && a.replace(/\/+$/, '') === b.replace(/\/+$/, '')
}

/** The display name of each provider record, by its reference. */
export function providerNames(
  providers: ProviderRecordDTO[],
): Map<string, string> {
  return new Map(
    providers.map((record) => [record.provider_ref, record.display_name]),
  )
}

function providerRows(
  availability: ModelAvailability[],
  catalogs: ToolCatalog[],
  providers: ProviderRecordDTO[],
  ollama: OllamaStatus | undefined,
  installedOllama: AvailableRow[],
): AvailableRow[] {
  const installed = new Set(
    installedOllama.map((row) => row.model.toLowerCase()),
  )
  const localOllama = new Set(
    providers
      .filter(
        (record) =>
          record.kind === 'ollama' &&
          sameEndpoint(record.base_url, ollama?.endpoint),
      )
      .map((record) => record.provider_ref),
  )
  const represented = new Set(
    catalogs.flatMap((entry) => {
      if (!entry.providerRef || entry.catalog?.state !== 'fresh') return []
      return entry.catalog.models.map(
        (model) => `${entry.providerRef}:${model.id.toLowerCase()}`,
      )
    }),
  )
  const listedByProfile = new Set(
    catalogs.flatMap((entry) =>
      entry.providerRef && entry.catalog?.state === 'fresh'
        ? [entry.providerRef]
        : [],
    ),
  )
  const names = providerNames(providers)
  return availability.flatMap((source) => {
    const providerRef = source.provider_ref
    if (!providerRef) return []
    // The Ollama rows (or the empty state) and a fresh profile list already speak for
    // these: a row of their own would name the same endpoint twice.
    if (
      source.models.length === 0 &&
      (localOllama.has(providerRef) || listedByProfile.has(providerRef))
    )
      return []
    if (source.models.length === 0)
      return [
        {
          key: `provider:${providerRef}`,
          source: 'provider' as const,
          owner: names.get(providerRef) || providerRef,
          providerRef,
          providerKind: source.provider_kind,
          model: '',
          label: '',
          state: source.state,
          sessions: [],
        },
      ]
    return source.models
      .filter(
        (model) =>
          !represented.has(`${providerRef}:${model.id.toLowerCase()}`) &&
          (source.provider_kind !== 'ollama' ||
            !localOllama.has(providerRef) ||
            !installed.has(model.id.toLowerCase())),
      )
      .map((model) => ({
        key: `provider:${providerRef}:${model.id.toLowerCase()}`,
        source: 'provider' as const,
        owner: names.get(providerRef) || providerRef,
        providerRef,
        providerKind: source.provider_kind,
        model: model.id,
        label: model.id,
        state: source.state,
        sessions: [],
      }))
  })
}

/** The models a run names: the one it was asked to use, then the one its tool reported using. */
function modelsOf(run: RunDTO): string[] {
  return [run.model_ref, run.usage_model_ref]
    .filter((m): m is string => !!m)
    .map((m) => m.toLowerCase())
}

// ponytail: a run joins a provider row by profile and provider, but tool rows still join by
// driver and model. With two accounts of one tool that default to different models, or one
// bound to a provider and one not that both list a model, the run lands on the first matching
// tool row. Add profile identity to the tool row when a tenant runs such accounts side by side.
/** The tool's default row, for a launch that named no model: the model its list marks (or
 * `default_model` names), else the row of a tool that lists nothing. */
function defaultRowOf(
  rows: AvailableRow[],
  run: RunDTO,
): AvailableRow | undefined {
  return rows.find(
    (r) =>
      r.source === 'tool' &&
      r.owner === run.provider_driver &&
      (r.isDefault || r.model === ''),
  )
}

/** The one row a run belongs to. A launch on the tool's default (no `model_ref`) belongs to
 * that tool's default row, whatever full id the provider reports after the first turn.
 * A bound provider's own row wins over any local Ollama row with the same model id. */
function rowOf(
  rows: AvailableRow[],
  run: RunDTO,
  profileProviders: ReadonlyMap<string, string | undefined>,
): AvailableRow | undefined {
  if (!run.model_ref) {
    const own = defaultRowOf(rows, run)
    if (own) return own
  }
  const providerRef = run.provider_profile_ref
    ? profileProviders.get(run.provider_profile_ref)
    : undefined
  const identityUnavailable =
    !!run.provider_profile_ref &&
    !profileProviders.has(run.provider_profile_ref)
  for (const ref of modelsOf(run)) {
    const ownRows = rows.filter(
      (r) =>
        r.source === 'tool' &&
        r.model !== '' &&
        r.owner === run.provider_driver,
    )
    const own =
      ownRows.find((r) => r.model.toLowerCase() === ref) ??
      // A list the tool took from a provider names the bare model; the run names it under
      // that provider (`olivares_ollama/<model>`).
      ownRows.find(
        (r) => r.viaProvider && ref.endsWith(`/${r.model.toLowerCase()}`),
      )
    if (own) return own
    if (providerRef) {
      const bound = rows.find(
        (r) =>
          r.source === 'provider' &&
          r.providerRef === providerRef &&
          (r.model.toLowerCase() === ref ||
            (r.providerKind === 'ollama' &&
              (ref === `ollama/${r.model.toLowerCase()}` ||
                ref === `olivares_ollama/${r.model.toLowerCase()}`))),
      )
      // Do not guess another endpoint when the bound provider has no matching row.
      if (bound) return bound
      continue
    }
    if (identityUnavailable) continue
    const row =
      rows.find(
        (r) =>
          r.source === 'ollama' &&
          r.model !== '' &&
          (r.model.toLowerCase() === ref ||
            ref === `ollama/${r.model.toLowerCase()}` ||
            ref === `olivares_ollama/${r.model.toLowerCase()}`),
      ) ??
      rows.find((r) => r.source === 'provider' && r.model.toLowerCase() === ref)
    if (row) return row
    if (!run.provider_driver) {
      const candidates = rows.filter(
        (r) =>
          r.source === 'tool' &&
          r.model !== '' &&
          r.model.toLowerCase() === ref,
      )
      if (candidates.length === 1) return candidates[0]
    }
  }
  return undefined
}

export function buildAvailableRows(input: AvailableInput): AvailableRow[] {
  const defaultDrivers = new Set(
    input.runs
      .filter((run) => isLiveRun(run) && !run.model_ref)
      .map((run) => run.provider_driver)
      .filter((driver): driver is string => !!driver),
  )
  const installedOllama = ollamaRows(input.ollama)
  const rows = [
    ...toolRows(input.catalogs, defaultDrivers),
    ...installedOllama,
    ...providerRows(
      input.availability,
      input.catalogs,
      input.providers ?? [],
      input.ollama,
      installedOllama,
    ),
  ]
  const profileProviders = new Map<string, string | undefined>(
    input.catalogs
      .filter(
        (catalog): catalog is ToolCatalog & { profileRef: string } =>
          !!catalog.profileRef,
      )
      .map((catalog) => [catalog.profileRef, catalog.providerRef] as const),
  )
  const loadingDrivers = new Set(
    input.catalogs.filter((c) => c.loading).map((c) => c.driver),
  )
  for (const run of input.runs) {
    if (!isLiveRun(run)) continue
    let row = rowOf(rows, run, profileProviders)
    // A session that only another source's row names waits for its tool's list: the list,
    // once it arrives, would move it to the tool's own row.
    if (
      row &&
      row.source !== 'tool' &&
      run.provider_driver &&
      loadingDrivers.has(run.provider_driver)
    )
      continue
    // A live run is proof that its exact model works, even when the tool's own list
    // uses aliases or omitted it. Name that fact instead of guessing an alias or a
    // provider. The next catalog refresh folds it into the ordinary tool row.
    if (!row && run.provider_driver && input.profilesSettled !== false) {
      const model = run.model_ref || run.usage_model_ref
      if (model) {
        const profiles = input.catalogs.filter(
          (catalog) => catalog.driver === run.provider_driver,
        )
        const own = profiles.find(
          (catalog) =>
            catalog.profileRef !== undefined &&
            catalog.profileRef === run.provider_profile_ref,
        )
        const bindings = new Set(
          profiles.map((catalog) => !!catalog.viaProvider),
        )
        const identity =
          own ??
          (profiles.length > 0 && bindings.size === 1 ? profiles[0] : undefined)
        // A provisional session row keeps the key of the tool row that will replace it.
        // Without a profile or one unambiguous binding, that identity is not known yet.
        const key = identity
          ? toolKey(run.provider_driver, !!identity.viaProvider, model)
          : `session:${run.provider_driver}:${model.toLowerCase()}`
        row = rows.find((r) => r.key === key)
        if (!row) {
          row = {
            key,
            source: 'session',
            owner: run.provider_driver,
            model,
            label: model,
            state: 'ready',
            sessions: [],
          }
          rows.push(row)
        }
      }
    }
    row?.sessions.push(run)
  }
  return rows
}
