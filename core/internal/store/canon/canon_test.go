// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package canon

import (
	"bytes"
	"errors"
	"testing"
)

// mustHash hashes an event the tests expect to be well-formed. EventHash refuses
// a malformed event, so a helper that fails the test on error keeps every other
// assertion about hashing free of error plumbing.
func mustHash(t *testing.T, e Event) []byte {
	t.Helper()
	h, err := EventHash(e)
	if err != nil {
		t.Fatalf("EventHash on a well-formed event: %v", err)
	}
	return h
}

func sampleEvent() Event {
	return Event{
		TenantID: "11111111-1111-7111-8111-111111111111", Seq: 7,
		OccurredAt: "2026-06-02T10:00:00.000000000Z", Actor: "user:1", ActorKind: "user",
		Action: "agent.create", TargetKind: "core.agent", TargetID: "22222222-2222-7222-8222-222222222222",
		MetaCommitment: MetaDigest("{}"), PayloadHash: nil, PrevHash: ZeroHash(),
	}
}

func TestEventHashDeterministic(t *testing.T) {
	e := sampleEvent()
	if !bytes.Equal(mustHash(t, e), mustHash(t, e)) {
		t.Fatal("EventHash is not deterministic")
	}
	if len(mustHash(t, e)) != 32 {
		t.Fatalf("hash length = %d, want 32", len(mustHash(t, e)))
	}
}

// TestEventHashSensitive checks that mutating any chained field changes the hash
// — no field is silently excluded from the integrity guarantee.
func TestEventHashSensitive(t *testing.T) {
	base := mustHash(t, sampleEvent())
	mutate := map[string]func(*Event){
		"tenant":         func(e *Event) { e.TenantID = "33333333-3333-7333-8333-333333333333" },
		"seq":            func(e *Event) { e.Seq = 8 },
		"occurredAt":     func(e *Event) { e.OccurredAt = "2026-06-02T10:00:00.000000001Z" },
		"actor":          func(e *Event) { e.Actor = "user:2" },
		"actorKind":      func(e *Event) { e.ActorKind = "agent" },
		"action":         func(e *Event) { e.Action = "agent.delete" },
		"targetKind":     func(e *Event) { e.TargetKind = "core.session" },
		"targetID":       func(e *Event) { e.TargetID = "44444444-4444-7444-8444-444444444444" },
		"metaCommitment": func(e *Event) { e.MetaCommitment = MetaDigest(`{"x":1}`) },
		"payload":        func(e *Event) { e.PayloadHash = MetaDigest("payload") },
		"prevHash":       func(e *Event) { e.PrevHash = MetaDigest("prev") },
	}
	for name, m := range mutate {
		e := sampleEvent()
		m(&e)
		if bytes.Equal(mustHash(t, e), base) {
			t.Fatalf("mutating %s did not change the hash", name)
		}
	}
}

// TestLengthPrefixingPreventsMerge proves the length-prefixed preimage defeats
// the "shift a byte from one field to the next" forgery: two events that share
// the naive concatenation of adjacent fields still hash differently.
func TestLengthPrefixingPreventsMerge(t *testing.T) {
	a := sampleEvent()
	a.Actor, a.ActorKind = "ab", "user"
	b := sampleEvent()
	b.Actor, b.ActorKind = "a", "buser"
	if bytes.Equal(mustHash(t, a), mustHash(t, b)) {
		t.Fatal("field-boundary ambiguity: length prefixing failed")
	}
}

func TestCanonicalMetaStable(t *testing.T) {
	// Key order in the source map must not affect the canonical encoding.
	m1 := map[string]any{"a": 1, "b": 2}
	m2 := map[string]any{"b": 2, "a": 1}
	c1, err := CanonicalMeta(m1)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := CanonicalMeta(m2)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != c2 {
		t.Fatalf("canonical meta differs by key order: %q vs %q", c1, c2)
	}
	if empty, _ := CanonicalMeta(nil); empty != "{}" {
		t.Fatalf("nil meta = %q, want {}", empty)
	}
}

// TestMetaCommitmentHidesAndSeparates pins the three properties the blinded
// commitment exists for. The equality property is the one an unblinded digest
// cannot provide: two records with IDENTICAL metadata must not produce identical
// commitments, or an exported projection leaks which events shared metadata.
func TestMetaCommitmentHidesAndSeparates(t *testing.T) {
	meta := `{"ip":"203.0.113.7"}`
	b1 := bytes.Repeat([]byte{0xA1}, BlindLen)
	b2 := bytes.Repeat([]byte{0xA2}, BlindLen)

	if bytes.Equal(MetaCommitment(b1, meta), MetaDigest(meta)) {
		t.Fatal("blinded commitment equals the unblinded digest: the domain separator or the blind is not in the preimage")
	}
	if bytes.Equal(MetaCommitment(b1, meta), MetaCommitment(b2, meta)) {
		t.Fatal("same metadata under different blinds collides: the commitment is not hiding")
	}
	if !bytes.Equal(MetaCommitment(b1, meta), MetaCommitment(b1, meta)) {
		t.Fatal("MetaCommitment is not deterministic")
	}
	if n := len(MetaCommitment(b1, meta)); n != hashLen {
		t.Fatalf("commitment length = %d, want %d", n, hashLen)
	}
}

// TestMetaCommitmentBoundaryIsUnforgeable is the concatenation attack the length
// prefix closes: without it, moving one byte from the end of the blind to the
// front of the metadata would leave the preimage — and so the commitment —
// unchanged, letting one commitment stand for two different (blind, metadata)
// pairs.
func TestMetaCommitmentBoundaryIsUnforgeable(t *testing.T) {
	if bytes.Equal(MetaCommitment([]byte("ab"), "c"), MetaCommitment([]byte("a"), "bc")) {
		t.Fatal("the blind/metadata boundary is forgeable: the blind is not length-prefixed")
	}
}

// TestMetaCommitmentForFollowsTheRecord pins the discriminator: a record with no
// stored blind was sealed under the unblinded rule and must keep verifying under
// it, because an append-only ledger cannot restate the hash rule of rows it has
// already sealed without making a legitimate history look forged.
func TestMetaCommitmentForFollowsTheRecord(t *testing.T) {
	meta := `{"analysis":"reachability"}`
	got, err := MetaCommitmentFor(nil, meta)
	if err != nil {
		t.Fatalf("an absent blind is legal: %v", err)
	}
	if !bytes.Equal(got, MetaDigest(meta)) {
		t.Fatal("a record with no blind must resolve to the legacy unblinded digest")
	}
	blind := bytes.Repeat([]byte{0x5A}, BlindLen)
	got, err = MetaCommitmentFor(blind, meta)
	if err != nil {
		t.Fatalf("a well-formed blind is legal: %v", err)
	}
	if !bytes.Equal(got, MetaCommitment(blind, meta)) {
		t.Fatal("a record with a blind must resolve to the blinded commitment")
	}
}

// TestMetaCommitmentForRefusesTheThirdState is the discriminator's whole point.
// NULL and BlindLen are the only legal states; anything else is a record whose
// own shape is illegal, and resolving it either way would mint a third hash rule.
//
// The zero-length case is the one that would slip through a naive length test: a
// non-NULL, zero-byte column is len()==0 exactly like a NULL, so "if len(blind)
// == 0 use the legacy rule" would hash a row whose column says it is blinded
// under the rule for rows that are not. It must be refused, not interpreted.
func TestMetaCommitmentForRefusesTheThirdState(t *testing.T) {
	meta := `{"analysis":"reachability"}`
	for _, bad := range [][]byte{
		{},
		bytes.Repeat([]byte{0xAA}, 1),
		bytes.Repeat([]byte{0xAA}, BlindLen-1),
		bytes.Repeat([]byte{0xAA}, BlindLen+1),
		bytes.Repeat([]byte{0xAA}, 64),
	} {
		got, err := MetaCommitmentFor(bad, meta)
		if err == nil {
			t.Fatalf("a %d-byte blind was accepted and resolved to %x", len(bad), got)
		}
		if !errors.Is(err, ErrMalformedBlind) {
			t.Fatalf("a %d-byte blind must report ErrMalformedBlind, got %v", len(bad), err)
		}
		if got != nil {
			t.Fatalf("a refused blind must yield no commitment, got %x", got)
		}
	}
}

// TestEventHashRefusesMalformedWidths proves the check cannot be bypassed by
// calling EventHash directly: the widths fixed() would silently normalize are
// refused by the hash itself, not merely by a caller that remembers to validate.
func TestEventHashRefusesMalformedWidths(t *testing.T) {
	for name, mutate := range map[string]func(*Event){
		"short commitment": func(e *Event) { e.MetaCommitment = []byte{0x01} },
		"long commitment":  func(e *Event) { e.MetaCommitment = bytes.Repeat([]byte{0x01}, hashLen+1) },
		"short prev":       func(e *Event) { e.PrevHash = []byte{0x02} },
		"short payload":    func(e *Event) { e.PayloadHash = []byte{0x03} },
	} {
		e := sampleEvent()
		mutate(&e)
		if h, err := EventHash(e); err == nil {
			t.Fatalf("%s: EventHash returned %x for a malformed event", name, h)
		}
	}
}

// TestValidateRejectsSilentlyNormalizedWidths covers what fixed() would otherwise
// absorb: a short or over-long digest still yields a well-formed hash, so the
// width has to be rejected at the boundary instead.
func TestValidateRejectsSilentlyNormalizedWidths(t *testing.T) {
	ok := sampleEvent()
	if err := ok.Validate(); err != nil {
		t.Fatalf("a well-formed event must validate: %v", err)
	}
	ok.PayloadHash = nil // "no payload" is a real state
	if err := ok.Validate(); err != nil {
		t.Fatalf("a nil payload hash must validate: %v", err)
	}
	for name, mutate := range map[string]func(*Event){
		"short commitment": func(e *Event) { e.MetaCommitment = []byte{1, 2, 3} },
		"nil commitment":   func(e *Event) { e.MetaCommitment = nil },
		"long commitment":  func(e *Event) { e.MetaCommitment = bytes.Repeat([]byte{9}, hashLen+1) },
		"short prev":       func(e *Event) { e.PrevHash = []byte{1} },
		"long payload":     func(e *Event) { e.PayloadHash = bytes.Repeat([]byte{9}, hashLen+1) },
	} {
		e := sampleEvent()
		mutate(&e)
		if err := e.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted a width the preimage encoder would silently normalize", name)
		}
	}
}
