// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE BUILD EDITION, as /v1/server-info reports it. The console shows only the controls
// this build serves: a surface that a Community build can only answer with 501 is not
// offered there. Business builds show it unchanged, and so does an engine that does not
// name its edition (an embedder) — nothing is hidden on a guess.
import { useServerInfo } from './use-server-info'

/** The edition the engine names ("community" for the default build), or undefined. */
export function useEdition(): string | undefined {
  return useServerInfo().data?.edition
}

/** True only when the engine names this build the Community edition. */
export function useCommunityBuild(): boolean {
  return useEdition() === 'community'
}
