// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The folder a session works in, by its last name ("repo" for /srv/work/repo); the full
 * path goes on `title`. Null when the run reported no folder. */
export function folderName(path: string | undefined | null): string | null {
  const trimmed = path?.replace(/[/\\]+$/, '') ?? ''
  if (!trimmed) return null
  const parts = trimmed.split(/[/\\]/)
  return parts[parts.length - 1] || trimmed
}

/** A run's folder by name, or null when it is the folder Olivares created for the run
 * itself (named by the run's id, which tells a person nothing). */
export function runFolder(
  run: { workspace_path?: string; run_ref: string } | undefined,
): string | null {
  const name = folderName(run?.workspace_path)
  return name && name !== run?.run_ref ? name : null
}
