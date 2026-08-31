// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// evidence.go (q1-MCP) — the evidence binding of the tools/call gate: strict
// params canonicalization, the client operation-key extension, and the
// OperationID/EffectDigest derivations the frozen S5 contract (sdk/evidence.go)
// requires BEFORE an external effect is emitted.
//
// # Canonical form (deliberately OURS, not RFC 8785)
//
// The gate re-encodes the JSON-RPC params into ONE deterministic canonical form and
// applies it consistently to BOTH the EffectDigest and the forwarded bytes — the
// bytes governed are the bytes sent (the F3 discipline). The form:
//
//   - strict decode: exactly one JSON value, NO duplicate object keys at ANY depth
//     (encoding/json silently keeps the last duplicate — a smuggling vector where the
//     gate reads one value and a first-wins upstream reads another; the token walk
//     below rejects the ambiguity outright), no trailing data;
//   - object keys recursively sorted (byte-wise);
//   - number literals PRESERVED exactly as received (json.Number): {"a":1} and
//     {"a":1.0} are DIFFERENT canonical bytes and therefore different digests —
//     the gate never asserts numeric equality across spellings;
//   - strings re-encoded with encoding/json's deterministic escaping, and — round-9
//     R9-02 — only AFTER the two contents whose decode is lossy have been refused
//     (invalid UTF-8 and unpaired UTF-16 surrogate escapes, rejectLossyJSONStrings).
//     Without that refusal the form is not INJECTIVE: `"\ud800"` and `"\ud801"` both
//     decode to U+FFFD, so two materially different accepted bodies share one digest;
//   - absent params, explicit null params and present params are three DISTINCT
//     identities (the params-presence marker binds into the EffectDigest).
//
// Full RFC 8785 (JCS) conformance is NOT claimed: JCS mandates ES6 number
// re-serialization (which would ERASE the 1-vs-1.0 distinction) and UTF-16 key
// ordering; this form deliberately preserves literals and sorts keys byte-wise.
// What matters for the contract is determinism + consistency between the digest and
// the forwarded bytes, and this form provides both.
//
// # The operation-key extension
//
// params._meta["ai.olivares/operationId"] is the OPTIONAL Olivares extension a
// client supplies for strong idempotency (design §3). It namespaces the OperationID
// under the caller's full identity (tenant, resource, issuer, client, subject,
// act-as), is STRIPPED from the forwarded params (the upstream never sees it), and
// is never persisted raw. Without it the gate mints a cryptographically random
// server OperationID per received request, labeled request_instance — evidence
// enforcement still holds, but NO transport-retry dedup is claimed for such legacy
// clients (a resent identical request is a new operation).
//
// # Effect view
//
// The EffectDigest excludes the versioned W3C trace-correlation members
// (_meta.traceparent/tracestate/baggage): a legitimate retry carries a fresh trace
// id but is the SAME effect, so correlation identifiers must not change the effect
// identity. They remain IN the forwarded bytes (propagation is their purpose).

// mcpOperationKeyMeta is the params._meta member carrying the client operation key.
const mcpOperationKeyMeta = "ai.olivares/operationId"

// Domain-separation labels of the MCP evidence derivations (length-prefixed,
// never joined with delimiters — see evidenceLPDigest).
const (
	mcpOperationDomainV1 = "olivares.mcp.operation.v1"
	mcpEffectDomainV1    = "olivares.mcp.effect.v1"
	mcpEffectProfileV1   = "mcp-binding-v1"
	mcpPolicyDomainV1    = "olivares.mcp.policy.v1"
	mcpOperationSurface  = "mcp.resource-server"
)

// OperationID provenance labels: a keyed operation carries client-supplied
// idempotency; a request_instance operation is server-minted per received request
// (no transport-retry dedup claim).
const (
	opIDKindKeyed           = "keyed"
	opIDKindRequestInstance = "request_instance"
)

// paramsPresence markers bound into the EffectDigest.
const (
	paramsAbsent  = "absent"
	paramsNull    = "null"
	paramsPresent = "present"
)

// --- strict decode -----------------------------------------------------------------

// canonKind enumerates the JSON value kinds of the canonical tree.
type canonKind int

const (
	canonNull canonKind = iota
	canonBool
	canonNumber
	canonString
	canonArray
	canonObject
)

// canonValue is one strictly decoded JSON value. Object members keep decode order
// internally; encoding sorts them (the tree is the single source for both the
// forwarded bytes and the effect view).
type canonValue struct {
	kind canonKind
	b    bool
	num  json.Number
	str  string
	arr  []canonValue
	obj  []canonMember
}

type canonMember struct {
	key string
	val canonValue
}

// member returns a pointer to the named object member, or nil.
func (v *canonValue) member(key string) *canonMember {
	if v.kind != canonObject {
		return nil
	}
	for i := range v.obj {
		if v.obj[i].key == key {
			return &v.obj[i]
		}
	}
	return nil
}

// removeMember drops the named object member (no-op when absent).
func (v *canonValue) removeMember(key string) {
	if v.kind != canonObject {
		return
	}
	kept := v.obj[:0]
	for _, m := range v.obj {
		if m.key != key {
			kept = append(kept, m)
		}
	}
	v.obj = kept
}

// rejectLossyJSONStrings refuses the two string contents whose standard-library
// decoding is LOSSY, BEFORE any byte of `raw` reaches `json.Decoder` (round-9
// R9-02). It is what makes the canonical tree INJECTIVE: two accepted JSON texts
// that differ materially can no longer produce the same canonical bytes, and
// therefore can no longer produce the same digest.
//
// The defect it closes. `json.Decoder.Token` maps EVERY unpaired UTF-16 surrogate
// escape — and every invalid UTF-8 byte sequence — to the SAME replacement rune
// U+FFFD, and `json.Marshal` then re-encodes that one rune. Two materially
// different terminal reports, one carrying `"\ud800"` and one carrying `"\ud801"`,
// therefore had EQUAL canonical digests while the gateway relayed their distinct
// RAW bodies to the client. The digest is the identity the owner's collection proof
// is bound to (`TaskRecord.OwnerCollectedDigest`), so an owner served report A
// satisfied the retirement precondition for a different, never-delivered report B,
// and the operator retirement then deleted B unread. That is a deterministic
// canonicalization collision, not a SHA-256 collision — no hash choice could have
// prevented it.
//
// Why REJECTING is the correct direction rather than a lossless re-encoding of
// nonsense, from the primary sources (accessed 2026-07-25):
//
//   - the MCP transport binding states "MCP uses JSON-RPC to encode messages.
//     JSON-RPC messages MUST be UTF-8 encoded"
//     (modelcontextprotocol.io/specification/draft/basic/transports), and RFC 8259
//     §8.1 says JSON text exchanged outside a closed ecosystem "MUST be encoded
//     using UTF-8". Invalid UTF-8 is not a conforming body at all;
//   - RFC 8259 §8.2 admits unpaired surrogates in its ABNF and warns explicitly that
//     they arise from truncation bugs and that software behavior on them is
//     unpredictable — they encode no Unicode character;
//   - RFC 8259 §9 grants exactly this remedy to a parser: "An implementation may set
//     limits on the length and character contents of strings."
//
// Stated PRECISELY, because the two inputs are NOT the same case (round-10 N10-02;
// the earlier wording here could be read as “no conforming body is refused”, which is
// too broad). Invalid UTF-8 is not a conforming interchange encoding at all, so
// refusing it refuses nothing a conforming peer may send. An escaped lone surrogate
// is different: the JSON ABNF DOES admit it, so this gateway does refuse some
// **ABNF-conforming JSON texts**. The honest claim is narrower and still sufficient:
// it is an EXPRESSLY PERMITTED parser limit (§9) over a string content that encodes
// no Unicode character and whose cross-implementation behavior is explicitly
// unpredictable (§8.2), it is applied uniformly to every strict decode in this
// connector, and it restores the identity invariant the deletion proof depends on.
// Every interoperable Unicode-scalar string and every conforming escape form remains
// accepted.
//
// ROUND-11 N11-03: this used to call rejection "the ONLY way" to restore that
// invariant, which is an overclaim about the design space rather than a statement
// about this code. It is the way THIS implementation chose. A lossless representation
// — one that preserved each unpaired escape distinctly instead of folding it to
// U+FFFD — could keep the distinction and stay injective without refusing anything;
// it would mean replacing the standard-library decode on every strict path and
// versioning the canonical report format, which is work, not stage 4's. The RULE
// below is unchanged: reject before decoding.
//
// The refusal is also the deny-closed direction: the body mutates nothing, the
// record stays retained and visible to reconciliation, and the upstream's own bytes
// are still relayed to the client unchanged where the caller relays them.
//
// What it does NOT claim: two DIFFERENT spellings of the SAME Unicode string — `"A"`
// versus `"\u0041"`, `"/"` versus `"\/"`, a literal character versus its `\uXXXX`
// escape — still canonicalize identically. That is semantic equality, not a
// collision: the two texts denote one string, so they are the same report.
// Injectivity is over the ACCEPTED reports' MEANINGS, which is exactly what a
// collection proof must identify. (Round-10 N10-02: the example here used to render
// as `"A"` versus `"A"` — both sides the same literal — which demonstrated nothing.
// The regression that actually pins it is `task_round9_test.go`, `A` vs `\u0041`.)
//
// The scan is lexical and deliberately independent of the decoder: it tracks string
// literals, honors `\\`-escapes so a `\"` never closes a string, and reads `\uXXXX`
// code units positionally. A malformed text can confuse the scan only into refusing
// (deny-closed) or into passing it to the decoder, which then refuses it anyway.
func rejectLossyJSONStrings(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("mcp: params: JSON text is not valid UTF-8 (RFC 8259 §8.1; MCP requires UTF-8 encoded messages)")
	}
	inString := false
	for i := 0; i < len(raw); {
		c := raw[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			i++
			continue
		}
		switch c {
		case '"':
			inString = false
			i++
		case '\\':
			if i+1 >= len(raw) {
				return fmt.Errorf("mcp: params: truncated string escape")
			}
			if raw[i+1] != 'u' {
				i += 2 // \" \\ \/ \b \f \n \r \t — the decoder validates which are legal
				continue
			}
			unit, ok := jsonUnicodeEscapeAt(raw, i)
			if !ok {
				return fmt.Errorf(`mcp: params: malformed \u escape in a string`)
			}
			i += 6
			switch {
			case unit >= 0xDC00 && unit <= 0xDFFF:
				return fmt.Errorf(`mcp: params: unpaired low UTF-16 surrogate escape \u%04X `+
					`(RFC 8259 §8.2: encodes no character; refused so the canonical form stays injective)`, unit)
			case unit >= 0xD800 && unit <= 0xDBFF:
				low, lok := jsonUnicodeEscapeAt(raw, i)
				if !lok || low < 0xDC00 || low > 0xDFFF {
					return fmt.Errorf(`mcp: params: unpaired high UTF-16 surrogate escape \u%04X `+
						`(RFC 8259 §8.2: encodes no character; refused so the canonical form stays injective)`, unit)
				}
				i += 6
			}
		default:
			i++
		}
	}
	return nil
}

// jsonUnicodeEscapeAt reads the `\uXXXX` escape starting at raw[at] and returns its
// UTF-16 code unit. It reports false when the six bytes are not exactly that shape.
func jsonUnicodeEscapeAt(raw []byte, at int) (rune, bool) {
	if at+6 > len(raw) || raw[at] != '\\' || raw[at+1] != 'u' {
		return 0, false
	}
	var unit rune
	for _, b := range raw[at+2 : at+6] {
		switch {
		case b >= '0' && b <= '9':
			unit = unit*16 + rune(b-'0')
		case b >= 'a' && b <= 'f':
			unit = unit*16 + rune(b-'a') + 10
		case b >= 'A' && b <= 'F':
			unit = unit*16 + rune(b-'A') + 10
		default:
			return 0, false
		}
	}
	return unit, true
}

// decodeStrictJSON decodes exactly ONE JSON value with duplicate-object-key
// rejection at every depth and no trailing data. Numbers are preserved as their
// exact literals (json.Number).
//
// ROUND-9 R9-02: it additionally refuses the string contents that make the decode
// LOSSY, so the canonical tree is INJECTIVE over the bodies this gateway accepts —
// see rejectLossyJSONStrings. Nothing else in the pipeline can restore a
// distinction the decoder has already erased.
func decodeStrictJSON(raw []byte) (canonValue, error) {
	if err := rejectLossyJSONStrings(raw); err != nil {
		return canonValue{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := decodeCanonValue(dec)
	if err != nil {
		return canonValue{}, err
	}
	if dec.More() {
		return canonValue{}, fmt.Errorf("mcp: params: trailing JSON value")
	}
	if _, err := dec.Token(); err != io.EOF {
		return canonValue{}, fmt.Errorf("mcp: params: trailing data after JSON value")
	}
	return v, nil
}

func decodeCanonValue(dec *json.Decoder) (canonValue, error) {
	tok, err := dec.Token()
	if err != nil {
		return canonValue{}, fmt.Errorf("mcp: params: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return decodeCanonObject(dec)
		case '[':
			return decodeCanonArray(dec)
		default:
			return canonValue{}, fmt.Errorf("mcp: params: unexpected delimiter %q", t.String())
		}
	case string:
		return canonValue{kind: canonString, str: t}, nil
	case json.Number:
		return canonValue{kind: canonNumber, num: t}, nil
	case bool:
		return canonValue{kind: canonBool, b: t}, nil
	case nil:
		return canonValue{kind: canonNull}, nil
	default:
		return canonValue{}, fmt.Errorf("mcp: params: unexpected token %T", tok)
	}
}

func decodeCanonObject(dec *json.Decoder) (canonValue, error) {
	obj := canonValue{kind: canonObject}
	seen := map[string]struct{}{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return canonValue{}, fmt.Errorf("mcp: params: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return canonValue{}, fmt.Errorf("mcp: params: object key is not a string")
		}
		if _, dup := seen[key]; dup {
			// The strictness this file exists for: Go keeps the LAST duplicate, a
			// first-wins consumer keeps the other — refuse the ambiguity.
			return canonValue{}, fmt.Errorf("mcp: params: duplicate object key %q", key)
		}
		seen[key] = struct{}{}
		val, err := decodeCanonValue(dec)
		if err != nil {
			return canonValue{}, err
		}
		obj.obj = append(obj.obj, canonMember{key: key, val: val})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return canonValue{}, fmt.Errorf("mcp: params: %w", err)
	}
	return obj, nil
}

func decodeCanonArray(dec *json.Decoder) (canonValue, error) {
	arr := canonValue{kind: canonArray, arr: []canonValue{}}
	for dec.More() {
		val, err := decodeCanonValue(dec)
		if err != nil {
			return canonValue{}, err
		}
		arr.arr = append(arr.arr, val)
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return canonValue{}, fmt.Errorf("mcp: params: %w", err)
	}
	return arr, nil
}

// scanTopLevelExactMember reads the TOP-LEVEL object members of raw with a
// duplicate-TOLERANT token scan and returns every EXACT-cased string value of
// the named member, in order, PLUS the count of EVERY occurrence of the exact
// key whatever its value type. It exists for one job (round-4 findings 1
// and 3): reading the exact `resultType`/`status` DISCRIMINATOR when
// decodeStrictJSON has ALREADY refused the whole tree because of a duplicate key
// or lossy string in an UNRELATED member. It is deliberately resilient — a
// duplicate elsewhere is tolerated so the discriminator can still be read — and
// STRICT about the named member: every exact occurrence is COUNTED so the caller
// can detect a first-wins/last-wins discriminator differential. Case variants of
// `name` are NOT this member (MCP property names are case-sensitive) and are
// skipped.
//
// STAGE-7 M-1 (r1 contrast, 2026-07-30): the occurrence count is the second
// return and it counts NON-STRING and composite values too. The prior shape
// returned only the string values, so `{"resultType":"complete","resultType":
// null}` looked like a SINGLE clean discriminator to every caller — an object
// with a duplicated reserved key is ambiguous by construction (a first-wins and
// a last-wins reader disagree about the member's very type), no conforming
// server produces it, and hiding the second occurrence behind its value type
// was a hole, not tolerance.
//
// It classifies NOTHING and it never widens a relay decision on its own: the
// caller uses the returned values only to decide whether the exact governed
// contract was selected. The scan is best-effort — on any token error it returns
// what it has already collected, so a governed discriminator read before a
// truncation still counts (fail-safe), and bytes with no readable top-level
// object yield no values.
func scanTopLevelExactMember(raw []byte, name string) (values []string, occurrences int) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, 0
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, 0
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return values, occurrences
		}
		key, ok := keyTok.(string)
		if !ok {
			return values, occurrences
		}
		if key == name {
			occurrences++
		}
		valTok, err := dec.Token()
		if err != nil {
			return values, occurrences
		}
		if _, ok := valTok.(json.Delim); ok {
			// valTok opened a nested object/array; consume it to its matching close.
			if err := skipCompositeValue(dec); err != nil {
				return values, occurrences
			}
			continue
		}
		if key == name {
			if s, ok := valTok.(string); ok {
				values = append(values, s)
			}
		}
	}
	return values, occurrences
}

// skipCompositeValue consumes the remainder of the object or array whose opening
// delimiter was just read from dec by the caller, matching nested delimiters by
// depth (the caller's already-consumed opener is accounted for by the initial
// depth of 1).
func skipCompositeValue(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// --- canonical encode --------------------------------------------------------------

// encodeCanonical renders the deterministic canonical bytes of v: recursively
// sorted object keys, preserved number literals, encoding/json string escaping.
func encodeCanonical(v canonValue) []byte {
	var buf bytes.Buffer
	writeCanonical(&buf, v)
	return buf.Bytes()
}

func writeCanonical(buf *bytes.Buffer, v canonValue) {
	switch v.kind {
	case canonNull:
		buf.WriteString("null")
	case canonBool:
		if v.b {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case canonNumber:
		buf.WriteString(v.num.String()) // the exact received literal
	case canonString:
		writeCanonicalString(buf, v.str)
	case canonArray:
		buf.WriteByte('[')
		for i, el := range v.arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonical(buf, el)
		}
		buf.WriteByte(']')
	case canonObject:
		members := append([]canonMember(nil), v.obj...)
		sort.Slice(members, func(i, j int) bool { return members[i].key < members[j].key })
		buf.WriteByte('{')
		for i, m := range members {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonicalString(buf, m.key)
			buf.WriteByte(':')
			writeCanonical(buf, m.val)
		}
		buf.WriteByte('}')
	}
}

// writeCanonicalString writes encoding/json's deterministic escaping of s (the
// stdlib escaper is the single string encoder of the canonical form).
func writeCanonicalString(buf *bytes.Buffer, s string) {
	b, err := json.Marshal(s)
	if err != nil { // unreachable for a string; keep the form total
		b = []byte(`""`)
	}
	buf.Write(b)
}

// --- tools/call canonical params ----------------------------------------------------

// canonicalToolCallParams is the strict, canonicalized view of one tools/call
// params payload: the governed forward bytes, the effect view, the canonical
// arguments and the extracted operation key. Every field the gate acts on is
// extracted from the strict tree with EXACT casing — a case-variant alias of a
// reserved key is refused before this value is built (see rejectReservedKeyAliases).
type canonicalToolCallParams struct {
	// Presence distinguishes absent params, explicit null and a present object.
	Presence string
	// Name is the exact-cased value of the top-level "name" member ("" when absent
	// or not a string). The gate resolves the toolset from THIS, never from a
	// case-insensitive struct unmarshal (the case-fold smuggling vector).
	Name string
	// HasArguments reports whether an exact-cased "arguments" member was present.
	HasArguments bool
	// InputResponses is the exact-cased params.inputResponses object the MRTR
	// mediation PEP inspects, rendered from the STRICT tree (round-1 F-07:
	// the mediator must read the same logical member a case-folding upstream
	// consumes from the forwarded bytes).
	InputResponses map[string]json.RawMessage
	// DeclaresTasks is the EXACT per-request Tasks-extension capability fact
	// (design adjudication §2/§6): the pin makes client capabilities
	// per-request and forbids inferring them from prior requests
	// (schema.ts:63-98,92-98), and a server MUST NOT honor an extension the
	// request did not declare (-32021, schema.ts:503-505). This fact — read
	// exact-cased from the strict tree, never a case-folding re-parse — is what
	// SELECTS the task-handle response contract for this call's result; exact
	// `resultType:"task"` without it is only a custom core ResultType string
	// (open enum, schema.ts:208-216) and relays as extension data.
	DeclaresTasks bool
	// OperationKey is the client-supplied idempotency key ("" when none). Never
	// persisted raw; consumed only by the OperationID derivation.
	OperationKey string
	// Forward is the canonical params to forward upstream: the operation key is
	// STRIPPED, everything else (trace correlation included) preserved. nil when
	// params are absent.
	Forward json.RawMessage
	// Effect is the canonical effect view bound into the EffectDigest: Forward
	// minus the versioned trace-correlation members (_meta traceparent/tracestate/
	// baggage). nil when params are absent.
	Effect []byte
	// Args is the canonical encoding of params.arguments (nil when absent).
	Args []byte
}

// toolCallReservedKeys are the top-level tools/call params keys the gate reads
// by exact name. encoding/json field-matching is case-INSENSITIVE, so a request
// that smuggles a case-variant alias ("Name" beside "name", "Arguments" beside
// "arguments", "_Meta" beside "_meta") could authorize one value while a
// case-insensitive upstream consumes the canonical bytes and executes another.
// The strict decoder already rejects EXACT duplicate keys; the alias check
// additionally forbids case-variant aliases so the authorized view and the
// forwarded bytes can never disagree (review round-1 P0). Each governed
// method family supplies its own reserved-key PROFILE (stage 4: the task
// methods reserve taskId/_meta/inputResponses — see taskReservedKeys).
//
// "inputResponses" is reserved here for the SAME reason (round-1 F-07):
// the MRTR mediator inspects it before the call is authorized, so a case-variant
// alias would let it approve one member while a case-folding upstream consumes
// another out of the very bytes forwarded.
var toolCallReservedKeys = []string{"name", "arguments", "_meta", "inputResponses"}

// keyFoldsTo is THE key-equivalence predicate of this connector: `strings.EqualFold`
// applies Unicode SIMPLE FOLDING, so it treats U+017F (ʼlong sʼ) as equivalent to
// ASCII `s`, U+212A (Kelvin sign) as equivalent to `k`, and so on.
//
// Round-3 R3-06: every consumer that classifies a member by name MUST use this
// one function. The reserved-alias rejection already used EqualFold while the
// task-marker classifiers lowercased the key and compared it to an ASCII literal
// — and `strings.ToLower("ſ")` is still "ſ". A member spelled `reſultType`
// was therefore an ALIAS to the rejector and NOT A MARKER to the classifier, and
// the classifier is what decides whether the alias error is fatal: the alias
// error was discarded and a live, unregistered durable task was relayed. Two
// predicates over the same namespace is the differential; there is now one.
func keyFoldsTo(key, reserved string) bool {
	return strings.EqualFold(key, reserved)
}

// rejectReservedKeyAliases returns an error if obj carries a top-level member
// that case-folds to one of the profile's reserved keys but is not the
// exact-cased key itself, or if _meta carries a case-variant alias of the
// operation-key member.
func rejectReservedKeyAliases(obj canonValue, reserved []string) error {
	for i := range obj.obj {
		key := obj.obj[i].key
		for _, res := range reserved {
			if key != res && keyFoldsTo(key, res) {
				return fmt.Errorf("mcp: params: case-variant alias %q of reserved key %q (ambiguous — refused)", key, res)
			}
		}
	}
	if meta := obj.member("_meta"); meta != nil && meta.val.kind == canonObject {
		for i := range meta.val.obj {
			key := meta.val.obj[i].key
			if key != mcpOperationKeyMeta && keyFoldsTo(key, mcpOperationKeyMeta) {
				return fmt.Errorf("mcp: params: _meta case-variant alias %q of the operation key (refused)", key)
			}
		}
	}
	return nil
}

// canonicalizeToolCallParams strictly decodes and canonicalizes tools/call params.
// Any error is a PROTOCOL refusal (invalid params) — it happens BEFORE the claim
// and before any forward.
func canonicalizeToolCallParams(raw json.RawMessage) (canonicalToolCallParams, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return canonicalToolCallParams{Presence: paramsAbsent}, nil
	}
	if trimmed == "null" {
		return canonicalToolCallParams{
			Presence: paramsNull,
			Forward:  json.RawMessage("null"),
			Effect:   []byte("null"),
		}, nil
	}
	v, err := decodeStrictJSON(raw)
	if err != nil {
		return canonicalToolCallParams{}, err
	}
	if v.kind != canonObject {
		return canonicalToolCallParams{}, fmt.Errorf("mcp: tools/call params must be a JSON object")
	}
	// Refuse case-variant aliases of the reserved keys BEFORE extracting anything —
	// the gate's authorized view and the forwarded bytes must agree exactly.
	if err := rejectReservedKeyAliases(v, toolCallReservedKeys); err != nil {
		return canonicalToolCallParams{}, err
	}

	out := canonicalToolCallParams{Presence: paramsPresent}
	// The tool name the gate authorizes is the EXACT-cased "name" member — never a
	// case-insensitive struct unmarshal.
	if nm := v.member("name"); nm != nil && nm.val.kind == canonString {
		out.Name = nm.val.str
	}
	// Extract + strip the operation-key extension (the upstream never sees it);
	// shared with the task-method canonicalization (evidencetask.go).
	opKey, kerr := extractOperationKey(&v)
	if kerr != nil {
		return canonicalToolCallParams{}, kerr
	}
	// The mediated MRTR member is read from the SAME strict tree with exact
	// casing (round-1 F-07) — never re-parsed out of the forwarded bytes.
	if members, ok := strictObjectMembers(&v, "inputResponses"); ok {
		out.InputResponses = members
	}
	out.DeclaresTasks = declaresTasksExtension(&v)
	out.OperationKey = opKey
	out.Forward = encodeCanonical(v)
	if args := v.member("arguments"); args != nil {
		out.HasArguments = true
		out.Args = encodeCanonical(args.val)
	}

	// Effect view: exclude the versioned trace-correlation members — a retry with a
	// fresh trace id is the SAME effect. (Only the standard W3C members are
	// excluded; everything else in _meta binds.) Forward and Args are already
	// rendered to bytes above, so mutating the tree here is safe.
	stripTraceMembers(&v)
	out.Effect = encodeCanonical(v)
	return out, nil
}

// declaresTasksExtension reads the per-request Tasks-extension declaration from
// the strict tools/call params tree with EXACT casing at every level:
// _meta["io.modelcontextprotocol/clientCapabilities"].extensions
// ["io.modelcontextprotocol/tasks"] (schema.ts:92-98; ClientCapabilities.
// extensions, schema.ts:776-786 — declaration values are settings JSONObjects,
// "an empty object indicates support with no settings").
//
// The reading is deliberately EXACT and conservative: extension identifiers
// must follow the `_meta` key naming rules (reverse-DNS, mandatory prefix), so
// the exact spelling is the conforming spelling, and a member that merely
// case-folds to it is untouched extension data — the adjudicated rule that
// incidental body/params shape never selects a contract. Declared residual: a
// non-conforming case-folding upstream could honor a case-variant declaration
// this gateway does not read; the task such an out-of-contract upstream creates
// relays as extension data and is never registered.
func declaresTasksExtension(v *canonValue) bool {
	meta := v.member("_meta")
	if meta == nil || meta.val.kind != canonObject {
		return false
	}
	caps := meta.val.member(metaClientCapabilities)
	if caps == nil || caps.val.kind != canonObject {
		return false
	}
	ext := caps.val.member("extensions")
	if ext == nil || ext.val.kind != canonObject {
		return false
	}
	decl := ext.val.member(extensionTasks)
	return decl != nil && decl.val.kind == canonObject
}

// --- derivations -------------------------------------------------------------------

// evidenceLPDigest is the injective length-prefixed digest of the MCP
// derivations: SHA-256 over uint64-big-endian(len(part)) || part for every part, in
// order — never delimiter joins, never NUL separators, and (unlike hashPlanParts)
// NO trimming: canonical bytes bind verbatim.
func evidenceLPDigest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		writeLenPrefixedHash(h, []byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeLenPrefixedHash(h hash.Hash, b []byte) {
	var lb [8]byte
	n := uint64(len(b))
	for i := 0; i < 8; i++ {
		lb[i] = byte(n >> (8 * (7 - uint(i))))
	}
	_, _ = h.Write(lb[:])
	_, _ = h.Write(b)
}

// deriveToolCallOperationID derives the single-use OperationID. With a client
// operation key it is deterministic within the SUPPLIED-KEY NAMESPACE — tenant,
// this resource server, token issuer, OAuth client, subject and act-as — so one
// client's key can never collide into another principal's operation (the design's
// shared-namespace defense; an empty ClientID on a legacy token degrades that
// isolation to issuer+subject, an accepted residual). Method and params are
// DELIBERATELY excluded: reusing one key for a different effect keeps the same
// OperationID and produces a different EffectDigest — the rebind refusal.
//
// Without a key the gate mints a cryptographically random server OperationID
// labeled request_instance: evidence enforcement holds, transport-retry dedup is
// NOT claimed.
func deriveToolCallOperationID(tenant, resource string, tok validatedToken, operationKey string) (id, kind string, err error) {
	if operationKey == "" {
		var buf [32]byte
		if _, rerr := rand.Read(buf[:]); rerr != nil {
			return "", "", fmt.Errorf("mcp: mint operation id: %w", rerr)
		}
		return hex.EncodeToString(buf[:]), opIDKindRequestInstance, nil
	}
	return evidenceLPDigest(
		mcpOperationDomainV1,
		tenant,
		mcpOperationSurface,
		resource,
		tok.Issuer,
		tok.ClientID,
		tok.Subject,
		tok.ActAs,
		operationKey,
	), opIDKindKeyed, nil
}

// pinBinding is the pin posture bound into the policy digest (round-2):
// State ∈ {unwired, verified, attested}; Fingerprint/Version carry the APPROVED
// pin identity ONLY when the verifier exposes the atomic ToolPinVerifyAttestor
// capability (decision+attestation under one snapshot round-3), and are
// EMPTY as the explicit stable absent marker otherwise — honest absence, never a
// separate re-read that could bind an identity the decision never authorized.
type pinBinding struct {
	State       string
	Fingerprint string
	Version     string
}

// coazBinding is the COAZ posture bound into the policy digest: State ∈
// {unwired, allow}; DecisionRef/PolicyVersion are the evaluator's stable
// references when provided, EMPTY as the explicit absent marker otherwise. The
// unstable Reason TEXT is deliberately never an input (cosmetic edits must not
// cause false rebinds).
type coazBinding struct {
	State         string
	DecisionRef   string
	PolicyVersion string
}

// toolCallPolicyDigest binds the STABLE policy posture of one tools/call into the
// EffectDigest: the server-owned policy entry (scope, flags, roles, annotations),
// the pin posture + approved-pin identity, and the COAZ posture + stable
// decision references. Only STABLE fields bind (review round-1/round-2): never
// the call-time pin fingerprint (a hash of params the digest already binds) and
// never reason text. Absent stable references bind as explicit empty parts — the
// preimage structure is FIXED, so absence itself is a stable identity.
func toolCallPolicyDigest(policy ToolPolicy, pin pinBinding, coaz coazBinding) string {
	parts := []string{
		mcpPolicyDomainV1,
		policy.RequiredScope,
		"destructive:" + boolMark(policy.Destructive),
		"deny:" + boolMark(policy.Deny),
		"app_only:" + boolMark(policy.AppOnly),
	}
	roles := append([]string(nil), policy.AllowedRoles...)
	sort.Strings(roles)
	parts = append(parts, fmt.Sprintf("roles:%d", len(roles)))
	parts = append(parts, roles...)
	if policy.Annotations == nil {
		parts = append(parts, "annotations:absent")
	} else {
		// The SERVER-OWNED annotations (never the tool's untrusted self-declared
		// ones); encoding/json struct marshaling has a deterministic field order.
		raw, err := json.Marshal(policy.Annotations)
		if err != nil {
			raw = []byte("annotations:unencodable")
		}
		parts = append(parts, "annotations:present", string(raw))
	}
	parts = append(parts,
		"pin:"+pin.State, pin.Fingerprint, pin.Version,
		"coaz:"+coaz.State, coaz.DecisionRef, coaz.PolicyVersion,
	)
	return evidenceLPDigest(parts...)
}

func boolMark(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// sortedScopeSet returns the deterministically sorted, trimmed granted scopes of a
// token — the normalized granted-scope view bound into the EffectDigest (review
// round-1 P1: the authorization context of the effect, not merely the required
// scope). Length-prefixed count guards against boundary collisions.
func sortedScopeSet(scopes map[string]struct{}) []string {
	out := make([]string, 0, len(scopes))
	for s := range scopes {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// deriveToolCallEffectDigest binds the FULL effective request (design §3): profile +
// tenant + resource + method + caller identity + normalized granted scopes + the
// upstream target descriptor + target + params presence + the
// canonical-effect-params hash + policy digest + approval binding.
// upstreamDescriptor is the STABLE identity of the configured upstream backend +
// credential profile (round-2: a re-pointed backend is a DIFFERENT effect — a
// keyed retry against it must rebind, not replay); empty is the explicit absent
// marker for deployments without a wired upstream descriptor. OperationID,
// JSON-RPC id, timestamps and trace context are deliberately excluded.
func deriveToolCallEffectDigest(tenant, resource, method string, tok validatedToken,
	targetKind, targetRef, upstreamDescriptor string, grantedScopes []string,
	canon canonicalToolCallParams, policyDigest, approvalRef, approvedPlanHash string) string {
	paramsSum := sha256.Sum256(canon.Effect)
	parts := []string{
		mcpEffectDomainV1,
		mcpEffectProfileV1,
		tenant,
		resource,
		method,
		tok.Issuer,
		tok.ClientID,
		tok.Subject,
		tok.ActAs,
		upstreamDescriptor, // stable upstream-target/credential-profile descriptor
		targetKind,
		targetRef,
		fmt.Sprintf("granted_scopes:%d", len(grantedScopes)),
	}
	parts = append(parts, grantedScopes...)
	parts = append(parts,
		canon.Presence,
		hex.EncodeToString(paramsSum[:]),
		policyDigest,
		approvalRef,
		approvedPlanHash,
	)
	return evidenceLPDigest(parts...)
}

// resultDigest is the opaque SHA-256 of the relayed result bytes ("" for none).
func resultDigest(result json.RawMessage) string {
	if len(result) == 0 {
		return ""
	}
	sum := sha256.Sum256(result)
	return hex.EncodeToString(sum[:])
}
