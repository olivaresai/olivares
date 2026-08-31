// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Managed-settings 2026 currency (VERIFIED 2026-06-16 against
// code.claude.com/docs/en/{settings,skills,permissions,server-managed-settings}, two
// independent reads).
// Per new key these tests prove the lockstep property: a key survives
// ParsePolicyFromWire→Render (CanonicalJSON), counts in HasAnyKeys, validates server-side,
// and drifts against a bare host. They also prove the managed-settings.d/ drop-in merge
// (ordering, deep-merge of objects, array concat+dedup, scalar later-wins).

// wire2026 exercises every key in its VERIFIED wire shape: disableRemoteControl as a
// top-level bool; skillOverrides as a top-level name→state map; policyHelper as the
// {"path": ...} object.
const wire2026 = `{
  "disableRemoteControl": true,
  "skillOverrides": {
    "legacy-context": "name-only",
    "deploy": "off",
    "triage": "user-invocable-only"
  },
  "policyHelper": {"path": "/usr/local/bin/claude-policy"}
}`

func TestS188RoundTripPreservesNewKeys(t *testing.T) {
	canon, err := CanonicalJSON([]byte(wire2026))
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	s := string(canon)
	for _, want := range []string{
		`"disableRemoteControl": true`,
		`"skillOverrides"`,
		`"legacy-context": "name-only"`,
		`"deploy": "off"`,
		`"triage": "user-invocable-only"`,
		`"policyHelper"`,
		`"path": "/usr/local/bin/claude-policy"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("canonical round-trip lost %q\n%s", want, s)
		}
	}
	if !HasAnyKeys([]byte(wire2026)) {
		t.Error("HasAnyKeys must report a new-keys-only document as governing")
	}
	if issues := ValidateJSON([]byte(wire2026)); len(issues) != 0 {
		t.Errorf("fixture must validate clean, got %v", issues)
	}
}

func TestS188HasAnyKeysPerKey(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want bool
	}{
		"remote control":      {`{"disableRemoteControl": true}`, true},
		"skill overrides":     {`{"skillOverrides": {"deploy": "off"}}`, true},
		"policy helper":       {`{"policyHelper": {"path": "/x"}}`, true},
		"helper without path": {`{"policyHelper": {}}`, false}, // a path-less helper delivers nothing
		"remote control off":  {`{"disableRemoteControl": false}`, false},
	} {
		if got := HasAnyKeys([]byte(tc.doc)); got != tc.want {
			t.Errorf("%s: HasAnyKeys = %v, want %v", name, got, tc.want)
		}
	}
}

func TestS188PolicyHelperForwardCompatExtraKeys(t *testing.T) {
	// A host policyHelper carrying the to-confirm sibling keys (timeoutMs/refreshIntervalMs)
	// must still parse — only `path` is asserted, the rest are ignored on read.
	doc := `{"policyHelper": {"path": "/opt/claude-policy", "timeoutMs": 5000, "refreshIntervalMs": 60000}}`
	if issues := ValidateJSON([]byte(doc)); len(issues) != 0 {
		t.Errorf("policyHelper with extra sibling keys must validate, got %v", issues)
	}
	p, err := ParsePolicyFromWire([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.PolicyHelper == nil || p.PolicyHelper.Path != "/opt/claude-policy" {
		t.Errorf("policyHelper path not extracted: %+v", p.PolicyHelper)
	}
}

func TestS188ValidateJSONNegatives(t *testing.T) {
	cases := map[string]struct {
		doc  string
		want string
	}{
		"unknown skill state":  {`{"skillOverrides": {"deploy": "hidden"}}`, "is not one of on|name-only|user-invocable-only|off"},
		"empty skill name":     {`{"skillOverrides": {"": "on"}}`, "empty skill-name key"},
		"helper no path":       {`{"policyHelper": {}}`, "non-empty `path`"},
		"helper blank path":    {`{"policyHelper": {"path": "   "}}`, "non-empty `path`"},
		"helper not object":    {`{"policyHelper": "/usr/local/bin/claude-policy"}`, "wrong type"},
		"remote ctrl non-bool": {`{"disableRemoteControl": "yes"}`, "wrong type"},
	}
	for name, tc := range cases {
		issues := ValidateJSON([]byte(tc.doc))
		found := false
		for _, is := range issues {
			if strings.Contains(is, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want an issue containing %q, got %v", name, tc.want, issues)
		}
	}
	// Valid forms must NOT be flagged.
	for name, doc := range map[string]string{
		"all four states":   `{"skillOverrides": {"a": "on", "b": "name-only", "c": "user-invocable-only", "d": "off"}}`,
		"helper with path":  `{"policyHelper": {"path": "/usr/local/bin/claude-policy"}}`,
		"remote control on": `{"disableRemoteControl": true}`,
	} {
		if issues := ValidateJSON([]byte(doc)); len(issues) != 0 {
			t.Errorf("%s must be valid, got %v", name, issues)
		}
	}
}

func TestS188DriftFindings(t *testing.T) {
	expected := Policy{
		DisableRemoteControl: true,
		SkillOverrides:       map[string]string{"deploy": SkillOff, "legacy-context": SkillNameOnly},
		PolicyHelper:         &PolicyHelper{Path: "/usr/local/bin/claude-policy"},
	}

	// A bare host drifts on every authored key, with the designed severities.
	d := driftFindings("host-a", expected, managedSettings{}, testNow())
	sev := func(sub string) (model.Severity, bool) {
		for _, f := range d {
			if strings.Contains(f.Title, sub) {
				return f.Severity, true
			}
		}
		return "", false
	}
	if s, ok := sev("Remote Control is NOT disabled"); !ok || s != model.SeverityHigh {
		t.Errorf("disableRemoteControl drift = (%v, %v), want HIGH", s, ok)
	}
	if s, ok := sev("skill visibility override not enforced on host: deploy"); !ok || s != model.SeverityMedium {
		t.Errorf("skillOverrides(deploy) drift = (%v, %v), want MEDIUM", s, ok)
	}
	if s, ok := sev("policyHelper is NOT configured"); !ok || s != model.SeverityMedium {
		t.Errorf("policyHelper drift = (%v, %v), want MEDIUM", s, ok)
	}

	// A host that fully matches the authored intent does NOT drift.
	rendered, err := Render(expected)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	matching, err := parseLive(rendered)
	if err != nil {
		t.Fatalf("parse rendered: %v", err)
	}
	if d := driftFindings("host-a", expected, matching, testNow()); len(d) != 0 {
		t.Errorf("a host that matches the policy must not drift, got %+v", d)
	}
}

func TestS188SkillOverrideEffectiveDefault(t *testing.T) {
	// A skill the org sets to "on" but the host omits is NOT drift (absent ⇒ "on").
	onlyOn := Policy{SkillOverrides: map[string]string{"deploy": SkillOn}}
	if d := driftFindings("h", onlyOn, managedSettings{}, testNow()); len(d) != 0 {
		t.Errorf(`authored "on" + host-absent must not drift (absent ⇒ "on"), got %+v`, d)
	}
	// A skill the org HIDES but the host leaves visible (absent ⇒ "on") IS drift.
	hide := Policy{SkillOverrides: map[string]string{"deploy": SkillOff}}
	d := driftFindings("h", hide, managedSettings{}, testNow())
	if len(d) != 1 || !strings.Contains(d[0].Title, "org wants off, host has on") {
		t.Errorf(`authored "off" + host-absent must drift (host shows it), got %+v`, d)
	}
	// A host that downgrades visibility differently than authored also drifts.
	liveVisible := managedSettings{SkillOverrides: map[string]string{"deploy": SkillNameOnly}}
	d2 := driftFindings("h", hide, liveVisible, testNow())
	if len(d2) != 1 || !strings.Contains(d2[0].Title, "org wants off, host has name-only") {
		t.Errorf("state mismatch must drift, got %+v", d2)
	}
}

// --- managed-settings.d/ drop-in merge ----------------------------------------

func TestDeepMergeJSONSemantics(t *testing.T) {
	// Objects deep-merge; scalars later-wins; arrays concat+dedup.
	base := mustObj(t, `{"permissions": {"allow": ["Read(/a)"], "defaultMode": "acceptEdits"}, "minimumVersion": "2.0.0"}`)
	overlay := mustObj(t, `{"permissions": {"allow": ["Read(/a)", "Read(/b)"], "deny": ["Read(/secret)"], "defaultMode": "plan"}}`)
	merged := deepMergeJSON(base, overlay).(map[string]any)

	perms := merged["permissions"].(map[string]any)
	// scalar later-wins
	if perms["defaultMode"] != "plan" {
		t.Errorf("scalar override: defaultMode = %v, want plan", perms["defaultMode"])
	}
	// object deep-merge keeps base-only keys (minimumVersion) and merges sub-objects (deny added)
	if merged["minimumVersion"] != "2.0.0" {
		t.Errorf("deep-merge dropped base-only key minimumVersion: %v", merged["minimumVersion"])
	}
	if _, ok := perms["deny"]; !ok {
		t.Error("deep-merge dropped overlay-only sub-key permissions.deny")
	}
	// array concat + dedup: ["Read(/a)"] ∪ ["Read(/a)","Read(/b)"] = ["Read(/a)","Read(/b)"]
	allow := perms["allow"].([]any)
	if len(allow) != 2 || allow[0] != "Read(/a)" || allow[1] != "Read(/b)" {
		t.Errorf("array concat+dedup: allow = %v, want [Read(/a) Read(/b)]", allow)
	}
}

func TestLoadDropinOrderingAndFiltering(t *testing.T) {
	dir := t.TempDir()
	// Alphabetical sort: 20- must win over 10- on a scalar conflict.
	write(t, dir, "20-security.json", `{"permissions": {"defaultMode": "bypassPermissions"}}`)
	write(t, dir, "10-telemetry.json", `{"permissions": {"defaultMode": "plan"}}`)
	// Ignored: dotfile, non-json, subdirectory.
	write(t, dir, ".hidden.json", `{"permissions": {"defaultMode": "acceptEdits"}}`)
	write(t, dir, "notes.txt", `not json`)
	if err := os.Mkdir(filepath.Join(dir, "nested.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	frags, findings, err := loadDropin(dir, "h", testNow())
	if err != nil {
		t.Fatalf("loadDropin: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("clean dir must yield no findings, got %+v", findings)
	}
	if len(frags) != 2 || frags[0].name != "10-telemetry.json" || frags[1].name != "20-security.json" {
		t.Fatalf("ordering/filtering wrong: %v", fragNames(frags))
	}
	merged, _ := mergeEffective(map[string]any{}, frags)
	live, _ := parseLive(merged)
	if live.Permissions == nil || live.Permissions.DefaultMode != "bypassPermissions" {
		t.Errorf("20- must win the alphabetical merge, got %+v", live.Permissions)
	}
}

func TestLoadDropinInvalidFragmentFinding(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "10-ok.json", `{"disableRemoteControl": true}`)
	write(t, dir, "20-bad.json", `{ not json`)
	write(t, dir, "30-array.json", `["not", "an", "object"]`)

	frags, findings, err := loadDropin(dir, "h", testNow())
	if err != nil {
		t.Fatalf("loadDropin: %v", err)
	}
	if len(frags) != 1 || frags[0].name != "10-ok.json" {
		t.Errorf("only the valid fragment should merge, got %v", fragNames(frags))
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 invalid-fragment findings, got %d: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if f.Severity != model.SeverityMedium || f.Kind != findingKindDrift {
			t.Errorf("invalid-fragment finding = %+v, want Medium policy_drift", f)
		}
	}
}

func TestGatherMergesDropinIntoDriftAndEdges(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "managed-settings.json")
	// Base disables NOTHING and allows one path.
	if err := os.WriteFile(base, []byte(`{"permissions": {"allow": ["Read(/data/**)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A drop-in fragment disables Remote Control and adds an allow rule.
	dd := filepath.Join(dir, "managed-settings.d")
	if err := os.Mkdir(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dd, "20-remote.json", `{"disableRemoteControl": true, "permissions": {"allow": ["Read(/logs/**)"]}}`)

	// The org requires Remote Control disabled. Without the fragment the base would drift;
	// WITH it merged the effective config satisfies the policy → no disableRemoteControl drift.
	expected := Policy{DisableRemoteControl: true}
	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgConfigPath: base, cfgScope: "host-a", cfgExpectedPolicy: mustJSON(t, expected),
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "Remote Control is NOT disabled") {
			t.Errorf("the drop-in fragment should satisfy disableRemoteControl — no drift expected, got %q", f.Title)
		}
	}
	// The merged effective allow (base ∪ fragment) feeds PERMITTED edges only when no
	// expected policy pins the allow set; here expected has no allow, so edges come from
	// the authored allow (empty). Re-run observe-only to inventory the merged allow.
	obs := New()
	if err := obs.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgConfigPath: base, cfgScope: "host-a",
	}}); err != nil {
		t.Fatalf("open observe: %v", err)
	}
	osink := &memSink{}
	if err := obs.Gather(t.Context(), osink); err != nil {
		t.Fatalf("gather observe: %v", err)
	}
	gotRules := map[string]bool{}
	for _, e := range osink.edges() {
		gotRules[e.ResourceRef] = true
	}
	if !gotRules["Read(/data/**)"] || !gotRules["Read(/logs/**)"] {
		t.Errorf("merged allow should inventory base ∪ fragment rules, got %v", gotRules)
	}
}

func TestGatherBaseAbsentFragmentsGovern(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "managed-settings.json") // never created
	dd := filepath.Join(dir, "managed-settings.d")
	if err := os.Mkdir(dd, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dd, "10-policy.json", `{"disableRemoteControl": true}`)

	s := New()
	if err := s.Open(t.Context(), sdk.Config{Settings: map[string]string{
		cfgConfigPath: base, cfgScope: "host-a",
		cfgExpectedPolicy: mustJSON(t, Policy{DisableRemoteControl: true}),
	}}); err != nil {
		t.Fatalf("open: %v", err)
	}
	sink := &memSink{}
	if err := s.Gather(t.Context(), sink); err != nil {
		t.Fatalf("gather: %v", err)
	}
	// A base-absent host governed entirely by fragments must NOT be reported ungoverned,
	// and the fragment's disableRemoteControl must satisfy the policy (no drift).
	for _, f := range sink.findings() {
		if strings.Contains(f.Title, "ungoverned") || strings.Contains(f.Title, "is absent") {
			t.Errorf("fragments govern the host — no absence finding expected, got %q", f.Title)
		}
		if strings.Contains(f.Title, "Remote Control is NOT disabled") {
			t.Errorf("fragment disables Remote Control — no drift expected, got %q", f.Title)
		}
	}
}

// --- helpers ----------------------------------------------------------------

func mustObj(t *testing.T, s string) map[string]any {
	t.Helper()
	obj, ok := jsonObject([]byte(s))
	if !ok {
		t.Fatalf("not a JSON object: %s", s)
	}
	return obj
}

// mustJSON encodes an authored Policy as the expected_policy config value (the snake_case
// Policy JSON, NOT the rendered managed-settings wire shape).
func mustJSON(t *testing.T, p Policy) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fragNames(frags []dropinFragment) []string {
	out := make([]string, 0, len(frags))
	for _, f := range frags {
		out = append(out, f.name)
	}
	return out
}
