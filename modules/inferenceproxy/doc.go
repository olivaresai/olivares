// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package inferenceproxy owns the per-tenant GOVERNANCE CONFIG and the inference-egress
// DLP policy for the inline inference PEP proxy — the OPTIONAL, OPT-IN gateway
// that fronts api.anthropic.com (the Claude /v1/messages contract) and runs a governed
// pipeline (residency, model-access, context-window, DLP, budget, recording) IN-BAND
// before forwarding, for traffic that is NOT Claude Code (raw SDK/curl callers) — the
// enforcement Anthropic's managed-settings cannot reach (a custom ANTHROPIC_BASE_URL
// bypasses server-managed-settings entirely).
//
// This module DECIDES NOTHING about a live request. It is the persistence + authoring
// surface only: a per-tenant config singleton (the per-gate toggles, the proxy-down
// fail posture, the streaming-response DLP mode, the recording mandate, and numeric
// per-request consumption ceilings) and the inference DLP rule set (per sensitivity
// class → allow|deny, the algebra). The composition root reads it via Policy() and
// composes the actual decision from the existing /core + /modules seams
// (models.EvaluateModelAccess, finops.CheckBudget, governance.KillSwitchState,
// core/residency, security.ClassifySensitivity, claudeapi.CheckContextWindowForSurface)
// — the module NEVER imports another module, exactly like every other module (the
// composition pattern: cmd composes).
//
// The protocol shell (parse /v1/messages, forward, tee the bodies through SHA-256) is
// the Apache connector connectors/claude-api (identity-blind); the governed decider is
// cmd/olivares (AGPL). This module is the third leg: the durable, console-authorable
// policy the decider consults — keeping the governance decision out of the
// Apache connector (the open-core boundary, LICENSING.md; scripts/check-boundary.sh).
//
// Minimal data (docs/SECURITY-HARDENING.md): no row this module persists — config, DLP rule, audit —
// ever carries a prompt, response, secret or matched PII value. The DLP rule names a
// class and an action; the config names toggles. The request/response bytes the proxy
// inspects in flight are fingerprinted (SHA-256) and anchored to the ledger by the
// composition root, never stored here.
package inferenceproxy
