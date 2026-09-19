// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

// TestDeployPrefersTheRegisteredRelease pins which candidate `agent deploy` prefers:
// the release `agent tool install` registered, before anything else on the host.
//
// MEASURED 2026-09-18: `agent tool install --driver grok --root R` placed a
// verified release in R; `agent deploy grok --root R` then reported the
// operator's own ~/.grok binary, because detection returns PATH candidates first
// and deploy printed found[0]. The operator was told their install was not the
// tool that would be used.
func TestDeployPrefersTheRegisteredRelease(t *testing.T) {
	cases := []struct {
		name  string
		found []toolinstall.Candidate
		want  int
	}{
		{
			name: "a registered release after an observed path",
			found: []toolinstall.Candidate{
				{Resolved: "/home/op/.grok/bin/grok", Match: toolinstall.MatchUnregisteredObserved},
				{Resolved: "/srv/tools/grok/1.0.34/bin/grok", Match: toolinstall.MatchRegistered},
			},
			want: 1,
		},
		{
			name: "only an observed path: order is unchanged",
			found: []toolinstall.Candidate{
				{Resolved: "/home/op/.grok/bin/grok", Match: toolinstall.MatchUnregisteredObserved},
				{Resolved: "/usr/local/bin/grok", Match: toolinstall.MatchUnregisteredObserved},
			},
			want: 0,
		},
		{
			// "unregistered-observed" CONTAINS "registered". A substring test here
			// would prefer exactly the candidate this function exists to reject.
			name: "unregistered-observed is not registered",
			found: []toolinstall.Candidate{
				{Resolved: "/a", Match: toolinstall.MatchUnregisteredObserved},
			},
			want: 0,
		},
		{
			// A release whose bytes did not verify is not the answer to "which
			// tool is installed".
			name: "damaged and unverified are not preferred",
			found: []toolinstall.Candidate{
				{Resolved: "/a", Match: toolinstall.MatchUnregisteredObserved},
				{Resolved: "/b", Match: toolinstall.MatchDamaged},
				{Resolved: "/c", Match: toolinstall.MatchUnverified},
			},
			want: 0,
		},
		{
			name: "manifest-corroborated is reported as found, not promoted over order",
			found: []toolinstall.Candidate{
				{Resolved: "/a", Match: toolinstall.MatchManifestCorroborated},
				{Resolved: "/b", Match: toolinstall.MatchRegistered},
			},
			want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferRegistered(tc.found); got != tc.want {
				t.Fatalf("preferRegistered = %d (%s), want %d (%s)",
					got, tc.found[got].Resolved, tc.want, tc.found[tc.want].Resolved)
			}
		})
	}
}

// TestDeployReportsTheRegisteredReleaseInItsPanel proves the registered-release
// preference through the panel an operator actually reads, not only through the
// helper.
func TestDeployReportsTheRegisteredReleaseInItsPanel(t *testing.T) {
	found := []toolinstall.Candidate{
		{Resolved: "/home/op/.grok/bin/grok", Match: toolinstall.MatchUnregisteredObserved},
		{Resolved: "/srv/tools/grok/1.0.34-linux-x86_64/bin/grok", Match: toolinstall.MatchRegistered},
	}
	var b bytes.Buffer
	if err := printDeploy(&b, "grok", found,
		map[string]any{"profile_ref": "ppf_01"},
		deployHomes{config: "/home/op/.grok", user: "/home/op"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.Contains(out, "installed: /srv/tools/grok/1.0.34-linux-x86_64/bin/grok (registered)") {
		t.Fatalf("the panel did not name the registered release:\n%s", out)
	}
	if strings.Contains(out, "installed: /home/op/.grok/bin/grok") {
		t.Fatalf("the panel still names the observed path as the installed tool:\n%s", out)
	}
	if !strings.Contains(out, "1 more candidate(s)") {
		t.Fatalf("the panel stopped saying that other candidates exist:\n%s", out)
	}
}

// TestDeployWithoutAProviderPointsAtTheSession pins the second half of the
// auth-source contract: after a deploy without --provider the next step is the
// session, not a bind.
//
// The profile deploy creates without --provider authenticates from the driver's
// own configuration home, so it IS launchable. Naming `provider bind` as the next
// step said the opposite — and in the measured walk `provider bind` could not
// have repaired the profile either, because binding requires managed_injection.
func TestDeployWithoutAProviderPointsAtTheSession(t *testing.T) {
	var b bytes.Buffer
	if err := printDeploy(&b, "grok", nil,
		map[string]any{"profile_ref": "ppf_01"},
		deployHomes{config: "/home/op/.grok", user: "/home/op"}); err != nil {
		t.Fatal(err)
	}
	// With no candidate the next step is the install; that branch is unchanged.
	if !strings.Contains(b.String(), "olivares agent tool install --driver grok") {
		t.Fatalf("the not-installed branch changed:\n%s", b.String())
	}

	b.Reset()
	if err := printDeploy(&b, "grok",
		[]toolinstall.Candidate{{Resolved: "/srv/grok", Match: toolinstall.MatchRegistered}},
		map[string]any{"profile_ref": "ppf_01"},
		deployHomes{config: "/home/op/.grok", user: "/home/op"}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	// One spelling for every next step in this binary: the renderer's `next:`.
	at := strings.Index(out, "\nnext:")
	if at < 0 {
		t.Fatalf("the panel names no next step at all:\n%s", out)
	}
	next := out[at:]
	if !strings.HasPrefix(next, "\nnext: olivares agent session create --provider-profile ppf_01") {
		t.Fatalf("the next step is not the session:\n%s", next)
	}
	if !strings.Contains(next, "olivares provider bind <provider-ref> --profile ppf_01") {
		t.Fatalf("binding a product-owned credential is no longer offered at all:\n%s", next)
	}
}

// TestDeployDeclaresAnAuthSource pins the first half of the auth-source contract, at
// the wire: the profile `agent deploy` registers names its `auth_source` in the body.
//
// MEASURED 2026-09-18 on a clean machine: `olivares agent deploy grok` created a
// profile, reported it and exited 0, and that profile could never launch —
// "set auth_source to provider_account_home or managed_injection". The home was
// then occupied (409 from `agent profile create`) and there is no
// `agent profile update` or `rm`, so the shortest documented path to a first
// session ended in a state the CLI could not leave.
func TestDeployDeclaresAnAuthSource(t *testing.T) {
	const profileJSON = `{"profile_ref":"ppf_fixture","driver":"grok","auth_source":"provider_account_home"}`

	t.Run("without --provider it declares provider_account_home", func(t *testing.T) {
		p := newProviderProbeServer(t, http.StatusCreated, profileJSON)
		home := t.TempDir()
		configHome := filepath.Join(home, ".grok")
		if err := os.MkdirAll(configHome, 0o700); err != nil {
			t.Fatal(err)
		}
		_, errb, err := execRoot(t, "agent", "deploy", "grok",
			"--config-home", configHome, "--user-home", home,
			"--root", filepath.Join(t.TempDir(), "tools"),
			"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a")
		if err != nil {
			t.Fatalf("deploy must succeed against 201: %v stderr=%s", err, errb)
		}
		var body map[string]any
		if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
			t.Fatal(uerr)
		}
		// The value is not a new decision: deploy's own help says the no-provider
		// case means "the host's own credential variables decide".
		if body["auth_source"] != "provider_account_home" {
			t.Fatalf("auth_source=%v, want provider_account_home; a profile with none cannot launch", body["auth_source"])
		}
		if _, bound := body["provider_record_ref"]; bound {
			t.Fatalf("no credential was named, so none may be bound: %v", body)
		}
	})

	t.Run("with --provider it still declares managed_injection", func(t *testing.T) {
		p := newProviderProbeServer(t, http.StatusCreated, profileJSON)
		home := t.TempDir()
		configHome := filepath.Join(home, ".grok")
		if err := os.MkdirAll(configHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, errb, err := execRoot(t, "agent", "deploy", "grok",
			"--config-home", configHome, "--user-home", home,
			"--root", filepath.Join(t.TempDir(), "tools"),
			"--provider", "prv_01",
			"--server", p.URL, "--token", "test-token", "--tenant", "tenant-a"); err != nil {
			t.Fatalf("deploy --provider must succeed against 201: %v stderr=%s", err, errb)
		}
		var body map[string]any
		if uerr := json.Unmarshal([]byte(p.lastBody()), &body); uerr != nil {
			t.Fatal(uerr)
		}
		if body["auth_source"] != "managed_injection" || body["provider_record_ref"] != "prv_01" {
			t.Fatalf("binding a credential must still say managed_injection: %v", body)
		}
	})
}
