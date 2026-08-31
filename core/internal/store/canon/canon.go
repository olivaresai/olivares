// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package canon computes the canonical preimage and hash of an audit event. It
// is the single source of truth for the hash-chain (docs/SECURITY-HARDENING.md): both Append
// and Verify call EventHash, so there is no second implementation to drift.
//
// The preimage is a fixed, length-prefixed binary encoding with versioned
// domain separators — never JSON — so that key ordering, whitespace and number
// formatting cannot be used to forge or to falsely break a chain. Length
// prefixes close the "merge two adjacent fields to forge a third" attack; raw
// 32-byte digests (not hex) avoid casing ambiguity; the version tag makes the
// scheme upgradable. The package depends only on the standard library.
package canon

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// hashLen is the byte length of a SHA-256 digest.
const hashLen = 32

// domainEvent, domainMeta and domainMetaCommitment are versioned domain
// separators that bind a hash to its purpose and version.
//
// domainMeta and domainMetaCommitment are BOTH live, and which one applies is a
// property of the RECORD, not of the build. A record appended with a blind
// commits its metadata under domainMetaCommitment; a record appended before
// blinding existed has no blind and commits under domainMeta. That is not a
// compatibility shim to retire: an append-only ledger cannot have its hash rule
// changed retroactively, because every pre-existing row would stop verifying and
// a legitimate history would be indistinguishable from tampering — the exact
// failure this package exists to make impossible. The stored blind column is the
// discriminator, so each row carries the rule it was sealed under.
const (
	domainEvent          = "olivares.audit.v1"
	domainMeta           = "olivares.audit.meta.v1"
	domainMetaCommitment = "olivares.audit.meta-commitment.v1"
)

// BlindLen is the byte length of a metadata blind: 256 bits of CSPRNG material,
// matching the digest width so the commitment's hiding strength is not the
// weakest term.
const BlindLen = 32

// ZeroHash returns the all-zero 32-byte digest used as the genesis prev_hash and
// as the placeholder for an absent payload hash.
func ZeroHash() []byte { return make([]byte, hashLen) }

// lp appends the length-prefixed bytes of s to dst: u32be(len(s)) followed by s.
func lp(dst []byte, s []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(s)))
	dst = append(dst, n[:]...)
	return append(dst, s...)
}

// lps length-prefixes a string.
func lps(dst []byte, s string) []byte { return lp(dst, []byte(s)) }

// CanonicalMeta returns a deterministic JSON encoding of an event's metadata,
// suitable for storage. Go's json.Marshal sorts map keys, so the encoding is
// stable for a given value. The stored string — not the in-memory map — is what
// MetaDigest hashes, so Append and Verify hash identical bytes with no
// round-trip ambiguity. A nil/empty map encodes as the literal "{}".
func CanonicalMeta(meta map[string]any) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// MetaDigest returns the 32-byte digest of the stored canonical metadata string,
// domain-separated from the event hash. It is the UNBLINDED rule, and it applies
// to exactly one class of record: one sealed before metadata blinding existed,
// which therefore has no stored blind. New appends use MetaCommitment.
//
// It is deliberately not deprecated away: see the domain-separator comment above
// for why an append-only ledger keeps both rules alive forever.
func MetaDigest(canonicalMeta string) []byte {
	h := sha256.New()
	h.Write(lps(nil, domainMeta))
	h.Write([]byte(canonicalMeta))
	return h.Sum(nil)
}

// MetaCommitment returns the 32-byte HIDING commitment to the stored canonical
// metadata string: SHA-256 over its own domain separator, the record's blind and
// the metadata bytes.
//
// The blind is what makes the value safe to project off-box. The unblinded digest
// is a deterministic function of the metadata alone, so a holder of one exported
// line can confirm a guessed metadata value by hashing it — and the ledger's
// metadata routinely carries guessable, attacker-influenced material (a login IP,
// a denial reason, a device-grant code). Worse, two records with identical
// metadata produce identical digests, so the export leaks an equality relation
// the projection deliberately withholds. With a per-record blind, neither holds:
// confirming a guess additionally requires the blind, which never leaves the
// store except inside the complete archive artifact.
//
// The event hash consumes this value directly, so offline recomputation from one
// emitted line is UNAFFECTED — the commitment travels on the wire. What the blind
// gates is the narrower question "which metadata produced it", which is exactly
// the question a minimal-data projection must not answer.
//
// MetaCommitmentFor picks between this and MetaDigest by the record's own blind;
// callers holding a row should use that rather than choosing here.
func MetaCommitment(blind []byte, canonicalMeta string) []byte {
	h := sha256.New()
	h.Write(lps(nil, domainMetaCommitment))
	h.Write(lp(nil, blind))
	h.Write([]byte(canonicalMeta))
	return h.Sum(nil)
}

// ValidateBlind admits exactly the two states a stored record may be in and
// rejects every other one loudly.
//
// A nil blind is the LEGACY state: the column was NULL, the record predates
// blinding, and it commits under domainMeta. A blind of exactly BlindLen bytes is
// the blinded state. Nothing else is legal, and the third state is not
// hypothetical: a non-NULL but zero-length BLOB is len()==0 like a NULL, so a
// naive length test would hash a row whose own column says "blinded" under the
// legacy rule — a silent third rule, which is precisely the class this package
// exists to make impossible (see the domain-separator comment above). The
// database mirrors this with a CHECK constraint so the state cannot exist at rest
// either; this is the in-flight half of that pair.
//
// The distinction relies on database/sql scanning a NULL column into a nil slice
// and a present one into a non-nil slice. Where a driver flattens the two, the
// CHECK constraint is what remains, which is why both halves ship together.
func ValidateBlind(blind []byte) error {
	if blind == nil || len(blind) == BlindLen {
		return nil
	}
	return fmt.Errorf("%w: is %d bytes, want NULL or %d", ErrMalformedBlind, len(blind), BlindLen)
}

// ErrMalformedBlind reports a stored blind that is neither absent nor BlindLen
// bytes. It is a sentinel so a reader can tell this apart from an I/O failure:
// on the Verify path a malformed blind is evidence ABOUT the ledger — a row whose
// discriminator was altered — and belongs in the chain-break report as a stated
// reason, not as an opaque error that makes verification merely unavailable.
var ErrMalformedBlind = errors.New("canon: malformed metadata blind")

// MetaCommitmentFor resolves the metadata commitment of a stored record from the
// blind the record carries: blinded when it has one, the legacy unblinded digest
// when it does not. Every read path that recomputes a chain hash MUST go through
// here, so the choice of rule can never diverge between Append, Verify, the
// archive and the archive verifier.
//
// It returns an error rather than a value for a malformed blind because there is
// no safe fallback: choosing either rule for an unrecognizable state would mint
// the third rule this package forbids, and returning the legacy digest for a row
// that claims to be blinded would let a tampered column silently pick the weaker
// commitment. The caller decides what a malformed record means in its context —
// a refused append, a reported chain break, or a refused export — but no caller
// gets to hash one.
func MetaCommitmentFor(blind []byte, canonicalMeta string) ([]byte, error) {
	if err := ValidateBlind(blind); err != nil {
		return nil, err
	}
	if blind == nil {
		return MetaDigest(canonicalMeta), nil
	}
	return MetaCommitment(blind, canonicalMeta), nil
}

// Event holds the chained fields of an audit event for hashing. The caller fills
// it from either an in-flight draft (Append) or a stored row (Verify).
type Event struct {
	TenantID   string
	Seq        int64
	OccurredAt string
	Actor      string
	ActorKind  string
	Action     string
	TargetKind string
	TargetID   string
	// MetaCommitment is the 32-byte commitment to the stored canonical metadata:
	// blinded (MetaCommitment) for a record sealed with a blind, the legacy
	// unblinded digest (MetaDigest) for one sealed before blinding existed.
	// Resolve it with MetaCommitmentFor, never by choosing a rule at the call site.
	MetaCommitment []byte
	PayloadHash    []byte // 32 bytes, or nil for none
	PrevHash       []byte // 32 bytes (ZeroHash at seq 1)
}

// Validate rejects the digest widths the preimage encoder would otherwise absorb
// in silence. fixed() zero-pads a short value and truncates a long one, which
// keeps the preimage unambiguous but would let a caller commit to only the first
// 32 bytes of a longer digest — or to a zero-padded stub — and still produce a
// well-formed hash. On an evidence ledger that is the wrong failure: a wrong-width
// input is a programming error, and it must surface as one at the boundary rather
// than as a hash nobody can explain later.
//
// PayloadHash stays nil-or-exact because "no payload" is a real state (ZeroHash
// stands in for it in the preimage); MetaCommitment and PrevHash are always
// present on a sealed record.
func (e Event) Validate() error {
	if len(e.MetaCommitment) != hashLen {
		return fmt.Errorf("canon: meta commitment is %d bytes, want %d", len(e.MetaCommitment), hashLen)
	}
	if len(e.PrevHash) != hashLen {
		return fmt.Errorf("canon: prev hash is %d bytes, want %d", len(e.PrevHash), hashLen)
	}
	if n := len(e.PayloadHash); n != 0 && n != hashLen {
		return fmt.Errorf("canon: payload hash is %d bytes, want 0 or %d", n, hashLen)
	}
	return nil
}

// fixed normalizes a digest slice to exactly hashLen bytes (nil/short → zero
// padded, long → truncated) so the preimage is unambiguous and fixed-width.
//
// EventHash validates before it encodes, so the only value that still reaches
// here needing normalization is a nil PayloadHash — "no payload", for which
// ZeroHash is the sanctioned stand-in. The padding and truncation branches are
// retained as the encoder's own width guarantee, but they are no longer a way in:
// a short or long digest is refused at Validate rather than silently reshaped
// into a hash nobody can explain later.
func fixed(b []byte) []byte {
	out := make([]byte, hashLen)
	copy(out, b)
	return out
}

// EventHash returns the chain hash of e: SHA-256 over the versioned,
// length-prefixed binary preimage. It is deterministic and is the one function
// used by both Append and Verify.
//
// It validates e first and refuses to hash a malformed event. Hashing is the
// last point at which a wrong-width digest is still recognizable as one: past
// here it becomes 32 indistinguishable bytes, so a caller that skipped the check
// would get a well-formed hash over a zero-padded stub and no way to tell. Making
// the check part of the hash rather than a discipline callers must remember is
// what stops that from depending on review.
func EventHash(e Event) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return eventHash(e), nil
}

// eventHash encodes and hashes an already-validated event.
func eventHash(e Event) []byte {
	var buf []byte
	buf = lps(buf, domainEvent)
	buf = lps(buf, e.TenantID)
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], uint64(e.Seq))
	buf = append(buf, seq[:]...)
	buf = lps(buf, e.OccurredAt)
	buf = lps(buf, e.Actor)
	buf = lps(buf, e.ActorKind)
	buf = lps(buf, e.Action)
	buf = lps(buf, e.TargetKind)
	buf = lps(buf, e.TargetID)
	buf = append(buf, fixed(e.MetaCommitment)...)
	buf = append(buf, fixed(e.PayloadHash)...)
	buf = append(buf, fixed(e.PrevHash)...)
	sum := sha256.Sum256(buf)
	return sum[:]
}
