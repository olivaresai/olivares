// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// This file makes ONE spelling — `--server`, with `OLIVARES_SERVER_URL` behind
// it — reach the command groups that each invented their own (E7).
//
// Measured before it existed, on the built binary: three conventions for the
// same idea, and an operator who learned one got nothing from the others.
//
//	--server    OLIVARES_SERVER_URL         status, agent, findings, mcp, auth
//	--url       OLIVARES_HOOK_PEP_URL       hookpep
//	--endpoint  OLIVARES_CODEX_HOOK_URL     codex-hook (and claude-hook)
//
// Nothing is removed. The old flag and the old environment variable keep
// working, and using the old FLAG says once, on stderr, what the canonical
// spelling is. Removing them would break every operator script and every
// deployment manifest that already sets them, which is the opposite of the point
// — the point is that a person who has learned `--server` can now use it
// everywhere, and a person who has not is told, once, where to look.

// canonicalServerFlag is the spelling the rest of the CLI uses.
const canonicalServerFlag = "server"

// canonicalServerEnv is the environment variable behind it.
const canonicalServerEnv = "OLIVARES_SERVER_URL"

// addServerAliasFlag declares `--server` next to a group's legacy spelling and
// returns a resolver.
//
// The resolver applies a precedence that cannot surprise anyone: an explicitly
// passed flag wins over any environment variable, the legacy env var wins over
// the canonical one (a deployment that already sets OLIVARES_HOOK_PEP_URL keeps
// working exactly as it did), and between the two flags the legacy one wins
// while warning — because if somebody passed it, they meant it.
func addServerAliasFlag(cmd *cobra.Command, target *string, legacyFlag, legacyEnv string, persistent bool) func() string {
	flags := cmd.Flags()
	if persistent {
		flags = cmd.PersistentFlags()
	}
	var canonical string
	flags.StringVar(&canonical, canonicalServerFlag, "",
		fmt.Sprintf("control-plane base URL (default $%s; the canonical spelling of --%s)",
			canonicalServerEnv, legacyFlag))

	return func() string {
		legacyPassed := flags.Changed(legacyFlag)
		canonicalPassed := flags.Changed(canonicalServerFlag)
		switch {
		case legacyPassed:
			if canonicalPassed {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"--%s and --%s were both passed; using --%s. --%s is the canonical spelling "+
						"across the CLI and --%s is kept only for compatibility\n",
					legacyFlag, canonicalServerFlag, legacyFlag, canonicalServerFlag, legacyFlag)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"--%s still works, and --%s is the canonical spelling this CLI uses everywhere else\n",
					legacyFlag, canonicalServerFlag)
			}
			return strings.TrimRight(strings.TrimSpace(*target), "/")
		case canonicalPassed:
			return strings.TrimRight(strings.TrimSpace(canonical), "/")
		}
		// Neither flag: environment, legacy first so an existing deployment is
		// untouched by the new variable appearing.
		//
		// TrimSpace BEFORE deciding non-empty. firstNonEmptyEnv treats whitespace
		// as a value, so a legacy variable holding only spaces used to shadow a
		// valid canonical one and then trim to "" — a silent nothing. Found by the
		// sol-max contrast.
		legacyValue := strings.TrimSpace(osGetenv(legacyEnv))
		canonicalValue := strings.TrimSpace(osGetenv(canonicalServerEnv))
		if legacyValue != "" && canonicalValue != "" && legacyValue != canonicalValue {
			// Deterministic, but say so: a stale legacy variable silently winning
			// over a newly rotated canonical one is the migration hazard here, and
			// it used to happen with no output at all.
			fmt.Fprintf(cmd.ErrOrStderr(),
				"$%s and $%s are both set and differ; using $%s (the legacy variable wins so an "+
					"existing deployment is unaffected). Unset $%s to use $%s\n",
				legacyEnv, canonicalServerEnv, legacyEnv, legacyEnv, canonicalServerEnv)
		}
		if legacyValue != "" {
			return strings.TrimRight(legacyValue, "/")
		}
		return strings.TrimRight(canonicalValue, "/")
	}
}

// missingServerError names EVERY place a server could have come from, including
// the two spellings and the client contexts — the same discipline as
// missingCLIValueError, for the groups that do not use client contexts.
func missingServerError(legacyFlag, legacyEnv string) error {
	return fmt.Errorf("no server: pass --%s (or --%s), or set $%s (or $%s)",
		canonicalServerFlag, legacyFlag, canonicalServerEnv, legacyEnv)
}
