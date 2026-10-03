// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
)

// SR-FH-CACHE-01: the host observer shares the process's verified releases but checks
// with an unavailable verifier on purpose. A release the installing engine verified and
// kept must still be "unverified" to the observer, never "registered" on that verdict.
func TestHostObserverStaysUnverifiedWithAWarmVerifiedCache(t *testing.T) {
	f := newAgentToolFixture(t)
	previous := verifiedToolReleases
	verifiedToolReleases = toolinstall.NewVerifiedCache()
	t.Cleanup(func() { verifiedToolReleases = previous })
	verifier, err := toolinstall.NewGPGVerifier(context.Background(), exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	toolInstallEngine = func(context.Context) *toolinstall.Engine {
		claude := toolinstall.NewClaudeWithTrust(toolinstall.ClaudeOptions{BaseURL: f.srv.URL, Verifier: verifier, ProbeBudget: 5 * time.Second, ProbeGrace: 500 * time.Millisecond}, f.key.Public, f.key.Fingerprint)
		return toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{InstallerVersion: "test", Verified: verifiedToolReleases})
	}
	f.publish("2.1.261")
	if code, out, errOut := f.run(f.installArgs("--version", "2.1.261", "--yes", "-o", "json")...); code != 0 {
		t.Fatalf("install = %d: %s %s", code, out, errOut)
	}
	inv, err := toolInstallEngine(context.Background()).List(context.Background(), f.root)
	if err != nil || len(inv.Installed) != 1 || inv.Installed[0].State != toolinstall.StateInstalled {
		t.Fatalf("verified and kept: %+v %v", inv, err)
	}
	observer := newHostToolObserverAt(f.root, nil, filepath.Join(t.TempDir(), "empty-home"), "")
	for i := 0; i < 2; i++ {
		obs, err := observer.ObserveHostTools(context.Background(), "claude", inv.Installed[0].Executable)
		if err != nil {
			t.Fatal(err)
		}
		if len(obs.Candidates) != 1 || obs.Candidates[0].Origin != "managed" || obs.Candidates[0].Match != "unverified" {
			t.Fatalf("observation %d took the installing engine's verdict: %+v", i, obs.Candidates)
		}
	}
	if n := observer.network.attempts.Load(); n != 0 {
		t.Fatalf("observer used the network %d times", n)
	}
}
