// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// observationApprovalInjections is the B1 discriminator matrix. Each spelling
// is one encoding/json would fold onto action, observed or existing (see
// encoding/json/fold.go: foldName equality is bytes.EqualFold). Values use the
// types those fields actually have, so a case-sensitive presence check is the
// only thing that can reject them: the decoder will otherwise accept them.
var observationApprovalInjections = []struct {
	name  string
	field string
}{
	{"lowercase action", `"action": "noop",`},
	{"title action", `"Action": "noop",`},
	{"upper action", `"ACTION": "noop",`},
	{"mixed action", `"aCtIoN": "noop",`},
	{"unicode-escaped title action", `"\u0041ction": "noop",`},
	{"unicode-escaped lowercase action", `"\u0061ction": "noop",`},
	{"lowercase observed", `"observed": [],`},
	{"title observed", `"Observed": [],`},
	{"upper observed", `"OBSERVED": [],`},
	{"mixed observed", `"ObSeRvEd": [],`},
	{"unicode-escaped title observed", `"\u004fbserved": [],`},
	{"long-s observed", `"ob\u017ferved": [],`},
	{"lowercase existing", `"existing": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
	{"title existing", `"Existing": {"release_dir": "/x", "receipt_present": true, "executable_present": true, "note": "already installed; this approval downloads nothing"},`},
	{"upper existing", `"EXISTING": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
	{"mixed existing", `"eXiStInG": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
	{"unicode-escaped title existing", `"\u0045xisting": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
	{"long-s existing", `"exi\u017fting": {"release_dir": "/x", "receipt_present": true, "executable_present": true},`},
	{"F4 combo title claims", `"Action": "noop",` + "\n  " + `"Observed": [],` + "\n  " + `"Existing": {"release_dir": "/x", "receipt_present": true, "executable_present": true, "note": "already installed; this approval downloads nothing"},`},
	{"duplicate action spellings", `"Action": "noop",` + "\n  " + `"ACTION": "noop",`},
}

func sampleApprovalJSON(t *testing.T) []byte {
	t.Helper()
	p := &Plan{
		Schema:           PlanSchema,
		Driver:           "claude",
		Channel:          ChannelExact,
		RequestedVersion: "2.1.261",
		Version:          "2.1.261",
		Platform:         Platform{OS: "linux", Arch: "amd64", Libc: "glibc"},
		VendorPlatform:   "linux-x64",
		Source: Source{
			Kind:         SourceOfficial,
			BaseURL:      "https://example.invalid",
			ManifestURL:  "https://example.invalid/manifest.json",
			SignatureURL: "https://example.invalid/manifest.json.sig",
			ArtifactURL:  "https://example.invalid/claude",
		},
		Artifact:    Artifact{Name: "claude", SHA256: "aa", Size: 1},
		Provenance:  Provenance{Class: ProvenancePublisherSigned, KeyFingerprint: "F", ManifestSHA256: "m", KeySHA256: "k"},
		Destination: Destination{Root: "/tmp/root", ReleaseDir: "/tmp/root/claude/v", Executable: "/tmp/root/claude/v/bin/claude"},
		Verified:    []string{"manifest_signature", "manifest_version", "artifact_selection"},
		Action:      ActionInstall,
		Observed:    []string{"existing_release_dir"},
		Existing:    &Observed{Note: "must not appear in the approval file"},
	}
	raw, err := MarshalPlan(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func injectApprovalJSON(raw []byte, field string) ([]byte, bool) {
	edited := bytes.Replace(raw, []byte(`"verified": [`), []byte(field+"\n  \"verified\": ["), 1)
	return edited, !bytes.Equal(edited, raw)
}

func TestEncodingJSONFoldsObservationFieldNames(t *testing.T) {
	// Characterization of the decoder the presence check has to match: these
	// spellings populate Plan.Action / Observed / Existing, so they are not
	// unknown fields. strings.ToLower is not the same relation (long s).
	if strings.ToLower("obſerved") == "observed" || strings.ToLower("exiſting") == "existing" {
		t.Fatal("this Go's strings.ToLower already maps long s; the EqualFold comment would be stale")
	}
	cases := []struct {
		raw  string
		want func(*Plan) bool
	}{
		{`{"Action":"noop"}`, func(p *Plan) bool { return p.Action == ActionNoop }},
		{`{"ACTION":"noop"}`, func(p *Plan) bool { return p.Action == ActionNoop }},
		{`{"aCtIoN":"noop"}`, func(p *Plan) bool { return p.Action == ActionNoop }},
		{`{"\u0041ction":"noop"}`, func(p *Plan) bool { return p.Action == ActionNoop }},
		{`{"ob\u017ferved":["x"]}`, func(p *Plan) bool { return len(p.Observed) == 1 && p.Observed[0] == "x" }},
		{`{"exi\u017fting":{"note":"n","receipt_present":true,"executable_present":true,"release_dir":"/x"}}`, func(p *Plan) bool {
			return p.Existing != nil && p.Existing.Note == "n"
		}},
	}
	for _, tc := range cases {
		var p Plan
		if err := json.Unmarshal([]byte(tc.raw), &p); err != nil || !tc.want(&p) {
			t.Fatalf("encoding/json did not fold %s into the observation field: err=%v plan=%+v", tc.raw, err, p)
		}
	}
}

func TestReadPlanRefusesDecoderEquivalentObservationKeys(t *testing.T) {
	raw := sampleApprovalJSON(t)
	approved, err := ReadPlan(bytes.NewReader(raw))
	if err != nil || approved.Action != "" || approved.Existing != nil || len(approved.Observed) != 0 {
		t.Fatalf("untouched approval: %+v %v", approved, err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"action", "observed", "existing"} {
		if _, present := keys[k]; present {
			t.Fatalf("MarshalPlan wrote observation key %q", k)
		}
	}

	for _, tc := range observationApprovalInjections {
		t.Run(tc.name, func(t *testing.T) {
			edited, ok := injectApprovalJSON(raw, tc.field)
			if !ok {
				t.Fatal("injection anchor missing")
			}
			_, err := ReadPlan(bytes.NewReader(edited))
			if KindOf(err) != KindInvalidRequest || !strings.Contains(err.Error(), "observation field") {
				t.Fatalf("accepted decoder-equivalent observation %s: %v", tc.field, err)
			}
			if tc.name == "lowercase action" || tc.name == "unicode-escaped lowercase action" {
				if !strings.Contains(err.Error(), `observation field "action"`) {
					t.Fatalf("lowercase action diagnostic drifted: %v", err)
				}
			}
		})
	}
}
