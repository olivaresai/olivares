// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { toolName } from '@/features/agentops/tool-names'
import type { ProviderSnapshot, ToolInventory } from './api'

export const toolLabel = (driver: string): string =>
  driver === 'ollama' ? 'Ollama' : toolName(driver)
/** "codex-cli 0.162.0" is the tool's own words; the list says "0.162.0". */
const versionNumber = (raw?: string) =>
  raw?.match(/\d+(?:\.\d+)+\S*/)?.[0] ?? raw

/** The Tools page and palette agree on managed and externally installed tools. */
export function toolFacts(
  inventory: ToolInventory | undefined,
  snaps: ProviderSnapshot[],
) {
  const drivers = [
    ...new Set([
      ...(inventory?.drivers ?? []),
      ...snaps.map((snap) => snap.driver),
    ]),
  ]
  return drivers.map((driver) => {
    const installs =
      inventory?.inventory.installed.filter((row) => row.driver === driver) ??
      []
    const own = snaps.filter((s) => s.driver === driver)
    const managed = installs.find((row) => row.state === 'installed')
    return {
      driver,
      installs,
      installed: !!managed || own.some((s) => s.installed),
      version: versionNumber(
        managed?.version ?? own.find((s) => s.version)?.version,
      ),
      snapshots: own,
    }
  })
}
