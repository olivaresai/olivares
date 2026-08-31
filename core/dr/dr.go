// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package dr is the disaster-recovery core for the evidence ledger: a
// backup/restore that preserves hash-chain CONTINUITY and key CUSTODY — not a
// naive database dump (OPS-2, docs/DR-RUNBOOK.md).
//
// # Why a signed, hash-chained ledger needs more than pg_dump
//
// The audit ledger (core/audit, docs/SECURITY-HARDENING.md) is an append-only, per-tenant
// hash-chain where EVERY event carries a detached Ed25519 signature over its
// chain hash, periodically anchored by signed checkpoints. Restoring it safely
// is not a byte dump because three hazards are unique to it:
//
//  1. KEY OMISSION. The per-event signing key is ALWAYS on-box (the hot path is
//     never routed off-box, even when checkpoints are KMS-signed —
//     core/audit/eventsig.go). Restore the store WITHOUT that key and every
//     per-event signature fails to verify; worse, a fresh boot silently mints a
//     NEW key and new events chain under it with no rotation record. The key file
//     is therefore an unavoidable part of the DR set.
//  2. INCONSISTENT SNAPSHOT. The chain's tail-truncation detection relies on the
//     audit_heads row matching the last audit_events row (core/internal/store/
//     sqlstore/audit.go). A per-table or torn copy that captures one without the
//     other turns a recoverable restore into a (correctly) detected break.
//  3. SILENT INCOMPLETE RESTORE. A restore that loads an older or partial bundle
//     than intended is cryptographically valid on its own — the operator needs an
//     out-of-band assertion of the expected tip to know the restore is complete.
//
// dr answers all three: BuildManifest records, per tenant, the chain tip
// (seq+hash) and the signing-key fingerprint at backup time and refuses to
// certify a backup whose chain is not already green; the bundle carries the
// signing keys ENCRYPTED under an operator KEK (keyenc.go); and RestoreVerify
// re-runs the full chain/per-event/checkpoint verification against the restored
// store AND asserts the restored tip and key fingerprint match the manifest. A
// restore is only "ledger-continuity-safe" when RestoreVerify is green.
//
// The snapshot mechanism is engine-specific and pluggable: SnapshotSQLite
// (VACUUM INTO, here, fully testable) for the single-node store, and pg_dump /
// PITR for Postgres (orchestrated by the CLI, documented in the runbook). The
// manifest and verification are engine-AGNOSTIC: they operate on any store.Store,
// so the continuity guarantee is identical across engines.
package dr

// ManifestFormat is the versioned format tag of a DR manifest. A reader rejects
// a manifest whose Format it does not recognize rather than guessing.
const ManifestFormat = "olivares.dr.manifest.v1"

// Snapshot methods recorded in a manifest's Store.Method. They document HOW the
// store snapshot in the bundle was produced, so a restore uses the matching path.
const (
	// MethodVacuumInto is a SQLite single-file consistent copy (VACUUM INTO).
	MethodVacuumInto = "sqlite-vacuum-into"
	// MethodPgDump is a Postgres logical dump (pg_dump custom/plain format).
	MethodPgDump = "postgres-pg_dump"
	// MethodPITR is a Postgres base backup + WAL archive (point-in-time recovery).
	// The bundle records the manifest only; the basebackup/WAL live in the
	// operator's archive (the snapshot file is a pointer, not the bytes).
	MethodPITR = "postgres-pitr-basebackup"
)

// Tip-match modes. They tell RestoreVerify how strictly to treat the per-tenant
// tip recorded in the manifest, which depends on how the manifest was built.
const (
	// TipExact means the manifest tips were read from the SAME snapshot that the
	// bundle carries (SQLite: the manifest is built by booting the snapshot copy).
	// The restored tip MUST equal the manifest tip — a mismatch is a hard failure
	// (an incomplete or wrong-bundle restore).
	TipExact = "exact"
	// TipAdvisory means the manifest tips were read from the LIVE store at backup
	// time, which may differ from the snapshot by the events appended during the
	// online backup window (Postgres pg_dump/PITR). The restored tip is reported
	// but a mismatch is NOT a failure; the chain/per-event/checkpoint verification
	// (always exact, self-contained in the restored data) is the real guarantee.
	TipAdvisory = "advisory"
)

// Key roles recorded in a KeyRef, inferred from the key file name.
const (
	// RoleAudit is the on-box Ed25519 key that signs every ledger event (and, by
	// default, the checkpoints). It is the key the ledger verification depends on.
	RoleAudit = "audit"
	// RoleCatalog is the independent Ed25519 key module XIV uses to sign approved
	// registry entries (boot.go). Part of the DR set, not the ledger chain.
	RoleCatalog = "catalog"
	// RoleOther is any other backed-up key material.
	RoleOther = "other"
)

// Manifest is the non-secret control record of a DR bundle. It is the expected
// post-restore state of the ledger: the per-tenant chain tips, the signing-key
// fingerprints, and the snapshot digest. It contains no secrets (a public-key
// fingerprint is one-way; the keys themselves live encrypted in the bundle), so
// it is safe to inspect, log and retain alongside the bundle.
type Manifest struct {
	// Format is ManifestFormat. A reader rejects an unknown format.
	Format string `json:"format"`
	// CreatedAt is the RFC3339 (UTC) instant the backup was taken. RPO at a
	// disaster is (time-of-disaster − CreatedAt of the latest good bundle).
	CreatedAt string `json:"created_at"`
	// EngineKind is "sqlite" or "postgres".
	EngineKind string `json:"engine"`
	// Version is the engine binary version that produced the bundle (operability).
	Version string `json:"engine_version,omitempty"`
	// Store describes the store snapshot bundled (or referenced, for PITR).
	Store StoreSnapshot `json:"store"`
	// Tenants is the chain tip of every tenant chain (including the system tenant)
	// at backup time.
	Tenants []TenantTip `json:"tenants"`
	// Keys is the signing-key material in the bundle, by fingerprint (the key
	// bytes are encrypted; this records only what is needed to detect a wrong key).
	Keys []KeyRef `json:"keys"`
	// TipMatch is TipExact or TipAdvisory (see the constants).
	TipMatch string `json:"tip_match"`
	// Notes is free-form operator context (no secrets).
	Notes string `json:"notes,omitempty"`
}

// StoreSnapshot describes the store snapshot a bundle carries.
type StoreSnapshot struct {
	// Method is one of MethodVacuumInto / MethodPgDump / MethodPITR.
	Method string `json:"method"`
	// File is the snapshot's path WITHIN the bundle (e.g. "store/olivares.db"),
	// or, for MethodPITR, a human pointer to the external archive.
	File string `json:"file"`
	// SizeBytes is the snapshot size in bytes (0 for an external PITR archive).
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 is the lowercase-hex SHA-256 of the snapshot bytes AS BUNDLED. Restore
	// recomputes it on the extracted file and refuses to proceed on a mismatch
	// (corruption/tamper of the bundle). Empty for an external PITR archive.
	SHA256 string `json:"sha256,omitempty"`
}

// TenantTip is one tenant chain's tip and backup-time verification result.
type TenantTip struct {
	// Tenant is the tenant id (the system tenant is flagged by System).
	Tenant string `json:"tenant"`
	// System marks the reserved system-tenant chain (auth + cross-tenant events).
	System bool `json:"system,omitempty"`
	// HeadSeq is the tip's per-tenant sequence number. Sequences are gap-free, so
	// HeadSeq equals the total number of events in the chain.
	HeadSeq int64 `json:"head_seq"`
	// HeadHash is the lowercase-hex chain hash of the tip event.
	HeadHash string `json:"head_hash"`
	// Checkpoints is the number of signed checkpoints found at backup time
	// (populated only when VerifyAtBackup ran).
	Checkpoints int `json:"checkpoints"`
	// VerifiedAtBackup is true when the chain, per-event signatures and
	// checkpoints all verified at backup time. A backup is only certified over a
	// chain that is ALREADY green — a corrupted ledger is never silently captured.
	VerifiedAtBackup bool `json:"verified_at_backup"`
	// VerifyReason is the first verification failure at backup time, or "".
	VerifyReason string `json:"verify_reason,omitempty"`
}

// KeyRef is a signing key carried (encrypted) in the bundle, recorded by its
// PUBLIC fingerprint so a restore can detect a wrong/substituted key without ever
// putting key material in the manifest.
type KeyRef struct {
	// File is the encrypted key's path within the bundle (e.g.
	// "keys/audit-signing.key.enc").
	File string `json:"file"`
	// Name is the original key file name in the data dir (e.g. "audit-signing.key").
	Name string `json:"name"`
	// Role is RoleAudit / RoleCatalog / RoleOther, inferred from Name.
	Role string `json:"role"`
	// PubSHA256 is the lowercase-hex SHA-256 of the Ed25519 PUBLIC key (32 bytes)
	// derived from this signing key. It is the fingerprint RestoreVerify checks the
	// restored key against. Empty for a non-Ed25519 key.
	PubSHA256 string `json:"pub_sha256,omitempty"`
}

// auditTip returns the manifest's tip for tenant, or (zero, false).
func (m *Manifest) tip(tenant string) (TenantTip, bool) {
	for _, t := range m.Tenants {
		if t.Tenant == tenant {
			return t, true
		}
	}
	return TenantTip{}, false
}

// auditKey returns the manifest's audit-role key ref, or (zero, false).
func (m *Manifest) auditKey() (KeyRef, bool) {
	for _, k := range m.Keys {
		if k.Role == RoleAudit {
			return k, true
		}
	}
	return KeyRef{}, false
}
