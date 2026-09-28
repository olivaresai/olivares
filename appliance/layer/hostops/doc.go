// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package hostops records host operations and is the only read model the
// console, the command line and the terminal frame share.
//
// The client mints the operation id when the operator confirms. Submitting
// that id again with the same plan returns the recorded operation and does not
// run its effect again. A changed plan refuses with plan_changed. The record names the task, the closed module, verb and target, the
// plan digest, the mode, the surface and the actor. Its log reference is the
// journal field for that id and nothing else.
package hostops
