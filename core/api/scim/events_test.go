// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestActionForEvent(t *testing.T) {
	cases := map[string]EventAction{
		EventProvDelete:       ActionDeprovision,
		EventProvDeactivate:   ActionDisable,
		EventProvActivate:     ActionActivate,
		EventProvCreateNotice: ActionIgnore,
		EventProvPatchFull:    ActionIgnore,
		EventFeedAdd:          ActionIgnore,
		EventMiscAsyncResp:    ActionIgnore,
		"urn:example:unknown": ActionIgnore,
	}
	for uri, want := range cases {
		if got := ActionForEvent(uri); got != want {
			t.Errorf("ActionForEvent(%q) = %q, want %q", uri, got, want)
		}
	}
}

func TestSubjectResourcePath(t *testing.T) {
	s := SubjectID{Format: "scim", URI: "Users/2819c223-7f76-453a-919d-413861904646"}
	rt, id := s.ResourcePath()
	if rt != "Users" || id != "2819c223-7f76-453a-919d-413861904646" {
		t.Errorf("ResourcePath = (%q,%q)", rt, id)
	}
	if _, id := (SubjectID{URI: "bare-id"}).ResourcePath(); id != "bare-id" {
		t.Errorf("bare id = %q", id)
	}
	if rt, id := (SubjectID{}).ResourcePath(); rt != "" || id != "" {
		t.Errorf("empty = (%q,%q)", rt, id)
	}
}

func TestParseCompactJWSAndDecodeSET(t *testing.T) {
	b64 := base64.RawURLEncoding
	hdr, _ := json.Marshal(JWSHeader{Alg: "ES256", Kid: "k1", Typ: "secevent+jwt"})
	// aud as a single string must still decode into the Audience slice.
	payload := []byte(`{"iss":"i","aud":"one-aud","iat":1700000000,"jti":"j","sub_id":{"format":"scim","uri":"Users/x"},"events":{"` + EventProvDelete + `":{}}}`)
	sig := []byte("sig")
	token := []byte(b64.EncodeToString(hdr) + "." + b64.EncodeToString(payload) + "." + b64.EncodeToString(sig))

	gotHdr, gotPayload, signingInput, gotSig, err := ParseCompactJWS(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gotHdr.Alg != "ES256" || gotHdr.Kid != "k1" {
		t.Errorf("header = %+v", gotHdr)
	}
	if string(gotSig) != "sig" {
		t.Errorf("sig = %q", gotSig)
	}
	if string(signingInput) != b64.EncodeToString(hdr)+"."+b64.EncodeToString(payload) {
		t.Errorf("signing input mismatch")
	}
	set, err := DecodeSET(gotPayload)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(set.Audience) != 1 || set.Audience[0] != "one-aud" {
		t.Errorf("aud = %v, want [one-aud] (string-or-array)", set.Audience)
	}
	if uris := set.EventURIs(); len(uris) != 1 || uris[0] != EventProvDelete {
		t.Errorf("event URIs = %v", uris)
	}
}

func TestParseCompactJWSMalformed(t *testing.T) {
	for _, bad := range []string{"", "onlyonepart", "a.b", "a.b.c.d", ".b.c", "!!!.b.c"} {
		if _, _, _, _, err := ParseCompactJWS([]byte(bad)); err == nil {
			t.Errorf("ParseCompactJWS(%q) = nil error, want failure", bad)
		}
	}
}
