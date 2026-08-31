// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Offline reconstruction: a consumer holding ONE emitted line rebuilds the
// canonical preimage from the fields that line carries, and recomputes the chain
// hash the line also carries. That is the property the metadata commitment
// completes, and it is proven here against a REAL store round trip — append, read
// back through the production decoder, render — never against a fabricated event,
// because a fabricated event proves the encoder agrees with the test rather than
// with the ledger.
//
// SCOPE, stated exactly rather than implied. This proves PREIMAGE RECOMPUTATION
// only. It does NOT prove authenticity (that needs an externally trusted Ed25519
// key, which no single line carries) and it does NOT prove completeness or
// ordering (that needs adjacent records and a checkpoint). Those are three
// separate claims and no surface may collapse them into "independent verification".
//
// COVERED TOKENS: syslog, CEF, LEEF and all three OTLP spellings.
//
// The claim is UNCONDITIONAL for the three TEXT dialects — syslog, CEF and LEEF —
// and does not depend on which bytes the values carry, invalid UTF-8 included.
//
// The three OTLP spellings stay CONDITIONAL on UTF-8 validity, and the limit is
// theirs rather than this encoding's: they carry every field as a JSON string, and
// encoding/json replaces an invalid byte with U+FFFD, so the line stops reproducing
// the hash printed beside it. An earlier revision of this header claimed all six were
// unconditional while a paragraph below it said the OTLP shapes were not — the two
// sentences contradicted each other and the hostile fixture, being entirely valid
// UTF-8, could not tell them apart. TestReconstructionRejectsWhatItCannotCarry now
// does.
//
// What changed in the TEXT dialects, which is where the qualification lifted:
//
//   - syslog and LEEF used to SUBSTITUTE a space for the bytes they could not carry
//     — CR and LF in an SD-PARAM, and in LEEF the TAB that is its own declared
//     delimiter. The substitution is gone. Control bytes now travel percent-encoded
//     (siemwire.EscapeControlBytes) UNDERNEATH each dialect's own escaping, in an
//     alphabet neither dialect's escape touches, so the two passes compose and a
//     consumer unwinds them in a fixed order;
//   - CEF joined the set for the same reason plus two fixes it needed of its own:
//     its extension KEY was written with no treatment at all (a caller field named
//     `x y` forged an extension pair) and its header folded only CR/LF, letting a
//     NUL through to truncate the record at a C-string receiver.
//
// The old text justified the qualification by saying the ledger's own values never
// contain those bytes — UUIDs, `kind:id` actors, dotted action verbs, a fixed-layout
// canonical timestamp and hex digests. That was true and it was not a guarantee:
// NOTHING at the append boundary enforces that alphabet (core/model.AuditDraft
// imposes none), so the property rested on an assumption about callers.
// TestReconstructionHoldsForValuesCarryingFramingBytes now seals an event whose
// values carry every one of those bytes and requires all six tokens to re-verify.
//
// OCSF remains excluded, and for a reason no encoding change reaches: it gives actor
// and action no verbatim `unmapped` channel and defaults an empty actor to the device
// product, so the line does not CARRY the inputs. That is a projection gap, not an
// escaping one.
package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// reconstructableTokens are the ledger tokens whose emitted line carries every
// preimage input in a form a decoder can return byte-for-byte.
//
// CEF and LEEF joined the set when the text dialects stopped substituting for the
// bytes they could not carry. They were excluded for one reason each and both are
// now closed: LEEF replaced a TAB — its own declared delimiter — with a space, and
// CEF let every C0 control other than CR/LF through raw.
var reconstructableTokens = []audit.Format{
	audit.FormatSyslog,
	audit.FormatCEF,
	audit.FormatLEEF,
	audit.FormatOTLP,
	audit.FormatOTLPEnvelope,
	audit.FormatOTLPLogRecord,
}

// cefLeefPreimageKeys maps the CEF/LEEF field vocabulary onto the canonical
// preimage names the rebuilder uses. Both dialects carry the same extension set
// (cefExtFields), so one map serves both.
var cefLeefPreimageKeys = map[string]string{
	"olvSeq": "seq", "olvTenant": "tenant", "olvOccurredAt": "occurred_at",
	"suser": "actor", "olvActorKind": "actor_kind", "act": "action",
	"olvTargetKind": "target_kind", "olvTargetId": "target_id",
	"olvMetaCommitment": "meta_commitment", "olvPayloadHash": "payload_hash",
	"olvPrevHash": "prev_hash", "olvHash": "hash",
}

// cefExtUnescaper reverses the CEF extension escaping the standard defines. The
// percent layer underneath it is undone separately, by UnescapeControlBytes; the
// two use disjoint characters, which is what lets them be unwound in sequence.
var cefExtUnescaper = strings.NewReplacer(`\\`, `\`, `\=`, `=`, `\n`, "\n", `\r`, "\r")

// cefExtValues parses a CEF extension section into raw key/value pairs.
//
// It exists because the older test parser split the section on every SPACE, which
// is only correct while no value contains one — and the point of this file is a
// claim that does NOT depend on which characters the values happen to hold. The
// grammar is unambiguous without that assumption: this encoder escapes '=' inside
// a value as `\=`, so an UNESCAPED '=' can only be a key/value boundary, and the
// key is the run of key characters immediately before it.
func cefExtValues(t *testing.T, line string) map[string]string {
	t.Helper()
	const hdr = "CEF:0|"
	if !strings.HasPrefix(line, hdr) {
		t.Fatalf("cef: not a CEF:0 line: %q", line)
	}
	rest := line[len(hdr):]
	for i := 0; i < 6; i++ { // vendor|product|version|signature|name|severity|
		j := nextUnescapedPipe(rest)
		if j < 0 {
			t.Fatalf("cef: truncated header in %q", line)
		}
		rest = rest[j+1:]
	}
	// Collect the offsets of every unescaped '=' — each one is a boundary.
	type boundary struct{ key, valStart int }
	var bs []boundary
	for i := 0; i < len(rest); i++ {
		if rest[i] != '=' || (i > 0 && rest[i-1] == '\\') {
			continue
		}
		k := i
		for k > 0 && isCEFKeyByte(rest[k-1]) {
			k--
		}
		if k == i {
			t.Fatalf("cef: '=' at %d has no key in %q", i, line)
		}
		bs = append(bs, boundary{key: k, valStart: i + 1})
	}
	if len(bs) == 0 {
		t.Fatalf("cef: no extension pairs in %q", line)
	}
	out := map[string]string{}
	for i, b := range bs {
		end := len(rest)
		if i+1 < len(bs) {
			// The value runs up to the SP that separates it from the next key.
			end = bs[i+1].key
			if end > 0 && rest[end-1] == ' ' {
				end--
			}
		}
		key := rest[b.key : b.valStart-1]
		if _, dup := out[key]; dup {
			t.Fatalf("cef: duplicate extension key %q", key)
		}
		out[key] = rest[b.valStart:end]
	}
	return out
}

func isCEFKeyByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// carriedFields returns the preimage inputs a single emitted line carries, keyed
// by their canonical names. It reads the line the way a CONSUMER would — parse the
// dialect, take the values — so a field the encoder forgot shows up as a missing
// key rather than as a silently zero value.
func carriedFields(t *testing.T, f audit.Format, line string) map[string]string {
	t.Helper()
	out := map[string]string{}
	switch f {
	case audit.FormatSyslog:
		params, err := syslogSDParams(line)
		if err != nil {
			t.Fatalf("syslog: %v", err)
		}
		for _, k := range []string{"seq", "tenant", "occurred_at", "actor", "actor_kind",
			"action", "target_kind", "target_id", "meta_commitment", "payload_hash", "prev_hash", "hash"} {
			v, ok := params[k]
			if !ok {
				t.Fatalf("syslog: no %q SD-PARAM in %q", k, line)
			}
			// syslogSDParams has already unwound RFC 5424 §6.3.3's three mandated
			// escapes and NOTHING else, exactly as a conforming receiver would. The
			// control layer underneath is unwound here, second. The order is fixed and
			// the two passes share no character, which is why they compose — see
			// siemwire.EscapeControlBytes.
			dec, ok := siemwire.UnescapeControlBytes(v)
			if !ok {
				t.Fatalf("syslog: SD-PARAM %q value %q is not decodable", k, v)
			}
			out[k] = dec
		}
	case audit.FormatLEEF:
		attrs, err := leefAttrPairs(line)
		if err != nil {
			t.Fatalf("leef: %v", err)
		}
		for wire, canonical := range cefLeefPreimageKeys {
			v, ok := attrs[wire]
			if !ok {
				t.Fatalf("leef: no %q attribute in %q", wire, line)
			}
			// LEEF publishes no escaping of its own, so there is exactly one pass.
			dec, ok := siemwire.UnescapeControlBytes(v)
			if !ok {
				t.Fatalf("leef: attribute %q value %q is not decodable", wire, v)
			}
			out[canonical] = dec
		}
	case audit.FormatCEF:
		ext := cefExtValues(t, line)
		for wire, canonical := range cefLeefPreimageKeys {
			v, ok := ext[wire]
			if !ok {
				t.Fatalf("cef: no %q extension key in %q", wire, line)
			}
			// PERCENT first, then CEF's own backslash escaping — the exact reverse of the
			// encode order. It matters: CEF's `\n` escape legitimately yields a raw CR
			// or LF when unwound, so unwinding CEF first would hand the percent decoder
			// raw control bytes, which it refuses as non-canonical (and rightly: the
			// encoder never puts one on the wire).
			pct, ok := siemwire.UnescapeControlBytes(v)
			if !ok {
				t.Fatalf("cef: extension %q value %q is not decodable", wire, v)
			}
			dec, ok := cefExtUnescaper.Replace(pct), true
			if !ok {
				t.Fatalf("cef: extension %q value %q is not decodable", wire, v)
			}
			out[canonical] = dec
		}
	case audit.FormatOTLP, audit.FormatOTLPEnvelope, audit.FormatOTLPLogRecord:
		attrs, body := otlpAttributesAndBody(t, f, line)
		for k, v := range attrs {
			const prefix = "ai.olivares.audit."
			if len(k) > len(prefix) && k[:len(prefix)] == prefix {
				out[k[len(prefix):]] = v
			}
		}
		// Require every key, exactly like the syslog branch. Without this the map
		// silently yields "" for an attribute the encoder dropped, hex-decodes it to
		// an empty slice, and the preimage encoder zero-pads it — so deleting an
		// all-zero field from the wire would still reconstruct, and the test would
		// certify a projection that is missing a field.
		for _, k := range []string{"seq", "tenant", "occurred_at", "actor", "actor_kind",
			"target_kind", "target_id", "meta_commitment", "payload_hash", "prev_hash", "hash"} {
			if _, ok := out[k]; !ok {
				t.Fatalf("%s: no ai.olivares.audit.%s attribute in %q", f, k, line)
			}
		}
		// action rides in the record body, not in the attribute set — a consumer
		// that only walked the attributes would rebuild a preimage missing a field
		// and see a mismatch it could not explain.
		out["action"] = body
	default:
		t.Fatalf("carriedFields: token %q is not in the reconstructable set", f)
	}
	return out
}

// otlpAttributesAndBody decodes either OTLP shape into its record attributes and
// body string.
func otlpAttributesAndBody(t *testing.T, f audit.Format, line string) (map[string]string, string) {
	t.Helper()
	type kv struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	}
	type record struct {
		Body struct {
			StringValue string `json:"stringValue"`
		} `json:"body"`
		Attributes []kv `json:"attributes"`
	}
	var rec record
	if f == audit.FormatOTLPLogRecord {
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("otlp_log_record: %v", err)
		}
	} else {
		var req struct {
			ResourceLogs []struct {
				ScopeLogs []struct {
					LogRecords []record `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(req.ResourceLogs) != 1 || len(req.ResourceLogs[0].ScopeLogs) != 1 ||
			len(req.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
			t.Fatalf("%s: want exactly one resource/scope/record, got %q", f, line)
		}
		rec = req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	}
	attrs := map[string]string{}
	for _, a := range rec.Attributes {
		if _, dup := attrs[a.Key]; dup {
			t.Fatalf("%s: duplicate attribute %q — reconstruction would be ambiguous", f, a.Key)
		}
		attrs[a.Key] = a.Value.StringValue
	}
	return attrs, rec.Body.StringValue
}

// rebuildPreimage turns the carried fields back into a canon.Event. Every value
// comes from the LINE; nothing is borrowed from the store, which is the whole
// point — borrowing even one field would make the test prove nothing.
func rebuildPreimage(t *testing.T, got map[string]string) canon.Event {
	t.Helper()
	seq, err := strconv.ParseInt(got["seq"], 10, 64)
	if err != nil {
		t.Fatalf("seq %q: %v", got["seq"], err)
	}
	decode := func(name string) []byte {
		b, derr := hex.DecodeString(got[name])
		if derr != nil {
			t.Fatalf("%s %q: %v", name, got[name], derr)
		}
		return b
	}
	return canon.Event{
		TenantID:       got["tenant"],
		Seq:            seq,
		OccurredAt:     got["occurred_at"],
		Actor:          got["actor"],
		ActorKind:      got["actor_kind"],
		Action:         got["action"],
		TargetKind:     got["target_kind"],
		TargetID:       got["target_id"],
		MetaCommitment: decode("meta_commitment"),
		PayloadHash:    decode("payload_hash"),
		PrevHash:       decode("prev_hash"),
	}
}

// sealedLedgerEvent appends one event through a REAL store and reads it back the
// way every export path does, so the event under test is decoder-produced rather
// than hand-built.
func sealedLedgerEvent(t *testing.T) model.AuditEvent {
	t.Helper()
	return sealedLedgerEventWith(t, model.AuditDraft{
		Actor: "user:abc", ActorKind: "user", Action: "access_edge.upsert",
		TargetKind: "core.access_edge", TargetID: model.NewID(),
		// A real payload hash, so payload_hash is an EXERCISED preimage input rather
		// than an empty string that reconstructs no matter what.
		PayloadHash: fixedWidth(32, 0xde, 0xad, 0xbe, 0xef),
		// Non-empty, low-entropy metadata: exactly the shape whose unblinded
		// digest would be dictionary-attackable from an exported line.
		Meta: map[string]any{"ip": "203.0.113.7", "reason": "policy"},
	})
}

// sealedLedgerEventWith is sealedLedgerEvent parameterized by the draft, so a test
// can seal an event whose values carry bytes the ordinary ledger never emits and
// still get an event the STORE produced rather than one the test built.
func sealedLedgerEventWith(t *testing.T, draft model.AuditDraft) model.AuditEvent {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(ctx, draft)
		return aerr
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	var out model.AuditEvent
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			out = ev
			return nil
		})
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	if out.Seq == 0 {
		t.Fatal("no event was read back from the store")
	}
	if len(out.MetaCommitment) != 32 {
		t.Fatalf("the decoder did not resolve the metadata commitment: %d bytes", len(out.MetaCommitment))
	}
	return out
}

// TestOneEmittedLineRecomputesTheChainHash is the unit's reason to exist: for each
// covered token, the fields the line carries rebuild the preimage, and the hash of
// that preimage equals the hash the same line carries.
func TestOneEmittedLineRecomputesTheChainHash(t *testing.T) {
	ev := sealedLedgerEvent(t)
	for _, f := range reconstructableTokens {
		line, err := audit.FormatEvent(ev, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		got := carriedFields(t, f, line)
		want, err := hex.DecodeString(got["hash"])
		if err != nil {
			t.Fatalf("%s: carried hash %q: %v", f, got["hash"], err)
		}
		if len(want) != 32 {
			t.Fatalf("%s: carried hash is %d bytes, want 32", f, len(want))
		}
		recomputed := mustLineHash(t, rebuildPreimage(t, got))
		if hex.EncodeToString(recomputed) != hex.EncodeToString(want) {
			t.Fatalf("%s: the line does not re-verify\n recomputed: %x\n carried:    %x",
				f, recomputed, want)
		}
		// And the carried hash is the one the STORE sealed, not merely internally
		// consistent with itself.
		if hex.EncodeToString(want) != hex.EncodeToString(ev.Hash) {
			t.Fatalf("%s: carried hash differs from the sealed hash", f)
		}
	}
}

// TestReconstructionHoldsForValuesCarryingFramingBytes is what makes the claim
// UNCONDITIONAL rather than "true for the values the ledger happens to emit".
//
// Until the text dialects stopped substituting, this property did not hold and
// could not be tested: a CR, an LF or a TAB in a hashed input was replaced by a
// space on the way out, so the rendered line described a record whose hash it
// could no longer reproduce — and it did so silently, because a space is a
// perfectly ordinary character to find in a value.
//
// The alphabet swept here is the one that actually breaks things: the bytes that
// FRAME a record in one dialect or another (CR, LF, TAB, NUL), the escape
// introducers of both layers ('%' and '\'), and the characters each grammar
// reserves (the SD-PARAM quote and bracket, the CEF '=' and space, the LEEF '=').
// Nothing at the append boundary forbids any of them — core/model.AuditDraft
// imposes no alphabet — so "the ledger would never emit that" was an assumption
// about callers, not a property of the system.
func TestReconstructionHoldsForValuesCarryingFramingBytes(t *testing.T) {
	const hostile = "a\rb\nc\td\x00e%f\\g\"h]i=j k"
	ev := sealedLedgerEventWith(t, model.AuditDraft{
		Actor:       "user:" + hostile,
		ActorKind:   "user\ttab",
		Action:      "access_edge.upsert" + hostile,
		TargetKind:  "core.access_edge",
		TargetID:    model.NewID(),
		PayloadHash: fixedWidth(32, 0xde, 0xad, 0xbe, 0xef),
		Meta:        map[string]any{"ip": "203.0.113.7"},
	})
	for _, f := range reconstructableTokens {
		line, err := audit.FormatEvent(ev, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		// First: the record must still be ONE record. A raw CR or LF would split it
		// at any line-oriented receiver, and a NUL would truncate it at a receiver
		// that stores the line as a C string — the failures the encoding exists to
		// prevent, and the reason substitution was there in the first place.
		if strings.ContainsAny(line, "\r\n\x00") {
			t.Fatalf("%s: a framing byte reached the wire: %q", f, line)
		}
		got := carriedFields(t, f, line)
		if got["actor"] != ev.Actor {
			t.Errorf("%s: actor did not survive the round trip:\n got: %q\nwant: %q",
				f, got["actor"], ev.Actor)
		}
		recomputed := mustLineHash(t, rebuildPreimage(t, got))
		if hex.EncodeToString(recomputed) != hex.EncodeToString(ev.Hash) {
			t.Fatalf("%s: a line whose values carry framing bytes does not re-verify\n"+
				" recomputed: %x\n sealed:     %x\n line:       %q", f, recomputed, ev.Hash, line)
		}
	}
}

// TestReconstructionDetectsTamperInEveryCarriedField flips one carried field at a
// time and requires the recomputation to diverge. Without this, a preimage that
// silently ignored a field would pass the test above.
func TestReconstructionDetectsTamperInEveryCarriedField(t *testing.T) {
	ev := sealedLedgerEvent(t)
	line, err := audit.FormatEvent(ev, audit.FormatOTLP)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	base := carriedFields(t, audit.FormatOTLP, line)
	tampers := map[string]func(map[string]string){
		"tenant":      func(m map[string]string) { m["tenant"] = model.NewTenantID().String() },
		"seq":         func(m map[string]string) { m["seq"] = "999" },
		"occurred_at": func(m map[string]string) { m["occurred_at"] = "2020-01-01T00:00:00.000000000Z" },
		"actor":       func(m map[string]string) { m["actor"] = "user:mallory" },
		"actor_kind":  func(m map[string]string) { m["actor_kind"] = "agent" },
		"action":      func(m map[string]string) { m["action"] = "access_edge.delete" },
		"target_kind": func(m map[string]string) { m["target_kind"] = "core.session" },
		"target_id":   func(m map[string]string) { m["target_id"] = model.NewID().String() },
		"meta_commitment": func(m map[string]string) {
			m["meta_commitment"] = hex.EncodeToString(canon.MetaDigest(`{"ip":"0.0.0.0"}`))
		},
		"prev_hash":    func(m map[string]string) { m["prev_hash"] = hex.EncodeToString(canon.MetaDigest("other")) },
		"payload_hash": func(m map[string]string) { m["payload_hash"] = hex.EncodeToString(canon.MetaDigest("other payload")) },
	}
	for name, tamper := range tampers {
		altered := map[string]string{}
		for k, v := range base {
			altered[k] = v
		}
		tamper(altered)
		if hex.EncodeToString(mustLineHash(t, rebuildPreimage(t, altered))) == hex.EncodeToString(ev.Hash) {
			t.Fatalf("tampering with %q did not change the recomputed hash: that input is not in the preimage", name)
		}
	}
}

// TestTheCommitmentOnTheWireIsBlinded proves the exported value is the HIDING
// commitment and not the dictionary-attackable digest of the metadata. A consumer
// holding the line must not be able to confirm a guessed metadata value, which is
// exactly what hashing the guess and comparing would do if the export carried the
// unblinded digest.
func TestTheCommitmentOnTheWireIsBlinded(t *testing.T) {
	ev := sealedLedgerEvent(t)
	line, err := audit.FormatEvent(ev, audit.FormatOTLP)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	carried := carriedFields(t, audit.FormatOTLP, line)["meta_commitment"]
	guessed := hex.EncodeToString(canon.MetaDigest(`{"ip":"203.0.113.7","reason":"policy"}`))
	if carried == guessed {
		t.Fatal("the exported commitment is the unblinded digest of the metadata: a holder of one line can confirm a guess offline")
	}
	if len(carried) != 64 {
		t.Fatalf("carried commitment is %d hex chars, want 64", len(carried))
	}
}

// TestTwoEventsWithIdenticalMetadataDoNotCollide closes the equality oracle: with
// an unblinded digest, two records whose metadata matches export the SAME value,
// leaking a relation the projection deliberately withholds.
func TestTwoEventsWithIdenticalMetadataDoNotCollide(t *testing.T) {
	first := sealedLedgerEvent(t)
	second := sealedLedgerEvent(t)
	if hex.EncodeToString(first.MetaCommitment) == hex.EncodeToString(second.MetaCommitment) {
		t.Fatal("two records with identical metadata share a commitment: the export leaks metadata equality")
	}
}

// TestALegacyRowCarriesNoCommitmentAndLeaksNothing pins the present-IFF-reconstructable
// rule on every dialect. A row sealed before metadata blinding existed has no
// commitment to carry, and the two tempting alternatives are both wrong: an EMPTY
// field is pseudo-evidence (a consumer decodes it to an empty digest, the preimage
// encoder zero-pads it, and reconstruction fails in a way indistinguishable from
// tampering), and its UNBLINDED digest is the dictionary-attackable value the blind
// exists to prevent — exported, for the first time, on exactly the records that
// predate the protection.
func TestALegacyRowCarriesNoCommitmentAndLeaksNothing(t *testing.T) {
	meta := `{"ip":"203.0.113.7"}`
	legacy := model.AuditEvent{
		ID: model.NewID(), TenantID: model.NewTenantID(), Seq: 3,
		OccurredAt: model.NewTimestamp(time.Unix(1700000000, 0).UTC()),
		Actor:      "user:abc", ActorKind: "user", Action: "access_edge.upsert",
		TargetKind: "core.access_edge", TargetID: model.NewID(),
		// EXACTLY what the row decoder yields for a pre-blinding row: the commitment
		// is POPULATED, with the legacy unblinded digest, and MetaBlinded is false.
		// Building this fixture with a nil commitment instead would test a state the
		// store never produces, and the assertions below would hold for the wrong
		// reason — the projections would be omitting a key that was empty rather
		// than declining to publish a value that leaks.
		MetaCommitment: canon.MetaDigest(meta),
		MetaBlinded:    false,
		PayloadHash:    fixedWidth(32, 0x01),
		PrevHash:       fixedWidth(32, 0x02),
		Hash:           fixedWidth(32, 0x03),
	}
	leak := hex.EncodeToString(canon.MetaDigest(meta))
	for _, f := range []audit.Format{audit.FormatCEF, audit.FormatLEEF, audit.FormatSyslog,
		audit.FormatOTLP, audit.FormatOTLPEnvelope, audit.FormatOTLPLogRecord, audit.FormatOCSF} {
		line, err := audit.FormatEvent(legacy, f)
		if err != nil {
			t.Fatalf("format %s: a legacy row must still export: %v", f, err)
		}
		if strings.Contains(line, "meta_commitment") || strings.Contains(line, "olvMetaCommitment") {
			t.Fatalf("%s: a legacy row must carry NO commitment key, got: %s", f, line)
		}
		if strings.Contains(line, leak) {
			t.Fatalf("%s: the line carries the UNBLINDED metadata digest — the exact oracle the blind closes", f)
		}
	}
}

// TestExportRefusesAWrongWidthCommitment keeps the other half of the rule: absent is
// legal, short is not. A present-but-short commitment would render a well-formed line
// whose reconstruction fails at the consumer, which reads as tampering.
func TestExportRefusesAWrongWidthCommitment(t *testing.T) {
	ev := sealedLedgerEvent(t)
	ev.MetaCommitment = []byte{1, 2, 3}
	if _, err := audit.FormatEvent(ev, audit.FormatOTLP); err == nil {
		t.Fatal("FormatEvent accepted a 3-byte metadata commitment")
	}
	ev.MetaCommitment = nil
	ev.PayloadHash = []byte{9}
	if _, err := audit.FormatEvent(ev, audit.FormatOTLP); err == nil {
		t.Fatal("FormatEvent accepted a 1-byte payload hash")
	}
}

// TestSegmentFormatFollowsItsOwnBytes pins the version bump as BIDIRECTIONAL. A
// segment whose rows all predate metadata blinding contains no meta_blind field, so
// it is byte-for-byte a v1 artifact: stamping it v2 would claim a shape its bytes do
// not have, strand it at any verifier that only knows v1, and break the byte-identical
// re-put a retried export to a WORM sink depends on. Only a segment that actually
// carries a blind claims v2.
func TestSegmentFormatFollowsItsOwnBytes(t *testing.T) {
	if audit.ArchiveFormat == audit.ArchiveFormatV1 {
		t.Fatal("the two format tags must differ for this test to mean anything")
	}
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 3, "blinded")
	var seg audit.Segment
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var ok bool
		var berr error
		seg, ok, berr = audit.BuildSegment(ctx, sc.Audit(), tenant, 1, 0, 0)
		if berr != nil {
			return berr
		}
		if !ok {
			t.Fatal("no segment was built")
		}
		return nil
	}); err != nil {
		t.Fatalf("build segment: %v", err)
	}
	if seg.Manifest.Format != audit.ArchiveFormat {
		t.Fatalf("a segment carrying blinds must be stamped %q, got %q",
			audit.ArchiveFormat, seg.Manifest.Format)
	}
	if !strings.Contains(string(seg.Events), "meta_blind") {
		t.Fatal("a v2 segment must actually carry the blind it claims")
	}
}

// TestTheManifestFormatIsBoundToTheSegmentContent pins the binding in BOTH
// directions: a tag that claims v1 while the body carries a blind is refused, and
// so is a tag that claims v2 over a body that carries none.
//
// Honest scope, because it is easy to overstate this one: the binding is a SHAPE
// guarantee, not what stops rule substitution. Each line's rule is already bound
// by its own hash — the blind enters the preimage through the commitment, so
// stripping it from a line or adding one to another changes the derived commitment
// and fails as a hash mismatch. What this adds is that a segment is exactly what
// the builder would have produced for its content, which is what the byte-identical
// re-put of a retried export depends on, and it removes the ambiguity of a file
// whose declared grammar and body disagree.
func TestTheManifestFormatIsBoundToTheSegmentContent(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 3, "blinded")
	var seg audit.Segment
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var ok bool
		var berr error
		seg, ok, berr = audit.BuildSegment(ctx, sc.Audit(), tenant, 1, 0, 0)
		if berr != nil {
			return berr
		}
		if !ok {
			t.Fatal("no segment was built")
		}
		return nil
	}); err != nil {
		t.Fatalf("build segment: %v", err)
	}

	// The honest artifact verifies.
	dir := t.TempDir()
	writeSegmentTo(t, dir, tenant, seg)
	if rep, verr := audit.VerifyArchiveDir(ctx, dir, audit.ArchiveVerifyOptions{}); verr != nil || !rep.OK {
		t.Fatalf("the segment as built must verify: err=%v rep=%+v", verr, rep)
	}

	// Same bytes, a manifest that lies about the grammar.
	lying := t.TempDir()
	downgraded := seg
	downgraded.Manifest.Format = audit.ArchiveFormatV1
	writeSegmentTo(t, lying, tenant, downgraded)
	rep, verr := audit.VerifyArchiveDir(ctx, lying, audit.ArchiveVerifyOptions{})
	if verr != nil {
		t.Fatalf("verify: %v", verr)
	}
	if rep.OK {
		t.Fatal("a manifest claiming v1 over a body carrying blinds was accepted")
	}
	if rep.Reason != "format-content-mismatch" {
		t.Fatalf("the refusal must name the mismatch, got %q", rep.Reason)
	}
}

// writeSegmentTo lays one built segment out on disk the way an archive sink does,
// so VerifyArchiveDir sees a real artifact rather than an in-memory struct.
func writeSegmentTo(t *testing.T, dir string, tenant model.TenantID, seg audit.Segment) {
	t.Helper()
	evPath := filepath.Join(dir, audit.SegmentKey(tenant.String(), seg.Manifest.FromSeq, seg.Manifest.ToSeq))
	if err := os.MkdirAll(filepath.Dir(evPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evPath, seg.Events, 0o644); err != nil {
		t.Fatal(err)
	}
	man, err := json.Marshal(seg.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	manPath := filepath.Join(dir, audit.SegmentManifestKey(tenant.String(), seg.Manifest.FromSeq, seg.Manifest.ToSeq))
	if err := os.WriteFile(manPath, man, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestABlindedSegmentRebuildsToTheSameBytes is what a retried export to a WORM
// sink depends on. Delivery is at-least-once, so a crash between the PUT and the
// receipt means the same range is built and put again; if the second build
// differed by a byte, an object-locked sink would hold two irreconcilable versions
// of the same evidence and the re-put would stop being idempotent.
//
// The blind makes this worth asserting rather than assuming: it is the one field
// in the line drawn from a random source. It is drawn ONCE, at append, and stored
// — so rebuilding reads it back rather than generating it, and the segment is
// reproducible. A design that re-derived the blind at export time would be
// non-deterministic here, which is exactly what this pins.
func TestABlindedSegmentRebuildsToTheSameBytes(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)
	appendMetaEvents(t, st, tenant, 4, "replay")

	build := func() audit.Segment {
		t.Helper()
		var seg audit.Segment
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var ok bool
			var berr error
			seg, ok, berr = audit.BuildSegment(ctx, sc.Audit(), tenant, 1, 0, 0)
			if berr != nil {
				return berr
			}
			if !ok {
				t.Fatal("no segment was built")
			}
			return nil
		}); err != nil {
			t.Fatalf("build segment: %v", err)
		}
		return seg
	}

	first, second := build(), build()
	if !bytes.Equal(first.Events, second.Events) {
		t.Fatal("rebuilding the same range produced different event bytes; a retried export would not re-put an identical object")
	}
	if first.Manifest != second.Manifest {
		t.Fatalf("rebuilding the same range produced a different manifest:\n %+v\n %+v", first.Manifest, second.Manifest)
	}
	if !strings.Contains(string(first.Events), "meta_blind") {
		t.Fatal("this fixture must carry blinds for the assertion to mean anything")
	}
}

// TestReconstructionRejectsWhatItCannotCarry draws the boundary of the claim with a
// byte the ledger's own append boundary does not forbid: a lone 0xFF, which is not
// part of any well-formed UTF-8 sequence.
//
// It exists because the boundary was ASSERTED and not tested. The hostile fixture
// above is entirely valid UTF-8, so it could not distinguish a dialect that carries
// any byte from one that carries only text — and the file's own header said both
// things in two different paragraphs.
//
// The text dialects recover it, because percent-encoding puts an ASCII spelling on
// the wire and the decode restores the original byte. The OTLP shapes do not, and the
// reason is structural rather than fixable here: every field travels as a JSON string
// and encoding/json substitutes U+FFFD for an invalid byte, so the line stops
// reproducing the hash it also carries.
func TestReconstructionRejectsWhatItCannotCarry(t *testing.T) {
	ev := sealedLedgerEventWith(t, model.AuditDraft{
		Actor:       "user:a\xffz", // a lone continuation byte: never valid UTF-8
		ActorKind:   "user",
		Action:      "access_edge.upsert",
		TargetKind:  "core.access_edge",
		TargetID:    model.NewID(),
		PayloadHash: fixedWidth(32, 0xde, 0xad, 0xbe, 0xef),
		Meta:        map[string]any{"ip": "203.0.113.7"},
	})

	textDialects := map[audit.Format]bool{
		audit.FormatSyslog: true, audit.FormatCEF: true, audit.FormatLEEF: true,
	}
	for _, f := range reconstructableTokens {
		line, err := audit.FormatEvent(ev, f)
		if err != nil {
			t.Fatalf("format %s: %v", f, err)
		}
		got := carriedFields(t, f, line)
		recovered := got["actor"] == ev.Actor
		if textDialects[f] {
			if !recovered {
				t.Errorf("%s: a text dialect must carry any byte, got %q want %q",
					f, got["actor"], ev.Actor)
			}
			continue
		}
		// The OTLP shapes: assert the LIMIT, so the day one of them starts carrying
		// invalid bytes verbatim this test tells us the claim can be widened rather
		// than leaving the documentation stale in the safe direction.
		if recovered {
			t.Errorf("%s recovered an invalid UTF-8 value; the conditional claim in this "+
				"file's header and in core/audit/export.go can now be widened", f)
		}
	}
}
