// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package scaffold generates a complete, compiling, boundary-clean OUT-OF-TREE
// connector repository — the starting point of "build your first connector"
// (S142). It renders one of the five CONNECTOR-SDK archetype templates
// (content-source, access-edge-source, output-sink, agent-surface,
// model-provider) plus a lifecycle test, a README that walks build → test →
// sign → register → operate, a standalone check-boundary.sh, and optionally
// the go-plugin main, into a fresh Go module the author owns.
//
// # Boundary by construction
//
// A third-party connector links only the Apache-2.0 SDK
// (github.com/olivaresai/olivares/sdk, plus sdk/plugin when it ships as a
// plugin) and must never link the AGPL engine (LICENSING.md). The generated code
// imports nothing else, and the generated scripts/check-boundary.sh enforces
// the same frontier the upstream repo enforces in CI — on the real
// `go list -deps` build graph, in the author's own CI.
//
// # Deny-closed validation
//
// Generate refuses unusable input with a precise error instead of degrading
// (house style): a malformed Name/Module/Kind/Template, an SDKPath
// that is not actually a checkout of the SDK module, and — never overwrite user
// work — a non-empty target directory are all hard refusals, not best-effort
// writes.
//
// # CLI
//
// cmd/olivares-connector-new is the stdlib-flag CLI over Generate:
//
//	olivares-connector-new -dir ./widget-audit -name acme.widget-audit \
//	    -module github.com/acme/olivares-connector-widget-audit \
//	    -kind source -plugin -sdk-path ~/src/olivares/sdk
//
// This module is intentionally stdlib-only (text/template + embed), so the
// scaffold itself adds nothing to an author's supply chain.
package scaffold
