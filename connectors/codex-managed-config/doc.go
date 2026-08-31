// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package codexmanagedconfig governs OpenAI Codex's managed configuration — the
// enforcement-posture leg the read-only codex governance connector (analytics/
// costs/compliance/audit, connectors/codex) does not have. It is the Codex mirror
// of connectors/managedsettings (which governs Claude Code's managed-settings.json):
// a control plane that markets "govern Codex" but can neither EMIT nor VERIFY this
// layer is observation-only for the fleet — it cannot actually stop a developer from
// running danger-full-access, an unapproved MCP server, or an unconstrained approval
// policy.
//
// Codex ships TWO managed files with DIFFERENT semantics (VERIFIED 2026-06-20, see
// the precedence/key reference below):
//
//   - requirements.toml — admin-enforced CONSTRAINTS users cannot override (the
//     non-overridable layer, the true analog of Claude's managed-settings.json):
//     allowed_approval_policies, allowed_sandbox_modes, allowed_web_search_modes,
//     allowed_permission_profiles, enforce_residency, Windows/remote sandbox
//     constraints, allow_remote_control/allow_managed_hooks_only, [computer_use],
//     [permissions.filesystem] deny_read, [features] pins, [experimental_network],
//     [marketplaces], [rules].prefix_rules, the [mcp_servers] allowlist, and
//     guardian_policy_config.
//   - managed_config.toml — managed DEFAULTS (same schema as the user config.toml):
//     starting values the user MAY change during a session, reapplied next launch
//     (approval_policy, sandbox_mode, web_search, [sandbox_workspace_write]
//     network_access, legacy experimental_network allowed_domains, the [otel]
//     telemetry pins). The live 2026-07-04 reference places [experimental_network] in
//     requirements; the managed-defaults field remains for backward compatibility with
//     already-authored policies.
//
// 2026-07-04 modeled additions: permission profiles require Codex >= 0.138.0; Codex
// 0.137.0 and earlier ignore allowed_permission_profiles and managed default_permissions,
// and this connector cannot observe a host's Codex binary version from TOML. The
// marketplaces gate documents two important enforcement facts: distinct names accumulate
// across requirements layers, and restrict_to_allowed_sources "doesn't filter already
// configured user marketplaces at runtime". The network parser tolerates the live
// domains-map form and dangerously_*/proxy keys without modeling them in this pass.
// Tolerated-unmodeled live surfaces verified 2026-07-04: [hooks], [apps],
// [plugins.<plugin>.mcp_servers], and the guardian_policy_config interplay with
// [auto_review]. They are accepted by Parse/Validate so newer Codex files do not fail
// closed merely because this connector has not modeled those semantics yet.
//
// This package provides both halves, mirroring managedsettings:
//
//   - AUTHORING (render.go + authoring.go): RenderRequirements / RenderManagedConfig
//     turn a governance-authored Policy into the exact requirements.toml /
//     managed_config.toml an operator distributes to the OS-policy paths
//     (/etc/codex on Unix, %ProgramData%\OpenAI\Codex on Windows) or base64-encodes
//     into the macOS MDM payload (com.openai.codex). Pure, no I/O. The exported
//     Validate*/Canonical*/VerifyDrift* entry points let the AGPL governance console
//     author + drift-verify these documents WITHOUT this Apache connector importing
//     /core (the legal arrow only runs module->connector).
//
//   - VERIFICATION (source.go + verify.go): a read-only SourceConnector reads the
//     LIVE system-tier requirements.toml + managed_config.toml on a host, emits the
//     allowed MCP servers / egress domains as PERMITTED policy edges (feeding module
//     III), and reports drift when the live files diverge from the governance-authored
//     intent — the PERMITTED-policy vs OBSERVED-config diff. An authored constraint a
//     host does not enforce drifts HIGH (a sandbox/approval/remote-control escape hatch
//     left open); a missing managed DEFAULT drifts softer (the user can change defaults
//     anyway, so it is a weaker posture than a missing constraint).
//
// HONEST PRECEDENCE (precedence.go). Requirements are resolved cloud-managed ->
// macOS MDM -> system file (first-wins); managed defaults are resolved macOS MDM ->
// managed_config.toml -> user config.toml (top-overrides). This connector reads only
// the SYSTEM-tier FILES; the cloud-managed (ChatGPT Business/Enterprise) and MDM
// tiers sit ABOVE the file and CANNOT be observed from the control plane's own
// environment — so an absent system file is reported honestly (a host governed only
// by cloud-managed requirements is NOT ungoverned, it is unverifiable-from-here),
// never as a fabricated "fully governed" or an absolute "ungoverned". Distribution of
// the rendered files (and the macOS MDM profile) is a deploy concern (VII), not this
// connector's.
//
// ENFORCEMENT semantics (verified, load-bearing): when a USER config value conflicts
// with a requirement, Codex "falls back to a compatible value and notifies the user"
// (a CLAMP, not a session reject); only an admin managed_config.toml default that
// violates a requirement, or a [features] pin conflict, is REJECTED. The connector
// therefore frames a missing requirement as "the constraint is not in force on the
// host", never as "Codex will reject the user" — the user is silently clamped, which
// is exactly why the org needs the requirement deployed in the first place.
//
// HONEST SCOPE / the Claude asymmetry. The network controls modeled here
// (managed_config.toml [sandbox_workspace_write].network_access + experimental_network
// allowed_domains / managed_allowed_domains_only) are SANDBOX EGRESS allow-rules for the
// agent's own tool/network actions — they are NOT a redirect of Codex's model-inference
// traffic. Unlike Claude Code, Codex under a ChatGPT subscription has NO documented
// equivalent of ANTHROPIC_BASE_URL: there is no sanctioned way to route Codex's own
// inference through a self-hosted gateway, so Codex is governed via THIS managed-config
// (constraints + defaults) plus the read-only analytics/compliance/audit ingest
// (connectors/codex) — NOT via inference interception. Inference interception remains an
// API-key/Bedrock-route concern (other connectors); this package never implies a Codex
// inference gateway exists. And, like every managed layer, this enforcement is
// VERIFIED-DEPLOYED, not unbypassable: the connector reports what the verifiable system
// tier carries, deny-closed on what it cannot read — never a fabricated green.
//
// The connector is read-only and imports only the SDK (Apache-2.0) plus the TOML
// codec: it never writes to a host and never imports /core or /modules.
//
// Key/precedence reference (VERIFIED 2026-06-20 against the live docs; re-verify on
// the next build, mark anything the docs no longer state as to-confirm):
// https://developers.openai.com/codex/enterprise/managed-configuration ·
// https://developers.openai.com/codex/config-reference
package codexmanagedconfig
