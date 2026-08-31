// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// These white-box tests reach the unexported requestFingerprint directly, because
// the fingerprint is domain-internal and two DISTINCT handles always differ by
// their JTI through the public API — the only way to prove that SPECIFIC subject
// fields and the capability vector are BOUND is to hold everything else constant.

func fpTestHandle() model.DelegationHandle {
	return model.DelegationHandle{
		BaseFields:     model.BaseFields{ID: model.ID("jti-fixed")},
		SourceCredKind: "user",
		SourceCredID:   model.ID("cred-1"),
		SubjectUserID:  model.ID("sub-1"),
		ActAsUserID:    model.ID(""),
		AgentRef:       "",
	}
}

func fpTestPresented(caps map[string]bool) PresentedRequest {
	return PresentedRequest{
		Nonce:                "nonce",
		OperationKind:        "messages",
		Model:                "m",
		ContentDigest:        strings.Repeat("a", 64),
		ContentSize:          1,
		MediaType:            "application/json",
		IssuedAt:             time.Unix(0, 0).UTC(),
		DeclaredCapabilities: caps,
	}
}

// TestRequestFingerprintBindsFullSubject pins M1: SourceCredID, ActAsUserID and
// AgentRef each change the fingerprint, so a retry is bound to the exact normative
// subject rather than just SourceCredKind + SubjectUserID.
func TestRequestFingerprintBindsFullSubject(t *testing.T) {
	pep := PEPIdentity{serviceID: model.ID("svc"), tenant: model.TenantID("t")}
	base := fpTestHandle()
	pr := fpTestPresented(map[string]bool{"buffer_request": true})
	f0 := requestFingerprint(base, pep, pep.tenant, "aud", pr)

	cases := []struct {
		name   string
		mutate func(*model.DelegationHandle)
	}{
		{"source cred id", func(h *model.DelegationHandle) { h.SourceCredID = model.ID("cred-2") }},
		{"act-as user id", func(h *model.DelegationHandle) { h.ActAsUserID = model.ID("act-2") }},
		{"agent ref", func(h *model.DelegationHandle) { h.AgentRef = "agent-2" }},
	}
	for _, tc := range cases {
		h := fpTestHandle()
		tc.mutate(&h)
		if requestFingerprint(h, pep, pep.tenant, "aud", pr) == f0 {
			t.Errorf("%s is not bound into the request fingerprint", tc.name)
		}
	}
}

// TestRequestFingerprintCapabilityEncoding pins H3(b): the capability encoding is
// injective over the closed vocabulary (distinct vectors differ) and IGNORES keys
// outside the vocabulary (an unknown key never perturbs the fingerprint).
func TestRequestFingerprintCapabilityEncoding(t *testing.T) {
	pep := PEPIdentity{serviceID: model.ID("svc"), tenant: model.TenantID("t")}
	h := fpTestHandle()
	fp := func(caps map[string]bool) string {
		return requestFingerprint(h, pep, pep.tenant, "aud", fpTestPresented(caps))
	}

	// Unknown declared keys — including one crafted to collide under a naive
	// delimiter-joined encoding — must NOT change the known-subset fingerprint.
	known := fp(map[string]bool{"buffer_request": true})
	if known != fp(map[string]bool{"buffer_request": true, "x": true, "a=1,b": true}) {
		t.Error("unknown capability keys must not affect the fingerprint")
	}

	// Distinct vocabulary vectors must differ.
	if fp(map[string]bool{"buffer_request": true}) == fp(map[string]bool{"streaming": true}) {
		t.Error("distinct vocabulary capability vectors must produce distinct fingerprints")
	}
	if fp(map[string]bool{"buffer_request": true}) == fp(map[string]bool{"buffer_request": true, "streaming": true}) {
		t.Error("adding a registered vocabulary capability must change the fingerprint")
	}
}
