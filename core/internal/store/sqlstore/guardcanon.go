// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

// guardcanon.go is the canonical byte serialization every guard hash is taken over.
//
// It exists because the alternative — hashing a JSON map, a `pg_get_functiondef` string
// or anything a SQL deparser produced — makes the golden a function of the SERVER'S
// formatting rather than of the definition. A minor version that reflows whitespace, a
// different `bytea_output`, a locale that orders map keys differently: each of those
// would turn an unchanged object into a drift diagnosis, and an operator who has learned
// to ignore drift diagnoses is the failure mode this whole ledger exists to avoid.
//
// Six properties carry it, and each has a failure it prevents:
//
//   - A fixed DOMAIN TAG opens every stream, so bytes hashed for one purpose can never
//     be replayed as bytes for another.
//   - The FORMAT version is the second field, so a reader that does not understand the
//     encoding refuses instead of decoding it wrongly.
//   - Every value is preceded by a TYPE TAG, so two adjacent fields cannot be confused
//     for one: an empty string followed by "x" and the string "x" followed by an empty
//     one must not produce the same bytes.
//   - Strings are LENGTH-PREFIXED and never trimmed or case-folded. PostgreSQL
//     identifiers may contain any byte but NUL, so `"a.b"."c"` and `"a"."b.c"` are two
//     relations; a dotted flattening gives them one identity. The lock plan already pays
//     for this lesson (see plannedLock.relation).
//   - NULL has its OWN tag, distinct from an empty string and from an empty collection.
//     `tgqual` NULL and `tgqual` ” are different catalog states and one of them is a
//     guard with a WHEN clause.
//   - Integers are fixed-width BIG-ENDIAN, so the bytes do not depend on the host.
type canonWriter struct {
	b   strings.Builder
	err error
}

// The domain tags. Each is a distinct purpose, and a value hashed under one is never
// valid under another.
const (
	canonDomainManifest   = "olivares.guard-manifest"
	canonDomainEntry      = "olivares.guard-entry"
	canonDomainDefinition = "olivares.guard-definition"
	canonDomainPrestate   = "olivares.guard-prestate"
	canonDomainDiagnostic = "olivares.guard-diagnostic"
	canonDomainReceipt    = "olivares.guard-receipt"
	canonDomainEvent      = "olivares.guard-event"
)

// The type tags. Written before every value.
const (
	canonTagString byte = 0x01
	canonTagNull   byte = 0x02
	canonTagUint   byte = 0x03
	canonTagBool   byte = 0x04
	canonTagList   byte = 0x05
	canonTagBytes  byte = 0x06
	canonTagInt    byte = 0x07
)

// newCanonWriter opens a stream for one domain. The domain is written as a
// length-prefixed string so a longer tag can never be a prefix of a shorter one plus
// data.
func newCanonWriter(domain string, format int64) *canonWriter {
	w := &canonWriter{}
	w.str(domain)
	w.i64(format)
	return w
}

func (w *canonWriter) str(s string) {
	w.b.WriteByte(canonTagString)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	w.b.Write(n[:])
	w.b.WriteString(s)
}

// optText is a catalog value that may be NULL, kept comparable with == so the whole
// projection can be compared as a value rather than field by field by hand.
//
// A bare string cannot express this. `tgqual` NULL means "no WHEN clause" and `tgqual`
// ” would mean a clause whose text is empty — not a state PostgreSQL produces, which is
// exactly why a projection that collapses them can be handed a reading the catalog could
// never have made.
type optText struct {
	Valid bool
	V     string
}

func someText(s string) optText { return optText{Valid: true, V: s} }

func (o optText) String() string {
	if !o.Valid {
		return "NULL"
	}
	return o.V
}

func (w *canonWriter) opt(o optText) {
	if !o.Valid {
		w.b.WriteByte(canonTagNull)
		return
	}
	w.str(o.V)
}

// u64 writes an unsigned integer big-endian.
func (w *canonWriter) u64(v uint64) {
	w.b.WriteByte(canonTagUint)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], v)
	w.b.Write(n[:])
}

// i64 writes a signed integer under its own tag.
//
// A separate tag rather than a cast, because the two domains overlap: the uint64 form of
// -1 is 18446744073709551615, and an encoder that shared a tag would let a negative
// value and a very large positive one produce identical bytes.
func (w *canonWriter) i64(v int64) {
	w.b.WriteByte(canonTagInt)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	w.b.Write(n[:])
}

func (w *canonWriter) boolean(v bool) {
	w.b.WriteByte(canonTagBool)
	if v {
		w.b.WriteByte(1)
		return
	}
	w.b.WriteByte(0)
}

// list writes a collection's length under its own tag, so an empty collection is
// distinguishable from NULL and from an absent field.
func (w *canonWriter) list(n int) {
	w.b.WriteByte(canonTagList)
	var l [8]byte
	binary.BigEndian.PutUint64(l[:], uint64(n))
	w.b.Write(l[:])
}

// bytes32 writes a fixed 32-byte digest.
func (w *canonWriter) bytes32(d [32]byte) {
	w.b.WriteByte(canonTagBytes)
	w.b.Write(d[:])
}

// float writes a float64 by its IEEE-754 bits, refusing the two values that have no
// canonical byte form worth hashing.
//
// procost and prorows are float4/float8 in the catalog. NaN is not equal to itself, so a
// comparison against a canonical NaN would report drift forever; an infinity is not a
// cost PostgreSQL produces for a plpgsql function. Refusing is honest — the projection is
// unusable — where hashing the bits would produce a stable golden for an unusable value.
func (w *canonWriter) float(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		if w.err == nil {
			w.err = fmt.Errorf("sqlstore: guard canonicalisation refuses the non-finite value %v; it has no comparable canonical form", v)
		}
		return
	}
	w.u64(math.Float64bits(v))
}

// sum returns the SHA-256 of everything written, or the first error the writer met.
func (w *canonWriter) sum() ([32]byte, error) {
	if w.err != nil {
		return [32]byte{}, w.err
	}
	return sha256.Sum256([]byte(w.b.String())), nil
}

// bytes returns the raw canonical bytes. Used by the event chain, which hashes a
// predecessor digest together with a body.
func (w *canonWriter) bytes() ([]byte, error) {
	if w.err != nil {
		return nil, w.err
	}
	return []byte(w.b.String()), nil
}

// hexDigest renders a digest the way every ledger column and every diagnostic carries
// it: lower-case hex, fixed width.
func hexDigest(d [32]byte) string { return hex.EncodeToString(d[:]) }

// digestFromHex parses a hex digest back, refusing anything that is not exactly 32
// bytes.
//
// The width check is not decoration. Every hash column is CHECKed at 32 octets in the
// database, and a Go side that accepted a short value would let a truncated digest pass
// comparison against another truncated digest — two wrong answers agreeing.
func digestFromHex(s string) ([32]byte, error) {
	var out [32]byte
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, fmt.Errorf("sqlstore: %q is not a hex digest: %w", s, err)
	}
	if len(raw) != len(out) {
		return out, fmt.Errorf("sqlstore: digest %q is %d bytes, want %d", s, len(raw), len(out))
	}
	copy(out[:], raw)
	return out, nil
}
