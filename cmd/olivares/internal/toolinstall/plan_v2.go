// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// planV2DigestInput is the selection the v2 digest binds. Field order is the
// struct order, so encoding/json produces one canonical byte string. Digest
// itself is not an input.
type planV2DigestInput struct {
	Schema    string      `json:"schema"`
	Selection SelectionV2 `json:"selection"`
}

// ComputeDigestV2 returns the SHA-256 of the v2 plan's canonical selection.
func ComputeDigestV2(p *PlanV2) string {
	in := planV2DigestInput{Schema: p.Schema, Selection: p.Selection}
	b, err := json.Marshal(in)
	if err != nil {
		panic("toolinstall: plan v2 digest: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// MarshalPlanV2 renders the v2 approval document: selection plus digest, with
// every field present. Observation names are not on the type, so they cannot
// appear even as empty values.
func MarshalPlanV2(p *PlanV2) ([]byte, error) {
	if p == nil {
		return nil, refuse(KindInvalidRequest, "v2 plan is missing")
	}
	out := PlanV2{Schema: PlanSchemaV2, Selection: p.Selection}
	if err := out.validate(); err != nil {
		return nil, err
	}
	out.Digest = ComputeDigestV2(&out)
	return json.MarshalIndent(&out, "", "  ")
}

// ReadPlanV2 parses a v2 approval file strictly: bounded, no observation fields
// under any decoder-equivalent spelling, no unknown fields, no trailing values,
// no duplicate keys, schema matched, selection canonical, and the recorded
// digest equal to the digest of its own selection.
func ReadPlanV2(r io.Reader) (*PlanV2, error) {
	data, err := readBoundedPlanFile(r)
	if err != nil {
		return nil, err
	}
	return parsePlanV2(data)
}

func parsePlanV2(data []byte) (*PlanV2, error) {
	if err := inspectPlanJSON(data); err != nil {
		return nil, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
	}
	if k := observationKeyIn(keys); k != "" {
		return nil, refuse(KindInvalidRequest, "plan file carries the observation field %q, which an approval never contains; regenerate it with plan --out instead of editing it", k)
	}
	if err := assertPlanV2Keys(keys); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p PlanV2
	if err := dec.Decode(&p); err != nil {
		return nil, refuse(KindInvalidRequest, "plan file is not a %s document: %v", PlanSchemaV2, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, refuse(KindInvalidRequest, "plan file has trailing JSON after the document")
	}
	if p.Schema != PlanSchemaV2 {
		return nil, refuse(KindInvalidRequest, "plan file declares schema %q, want %q", p.Schema, PlanSchemaV2)
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	if got := ComputeDigestV2(&p); got != p.Digest {
		return nil, refuse(KindInvalidRequest, "plan file digest %s does not match its content (%s); the file was edited after it was written", short(p.Digest), short(got))
	}
	return &p, nil
}

func readBoundedPlanFile(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPlanFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	if len(data) > maxPlanFileBytes {
		return nil, refuse(KindInvalidRequest, "plan file is larger than %d bytes", maxPlanFileBytes)
	}
	return data, nil
}

func inspectPlanJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return refuse(KindInvalidRequest, "plan file is not a JSON object")
	}
	if err := walkJSONObjectRejectDup(dec, 1); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return refuse(KindInvalidRequest, "plan file has trailing JSON after the document")
	}
	return nil
}

const maxJSONDepth = 32

func walkJSONObjectRejectDup(dec *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return refuse(KindInvalidRequest, "plan file JSON nesting exceeds %d", maxJSONDepth)
	}
	seen := make([]string, 0, 8)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return refuse(KindInvalidRequest, "plan file object key is not a string")
		}
		if depth == 1 {
			if canonical, ok := decoderEquivalentObservationKey(key); ok {
				return refuse(KindInvalidRequest, "plan file carries the observation field %q, which an approval never contains; regenerate it with plan --out instead of editing it", canonical)
			}
		}
		kb := []byte(key)
		for _, prev := range seen {
			if bytes.EqualFold(kb, []byte(prev)) {
				return refuse(KindInvalidRequest, "plan file has duplicate key %q", key)
			}
		}
		seen = append(seen, key)
		if err := walkJSONValueRejectDup(dec, depth); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return refuse(KindInvalidRequest, "plan file is not a JSON object: %v", err)
	}
	if delim, ok := end.(json.Delim); !ok || delim != '}' {
		return refuse(KindInvalidRequest, "plan file is not a JSON object")
	}
	return nil
}

func walkJSONArrayRejectDup(dec *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return refuse(KindInvalidRequest, "plan file JSON nesting exceeds %d", maxJSONDepth)
	}
	for dec.More() {
		if err := walkJSONValueRejectDup(dec, depth); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return refuse(KindInvalidRequest, "plan file is not a JSON array: %v", err)
	}
	if delim, ok := end.(json.Delim); !ok || delim != ']' {
		return refuse(KindInvalidRequest, "plan file is not a JSON array")
	}
	return nil
}

func walkJSONValueRejectDup(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return refuse(KindInvalidRequest, "plan file is not JSON: %v", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return walkJSONObjectRejectDup(dec, depth+1)
	case '[':
		return walkJSONArrayRejectDup(dec, depth+1)
	default:
		return refuse(KindInvalidRequest, "plan file has unexpected JSON delimiter %q", delim.String())
	}
}

func foldRaw(keys map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if v, ok := keys[name]; ok {
		return v, true
	}
	nb := []byte(name)
	for k, v := range keys {
		if bytes.EqualFold([]byte(k), nb) {
			return v, true
		}
	}
	return nil, false
}

func requireKeys(keys map[string]json.RawMessage, names ...string) error {
	for _, n := range names {
		if _, ok := foldRaw(keys, n); !ok {
			return refuse(KindInvalidRequest, "plan file omits required field %q", n)
		}
	}
	return nil
}

func jsonNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func rejectJSONNull(keys map[string]json.RawMessage, names ...string) error {
	for _, n := range names {
		raw, ok := foldRaw(keys, n)
		if !ok {
			continue
		}
		if jsonNull(raw) {
			return refuse(KindInvalidRequest, "plan file field %q must not be null", n)
		}
	}
	return nil
}

// requireNonNullKeys requires each named field and rejects JSON null before
// encoding/json can coerce a scalar to its Go zero value. Decoder-equivalent
// spellings are resolved with the same fold as requireKeys.
func requireNonNullKeys(keys map[string]json.RawMessage, names ...string) error {
	if err := requireKeys(keys, names...); err != nil {
		return err
	}
	return rejectJSONNull(keys, names...)
}

func objectKeys(raw json.RawMessage, name string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, refuse(KindInvalidRequest, "%s must be a JSON object", name)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, refuse(KindInvalidRequest, "%s is not a JSON object: %v", name, err)
	}
	return keys, nil
}

func arrayElems(raw json.RawMessage, name string) ([]json.RawMessage, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, refuse(KindInvalidRequest, "%s must be a JSON array, not null", name)
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, refuse(KindInvalidRequest, "%s is not a JSON array: %v", name, err)
	}
	if elems == nil {
		elems = []json.RawMessage{}
	}
	return elems, nil
}

func assertPlanV2Keys(top map[string]json.RawMessage) error {
	if err := requireNonNullKeys(top, "schema", "selection", "digest"); err != nil {
		return err
	}
	sel, err := objectKeys(mustFold(top, "selection"), "selection")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(sel,
		"driver", "channel", "requested_version", "version", "platform",
		"vendor_platform", "source", "fetched_object", "layout",
		"required_subjects", "package_policy_id", "verification", "destination",
	); err != nil {
		return err
	}
	plat, err := objectKeys(mustFold(sel, "platform"), "platform")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(plat, "os", "arch", "libc"); err != nil {
		return err
	}
	src, err := objectKeys(mustFold(sel, "source"), "source")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(src, "kind", "pointer", "checksums", "package", "proofs", "allowed_origins", "redirects"); err != nil {
		return err
	}
	for _, u := range []string{"pointer", "checksums", "package"} {
		ukeys, err := objectKeys(mustFold(src, u), "source."+u)
		if err != nil {
			return err
		}
		if err := requireNonNullKeys(ukeys, "state", "url"); err != nil {
			return err
		}
	}
	proofs, err := arrayElems(mustFold(src, "proofs"), "source.proofs")
	if err != nil {
		return err
	}
	for i, raw := range proofs {
		pkeys, err := objectKeys(raw, fmt.Sprintf("source.proofs[%d]", i))
		if err != nil {
			return err
		}
		if err := requireNonNullKeys(pkeys, "role", "url"); err != nil {
			return err
		}
	}
	if _, err := arrayElems(mustFold(src, "allowed_origins"), "source.allowed_origins"); err != nil {
		return err
	}
	redir, err := objectKeys(mustFold(src, "redirects"), "source.redirects")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(redir, "max_hops", "allowed_origins"); err != nil {
		return err
	}
	if _, err := arrayElems(mustFold(redir, "allowed_origins"), "source.redirects.allowed_origins"); err != nil {
		return err
	}
	fo, err := objectKeys(mustFold(sel, "fetched_object"), "fetched_object")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(fo, "digest_state", "sha256", "size_state", "size", "max_size"); err != nil {
		return err
	}
	layout, err := objectKeys(mustFold(sel, "layout"), "layout")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(layout, "id", "variant", "entry_point", "resources_dir", "path_dir", "members", "limits"); err != nil {
		return err
	}
	members, err := arrayElems(mustFold(layout, "members"), "layout.members")
	if err != nil {
		return err
	}
	for i, raw := range members {
		mkeys, err := objectKeys(raw, fmt.Sprintf("layout.members[%d]", i))
		if err != nil {
			return err
		}
		if err := requireNonNullKeys(mkeys, "path", "kind", "role", "final_mode"); err != nil {
			return err
		}
	}
	limits, err := objectKeys(mustFold(layout, "limits"), "layout.limits")
	if err != nil {
		return err
	}
	if err := requireNonNullKeys(limits, "max_compressed_bytes", "max_expanded_bytes", "max_members", "max_member_bytes"); err != nil {
		return err
	}
	subs, err := arrayElems(mustFold(sel, "required_subjects"), "required_subjects")
	if err != nil {
		return err
	}
	for i, raw := range subs {
		skeys, err := objectKeys(raw, fmt.Sprintf("required_subjects[%d]", i))
		if err != nil {
			return err
		}
		if err := requireNonNullKeys(skeys, "path", "expected_sha256", "identity", "issuer", "proof_role"); err != nil {
			return err
		}
	}
	ver, err := objectKeys(mustFold(sel, "verification"), "verification")
	if err != nil {
		return err
	}
	if err := requireKeys(ver, "kind", "cosign"); err != nil {
		return err
	}
	if err := rejectJSONNull(ver, "kind"); err != nil {
		return err
	}
	cosignRaw, _ := foldRaw(ver, "cosign")
	if !jsonNull(cosignRaw) {
		ckeys, err := objectKeys(cosignRaw, "verification.cosign")
		if err != nil {
			return err
		}
		if err := requireNonNullKeys(ckeys,
			"id", "version", "executable_sha256", "trusted_root_iteration",
			"trusted_root_sha256", "mode", "refresh_policy", "egress_policy",
			"bundle_format", "require_signature", "require_rekor", "require_sct",
		); err != nil {
			return err
		}
	}
	dest, err := objectKeys(mustFold(sel, "destination"), "destination")
	if err != nil {
		return err
	}
	return requireNonNullKeys(dest, "root", "release_dir", "executable")
}

func mustFold(keys map[string]json.RawMessage, name string) json.RawMessage {
	v, _ := foldRaw(keys, name)
	return v
}
