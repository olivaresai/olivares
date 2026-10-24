// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// A Community build with a Business license names the one next step for the way THIS
// installation was installed, and offers it as one action where the engine can take it.

func TestInstallKindFollowsHowTheEngineWasInstalled(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		container bool
		k8s       string
		pkg       bool
		want      string
	}{
		{name: "a tarball or a copied binary", want: installBinary},
		{name: "a docker container", container: true, want: installContainer},
		{name: "a kubernetes pod", k8s: "10.0.0.1", want: installKubernetes},
		{name: "the package-owned binary", pkg: true, want: installPackage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous, previousPkg := inContainer, installedPackageBinary
			t.Cleanup(func() { inContainer, installedPackageBinary = previous, previousPkg })
			inContainer = func() bool { return tc.container }
			installedPackageBinary = "/nowhere/olivares"
			if tc.pkg {
				installedPackageBinary = exe
			}
			t.Setenv("KUBERNETES_SERVICE_HOST", tc.k8s)
			if got := installKind(); got != tc.want {
				t.Fatalf("installKind() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEditionSwitchStepNamesTheCommandOfEachInstallation(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		command string
		console bool
	}{
		{installBinary, "olivares upgrade --edition business", true},
		{installContainer, "docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --edition business --yes", true},
		{installPackage, "sudo --preserve-env=OLIVARES_UPGRADE_TOKEN olivares upgrade --edition business --target /usr/bin/olivares --data-dir /var/lib/olivares", false},
		{installKubernetes, "", false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			got := editionSwitchStep(tc.kind, false)
			if got.Install != tc.kind || got.Command != tc.command || got.ConsoleAction != tc.console {
				t.Fatalf("step = %+v, want command %q console=%v", got, tc.command, tc.console)
			}
			if got.Then == "" {
				t.Fatal("the step does not say what follows the swap")
			}
		})
	}
}

// writeConnectedState puts a connected-client state in dataDir, as `license connect start` leaves it.
func writeConnectedState(t *testing.T, dataDir string, st connectState) {
	t.Helper()
	st.Schema = connectStateSchema
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(connectDir(dataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connectDir(dataDir), connectStateFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func connectedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeConnectedState(t, dir, connectState{Binding: &connectBinding{DeploymentID: "dep_1", Status: "active"}})
	return dir
}

// Connected means an approved identity that can fetch a token, not a file that exists:
// `license connect start` writes the state before the owner approves.
func TestIsConnectedNeedsAnApprovedIdentity(t *testing.T) {
	approved := connectedDir(t)
	pending := t.TempDir()
	writeConnectedState(t, pending, connectState{Request: &connectApprovalRequest{Operation: "bind", RequestID: "req_1"}})
	deleted := t.TempDir()
	writeConnectedState(t, deleted, connectState{Binding: &connectBinding{DeploymentID: "dep_1", Status: "deleted"}})
	corrupt := t.TempDir()
	if err := os.MkdirAll(connectDir(corrupt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(connectDir(corrupt), connectStateFileName), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir string
		want      bool
	}{
		{"an approved binding", approved, true},
		{"an approval still pending", pending, false},
		{"a deleted binding", deleted, false},
		{"a state that is not JSON", corrupt, false},
		{"no state at all", t.TempDir(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isConnected(tc.dir); got != tc.want {
				t.Fatalf("isConnected = %v, want %v", got, tc.want)
			}
		})
	}
}

// The step says where the download token comes from only while there is none to present.
func TestEditionSwitchStepNamesWhereTheDownloadTokenComes(t *testing.T) {
	connected := connectedDir(t)
	pending := t.TempDir()
	writeConnectedState(t, pending, connectState{Request: &connectApprovalRequest{Operation: "bind", RequestID: "req_1"}})
	for _, tc := range []struct {
		name, dataDir, env string
		want               bool
	}{
		{"neither a token nor a connection", t.TempDir(), "", true},
		{"a token in the environment", t.TempDir(), "tkn", false},
		{"a connected identity", connected, "", false},
		{"a connection still waiting for approval", pending, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OLIVARES_UPGRADE_TOKEN", tc.env)
			if got := downloadTokenNeeded(tc.dataDir); got != tc.want {
				t.Fatalf("downloadTokenNeeded = %v, want %v", got, tc.want)
			}
		})
	}
	with, without := editionSwitchStep(installBinary, true), editionSwitchStep(installBinary, false)
	for _, want := range []string{"OLIVARES_UPGRADE_TOKEN", "olivares license connect start", "licenses.olivares.ai"} {
		if !strings.Contains(with.Then, want) || strings.Contains(without.Then, want) {
			t.Errorf("the token sentence (%q) must be in the step that needs it and only there:\nwith: %s\nwithout: %s", want, with.Then, without.Then)
		}
	}
	if k := editionSwitchStep(installKubernetes, true); strings.Contains(k.Then, "OLIVARES_UPGRADE_TOKEN") {
		t.Errorf("a pod runs no command, so it needs no token sentence: %s", k.Then)
	}
	// A button the apply would refuse is no button: without a token the console shows the command.
	if with.ConsoleAction || !without.ConsoleAction {
		t.Errorf("console_action must be true only where the engine can present a token: with=%v without=%v", with.ConsoleAction, without.ConsoleAction)
	}
}

// licensedService is a Community engine with a license for profile, installed as kind, whose
// environment holds a download token: the case in which the console can take the step itself.
func licensedService(t *testing.T, f *fakeUpgrade, kind, profile string) updateService {
	t.Helper()
	t.Setenv("OLIVARES_UPGRADE_TOKEN", "tkn")
	svc := newTestUpdateService(t, f, func(string) error { return nil })
	svc.dataDir = t.TempDir()
	svc.installKind = func() string { return kind }
	svc.licensed = func() string { return profile }
	return svc
}

func TestUpdateOnACommunityBuildWithABusinessLicenseTakesTheBusinessBuild(t *testing.T) {
	doc := availableDoc()
	doc.Current, doc.Available, doc.Action = "0.3", "0.3", upgradeActionInstalled
	f := &fakeUpgrade{doc: doc}
	svc := licensedService(t, f, installContainer, "business")
	_, svc.settings = bootForSettings(t)

	got, err := svc.UpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(f.calls[0], "--edition"); i < 0 || f.calls[0][i+1] != "business" || !slices.Contains(f.calls[0], "--check") || slices.Contains(f.calls[0], "--connect") {
		t.Fatalf("status ran %v, want `upgrade --edition business --check` with no --connect (nothing is connected)", f.calls[0])
	}
	want := &api.EditionSwitchDTO{To: "business", Install: installContainer,
		Command: "docker compose exec -e OLIVARES_UPGRADE_TOKEN olivares olivares upgrade --edition business --yes",
		Then:    editionSwitchStep(installContainer, false).Then, ConsoleAction: true}
	if got.EditionSwitch == nil || *got.EditionSwitch != *want {
		t.Fatalf("edition_switch = %+v, want %+v", got.EditionSwitch, want)
	}

	if _, err := svc.ApplyUpdate(context.Background(), operator(t)); err != nil {
		t.Fatal(err)
	}
	apply := f.calls[len(f.calls)-1]
	if i := slices.Index(apply, "--edition"); i < 0 || apply[i+1] != "business" || !slices.Contains(apply, "--yes") {
		t.Fatalf("apply ran %v, want `upgrade --edition business --yes`", apply)
	}
}

func TestUpdateKeepsItsRouteWhenThereIsNoEditionToMoveTo(t *testing.T) {
	for _, tc := range []struct {
		name, edition, profile string
	}{
		{"community without a license", "community", ""},
		{"a Business build with a license", "enterprise", "business"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeUpgrade{doc: availableDoc()}
			svc := licensedService(t, f, installBinary, tc.profile)
			svc.edition = tc.edition
			got, err := svc.UpdateStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.EditionSwitch != nil || slices.Contains(f.calls[0], "--edition") {
				t.Fatalf("edition_switch = %+v, args %v: nothing to move to", got.EditionSwitch, f.calls[0])
			}
		})
	}
}

func TestEnterpriseLicenseNamesTheSameBuild(t *testing.T) {
	f := &fakeUpgrade{doc: availableDoc()}
	got, err := licensedService(t, f, installBinary, "enterprise").UpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.EditionSwitch == nil || got.EditionSwitch.To != "enterprise" || !strings.Contains(got.EditionSwitch.Command, "--edition business") {
		t.Fatalf("edition_switch = %+v, want Enterprise on the one licensed build", got.EditionSwitch)
	}
}

func TestCommercialEditionReadsTheLiveLicense(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    api.LicenseDisplayInfo
		want string
	}{
		{"none", api.LicenseDisplayInfo{Status: "none"}, ""},
		{"expired", api.LicenseDisplayInfo{Status: "expired", SupportProfile: "business"}, ""},
		{"invalid", api.LicenseDisplayInfo{Status: "invalid"}, ""},
		{"valid business", api.LicenseDisplayInfo{Status: "valid", SupportProfile: "business"}, "business"},
		{"valid enterprise by plan", api.LicenseDisplayInfo{Status: "valid", Plan: "Enterprise"}, "enterprise"},
		{"valid, plan not named", api.LicenseDisplayInfo{Status: "valid", Plan: "pro"}, "business"},
		{"perpetual", api.LicenseDisplayInfo{Status: "perpetual"}, "business"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commercialEdition(tc.d); got != tc.want {
				t.Fatalf("commercialEdition(%+v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

// OLIVARES_UPDATE_ENDPOINT names the public update channel, and the licensed download sends its
// token to the endpoint it is given. Only a connected installation (whose state binds the origin
// and refuses another) may name one on the licensed route. A Business build has no other route, so
// nothing runs and the owner is told; a Community build with a license keeps checking and installing
// from its mirror, as one without a license does (TestCommunityWithALicenseKeepsItsPublicChannel...).
func TestLicensedUpdateSendsNoTokenToThePublicEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, edition string
		connected     bool
	}{
		{"a Business build, not connected", "enterprise", false},
		{"a Community build with a license, connected", "community", true},
		{"a Business build, connected", "enterprise", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeUpgrade{doc: availableDoc()}
			svc := licensedService(t, f, installBinary, "business")
			svc.edition = tc.edition
			if tc.connected {
				svc.dataDir = connectedDir(t)
			}
			svc.getenv = func(k string) string {
				if k == "OLIVARES_UPDATE_ENDPOINT" {
					return "https://mirror.example/olivares"
				}
				return ""
			}
			got, err := svc.UpdateStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if tc.connected {
				args := f.calls[0]
				if !slices.Contains(args, "--endpoint") || !slices.Contains(args, "--connect") {
					t.Fatalf("a connected installation must keep its endpoint and its connection: %v", args)
				}
				return
			}
			if len(f.calls) != 0 || !strings.Contains(got.CheckError, "OLIVARES_UPDATE_ENDPOINT") || !strings.Contains(got.CheckError, "license connect start") {
				t.Fatalf("calls=%v check_error=%q: nothing may run, and the owner is told what to do", f.calls, got.CheckError)
			}
			if _, err := svc.ApplyUpdate(context.Background(), operator(t)); !errors.Is(err, api.ErrUpdateRefused) || len(f.calls) != 0 {
				t.Fatalf("apply = %v, calls %v: the install must be refused before any run", err, f.calls)
			}
		})
	}
}

// With no download token to present, the console cannot take the step: it shows the command and
// where the token comes from, and the apply stays the public update instead of asking the licensed
// download for a token it does not have.
func TestSwitchWithoutADownloadTokenOffersNoButton(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     string
		connected bool
		button    bool
	}{
		{"neither a token nor a connection", "", false, false},
		{"a token in the engine's environment", "tkn", false, true},
		{"a connected identity", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeUpgrade{doc: availableDoc()}
			svc := licensedService(t, f, installBinary, "business")
			t.Setenv("OLIVARES_UPGRADE_TOKEN", tc.token)
			if tc.connected {
				svc.dataDir = connectedDir(t)
			}
			got, err := svc.UpdateStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			sw := got.EditionSwitch
			if sw == nil || sw.ConsoleAction != tc.button {
				t.Fatalf("edition_switch = %+v, want console_action=%v", sw, tc.button)
			}
			if tc.button {
				return
			}
			if !strings.Contains(sw.Then, "OLIVARES_UPGRADE_TOKEN") || !strings.Contains(sw.Then, "license connect start") || sw.Command == "" {
				t.Fatalf("the step must carry the command and where the token comes from: %+v", sw)
			}
			runs := len(f.calls)
			if _, err = svc.ApplyUpdate(context.Background(), operator(t)); err != nil || len(f.calls) != runs+1 {
				t.Fatalf("apply = %v, calls %v: want one public `upgrade --yes`", err, f.calls[runs:])
			}
			if apply := f.calls[runs]; slices.Contains(apply, "--edition") || slices.Contains(apply, "--connect") || !slices.Contains(apply, "--yes") {
				t.Fatalf("apply ran %v: without a token it must not take the licensed route", apply)
			}
		})
	}
}

// A package-owned file and a pod are switched where the owner runs the printed step, never by the
// engine, token or not: their apply is the public update (TestCommunityWithALicenseKeepsItsPublicChannel...).

// Every command the step prints is one `olivares upgrade` accepts: the words are written once, in
// editionswitch.go, and a flag renamed there must break here and not in an owner's terminal.
func TestEveryPrintedStepIsAnUpgradeTheFlagsAccept(t *testing.T) {
	for _, kind := range []string{installBinary, installContainer, installPackage} {
		t.Run(kind, func(t *testing.T) {
			fields := strings.Fields(editionSwitchStep(kind, false).Command)
			i := slices.Index(fields, "upgrade")
			if i < 0 {
				t.Fatalf("%v is not an `olivares upgrade` command", fields)
			}
			up := newUpgradeCmd()
			if err := up.ParseFlags(fields[i+1:]); err != nil {
				t.Fatalf("`upgrade` rejects its own printed step %q: %v", fields[i:], err)
			}
			if got, _ := up.Flags().GetString("edition"); got != "business" {
				t.Fatalf("the printed step does not select the licensed edition: --edition=%q", got)
			}
		})
	}
}

func TestEditionSwitchNoteNamesTheEditionAndOnlyAnExistingCommand(t *testing.T) {
	ent := editionSwitchNote(withEdition(editionSwitchStep(installBinary, false), "enterprise"))
	if !strings.Contains(ent, "Enterprise edition") || strings.Contains(ent, "Business edition") {
		t.Errorf("an Enterprise license must be named Enterprise:\n%s", ent)
	}
	pod := editionSwitchNote(withEdition(editionSwitchStep(installKubernetes, false), "business"))
	if strings.Contains(pod, "Next step") || !strings.Contains(pod, "roll the Business image") {
		t.Errorf("a pod has no command to print:\n%s", pod)
	}
}

// isConnected reads the state the way `license connect` does, so a state that command would refuse
// (open to others, a symlink, an unknown schema or field) is not a connection.
func TestIsConnectedRefusesWhatLicenseConnectRefuses(t *testing.T) {
	write := func(t *testing.T, dir, body string, mode os.FileMode) string {
		t.Helper()
		if err := os.MkdirAll(connectDir(dir), 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(connectDir(dir), connectStateFileName)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	active := `"binding":{"deployment_id":"dep_1","status":"active"}`
	schema := `"schema":"` + connectStateSchema + `"`
	for _, tc := range []struct {
		name string
		prep func(t *testing.T) string
	}{
		{"readable by others", func(t *testing.T) string {
			d := t.TempDir()
			p := write(t, d, `{`+schema+`,`+active+`}`, 0o600)
			if err := os.Chmod(p, 0o644); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"a symlink", func(t *testing.T) string {
			d := t.TempDir()
			real := write(t, t.TempDir(), `{`+schema+`,`+active+`}`, 0o600)
			if err := os.MkdirAll(connectDir(d), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(connectDir(d), connectStateFileName)); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"another schema", func(t *testing.T) string {
			d := t.TempDir()
			write(t, d, `{"schema":"other.v9",`+active+`}`, 0o600)
			return d
		}},
		{"a field the schema does not have", func(t *testing.T) string {
			d := t.TempDir()
			write(t, d, `{`+schema+`,"surprise":1,`+active+`}`, 0o600)
			return d
		}},
		{"larger than the state can be", func(t *testing.T) string {
			d := t.TempDir()
			write(t, d, `{`+schema+`,`+active+`,"pad":"`+strings.Repeat("x", int(connectStateMaxBytes))+`"}`, 0o600)
			return d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if isConnected(tc.prep(t)) {
				t.Fatal("isConnected accepted a state `license connect` would refuse")
			}
		})
	}
	// The control: the same body, well formed, is a connection.
	d := t.TempDir()
	write(t, d, `{`+schema+`,`+active+`}`, 0o600)
	if !isConnected(d) {
		t.Fatal("the control state is not read as connected: the cases above prove nothing")
	}
}

// The public update channel is never given the licensed token's route, whatever the endpoint.
func TestCommunityWithoutALicenseKeepsItsEndpoint(t *testing.T) {
	f := &fakeUpgrade{doc: availableDoc()}
	svc := newTestUpdateService(t, f, nil)
	svc.getenv = func(k string) string {
		if k == "OLIVARES_UPDATE_ENDPOINT" {
			return "http://mirror.example/olivares"
		}
		return ""
	}
	if _, err := svc.UpdateStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.calls[0], "--endpoint") {
		t.Fatalf("the public channel lost its mirror: %v", f.calls[0])
	}
}

// The licensed build has the version of Community, so an install nobody restarted shows only in
// the edition the on-disk binary reports.
func TestAnEditionOnlySwapIsSeenAsWaitingForARestart(t *testing.T) {
	doc := availableDoc()
	doc.Current, doc.Available = "0.2", "0.2"
	doc.Status, doc.EditionCurrent, doc.EditionAvailable = upgradeStatusUpToDate, "enterprise", "enterprise"

	t.Run("status", func(t *testing.T) {
		got, err := licensedService(t, &fakeUpgrade{doc: doc}, installBinary, "business").UpdateStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !got.RestartRequired {
			t.Fatalf("status = %+v, want restart_required for a swapped edition at the same version", got)
		}
	})

	t.Run("apply restarts the engine on what is installed", func(t *testing.T) {
		restarts := 0
		f := &fakeUpgrade{doc: doc}
		svc := licensedService(t, f, installBinary, "business")
		_, svc.settings = bootForSettings(t)
		svc.restart = func(string) error { restarts++; return nil }
		got, err := svc.ApplyUpdate(context.Background(), operator(t))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Restarting || restarts != 1 {
			t.Fatalf("restarting=%v restarts=%d, want one restart onto the installed Business build", got.Restarting, restarts)
		}
	})

	t.Run("a run that dies after the swap is not called refused", func(t *testing.T) {
		restarts := 0
		f := &fakeUpgrade{doc: doc, applyErr: context.DeadlineExceeded}
		svc := licensedService(t, f, installBinary, "business")
		svc.restart = func(string) error { restarts++; return nil }
		_, p := bootForSettings(t)
		svc.settings = p
		got, err := svc.ApplyUpdate(context.Background(), operator(t))
		if err != nil {
			t.Fatalf("an installed swap was reported as %v", err)
		}
		if !got.Restarting || restarts != 1 {
			t.Fatalf("restarting=%v restarts=%d", got.Restarting, restarts)
		}
	})

	// The route's published behavior: an apply that installed nothing restarts the engine only for
	// the edition switch above. A newer version already on disk is shown as restart_required, and
	// stays the administrator's restart, not a side effect of a click that says "already current".
	t.Run("a newer version on disk is not restarted onto by an apply that installed nothing", func(t *testing.T) {
		d := availableDoc()
		d.Current, d.Status, d.EditionCurrent = "0.9", upgradeStatusUpToDate, "community"
		restarts := 0
		svc := newTestUpdateService(t, &fakeUpgrade{doc: d}, func(string) error { restarts++; return nil })
		got, err := svc.ApplyUpdate(context.Background(), operator(t))
		if err != nil {
			t.Fatal(err)
		}
		if got.Restarting || restarts != 0 {
			t.Fatalf("restarting=%v restarts=%d: nothing was installed, so nothing restarts", got.Restarting, restarts)
		}
	})

	t.Run("a Business engine on the Business build is not waiting", func(t *testing.T) {
		svc := licensedService(t, &fakeUpgrade{doc: doc}, installBinary, "business")
		svc.edition = "enterprise"
		got, err := svc.UpdateStatus(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.RestartRequired {
			t.Fatalf("status = %+v: the engine already runs the edition on disk", got)
		}
	})
}
