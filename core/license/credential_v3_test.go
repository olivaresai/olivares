// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// The v3 container's battery. EVERY refusal case is a MUTANT of one golden payload with
// exactly one thing broken, and the golden itself is asserted to parse first — so a rejection
// is attributable to the mutation and to nothing else. A battery of hand-written broken
// payloads proves only that broken payloads are broken.
//
// The rejections are checked by MESSAGE as well as by error, because all of them return an
// error and a parser that refuses for the wrong reason has told an operator nothing they can
// act on.

package license

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// The golden payload: a real v8 purchase — one base plus one add-on — with the two lines in
// DIFFERENT phases, because `mixed_phase_allowed: true` is the property the container exists
// for and a single-phase fixture would never exercise it.
const goldenV3 = `{
  "schema": "olivares.commercial.credential.v3",
  "serial": "cred_01J",
  "issue_seq": 3,
  "key_id": "issuer-2026-08",
  "key_epoch": 1,
  "issued_at": "2026-08-07T10:00:00Z",
  "not_before": "2026-08-07T10:00:00Z",
  "entity_id": "ent_acme_sl",
  "deployment_id": "dep_prod_01",
  "purpose": "production",
  "licensee": {"display_name": "ACME S.L."},
  "support_profile": "business",
  "max_users": 0,
  "clock_policy": "hwm-v1",
  "clock_key_id": "clock-2026-08",
  "clock_anchor_id": "anchor-01",
  "clock_generation": 1,
  "grants": [
    {
      "grant_id": "gr_base_01",
      "order_line_id": "ol_base_01",
      "product_id": "pdt_business",
      "kind": "base",
      "cadence": "month",
      "paid_through": "2026-09-07T10:00:00Z",
      "expires_at": "2026-09-07T10:00:00Z",
      "issuance_phase": "term",
      "guarantee_deadline": null,
      "promotion_hold_deadline": null,
      "lease_until": null,
      "price_vintage": "catalog-v8:self_hosted.business"
    },
    {
      "grant_id": "gr_addon_01",
      "order_line_id": "ol_addon_01",
      "product_id": "adn_regulated_ops",
      "kind": "addon",
      "cadence": "month",
      "paid_through": "2026-09-07T10:00:00Z",
      "expires_at": "2026-08-10T10:00:00Z",
      "issuance_phase": "refund_window",
      "guarantee_deadline": "2026-08-21T10:00:00Z",
      "promotion_hold_deadline": "2026-08-24T10:00:00Z",
      "lease_until": "2026-08-10T10:00:00Z",
      "price_vintage": "catalog-v8:addon.regulated_ops"
    }
  ]
}`

func mustParse(t *testing.T, payload string) Credential {
	t.Helper()
	c, err := ParseCredentialV3([]byte(payload))
	if err != nil {
		t.Fatalf("golden payload must parse, got: %v", err)
	}
	return c
}

// mutate rewrites the golden through a generic map so a case can break ONE thing without
// re-typing the document. Re-encoding is safe here: every mutant is re-parsed from its own
// bytes, and the cases that depend on raw bytes (duplicate keys, non-UTC, trailing data,
// non-canonical numbers) use string surgery instead and say so.
func mutate(t *testing.T, f func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(goldenV3), &doc); err != nil {
		t.Fatalf("golden is not JSON: %v", err)
	}
	f(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("mutant does not encode: %v", err)
	}
	return out
}

func grantOf(doc map[string]any, i int) map[string]any {
	return doc["grants"].([]any)[i].(map[string]any)
}

func TestV3GoldenParses(t *testing.T) {
	c := mustParse(t, goldenV3)
	if c.Schema != CredentialSchemaV3 {
		t.Fatalf("schema = %q", c.Schema)
	}
	if len(c.Grants) != 2 {
		t.Fatalf("grants = %d, want 2", len(c.Grants))
	}
	// The property the container exists for: two lines of ONE purchase in two phases.
	if c.Grants[0].Phase != PhaseTerm || c.Grants[1].Phase != PhaseRefundWindow {
		t.Fatalf("phases = %q/%q, want term/refund_window — the mixed-phase fixture is the point",
			c.Grants[0].Phase, c.Grants[1].Phase)
	}
	if c.Grants[0].Kind != GrantKindBase || c.Grants[1].Kind != GrantKindAddon {
		t.Fatal("the golden must carry exactly one base and one add-on")
	}
}

// Every refusal, as a mutation of the golden. The `want` string must appear in the error:
// an exit code without a reason is a red nobody can act on.
func TestV3Refusals(t *testing.T) {
	cases := []struct {
		name string
		doc  func(map[string]any)
		want string
	}{
		{"empty serial", func(d map[string]any) { d["serial"] = "" }, "serial is empty"},
		{"empty entity_id", func(d map[string]any) { d["entity_id"] = "" }, "entity_id is empty"},
		{"empty deployment_id", func(d map[string]any) { d["deployment_id"] = "" }, "deployment_id is empty"},
		{"empty licensee", func(d map[string]any) {
			d["licensee"] = map[string]any{"display_name": ""}
		}, "licensee.display_name is empty"},
		{"purpose not production/staging", func(d map[string]any) { d["purpose"] = "demo" }, "not production or staging"},
		{"max_users reintroduces a seat cap", func(d map[string]any) { d["max_users"] = 25 }, "max_users must be 0"},
		{"no grants at all", func(d map[string]any) { d["grants"] = []any{} }, "carries no grants"},
		{"two bases", func(d map[string]any) { grantOf(d, 1)["kind"] = "base" }, "exactly one base grant"},
		{"zero bases", func(d map[string]any) { grantOf(d, 0)["kind"] = "addon" }, "exactly one base grant"},
		{"duplicate grant_id", func(d map[string]any) { grantOf(d, 1)["grant_id"] = "gr_base_01" }, "appears twice"},
		{"duplicate order_line_id", func(d map[string]any) { grantOf(d, 1)["order_line_id"] = "ol_base_01" }, "appears twice"},
		{"empty grant_id", func(d map[string]any) { grantOf(d, 0)["grant_id"] = "" }, "grant_id is empty"},
		{"unknown kind", func(d map[string]any) { grantOf(d, 1)["kind"] = "bundle" }, "neither base nor addon"},
		{"unknown cadence", func(d map[string]any) { grantOf(d, 0)["cadence"] = "week" }, "neither month nor year"},
		{"unknown phase", func(d map[string]any) { grantOf(d, 0)["issuance_phase"] = "trialing" }, "not a phase this build knows"},

		// The invariant that keeps a paid module from outliving the product hosting it.
		{"add-on outlasts its base", func(d map[string]any) {
			grantOf(d, 1)["paid_through"] = "2026-12-07T10:00:00Z"
		}, "past its base"},

		// Provisional phases.
		{"provisional with no lease", func(d map[string]any) { grantOf(d, 1)["lease_until"] = nil }, "carries no lease_until"},
		{"lease != expires", func(d map[string]any) {
			grantOf(d, 1)["lease_until"] = "2026-08-09T10:00:00Z"
		}, "lease_until == expires_at"},
		{"lease past the 72h ceiling", func(d map[string]any) {
			g := grantOf(d, 1)
			g["lease_until"] = "2026-08-11T10:00:00Z"
			g["expires_at"] = "2026-08-11T10:00:00Z"
		}, "ceiling from issue"},
		{"lease past the hold deadline", func(d map[string]any) {
			g := grantOf(d, 1)
			g["promotion_hold_deadline"] = "2026-08-09T10:00:00Z"
		}, "past promotion_hold_deadline"},
		{"provisional with no hold deadline", func(d map[string]any) {
			grantOf(d, 1)["promotion_hold_deadline"] = nil
		}, "no promotion_hold_deadline"},

		// Term.
		{"term still leasing", func(d map[string]any) {
			grantOf(d, 0)["lease_until"] = "2026-08-09T10:00:00Z"
		}, "must carry no lease_until"},
		{"term expires != paid_through", func(d map[string]any) {
			grantOf(d, 0)["expires_at"] = "2026-08-20T10:00:00Z"
		}, "expires_at == paid_through"},

		// Renewal grace: the only phase allowed past paid_through, and only bounded.
		{"grace without the attested reason", func(d map[string]any) {
			g := grantOf(d, 0)
			g["issuance_phase"] = "renewal_grace"
			g["grace_ends_at"] = "2026-09-10T10:00:00Z"
		}, "grace_reason=renewal_failure"},
		{"grace with no end", func(d map[string]any) {
			g := grantOf(d, 0)
			g["issuance_phase"] = "renewal_grace"
			g["grace_reason"] = "renewal_failure"
		}, "carries no grace_ends_at"},
		{"grace wider than the published maximum", func(d map[string]any) {
			g := grantOf(d, 0)
			g["issuance_phase"] = "renewal_grace"
			g["grace_reason"] = "renewal_failure"
			g["grace_ends_at"] = "2026-09-20T10:00:00Z" // paid_through + 13 days > 168h
		}, "exceeds the published maximum"},
		{"expires past paid_through outside a grace", func(d map[string]any) {
			g := grantOf(d, 0)
			g["expires_at"] = "2026-10-07T10:00:00Z"
			g["issuance_phase"] = "refund_window"
			g["lease_until"] = "2026-10-07T10:00:00Z"
			g["promotion_hold_deadline"] = "2026-11-07T10:00:00Z"
		}, "ceiling from issue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCredentialV3(mutate(t, tc.doc))
			if err == nil {
				t.Fatalf("mutant SURVIVED: %s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for the wrong reason\n got: %v\nwant substring: %q", err, tc.want)
			}
		})
	}
}

// These four cannot be expressed through a map: they are properties of the BYTES, and
// re-encoding a map would repair them silently.
func TestV3ByteLevelRefusals(t *testing.T) {
	cases := []struct {
		name, payload, want string
	}{
		{
			// encoding/json keeps the LAST occurrence and says nothing, so a blob signed over
			// bytes containing both values verifies while two readers disagree about when the
			// right ends.
			name:    "duplicate key",
			payload: strings.Replace(goldenV3, `"paid_through": "2026-09-07T10:00:00Z",`, `"paid_through": "2026-09-07T10:00:00Z", "paid_through": "2027-09-07T10:00:00Z",`, 1),
			want:    "twice in one object",
		},
		{
			// Same instant, different bytes. Refused rather than normalised, or the canonical-
			// bytes guarantee stops being one.
			name:    "offset instead of Z",
			payload: strings.Replace(goldenV3, `"issued_at": "2026-08-07T10:00:00Z"`, `"issued_at": "2026-08-07T10:00:00+00:00"`, 1),
			want:    "must be UTC with a trailing Z",
		},
		{
			name:    "unknown field",
			payload: strings.Replace(goldenV3, `"purpose": "production",`, `"purpose": "production", "unlimited": true,`, 1),
			want:    "does not decode",
		},
		{
			name:    "non-canonical number",
			payload: strings.Replace(goldenV3, `"issue_seq": 3,`, `"issue_seq": 3e0,`, 1),
			want:    "non-canonical number",
		},
		{
			name:    "trailing data after the payload",
			payload: goldenV3 + `{"schema":"olivares.commercial.credential.v3"}`,
			want:    "trailing data",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCredentialV3([]byte(tc.payload))
			if err == nil {
				t.Fatalf("mutant SURVIVED: %s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refused for the wrong reason\n got: %v\nwant substring: %q", err, tc.want)
			}
		})
	}
}

func TestV3NotAV3PayloadIsDistinguishable(t *testing.T) {
	// A v1/v2 blob must come back as ErrNotV3 and NOT as "broken v3": a caller that falls back
	// to the old reader on any error would silence real corruption in a genuine v3.
	_, err := ParseCredentialV3([]byte(`{"licensee":"ACME","issued_at":"2026-08-07T10:00:00Z"}`))
	if !errors.Is(err, ErrNotV3) {
		t.Fatalf("a v1/v2 payload must report ErrNotV3, got %v", err)
	}
	// And a payload that NAMES v3 and is broken must NOT be ErrNotV3.
	_, err = ParseCredentialV3([]byte(`{"schema":"olivares.commercial.credential.v3","grants":`))
	if err == nil || errors.Is(err, ErrNotV3) {
		t.Fatalf("a broken v3 must not be reported as 'not v3', got %v", err)
	}
}

func TestV3EffectiveBoundaryPerPhase(t *testing.T) {
	c := mustParse(t, goldenV3)
	base, addon := c.Grants[0], c.Grants[1]

	// term -> paid_through
	if got := base.EffectiveBoundary(); !got.Equal(base.PaidThrough) {
		t.Fatalf("term boundary = %s, want paid_through %s", got, base.PaidThrough)
	}
	// provisional -> min(paid_through, lease_until); the lease is the earlier one here, which
	// is the whole point of a provisional grant.
	if got := addon.EffectiveBoundary(); !got.Equal(addon.LeaseUntil) {
		t.Fatalf("provisional boundary = %s, want lease_until %s", got, addon.LeaseUntil)
	}
	if !addon.LeaseUntil.Before(addon.PaidThrough) {
		t.Fatal("the fixture must have the lease earlier than paid_through, or the min() is untested")
	}
}

func TestV3HalfOpenBoundary(t *testing.T) {
	// `intervals: half-open` (PRICING-CANON.md:132): the boundary instant itself is OUT, so a
	// grant and its successor never both hold at the same moment.
	c := mustParse(t, goldenV3)
	g := c.Grants[0]
	b := g.EffectiveBoundary()
	if !g.Active(b.Add(-time.Nanosecond)) {
		t.Fatal("a grant must be active one instant before its boundary")
	}
	if g.Active(b) {
		t.Fatal("a grant must NOT be active AT its boundary — half-open intervals")
	}
}

func TestV3ActiveGrantsIsAFilterAndNotASummary(t *testing.T) {
	c := mustParse(t, goldenV3)
	// A moment after the add-on's lease ends but well inside the base's term. If the container
	// had one expiry, one of these two answers would be wrong.
	at := c.Grants[1].LeaseUntil.Add(time.Hour)
	active := c.ActiveGrants(at)
	if len(active) != 1 || active[0].Kind != GrantKindBase {
		t.Fatalf("at %s only the base must be active, got %d grants", at.Format(time.RFC3339), len(active))
	}
	// And before not_before, nothing is active at all.
	if got := c.ActiveGrants(c.NotBefore.Add(-time.Second)); len(got) != 0 {
		t.Fatalf("before not_before nothing may be active, got %d", len(got))
	}
}
