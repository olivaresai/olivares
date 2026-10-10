// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "github.com/olivaresai/olivares/core/envconfig"

// engineEnvironment keeps process values ahead of the existing activation
// manifest for activation-aware loaders. Process-only settings (credentials,
// custody, CLI contexts) use envconfig.Get without extending their sources.
var engineEnvironment = envconfig.Reader{Fallback: activationManifestLookup}

// osGetenv is the composition root's effective environment reader. Keep this
// entry point for loaders and edition overlays that accept a getenv function.
func osGetenv(key string) string { return engineEnvironment.Get(key) }
