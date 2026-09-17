// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessioncockpit

import (
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// The placeholder's own battery.
//
// ⛔ IT EXISTS BECAUSE `[no test files]` READS LIKE A PASS. `go test` prints it in the
// same column as `ok`, and this package is the ONE piece of the cockpit that ships in
// the artifact everybody downloads — the shape of its refusal is the whole product
// experience of "this build does not include it".

// recordingRegistrar captures what APIRoutes mounts, so the surface can be asserted
// without standing up an engine.
type recordingRegistrar struct {
	methods  []string
	patterns []string
	perms    []auth.Permission
	entities int
}

func (r *recordingRegistrar) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	r.methods = append(r.methods, method)
	r.patterns = append(r.patterns, pattern)
	r.perms = append(r.perms, perm)
}

func (r *recordingRegistrar) HandleEntity(string, string, auth.Permission, api.EntityRef, api.ModuleHandler) {
	r.entities++
}

// TestPlaceholderRegistersNoRouteAtAll pins the retirement of `GET /availability`, so
// that adding a route back here is a RED test and not a quiet edit.
//
// The route is not missing by oversight. `lint:public-counts` demands that the set of
// module-registered routes EQUAL the operations in `web/openapi/openapi.beta.json`, and
// it grants no waiver — `scripts/openapi-op-descriptions/routes.go` reports the mismatch
// "instead of skipped". That public document is exactly where a cockpit route must NOT
// appear: it would put product names in the public tree, which is what the edition-cut
// gate exists to prevent. And the v4 closes the cut by ABSENCE anyway — no add-on, no
// handler, 404 from the router — so a 501 from an AGPL placeholder has no future.
func TestPlaceholderRegistersNoRouteAtAll(t *testing.T) {
	reg := &recordingRegistrar{}
	NewPlaceholder().APIRoutes(reg)

	if len(reg.patterns) != 0 {
		t.Fatalf("the placeholder must register NO route, got %d: %v\n"+
			"A route here must also appear in web/openapi/openapi.beta.json, which is the "+
			"public document; this lane may not touch web/ and a cockpit operation does not "+
			"belong in the public surface.", len(reg.patterns), reg.patterns)
	}
	if reg.entities != 0 {
		t.Errorf("the placeholder must register no entity route, got %d", reg.entities)
	}
}

func TestPlaceholderDeclaresOnlyTheAvailabilityPermission(t *testing.T) {
	perms := NewPlaceholder().Permissions()
	if len(perms) != 1 || perms[0] != PermAvailabilityRead {
		t.Fatalf("permissions = %v, want exactly [%s]", perms, PermAvailabilityRead)
	}
	// ⛔ AND NO OPERATIONAL PERMISSION, which is the half that matters: a build without
	// the add-on must not advertise authority it cannot honour. A role that could be
	// granted session-cockpit:input:write on a community binary would be a promise the
	// artifact cannot keep.
	for _, p := range perms {
		for _, forbidden := range []string{":input:", ":shell:", ":stop:", ":adopt:", ":node:"} {
			if contains(string(p), forbidden) {
				t.Errorf("the placeholder declares an operational permission %q", p)
			}
		}
	}
}

// TestConstantsAreTheAllowlistedLiterals ties this package to
// scripts/community-session-cockpit-strings.allow. The allowlist enumerates literals of
// the shipped binary; if a constant here is edited and the allowlist is not, the gate
// goes red at push time — this test says so HERE, where the edit happens, so the reason
// is in front of whoever makes it.
func TestConstantsAreTheAllowlistedLiterals(t *testing.T) {
	// The 501 code and message left with the route they existed for: with no handler,
	// nothing referenced them, and an unreferenced constant is not guaranteed to survive
	// into the binary the allowlist is checked against. A literal the allowlist REQUIRES
	// and the artifact does not carry fails the gate just as loudly as an extra one.
	cases := map[string]string{
		"namespace":  Namespace,
		"permission": string(PermAvailabilityRead),
	}
	want := map[string]string{
		"namespace":  "session-cockpit",
		"permission": "session-cockpit:availability:read",
	}
	for k, got := range cases {
		if got != want[k] {
			// ⛔ THE MESSAGE NAMES ONLY THE PUBLIC FILE, deliberately. A shipped STRING is
			// never scrubbed by the export, so citing an internal document here would
			// publish that path; the allowlist beside it is public and is the file whose
			// edit actually matters to whoever trips this.
			t.Errorf("%s = %q, want %q — if this is a deliberate change, update "+
				"scripts/community-session-cockpit-strings.allow in the SAME commit", k, got, want[k])
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
