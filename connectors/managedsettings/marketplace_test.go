// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestValidateMarketplaceArray covers each source type's required fields, regex
// validity, the non-array (legacy bool) rejection, and the absent/empty cases.
func TestValidateMarketplaceArray(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantSub string // substring expected in an issue; "" = expect NO issues
	}{
		{"absent", ``, ""},
		{"null", `null`, ""},
		{"empty-array-lockdown", `[]`, ""},
		{"github-ok", `[{"source":"github","repo":"acme/plugins"}]`, ""},
		{"github-ref-ok", `[{"source":"github","repo":"acme/plugins","ref":"v2.0"}]`, ""},
		{"url-ok", `[{"source":"url","url":"https://x/marketplace.json"}]`, ""},
		{"hostPattern-ok", `[{"source":"hostPattern","hostPattern":"^github\\.acme\\.com$"}]`, ""},
		{"pathPattern-ok", `[{"source":"pathPattern","pathPattern":"^/opt/approved/"}]`, ""},
		{"legacy-bool-rejected", `true`, "must be an ARRAY"},
		{"object-rejected", `{"source":"github"}`, "must be an ARRAY"},
		{"github-missing-repo", `[{"source":"github"}]`, "repo is required"},
		{"url-missing-url", `[{"source":"url"}]`, "url is required"},
		{"hostPattern-missing", `[{"source":"hostPattern"}]`, "hostPattern is required"},
		{"hostPattern-bad-regex", `[{"source":"hostPattern","hostPattern":"("}]`, "not a valid regular expression"},
		{"pathPattern-bad-regex", `[{"source":"pathPattern","pathPattern":"[a-"}]`, "not a valid regular expression"},
		{"unknown-source", `[{"source":"gitlab","repo":"x/y"}]`, "is not one of github|url|hostPattern|pathPattern"},
		{"empty-source", `[{"repo":"x/y"}]`, "source is required"},
		{"second-entry-bad", `[{"source":"github","repo":"a/b"},{"source":"url"}]`, "[1].url is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issues := validateMarketplaceArray(json.RawMessage(tc.raw), "strictKnownMarketplaces")
			if tc.wantSub == "" {
				if len(issues) != 0 {
					t.Fatalf("expected no issues, got %v", issues)
				}
				return
			}
			if !containsSub(issues, tc.wantSub) {
				t.Fatalf("expected an issue containing %q, got %v", tc.wantSub, issues)
			}
		})
	}
}

// TestValidateJSONMarketplace proves the marketplace validation is wired into the
// document-level ValidateJSON (the server-side authoring gate).
func TestValidateJSONMarketplace(t *testing.T) {
	bad := `{"strictKnownMarketplaces": true, "blockedMarketplaces": [{"source":"github"}]}`
	issues := ValidateJSON([]byte(bad))
	if !containsSub(issues, "strictKnownMarketplaces must be an ARRAY") {
		t.Errorf("expected strictKnownMarketplaces array issue, got %v", issues)
	}
	if !containsSub(issues, "blockedMarketplaces[0].repo is required") {
		t.Errorf("expected blockedMarketplaces repo issue, got %v", issues)
	}
	good := `{"strictKnownMarketplaces": [{"source":"github","repo":"acme/p"}]}`
	if issues := ValidateJSON([]byte(good)); len(issues) != 0 {
		t.Errorf("valid marketplace allowlist reported issues: %v", issues)
	}
}

// TestMarketplaceRoundTrip verifies the array (allowlist) and the empty-array lockdown
// both render and parse back to the same Policy intent.
func TestMarketplaceRoundTrip(t *testing.T) {
	// Allowlist with all four source types.
	p := Policy{StrictKnownMarketplaces: &[]Marketplace{
		{Source: MarketplaceSourceGitHub, Repo: "acme/approved", Ref: "main", Path: "mp"},
		{Source: MarketplaceSourceURL, URL: "https://x/marketplace.json"},
		{Source: MarketplaceSourceHostPattern, HostPattern: "^github\\.acme\\.com$"},
		{Source: MarketplaceSourcePathPattern, PathPattern: "^/opt/"},
	}}
	out, err := Render(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), `"strictKnownMarketplaces"`) {
		t.Fatalf("rendered file missing strictKnownMarketplaces:\n%s", out)
	}
	got, err := ParsePolicyFromWire(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.StrictKnownMarketplaces == nil || len(*got.StrictKnownMarketplaces) != 4 {
		t.Fatalf("round-trip lost entries: %+v", got.StrictKnownMarketplaces)
	}
	if !sameMarketplaceSet(*p.StrictKnownMarketplaces, *got.StrictKnownMarketplaces) {
		t.Errorf("round-trip changed the allowlist set")
	}
}

// TestMarketplaceLockdownRendersEmptyArray proves the `[]` complete-lockdown posture is
// distinct from "unset": a non-nil empty slice renders as `[]` (not omitted, not null)
// and round-trips back to a non-nil empty allowlist.
func TestMarketplaceLockdownRendersEmptyArray(t *testing.T) {
	p := Policy{StrictKnownMarketplaces: &[]Marketplace{}}
	out, err := Render(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(out), `"strictKnownMarketplaces": []`) {
		t.Fatalf("lockdown did not render as []:\n%s", out)
	}
	got, err := ParsePolicyFromWire(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.StrictKnownMarketplaces == nil {
		t.Fatal("lockdown round-tripped to unset (nil) — lost the [] posture")
	}
	if len(*got.StrictKnownMarketplaces) != 0 {
		t.Errorf("lockdown round-trip gained entries: %+v", *got.StrictKnownMarketplaces)
	}
	// An UNSET allowlist must omit the key entirely (distinct from lockdown).
	if out, _ := Render(Policy{}); strings.Contains(string(out), "strictKnownMarketplaces") {
		t.Errorf("unset allowlist must omit the key, got:\n%s", out)
	}
}

// TestMarketplaceDrift covers the allowlist drift cases (absent, lockdown-not-enforced,
// set mismatch, exact match) and the blocklist missing-entry case.
func TestMarketplaceDrift(t *testing.T) {
	at := testNow()
	allow := &[]Marketplace{{Source: MarketplaceSourceGitHub, Repo: "acme/approved"}}

	// Host has no allowlist at all → Medium drift.
	exp := Policy{StrictKnownMarketplaces: allow}
	live := managedSettings{}
	if !hasDrift(driftFindings("h", exp, live, at), "allowlist is NOT enforced") {
		t.Error("expected allowlist-not-enforced drift")
	}

	// Org authored [] lockdown but host has nothing → HIGH drift.
	expLock := Policy{StrictKnownMarketplaces: &[]Marketplace{}}
	fs := driftFindings("h", expLock, managedSettings{}, at)
	if !hasDrift(fs, "LOCKDOWN") {
		t.Error("expected lockdown-not-enforced drift")
	}
	if sevOf(fs, "LOCKDOWN") != model.SeverityHigh {
		t.Error("lockdown-not-enforced must be HIGH")
	}

	// Host allowlist differs from authored → Medium drift.
	liveDiff := managedSettings{StrictKnownMarketplaces: json.RawMessage(`[{"source":"github","repo":"other/repo"}]`)}
	if !hasDrift(driftFindings("h", exp, liveDiff, at), "drifts from the authored set") {
		t.Error("expected set-mismatch drift")
	}

	// Host allowlist matches exactly → NO marketplace drift.
	liveMatch := managedSettings{StrictKnownMarketplaces: json.RawMessage(`[{"source":"github","repo":"acme/approved"}]`)}
	for _, f := range driftFindings("h", exp, liveMatch, at) {
		if strings.Contains(f.Title, "marketplace") || strings.Contains(f.Title, "allowlist") {
			t.Errorf("matching allowlist should not drift, got %q", f.Title)
		}
	}

	// Blocklist: authored entry missing on host → Medium drift naming the source.
	expBlock := Policy{BlockedMarketplaces: []Marketplace{{Source: MarketplaceSourceGitHub, Repo: "bad/repo"}}}
	if !hasDrift(driftFindings("h", expBlock, managedSettings{}, at), "blocklist entry missing on host: github:bad/repo") {
		t.Error("expected blocklist-missing drift naming the source")
	}
}

// --- helpers -----------------------------------------------------------------

func containsSub(issues []string, sub string) bool {
	for _, i := range issues {
		if strings.Contains(i, sub) {
			return true
		}
	}
	return false
}

// hasDrift reports whether any finding's title contains sub.
func hasDrift(fs []model.FindingReport, sub string) bool {
	for _, f := range fs {
		if strings.Contains(f.Title, sub) {
			return true
		}
	}
	return false
}

// sevOf returns the severity of the first finding whose title contains sub (or "").
func sevOf(fs []model.FindingReport, sub string) model.Severity {
	for _, f := range fs {
		if strings.Contains(f.Title, sub) {
			return f.Severity
		}
	}
	return ""
}
