// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import "strings"

// The SELinux domains of the engine's network git on the Olivares Server. The policy lets only the engine
// enter them, by naming one for its next exec; every other git the engine runs enters the command domain.
const (
	// GitPublishDomain is gitpublish's git: its repositories, a session repository read only, https and ssh.
	GitPublishDomain = "olivares_gitpub_t"
	// GitImportDomain is the skills Git import's git: its staging and the engine's loopback relay, nothing else.
	GitImportDomain = "olivares_git_import_t"
)

// engineDomain is the engine's domain under the Olivares Server policy. Only there does a named domain exist.
const engineDomain = "olivares_t"

// execContext is the context the next exec should enter: current with its type replaced by domain, when current is
// the engine's context under the Olivares policy. Anywhere else (no SELinux, another policy, another domain) it
// reports false and the command starts unchanged.
func execContext(current, domain string) (string, bool) {
	fields := strings.SplitN(strings.TrimRight(current, "\x00\n"), ":", 4)
	if len(fields) != 4 || fields[2] != engineDomain {
		return "", false
	}
	fields[2] = domain
	return strings.Join(fields, ":"), true
}
