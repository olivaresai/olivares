// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

// ArchiveVerifyOptions select which signature checks the archive verifier runs
// on top of the always-on structural checks (canon re-derivation, linkage,
// declared-gap validation, manifest digests, cross-segment continuity).
type ArchiveVerifyOptions struct {
	// EventKeys are the epoch-fenced candidate Ed25519 keys for per-event
	// signatures (current + prior generations, the VerifyEventsFenced model).
	// Each key carries the per-tenant sequence range it is trusted to have
	// signed: an unbounded key (LastSeq==0) is the current generation, a bounded
	// key (LastSeq==L) is a RETIRED generation valid only through its epoch — so a
	// single unbounded key reproduces the pre single-generation behavior
	// exactly, while a rotated archive is fenced (a retired key can NEVER validate
	// a current-epoch archived event, closing F-07). Empty skips the per-event
	// signature check; non-empty makes a missing signature on a non-checkpoint
	// event a failure, exactly like VerifyEventsFenced. Non-empty EventKeys
	// WITHOUT Checkpoints makes every checkpoint line a failure
	// ("checkpoint-unverifiable"): checkpoint lines are exempt from the
	// per-event check, so an attacker who re-derived a whole forged chain could
	// otherwise dress every event as a checkpoint and dodge the only signature
	// check in force — pin both key sets, or neither (the advisory
	// chain-structure-only mode). The boundaries come from the operator's
	// `--event-pubkey key@last_seq` pins (the attacker-resistant flow, identical
	// to the live path's external pin); an archive's own keys.json lists the
	// generations UNFENCED, so an advisory verify against it stays honestly
	// advisory — a keys file that rode with the archive proves nothing (docs/SECURITY-HARDENING.md).
	EventKeys []FencedKey
	// Checkpoints verifies each checkpoint event's signature over its attested
	// head (the event's own PrevHash — O(1) in the stream, unlike
	// VerifyCheckpointsWith's O(chain) seq→hash map). nil/empty skips it.
	Checkpoints *CheckpointVerifier
}

// ArchiveVerifyReport is the outcome of verifying an archive directory. Its
// Reason vocabulary extends store.VerifyReport's ("hash-mismatch",
// "prev-mismatch", "seq-gap", "gap-mismatch") and the signature reports'
// ("event-sig-invalid", "event-sig-missing", "checkpoint-sig-invalid",
// "checkpoint-unverifiable", "recovery-sig-invalid",
// "recovery-position-invalid", "recovery-unverifiable", "keyrotation-sig-invalid",
// "keyrotation-position-invalid", "keyrotation-unverifiable") with archive-specific reasons:
// "manifest-unreadable", "bad-format", "key-mismatch", "events-missing",
// "manifest-missing", "bad-line", "line-not-canonical", "count-mismatch",
// "first-hash-mismatch", "last-hash-mismatch", "events-sha256-mismatch",
// "segment-gap", "segment-link-mismatch", "no-events".
type ArchiveVerifyReport struct {
	// OK is true only when at least one archived event was found and every
	// segment of every tenant verified.
	OK bool
	// Tenants/Segments/Events/Checkpoints count what was checked.
	Tenants     int
	Segments    int
	Events      int64
	Checkpoints int
	// DeclaredGaps is the number of sanctioned, marker-declared holes crossed.
	// They do not fail verification; authenticity rides on each marker's normal
	// per-event signature when EventKeys are pinned, so structure-only checking
	// remains advisory just as it is for every other event.
	DeclaredGaps int64
	// Ranges maps each tenant to the contiguous sequence range its verified
	// segments cover. An auditor MUST read it: a green OK attests exactly this
	// range and nothing outside it (a removed prefix or tail is offline-
	// undetectable; see VerifyArchiveDir).
	Ranges map[string]ArchiveTenantRange
	// BreakTenant/BreakSegment/BreakAt locate the first failure (the tenant id,
	// the failing segment's events key, and the event sequence when the failure
	// is event-level, else 0).
	BreakTenant  string
	BreakSegment string
	BreakAt      int64
	// Reason describes the first failure, or "".
	Reason string
}

// ArchiveTenantRange is the contiguous sequence range one tenant's verified
// segments cover.
type ArchiveTenantRange struct {
	FromSeq int64
	ToSeq   int64
	// StartsMidChain is true when FromSeq > 1: the archive does not reach back
	// to genesis, so everything before FromSeq is simply NOT attested (an
	// offline verifier cannot tell a legitimate partial export from a removed
	// prefix). It is deliberately not a failure — partial exports are
	// legitimate — but the flag must reach the auditor.
	StartsMidChain bool
}

// archiveSeg pairs one manifest with its on-disk file paths during a verify.
type archiveSeg struct {
	manifest     SegmentManifest
	eventsPath   string
	manifestPath string
}

// maxArchiveLine bounds one JSONL line during verify (audit meta is minimal
// data, so real lines are far smaller; the bound only prevents a hostile file
// from ballooning memory — the verifier is constant-memory by design).
const maxArchiveLine = 4 << 20
