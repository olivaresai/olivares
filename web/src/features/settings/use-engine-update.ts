// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The engine's own update, shared by Settings > Updates and the licensed-edition switch: ask
// the engine what it found (`olivares upgrade --check`), apply it (`olivares upgrade`, then the
// engine restarts itself on the new binary), wait for the engine to come back and read the
// result. One mechanism: the licensed switch is the same apply, on a different release.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { waitForEngineRestart } from '@/features/console/engine-restart'
import { http, queryKeys } from '@/lib/api'
import { ApiError } from '@/lib/api/errors'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'

/** The one next step from a Community build to the licensed build, for this installation (core/api/updates.go). */
export interface EditionSwitchStep {
  /** The edition the license entitles; Business and Enterprise are one licensed build. */
  to: 'business' | 'enterprise'
  install: 'binary' | 'container' | 'package' | 'kubernetes'
  /** The exact command for this installation; absent where the manifest does it. */
  command?: string
  then: string
  /** The engine's apply takes the step itself, so the console offers a button. */
  console_action: boolean
}

/** GET /v1/console/updates and POST /v1/console/updates/apply (core/api/updates.go). */
export interface EngineUpdate {
  edition: string
  /** The version the engine runs now; after an apply it stays the old one until the engine restarts. */
  version: string
  channel?: string
  /** The newest release in the channel, when the check reached it. */
  available?: string
  update_available: boolean
  released_at?: string
  notes?: string
  security?: boolean
  advisories?: string[]
  /** Why the channel could not be checked, in the words `olivares upgrade` uses. */
  check_error?: string
  /** A newer binary than the running one is already installed; the engine runs the old version until it restarts. */
  restart_required?: boolean
  restarting?: boolean
  /** Sessions the engine runs now; the restart that applies an update stops them. */
  running_sessions: number
  /** Present on a Community build with a live Business or Enterprise license. */
  edition_switch?: EditionSwitchStep
}

export const updatesApi = {
  get: () => http.get<EngineUpdate>('/v1/console/updates'),
  apply: () => http.post<EngineUpdate>('/v1/console/updates/apply', {}),
}

export const updatesKey = ['settings', 'updates'] as const

/** What the engine answers as: its version and its edition. A switch changes only the edition. */
interface EngineIdentity {
  version?: string
  edition?: string
}

async function engineIdentity(): Promise<EngineIdentity> {
  try {
    return await http.get<EngineIdentity>('/v1/server-info', {
      anonymous: true,
    })
  } catch {
    return {}
  }
}

/** The engine is back when it answers as something other than what it was: a new version, or the licensed edition. */
function changedFrom(from: EngineIdentity, now: EngineIdentity): boolean {
  if (now.version === undefined) return false
  return now.version !== from.version || now.edition !== from.edition
}

/**
 * An apply whose answer never arrived (a dropped connection, or a proxy that gave up
 * on a long request): the engine keeps installing and restarts, so the page waits for
 * it instead of calling the update refused. The engine's own refusals carry `update_*`.
 */
function answerLost(err: unknown): boolean {
  if (!(err instanceof ApiError)) return true
  return err.status >= 502 && !err.code.startsWith('update_')
}

export type UpdatePhase = 'idle' | 'waiting' | 'stuck'

/** Mounted for a system administrator only; the engine checks system:admin and the step-up itself. */
export function useEngineUpdate() {
  const { t } = useTranslation('settings')
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: updatesKey,
    queryFn: () => updatesApi.get(),
    retry: false,
    refetchOnWindowFocus: false,
  })
  const [phase, setPhase] = useState<UpdatePhase>('idle')
  const [refusal, setRefusal] = useState<string | null>(null)

  const waitForNewEngine = (from: EngineIdentity) => {
    setPhase('waiting')
    void waitForEngineRestart({
      probe: async () => changedFrom(from, await engineIdentity()),
    }).then((ready) => {
      setPhase(ready ? 'idle' : 'stuck')
      void queryClient.invalidateQueries({ queryKey: updatesKey })
      void queryClient.invalidateQueries({ queryKey: queryKeys.serverInfo })
    })
  }

  const apply = usePrivilegedMutation<void, EngineUpdate>({
    mutationFn: () => updatesApi.apply(),
    successMessage: (status) =>
      status.restarting
        ? t(
            query.data?.edition_switch?.console_action
              ? 'updates.switched'
              : 'updates.applied',
          )
        : t('updates.alreadyCurrent'),
    stepUpAction: 'updates',
    onError: (err) => {
      if (answerLost(err)) {
        waitForNewEngine({
          version: query.data?.version ?? '',
          edition: query.data?.edition,
        })
        return true
      }
      setRefusal(err instanceof Error ? err.message : t('updates.failed'))
      void queryClient.invalidateQueries({ queryKey: updatesKey })
      return true
    },
    onDone: (status) => {
      if (!status.restarting) {
        void queryClient.invalidateQueries({ queryKey: updatesKey })
        return
      }
      // No read until the engine is back: the page keeps showing what it knew, with the
      // install disabled, and the answer to the apply (running version old, newest new)
      // would read as "up to date" if it were shown as a status.
      waitForNewEngine({ version: status.version, edition: status.edition })
    },
  })

  return {
    query,
    apply,
    phase,
    refusal,
    clearRefusal: () => setRefusal(null),
    busy: apply.isPending || phase === 'waiting',
  }
}
