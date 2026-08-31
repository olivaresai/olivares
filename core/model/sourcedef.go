// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// SourceDef is one row of the DURABLE SOURCE ROSTER: the operator's
// definition of one observation-source connector, persisted so it survives a
// process restart and can be reconciled into the running engine WITHOUT one. It
// is the store-backed successor to a `sources[]` entry in the boot config file —
// the file becomes a one-time bootstrap seed, and this table is the source of
// truth the live reconciler diffs against.
//
// Like SecretEntry and FederationConfig it lives in the reserved system tenant
// (BaseFields.TenantID == SystemTenantID) and is reached ONLY through the engine's
// auth partition (store.AuthScope.Sources) — operator-global configuration, edited
// by a superadmin from the console/CLI, never reachable from module code. The
// Scope column is the deployment-wide-vs-per-tenant axis stores and
// reconciles a single GLOBAL scope (Scope == SystemTenantID), the column present
// from day one so a per-tenant source roster is an additive row, never a schema
// change (the SecretEntry / federation_config precedent).
//
// CRITICAL — this row never carries a secret VALUE. Config holds connector
// settings and secret REFERENCES (`store:<name>`, `vault:…`, `env:…`); the engine
// resolves each reference to its sealed value at Open (the secret store).
// Persisting only references is what keeps the roster non-secret-bearing and the
// secret of record durable in the sealed store, not scattered in plaintext config.
type SourceDef struct {
	BaseFields
	// Scope is the configuration scope this source belongs to. SystemTenantID is
	// the global (deployment-wide) roster — the only scope reconciles.
	Scope TenantID
	// Name is the operator-facing unique handle for this source within its scope —
	// the stable identity the reconciler diffs on (NOT the connector's kind-derived
	// Descriptor name, which the operator never sees). Non-secret.
	Name string
	// Kind selects the first-party connector (e.g. "vault", "claude"). Ignored when
	// Plugin is set (an external binary self-describes via its Descriptor).
	Kind string
	// Tenant is the business tenant reference its observations are stamped with.
	Tenant string
	// PollSeconds re-runs a BATCH source every interval (0 = run once / streaming).
	// Applies to in-process sources; a streaming plugin source ignores it.
	PollSeconds int
	// Enabled gates whether the source is wired into the engine. A disabled source
	// is persisted (and shown) but not run — so an operator can pause a connector
	// without losing its configuration, and re-enable it with a live reload.
	Enabled bool
	// Config is the connector's settings plus secret REFERENCES — never literal
	// secret values. Stored as a JSON column.
	Config map[string]string
	// Plugin provisions this source as an EXTERNAL (third-party) connector plugin
	// binary (S142): an operator-pinned digest + Sigstore attestation the admission
	// gate verifies against the deployment's connector-trust policy. nil ⇒ a
	// first-party kind. Stored as a JSON column.
	Plugin *SourcePluginRef
}

// SourcePluginRef is the external (third-party) connector-plugin provisioning on a
// SourceDef. It mirrors the operator's file-config plugin spec so the durable
// roster carries the same trust inputs: the on-host binary path, the REQUIRED
// sha256 artifact pin, the detached Sigstore attestation bundle path, and an
// optional per-source narrowing of the trust policy's predicate allow-list. It
// holds signature/trust material (public), never a secret value.
type SourcePluginRef struct {
	// Path is the absolute path to the plugin executable on the host. The engine
	// never downloads a binary; placement is an operator act.
	Path string `json:"path"`
	// SHA256 is the REQUIRED lowercase-hex sha256 pin of the binary. The on-disk
	// file must hash to exactly this and the attestation's subject must cover it;
	// missing/malformed ⇒ the source is refused (deny-closed).
	SHA256 string `json:"sha256"`
	// Bundle is the path to the detached Sigstore attestation bundle JSON over the
	// pinned digest. Empty ⇒ refused: unsigned external plugins never run.
	Bundle string `json:"bundle"`
	// PredicateTypes optionally NARROWS (never widens) the trust policy's predicate
	// allow-list for THIS source.
	PredicateTypes []string `json:"predicate_types,omitempty"`
}
