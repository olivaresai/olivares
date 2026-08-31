// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/olivaresai/olivares/core/model"
)

// frameDomain version-binds the frame-hash preimage to its purpose, so a frame
// hash can never be confused with any other digest the engine produces
// (mirrors the ledger's olivares.audit.* domains).
const frameDomain = "olivares.recording.frame.v1"

// schemaVersion names the persisted frame schema the replay/verify envelopes
// declare, so external consumers can detect a future shape change.
const schemaVersion = "olivares.recording/v1"

// semconvVersion is the PINNED OpenTelemetry semantic-conventions vocabulary label
// the frame attribute semantics are documented against. Re-verified 2026-07-05:
// v1.41.1 (2026-05-11) is the LAST VERSIONED release that carried the gen-ai
// conventions; semconv v1.42.0 (2026-06-12, #3696) moved them from the main repo to
// open-telemetry/semantic-conventions-genai, whose live shape is pinned separately
// by semconvUpstreamRepo/Ref (0 releases; README Schema URL TODO). The GenAI
// agent-span conventions are still Development status, so this is a documentation
// pin behind a mapping layer, never a live coupling — breaking renames have already
// happened upstream (gen_ai.system → gen_ai.provider.name in 1.37.0). MIRRORS
// genAISemconvVersion (connectors/claude/genai.go) and otelGenAIVersion
// (modules/observability); keep all three equal — coordinate any bump across them.
const (
	semconvVersion      = "1.41.1"
	semconvUpstreamRepo = "open-telemetry/semantic-conventions-genai"
	semconvUpstreamRef  = "main@c321d7e, verified 2026-07-05"
)

// zeroHash is the 32-byte genesis prev-hash of every frame chain.
var zeroHash = make([]byte, sha256.Size)

// frameFields is everything that enters a frame's hash, in preimage order.
// One struct so the writer (appendFrame) and the verifier (verifySession)
// cannot drift apart on what is covered.
type frameFields struct {
	Idx       int64
	At        string
	Actor     string
	ActorKind string
	ActorUser string
	ActAs     string
	Namespace string
	Method    string
	Pattern   string
	Perm      string
	Params    map[string]string
	QueryKeys string
	Status    int64
	Outcome   string
	BodySHA   string
	BodyBytes int64
	DurMS     int64
}

// frameHash computes a frame's chain hash: SHA-256 over the domain-separated,
// length-prefixed preimage of every covered field plus the previous frame's
// hash (zeroHash at idx 1). Params are canonicalized as sorted-key JSON
// (encoding/json sorts map keys), the same convention the ledger uses for Meta.
func frameHash(tenant model.TenantID, sessionID model.ID, f frameFields, prev []byte) []byte {
	var buf []byte
	buf = lenPrefix(buf, []byte(frameDomain))
	buf = lenPrefix(buf, []byte(tenant))
	buf = lenPrefix(buf, []byte(sessionID))
	buf = appendInt(buf, f.Idx)
	buf = lenPrefix(buf, []byte(f.At))
	buf = lenPrefix(buf, []byte(f.Actor))
	buf = lenPrefix(buf, []byte(f.ActorKind))
	buf = lenPrefix(buf, []byte(f.ActorUser))
	buf = lenPrefix(buf, []byte(f.ActAs))
	buf = lenPrefix(buf, []byte(f.Namespace))
	buf = lenPrefix(buf, []byte(f.Method))
	buf = lenPrefix(buf, []byte(f.Pattern))
	buf = lenPrefix(buf, []byte(f.Perm))
	buf = lenPrefix(buf, []byte(canonicalParams(f.Params)))
	buf = lenPrefix(buf, []byte(f.QueryKeys))
	buf = appendInt(buf, f.Status)
	buf = lenPrefix(buf, []byte(f.Outcome))
	buf = lenPrefix(buf, []byte(f.BodySHA))
	buf = appendInt(buf, f.BodyBytes)
	buf = appendInt(buf, f.DurMS)
	buf = lenPrefix(buf, prev)
	sum := sha256.Sum256(buf)
	return sum[:]
}

// canonicalParams renders the redacted URL parameters deterministically
// (encoding/json marshals map keys sorted); nil/empty renders as "{}".
func canonicalParams(p map[string]string) string {
	if len(p) == 0 {
		return "{}"
	}
	b, err := json.Marshal(p)
	if err != nil {
		// A map[string]string cannot fail to marshal; keep the chain total anyway.
		return "{}"
	}
	return string(b)
}

// lenPrefix appends a 4-byte big-endian length then the bytes (the ledger's
// canonical-encoding convention, core/audit/checkpoint.go).
func lenPrefix(dst, b []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	dst = append(dst, n[:]...)
	return append(dst, b...)
}

// appendInt appends an int64 as 8 big-endian bytes.
func appendInt(dst []byte, v int64) []byte {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(v))
	return append(dst, n[:]...)
}

// fieldsOf rebuilds the hashed fields from a stored frame row (the verifier's
// half of the writer/verifier pair).
func fieldsOf(rec model.Record) frameFields {
	return frameFields{
		Idx: rec.Int(colFrIdx), At: rec.String(colFrAt),
		Actor: rec.String(colFrActor), ActorKind: rec.String(colFrActorKind),
		ActorUser: rec.String(colFrActorUser), ActAs: rec.String(colFrActAs),
		Namespace: rec.String(colFrNamespace), Method: rec.String(colFrMethod),
		Pattern: rec.String(colFrPattern), Perm: rec.String(colFrPerm),
		Params: paramsOf(rec), QueryKeys: rec.String(colFrQueryKeys),
		Status: rec.Int(colFrStatus), Outcome: rec.String(colFrOutcome),
		BodySHA: rec.String(colFrBodySHA), BodyBytes: rec.Int(colFrBodyBytes),
		DurMS: rec.Int(colFrDurMS),
	}
}

// paramsOf decodes the stored params JSON object ("" / null → nil).
func paramsOf(rec model.Record) map[string]string {
	raw := rec.String(colFrParams)
	if raw == "" {
		return nil
	}
	var p map[string]string
	if err := json.Unmarshal([]byte(raw), &p); err != nil || len(p) == 0 {
		return nil
	}
	return p
}

// decodeHexHash decodes a stored hex hash; ok=false on any malformation.
func decodeHexHash(s string) ([]byte, bool) {
	if s == "" {
		return nil, false
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return nil, false
	}
	return b, true
}
