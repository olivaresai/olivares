// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// A Business binary installs only a Business release. `upgrade` without --enterprise used
// to read the public channel, whose manifest is signed by the same OTA key and carries no
// edition, so it verified and swapped the Community binary onto a Business install.
// These tests run the real command with this binary
// reporting the Business edition; the overlay build reports the same name.

// asEdition makes thisEdition report name for one test, whichever edition the test binary
// was built as: the Community controls below must also hold in the overlay build.
func asEdition(t *testing.T, name string) {
	t.Helper()
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = name
}

// hermeticUpgradeEnv hides a license or download token the developer's shell exports, so
// the licensed route refuses for the missing license and never reaches a network.
func hermeticUpgradeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"OLIVARES_LICENSE", "OLIVARES_LICENSE_PATH", "OLIVARES_UPGRADE_TOKEN"} {
		t.Setenv(k, "")
	}
}

// ephemeralPubkey is a --pubkey value, so a test build with no embedded OTA key still
// reaches the source selection the test is about.
func ephemeralPubkey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub)
}

// unitLines reads the generated service unit and returns its ExecStart line and whether it
// has an EnvironmentFile line, so a comment in the unit cannot satisfy an assertion.
func unitLines(t *testing.T, dir string) (execStart string, hasEnvFile bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "olivares-upgrade.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			execStart = line
		case strings.HasPrefix(line, "EnvironmentFile="):
			hasEnvFile = true
		}
	}
	if execStart == "" {
		t.Fatalf("no ExecStart in the generated unit:\n%s", raw)
	}
	return execStart, hasEnvFile
}

// A Business build with no flags takes the licensed route: with no license installed it
// refuses with the license next step, on every channel.
func TestBusinessUpgradeWithoutFlagsNeverSelectsThePublicChannel(t *testing.T) {
	hermeticUpgradeEnv(t)
	for _, channel := range []string{"stable", "security"} {
		_, err := runUpgradeCmdAs(t, "enterprise", "--check", "--channel", channel,
			"--pubkey", ephemeralPubkey(t), "--data-dir", t.TempDir(), "--timeout", "3s")
		if err == nil || !strings.Contains(err.Error(), "no license installed") {
			t.Fatalf("Business `upgrade --check --channel %s`: err = %v, want the licensed route's refusal (no license installed)", channel, err)
		}
	}
}

// A unit written by an older binary carries no --enterprise. The same argv, run by a
// Business build, must refuse for the missing license and install nothing.
func TestBusinessScheduledRunFromAnOlderUnitRefuses(t *testing.T) {
	hermeticUpgradeEnv(t)
	_, err := runUpgradeCmdAs(t, "enterprise", "--channel", "stable", "--if-eligible", "--yes",
		"--pubkey", ephemeralPubkey(t), "--data-dir", t.TempDir(), "--timeout", "3s")
	if err == nil || !strings.Contains(err.Error(), "no license installed") {
		t.Fatalf("older unit argv on a Business build: err = %v, want the licensed route's refusal (no license installed)", err)
	}
}

// The scheduled run has the same default: the unit a Business build generates is the
// gated one, with the token read from the EnvironmentFile and never from argv.
func TestBusinessUpgradeTimerIsGatedByDefault(t *testing.T) {
	dir := t.TempDir()
	if _, err := runUpgradeCmdAs(t, "enterprise", "--install-timer", "--timer-dir", dir, "--data-dir", t.TempDir()); err != nil {
		t.Fatalf("--install-timer: %v", err)
	}
	execStart, hasEnvFile := unitLines(t, dir)
	if !strings.Contains(execStart, " --enterprise ") {
		t.Errorf("Business timer unit does not run the licensed route: %s", execStart)
	}
	if !hasEnvFile {
		t.Error("Business timer unit has no EnvironmentFile for the download token")
	}
	if strings.Contains(execStart, "OLIVARES_UPGRADE_TOKEN") || strings.Contains(execStart, "--token") {
		t.Errorf("Business timer unit puts the download token in argv: %s", execStart)
	}
}

// --connect is the licensed route's own switch, so it needs no --enterprise on a Business
// build: the default is applied before the --connect checks, in the command and in the unit.
func TestBusinessConnectNeedsNoEnterpriseFlag(t *testing.T) {
	hermeticUpgradeEnv(t)
	_, err := runUpgradeCmdAs(t, "enterprise", "--connect", "--check",
		"--pubkey", ephemeralPubkey(t), "--data-dir", t.TempDir(), "--timeout", "3s")
	if !errors.Is(err, errConnectNoState) {
		t.Errorf("Business `upgrade --connect`: err = %v, want the missing connected-client state refusal", err)
	}

	_, err = runUpgradeCmdAs(t, "enterprise", "--connect", "--token", "x", "--data-dir", t.TempDir())
	if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "do not pass --token") {
		t.Errorf("Business `upgrade --connect --token`: err = %v, want the usage refusal of the connected route", err)
	}

	dir := t.TempDir()
	if _, err := runUpgradeCmdAs(t, "enterprise", "--install-timer", "--connect", "--timer-dir", dir, "--data-dir", t.TempDir()); err != nil {
		t.Fatalf("--install-timer --connect: %v", err)
	}
	execStart, hasEnvFile := unitLines(t, dir)
	if !strings.Contains(execStart, " --enterprise ") || !strings.Contains(execStart, " --connect") {
		t.Errorf("connected Business unit: %s", execStart)
	}
	if hasEnvFile {
		t.Error("a connected unit carries no token and needs no EnvironmentFile")
	}
}

// --endpoint without --enterprise names a public or static channel. The licensed route
// would send the download token to it, so a Business build refuses and names the way out.
func TestBusinessUpgradeRefusesAnExplicitEndpointWithoutEnterprise(t *testing.T) {
	_, err := runUpgradeCmdAs(t, "enterprise", "--check", "--pubkey", ephemeralPubkey(t),
		"--endpoint", "https://github.com/olivaresai/olivares", "--data-dir", t.TempDir())
	if err == nil {
		t.Fatal("Business `upgrade --endpoint <public repository>` was accepted")
	}
	if got := exitcode.From(err); got != exitcode.Usage {
		t.Errorf("exit code = %d, want %d (usage)", got, exitcode.Usage)
	}
	for _, want := range []string{"--enterprise", "--bundle"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %s as the next step", err, want)
		}
	}
}

// The default is a property of the Business build only. Community, and --bundle on either
// edition, keep the selection they had; an edition this code does not know fails closed.
func TestUpgradeEditionDefaultLeavesCommunityAndBundlesAlone(t *testing.T) {
	cases := []struct {
		name         string
		edition      string
		opts         upgradeOptions
		wantGated    bool
		wantRefuse   bool
		wantEndpoint string
	}{
		{name: "community with no flags", edition: "community"},
		{name: "community with a mirror", edition: "community",
			opts: upgradeOptions{endpoint: "https://mirror.example/olivares"}, wantEndpoint: "https://mirror.example/olivares"},
		{name: "business with no flags", edition: "enterprise", wantGated: true},
		{name: "business with --enterprise", edition: "enterprise", opts: upgradeOptions{enterprise: true}, wantGated: true},
		{name: "business with a licensed mirror", edition: "enterprise",
			opts:      upgradeOptions{enterprise: true, endpoint: "https://licenses.example"},
			wantGated: true, wantEndpoint: "https://licenses.example"},
		{name: "business with --bundle", edition: "enterprise", opts: upgradeOptions{bundle: "f.tar.gz"}},
		{name: "business with a public endpoint", edition: "enterprise",
			opts: upgradeOptions{endpoint: "https://github.com/olivaresai/olivares"}, wantRefuse: true},
		{name: "business with a blank endpoint", edition: "enterprise",
			opts: upgradeOptions{endpoint: "  \t"}, wantGated: true},
		{name: "an edition name this build does not know", edition: "business-next", wantGated: true},
		{name: "an empty edition name", edition: "", wantGated: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asEdition(t, tc.edition)
			o := tc.opts
			err := defaultToLicensedChannel(&o)
			if (err != nil) != tc.wantRefuse {
				t.Fatalf("err = %v, want refusal = %v", err, tc.wantRefuse)
			}
			if err != nil {
				if got := exitcode.From(err); got != exitcode.Usage {
					t.Errorf("exit code = %d, want %d (usage)", got, exitcode.Usage)
				}
				return
			}
			if o.enterprise != tc.wantGated {
				t.Errorf("enterprise = %v, want %v", o.enterprise, tc.wantGated)
			}
			if o.endpoint != tc.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", o.endpoint, tc.wantEndpoint)
			}
		})
	}
}

// A Community timer is unchanged: no licensed route, no EnvironmentFile.
func TestCommunityUpgradeTimerStaysOnThePublicChannel(t *testing.T) {
	dir := t.TempDir()
	if _, err := runUpgradeCmdAs(t, "community", "--install-timer", "--timer-dir", dir, "--data-dir", t.TempDir()); err != nil {
		t.Fatalf("--install-timer: %v", err)
	}
	execStart, hasEnvFile := unitLines(t, dir)
	if strings.Contains(execStart, "--enterprise") || hasEnvFile {
		t.Errorf("Community timer unit picked up the licensed route: %s (EnvironmentFile=%v)", execStart, hasEnvFile)
	}
}
