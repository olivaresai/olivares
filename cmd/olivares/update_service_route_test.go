// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A Community build with a license takes the licensed download only where it can run. Where it
// cannot (no token and no connection, an own mirror that is not connected, a package or a pod),
// the console keeps checking and installing from the public channel exactly as an unlicensed
// Community build does, so a security release is never held back by a switch nobody can take.
func TestCommunityWithALicenseKeepsItsPublicChannelWhereTheSwitchCannotRun(t *testing.T) {
	const mirror = "https://mirror.example/olivares"
	for _, tc := range []struct {
		name, token, endpoint, kind string
	}{
		{"license install left no token and no connection", "", "", installBinary},
		{"an own mirror, no token", "", mirror, installBinary},
		{"an own mirror, a token, not connected", "tkn", mirror, installBinary},
		{"a package install", "tkn", "", installPackage},
		{"a pod", "tkn", "", installKubernetes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := availableDoc()
			doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
			f := &fakeUpgrade{doc: doc}
			restarts := 0
			svc := licensedService(t, f, tc.kind, "business")
			t.Setenv("OLIVARES_UPGRADE_TOKEN", tc.token)
			_, svc.settings = bootForSettings(t)
			svc.restart = func(string) error { restarts++; return nil }
			svc.getenv = func(k string) string {
				if k == "OLIVARES_UPDATE_ENDPOINT" {
					return tc.endpoint
				}
				return ""
			}

			got, err := svc.UpdateStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(f.calls) == 0 {
				t.Fatalf("the check ran nothing; status = %+v, want the public channel checked (endpoint %q)", got, tc.endpoint)
			}
			check := f.calls[0]
			if slices.Contains(check, "--edition") || slices.Contains(check, "--connect") || slices.Contains(check, "--enterprise") ||
				slices.Contains(check, "--endpoint") != (tc.endpoint != "") {
				t.Fatalf("the check ran %v, want the public channel (endpoint %q)", check, tc.endpoint)
			}
			if got.CheckError != "" || !got.UpdateAvailable || !got.Security {
				t.Fatalf("status = %+v, want the pending security release", got)
			}
			if sw := got.EditionSwitch; sw == nil || sw.ConsoleAction {
				t.Fatalf("edition_switch = %+v, want the step shown without a button", sw)
			}

			if _, err := svc.ApplyUpdate(context.Background(), operator(t)); err != nil {
				t.Fatalf("the public update was refused: %v", err)
			}
			apply := f.calls[len(f.calls)-1]
			if slices.Contains(apply, "--edition") || slices.Contains(apply, "--connect") || !slices.Contains(apply, "--yes") || restarts != 1 {
				t.Fatalf("apply ran %v, restarts %d, want `upgrade --yes` on the public channel and one restart", apply, restarts)
			}
		})
	}
}

// A button the engine cannot honour is no button: with an own mirror that is not connected the
// licensed download would send its token there, so the step carries the command and why.
func TestSwitchNamesTheMirrorThatBlocksIt(t *testing.T) {
	f := &fakeUpgrade{doc: availableDoc()}
	svc := licensedService(t, f, installBinary, "business") // holds a token
	svc.getenv = func(k string) string {
		if k == "OLIVARES_UPDATE_ENDPOINT" {
			return "https://mirror.example/olivares"
		}
		return ""
	}
	sw := svc.editionSwitch()
	if sw == nil || sw.ConsoleAction || sw.Command == "" || !strings.Contains(sw.Then, "OLIVARES_UPDATE_ENDPOINT") {
		t.Fatalf("edition_switch = %+v, want the command without a button, and the mirror named", sw)
	}
	svc.dataDir = connectedDir(t)
	if sw := svc.editionSwitch(); sw == nil || !sw.ConsoleAction {
		t.Fatalf("a connected installation binds the origin, so the button is back: %+v", sw)
	}
}

// A Business engine whose file a package upgrade put back to Community has not installed
// anything: the Install the package guard refused must stay the refusal, not become a restart.
func TestABusinessEngineNeverReadsACommunityFileAsAnInstall(t *testing.T) {
	doc := availableDoc()
	doc.Current, doc.Status, doc.EditionCurrent = "0.2", upgradeStatusUpToDate, "community"
	svc := licensedService(t, &fakeUpgrade{doc: doc}, installPackage, "")
	svc.edition = "enterprise"
	got, err := svc.UpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.RestartRequired {
		t.Fatalf("status = %+v: a Community file under a Business engine is not an install waiting for a restart", got)
	}
	restarts := 0
	svc.restart = func(string) error { restarts++; return nil }
	if got, err := svc.ApplyUpdate(context.Background(), operator(t)); err != nil || got.Restarting || restarts != 0 {
		t.Fatalf("apply = %+v, %v, restarts %d: nothing was installed, so nothing restarts", got, err, restarts)
	}
}

// An engine that restarts onto an edition another run installed changes what it is: the ledger
// says who asked for that.
func TestRestartOntoAnEditionAnotherRunInstalledIsAudited(t *testing.T) {
	doc := availableDoc()
	doc.Current, doc.Available, doc.Status = "0.2", "0.2", upgradeStatusUpToDate
	doc.EditionCurrent, doc.EditionAvailable = "enterprise", "enterprise"
	_, p := bootForSettings(t)
	svc := licensedService(t, &fakeUpgrade{doc: doc}, installBinary, "business")
	svc.settings = p
	if _, err := svc.ApplyUpdate(context.Background(), operator(t)); err != nil {
		t.Fatal(err)
	}
	ev, ok := auditedUpdate(t, p.st)
	if !ok {
		t.Fatal("the restart onto the licensed build left no audit event")
	}
	if want := operator(t); ev.Actor != want.Actor() || ev.TargetKind != "core.engine_binary" {
		t.Fatalf("audit event = actor %q target %q, want actor %q on core.engine_binary", ev.Actor, ev.TargetKind, want.Actor())
	}
}

// The licensed route stays the licensed route where it can run: a connected Community installation
// may name its mirror (the connect state binds the origin), and the apply asks for the Business build
// over that connection, never for the public one.
func TestConnectedCommunityWithAMirrorAppliesTheLicensedDownload(t *testing.T) {
	const mirror = "https://mirror.example/olivares"
	doc := availableDoc()
	doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
	f := &fakeUpgrade{doc: doc}
	svc := licensedService(t, f, installBinary, "business")
	svc.dataDir = connectedDir(t)
	_, svc.settings = bootForSettings(t)
	svc.getenv = func(k string) string {
		if k == "OLIVARES_UPDATE_ENDPOINT" {
			return mirror
		}
		return ""
	}
	if _, err := svc.ApplyUpdate(context.Background(), operator(t)); err != nil {
		t.Fatal(err)
	}
	apply := f.calls[len(f.calls)-1]
	for _, want := range []string{"--endpoint", mirror, "--edition", "business", "--connect", "--yes"} {
		if !slices.Contains(apply, want) {
			t.Fatalf("apply ran %v, want the licensed download over the connection (missing %q)", apply, want)
		}
	}
	if slices.Contains(apply, "--enterprise") {
		t.Fatalf("apply ran %v: a Community engine switches with --edition, not --enterprise", apply)
	}
}

// A license that entitles nothing (expired, revoked) leaves the Community update on its public route
// when applied, not only when checked.
func TestApplyWithoutALiveLicenseKeepsThePublicChannel(t *testing.T) {
	doc := availableDoc()
	doc.Action, doc.Installed = upgradeActionInstalled, "olivares 0.3 (commit abc, built now)"
	f := &fakeUpgrade{doc: doc}
	svc := licensedService(t, f, installBinary, "") // the live license entitles no edition
	_, svc.settings = bootForSettings(t)
	if _, err := svc.ApplyUpdate(context.Background(), operator(t)); err != nil {
		t.Fatal(err)
	}
	if apply := f.calls[len(f.calls)-1]; slices.Contains(apply, "--edition") || slices.Contains(apply, "--connect") || !slices.Contains(apply, "--yes") {
		t.Fatalf("apply ran %v, want the public `upgrade --yes`", apply)
	}
}

// A target that did not say which edition it is cannot show a switch, so it never reads as one
// waiting for a restart.
func TestAnUnknownEditionOnDiskIsNotASwitchWaitingForARestart(t *testing.T) {
	for _, ed := range []string{"", "unknown"} {
		doc := availableDoc()
		doc.Current, doc.Status, doc.EditionCurrent = "0.2", upgradeStatusUpToDate, ed
		got, err := licensedService(t, &fakeUpgrade{doc: doc}, installBinary, "business").UpdateStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.RestartRequired {
			t.Fatalf("edition_current %q: status = %+v, want no restart", ed, got)
		}
	}
}

// An unusable connect state is named in the step, not read as "not connected" with advice to run
// `license connect start` that would meet the same refusal; a state that is merely pending is not.
func TestSwitchStepNamesAnUnusableConnectState(t *testing.T) {
	t.Setenv("OLIVARES_UPGRADE_TOKEN", "")
	dir := t.TempDir()
	if err := os.MkdirAll(connectDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := switchStepFor(installBinary, dir); strings.Contains(got.Then, "connected-client state") {
		t.Fatalf("no state at all must not be blamed: %q", got.Then)
	}
	if err := os.WriteFile(filepath.Join(connectDir(dir), connectStateFileName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := switchStepFor(installBinary, dir)
	if !strings.Contains(got.Then, "connected-client state") || !strings.Contains(got.Then, connectStateFileName) || got.ConsoleAction {
		t.Fatalf("step = %+v, want the state named with its path and no button", got)
	}
}
