// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentType } from 'react'

export interface RunOnExtensionProps {
  value: string
  onChange: (node: string) => void
  pending: boolean
}

/** Business supplies the fleet selector; Community mounts no control. */
export type RunOnExtension = ComponentType<RunOnExtensionProps>
