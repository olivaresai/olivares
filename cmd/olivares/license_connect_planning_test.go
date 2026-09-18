// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

// license_connect_planning_test.go: the local refresh planning boundary is the earliest
// EffectiveBoundary among the signed grants active at install, never the answer's unsigned
// credential_effective_until (an internal design note (not shipped)).

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license"
)

type connectBFixture struct {
	Signer struct {
		PublicKeyB64 string `json:"public_key_b64"`
		KeyID        string `json:"key_id"`
	} `json:"signer"`
	Effects []struct {
		Label                    string `json:"label"`
		Now                      string `json:"now"`
		Credential               string `json:"credential"`
		CredentialEffectiveUntil string `json:"credential_effective_until"`
		Lines                    map[string]struct {
			Phase       string  `json:"phase"`
			LeaseUntil  *string `json:"lease_until"`
			PaidThrough string  `json:"paid_through"`
		} `json:"lines"`
	} `json:"effects"`
}

func mustRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return v
}

// TestConnectRefreshPlanningFollowsTheShortestActiveSignedLineOfBCredentials verifies every credential
// connect-v1 B derived in its workerd paid mixed-renewal journey (B 174ceb1, receipt 09; public signer
// only) at the instant B issued it, and plans its refresh. B's own recorded per-line facts are the
// oracle: a line's boundary is its lease while provisional, else its paid_through. The unsigned
// summary B answered is the base line's boundary, and on the mixed credentials it is later.
func TestConnectRefreshPlanningFollowsTheShortestActiveSignedLineOfBCredentials(t *testing.T) {
	data, err := os.ReadFile("testdata/connect-b-mixed-renewal-174ceb1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f connectBFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(f.Signer.PublicKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("fixture signer: %v", err)
	}
	if kid, _ := license.KeyID(pub); kid != f.Signer.KeyID {
		t.Fatalf("fixture signer key id %s, derived %s", f.Signer.KeyID, kid)
	}
	kr, err := license.SingleKeyKeyring(pub, "connect-b-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Effects) != 5 {
		t.Fatalf("%d fixture credentials, want the five B derived", len(f.Effects))
	}
	laterSummaries := 0
	for _, e := range f.Effects {
		now := mustRFC3339(t, e.Now)
		v, err := kr.Verify(e.Credential, now)
		if err != nil || !v.IsCredentialV3() {
			t.Fatalf("%s: %v", e.Label, err)
		}
		var want time.Time
		for key, l := range e.Lines {
			boundary := mustRFC3339(t, l.PaidThrough)
			switch l.Phase {
			case "refund_window", "promotion_hold":
				if l.LeaseUntil == nil {
					t.Fatalf("%s: provisional %s has no lease", e.Label, key)
				}
				if lease := mustRFC3339(t, *l.LeaseUntil); lease.Before(boundary) {
					boundary = lease
				}
			case "term":
			default:
				t.Fatalf("%s: %s phase %q has no oracle here", e.Label, key, l.Phase)
			}
			if want.IsZero() || boundary.Before(want) {
				want = boundary
			}
		}
		if got := connectRefreshBoundary(v, now); !got.Equal(want) {
			t.Fatalf("%s: planning boundary %s, want %s", e.Label, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
		if mustRFC3339(t, e.CredentialEffectiveUntil).After(want) {
			laterSummaries++
		}
		if n := len(v.Credential.ActiveGrants(now)); n != len(e.Lines) {
			t.Fatalf("%s: %d active lines at issue, B recorded %d", e.Label, n, len(e.Lines))
		}
	}
	// Three of B's five: the mixed refresh and the two progression steps. At bind every line shares one
	// lease, and after the minimum hold every line is term to the same paid_through, so there the
	// summary equals the plan.
	if laterSummaries != 3 {
		t.Fatalf("%d credentials answered a summary later than the planning boundary, want 3", laterSummaries)
	}

	// Independent line rights on B's mixed refresh: at the add-on's own boundary that line stops, the
	// base and the renewed add-on keep theirs, and the plan moves to the next active boundary.
	var mixed string
	for _, e := range f.Effects {
		if e.Label == "mixed refresh" {
			mixed = e.Credential
		}
	}
	lease, term := mustRFC3339(t, "2026-09-03T10:00:00Z"), mustRFC3339(t, "2026-09-30T10:00:00Z")
	v, err := kr.Verify(mixed, lease)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(v.Credential.ActiveGrants(lease.Add(-time.Second))); n != 3 {
		t.Fatalf("%d active lines just before the add-on lease ends, want 3", n)
	}
	active := v.Credential.ActiveGrants(lease)
	if len(active) != 2 || v.Status(lease) != license.StatusValid || !v.Term().Equal(term) {
		t.Fatalf("at the add-on lease: %d active lines, status %s, base term %s", len(active), v.Status(lease), v.Term())
	}
	for _, g := range active {
		if g.Phase != license.PhaseTerm {
			t.Fatalf("a %s line is still active at its own lease boundary", g.Phase)
		}
	}
	if got := connectRefreshBoundary(v, lease); !got.Equal(term) {
		t.Fatalf("plan after the add-on lease: %s, want the base term %s", got, term)
	}
	if len(v.Credential.ActiveGrants(term)) != 0 || v.Status(term) == license.StatusValid || !connectRefreshBoundary(v, term).Equal(term) {
		t.Fatal("at the base term nothing is active and the plan is the base's end")
	}
}

// TestConnectRefreshPlanningIgnoresALaterUnsignedSummary drives bind and refresh through the real
// command tree. The credential carries a term base line and a refund-window add-on whose 72-hour lease
// ends first; the answer's credential_effective_until is the base term (as B answers) and then a far
// later instant. The recorded planning boundary is the add-on lease both times, and every line keeps
// its own right.
func TestConnectRefreshPlanningIgnoresALaterUnsignedSummary(t *testing.T) {
	stub := newConnectStub(t)
	stub.mixedAddon = true
	c := newConnectCLI(t, stub)
	c.bind()
	check := func(stage string) {
		t.Helper()
		now := time.Now()
		kr, err := licenseKeyringForDataDir(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		v, err := kr.Verify(strings.TrimSpace(string(c.license())), now)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		base, _ := v.Credential.BaseGrant()
		var addon license.Grant
		for _, g := range v.Credential.Grants {
			if g.Kind == license.GrantKindAddon {
				addon = g
			}
		}
		stub.mu.Lock()
		summary := mustRFC3339(t, stub.lastSummary)
		stub.mu.Unlock()
		lease := addon.EffectiveBoundary()
		if addon.Phase != license.PhaseRefundWindow || !lease.Before(base.EffectiveBoundary()) || !lease.Before(summary) {
			t.Fatalf("%s: the add-on lease %s must end before the base term %s and the summary %s", stage, lease, base.EffectiveBoundary(), summary)
		}
		want := lease.UTC().Format(time.RFC3339)
		if got := c.state().Last.EffectiveUntil; got != want {
			t.Fatalf("%s: recorded planning boundary %s, want the add-on lease %s (summary %s)", stage, got, want, summary.Format(time.RFC3339))
		}
		code, rep := c.run("status")
		if last, _ := rep["last"].(map[string]any); code != exitcode.OK || last["effective_until"] != want {
			t.Fatalf("%s: status = %d %v", stage, code, rep)
		}
		if n := len(v.Credential.ActiveGrants(now)); n != 2 || v.Status(now) != license.StatusValid || !v.Term().Equal(base.EffectiveBoundary()) {
			t.Fatalf("%s: %d active lines, status %s: the lines must keep their own rights", stage, n, v.Status(now))
		}
	}
	check("bind, summary at the base term")
	stub.mu.Lock()
	stub.summary = "2099-12-31T00:00:00Z"
	stub.mu.Unlock()
	if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
		t.Fatalf("refresh = %d %v", code, rep)
	}
	check("refresh, summary far later")
	c.assertNoSecretsLeaked()
}
