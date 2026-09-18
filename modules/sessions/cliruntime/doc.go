// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package cliruntime is the Community contract for an owned official-CLI child.
//
// It covers launch, stdin/stdout/stderr streaming, attach, resume, reconnect
// after process loss, and stop with an observed exit status. The three kinds
// are Claude Code, Codex CLI and Grok CLI. Olivares integrates those programs;
// it does not replace them.
//
// This package is process custody and I/O. JSON-RPC / stream-json codecs stay
// in the parent sessions module (ProviderDriver). The Identity & Scale overlay
// adds the multi-pane listener engine; it is not implemented here.
package cliruntime
