// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/release"
)

// Installing a commercial license on a Community build prints the one next step for this
// installation, the same words the console shows; nothing is printed where there is nothing
// to switch to.

func TestLicenseInstallOnACommunityBuildNamesTheNextStep(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	out, err := installLicence(t, signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour)), "--data-dir", t.TempDir())
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	step := editionSwitchStep(installKind(), true)
	for _, want := range []string{
		"this installation runs the Community build",
		"olivares upgrade --edition business",
		step.Command, step.Then,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the install output does not say %q:\n%s", want, out)
		}
	}
}

func TestLicenseInstallSaysNothingToSwitchWhenThereIsNothingToSwitchTo(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edition string
		expires time.Time
	}{
		{"a Business build", "enterprise", time.Now().Add(365 * 24 * time.Hour)},
		{"an expired license", "community", time.Now().Add(-24 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asEdition(t, tc.edition)
			out, err := installLicence(t, signTestLicence(t, "Acme Ltd", tc.expires), "--data-dir", t.TempDir())
			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			if strings.Contains(out, "--edition business") {
				t.Fatalf("a switch was offered where there is none:\n%s", out)
			}
		})
	}
}

func TestLicenseStatusCarriesTheSwitchForScripts(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	dir := t.TempDir()
	if out, err := installLicence(t, signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour)), "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs([]string{"license", "status", "--data-dir", dir, "-o", "json"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatal(err)
	}
	var got struct {
		EditionSwitch struct {
			To      string `json:"to"`
			Install string `json:"install"`
			Command string `json:"command"`
		} `json:"edition_switch"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("status is not JSON: %v\n%s", err, out.String())
	}
	if got.EditionSwitch.To != "business" || !strings.Contains(got.EditionSwitch.Command, "upgrade --edition business") || got.EditionSwitch.Install == "" {
		t.Fatalf("edition_switch = %+v", got.EditionSwitch)
	}
}

func TestLicenseInstallReportCarriesTheSwitchForScripts(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	out, err := installLicence(t, signTestLicence(t, "Acme Ltd", time.Now().Add(365*24*time.Hour)), "--data-dir", t.TempDir(), "-o", "json")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	start := strings.Index(out, "{")
	var got struct {
		EditionSwitch *struct {
			To      string `json:"to"`
			Command string `json:"command"`
		} `json:"edition_switch"`
	}
	dec := json.NewDecoder(strings.NewReader(out[start:]))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, out)
	}
	if got.EditionSwitch == nil || got.EditionSwitch.To != "business" || !strings.Contains(got.EditionSwitch.Command, "--edition business") {
		t.Fatalf("report edition_switch = %+v", got.EditionSwitch)
	}
}

// A revoked license is not a license to switch on. `license status` counts the revocation list in
// its status, and the step it prints must agree with that status. The control (the same license, no
// list) proves the setup offers a switch, so the revoked case is not silent for another reason.
func TestLicenseStatusOffersNoSwitchForARevokedLicense(t *testing.T) {
	hermeticUpgradeEnv(t)
	asEdition(t, "community")
	if !license.HasDevKey {
		t.Skip("this build ships no dev signing key")
	}
	now := time.Now().UTC()
	blob, err := license.Sign(license.Claims{
		Licensee: "Acme Ltd", Plan: "business", Serial: "ser-revoked",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(365 * 24 * time.Hour),
	}, license.DevPrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if out, err := installLicence(t, blob, "--data-dir", dir); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	otaPub, otaPriv, _ := ed25519.GenerateKey(nil)
	manifest := writeCRLManifest(t, t.TempDir(), &release.RevokedSet{Serials: []string{"ser-revoked"}}, otaPriv)

	status := func(extra ...string) map[string]any {
		t.Helper()
		root := newRootCmd()
		var out strings.Builder
		root.SetOut(&out)
		root.SetArgs(append([]string{"license", "status", "--data-dir", dir, "-o", "json"}, extra...))
		if _, err := root.ExecuteC(); err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out.String()), &doc); err != nil {
			t.Fatalf("status is not JSON: %v\n%s", err, out.String())
		}
		return doc
	}
	if control := status(); control["edition_switch"] == nil {
		t.Fatalf("the control (no revocation list) offers no switch, so the revoked case proves nothing: %v", control)
	}
	revoked := status("--manifest", manifest, "--ota-pubkey", base64.StdEncoding.EncodeToString(otaPub))
	if revoked["status"] != "revoked" || revoked["edition_switch"] != nil {
		t.Fatalf("status=%v edition_switch=%v: a revoked license must print no switch", revoked["status"], revoked["edition_switch"])
	}
}

// A client signed in to an engine is told that engine's licence state, not what its own empty data
// directory holds (a 0.6 client printed `status: none` whatever the engine ran). The
// data-dir form stays for the engine host: naming --data-dir keeps the answer offline.
func TestLicenseStatusAsksTheSignedInEngine(t *testing.T) {
	var asked atomic.Int64
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/console/license" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"edition": "enterprise", "status": "valid", "source": "data-dir", "plan": "business"})
		}
	}))
	defer server.Close()
	t.Setenv("OLIVARES_DATA_DIR", t.TempDir())
	t.Setenv("OLIVARES_SERVER_URL", server.URL)
	t.Setenv("OLIVARES_TOKEN", "secret-token")

	out, _, err := execRoot(t, "license", "status")
	var got map[string]any
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got["status"] != "valid" || got["plan"] != "business" {
		t.Fatalf("license status signed in: err=%v output=%q, want the engine's valid business licence", err, out)
	}

	before := asked.Load()
	out, _, err = execRoot(t, "license", "status", "--data-dir", t.TempDir())
	if err != nil || !strings.Contains(out, `"status": "none"`) || asked.Load() != before {
		t.Fatalf("license status --data-dir must stay offline: err=%v output=%q, engine asked %d more times", err, out, asked.Load()-before)
	}

	status = http.StatusForbidden
	_, _, err = execRoot(t, "license", "show")
	if exitcode.From(err) != exitcode.Auth || err == nil || !strings.Contains(err.Error(), "superadmin") {
		t.Fatalf("a non-superadmin must be told the licence needs a superadmin, with the auth exit code: %v", err)
	}
}

// A named server is a sign-in the caller asked for. Without a credential `license status` says so
// instead of answering `none` from an empty data directory; on the host of the engine it is signed
// in to it keeps the full offline document (edition_switch, reason, claims) that only that host
// holds; and an engine that answers `null` is an error, not a panic.
func TestLicenseStatusSignedInBoundaries(t *testing.T) {
	var asked atomic.Int64
	body := "null"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	hostPort := strings.TrimPrefix(server.URL, "http://")
	cases := []struct {
		name     string
		args     []string
		env      map[string]string
		engine   bool // the data directory records server as the engine this host runs
		wantCode int
		wantOut  string
		wantAsk  int64
	}{
		{name: "--server without a credential", args: []string{"--server", server.URL},
			wantCode: exitcode.Usage},
		{name: "$OLIVARES_SERVER_URL without a credential", env: map[string]string{"OLIVARES_SERVER_URL": server.URL},
			wantCode: exitcode.Usage},
		{name: "the engine this host runs stays offline", env: map[string]string{"OLIVARES_SERVER_URL": server.URL, "OLIVARES_TOKEN": "secret-token"},
			engine: true, wantOut: `"status": "none"`},
		{name: "a null answer", env: map[string]string{"OLIVARES_SERVER_URL": server.URL, "OLIVARES_TOKEN": "secret-token"},
			wantCode: exitcode.Server, wantAsk: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := t.TempDir()
			t.Setenv("OLIVARES_DATA_DIR", data)
			t.Setenv("OLIVARES_SERVER_URL", "")
			t.Setenv("OLIVARES_TOKEN", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.engine {
				if err := os.Chmod(data, 0o700); err != nil { // localEngineFor trusts only a directory no other account can write
					t.Fatal(err)
				}
				writeConsoleFixture(t, data, consoleState{Version: consoleStateVersion, Listen: hostPort, Insecure: true})
			}
			before := asked.Load()
			out, _, err := execRoot(t, append([]string{"license", "status"}, tc.args...)...)
			got := 0
			if err != nil {
				got = exitcode.From(err)
			}
			if got != tc.wantCode {
				t.Fatalf("license status: exit %d err=%v output=%q, want exit %d", got, err, out, tc.wantCode)
			}
			if tc.wantOut != "" && !strings.Contains(out, tc.wantOut) {
				t.Fatalf("license status output %q lacks %q", out, tc.wantOut)
			}
			if asked.Load()-before != tc.wantAsk {
				t.Fatalf("the engine was asked %d times, want %d", asked.Load()-before, tc.wantAsk)
			}
		})
	}
}
