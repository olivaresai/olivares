// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
)

// signedEvent is a fixture audit event with a non-empty Ed25519 checkpoint
// signature, so the LEEF export must carry olvSig too.
func signedEvent() model.AuditEvent {
	return model.AuditEvent{
		ID:         "11111111-1111-7111-8111-111111111111",
		TenantID:   "22222222-2222-7222-8222-222222222222",
		Seq:        42,
		OccurredAt: model.NewTimestamp(time.Unix(1700000000, 0).UTC()),
		Actor:      "user:abc",
		ActorKind:  "user",
		Action:     "access_edge.upsert",
		TargetKind: "core.access_edge",
		TargetID:   "33333333-3333-7333-8333-333333333333",
		// See fixtureEvent: real widths, and a full 64-byte signature whose base64
		// carries "==" padding.
		// A BLINDED record: the shared fixture stands for a row sealed under the
		// current rule, so the projections carry its commitment. The blind is fixed
		// (not random) because these are byte-exact goldens; a legacy row is covered
		// separately by the tests that assert the key is absent.
		MetaCommitment: canon.MetaCommitment(bytes.Repeat([]byte{0x5A}, canon.BlindLen), "{}"),
		MetaBlinded:    true,
		PrevHash:       fixedWidth(32, 0x01, 0x02, 0x03),
		Hash:           fixedWidth(32, 0x0a, 0x0b, 0x0c, 0x0d),
		Sig:            fixedWidth(64, 0xff, 0xee, 0xdd),
	}
}

func TestLEEFIsAValidFormat(t *testing.T) {
	if !audit.ValidFormat(audit.FormatLEEF) {
		t.Fatal("LEEF must be a valid audit export format (OBS-08)")
	}
}

// TestLEEFCarriesIntegrityFieldsUnaltered is the OBS-08 guarantee: a QRadar/LEEF
// shop can ingest the tamper-evident chain, and the export transports the chain's
// integrity fields (seq/prev_hash/hash/sig) WITHOUT re-deriving or altering them.
// We parse the emitted LEEF attributes back and require byte-exact equality with
// the source event's integrity fields.
func TestLEEFCarriesIntegrityFieldsUnaltered(t *testing.T) {
	ev := signedEvent()
	line, err := audit.FormatEvent(ev, audit.FormatLEEF)
	if err != nil {
		t.Fatalf("LEEF format: %v", err)
	}
	if !strings.HasPrefix(line, "LEEF:2.0|Olivares|ControlPlane|1.0|access_edge.upsert|0x09|") {
		t.Fatalf("LEEF header wrong: %q", line)
	}
	attrs := parseLEEFAttrs(t, line)

	wantHash := hex.EncodeToString(ev.Hash)
	wantPrev := hex.EncodeToString(ev.PrevHash)
	wantSig := base64.StdEncoding.EncodeToString(ev.Sig)

	if attrs["olvHash"] != wantHash {
		t.Errorf("olvHash = %q, want %q (chain hash altered!)", attrs["olvHash"], wantHash)
	}
	if attrs["olvPrevHash"] != wantPrev {
		t.Errorf("olvPrevHash = %q, want %q", attrs["olvPrevHash"], wantPrev)
	}
	if attrs["olvSig"] != wantSig {
		t.Errorf("olvSig = %q, want %q", attrs["olvSig"], wantSig)
	}
	if attrs["olvSeq"] != "42" {
		t.Errorf("olvSeq = %q, want 42", attrs["olvSeq"])
	}
	if attrs["devTime"] != "1700000000000" {
		t.Errorf("devTime = %q, want 13-digit epoch milliseconds", attrs["devTime"])
	}
	// The canonical hashed text rides verbatim next to the lossy devTime: it is
	// the exact occurred_at input of canon.EventHash.
	if attrs["olvOccurredAt"] != "2023-11-14T22:13:20.000000000Z" {
		t.Errorf("olvOccurredAt = %q, want the canonical layout text", attrs["olvOccurredAt"])
	}
	// Determinism: re-render must be byte-identical (a SIEM de-duplicates on it).
	again, _ := audit.FormatEvent(ev, audit.FormatLEEF)
	if again != line {
		t.Fatalf("LEEF not deterministic")
	}
}

// TestLEEFOmitsSigWhenUnsigned mirrors the CEF behavior: an unsigned (ordinary)
// event carries no olvSig attribute.
func TestLEEFOmitsSigWhenUnsigned(t *testing.T) {
	ev := signedEvent()
	ev.Sig = nil
	line, _ := audit.FormatEvent(ev, audit.FormatLEEF)
	if strings.Contains(line, "olvSig=") {
		t.Errorf("unsigned event must not carry olvSig: %q", line)
	}
	// The hash chain is still present so the chain is verifiable from the export.
	if !strings.Contains(line, "olvHash=0a0b0c0d") {
		t.Errorf("unsigned event still must carry the chain hash: %q", line)
	}
}

// parseLEEFAttrs splits a LEEF 2.0 line's tab-delimited key=value attribute section
// into a map (the attribute section follows the "...|0x09|" delimiter header).
func parseLEEFAttrs(t *testing.T, line string) map[string]string {
	t.Helper()
	const mark = "|0x09|"
	i := strings.Index(line, mark)
	if i < 0 {
		t.Fatalf("no LEEF delimiter header in %q", line)
	}
	out := map[string]string{}
	for _, pair := range strings.Split(line[i+len(mark):], "\t") {
		if k, v, ok := strings.Cut(pair, "="); ok {
			out[k] = v
		}
	}
	return out
}

// TestLEEFAndCEFNeverFabricateATimeForAZeroTimestamp: an event with no recorded
// time must not acquire one on the way out. Go's zero time is year 1, whose epoch
// milliseconds are a large NEGATIVE number — emitting it as devTime/rt would tell
// a SIEM the event happened in 1754, which is worse than telling it nothing (a
// receiver with no event time falls back to receipt time). The attribute is
// omitted instead; nothing else about the record changes.
func TestLEEFAndCEFNeverFabricateATimeForAZeroTimestamp(t *testing.T) {
	ev := signedEvent()
	ev.OccurredAt = model.Timestamp{}

	leef, err := audit.FormatEvent(ev, audit.FormatLEEF)
	if err != nil {
		t.Fatalf("LEEF format: %v", err)
	}
	if got := parseLEEFAttrs(t, leef)["devTime"]; got != "" {
		t.Errorf("devTime = %q for a zero timestamp, want the attribute omitted", got)
	}
	if strings.Contains(leef, "=-") {
		t.Errorf("a negative epoch reached the wire: %q", leef)
	}

	cef, err := audit.FormatEvent(ev, audit.FormatCEF)
	if err != nil {
		t.Fatalf("CEF format: %v", err)
	}
	if strings.Contains(cef, "rt=-") {
		t.Errorf("negative rt reached the wire: %q", cef)
	}

	// The chain-integrity fields are unaffected by the omission.
	if attrs := parseLEEFAttrs(t, leef); attrs["olvSeq"] != "42" || attrs["olvHash"] == "" {
		t.Errorf("integrity fields disturbed: %v", attrs)
	}

	// olvOccurredAt is DIFFERENT in kind from devTime/rt and is NOT omitted: the
	// zero Timestamp hashes as its canonical epoch-zero text (core/model/time.go:40-43),
	// so that text is ledger evidence, not a fabricated event time. A SIEM parses
	// event time from devTime/rt, which stay absent.
	const zeroText = "0001-01-01T00:00:00.000000000Z"
	if got := parseLEEFAttrs(t, leef)["olvOccurredAt"]; got != zeroText {
		t.Errorf("LEEF olvOccurredAt = %q, want the canonical epoch-zero text %q", got, zeroText)
	}
	if !strings.Contains(cef, "olvOccurredAt="+zeroText) {
		t.Errorf("CEF must carry the canonical epoch-zero text verbatim: %q", cef)
	}
}

// TestOTLPNeverFabricatesATimeForAZeroTimestamp is the third face of the same
// defect the LEEF/CEF test above pins: Go's zero time has no representable
// UnixNano, so an event with no recorded time was emitting an overflowed
// nanosecond count as its OTLP timestamp. OTLP's own encoding for "no time" is 0,
// and that is what an unset timestamp must produce. Decoded from the bare
// LogRecord projection (otlp_log_record — the token the shape moved to in the
// catalog remap); the envelope's timestamps are covered by the domain test.
func TestOTLPNeverFabricatesATimeForAZeroTimestamp(t *testing.T) {
	ev := signedEvent()
	ev.OccurredAt = model.Timestamp{}

	line, err := audit.FormatEvent(ev, audit.FormatOTLPLogRecord)
	if err != nil {
		t.Fatalf("OTLP format: %v", err)
	}
	var rec struct {
		TimeUnixNano string `json:"timeUnixNano"`
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("decode OTLP record: %v", err)
	}
	if rec.TimeUnixNano != "0" {
		t.Errorf("timeUnixNano = %q for a zero timestamp, want \"0\" (OTLP's unset)", rec.TimeUnixNano)
	}

	// A real timestamp is still carried exactly.
	ev = signedEvent()
	line, err = audit.FormatEvent(ev, audit.FormatOTLPLogRecord)
	if err != nil {
		t.Fatalf("OTLP format: %v", err)
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("decode OTLP record: %v", err)
	}
	if want := "1700000000000000000"; rec.TimeUnixNano != want {
		t.Errorf("timeUnixNano = %q, want %q", rec.TimeUnixNano, want)
	}
}
