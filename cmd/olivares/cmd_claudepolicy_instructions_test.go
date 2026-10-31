// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The instructions file of Claude Code's managed tier is the surface managed-instructions@<department>
// (#530): one document per department, rendered by the engine from what the department carries,
// so the verbs take no document for it.

func TestClaudePolicyDepartmentInstructionsReachTheirRoutes(t *testing.T) {
	for _, tc := range []struct {
		argv       []string
		wantMethod string
		wantPath   string
	}{
		{[]string{"claude-policy", "validate", "managed-instructions@eng"},
			"POST", "/v1/m/claude-policy/managed-instructions@eng/validate"},
		{[]string{"claude-policy", "dry-run", "managed-instructions@eng"},
			"POST", "/v1/m/claude-policy/managed-instructions@eng/dry-run"},
		{[]string{"claude-policy", "publish", "managed-instructions@platform-2", "--note", "Q4 standards"},
			"POST", "/v1/m/claude-policy/managed-instructions@platform-2/publish"},
		{[]string{"claude-policy", "versions", "ls", "managed-instructions@eng"},
			"GET", "/v1/m/claude-policy/managed-instructions@eng/versions"},
		{[]string{"claude-policy", "distribution", "managed-instructions@eng"},
			"GET", "/v1/m/claude-policy/managed-instructions@eng/distribution"},
	} {
		t.Run(strings.Join(tc.argv[1:3], "-"), func(t *testing.T) {
			srv := newLot3Server(t, lot3OK(`{"ok":true,"items":[],"scopes":[],"diagnostics":[],"drift_computed":true,"distribution":"distributed"}`))
			if _, _, err := execRoot(t, lot3Args(srv.URL, tc.argv...)...); err != nil {
				t.Fatalf("verb failed: %v", err)
			}
			if got, _ := srv.method.Load().(string); got != tc.wantMethod {
				t.Errorf("method = %s, want %s", got, tc.wantMethod)
			}
			if got := srv.lastPath(); got != tc.wantPath {
				t.Errorf("path = %s, want %s", got, tc.wantPath)
			}
		})
	}
}

// A department's document is rendered by the engine, so the request carries none; a document
// typed with the verb is still validated as typed.
func TestClaudePolicyDepartmentInstructionsSendNoDocumentUnlessOneIsGiven(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true,"diagnostics":[]}`))
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "validate", "managed-instructions@eng")...); err != nil {
		t.Fatal(err)
	}
	if body := srv.lastBody(); !strings.Contains(body, `"content":""`) {
		t.Errorf("body = %s, want an empty content so the engine renders the department's", body)
	}
	doc := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := os.WriteFile(doc, []byte("# Typed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "validate", "managed-instructions@eng", "--content-file", doc)...); err != nil {
		t.Fatal(err)
	}
	if body := srv.lastBody(); !strings.Contains(body, `# Typed`) {
		t.Errorf("body = %s, want the typed document", body)
	}
}

func TestClaudePolicyRefusesABadDepartmentBeforeAnyRequest(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true}`))
	for _, surface := range []string{
		"managed-instructions@Eng", "managed-instructions@", "managed-instructions@a@b", "managed-instructions@-eng",
		"managed-settings@eng", "sandbox@eng", "managed-instruction@eng",
	} {
		_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", "versions", "ls", surface)...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Errorf("%q must exit %d, got %v", surface, exitcode.Usage, err)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent for a surface that does not exist", n)
	}
}

// Every other surface still needs its document: only a department's is rendered by the engine.
func TestClaudePolicyOtherSurfacesStillNeedADocument(t *testing.T) {
	srv := newLot3Server(t, lot3OK(`{"ok":true}`))
	for _, verb := range []string{"validate", "dry-run", "publish"} {
		_, _, err := execRoot(t, lot3Args(srv.URL, "claude-policy", verb, "managed-settings")...)
		if err == nil || exitcode.From(err) != exitcode.Usage {
			t.Errorf("%s of managed-settings with no document must exit %d, got %v", verb, exitcode.Usage, err)
		}
	}
	if n := srv.calls.Load(); n != 0 {
		t.Fatalf("%d request(s) were sent without a document", n)
	}
}

// A workstation pulls its department's file to the managed tier of Claude Code and the digest on
// disk is the one the plane signed.
func TestClaudePolicyPullWritesADepartmentsInstructionsFile(t *testing.T) {
	const doc = "# Org standards\n\nUse tabs.\n"
	p := newPullPlane(t, "tenant-a", "managed-instructions@eng", 2, doc, nil)
	out := filepath.Join(t.TempDir(), "claude-code", "CLAUDE.md")

	stdout, _, err := execRoot(t, lot3Args(p.URL, "claude-policy", "pull", "managed-instructions@eng",
		"--out", out, "--scope", "ws-12", "--pin-key-fingerprint", p.fingerprint())...)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, stdout)
	}
	got, _ := os.ReadFile(out)
	sum := sha256.Sum256(got)
	if string(got) != doc || hex.EncodeToString(sum[:]) != p.artifact["artifact_sha256"] {
		t.Fatalf("file on disk = %q (sha256 %x), want the department's document with the signed digest %v", got, sum, p.artifact["artifact_sha256"])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.artifactPath != "/v1/m/claude-policy/managed-instructions@eng/artifact" {
		t.Errorf("the pull asked for %q, want the department's artifact", p.artifactPath)
	}
	if p.lastPath() != "/v1/m/claude-policy/managed-instructions@eng/checkin" || p.checkin["scope"] != "ws-12" {
		t.Errorf("the check-in went to %q as %v, want the department's surface for host ws-12", p.lastPath(), p.checkin["scope"])
	}
}
