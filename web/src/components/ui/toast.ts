// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE LIFETIME RULE OF A TOAST, ONCE: it lasts 6 seconds and an error stays until
// the person dismisses it. `Toaster` (toaster.tsx) is the surface and sets the 6 seconds; this
// is the `toast` every caller imports (through toaster.tsx), so the error rule holds for each
// of the console's `toast.error(...)` calls without touching them. A caller that needs another
// lifetime passes `duration`.
import { toast as sonnerToast } from 'sonner'

export const TOAST_MS = 6_000

/** sonner's `toast`, with the error lifetime of the rule above. */
export const toast: typeof sonnerToast = Object.assign(
  (...args: Parameters<typeof sonnerToast>) => sonnerToast(...args),
  sonnerToast,
  {
    error: (...[message, data]: Parameters<typeof sonnerToast.error>) =>
      sonnerToast.error(message, { duration: Infinity, ...data }),
  },
)
