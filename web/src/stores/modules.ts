// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useEffect } from 'react'
import { create } from 'zustand'
import { useServerInfo } from '@/lib/hooks/use-server-info'

/**
 * The engine modules this installation runs without (ARCH C1). A module that is not
 * enabled answers 404 module_not_enabled, so its pages, Home tiles and polls are not
 * shown at all: an operator never sees an error for something simply switched off.
 *
 * The list comes from GET /v1/server-info `modules_not_enabled`; absent means every
 * module runs. It is kept here, outside the query cache, so the navigation gate and
 * the views read one answer without each mounting the read.
 */
interface ModulesState {
  off: ReadonlySet<string>
  setOff: (ids: readonly string[] | undefined) => void
}

export const useModulesStore = create<ModulesState>((set) => ({
  off: new Set(),
  setOff: (ids) => set({ off: new Set((ids ?? []).map(moduleKey)) }),
}))

/** One spelling for a module id: lowercase, as in /v1/m/<id>. */
function moduleKey(id: string): string {
  return id.trim().toLowerCase()
}

/** The module a permission belongs to: its first segment (finops:spend:read → finops),
 * the same name as the module's routes (/v1/m/finops). */
export function moduleOfPermission(permission?: string): string | undefined {
  const i = permission?.indexOf(':') ?? -1
  return i > 0 ? moduleKey(permission!.slice(0, i)) : undefined
}

/** Views whose module is not their permission's first segment (ARCH, 2026-10-01): the
 * Claude policy console reads governance:claude-policy:* from /v1/m/claude-policy, the
 * Observability page is the observability module's, and
 * the K3 communication screens read sessions permissions but work only while the
 * communication plane is effective ("communication" is this console's flag for that,
 * not an engine module id). */
const VIEW_MODULE: Readonly<Record<string, string>> = {
  claudePolicy: 'claude-policy',
  // The Observability page asks health:status:read but reads /v1/m/observability (EU18).
  observability: 'observability',
  communications: 'communication',
  communicationsInbox: 'communication',
  communicationsNew: 'communication',
  communicationsHandoffs: 'communication',
  communicationsAdministration: 'communication',
}

/** The module a view belongs to: its own entry, else its permission's first segment. */
export function moduleOfView(
  permission?: string,
  viewId?: string,
): string | undefined {
  return (
    (viewId ? VIEW_MODULE[viewId] : undefined) ?? moduleOfPermission(permission)
  )
}

/** Whether the module behind a view (or a bare permission) runs here. No module: on. */
export function moduleEnabled(
  off: ReadonlySet<string>,
  permission?: string,
  viewId?: string,
): boolean {
  const module = moduleOfView(permission, viewId)
  return !module || !off.has(module)
}

/** Whether module `id` (as in /v1/m/<id>) runs on this installation. A panel that reads
 * another module's routes asks this before it mounts its reads (EU18). */
export function useModuleOn(id: string): boolean {
  const off = useModulesStore((s) => s.off)
  return !off.has(moduleKey(id))
}

/** The same answer outside React (a loader, an API wrapper): read once, not subscribed. */
export function moduleOn(id: string): boolean {
  return !useModulesStore.getState().off.has(moduleKey(id))
}

/** `(permission, viewId?) => boolean` for the current installation. */
export function useModuleEnabled(): (
  permission?: string,
  viewId?: string,
) => boolean {
  const off = useModulesStore((s) => s.off)
  return (permission, viewId) => moduleEnabled(off, permission, viewId)
}

/** Mounted once by the shell: keeps the store in step with server-info. Its
 * `communication_ready` says whether the K3 communication plane is effective; until it
 * is, the communication screens are hidden. No route is probed for it, so a plane that
 * is staged by design never shows up as failed requests. */
export function useSyncModulesNotEnabled(): void {
  const info = useServerInfo().data
  const list = info?.modules_not_enabled
  const k3Off = info !== undefined && info.communication_ready !== true
  const setOff = useModulesStore((s) => s.setOff)
  const key = list?.join(',') ?? ''
  useEffect(() => {
    setOff([
      ...(key ? key.split(',') : []),
      ...(k3Off ? ['communication'] : []),
    ])
  }, [key, k3Off, setOff])
}
