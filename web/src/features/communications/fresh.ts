// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { ApiError } from '@/lib/api/errors'
import { classifyFailure, isAbortError, type Failure } from './errors'

/**
 * The answer of one FRESH point read, as `useFreshRead` carries it: either the
 * value, or the classified refusal that replaced whatever was open before.
 *
 * `useFreshRead` already turns a 403 into its own `forbidden` state and keeps only
 * the message of any other error. The sheets need more than the message — a 404
 * (absent, or concealed), a 503 (the engine could not look) and a 412 are three
 * different things to say — so the read returns the classified failure as a VALUE
 * for everything but forbidden and abort, which keep the hook's own paths. Either
 * way the previous content is gone: the hook replaces its state on every cycle.
 */
export type Fresh<T> = { ok: true; value: T } | { ok: false; failure: Failure }

export async function freshly<T>(read: Promise<T>): Promise<Fresh<T>> {
  try {
    return { ok: true, value: await read }
  } catch (err) {
    if (isAbortError(err)) throw err
    if (err instanceof ApiError && err.isForbidden && !err.isStepUpRequired) {
      throw err
    }
    return { ok: false, failure: classifyFailure(err) }
  }
}
