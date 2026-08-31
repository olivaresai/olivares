// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// SecretEntry is one row of the RUNTIME SECRET STORE: a named secret whose
// value is SEALED at rest (AES-256-GCM under an engine-held key, bound to the
// entry's scope as AAD) so the cleartext is never persisted, never logged and
// never returned by the API. An operator's connector / notify / identity / MCP
// config no longer carries a literal secret by value — it carries a reference
// (`store:<name>`) that the engine resolves to this sealed value at Open.
//
// Like every credential-bearing row (api_token, federation_config) it lives in
// the reserved system tenant (BaseFields.TenantID == SystemTenantID) and is
// reachable ONLY through the engine's auth partition (store.AuthScope) — a module
// holds no Store, so it can never read a secret value (the same isolation the auth
// catalog relies on). The Scope column is the deployment-wide-vs-per-tenant axis:
// Stores and resolves a single GLOBAL scope (Scope == SystemTenantID); the
// column is present from day one so per-tenant secrets are an additive row, never
// a schema change (the federation_config TargetTenantID precedent).
type SecretEntry struct {
	BaseFields
	// Scope is the tenant this secret governs. SystemTenantID is the global
	// (deployment-wide) secret — the only scope resolves. A real tenant id is
	// a per-tenant secret (additive, future).
	Scope TenantID
	// Name is the operator-facing handle a `store:<name>` reference resolves to.
	// Unique per scope; non-secret.
	Name string
	// ValueSealed is the sealed secret value (the engine's Sealer output, never the
	// cleartext, never a one-way hash — the engine must replay the real value to
	// the connector at Open).
	ValueSealed string
	// Hint is a non-secret SHA-256 fingerprint prefix of the value, for display and
	// change-detection without revealing it.
	Hint string
	// Description is an optional non-secret note (what the secret is for).
	Description string
}
