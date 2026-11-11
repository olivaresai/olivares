// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** The name a folder was registered under, else the last segment of its path. */
export const folderName = (w: { name?: string; root_path: string }) =>
  w.name || w.root_path.split('/').filter(Boolean).pop() || w.root_path
