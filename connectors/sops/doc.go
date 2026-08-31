// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package sops is the Olivares AI connector for SOPS+age GitOps secrets
//. It reads ONLY the cleartext METADATA SOPS already records in
// a repository — the recipient public identifiers — and NEVER decrypts anything,
// NEVER reads the encrypted file body, and NEVER emits the encrypted data key.
//
// It is read-first (docs/SECURITY-HARDENING.md): the operator points the connector at a checked-out
// GitOps repo (a file or a directory). The connector walks it and parses two kinds
// of YAML metadata with gopkg.in/yaml.v3:
//
//   - a `.sops.yaml` rules file — its `creation_rules` declare which recipients
//     (age/kms/gcp_kms/azure_keyvault/hc_vault/pgp) encrypt which `path_regex`;
//   - any other YAML/JSON file carrying a top-level `sops:` block — that file is
//     SOPS-ENCRYPTED, and the connector reads ONLY the `sops:` metadata (the
//     recipient list), never the rest of the file.
//
// A SOPS file records, per recipient, both a PUBLIC identifier (an age `recipient`
// `age1…`, a KMS key `arn`, a GCP `resource_id`, an Azure Key Vault `vault_url`/
// `name`, a Vault Transit address, a PGP `fp`) and an `enc` field — the ENCRYPTED
// DATA KEY for that recipient. The connector emits the public identifier and NEVER
// the `enc` value, the `mac`, or any other ciphertext. There is a negative test
// (TestNoDataKeyLeaks) that json.Marshals every emitted edge AND the snapshot
// identities and asserts the fake `enc` data-key string never appears.
//
// Provisioning edges. For every ENCRYPTED file, for every recipient that can
// decrypt it, the connector emits ONE model.EdgeObservation:
//
//	OriginKind "identity"  OriginRef sops.<type>:<public-id>
//	ResourceKind "sops.file"  ResourceRef <path relative to the configured root>
//	Mode read  Source SignalSource("sops")  ToolRef <type>  Confidence attributed
//
// — "which recipient key is permitted to DECRYPT/read which file", NOT the
// material. A recipient holding the data key is, by construction, permitted to read
// the file, so the Mode is read.
//
// Inventory. Snapshot exposes every DISTINCT recipient seen — across
// the encrypted files AND the `.sops.yaml` rules — as a secret_store NHI (the
// custodian of the secrets it can unseal), converging on the SAME ref string used
// as the edge OriginRef. It carries the recipient's public identifier and type
// only, never key material. With no configured path it returns an empty graph
// (offline), no error.
//
// Guardrails: zero crypto calls, zero network calls — only YAML metadata parsing.
// It imports only the SDK, the Apache identitysource/internal helpers, gopkg.in/
// yaml.v3 and the standard library — never the engine (/core).
package sops
