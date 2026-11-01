// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE MODEL AND EFFORT CONTROLS KNOW: the live catalog of one profile, read under the
// authority boundary, and what follows from the current choice (the model a session starts
// on, the effort levels it takes). The controls themselves are model-effort-picker.tsx.
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi, agentOpsKeys } from './api'
import { useAuthBoundary } from './auth-boundary'
import { effortChoices, startModel } from './model-catalog'
import { toolName } from './engine-tools'
import type { ProfileModelCatalogDTO, ProfileModelDTO } from './types'
import './i18n'

/** The engine caches a catalog for two minutes; asking again sooner only repeats it. */
const CATALOG_STALE_MS = 120_000

export type CatalogStatus =
  | 'none' // no profile to ask about, or no right to read it
  | 'loading'
  | 'fresh'
  | 'unsupported'
  | 'failed'

export interface ModelChoices {
  status: CatalogStatus
  /** The catalog, only while it is a real answer with models. */
  catalog: ProfileModelCatalogDTO | undefined
  /** The model a session starts on with the current choice; undefined while none is known. */
  start: ProfileModelDTO | undefined
  /** The effort levels of that model, or null while no model is known. */
  efforts: string[] | null
}

/**
 * The profile's catalog and what follows from the current choice. `fallbackModel` is the
 * model a launch uses when none is picked and the catalog does not say (a key's saved
 * default); the catalog's own default wins over it.
 */
export function useModelChoices({
  profileRef,
  driver,
  model,
  enabled,
  fallbackModel = '',
}: {
  profileRef: string
  driver: string
  model: string
  enabled: boolean
  fallbackModel?: string
}): ModelChoices {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const allowed =
    enabled && !!profileRef && !!activeTenant && can('sessions:profile:read')
  const query = useQuery({
    queryKey: agentOpsKeys.profileModels(
      activeTenant,
      boundary.epoch,
      profileRef,
    ),
    queryFn: ({ signal }) =>
      agentOpsApi.profileModels(profileRef, { tenant: activeTenant, signal }),
    enabled: allowed,
    staleTime: CATALOG_STALE_MS,
    retry: false,
  })
  const answer = allowed && !query.isError ? query.data : undefined
  const status: CatalogStatus = !allowed
    ? 'none'
    : query.isError
      ? 'failed'
      : !answer
        ? 'loading'
        : answer.state === 'fresh' && answer.models.length > 0
          ? 'fresh'
          : answer.state === 'failed'
            ? 'failed'
            : 'unsupported'
  const catalog = status === 'fresh' ? answer : undefined
  const start = startModel(catalog, model || fallbackModel)
  return { status, catalog, start, efforts: effortChoices(driver, start) }
}

export function modelLabel(m: ProfileModelDTO): string {
  return m.display_name || m.id
}

/** What a model trigger shows: the chosen model, else the model the tool starts on, else
 * the tool's default said as such. */
export function useModelTriggerText(
  driver: string,
  choices: ModelChoices,
  model: string,
): string {
  const { t } = useTranslation('agentops')
  if (model) {
    const listed = choices.catalog?.models.find((m) => m.id === model)
    return listed ? modelLabel(listed) : model
  }
  return choices.start
    ? t('picker.defaultNamed', { model: modelLabel(choices.start) })
    : t('picker.defaultUnnamed', { tool: toolName(driver) })
}

/** The effort a different model keeps: the same level when the new model takes it. */
export function effortAfterModel(
  driver: string,
  catalog: ProfileModelCatalogDTO | undefined,
  model: string,
  fallbackModel: string,
  effort: string,
): string {
  const next = effortChoices(
    driver,
    startModel(catalog, model || fallbackModel),
  )
  return effort && next?.includes(effort) ? effort : ''
}
