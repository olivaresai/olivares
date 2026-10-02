// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

func TestSessionTemplatePeerRuleDefaultsAtLaunchAndPreservesRunChoicesOnResume(t *testing.T) {
	f, _, templateID := newSessionPeersFixture(t, true)
	setTemplate := func(body map[string]any) {
		t.Helper()
		got := f.h.doJSON("PUT", "/v1/m/sessions/templates/"+templateID, f.admin, map[string]any{"body": body}, tenantHdr(f.tenant))
		if got.code != http.StatusOK {
			t.Fatalf("template peer option = %d %s", got.code, got.raw)
		}
	}
	readRule := func(run, rule string, wantPeers []string) {
		t.Helper()
		got := f.h.do("GET", "/v1/m/sessions/runs/"+run, f.admin, tenantHdr(f.tenant))
		if got.code != http.StatusOK {
			t.Fatalf("read run peers = %d %s", got.code, got.raw)
		}
		if (rule == "" && got.body["peers_rule"] != nil) || (rule != "" && got.body["peers_rule"] != rule) {
			t.Fatalf("run peer rule = %v, want %q", got.body["peers_rule"], rule)
		}
		peers, ok := got.body["peers"].([]any)
		if !ok || len(peers) != len(wantPeers) {
			t.Fatalf("run peers = %v, want %v", got.body["peers"], wantPeers)
		}
		for i, sid := range wantPeers {
			if peers[i] != sid {
				t.Fatalf("run peer = %v, want %s", peers[i], sid)
			}
		}
	}
	cycle := func(run string) {
		t.Helper()
		for _, action := range []string{"stop", "resume"} {
			got := f.h.do("POST", "/v1/m/sessions/runs/"+run+"/"+action, f.admin, tenantHdr(f.tenant))
			if got.code != http.StatusOK {
				t.Fatalf("%s = %d %s", action, got.code, got.raw)
			}
		}
	}
	setTemplate(map[string]any{"peers_rule": "same-template"})
	// Editing a template does not retroactively grant peers to an older run.
	cycle(f.run)
	readRule(f.run, "", nil)
	peerSID, peerRun := newSessionPeer(t, f, templateID, "")
	_, run := newSessionPeer(t, f, templateID, "")
	readRule(run, "same-template", nil)
	readRule(peerRun, "same-template", nil)
	preview := f.h.do("POST", "/v1/m/sessions/templates/"+templateID+"/apply", f.admin, tenantHdr(f.tenant))
	merged, _ := preview.body["merged"].(map[string]any)
	if preview.code != http.StatusOK || preview.body["applied"] != true || merged["peers_rule"] != "same-template" {
		t.Fatalf("preview must show the launch default = %d %s", preview.code, preview.raw)
	}
	// A run's existing rule also survives removal of the template default.
	setTemplate(map[string]any{})
	cycle(run)
	readRule(run, "same-template", nil)
	_, withoutRule := newSessionPeer(t, f, templateID, "")
	readRule(withoutRule, "", nil)
	// Re-enabling the template must never override an operator's explicit choice,
	// including an empty list, when that same run resumes.
	setTemplate(map[string]any{"peers_rule": "same-template"})
	for _, peers := range [][]string{{}, {peerSID}} {
		chosen := f.h.doJSON("PUT", "/v1/m/sessions/runs/"+run+"/peers", f.admin, map[string]any{"peers": peers}, tenantHdr(f.tenant))
		if chosen.code != http.StatusOK {
			t.Fatalf("operator peer choice = %d %s", chosen.code, chosen.raw)
		}
		cycle(run)
		readRule(run, "", peers)
	}
	for _, invalid := range []string{"all", " same-template "} {
		body := map[string]any{"peers_rule": invalid}
		create := f.h.doJSON("POST", "/v1/m/sessions/templates", f.admin, map[string]any{"name": "Invalid peer option", "body": body}, tenantHdr(f.tenant))
		update := f.h.doJSON("PUT", "/v1/m/sessions/templates/"+templateID, f.admin, map[string]any{"body": body}, tenantHdr(f.tenant))
		if create.code != http.StatusBadRequest || update.code != http.StatusBadRequest {
			t.Fatalf("invalid peer rule must be refused at authoring: create=%d update=%d", create.code, update.code)
		}
	}
}
